# ADR-0007 — `axum` di atas `compio` (`cyper-axum`) sebagai fast path

- **Status:** Proposed (diputuskan final setelah benchmark M0)
- **Tanggal:** 2025-11
- **Riset pendukung:** [`../RESEARCH-compio.md`](../RESEARCH-compio.md) — 31 halaman docs, source, issue tracker
- **Merevisi:** [ADR-0002](0002-io-uring-data-plane.md) (premis "axum tidak bisa di atas io_uring" tidak lagi benar)
- **Konteks:** pertanyaan "bagaimana dengan <https://compio.rs/docs/cyper/axum>?"

## Konteks

> **Sumber terverifikasi (dibaca 2025-11, via browser karena docs adalah SPA):**
> - `compio.rs/docs/cyper/axum` — *"cyper-axum is a simple adaptor. It provides an alternative serve method, which uses compio types. This is the only method you need to be aware of, and you can still use other powerful tools in the axum environment."*
> - `compio.rs/docs/cyper/core` — *"hyper is designed for tokio, though it allows switching to another runtime. It requires all types and tasks to be Send. We use `SendWrapper` here to bypass the requirements."*
> - `compio.rs/docs/compio/driver/polling` — *"When the user enables both `io-uring` and `polling`, a 'fusion' driver is used and responsible for deciding which one to use **at runtime**. You are able to specify the exact driver to use with the `ProactorBuilder`."*
> - `compio.rs/docs/compio/driver/io-uring` — *"If the kernel doesn't support these feature-gated capabilities, the io-uring driver will fail to create. If `polling` feature is also enabled, it will fallback to the polling driver."* + *"if the kernel doesn't support the opcode being submitted, that operation will fall back to a blocking system call, while other io-uring operations continue as normal."*
> - `compio.rs/docs/compio/runtime` — *"compio-runtime provides a single-threaded async runtime."*
> - `compio.rs/docs/cyper/client` — client ala `reqwest`: TLS (rustls-platform-verifier), HTTP/2, HTTP/3 (compio-quic), cookies, charset, serde, Alt-Svc upgrade.

ADR-0002 menyimpulkan stack HTTP tidak bisa memakai io_uring tanpa meninggalkan `axum`, dengan alasan: `tokio` memakai epoll, `tokio-uring` bukan drop-in, dan runtime io_uring-native (`monoio`, `compio`) tidak punya HTTP stack `hyper`.

Alasan ketiga ternyata **sudah tidak berlaku**. Ada ekosistem `compio` + `cyper` yang justru menyediakan `hyper` di atas `compio`:

| Crate | Isi | Versi | Bukti |
| --- | --- | --- | --- |
| `compio` | Runtime thread-per-core, driver io_uring / IOCP / polling | 0.19.2 stabil (0.20.0-beta.1) | 1,19 jt downloads; default features `runtime` + `io-uring`; ada feature `polling` (fallback) dan `compat-tokio` |
| `cyper` | HTTP client ala `reqwest` di atas compio + hyper | 0.9.0 | TLS (rustls/native-tls), HTTP/2, HTTP/3, redirect, decompression, proxy, hickory DNS |
| `cyper-axum` | Adaptor: `serve()` sebagai pengganti langsung `axum::serve` | 0.9.0 (0.10.0-beta.1) | 138 rb downloads; MIT; axum 0.8.9; internal: `hyper_util::server::conn::auto::Builder` + `TowerToHyperService` + `compio::net::TcpListener` |

Artinya: **`axum` + `hyper` + `io_uring` memang mungkin hari ini**, lewat penggantian runtime + listener, bukan penulisan ulang HTTP stack. `cyper-axum::serve(listener, router)` menerima `axum::Router` yang sama — jadi `Router`, extractor, `tower::Service`, dan handler `axum` tetap dipakai.

Konsekuensi arsitekturalnya bukan "gratis", melainkan **perubahan model eksekusi**:

1. **Thread-per-core, task `!Send`.** `compio-axum` memakai `Rc`/`SendWrapper`. `tokio::spawn` tidak ada; penggantinya `compio::runtime::spawn` yang terikat ke core saat ini.
2. **`tokio::*` hilang dari handler.** `tokio::fs`, `tokio::time::sleep`, `tokio::sync` (sebagian), `reqwest`, `aws-sdk-*`, `sqlx`, `redis` tidak bisa dipakai langsung.
3. **Tower middleware bertimer tidak jalan.** `tower::timeout`, `tower_http::timeout::TimeoutLayer`, `tower::limit` yang memakai timer tokio akan menggantung/panic. Butuh padanan berbasis `compio::time`.
4. **Pre-1.0.** compio menyatakan diri belum punya MSRV policy dan bisa memakai fitur Rust terbaru kapan saja; `cyper-axum` masih 0.x dengan beta di jalur.
5. **Fallback seccomp (sekarang terverifikasi).** compio punya driver `polling` dan, bila kedua feature diaktifkan, sebuah **"fusion" driver** memilih di runtime; `ProactorBuilder` bisa memaksa driver tertentu. Bila io_uring gagal dibuat (kernel tidak mendukung capability yang di-feature-gate) → fallback ke polling. Bahkan per-operasi: opcode yang tidak didukung kernel jatuh ke syscall blocking sementara op lain tetap io_uring.
6. **`Send` ditegakkan saat runtime, bukan saat kompilasi.** `cyper-core` memakai `SendWrapper` untuk menembus requirement `Send` milik `hyper`. Artinya kompilasi bisa lolos walau ada tipe non-`Send` yang menyeberang thread — dan hasilnya **panic saat runtime**, bukan error compile. Ini footgun nyata untuk layer `tower` yang memakai `spawn_blocking`.

## Temuan tambahan setelah membaca seluruh dokumentasi (31 halaman)

Selain 6 butir di atas, ada enam hal yang **mengubah desain ioimg**, bukan sekadar catatan:

| # | Temuan (kutipan dokumentasi) | Dampak pada ioimg |
| --- | --- | --- |
| 1 | `compio::driver::Proactor::driver_type` tersedia untuk mengambil driver **efektif** | `probe-uring` tidak perlu menebak: laporkan `driver_type`, bukan cuma "io_uring tersedia" |
| 2 | *"the polling driver doesn't support async file IO. Therefore, the file operations in polling driver are actually spawned into a thread pool … There is a limitation on the number of threads … If the number of threads exceeds the limitation, the operation may fail immediately."* | Di container dengan seccomp (→ polling), **L2 cache berubah dari io_uring menjadi thread-pool blocking dengan batas keras**. Ini bukan sekadar "lebih lambat": ia bisa **gagal**. → gate: compio hanya dipakai bila io_uring benar-benar aktif |
| 3 | Buffer dimiliki, bukan dipinjam: `IoBuf`/`IoBufMut`, `BufResult<T, B>`, `buf_try!` | Signature `IoBackend::read_at` harus berbentuk completion (`BufResult`), bukan `&mut BytesMut` (sudah diperbaiki di ARCHITECTURE §6.1) |
| 4 | *"the read or write operations could not be cancelled by simply dropping them … Were the future dropped, the buffer would never come back."* | Timeout fetch 5 s & transformasi 2 s dan disconnect client tidak boleh sekadar `drop` future I/O → butuh cancel + reclaim buffer. Ini **kebocoran memori lambat** bila salah |
| 5 | `compio::dispatcher::Dispatcher` mengelola banyak runtime lintas thread dan berbagi satu thread pool | Multi-worker di profil compio memakai Dispatcher (atau `SO_REUSEPORT`), bukan `tokio::spawn` per koneksi |
| 6 | `compio-tls` memakai `SyncStream` (adaptor `std::io::Read/Write`); native-tls tanpa QUIC | Jalur TLS **selalu menyalin** dan tidak bisa `splice` → konfirmasi bahwa zero-copy hanya berlaku bila TLS diterminasi di depan |

Tambahan penting untuk ekspektasi: penulis compio sendiri menyebut proyek ini *"a good **experimental** project"* dan mimpi menjadikannya alternatif tokio. Itu bukan alasan menolak, tapi alasan menahan diri dari menjadikannya default.

## Keputusan

**Jangan ganti stack sekarang. Jadikan runtime sebagai sumbu konfigurasi (build feature), dan uji `cyper-axum` di M0/M3 sebagai kandidat fast path.**

### 1. Abstraksi runtime (`ioimg-runtime`)

```rust
pub trait Rt: 'static + Clone {
    fn spawn<F: Future<Output = ()> + 'static>(f: F);
    fn spawn_blocking<F, T>(f: F) -> JoinHandle<T>;   // CPU pool (decode/encode)
    fn sleep(d: Duration) -> impl Future<Output = ()>;
    fn now() -> Instant;
}
```

- Dua implementasi: `TokioRt` (default) dan `CompioRt`.
- Handler di `ioimg-http` ditulis generik/di belakang trait ini — **tanpa** `tokio::spawn`/`tokio::time` langsung.
- Yang **tidak** diabstraksi: `tokio::sync` primitives yang sudah dipakai `hyper`/`axum` internal (aman, karena `cyper-axum` sendiri memakai `tokio::sync::watch`).

### 2. Dua profil build

| Profil | Runtime | Data plane I/O | Status |
| --- | --- | --- | --- |
| `runtime-tokio` (**default, produksi**) | tokio/epoll | `IoBackend::Tokio` atau `::Uring` (ADR-0002) | Stabil, semua integrasi infra jalan |
| `runtime-compio` (**eksperimen**) | compio (io_uring, fusion driver → polling bila perlu) | `IoBackend::Compio` (file via compio-driver) | Kandidat fast path; fitur integrasi dibatasi |

Keduanya berbagi `ioimg-core`, `ioimg-ops`, `ioimg-transform`, `ioimg-codec` — bagian yang paling mahal untuk ditulis ulang.

### 3. Strategi integrasi di jalur compio

Masalah terbesar bukan HTTP, tapi **SDK**: `aws-sdk-s3`, `google-cloud-storage`, `azure_storage` semuanya butuh tokio. Ada dua jalan, keduanya sah:

- **(a) Bridge**: jalankan SDK di thread pemilik `tokio` runtime kecil, komunikasi via channel + oneshot. Sederhana, biaya ~1 hop per fetch (di luar hot path cache-hit, jadi dapat diterima).
- **(b) Native**: karena kebutuhan kita sempit — `GET`, `HEAD`, `presign` (tanpa multipart/list/copy) — implementasi SigV4 + `cyper` client adalah ±500 baris. Ini yang direkomendasikan untuk fast path, dengan contract test melawan MinIO/R2.

Untuk CDN purge, admin API, billing, dan OTel export: **tetap di proses terpisah** (control plane) atau bridge, karena semuanya bukan hot path.

### 4. Gate promosi (kapan compio jadi default)

`runtime-compio` hanya dipromosikan bila **semua** terpenuhi:

- [x] **Keep-alive tidak regresi** (cyper#37) — **diukur**: 36.749 RPS keep-alive vs 50.150 `tokio current_thread` (73%), 13.972 tanpa keep-alive. Regresi 100× **tidak tereproduksi** di cyper-axum 0.9.0 / compio 0.19.2. Wajib diukur ulang setiap upgrade (atribusi "fixed in 0.5.0" tidak akurat — `RESEARCH-compio.md §8`)
- [x] **Driver io_uring aktif** — `anon_inode:[io_uring]` + `Proactor::driver_type = IoUring`
- [ ] **Timeout koneksi wajib ditambahkan** — **diverifikasi tidak ada**: koneksi idle dan request parsial tetap terbuka > 8 s. Timeout harus datang dari LB/proxy depan **dan** layer sendiri berbasis `compio::time`
- [ ] **Multi-core** — `#[compio::main]` + `serve` memakai **1 core** (diverifikasi). Wajib `Dispatcher` atau `N` proses + `SO_REUSEPORT`, lalu ukur ulang throughput
- [ ] **Timeout koneksi ada dan terbukti** (hyper di `cyper-axum` tidak memasang timer): idle connection ditutup, atau mitigasi LB terdokumentasi + diuji

- [ ] `compio` ≥ 1.0 (atau soak produksi ≥ 6 bulan dengan 0 insiden runtime)
- [ ] Benchmark M0: pada W1 (64 KB hit), W2 (1 MB hit), W3 (random), `compio` mengungguli `tokio + IoBackend::Uring` **≥ 15% p99** atau **≥ 20% CPU**, dengan RPS tidak turun
- [ ] `probe-uring` fallback ke driver `polling` terverifikasi **via `Proactor::driver_type`**, dan performanya tidak lebih buruk dari `tokio` (kalau lebih buruk → compio hanya dipakai saat io_uring tersedia)
- [ ] Perilaku L2 pada driver `polling` diukur eksplisit (file I/O = thread pool): throughput, p99, dan frekuensi kegagalan saat pool jenuh
- [ ] Uji kebocoran buffer: 72 jam soak dengan timeout & disconnect acak → RSS **flat** (memvalidasi bahwa `drop` tidak dipakai sebagai cancel)
- [ ] Audit `SendWrapper`: tidak ada nilai non-`Send` (tipe `hyper`/`axum`) yang menyeberang thread. Uji dengan soak + panic hook, karena ini **tidak** tertangkap compiler
- [ ] Semua storage P0 (S3, R2, MinIO, local, HTTP) lulus contract test di jalur compio
- [ ] OTel/metrics tidak blocking; panic/abort rate = 0 dalam 72 jam soak
- [ ] Ada jalur rollback: image `runtime-tokio` tetap dipublikasikan untuk versi yang sama

## Konsekuensi

**Positif**
- Jalur untuk mendapat keuntungan io_uring **penuh** (accept, recv, send, file, splice) tanpa meninggalkan `axum`/`tower`.
- Abstraksi `ioimg-runtime` memaksa disiplin: handler tidak boleh bergantung pada detail runtime. Ini menurunkan biaya migrasi di masa depan (termasuk ke `monoio` bila perlu).
- Jika gate lolos, klaim pemasaran "axum + io_uring" menjadi akurat secara teknis — sebelumnya harus diberi kualifikasi.

**Negatif**
- Dua jalur build = dua matriks CI, dua profil performa, dua set bug.
- **Bug thread-affinity tidak terdeteksi compiler** (`SendWrapper`), jadi kelas bug baru: panic produksi akibat nilai non-`Send` yang menyeberang thread. Mitigasi: larangan `spawn_blocking` pada tipe request/response, dan soak test.
- Ekosistem `compio` lebih kecil; kalau proyek itu berhenti, kita harus mampu kembali ke tokio (karena abstraksi, ini mungkin — itulah alasan abstraksinya ada).
- Menulis handler runtime-agnostik menambah friksi harian untuk kontributor.
- Pre-1.0 berarti breaking changes mungkin; pin versi + review upgrade wajib.

## Alternatif yang ditolak

| Alternatif | Alasan |
| --- | --- |
| Langsung pindah seluruh service ke `compio` sekarang | Kehilangan ekosistem (aws-sdk, sqlx, OTel) dan menjadikan produk bergantung pada pre-1.0 runtime sebelum ada angka. |
| Abaikan `cyper-axum`, tetap ADR-0002 | Premis ADR-0002 (tidak ada axum di io_uring) terbukti salah; menolak tanpa mengukur adalah keputusan berdasarkan asumsi usang. |
| Tulis ulang HTTP stack sendiri di atas `io-uring` crate | Jauh lebih mahal dari memakai `cyper-axum`; risiko parser HTTP jauh lebih besar daripada risiko runtime. |
| `monoio` + HTTP stack-nya | Ekosistem lebih kecil lagi, tanpa adaptor `axum` resmi setara `cyper-axum`. |
| Dua binary terpisah (bukan feature flag) | Duplikasi konfigurasi/ops; feature flag dengan satu binary lebih mudah di-deploy dan di-benchmark A/B. |
