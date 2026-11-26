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

## Biaya `dt_` / `col_` (diukur, 1 koneksi, MISS, 2400×1600 → w_400 JPEG)

| Ops | rps | p50 |
| --- | --- | --- |
| `w_400,f_jpg,q_80` (baseline) | 82 | 10,4 ms |
| `…,dt_0.6:1a1a2e:e94560` | 47 | 18,2 ms |
| `…,col_0.4:ff8800:0` | 47 | 18,1 ms |
| `…,dt_…,col_…` | 45 | 19,0 ms |

HIT tidak terpengaruh sama sekali (80,5k rps, p50 78 µs — di dalam noise).

Di backend vips, duotone/colorize berjalan lewat satu round-trip
`ToImage` → efek Go → `NewImageFromGoImage`, karena govips tidak punya op
per-piksel yang murah. Biayanya ~75% dari MISS: 10,4 → 18,2 ms. Dua opsi:

1. **Biarkan** — hasilnya di-cache, jadi biaya ini hanya untuk request pertama
   per (URL, source) dan hanya untuk URL yang memang meminta efek.
2. **vips-native** (belum dikerjakan): colorize = `Linear` per band
   (`a=1-opacity`, `b=opacity×color`) + `BandJoin` untuk alpha; duotone =
   `Colourspace(BW)` → `Linear` → `BandJoin` → `Composite` dengan bobot
   intensity. Menghilangkan round-trip, tapi menambah ~6 op vips dan risiko
   perbedaan hasil antar-backend; untungnya ada test paritas yang bisa
   menahannya.

Keputusan: mulai dari (1). Efek ini Pro di imgproxy dan bukan hot path; kalau
ada permintaan volume besar untuk efek ini, baru kerjakan (2).

### Gradient dan opsi encoder per format

Sesi terpisah, build `-tags vips`, 1 thread, MISS, sumber 2400×1600 → `w_400`:

| Opsi | rps | p50 |
| --- | --- | --- |
| baseline `f_jpg,q_80` | 70 | 10,7 ms |
| `gr_0.5:000000:down` | 47 | 18,4 ms |
| `dt_…` / `col_…` (pembanding) | 47 | 18,1–18,4 ms |
| `f_webp,q_80` (method default 4) | 50 | 17,6 ms |
| `f_webp,q_80,webpo_6` | 38 | 23,3 ms |
| `f_avif,q_50` | 7 | 142 ms |
| `f_avif,q_50,avifo_0` (4:4:4) | 7 | 140 ms |
| `f_png` | 18 | 52,6 ms |
| `f_png,pngo_1:1:64` | 21 | 45,7 ms |

- `gr` memakai round-trip vips yang sama dengan `dt`/`col`, jadi biayanya
  identik (~+75% waktu MISS). HIT tidak terpengaruh (80,5k rps, p50 78 µs).
- `webpo_compression` benar-benar sampai ke encoder: method 6 = −24% throughput
  dibanding default 4, dengan output lebih kecil.
- `avifo_subsample` **tidak** terlihat di vips (govips tidak meneruskan
  subsample AVIF) — 7 rps pada keduanya, sesuai tabel dukungan di
  `docs/API.md`. Biaya AVIF didominasi encoder (~140 ms/gambar), bukan opsi.
- `pngo` di vips hanya menghormati `interlaced`; `quantization_colors` diabaikan
  (pure Go yang mendukungnya). Selisih 18 → 21 rps di sini adalah noise +
  interlace, bukan kuantisasi.

Kesimpulan: opsi encoder adalah keputusan kualitas/ukuran, bukan keputusan
throughput; yang mahal tetap format (AVIF 7 rps, PNG 18–21 rps vs JPEG 70 rps
pada `w_400`). Cache yang menyelamatkan, bukan kecepatan encode.

## Watermark (`wm_`/`wmu_`)

Satu gambar 2400×1600 → `w_400,f_jpg,q_80`, MISS, 1 koneksi, libvips:

| Kasus | rps | p50 |
| --- | --- | --- |
| tanpa watermark | 76 | 10,3 ms |
| `wm_0.6:soea:10:10:0.2` (watermark 2400 px di-stamp 80 px) | 49 | 17,4 ms |

Biayanya di round-trip vips→Go→vips (jalur yang sama dengan duotone/colorize),
bukan di compositing: `applyWatermark` sendiri 1,7 ms. Perbaikan penting yang
ditemukan saat bench: downscale Catmull-Rom langsung dari 2400 px ke 80 px
memakan **72 ms** (kernel support melebar seiring rasio); pra-shrink bilinear ke
2× target menurunkannya ke **1,7 ms**. HIT tidak terpengaruh (84,8k rps,
p50 80 µs).

## 8. c_fill/c_force satu sisi (2026-11-26)

`w_800,c_fill` sebelumnya **tidak** memakai shrink-on-load: rencana thumbnail
mensyaratkan kedua sisi ada, sehingga satu sisi jatuh ke jalur decode penuh.
Secara semantik satu sisi memang tidak memotong apa pun (setara fit), jadi
sekarang dipetakan ke `vips_thumbnail_buffer` dengan sisi lain tak terbatas.

Mikro-benchmark in-process, sumber JPEG 2400×1600 (853 KB), `-tags vips`,
libvips tanpa cache:

| Opsi | jalur cepat | decode penuh | rasio |
| --- | --- | --- | --- |
| `w_400` (fit) | **10,52 ms** (95 img/s) | 19,41 ms | 1,85× |
| `w_800` (fit) | 17,09 ms | 17,81 ms | 1,04× |
| `w_800,c_fill` | **17,10 ms** | 17,96 ms | 1,05× |
| `w_400,h_400,c_fill` | **11,11 ms** (90 img/s) | 20,56 ms | 1,85× |

Dua hal yang perlu dicatat:

- `c_fill` satu sisi kini **identik** dengan `w_800` (17,10 vs 17,09 ms) — bukti
  jalur cepat benar-benar terpakai, dan HTTP MISS-nya turun dari 21,6 ms ke
  ~17,1 ms (−21%).
- Pada `w_800` keuntungan shrink-on-load hampir hilang (1,04×): JPEG DCT hanya
  memberi faktor 1/2 (2400→1200 px) sebelum resize ke 800, jadi biaya encode
  dan resize mengambil alih. Manfaat terbesar ada di target kecil (`w_400`).

## 9. Biaya efek warna (`dt_`/`col_`/`gr_`)

Angka yang sama, setelah resize:

| Opsi | ms/img | selisih vs `w_400` |
| --- | --- | --- |
| `w_400` | 10,52 | — |
| `w_400,dt_…` | 17,87 | +7,35 |
| `w_400,col_…` | 17,61 | +7,09 |
| `w_400,gr_…` | 18,12 | +7,60 |

Jalur vips sekarang mengirim buffer piksel hasil efek langsung lewat
`vips.NewImageFromMemory` (bukan `NewImageFromGoImage`, yang berjalan dua kali
atas piksel yang sama). Itu menghapus satu lintasan penuh, tetapi **tidak**
menjelaskan +7 ms: pada gambar hasil resize 400 px, satu lintasan piksel hanya
sepersekot dari itu. Biaya sebenarnya belum teridentifikasi — kandidat berikutnya
adalah `ToImage`/`ExportParams{}` dan interprestasi band alpha. Profil CPU
diperlukan sebelum ada klaim lebih jauh.

## 10. Diukur lewat binary hasil build (HTTP), bukan `go test`

Angka in-process (§8/§9) berguna untuk membandingkan jalur kode, tapi bukan
angka yang dikirim ke pengguna: `go test` tidak melewati HTTP, fetch origin,
penulisan cache, atau alokasi per-request. Semua di bawah ini diukur pada
**binary `-tags vips` yang di-build**, 1 koneksi keep-alive, `?i={i}` unik supaya
selalu MISS, cache di ext4, origin `python3 -m http.server` di host yang sama,
8 s per titik. `ms/img` = total wall per request (fetch + decode + resize +
encode + tulis cache).

| Opsi | jalur cepat | `IOIMG_VIPS_THUMBNAIL=0` | rasio |
| --- | --- | --- | --- |
| `w_400` | **15,7 ms** (64 img/s) | 24,3 ms (41 img/s) | **1,55×** |
| `w_400,h_400,c_fill` | **16,8 ms** | 25,9 ms | 1,54× |
| `w_800` | 23,7 ms | 23,7 ms | 1,00× |
| `w_800,c_fill` | 23,5 ms | 23,7 ms | 1,01× |
| `w_400 + dt_` | 23,5 ms | 32,7 ms | 1,39× |
| `w_400 + col_` | 22,7 ms | 33,5 ms | 1,48× |
| `w_400 + gr_` | 24,6 ms | 34,2 ms | 1,39× |

Yang dikonfirmasi lewat binary:

1. **`c_fill` satu sisi sudah setara `w_800` biasa** (23,5 vs 23,7 ms) — dulu
   21,6 vs 14,0 ms karena jatuh ke decode penuh. Perbaikan `thumbnailPlan`
   terverifikasi di jalur produksi, bukan hanya di unit test.
2. **Shrink-on-load = 1,55× end-to-end** pada `w_400`. Pada `w_800` manfaatnya
   hilang (DCT hanya bisa 1/2) — sesuai §8.
3. **Efek warna tetap +7 ms** pada `w_400` (23,5 vs 15,7). Penyebabnya sekarang
   teridentifikasi: `applyColorEffectsVips` memanggil `img.ToImage()`, yang untuk
   JPEG tanpa alpha mengembalikan `*image.YCbCr`, lalu `nrgbaOf()` mengonversi
   seluruh gambar ke NRGBA lewat loop Go (plus alokasi) sebelum efek dijalankan
   dan dikembalikan ke vips. Itu ~3 ms untuk `ToImage` + ~4 ms konversi.
   Perbaikan berikutnya: jalankan efek langsung di domain vips (maplut/LUT untuk
   duotone, composite untuk colorize/gradient) sehingga tidak ada konversi
   ruang warna sama sekali.

Catatan metodologi: `w_800` pada jalur cepat dan lambat sama-sama ~23,7 ms, jadi
perbandingan ini bukan bukti bahwa jalur cepat dipakai untuk `w_800` — yang
membuktikannya `w_400` (1,55×) dan `c_fill` (yang sebelumnya 1,5× lebih lambat).
