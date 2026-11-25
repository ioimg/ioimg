# Performance & io_uring — ioimg


> **Catatan implementasi (2026-11-25):** stack final adalah **Go** (`service/`),
bukan Rust/axum/io_uring. Lihat [ADR-0008](adr/0008-go-data-plane.md) dan
[service/README.md](../service/README.md). Dokumen ini tetap menjadi sumber
requirements produk; bagian stack/io_uring adalah riwayat desain.

Dokumen ini menjelaskan **kenapa** io_uring dipakai, **di mana** ia benar-benar menang, dan **bagaimana** klaim performa diverifikasi.

---

## 1. Ringkasan jujur

Image proxy punya dua fase dengan profil berbeda:

| Fase | Dominan | Dampak io_uring |
| --- | --- | --- |
| **Cache hit / serving** | syscall (open/stat/read/write/send) + memcpy + TLS | **Signifikan** untuk objek besar & banyak koneksi |
| **Cache miss / transformasi** | CPU: decode, resize, encode (AVIF) | **Kecil**, karena CPU-bound |
| **Fetch origin** | latensi jaringan | **Nol** |

Karena itu:

- Klaim utama: **"p95 cache-hit & egress tercepat di kelas self-host"**, bukan "transformasi 10× lebih cepat".
- io_uring memberi keuntungan ~20–40% syscall reduction pada serving, dan jauh lebih besar pada **batching** banyak operasi kecil (mis. 1 request = open+stat+read+write, atau N request paralel per core).
- Fallback `tokio`/epoll tetap tersedia; sistem tidak boleh bergantung pada io_uring untuk benar.

## 2. Mengapa model io_uring lebih murah

### 2.1 epoll (readiness) vs io_uring (completion)

```
epoll:  read() → EAGAIN → epoll_wait → read() lagi → ...
        3-4 syscall per operasi, plus context switch saat data belum siap

io_uring: SQE ke shared ring (tanpa syscall bila SQPOLL/enter batching)
          → 1 io_uring_enter untuk N operasi
          → CQE berisi hasil (tidak perlu retry)
```

Keuntungan yang relevan untuk ioimg:

1. **Batching** — satu `io_uring_enter` untuk `openat`+`statx`+`read` (linked) atau untuk 128 read dari 128 koneksi (multishot accept/recv).
2. **Registered files & buffers** — `fget`/`fput` dan pin/unpin hilang dari hot path; pada 20k RPS dengan 3 operasi file per request, ini menghilangkan ~60k fd refcount ops/detik.
3. **`O_DIRECT` tanpa penalty** — page cache bisa dilewati; pada file cache yang tidak akan dibaca ulang oleh kernel (kita punya LRU sendiri), ini menghindari double caching (RAM terbuang) dan mempermudah prediksi latensi.
4. **`splice`/zero-copy** — body cache bisa dipindah dari page cache/file ke socket tanpa menyalin ke user-space.
5. **Tanpa thread pool file** — `tokio::fs` memakai `spawn_blocking` (thread per operasi blocking). Pada 8 GB/s egress, itu berarti ratusan thread blokir. io_uring tetap di satu thread.

### 2.2 Angka rujukan (indikatif, bukan janji)

| Beban | epoll + tokio::fs | io_uring + O_DIRECT | Delta |
| --- | --- | --- | --- |
| 20k RPS, 64 KB, cache hit | baseline | −25% p99, −35% CPU sys | — |
| 1 file 1 MB, 1000 koneksi paralel | baseline | −40% p50 | — |
| 1000 file kecil per detik (metadata heavy) | baseline | −60% syscalls | — |
| Transform 3000px → WebP | baseline | ~0% | CPU-bound |

Angka di atas **wajib diganti hasil benchmark M0** terhadap mesin uji yang didokumentasikan. Jangan memakai angka ini di materi pemasaran sebelum terverifikasi.

## 3. Dua profil runtime: `tokio` (default) vs `compio` (fast path)

Fakta teknis yang membentuk desain:

- `axum` tidak melakukan I/O sendiri; ia berjalan di atas `hyper`. `hyper` butuh `AsyncRead + AsyncWrite` + timer — **bukan** tokio secara spesifik. `tokio` memakai epoll/kqueue/IOCP.
- `tokio-uring` adalah proyek **terpisah** dengan API sendiri, bukan `tokio` yang "diaktifkan io_uring", dan tidak drop-in untuk `hyper`.
- **`compio` + `cyper` + `cyper-axum` menyediakan `hyper` di atas driver io_uring.** `cyper_axum::serve(listener, router)` adalah pengganti langsung `axum::serve` untuk `axum::Router` yang sama (`hyper_util` auto Builder + `TowerToHyperService`). Jadi "axum + io_uring" **mungkin** hari ini — bukan hipotesis.
- biayanya bukan di HTTP, tetapi di **model eksekusi & ekosistem**: runtime single-threaded per thread, task `!Send`, tanpa `tokio::spawn`/`tokio::time`, dan tanpa `aws-sdk-*`/`sqlx`/timer `tower`;
- **`Send` ditegakkan runtime, bukan compiler**: `cyper-core` memakai `SendWrapper` untuk menembus requirement `Send` milik `hyper` → satu tipe non-`Send` yang menyeberang thread = panic produksi;
- **fallback io_uring sudah ada dan terverifikasi**: dengan feature `io-uring` + `polling`, compio memakai **fusion driver** yang memilih di runtime (`ProactorBuilder` untuk memaksa); kalau io_uring gagal dibuat → polling; opcode yang tak didukung → syscall blocking per-operasi;
- `io_uring` tetap diblokir seccomp default Docker/Kubernetes (`io_uring_enter`) → di lingkungan itu fusion driver berakhir di polling (epoll dengan API compio), belum tentu lebih baik dari `tokio`.

**Keputusan (ADR-0002 + ADR-0007):** runtime menjadi sumbu konfigurasi build.

| Profil | HTTP | File/cache I/O | Status |
| --- | --- | --- | --- |
| `runtime-tokio` | `axum::serve` | `IoBackend::Tokio` \| `::Uring` | Default produksi |
| `runtime-compio` | `cyper_axum::serve` | `IoBackend::Compio` | Eksperimen, gate benchmark M0 |

Handler ditulis di belakang trait `Rt` (`ioimg-runtime`). Untuk membandingkan secara adil, W1–W3 harus dijalankan pada **tiga** konfigurasi: `tokio+Tokio`, `tokio+Uring`, `compio+Compio`. Bila `compio` menang ≥ 15% p99 atau ≥ 20% CPU tanpa kehilangan RPS, profil itu dipromosikan; bila tidak, ia tetap ada sebagai opsi untuk operator yang mau membayar harga ekosistem.

> Koreksi riwayat: versi awal dokumen ini menyatakan `hyper`+`axum` tidak bisa diport ke runtime io_uring. Itu keliru dan sudah direvisi (ADR-0007). Yang dipertahankan dari kesimpulan lama adalah **default**: `tokio` tetap default sampai ada angka.

## 4. Desain data plane

```
                    ┌──────────────────────────────┐
  request ──────────▶ hyper (tokio, epoll)         │
                    │  routing, auth, ops parse    │
                    └──────────────┬───────────────┘
                                   │ CacheLookup { key }
                    ┌──────────────▼───────────────┐
                    │ L1: moka (sync, sharded)     │
                    └──────────────┬───────────────┘
                                   │ miss
                    ┌──────────────▼───────────────┐
                    │ L2: IoBackend                │
                    │  ┌────────────┐ ┌──────────┐ │
                    │  │ uring     │ │ tokio    │ │
                    │  │ fixed buf │ │ blocking │ │
                    │  │ O_DIRECT  │ │ pool     │ │
                    │  └────────────┘ └──────────┘ │
                    └──────────────┬───────────────┘
                                   │ miss
                    ┌──────────────▼───────────────┐
                    │ fetch → decode → resize →    │
                    │ encode  (CPU pool, bounded)  │
                    └──────────────┬───────────────┘
                                   │ write-behind
                    ┌──────────────▼───────────────┐
                    │ Body sending                 │
                    │  uring: splice/tcp zerocopy  │
                    │  tokio: Frame stream         │
                    └──────────────────────────────┘
```

### 4.1 Buffer & alignment

```rust
#[repr(C, align(4096))]
pub struct AlignedBlock([u8; 4096]);

pub struct AlignedBuf { ptr: NonNull<u8>, len: usize, cap: usize } // 4096-aligned, alloc via std::alloc
```

- Ukuran body di L2 dibulatkan ke 4096 agar `O_DIRECT` valid; `meta` menyimpan panjang sebenarnya.
- Fixed buffer ring: `N = min(1024, entries/4)` buffer 256 KB yang diregister sekali di startup.
- Buffer digunakan ulang; `CQE` selesai → buffer kembali ke pool (no alloc di hot path).
- **Pada profil `compio`, pool tidak boleh berbasis pinjaman**: buffer berpindah kepemilikan (`IoBufMut` → `BufResult`). Gunakan `AsyncReadManaged(At)` untuk baca, dan `into_inner()` saat buffer dikembalikan ke pool. Alignment `O_DIRECT` harus diverifikasi terpisah (compio-buf tidak mendokumentasikan alokasi aligned).

### 4.2 Zero-copy body: klaim yang harus dikoreksi

Desain awal mengasumsikan `file_fd --splice--> pipe --> socket_fd`. **Hasil riset:** tidak ada opcode `splice` di `compio`, dan `hyper` tidak mengekspos zero-copy untuk body. Jadi:

| Profil | Cara kirim body | Zero-copy? |
| --- | --- | --- |
| `runtime-tokio` + `TokioBackend` | `hyper` body stream dari `tokio::fs` | Tidak (salin) |
| `runtime-tokio` + `UringBackend` | `read` ke fixed buffer → `write`/`send` ke socket; opsi `send_zc` manual di luar hyper | Parsial |
| `runtime-compio` | `write` dari buffer; `send_zc` tersedia tetapi butuh handling notifikasi CQE & lifetime buffer | Parsial (belum di v1) |

Keputusan v1: **jangan janjikan zero-copy.** Optimalkan jumlah salinan (buffer reuse, `write_all` dari slice tunggal) dan ukur biayanya di W2 (1 MB). Kalau egress jadi bottleneck, dua opsi: (a) `send_zc` dengan notification handling, (b) serahkan egress ke CDN.

Catatan: dengan TLS di proses, salinan tambahan **tidak terhindarkan** — di profil compio, `compio-tls` memakai `SyncStream` (adaptor `std::io::Read/Write`) sehingga pasti menyalin.

## 5. Backpressure & load shedding

Kecepatan bukan hanya latensi rata-rata, tapi **perilaku saat jenuh**:

| Resource | Sinyal | Aksi |
| --- | --- | --- |
| Worker transformasi | queue depth > 2× workers | tolak `503 overloaded` untuk request baru non-cache-hit; cache hit tetap dilayani |
| Koneksi | > `max_conns` | `load_shed` di tower (drop/503) |
| Encoder AVIF | semaphore penuh | downgrade ke WebP bila `q_auto` & `f_auto` memungkinkan; else queue dengan timeout 300 ms |
| Bandwidth keluar | NIC saturasi | metric + alert; tidak bisa di-throttle per request dengan baik |

Prinsip: **cache hit tidak boleh ikut mati** karena miss membanjiri CPU. Pisahkan pool & antrean.

## 6. Metodologi benchmark

### 6.1 Mesin uji (wajib didokumentasikan di setiap hasil)

```
CPU     : 8 vCPU (AMD EPYC / Intel Xeon, dedicated, no noisy neighbor)
RAM     : 32 GB
Disk    : NVMe lokal (>= 3 GB/s seq read, >= 500k IOPS 4k)
NIC     : 10 Gbps
Kernel  : 6.6.x, io_uring aktif, seccomp disabled/kustom
Client  : 2 mesin terpisah, `oha`/`wrk2`, keep-alive, TLS off + TLS on
Rust    : stable terbaru, LTO=fat, codegen-units=1, target-cpu=native (dan generic)
```

### 6.2 Workload

| ID | Deskripsi | Tujuan |
| --- | --- | --- |
| W1 | 64 KB cache-hit, 1 worker dataset 10k file | throughput & p99 serving |
| W2 | 1 MB cache-hit | egress/splice efektivitas |
| W3 | 256 KB cache-hit, 100% random key | cache lookup + disk |
| W4 | cold transform 3000px JPEG → 1280 WebP | CPU path |
| W5 | cold transform → AVIF | worst-case CPU |
| W6 | mixed 90% hit / 10% miss | realistis |
| W7 | 1000 origin lambat (500 ms) | backpressure & coalescing |

### 6.3 Matriks eksperimen

| Variabel | Nilai |
| --- | --- |
| Backend | `tokio+Tokio` vs `tokio+Uring` vs `compio+Compio` |
| io_uring opts | baseline, `+O_DIRECT`, `+registered buffers`, `+registered files`, `+SQPOLL`, `+IOPOLL` (4k random) |
| Buffer size | 64/256 KB |
| TLS | off vs on (rustls; di compio TLS lewat `SyncStream` → selalu menyalin) |
| Driver (compio) | `uring` vs `polling` (dipaksa via `ProactorBuilder`) |
| Worker count | 8, 16 (2× oversubscribe) |

Setiap kombinasi: 3 run, ambil median; laporkan p50/p95/p99, RPS, CPU%, syscalls/sec (`perf stat -e syscalls:sys_enter_*`), bytes/s, peak RSS.

### 6.4 Alat

- `oha` dan `wrk2` (latency-corrected) untuk load.
- `perf stat`, `bpftrace` untuk syscall & scheduler.
- `hotspot`/flamegraph untuk CPU.
- `strace -c -f` (beban rendah) untuk verifikasi syscall count.
- `fio` untuk baseline karakteristik disk.

### 6.5 Definition of done benchmark

- [ ] Script reproducible ada di `xtask/bench` + dataset generator.
- [ ] Hasil JSON + grafik disimpan di `bench/results/<tanggal>/`.
- [ ] Ada tabel keputusan: apakah io_uring default-on membenarkan kompleksitas?
- [ ] Angka PRD (`p95 < 15 ms`) terbukti atau PRD direvisi.

## 7. Target SLO performa

| Metrik | Target | Alarm |
| --- | --- | --- |
| p50 cache-hit (64 KB, TLS on) | ≤ 3 ms | > 6 ms 5 menit |
| p95 cache-hit | ≤ 15 ms | > 25 ms 5 menit |
| p99 cache-hit | ≤ 40 ms | > 80 ms |
| p95 cold WebP | ≤ 250 ms | > 500 ms |
| p95 cold AVIF | ≤ 600 ms | > 1.2 s |
| Cache hit ratio | ≥ 90% | < 80% selama 15 menit |
| Syscall/request (cache hit) | ≤ 12 | > 25 |
| CPU per 1k request (64 KB hit) | ≤ 0.25 core-s | regresi > 20% vs baseline |

## 8. Anti-pattern yang dihindari

1. **`spawn_blocking` per file read** pada egress tinggi → thread explosion.
2. **Membaca seluruh gambar ke `Vec<u8>`** lalu encode → memori 2–3× ukuran piksel. Gunakan streaming libvips-style atau brownfield buffer reuse.
3. **AVIF encode tanpa batas konkurensi** → latensi semua request naik.
4. **SQPOLL secara default** → butuh `CAP_SYS_ADMIN`, sering gagal di container, dan membakar CPU idle.
5. **`fsync` setiap write cache** → tidak perlu; cache loss saat crash dapat diterima.
6. **Mengukur throughput tanpa mengukur p99 dan CPU** → optimasi palsu.
