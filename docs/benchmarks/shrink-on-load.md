# Shrink-on-load & konkurensi libvips

Diukur 2026-11-25, AMD Ryzen 7 PRO 5850U (16 thread), Go 1.27, libvips 8.18.
Sumber: `poc/data/src/photo00.jpg` (2400x1600, 373 KB). Reproduksi:

```bash
IOIMG_BENCH_SRC=$PWD/../../poc/data/src/photo00.jpg \
  go test -tags vips ./internal/imaging/ -run xxx -bench . -benchtime 25x
IOIMG_VIPS_CONCURRENCY=1  go test -tags vips ./internal/imaging/ -bench Parallel -benchtime 60x
IOIMG_VIPS_CONCURRENCY=16 go test -tags vips ./internal/imaging/ -bench Parallel -benchtime 60x
```

## 1. Shrink-on-load (P0)

Sebelumnya semua request melewati `NewImageFromBuffer` (decode resolusi penuh)
lalu `ResizeWithVScale`. Sekarang request yang **murni resize** memakai
`vips_thumbnail_buffer` (`LoadThumbnailFromBuffer`), yang membiarkan libvips
men-decode pada skala yang diminta (JPEG DCT scaling / reduced-size decode).

| Operasi | decode-penuh (ms/img) | shrink-on-load (ms/img) | img/s/core |
| --- | --- | --- | --- |
| `w_400,c_fit` | 19,2 | **9,6** | 52 → **104** |
| `w_300,h_300,c_fill` | 19,3 | **9,5** | 52 → **105** |

2x, dan `c_fill` ikut terbawa karena jalurnya sama (`ThumbnailWithSize` dengan
crop bisa langsung dari buffer).

End-to-end lewat HTTP (MISS, satu request, `Server-Timing`):

```
sebelum : fetch 1,1 + decode 0,2 + transform 0,4 + encode 28,8  = 30,5 ms
sesudah : fetch 1,0 + transform 0,8 + encode 16,8               = 18,6 ms
```

Perhatikan `decode`/`transform` hampir nol: libvips mengeksekusi pipeline secara
lazy saat `ExportJpeg`, jadi biaya decode yang sebenarnya muncul di `encode`.
Rasio end-to-end 1,6x lebih kecil dari 2x karena tahap encode ikut serta.

Yang tetap di jalur lambat: trim, rotate, flip, blur, sharpen, pixelate,
penyesuaian warna, `c_pad`, `c_crop`, serta input animasi (GIF/WebP-ANIM).
`IOIMG_VIPS_THUMBNAIL=0` mematikan jalur cepat (dipakai test paritas).

## 2. Konkurensi libvips (P0)

`ConcurrencyLevel` sebelumnya `runtime.NumCPU()`. Server sudah menjalankan satu
transform per request secara paralel, jadi libvips memecah satu gambar ke semua
core dan mesin kelebihan thread.

| Konkurensi libvips | 16 transform paralel |
| --- | --- |
| `1` (default baru) | 1,87 ms/op → **535 img/s** |
| `16` | 3,42 ms/op → 293 img/s |

**1,83x** lebih cepat dengan `1`. `IOIMG_VIPS_CONCURRENCY` menaikkannya untuk
beban batch/warm yang memang sekuensial per gambar.

## 3. Catatan

- `go test -tags vips ./...` dan `go test ./...` (tanpa cgo) sama-sama lulus,
  termasuk `TestVipsThumbnailParity` yang membandingkan jalur cepat vs decode
  penuh (geometri identik, beda piksel rata-rata < 5/255) dan memastikan jalur
  yang diuji benar-benar dipakai.
- Metrik `ioimg_cache_misses_total` sebelumnya dihitung sebelum single-flight,
  jadi 8 request paralel untuk satu key tercatat 7 miss padahal transform yang
  jalan 1. Sekarang miss/hit dicatat di tempat kerja benar-benar terjadi, dan
  `ioimg_coalesced_total` menghitung request yang menumpang transform in-flight.
- Service yang dibangun tanpa `-tags vips` memakai pipeline pure-Go (~9x lebih
  lambat per transform); sekarang startup menulis WARN, bukan diam saja.

## Anti-regresi setelah hardening

Diukur ulang setelah uji dimensi (`MaxSide`/`MaxPixels` dari `DecodeConfig`) dan
pemindahan config celeris ke `celerisConfig()`:

| Jalur | rps | p50 | catatan |
| --- | --- | --- | --- |
| HIT 17.751 B (celeris `io_uring`, 8 keep-alive) | **80.529** | 80 µs | baseline sebelumnya 79.206 → tidak ada regresi |
| MISS `w_400,c_fit` (libvips, 1 koneksi, concurrency 1) | 73 | 10,35 ms | `transform_ns_total` 9,0 ms/gambar → shrink-on-load masih aktif |

Guard dimensi tidak menambah biaya: ia hanya membaca IHDR
(`image.DecodeConfig`) sebelum decode, dan pada jalur vips dimensi datang dari
header yang memang sudah dibaca lazy.

## Biaya `ra_*`, `mb_` (diukur, 1 koneksi, MISS, 2400×1600 → w_400 JPEG)

| Ops | rps | p50 | catatan |
| --- | --- | --- | --- |
| `w_400,c_fit,f_jpg,q_80` (default lanczos3) | 73 | 10,9 ms | shrink-on-load |
| `…,mb_20000` | 67 | 11,5 ms | +5%: 1–2 export tambahan untuk cari `q` |
| `…,ra_linear` | 44 | 20,2 ms | keluar dari shrink-on-load |
| `…,ra_nearest` | 38 | 25,1 ms | keluar dari shrink-on-load |

`vips_thumbnail` tidak menerima `kernel`, hanya option string untuk *loader*.
Jadi `ra_*` selain `lanczos3` sengaja jatuh ke jalur eksplisit (decode penuh +
`ResizeWithVScale(kernel)`), dan biayanya ~2× lebih lambat pada MISS. Rekomendasi:
biarkan default `lanczos3`; pakai `ra_nearest` hanya kalau resize memang jadi
bottleneck CPU (mis. upscale besar), bukan untuk menghemat waktu.

`mb_` aman untuk hot path: ia hanya berjalan saat MISS, deterministik, dan
hasilnya ikut cache key (`mb_<n>` ada di canonical), jadi ETag tetap stabil.
