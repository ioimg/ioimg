# imgproxy Feature Parity — ioimg

Perbandingan langsung dengan [imgproxy features](https://imgproxy.net/features/).
Dokumen ini menjawab satu pertanyaan: **seberapa jauh ioimg bisa mengimbangi
imgproxy, dan apa yang harus ditambah?**

Legenda: ✅ setara · 🟡 sebagian · ⛔ belum ada · 🔒 sengaja tidak didukung

Status di bawah mencerminkan kode di [`service/`](../service/README.md), bukan
rencana. Item 🟡/⛔ adalah backlog, bukan klaim marketing.

---

## 1. Ringkasan

| Area | Status ioimg | Catatan |
| --- | --- | --- |
| Core resize & crop | ✅ | `fit/fill/force/pad/crop/scale`, gravity 9 arah, DPR |
| Format web modern | ✅ | JPEG/PNG/WebP/AVIF/GIF + `f_auto` |
| Format input luas | 🟡 | +BMP/TIFF; belum JXL/HEIC/ICO/PSD/PDF/video |
| Filter dasar | ✅ | blur, sharpen, pixelate, brightness, contrast, saturation, monochrome, trim |
| Watermark | ⛔ | image/text/custom/shadow/rotate belum ada |
| Transform lanjutan | 🟡 | duotone, colorize, gradient ✅ (ketiganya **Pro** imgproxy); chained pipeline belum |
| Smart/AI | ⛔ | smart crop, face crop, object crop/blur, autoquality DSSIM |
| Keamanan URL | 🟡 | signing + expiry + allowlist + SSRF; belum enkripsi source/hashsum/auth header |
| Sumber gambar | 🟡 | HTTP(S) saja; S3/GCS/Azure/Swift sebagai *cache*, bukan source |
| Observability | 🟡 | Prometheus/metrics/health; belum OTel/tracing vendor |
| Integrasi CDN & cache | ✅ | L1+L2, purge fan-out, ETag, warm/batch |
| Media pipeline | ⛔ | GIF→MP4, PDF/video/PSD thumbnail |

**Kesimpulan jujur:** ioimg bisa mengimbangi **inti OSS imgproxy** untuk
resize/filter/format web, keamanan URL, cache, dan operasi. ioimg **belum bisa**
mengimbangi seluruh halaman fitur imgproxy hari ini, terutama codec eksotis,
watermark, chained pipeline, smart/AI crop, dan media (PDF/video/PSD). Sebagian
besar sisanya bisa ditambah bertahap; beberapa hanya realistis di atas libvips
atau service terpisah.

### 1.1 Mana yang OSS, mana yang Pro (diverifikasi 2026-11-25)

imgproxy menandai opsi berbayar dengan badge **Pro** di
[docs.imgproxy.net/usage/processing](https://docs.imgproxy.net/usage/processing).
Diverifikasi langsung di halaman itu, bukan dari ingatan:

- **Pro**: resizing algorithm (`ra`), adjust/brightness/contrast/saturation/
  monochrome (`bri`/`con`/`sat`/`mono`), unsharp masking (`sharp`), duotone
  (`dt`), colorize (`col`), gradient, watermark (URL/text/size/rotate/shadow),
  style, DPI, color profile, autoquality, **JPEG/PNG/WebP/AVIF options**,
  page/pages, disable animation, video thumbnail, fallback image, hashsum,
  AES-CBC, best format, crop aspect ratio, object/blur detections.
- **OSS**: `max_bytes`, `q`, auto-rotate, strip metadata, cache buster, zoom,
  extend, preset, trim, min/max width & height, resize type, enlarge, rotate,
  flip, padding, background, gravity, format, `dpr`, max src resolution, max
  result dimension.

Konsekuensinya untuk dokumen ini:

1. Beberapa opsi yang **sudah** ioimg implementasi adalah fitur **Pro** imgproxy:
   `ra`, `bri`, `con`, `sat`, `mono`, `sharp`, `dt`, `col`. Delapan fitur
   berbayar sudah tertutup — bukan "parity OSS", tapi melebihi OSS.
2. Item yang tersisa di daftar Pro (watermark, gradient, autoquality DSSIM,
   option codec per-format, DPI, color profile) adalah **pembeda berbayar**,
   bukan sekadar backlog parity. Prioritasnya keputusan produk, bukan teknis.
3. Harga resmi Pro (halaman pricing, 2026-11-25): **$49/bulan** atau
   **$499/tahun** untuk **16 worker**; di atas itu "price on request".
   Worker = total worker di semua instance (default 2× CPU, `IMGPROXY_WORKERS`).
   Artinya plafon harga referensi untuk paket "Pro-equivalent" kita ada di
   sekitar $50/bulan per 16 worker — cukup rendah sehingga menjual fitur Pro
   secara terpisah sulit; yang dijual adalah kemudahan operasional.

---

## 2. Matriks lengkap

### 2.1 Format gambar

| Fitur imgproxy | ioimg | Rencana |
| --- | --- | --- |
| JPEG, PNG, WebP, AVIF | ✅ | — |
| GIF (statis) | ✅ | — |
| BMP | ✅ | input, baru |
| TIFF | ✅ | input, baru |
| Animated GIF/WebP | 🟡 | hanya frame pertama; output animasi belum |
| JPEG XL | ⛔ | butuh binding libjxl; prioritas rendah |
| SVG | 🔒 | sengaja ditolak (attack surface); sanitization belum |
| ICO | ⛔ | decoder mudah; prioritas rendah |
| HEIC/HEIF | ⛔ | butuh libheif; biasanya Pro |
| PSD/PSB | ⛔ | butuh ImageMagick/libvips + worker |
| PDF thumbnail | ⛔ | butuh PDFium/Poppler + worker |
| Video thumbnail | ⛔ | butuh ffmpeg + worker |
| GIF → MP4 | ⛔ | butuh ffmpeg + worker |

### 2.2 Image processing

| Fitur imgproxy | ioimg | Rencana |
| --- | --- | --- |
| Resizing | ✅ | `w`, `h`, `c_*` |
| Resize dengan algoritma pilihan | ✅ | `ra_nearest/linear/cubic/lanczos2/lanczos3`; vips pakai kernel native, pure Go memetakan Lanczos→Catmull-Rom |
| Crop & trim | ✅ | `c_crop`, `trim` |
| Padding | ✅ | `c_pad`, `bg` |
| Min width / min height | ✅ | `mw`, `mh`: hasil lebih kecil diperbesar, rasio tetap; boleh tanpa `w`/`h` |
| Extending | 🟡 | via `c_pad`; belum op `extend` terpisah |
| Rotating | 🟡 | 90/180/270 + auto EXIF (`ar_0` mematikan auto-rotate); belum sudut arbitrer |
| Flattening | 🟡 | otomatis untuk JPEG & pad; belum op flatten independen |
| Flatten background semi-transparan | ⛔ | `bg` hanya `RRGGBB` |
| Watermark image/text/custom | ⛔ | perlu compositing + font; kandidat M3 |
| Watermark shadow/rotate | ⛔ | ikut watermark |
| Blur, sharpen, pixelate | ✅ | `blur`, `sharp`, `pixel`/`px` |
| Unsharp masking | ✅ | `sharp` (approx.) |
| Brightness/contrast/saturation | ✅ | `bri`, `con`, `sat`, baru |
| Monochrome | ✅ | `mono`, baru |
| Duotone | ✅ | `dt_intensity:color1:color2`, baru (Pro di imgproxy); 0 = mati |
| Colorize | ✅ | `col_opacity:color:keep_alpha`, baru (Pro di imgproxy) |
| Gradient overlay | ✅ | `gr_opacity:color:direction:start:stop`, baru (Pro di imgproxy) |
| SVG CSS injection | 🔒 | SVG ditolak |
| Chained pipelines | ⛔ | butuh parser pipeline; perubahan besar pada cache key |

### 2.3 Optimasi

| Fitur imgproxy | ioimg | Rencana |
| --- | --- | --- |
| Quality output | ✅ | `q` |
| Auto WebP/AVIF | ✅ | `f_auto` via `Accept` |
| Auto JPEG XL | ⛔ | menunggu codec |
| Auto PNG quantization | 🟡 | `palette_1` manual; belum otomatis |
| Strip metadata | ✅ | default; `sm_` alias `strip_` (nama imgproxy). Belum ada mode keep-metadata |
| Keep copyright saat strip | ⛔ | perlu metadata writer |
| Strip ICC / konversi sRGB | ✅ | encoder Go/vips membuang profil |
| Advanced JPEG (mozjpeg) | ⛔ | butuh encoder mozjpeg/libvips (Pro di imgproxy) |
| Advanced WebP options | ✅ | `webpo_compression` (method 0..6) di kedua backend; `smart_subsample`/`preset` belum |
| Advanced PNG options | 🟡 | `pngo_interlaced` (vips saja), `pngo_quantize`/`quantization_colors` (pure Go saja) |
| Advanced AVIF options | 🟡 | `avifo_subsample` 4:4:4/4:2:2/4:2:0 (pure Go saja; govips tidak meneruskan) |
| SVG minification | 🔒 | SVG ditolak |

### 2.4 Smart features

| Fitur imgproxy | ioimg | Rencana |
| --- | --- | --- |
| Smart crop (`g_auto`) | 🟡 | vips: attention; pure Go: fallback center |
| Advanced smart crop + face | ⛔ | butuh face/feature detector |
| Object-oriented crop (AI) | ⛔ | butuh model/worker |
| Object blurring (AI) | ⛔ | butuh model/worker |
| Autoquality by file size | ✅ | `mb_<bytes>`: binary search `q` 20..q0, maks 5 probe, deterministik; tidak pernah gagal (kembalikan terkecil) |
| Autoquality by DSSIM | ⛔ | butuh metrik DSSIM |
| Automatic best format | 🟡 | `f_auto` memilih berdasarkan `Accept`, bukan rasio kompresi |

### 2.5 Keamanan

| Fitur imgproxy | ioimg | Rencana |
| --- | --- | --- |
| URL signing | ✅ | HMAC + rotasi key |
| Source URL encryption | ⛔ | AES-GCM + key terpisah |
| Authorization header | ⛔ | perlu mode auth upstream |
| Resolution restriction | ✅ | 40 MP / 8192 px |
| Source file size restriction | ✅ | `IOIMG_SOURCE_MAX_BYTES` |
| Animation frame restriction | ⛔ | decode animasi belum penuh |
| Allowed source restriction | ✅ | allowlist + SSRF guard |
| Source hashsum verification | ⛔ | parameter `hs_` + verifikasi |
| URL expiration | ✅ | `exp` |
| Response filename | ✅ | `fn_` → `Content-Disposition`; signed, tapi tidak masuk cache key |
| Cache buster | ✅ | `cb_`; signed, tidak masuk cache key |
| SVG sanitization | 🔒 | SVG tidak diterima |

### 2.6 Sumber gambar

| Fitur imgproxy | ioimg | Rencana |
| --- | --- | --- |
| HTTP(S) | ✅ | fetcher + SSRF guard |
| Local files | 🔒 | hindari path traversal; bisa via sidecar |
| Amazon S3 / R2 / MinIO | 🟡 | dipakai untuk **cache L2**, bukan source |
| Google Cloud Storage | ⛔ | adaptor `StorageBackend` (M2) |
| Azure Blob / Swift | ⛔ | adaptor (M2) |

### 2.7 Monitoring & error reporting

| Fitur imgproxy | ioimg | Rencana |
| --- | --- | --- |
| Prometheus | ✅ | `/metrics` |
| OpenTelemetry tracing | ⛔ | kandidat M2/M3 |
| New Relic/Datadog/CloudWatch | ⛔ | via OTel collector |
| Sentry/Honeybadger/Bugsnag/Airbrake | ⛔ | hook logger; prioritas rendah |

### 2.8 Miscellaneous

| Fitur imgproxy | ioimg | Rencana |
| --- | --- | --- |
| Image info | 🟡 | `f_json` (format, dimensi, alpha, animasi, warna dominan) |
| Presets | ✅ | `IOIMG_PRESETS`, `t_*` |
| Fallback image | 🟡 | hanya redirect via `IOIMG_FALLBACK_REDIRECT`; belum serve fallback image |
| Per-request fallback | ⛔ | parameter URL |
| Skip processing | ⛔ | pass-through berdasarkan format sumber |
| Raw file streaming | ⛔ | endpoint raw tanpa decode |
| ETag | ✅ | kuat, berbasis cache key |
| Cookie pass-through | ⛔ | perlu allowlist eksplisit |
| Custom source request headers | ⛔ | config per-host |
| Request headers pass-through | ⛔ | allowlist eksplisit |
| Custom response headers | ⛔ | config |
| Response headers pass-through | ⛔ | allowlist eksplisit |

---

## 3. Prioritas penambahan

### Parity pack 1 — sudah ditambahkan di repo ini

- Input BMP & TIFF (decode-only).
- `bri` / `con` / `sat` (brightness, contrast, saturation).
- `pixel` / `px` (pixelate).
- `mono` (grayscale).
- Semua masuk canonical URL + cache key, jadi aman untuk signed URL.

### Parity pack 2 — sudah ditambahkan di repo ini

- `mw` / `mh` (minimum width/height) dan `ar_0` (auto-rotate off), `sm` alias `strip`.
- `dt` (duotone), `col` (colorize), `gr` (gradient), `pngo`/`webpo`/`avifo`
  (opsi encoder per format) — semuanya **Pro** di imgproxy.

### Parity pack 3 — quick wins berikutnya (1–2 sprint)

1. **Fallback image**: `IOIMG_FALLBACK_IMAGE` + override per request.
2. **Custom source headers**: `IOIMG_SOURCE_HEADERS` per host.
3. **Skip processing / raw streaming**: `raw_1` atau endpoint `/raw/...`.
4. **Option codec per-format** (`pngo`/`webpo`/`avifo`) — Pro di imgproxy.
5. ~~**Gradient** (`gr_`)~~ — selesai (Pro di imgproxy).
6. **Auto palette PNG**: aktifkan kuantisasi jika ukuran turun.
7. **Animation frame limit**: batas jumlah frame + fallback statis.
8. **Source hashsum**: `hs_<sha256>` untuk origin yang tidak ingin di-fetch ulang.

### Parity pack 4 — butuh pekerjaan lebih besar

1. **Watermark** image/text + shadow/rotate (compositing, font, cache key).
2. **Chained pipelines** (parser baru; cache key harus mencakup urutan).
3. **Autoquality by size/DSSIM** (loop encode; CPU cost).
4. **Smart crop/face** (detektor; vips attention sudah sebagian).
5. **Source URL encryption** + auth header mode.
6. **OpenTelemetry** tracing.

### Parity pack 5 — keputusan produk

1. **PDF/video/PSD/GIF→MP4**: worker terpisah (ffmpeg/Poppler), bukan data plane Go murni.
2. **AI object crop/blur**: model + GPU; jangan dipaksa di request path.
3. **JPEG XL/HEIC/ICO**: tambah codec native; ukur demand dulu.
4. **S3/GCS/Azure source**: adaptor `StorageBackend`, bukan fetch HTTP.
5. **SVG**: hanya jika sanitizer matang; default tetap tolak.

---

## 4. Yang tidak perlu dikejar

- Menjadi clone fitur-per-fitur imgproxy. Keunggulan ioimg ada di **DX,
  observability, cache/purge, dan integrasi**, bukan di jumlah parameter.
- AI/object detection di request path. Itu mengubah profil latency dan biaya.
- SVG tanpa sanitizer. Risiko XSS/SSRF lebih besar daripada nilainya.

Kalau targetnya "bisa menggantikan imgproxy untuk 80% kasus web image", urutan
yang benar adalah **pack 2 → pack 3 watermark/chained → pack 4 media**. Format
eksotis dan AI sebaiknya menunggu permintaan nyata, bukan dikejar untuk parity
di atas kertas.
