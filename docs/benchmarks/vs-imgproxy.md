# ioimg vs imgproxy, head-to-head di mesin yang sama

Benchmark [`imgproxy.net/blog/image-processing-servers-benchmark`](https://imgproxy.net/blog/image-processing-servers-benchmark/)
(Apr 2024) memakai dataset **DIV2K** dan kode yang terbuka, jadi bisa direproduksi.
Dokumen ini mencatat reproduksinya dan hasil head-to-head di satu mesin.

Angka absolut hanya berlaku untuk mesin ini; yang bisa dibaca adalah **rasio**.

## 1. Metode (sama dengan repo mereka)

Sumber: [`imgproxy/image-servers-benchmark`](https://github.com/imgproxy/image-servers-benchmark).

1. `DIV2K_valid_HR.zip` (100 gambar, 2040×1356) — sumber dari ETH Zürich.
2. Tiap PNG dikonversi dengan `vips copy` persis seperti `Makefile` mereka:
   `.jpg`, `.webp`, dan `.avif[Q=65,effort=0]`; PNG asli tetap dipakai.
3. Origin: nginx (`nginx:alpine`), bukan `python -m http.server`, agar fetch
   bukan variabel.
4. Beban: 2 koneksi keep-alive (k6 mereka jalan dengan `-u $(nproc)` = 2 VU di
   c7i.large), rotasi 100 file, `rs:fit:512:512`, format output = format sumber,
   JPEG q80 (**progressive** di imgproxy), PNG q80, WebP q75, AVIF q65.
5. Cache **mati** di kedua sisi (ioimg: direktori cache baru + sumber unik
   `?i=N`; imgproxy: tanpa cache backend).
6. Pembatas CPU: ioimg `taskset -c 0,1`; imgproxy `docker --cpus=2`.
7. imgproxy: `darthsim/imgproxy:latest` dengan env dari `docker-compose.yml`
   mereka (`IMGPROXY_MAX_SRC_RESOLUTION=99`, `IMGPROXY_JPEG_PROGRESSIVE=1`,
   `IMGPROXY_AVIF_SPEED=9`) + `IMGPROXY_ALLOW_LOOPBACK_SOURCE_ADDRESSES=true`.
8. Durasi 10 s per format setelah server siap; latensi p50 dari loadgen.

Mesin: Ryzen 7 PRO 5850U (15 W), 16 core, cache disk di NVMe.

## 2. Hasil

| Format | ioimg | imgproxy | Rasio ioimg/imgproxy | Ukuran output (ioimg / imgproxy) |
| --- | --- | --- | --- | --- |
| JPEG q80 | 135,6 img/s | 137,4 img/s | 0,99× | 55,3 KB / 51,0 KB (mereka progressive) |
| PNG | 22,4 img/s | 27,5 img/s | 0,81× | 389 KB / 384 KB |
| WebP q75 | 44,2 img/s | 33,2 img/s | **1,33×** | 40,3 KB / 40,5 KB |
| AVIF q65, effort 3 (default ioimg) | 11,8 img/s | 16,9 img/s | 0,70× | 36,9 KB / 37,6 KB |
| AVIF q65, `IOIMG_AVIF_EFFORT=1` | 20,6 img/s | 16,9 img/s | **1,22×** | 36,8 KB / 37,6 KB |

p50 (2 klien, cache mati): JPEG 14,7 vs 14,0 ms; PNG 87,9 vs 74,7 ms;
WebP 44,3 vs 60,3 ms; AVIF effort 1 96,6 vs 105,5 ms.

## 3. Kesimpulan

- **Bukan 2× lebih cepat.** Pada dataset dan mesin yang sama, ioimg setara
  imgproxy untuk JPEG, ~19% lebih lambat di PNG, 33% lebih cepat di WebP, dan
  AVIF tergantung effort.
- Perbandingan sebelumnya (1,5–3,2×) memakai gambar sumber yang berbeda dan
  membandingkan mesin lokal dengan angka AWS terbitan imgproxy — tidak valid
  untuk klaim. Angka yang boleh dipakai adalah tabel di atas.
- AVIF default (`effort 3`) kalah dari `IMGPROXY_AVIF_SPEED=9` tanpa keuntungan
  ukuran di dataset ini (36,9 vs 37,6 KB). `effort 1` menang 22% dengan ukuran
  yang sama. Di gambar lain (foto 2400×1600) effort 3 memang lebih kecil
  (8,7 KB vs 11,6 KB), jadi default adalah trade-off konten — lihat
  §12 shrink-on-load.
- PNG: zlib masih jalur terpanas; kandidat tuning level kompresi.

## 4. Yang tidak diukur di sini

- HIT/cache hidup (imgproxy tidak mengevaluasinya; ioimg 77k img/s p50 86 µs
  di 16 core).
- Memori per request dan waktu cold start.
- AVIF/WebP dengan setelan encoder yang identik (encoder berbeda implementasi).
- Mesin produksi (server 8 core Jakarta). Rasio di atas yang dipakai untuk
  memperkirakan kapasitas, bukan angka absolut laptop.

## Hasil setelah optimasi

Setelah transform dipindah keluar dari core yang di-pin celeris dan zlib-ng
dipakai (lihat [`pinning-and-zlib-ng.md`](pinning-and-zlib-ng.md)), head-to-head
yang sama diulang di mesin yang sama:

| Format | ioimg sebelum | ioimg sesudah | imgproxy | Rasio sesudah |
| --- | --- | --- | --- | --- |
| JPEG q80 | 135,6 | 135,0 | 137,4 | 0,98× |
| PNG | 22,4 | **43,0** | 27,5 | **1,56×** |
| WebP q75 | 44,2 | **49,0** | 33,2 | **1,48×** |
| AVIF q65 | 11,8 (effort 3) | **22,0–24,0** (effort 2) | 16,9 | **1,30–1,42×** |

Catatan: angka "sesudah" memakai zlib-ng lewat `LD_LIBRARY_PATH` di mesin uji
(glibc), bukan image Alpine — lihat §8 dokumen di atas untuk selisihnya.

## Hasil final: image produksi (`ioimg:vips-prod`) vs imgproxy

Kedua server dijalankan sebagai container di mesin yang sama, `--network host`,
`--cpus=2` (setara c7i.large 2 vCPU), origin nginx lokal, DIV2K, `fit 512x512`,
cache dimatikan di kedua sisi, loadgen 2 koneksi selama 10 s, **semua request
cache-MISS**. imgproxy memakai setelan benchmark mereka sendiri:
`IMGPROXY_JPEG_PROGRESSIVE=1`, `IMGPROXY_AVIF_SPEED=9`.

| Format | ioimg 2 core | imgproxy 2 core | Rasio | p50 ioimg / imgproxy |
| --- | --- | --- | --- | --- |
| JPEG q80 | 134 | 142 | 0,94x | 14,6 / 13,4 ms |
| PNG q80 | **40** | 28 | **1,43x** | 42,7 / 72,7 ms |
| WebP q75 | **40** | 33 | **1,21x** | 43,2 / 64,5 ms |
| AVIF q65 effort 1 | 12 | **17** | **0,71x** | 160,3 / 105,6 ms |

Ukuran output, satu gambar yang sama, kualitas sama:

| Format | ioimg | imgproxy | Selisih |
| --- | --- | --- | --- |
| JPEG q80 | 55.347 B | 50.955 B | +8,6% (mereka progressive) |
| PNG | 384.065 B | 384.257 B | -0,05% |
| WebP q75 | 40.332 B | 40.484 B | -0,4% |
| AVIF q65 | 36.790 B | 37.603 B | -2,2% |

### Koreksi: harness bench sebelumnya menghitung sebagian HIT sebagai MISS

Sampai bagian ini, loadgen memberi indeks request dari counter **per koneksi**.
Dengan 2 koneksi, kedua koneksi memutar URL yang sama dari awal, sehingga
sekitar separuh "MISS" sebenarnya cache HIT: throughput naik, p50 turun ke
ratusan mikrodetik, dan rasio terhadap imgproxy (yang tidak punya cache) jadi
terlalu bagus. Tabel di atas memakai loadgen yang sudah diperbaiki (counter
global + latensi diukur setelah body dibaca), dan diverifikasi silang: jumlah
file cache yang dibuat = jumlah request. Angka lama (PNG 1,83x, AVIF 1,31x)
**tidak valid** dan sudah diganti.

### Kesimpulan yang benar

- **Menang**: PNG +43%, WebP +21% throughput, dengan ukuran output sama.
- **Seri**: JPEG (0,94x, dalam noise + mereka progressive 8,6% lebih kecil).
- **Kalah**: AVIF 0,71x — satu-satunya format di mana imgproxy lebih cepat.

### AVIF: kenapa kalah, dan kenapa tetap effort 1

`IMGPROXY_AVIF_SPEED=9` berarti "paling cepat" di imgproxy; di tumpukan kita
`effort` justru makin lambat kalau dinaikkan. Diukur lewat HTTP pada image
produksi (512 px, Q=65, satu request, termasuk decode + cache write):

| effort | waktu | ukuran |
| --- | --- | --- |
| **1** | **176 ms** | **36.790 B** |
| 2 | 185 ms | 37.128 B |
| 3 | 232 ms | 36.933 B |
| 4 | 589 ms | 37.794 B |
| 6 | 1,46 s | 37.568 B |
| 9 | 7,13 s | 37.749 B |

Jadi effort 1 adalah yang tercepat **dan** terkecil: default sekarang 1
(`IOIMG_AVIF_EFFORT`). imgproxy tetap lebih cepat karena meneruskan `speed` ke
`cpu-used` libaom (jalur "cepat" yang tidak diekspos libvips 8.18 lewat
`effort`). Mitigasi kita bukan mengejar angka itu, tapi menempatkan AVIF di
tempat yang benar: `f_auto` + cache, di mana biaya encode hanya dibayar sekali
per objek (HIT 77k img/s).

Catatan pembacaan lain:

- PNG/WebP "tanpa limit" tidak lebih tinggi dari 2 core karena pipeline
  transform memang hanya memakai ~1,4 core per request (lihat §2 dokumen
  `pinning-and-zlib-ng.md`); sisa core dipakai request lain.
- Variasi antar-run di laptop ini 5-10% karena thermal, jadi angka 2 core
  dibaca sebagai rentang (PNG 38-42, WebP 38-42, AVIF 11-13).
