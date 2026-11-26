# Menutup gap PNG & AVIF vs imgproxy

Dua penyebab berbeda, keduanya terukur: satu di runtime kita sendiri (affinity
CPU), satu di build (zlib). Dokumen ini mencatat cara mengukurnya supaya angka
bisa diulang, dan apa yang akhirnya diubah di kode.

Latar: benchmark head-to-head di [`vs-imgproxy.md`](vs-imgproxy.md) menunjukkan
ioimg kalah di PNG (0,81×) dan AVIF (0,70×), seri di JPEG, menang di WebP.

## 1. Metode

- Sumber: `~/div2k/dataset` (DIV2K, 2040×1356), dilayani nginx container di
  `127.0.0.1:8099`, dikonversi persis seperti `Makefile` benchmark imgproxy.
- Beban: `bench/loadgen -list ~/div2k/list-<fmt>.txt -threads 2 -secs 8`
  (5.000 URL unik per format, jadi semua request MISS), 2 koneksi keep-alive —
  sama dengan 2 virtual user di benchmark imgproxy.
- Binary: `go build -tags vips -o /tmp/ioimg3 ./cmd/ioimg`, bukan `go test`.
- Profil in-process (untuk memisahkan decode/encode): binary kecil yang
  memanggil `internal/imaging` langsung, 2 warmup + 5 pengukuran.
- zlib-ng: `zlib-ng 2.3.3` dibangun dengan `-DZLIB_COMPAT=ON`, lalu dijalankan
  sebagai `LD_LIBRARY_PATH=/tmp/zlibng/lib` (libpng/libvips memuat `libz.so.1`
  yang kompatibel). Ini meniru apa yang dibundel image imgproxy
  (`/opt/imgproxy/lib/libz.so.1` = zlib-ng 2.3.3), tanpa membangun ulang libvips.

## 2. Penyebab pertama: celeris mem-pin worker ke satu core

`Server-Timing` pada request yang sama memberi `encode;dur=69 ms` lewat celeris
tetapi 51 ms lewat fiber/nethttp — binary sama, ops sama, output sama
(389.105 B). Isolasi dengan probe in-process (tanpa HTTP):

| Pembatasan CPU (`taskset`) | PNG 512px |
| --- | --- |
| 1 core | 69,3 ms |
| 2 core | 57,7 ms |
| 3 core | 50,8 ms |
| 4 core | 52,0 ms |
| tanpa batas | 51,7 ms |

`cpu/wall` = 0,99 di 1 core, 1,42 di 4 core: pipeline libvips **memang**
memakai ~1,4 core (thread decode menghasilkan strip sementara thread encode
mengonsumsi). celeris memanggil `sched_setaffinity` + `runtime.LockOSThread`
per worker, dan thread turunan mewarisi mask satu core itu — jadi overlap
decode/encode hilang.

Bukan GOMAXPROCS (1/2/16 sama saja), bukan GC (`GOGC=off` sama saja), bukan
jumlah worker (4 vs 16 sama), bukan IRQ (`taskset` ke core 0/1/3/5 sama).

Perbaikan: transform dijalankan di goroutine sendiri (`IOIMG_OFFLOAD`, default
aktif). Goroutine itu mendapat M tanpa pinning, jadi thread libvips-nya bebas
dijadwalkan di beberapa core.

## 3. Penyebab kedua: zlib vs zlib-ng

PNG berbiaya **decode**, bukan encode. Profil in-process pada sumber 2040×1356:

| Tahap | Waktu |
| --- | --- |
| decode penuh (PNG) | 44,9 ms |
| decode + resize 512 | 44,4 ms |
| decode + resize + simpan PNG | 50,8 ms |

Jadi ~88% waktu PNG adalah inflate. zlib-ng mempercepat inflate (dan deflate)
DI level ABI yang sama:

| Konfigurasi | PNG | JPEG | WebP |
| --- | --- | --- | --- |
| zlib sistem | 53,3 ms | 44,6 ms | 60,1 ms |
| zlib-ng 2.3.3 | 39,7 ms | 37,1 ms | 60,0 ms |

JPEG juga ikut cepat karena **sumbernya PNG** (decode tetap lewat zlib). WebP
tidak berubah karena sumber `.webp` tidak memakai zlib. Ukuran output juga
sedikit lebih kecil (389.105 → 384.065 B), sama dengan ukuran yang dihasilkan
imgproxy (384 KB).

## 4. Hasil akhir lewat binary + loadgen

DIV2K, 2 koneksi, 8 detik, semua MISS (images/s):

| Konfigurasi | PNG | JPEG | WebP | AVIF q65 |
| --- | --- | --- | --- | --- |
| imgproxy (image resmi, 2 core) | 27,5 | 137,4 | 33,2 | 16,9 |
| ioimg sebelum | 22,4 | 135,6 | 44,2 | 11,8 (effort 3) |
| + offload transform | 29,0 | 135,0 | 49,0 | — |
| + zlib-ng (keduanya) | **43,0** | 135,0 | **49,0** | 22,0–24,0 (effort 2) |

Kesimpulan: PNG 22 → 43 images/s (+92%, kini 1,56× imgproxy), WebP 1,48×,
JPEG seri, AVIF 1,3–1,4× dengan file yang sama besar.

p50: PNG 46,8 ms (imgproxy 74,7), JPEG 14,6 (14,0), WebP 39,8 (60,3),
AVIF 84,8 (105,5).

## 5. AVIF: effort 2 sebagai default

Sumber `.avif` DIV2K, `fit 512`, q65, 2 koneksi:

| effort | images/s | ukuran |
| --- | --- | --- |
| 3 (default lama) | 14 | 36.933 B |
| **2 (default baru)** | **22** | 37.128 B |
| 1 | 26 | 36.790 B |
| imgproxy (speed 9) | 16,9 | 37.603 B |

Effort 2 dipilih: 57% lebih cepat dari effort 3 dengan file 0,5% lebih besar,
dan tetap di atas imgproxy. Untuk beban yang sangat CPU-bound, `IOIMG_AVIF_EFFORT=1`
masih tersedia (26 images/s, file justru paling kecil di korpus ini).

## 6. Progressive JPEG: diukur, tidak dijadikan default

| | images/s | ukuran |
| --- | --- | --- |
| baseline (default) | 119 | 55.329 B |
| progressive | 95 | 51.511 B |

−7% byte untuk −20% throughput. imgproxy menyalakannya default; kita
menyediakannya lewat `IOIMG_JPEG_PROGRESSIVE=true` dan membiarkannya mati,
karena jalur JPEG adalah jalur yang paling sering cache-miss dan throughput-nya
lebih bernilai daripada 4 KB per gambar.

## 7. Yang belum dikerjakan

- **zlib-ng harus masuk image produksi.** Di mesin pengembangan ini hanya
  diaktifkan lewat `LD_LIBRARY_PATH`; `Dockerfile.vips` sekarang membangun
  zlib-ng dan memasangnya lebih dulu di loader path.
- `IOIMG_VIPS_CONCURRENCY=2` tidak menambah throughput pada 2 koneksi
  (43 vs 43 images/s PNG) dan sedikit lebih buruk di 3 — tetap default 1.
- Gap yang tersisa ke imgproxy bukan lagi di PNG/AVIF, melainkan di ukuran
  file JPEG (mereka progressive) dan fitur, bukan kecepatan.

## 8. Cek artifak: image Alpine lebih lambat dari mesin uji

Angka di §4 diukur dengan binary glibc di Arch + `LD_LIBRARY_PATH` zlib-ng.
Setelah `Dockerfile.vips` memasang zlib-ng, image diuji dengan cara yang sama
(DIV2K, 2 koneksi, 8 detik, semua MISS):

| Format | host glibc (tanpa limit) | image alpine, `--cpus=2` | image alpine, `--cpus=4` |
| --- | --- | --- | --- |
| PNG | 43 | 30 | 33 |
| JPEG | 135 | 106 | — |
| WebP | 49 | 52 | — |
| AVIF q65 effort 2 | 22–24 | 11 | 15 |

Dua sebab, keduanya bukan bug kode kita:

1. `--cpus` adalah kuota CFS, dan tiap request PNG memakai ~1,4 core (decode +
   encode paralel). Dua request serentak butuh ~2,8 core, jadi kuota 2 core
   menahan throughput. Kuota 4 core menaikkan PNG 30 → 33 dan AVIF 11 → 15.
2. Build libvips Alpine (musl) lebih lemah daripada build glibc untuk beban ini,
   terutama AVIF: pada kuota yang sama AVIF tetap jauh di bawah host. Ini
   konsisten dengan imgproxy yang membangun libvips + libaom sendiri di atas
   Ubuntu, bukan memakai paket distro.

Konsekuensi: **image produksi sebaiknya berbasis glibc dengan libvips yang
dibangun sendiri** (zlib-ng + libaom/libheif), seperti milik imgproxy. Image
Alpine sekarang tetap benar secara fungsional dan sudah memakai zlib-ng, tetapi
bukan wadah yang tepat untuk mengejar angka di §4. Ini pekerjaan berikutnya,
bukan klaim yang sudah selesai.

## 9. Image glibc dengan libvips 8.18.7 dari source

§8 menyebut versi libvips sebagai tersangka, dan setelah image glibc diuji
tersangka itu terbukti. Urutan percobaannya:

| Image | libvips | PNG, tanpa limit | PNG, `--cpus=2` | AVIF, tanpa limit |
| --- | --- | --- | --- | --- |
| alpine 3.21 | 8.16.x | 34 | 30 | 13 |
| debian trixie (`libvips42t64`) | 8.16.1 | 34 | 30 | 20 |
| debian trixie, vips 8.18.7 dari source | 8.18.7 | **47** | **51** | 20–22 |

Jadi penjelasan di §8 bahwa libc (musl vs glibc) sebagai sebab utama **salah**:
Debian glibc dengan libvips 8.16.1 memberi angka yang sama dengan Alpine
(34 PNG), dan baru naik ke 47 setelah libvips dibangun sendiri di versi 8.18.7
— versi yang sama dengan mesin uji. Yang benar: **versi libvips**, bukan libc,
yang menentukan kecepatan PNG. Kuota CFS tetap berpengaruh (§8), tetapi bukan
sebab utama.

`Dockerfile.vips` sekarang: stage `zlibng`, stage `vips` (meson, `-Dspng=disabled`
mengikuti mesin uji), stage build Go dengan `PKG_CONFIG_PATH` ke libvips sendiri,
dan runtime `debian:trixie-slim` yang memakai paket `libvips42t64` hanya untuk
menarik dependency (libjpeg, libpng, libwebp, libheif, libjxl, orc, imagequant,
deflate, cgif) sementara `LD_LIBRARY_PATH=/opt/vips/lib:/opt/zlib-ng/lib`
menutupi libvips-nya. Output PNG identik byte-per-byte dengan host: 384.065 B.

### Satu perbedaan yang tersisa: AVIF memakai encoder berbeda

Ukuran AVIF q65 effort 2 untuk sumber yang sama:

| Lingkungan | libheif | Encoder AV1 tersedia | Ukuran |
| --- | --- | --- | --- |
| host Arch | 1.23.7 | aom, **rav1e**, svt-av1 | 37.128 B |
| image debian | 1.23.6 | aom saja | **49.205 B** |

Debian tidak memaketkan plugin `rav1e`/`svtenc` untuk libheif, jadi image hanya
bisa memakai `aomenc` dan hasilnya ~32% lebih besar pada q yang sama. Kecepatan
keduanya setara (20–22 img/s), yang berbeda hanya efisiensi kompresi. Pilihan
berikutnya: bangun libheif dengan plugin rav1e di image, atau terima `aomenc`
dan sesuaikan klaim ukuran AVIF di dokumen lain agar tidak memakai angka host.
