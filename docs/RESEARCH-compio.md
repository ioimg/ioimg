# Riset: `compio` / `cyper-axum` — verifikasi sebelum implementasi

- **Tanggal:** 2025-11
- **Pertanyaan:** apakah `axum` di atas io_uring (`cyper-axum`) layak jadi jalur produksi ioimg, dan di mana lubangnya?
- **Metode:** dokumentasi resmi (31 halaman), source code `compio-rs/compio` & `compio-rs/cyper` (raw GitHub), crate metadata, issue tracker (open + closed), docs vendor CDN.
- **Status:** riset selesai; **belum ada kode**. Dokumen ini adalah gerbang sebelum M0.

---

## 1. Kesimpulan

Jalur `compio` **nyata dan bisa dipakai**, tetapi belum layak jadi default. Tiga alasan, berurutan menurut berat:

1. **Regresi keep-alive cyper#37 tidak tereproduksi** (diukur: 36.749 RPS keep-alive vs 50.150 `tokio current_thread`), tetapi statusnya hanya "lulus sementara" — harus diukur ulang setiap upgrade karena atribusi versi perbaikannya tidak akurat.
2. **Beberapa jaminan produksi tidak ada di adaptornya**: tidak ada timer hyper yang dipasang (timeout header/keep-alive), tidak ada batas koneksi, dan tidak ada registered buffers/splice.
3. **Kegagalan mode-nya baru**: `SendWrapper` (panic runtime, bukan compile error), `drop` ≠ cancel (buffer leak), driver polling (file I/O jadi thread pool yang bisa menolak operasi).

Keputusan ADR-0007 (**dua profil runtime, tokio default**) tetap. Yang berubah: daftar gate-nya sekarang **konkret dan bisa diuji**, bukan daftar harapan.

---

## 2. Fakta terverifikasi

### 2.1 Anatomi `cyper-axum` (source: `cyper-axum/src/lib.rs`, 634 baris)

| Fakta | Bukti | Implikasi |
| --- | --- | --- |
| Hanya membungkus `hyper_util::server::conn::auto::Builder` | `let mut builder = Builder::new(CompioExecutor);` (L311, L442) | `axum::Router` & `tower` tetap dipakai; tidak ada parser HTTP baru |
| Satu task per koneksi, di-`detach` | `compio::runtime::spawn(async move { ... }).detach()` (L309) | Tidak ada handle untuk membatalkan koneksi; drain saat shutdown bergantung pada `watch` |
| **Tidak memasang hyper timer** | tidak ada `.timer(...)` di L311/L442, padahal `CompioTimer` tersedia di `cyper-core/src/executor.rs` | Timeout header-read / keep-alive hyper **tidak aktif** → risiko slowloris |
| Graceful shutdown lewat `tokio::sync::watch` | L395–L405, L437–L465 | Butuh `tokio` sebagai dependency walau runtime compio |
| `http2` feature: `builder.http2().enable_connect_protocol()` | L313–L314 | HTTP/2 ada, tapi tanpa timer (lihat di atas) |
| Service & future dibungkus `SendWrapper` | L578–L592, `cyper-core/src/stream.rs` | Nilai non-`Send` menyeberang thread → **panic**, bukan error kompilasi |

### 2.2 `cyper-core`

- `CompioExecutor` mengimplementasi `hyper::rt::Executor` → `compio::runtime::spawn(fut).detach()`.
- `CompioTimer` mengimplementasi `hyper::rt::Timer` via `compio::time::sleep` — **ada**, tetapi tidak dipakai di jalur server (hanya mungkin di client).
- `HyperStream` membungkus `MaybeTlsStream` dengan `SendWrapper`; TLS via `compio-tls` yang memakai `SyncStream` (`std::io::Read/Write` adapter) → **selalu menyalin**, tidak bisa zero-copy.

### 2.3 `compio-driver` (kapabilitas nyata)

| Kapabilitas | Status | Bukti |
| --- | --- | --- |
| Registered **files** | ✅ (io-uring saja) | `Proactor::register_files`, error `"Fixed-file registration is only supported on io-uring driver"` |
| Registered **personality** | ✅ | `register_personality` |
| Registered **buffers** | ❌ | tidak ada API; issue #28 "Support of registered file descriptors" & #955 "multi buffer pool" masih terbuka |
| `splice` / `sendfile` | ❌ | tidak ada opcode; hanya PR draft #1055 "linked splice batches" untuk `copy` |
| `send_zc` (io_uring `SEND_ZC`) | ✅ | `sys/op/zerocopy/iour.rs`: `SendZc`, `SendToZc`, `SendVectoredZc`, `SendMsgZc` |
| `statx` | ✅ | `sys/op/fs/iour.rs` memakai `pal::statx` |
| `O_DIRECT` | ⚠️ lewat `OpenOptions::custom_flags(i32)` | `compio-fs/src/open_options/unix.rs`; compio **tidak** menyediakan alokasi aligned |
| Buffer alignment helper | ❌ | `compio-io/src/buffer.rs` tidak menyebut alignment; pool internal tanpa alignment |
| Deteksi driver efektif | ✅ | `DriverType::{Poll,IoUring,IOCP}`, `.is_iouring()`, `Proactor::driver_type` |
| Pemilihan driver | ✅ runtime (fusion) | `DriverType::suggest()` memilih `IoUring` hanya bila **semua** opcode yang dibutuhkan didukung |

### 2.4 Kegagalan mode yang terdokumentasi

- **AsyncifyPool bounded**: `DispatchError` → *"all threads are busy"*. Di driver `polling`, operasi file masuk pool ini.
- **compio#1091 (open)**: *"if an Asyncfy task is polled by the runtime, and the thread pool is full, it simply spins waiting for a free thread."* → runtime thread **busy-spin**, bukan queue.
- **compio#996 (open)**: *"Feature request: OOM handling"* → perilaku OOM tidak didefinisikan.
- **compio#1107 (open)**: alokasi 32 KiB berulang di driver polling (macOS).

### 2.5 Issue yang langsung menyangkut ioimg

| Issue | Isi | Status |
| --- | --- | --- |
| **cyper#37** | Keep-alive membuat RPS **turun 100×** (9.790 → 98 RPS, 64 KiB, `ab -k`); axum+tokio justru naik 3× (16.515 → 51.462). Dugaan: error di `accept` memicu tunggu 1 detik. | **Open**, tetapi **tidak tereproduksi** pada pengukuran sendiri (§8). PR #40 tidak di-merge; perbaikan tampaknya datang dari #70/#75 |
| **cyper#72** | `axum::extract::Sse::keep_alive()` **panic**: *"there is no reactor running, must be called from the context of a Tokio 1.x runtime"* karena memakai `tokio::time::Sleep` | **Open** |
| compio#28/#955 | Registered fds (parsial) / multi buffer pool | Open |

`cyper#72` bukan soal SSE. Itu bukti pola: **setiap fitur `axum`/`tower` yang menyentuh timer atau executor tokio akan panic saat runtime**, dan compiler tidak membantu.

---

## 3. Koreksi terhadap dokumen kita sendiri

Riset ini membatalkan tiga klaim yang sudah masuk ke dokumen. Semua sudah diperbaiki, tapi dicatat di sini supaya tidak muncul lagi:

| Klaim lama | Kenyataan | Dampak |
| --- | --- | --- |
| "compio mendukung registered buffers; kita akan pin buffer 256 KB" | **Tidak ada** API registered buffers di compio; hanya registered *files* | Desain `fixed buffer ring` hanya mungkin di backend `io_uring` mentah (profil tokio), bukan di profil compio |
| "Body dikirim via `splice`/zero-copy dari L2 ke socket" | **Tidak ada `splice`**; yang ada `send_zc` (butuh handling notifikasi CQE & lifetime buffer) | Klaim zero-copy harus dicabut. Yang realistis: `write` dari buffer + `send_zc` sebagai optimasi lanjutan |
| "Cloudflare mengabaikan `Vary` selain `Accept-Encoding`" | **Sudah berubah**: Cloudflare kini mendukung `Vary` (semua plan) lewat Cache Rules, dengan aksi per-header `normalize`/`passthrough`/`bypass`; ada juga "Vary for images" | Rekomendasi "selalu pakai format eksplisit" jadi *preferensi kehati-hatian*, bukan keharusan mutlak. CloudFront **belum terverifikasi** |

Catatan penting untuk Cloudflare: mode `normalize` pada `Accept` **menulis ulang** header ke origin dan **membuang media type yang tidak ada di allowlist** (`media_types`). Kalau `image/avif` tidak didaftarkan, ioimg tidak akan pernah melihat AVIF → semua user dapat WebP. Ini footgun konfigurasi yang harus masuk dokumen CDN kita.

---

## 4. Risk register produksi

Severity: S1 = bisa membunuh produk, S2 = mahal tapi terdeteksi, S3 = annoyance.

| ID | Risiko | Sev | Deteksi | Mitigasi |
| --- | --- | --- | --- | --- |
| R1 | Regresi keep-alive ala cyper#37 | S3 | Benchmark 64 KiB dengan & tanpa `-k` setiap upgrade | **Terukur tidak tereproduksi** di 0.9.0; tetap diuji tiap bump versi |
| R2 | Tanpa hyper timer → tidak ada timeout header/idle → slowloris & koneksi menggantung | **S1** | Uji: buka 1.000 koneksi, kirim 0 byte, ukur FD & memori 60 s | Timeout di LB/proxy depan **wajib**; tambah layer tower berbasis `compio::time`; jangan klaim aman tanpa keduanya |
| R3 | `drop` ≠ cancel → buffer tidak kembali (leak) | **S1** | Soak 72 jam, timeout & disconnect acak, RSS harus flat | Cancel eksplisit + reclaim; batas ukuran antrean; alert RSS |
| R4 | `SendWrapper` panic saat nilai menyeberang thread | S2 | Panic hook + soak; audit layer | Larang `spawn_blocking` pada tipe request/response; `clippy::disallowed_methods` untuk API tokio |
| R5 | Driver `polling`: file I/O = thread pool terbatas; pool penuh → operasi **gagal**; runtime **spin** (compio#1091) | **S1** di container | `probe-uring` + metric `driver`, uji beban di seccomp ketat | compio hanya dipakai bila `driver=io_uring`; kalau polling → tolak start (fail fast) atau turun ke profil tokio |
| R6 | Tanpa registered buffers → ceiling performa lebih rendah dari desain awal | S2 | Benchmark syscall/request & CPU | Turunkan target p99; atau backend `io_uring` mentah hanya di profil tokio |
| R7 | Tanpa `splice` → biaya salin untuk body besar | S2 | W2 (1 MB): CPU/s & memcpy di flamegraph | `send_zc` fase lanjut; atau serahkan egress ke CDN |
| R8 | Perilaku OOM tidak didefinisikan (compio#996) | S2 | Uji dengan `MemoryMax`/cgroup limit | Batasi MP input; pool terpisah; restart policy |
| R9 | Fitur axum/tower bertimer panic (contoh: SSE keep-alive) | S2 | CI: smoke test tiap fitur yang dipakai | `clippy::disallowed_methods` untuk `tokio::time::*`, `tokio::spawn`; daftar API terlarang di CONTRIBUTING |
| R10 | Tanpa batas koneksi → FD exhaustion | S2 | Uji 100k koneksi | Limit di LB + `SO_REUSEPORT`/semaphore di accept loop |
| R11 | Churn pre-1.0 (compio tanpa MSRV policy) | S3 | `cargo update` PR | Pin minor, upgrade terjadwal, kontrak test |
| R12 | TLS selalu menyalin (SyncStream) | S3 | W1 dengan TLS | Terminasi TLS di depan untuk jalur panas |

---

## 5. Unknown yang harus dijawab sebelum kode (bukan setelah)

Setiap item punya eksperimen kecil (≤ 2 hari) dan **kill criteria**. Kalau satu S1 gagal, kita berhenti dan tetap di `tokio + IoBackend::Uring`.

| # | Pertanyaan | Eksperimen | Lulus bila |
| --- | --- | --- | --- |
| U1 | Apakah keep-alive masih 100× lebih lambat? | **Sudah dijalankan** (§8) | Lulus sementara: 73% dari `tokio current_thread` |
| U2 | Apakah idle connection pernah ditutup? | **Sudah dijalankan** (§8) | **Gagal**: tetap terbuka > 8 s → mitigasi wajib |
| U3 | `O_DIRECT` lewat `custom_flags` benar-benar jalan? | Tulis/baca 4096-aligned & non-aligned; cek `EINVAL` | Ada jalur aligned yang berfungsi; kalau tidak, `direct_io=false` |
| U4 | Apakah `AsyncReadManaged` mengurangi syscall/CPU? | Bandingkan vs `read` manual pada W1 | ≥ 10% syscall turun |
| U5 | Driver efektif di container seccomp kita | `probe-uring` + `driver_type`, 4 platform | Terdeteksi jujur; fallback terkontrol |
| U6 | HTTP/2 tanpa timer: berfungsi atau error? | `h2load` + idle ping | Tidak error; kalau error → HTTP/1.1 saja di profil compio |
| U7 | Graceful shutdown benar-benar drain? | Kirim SIGTERM saat 500 request in-flight | 0 request terputus, proses exit < 20 s |
| U8 | Timeout client (`cyper`) tersedia/dapat dikonfigurasi? | Lihat API + uji origin lambat | Ada timeout nyata; kalau tidak, bungkus sendiri |
| U9 | SigV4 lewat `cyper` cocok dengan S3/R2/MinIO? | Contract test 3 backend | 100% lulus, termasuk `Range` & `UNSIGNED-PAYLOAD` |
| U10 | OTel export tanpa tokio di proses | Bridge thread + channel | Trace muncul end-to-end, tidak blocking request |
| U11 | Panic rate di bawah churn | 1 jam traffic acak + kill -9 client | 0 panic |
| U12 | RSS stabil dengan timeout/disconnect | Soak 72 jam | ΔRSS < 5% |

---

## 6. Yang harus ada **sebelum** menulis `main.rs`

1. **`clippy.toml` disallowed-methods** untuk profil compio: `tokio::spawn`, `tokio::time::*`, `tokio::fs::*`, `tokio::sync` yang tidak dipakai internal hyper. Tanpa ini, R4/R9 akan berulang.
2. **`probe-uring` sebagai binary terpisah** yang mencetak: driver efektif, opcode yang didukung, hasil alokasi aligned, versi kernel, status seccomp. Dipakai CI & operator.
3. **Harness benchmark yang sama** untuk tiga konfigurasi, dengan dataset yang di-generate (10k file 64 KiB, 1k file 1 MB, 100 file 5 MP) dan skrip `oha`.
4. **Test kontrak `IoBackend`** yang dijalankan untuk semua backend (tokio, uring, compio) — bukan hanya happy path.
5. **Keputusan seccomp**: profil `uring` disertakan di repo dan diuji di kind; `RuntimeDefault` jadi default chart.

## 7. Rekomendasi urutan kerja

```
M0-a  probe-uring + harness benchmark (2 hari)      → U5, U2
M0-b  spike compio 64 KiB keep-alive (3 hari)       → U1  ← gerbang utama
M0-c  cache I/O compio: O_DIRECT, AsyncReadManaged  → U3, U4
M0-d  soak + timeout/disconnect (2 hari)            → U11, U12
M0-e  storage SigV4 via cyper (3 hari)              → U9
      ↓
   keputusan ADR-0007: promosi / tunda / batalkan
```

Kalau U1 gagal, M0-c..e **dibatalkan** dan ioimg tetap `tokio + IoBackend::Uring`. Kerugiannya kecil karena semuanya masih di balik trait.

---

## 8. Hasil eksperimen yang dijalankan sendiri

Klaim "looks like this issue is fixed in cyper 0.5.0" tidak cukup — jadi diukur. Setup:

```
kernel 7.2.15-zen, 16 core, loopback, tanpa TLS, tanpa transformasi gambar
axum 0.8.9 | tokio 1.54.1 | hyper 1.12.0 | cyper-axum 0.9.0 | compio 0.19.2
payload 64 KiB, keep-alive vs koneksi baru, 4 s per run, load generator Rust (std, tanpa dep)
```

Verifikasi driver: proses server memiliki `anon_inode:[io_uring]`, dan probe terpisah mencetak `io_uring_setup: OK` + `compio driver_type = IoUring`. **Bukan** fallback polling.

| Server | keep-alive 4 conn | tanpa keep-alive 4 | keep-alive 32 | tanpa keep-alive 32 |
| --- | --- | --- | --- | --- |
| axum + tokio (multi-thread, 16 core) | 75.965 | 14.194 | 122.184 | 14.520 |
| axum + tokio (`current_thread`) | **50.150** | 14.134 | **50.767** | 14.238 |
| **cyper-axum + compio/io_uring** | **36.749** | 13.972 | **45.470** | 13.162 |

### Verdict

| # | Pertanyaan | Hasil |
| --- | --- | --- |
| U1 | Keep-alive masih 100× lebih lambat? | **Tidak.** Regresi cyper#37 tidak tereproduksi. Keep-alive justru 2,6× lebih cepat dari koneksi baru (36,7k vs 14,0k). Single-thread compio ≈ 73–90% `tokio current_thread` |
| U2 | Ada timeout koneksi idle? | **Tidak ada.** Koneksi tanpa byte apa pun tetap terbuka > 8 s; begitu juga koneksi dengan request parsial (`GET / HTTP/1.1\r\n` lalu diam) |
| — | Berapa core dipakai default? | **1.** `#[compio::main]` + `cyper_axum::serve` = satu runtime single-thread. 15 dari 16 core menganggur |

### Catatan penting soal atribusi "fixed in 0.5.0"

Riwayat commit `cyper-axum/src/lib.rs` tidak menunjukkan perbaikan keep-alive: guard `is_connection_error` sudah ada sejak commit "add Listener trait" (2025-06-22), **sebelum** issue dilaporkan (2025-07-26). Perubahan yang paling mungkin bertanggung jawab:

- `#70 perf(cyper-axum): avoid buffered TCP compatibility I/O` (2026-08-11) — menghapus `SyncStream`/compat I/O berbuffer di jalur socket.
- `#75 feat: update compio and use pollfd` (2026-10-01).

Artinya: klaim publik "fixed in 0.5.0" kemungkinan **salah versi**, meski kesimpulan akhirnya benar untuk 0.9.0. Praktisnya: **jangan bergantung pada nomor versi; bergantung pada benchmark yang kita jalankan sendiri setiap upgrade.**

### Batasan eksperimen ini (jangan di-overclaim)

- Loopback tanpa TLS → tidak mengukur biaya `compio-tls`/`SyncStream`.
- Tanpa disk cache → **tidak mengukur** keunggulan io_uring yang sesungguhnya (baca/tulis file, `statx`, `O_DIRECT`). Ini justru jalur utama ioimg.
- Tanpa `Dispatcher`/`SO_REUSEPORT` → semua angka compio = 1 core.
- Tanpa transformasi gambar (CPU-bound) → tidak mewakili W4/W5.
- Satu mesin, satu kali jalan; median 3 run belum dikumpulkan.

Karena itu U1 dinyatakan **lulus sementara**, bukan lulus permanen. Angka di atas menjadi baseline yang harus dipertahankan setiap upgrade compio/cyper.

## 9. Hasil PoC (kode nyata, bukan spike)

PoC lengkap ada di [`../poc/`](../poc/README.md): dua server (tokio, compio), tiga backend (`std::fs`, `tokio::fs`, io_uring), router + transform + cache + metrics, plus `probe-uring` dan load generator.

Temuan yang mengubah prioritas:

| Temuan | Angka | Konsekuensi |
| --- | --- | --- |
| Ring naif (1 `submit_and_wait` per op) **tidak lebih cepat** dari `std::fs` | read 64 MiB: 32,0 vs 32,5 ms (seri); RPS HIT 94k vs 118k | `IoBackend::Uring` wajib batching + multi-ring + `statx`/`openat` di ring, atau tidak dipakai |
| Transform CPU mendominasi | 52,9 ms/gambar → 19 img/s/core (`image` crate) vs **9,6–14,5 ms → 69–104 img/s/core (libvips)** | Bottleneck produk = pustaka codec, bukan io_uring. libvips menang 3,6–5,5× di Rust maupun Go |
| compio default 1 core | 44,9k RPS vs 103k tokio (16 core) | Perbandingan adil butuh `SO_REUSEPORT`/`Dispatcher` |
| `O_DIRECT` bergantung filesystem | ext4: unaligned = `EINVAL`; tmpfs: **diabaikan diam-diam** | `probe-uring` wajib dijalankan pada direktori cache sebenarnya |
| `O_DIRECT` + buffer aligned berfungsi | baca 64 MiB OK | Jalur `AlignedBuf` di ARCHITECTURE §7.2 valid |

Kesimpulan: `tokio` tetap default; `compio` tetap eksperimen; **investasi pertama adalah pipeline transform**, bukan io_uring.

## 10. Sumber

- `compio.rs/docs/*` (31 halaman; buffers, io, driver/{io-uring,polling,iocp}, runtime, dispatcher, fs/*, net/*, signal, process, tls, ws, cyper/{core,client,axum}) — dibaca via browser karena SPA.
- `github.com/compio-rs/compio`: `compio-driver/src/{lib.rs,driver_type.rs,asyncify.rs,cancel.rs,buffer_pool.rs,sys/op/**}`, `compio-fs/src/open_options/unix.rs`, `compio-io/src/buffer.rs`.
- `github.com/compio-rs/cyper`: `cyper-axum/src/lib.rs`, `cyper-core/src/{executor.rs,stream.rs}`, `cyper-axum/benches/comparison.rs`.
- Issue: cyper#37, cyper#72; compio#28, #955, #996, #1091, #1107; PR cyper#40, #70, #71.
- `developers.cloudflare.com/cache/concepts/vary/` — dukungan `Vary` + aksi `normalize`/`passthrough`/`bypass`.
