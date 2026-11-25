# ioimg PoC — bukti, bukan janji

Proof of concept yang menjawab pertanyaan: **apakah jalur io_uring benar-benar memberi keuntungan untuk image proxy, dan di mana bottleneck sebenarnya?**

Semua angka di bawah diukur di mesin ini, bukan diambil dari dokumentasi.

```
kernel 7.2.15-zen (ext4), 16 core, loopback, tanpa TLS
axum 0.8.9 | hyper 1.12 | tokio 1.54 | cyper-axum 0.9.0 | compio 0.19.2
dataset: JPEG sintetis 2400x1600 (~360 KiB), 8 file
```

## Isi

| Binary | Fungsi |
| --- | --- |
| `ioimg-poc-tokio` | server axum di tokio; `IOIMG_IO=std|tokio|uring` |
| `ioimg-poc-compio` | server axum di io_uring via `cyper_axum::serve`; `IOIMG_IO=std|uring` |
| `probe-uring` | cek setup io_uring, read/write, `O_DIRECT` (+benchmark) |
| `bench` | load generator HTTP: RPS + p50/p90/p99; `{i}` di path = request unik (memaksa cache miss) |
| `bench-transform` | biaya CPU transformasi tanpa HTTP |
| `gen-images` | dataset uji sintetis |

```bash
cargo build --release
./target/release/gen-images ../../artifacts/datasets/src 8 2400 1600
IOIMG_PORT=8080 IOIMG_IO=uring ./target/release/ioimg-poc-tokio
curl -D- 'localhost:8080/w_400,f_jpeg/photo00.jpg'      # MISS lalu HIT
curl -D- 'localhost:8080/w_300,h_300,fit_cover,f_png/photo01.jpg'
curl localhost:8080/metrics
./target/release/bench 127.0.0.1:8080 /w_400,f_jpeg/photo00.jpg 8 5
```

## 1. Yang terbukti berfungsi

Pipeline lengkap, 8/8 skenario lulus:

| Kasus | Hasil |
| --- | --- |
| Miss → transform → tulis cache | `200`, `x-ioimg-cache: MISS`, file cache muncul |
| Request berikutnya | `200`, `x-ioimg-cache: HIT`, byte identik |
| `f_webp`, `f_png`, `fit_cover` | `content-type` benar |
| `w_0` (invalid) | `400` + pesan jelas |
| Sumber tidak ada | `404` |
| `../` di path | `400` (ditolak) |
| `/metrics` | counter hits/misses/bytes/ns |
| Cache key | `blake3_128(v1|ops|source)`, terbukti order-independent (unit test) |

Unit test: `cargo test -p poc-core` → 3 lulus (parsing ops, kanonikalisasi key, penolakan ops invalid).

## 2. Throughput HTTP (body 30 KiB, 8 koneksi keep-alive)


Metodologi: **3 putaran per konfigurasi, ambil median**; 8 koneksi keep-alive; 3 s per putaran; load average mesin 2–5 (ada browser berjalan), jadi angka absolut bisa bergeser ±10% antar sesi — **rasio** antar konfigurasi stabil. Client benchmark membangun request sekali (bukan `format!` per request); versi sebelumnya sempat membatasi throughput client dan membuat angka terlalu rendah.

| Konfigurasi | RPS (median) | Rentang | p50 |
| --- | --- | --- | --- |
| **Rust `tokio` + `std::fs`** | **118.010** | 103.399–122.985 | 63 µs |
| Go Fiber + prefork (16 proses) | 79.419 | 77.062–82.368 | 90 µs |
| Go Fiber (1 proses) | 55.152 | 53.736–57.119 | 106 µs |
| Go `net/http` | 37.544 | 30.009–38.403 | 169 µs |

Angka `io_uring` (backend ring) dan `compio` di bawah ini **satu run** (belum diulang 3×), jadi jangan dibandingkan langsung dengan tabel di atas:

| Konfigurasi (1 run) | RPS | p50 | p99 |
| --- | --- | --- | --- |
| tokio + io_uring (1 ring thread) | 94.080 | 78 µs | 163 µs |
| tokio + io_uring (4 ring thread) | 84.880 | 85 µs | 195 µs |
| compio + io_uring (1 ring, 1 core) | 44.914 | 168 µs | 294 µs |

## 3. Yang sebenarnya jadi bottleneck: CPU transform

2400×1600 → 400 px, JPEG q80, diukur tanpa HTTP & tanpa cache. Median 3 putaran:

| Pipeline | ms/gambar | Rentang | img/s/core |
| --- | --- | --- | --- |
| **govips `NewThumbnailFromBuffer`** (Go, shrink-on-load) | **9,62** | 9,43–10,14 | **104** |
| Rust + libvips FFI (`vips_thumbnail_buffer`) | 14,54 | 14,48–14,85 | 69 |
| govips `NewImageFromBuffer` + resize (decode dulu) | 21,70 | 19,90–23,50 | 46 |
| Rust `image` (Lanczos3) | 52,90 | 52,20–53,50 | 19 |
| Go `x/image` Bilinear | 57,00 | 56,30–57,20 | 18 |
| Go `x/image` CatmullRom | 135,70 | 133,70–151,70 | 7 |

MISS end-to-end (transform tiap request, `IOIMG_NOCACHE=1`/`-nocache`, 8 koneksi):

| Konfigurasi | RPS | p50 |
| --- | --- | --- |
| Rust tokio + Lanczos | **86** | 92 ms |
| Go Fiber + CatmullRom | 37 | 226 ms |

Kesimpulan yang sama seperti sebelumnya, sekarang dengan data yang stabil: **pustaka codec menentukan 3–14×, bahasa hanya ±30%.** `libvips` + shrink-on-load menang di kedua bahasa; `image` crate dan `x/image` sama-sama terlalu lambat untuk produksi.

## 4. `probe-uring` (ext4, 64 MiB)

| Operasi | `std::fs` | io_uring (naif) |
| --- | --- | --- |
| read 64 MiB | 23,1 ms (2,70 GiB/s) | 24,4 ms (2,56 GiB/s) |
| write 16 MiB | 14,7 ms (4,25 GiB/s) | 16,8 ms (3,73 GiB/s) |

- `io_uring_setup` **OK**, `features=0x3ffff` — io_uring tersedia dan dipakai (server punya fd `anon_inode:[io_uring]`).
- `O_DIRECT` + buffer aligned 4096 → **OK**.
- `O_DIRECT` + buffer tidak aligned → **EINVAL** (sesuai harapan) — **tetapi hanya di ext4**. Di tmpfs, `O_DIRECT` **diabaikan diam-diam** dan uji unaligned "lulus". Artinya: perilaku cache berubah tergantung filesystem, dan ini tidak akan terlihat tanpa probe.

## 5. Temuan yang mengubah rencana

1. **Ring naif kalah dari `std::fs`** untuk objek kecil. `IoBackend::Uring` harus punya: batching (banyak SQE per `io_uring_enter`), multi-ring per worker, dan `statx/openat` di dalam ring. Tanpa itu, backend uring hanya menambah kompleksitas.
2. **Transform CPU mendominasi** (72 ms vs cache read ~0,1 ms). Investasi pertama harus di codec, bukan I/O.
3. **compio hanya memakai 1 core** dengan `#[compio::main]` + `serve`. Perbandingan adil butuh `SO_REUSEPORT` multi-proses atau `Dispatcher`.
4. **`O_DIRECT` bergantung filesystem**: ext4 menegakkan alignment, tmpfs mengabaikannya. `probe-uring` wajib dijalankan pada direktori cache yang sebenarnya, bukan `/tmp`.
5. **WebP lossless bukan opsi**: output 4× lebih besar dari JPEG q80.

## 6. Yang **belum** dibuktikan (jangan diklaim)

- TLS (profil compio memakai `SyncStream` → selalu menyalin)
- Fetch origin S3/R2 + SigV4, CDN purge
- AVIF (belum ada encoder di PoC), WebP lossy
- Eviction/LRU, ukuran cache, konsistensi multi-proses
- Signed URL & rate limit
- Batching io_uring, registered buffers, `splice`/`send_zc` (tidak ada di compio)
- Soak 72 jam, kebocoran memori, perilaku saat FD habis
- Skala multi-worker / multi-mesin; semua angka = 1 proses, 1 run

## 6b. Pembanding Go + libvips

Lihat [`../go-fiber/README.md`](../go-fiber/README.md). Ringkas (median 3 run, dataset & harness sama):

| Pipeline transform (2400×1600 → w400 JPEG q80) | ms/gambar | img/s/core |
| --- | --- | --- |
| govips shrink-on-load | 9,62 | 104 |
| Rust + libvips FFI | 14,54 | 69 |
| Rust `image` Lanczos3 | 52,90 | 19 |
| Go `x/image/draw` CatmullRom | 135,70 | 7 |

Hot path HIT: Rust tokio 118k RPS · Go Fiber prefork 79k · Go Fiber 1 proses 55k · Go `net/http` 38k.

Artinya: **pilihan pustaka (libvips) 3–14× lebih menentukan daripada pilihan bahasa**; dengan libvips, Rust dan Go berada dalam orde yang sama. Detail penting: **shrink-on-load** (`vips_thumbnail_buffer` / `NewThumbnailFromBuffer`) 2× lebih cepat daripada decode-dulu.

Baca io_uring: read 64 MiB via `std::fs` 32,50 ms vs via ring 32,00 ms — **seri**. Ring yang dipakai PoC ini submit-satu-per-operasi, tanpa batching dan tanpa registered buffers.

## 7. Rekomendasi

1. **Default produksi tetap `tokio`** + `IoBackend` (std/uring). compio tetap eksperimen.
2. **Ganti pipeline transform lebih dulu** (vips atau `fast_image_resize`+`mozjpeg`+`libwebp` lossy) dan ukur ulang; target realistis ≥ 100 img/s/core.
3. **Baru** optimalkan I/O: batching ring, multi-ring, `statx` di ring — dengan benchmark yang sama sebagai pembanding.
4. `probe-uring` dijalankan di setiap node & CI; laporkan driver efektif, bukan "io_uring tersedia".
