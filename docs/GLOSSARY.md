# Glossary — ioimg

Kamus istilah yang dipakai di seluruh dokumen. Urut abjad per kategori.

Format tiap entri: **Istilah** — definisi singkat + relevansi di ioimg.

---

## 1. Produk & domain

| Istilah | Definisi |
| --- | --- |
| **Image proxy** | Service perantara yang mengambil gambar dari origin, mentransformasi, lalu menyajikannya ke client. Menyembunyikan origin, menormalkan ukuran/format, dan menyediakan cache. |
| **Origin** | Sumber gambar asli (S3, CMS, CDN pelanggan). ioimg tidak menyimpan master asset di v1. |
| **Transformasi** | Operasi pada gambar: resize, crop, format convert, quality, blur, rotate, watermark. |
| **Varian (variant)** | Kombinasi unik (source + ops + format) yang menghasilkan satu byte stream, disimpan sebagai satu cache entry. |
| **Preset** | Alias bernama untuk sekumpulan parameter, mis. `t_thumb` → `w=320,h=320,c_fill,f_auto,q_auto`. |
| **Immutable URL** | URL yang bila berubah menghasilkan resource berbeda, sehingga aman di-cache `immutable` 1 tahun. Karena itu ops disimpan di path, bukan header. |
| **Allowlist** | Daftar host/skema/port yang boleh di-fetch untuk sebuah project. Default-deny. |
| **Project (tenant)** | Unit isolasi konfigurasi, kredensial, kuota, dan cache namespace. |
| **Organization** | Grup berisi banyak project, pemilik billing & anggota tim. |
| **API key** | Kredensial untuk admin API/purge; disimpan sebagai hash (Argon2id). |
| **Signing key / salt** | Secret per project untuk HMAC signed URL. Dapat dirotasi dengan `key_id`. |
| **Signed URL** | URL yang menyertakan signature HMAC sehingga parameter tidak bisa diubah dan akses bisa dibatasi waktu. |
| **Cache key** | String deterministik yang mengidentifikasi satu varian. Lihat `ARCHITECTURE.md §5`. |
| **Cache hit / miss / bypass** | Objek ditemukan di cache / tidak ditemukan (perlu fetch+transform) / sengaja dilewati (e.g. `no-cache`). |
| **Single-flight / coalescing** | Mekanisme agar N request identik hanya memicu satu transformasi; sisanya menunggu hasil yang sama. |
| **Purge / invalidation** | Menghapus entry cache secara eksplisit (by URL, prefix, project). |
| **Warm / prefetch** | Mengisi cache lebih awal (batch API) agar request user pertama sudah hit. |
| **Serve stale** | Menyajikan entry kedaluwarsa sementara revalidasi berjalan atau saat origin error (`stale-if-error`). |
| **Byte reduction** | Persentase pengurangan ukuran byte hasil ioimg vs file origin. Metrik nilai utama. |
| **Quota** | Batas pemakaian per periode (jumlah transformasi, byte egress, jumlah varian). |
| **Rate limit** | Batas laju request (RPS/burst) per key/IP/project. |
| **Control plane** | Bagian sistem yang mengelola konfigurasi, auth, billing, admin API (tidak di hot path). |
| **Data plane** | Hot path serving gambar: fetch, transform, cache I/O, response. |

## 2. HTTP & delivery

| Istilah | Definisi |
| --- | --- |
| **CDN** | Jaringan edge caching di depan ioimg (Cloudflare/Fastly/Bunny) atau ioimg sendiri sebagai origin-shield. |
| **Edge** | Titik terdekat dengan user tempat cache dibaca. |
| **Origin shield** | Lapisan cache tunggal di depan origin untuk mengurangi request berulang. |
| **TTFB** | Time To First Byte — waktu sampai byte pertama response. |
| **`Cache-Control`** | Header yang mengatur cacheability dan umur (`max-age`, `s-maxage`, `immutable`, `stale-while-revalidate`). |
| **`ETag`** | Identifier versi resource. Dipakai untuk `If-None-Match` → `304 Not Modified`. |
| **`Vary: Accept`** | Memberi tahu cache bahwa response berbeda menurut `Accept` (karena `f_auto` memilih AVIF/WebP/JPEG). |
| **Content negotiation** | Pemilihan format output berdasarkan header `Accept` client. |
| **`304 Not Modified`** | Response tanpa body saat resource belum berubah. |
| **Range request** | Permintaan sebagian byte; didukung untuk objek besar. |
| **Keep-alive / pipelining** | Reuse koneksi TCP untuk banyak request; mempengaruhi throughput nyata. |
| **`srcset` / `sizes`** | Atribut HTML untuk responsif image, dipetakan ke parameter `w`/`dpr`. |
| **LCP** | Largest Contentful Paint — metrik Core Web Vitals yang paling dipengaruhi ukuran gambar. |
| **Zero-copy** | Pengiriman data tanpa menyalin buffer antar user-space/kernel, mis. `splice`, `sendfile`, `MSG_ZEROCOPY`. |

## 3. Image processing

| Istilah | Definisi |
| --- | --- |
| **Pixel / megapixel (MP)** | Satuan resolusi. Batas `40 MP` melindungi dari decompression bomb. |
| **Decode / Encode** | Mengubah byte terkompresi → raster piksel, dan sebaliknya. Biasanya fase paling mahal CPU. |
| **Downscale / Upscale** | Memperkecil/memperbesar gambar. Upscale tidak dianjurkan (kualitas turun) kecuali diminta. |
| **Resampling filter** | Algoritma interpolasi: nearest, bilinear, bicubic, Lanczos3. Default `Lanczos3` untuk kualitas; `bilinear` untuk kecepatan. |
| **Crop / fit / fill / pad** | Bagaimana rasio aspek dipenuhi: potong, muat dalam kotak, penuhi kotak (potong), atau tambah padding. |
| **Gravity** | Titik acuan crop: `north`, `south`, `east`, `west`, `center`, `face`, `auto` (saliency). |
| **DPR** | Device Pixel Ratio; `dpr=2` menghasilkan 2× dimensi CSS. |
| **AVIF** | Format berbasis AV1; kompresi terbaik untuk foto (30–50% lebih kecil dari JPEG) tetapi encode mahal. |
| **WebP** | Format Google; lossy & lossless & animasi; didukung luas, encode cepat. |
| **JPEG / mozjpeg** | Format lossy klasik; `mozjpeg` memberi file 5–10% lebih kecil. |
| **PNG / oxipng** | Lossless; optimasi via `oxipng`/`zopfli`. |
| **HEIC/HEIF** | Format Apple; dukungan decode opsional (paten lisensi → bukan P0). |
| **Progressive JPEG** | JPEG yang ter-render bertahap; berguna untuk koneksi lambat. |
| **Chroma subsampling** | 4:4:4 / 4:2:0; mempengaruhi ukuran & kualitas warna. |
| **Quality (q)** | Parameter kompresi lossy. Default terkalibrasi per format. |
| **Butteraugli / SSIM / VMAF** | Metrik kualitas persepsi untuk memvalidasi bahwa penurunan ukuran tidak merusak tampilan. |
| **EXIF / GPS** | Metadata kamera & lokasi. Di-strip secara default (privasi), kecuali `keep_meta`. |
| **Auto-orient** | Menerapkan rotasi dari tag orientasi EXIF sebelum transform. |
| **Blurhash / LQIP** | Placeholder blur base64 sangat kecil untuk loading state. |
| **Decompression bomb** | Gambar kecil terkompresi yang mengembang jadi piksel masif; dicegah via batas dimensi/pixel. |
| **Sanitize SVG** | Menghapus script, event handler, dan referensi eksternal dari SVG sebelum disajikan/rasterisasi. |

## 4. Rust & arsitektur

| Istilah | Definisi |
| --- | --- |
| **Rust** | Bahasa sistem dengan jaminan memory safety tanpa GC; dasar ioimg. |
| **`axum`** | Web framework Rust di atas `hyper`/`tower`; dipakai untuk API publik & admin. |
| **`hyper`** | HTTP/1.1 & HTTP/2 implementation Rust; lapisan yang menangani koneksi. |
| **`tokio`** | Async runtime Rust berbasis epoll/kqueue/IOCP (bukan io_uring secara default). |
| **`tower`** | Abstraksi middleware/service (timeout, rate limit, load shed) untuk `axum`. |
| **`tracing`** | Structured logging + span untuk tracing terdistribusi. |
| **`serde`** | Serialisasi/deserialisasi (JSON, TOML) — parsing ops & config. |
| **Crate workspace** | Satu repo berisi banyak crate (`ioimg-core`, `ioimg-http`, dst.) dengan dependency graph bersama. |
| **Feature flag (Cargo)** | Kompilasi kondisional, mis. `--features io-uring`. |
| **Trait `IoBackend`** | Abstraksi ioimg untuk operasi I/O cache (`read_at`, `write_at`, `send_body`) agar backend bisa ditukar. |
| **`libvips`** | Library pemrosesan gambar streaming, hemat memori, sangat cepat untuk resize. |
| **`sharp`** | Binding Node ke libvips; pembanding performa (bukan dependency ioimg). |
| **`image` / `zune-jpeg`** | Crate Rust decoder/encoder; `zune-jpeg` jauh lebih cepat untuk JPEG baseline. |
| **`ravif` / `rav1e`** | Encoder AVIF Rust (rav1e-based). |
| **`libwebp` (via bindings)** | Encoder WebP; alternatif pure-Rust `webp-encoder` masih terbatas. |
| **`fast_image_resize`** | Resizer SIMD Rust (AVX2/NEON) — pilihan utama untuk downscale. |

## 5. io_uring & sistem

| Istilah | Definisi |
| --- | --- |
| **`io_uring`** | Antarmuka I/O asynchronous Linux (kernel ≥ 5.1) berbasis dua ring di shared memory: submission queue & completion queue. Memungkinkan banyak operasi I/O per syscall, termasuk buffered & direct I/O. |
| **SQ / CQ (submission/completion queue)** | Ring tempat user-space menaruh permintaan (SQE) dan kernel menaruh hasil (CQE). |
| **SQE / CQE** | Submission Queue Entry / Completion Queue Entry — satu entri per operasi. |
| **`io_uring_setup` / `io_uring_enter` / `io_uring_register`** | Tiga syscall io_uring. Wajib diizinkan seccomp; profil default Docker/K8s **memblokir** `io_uring_enter`. |
| **SQPOLL** | Kernel thread mem-poll SQ sehingga submit tidak butuh syscall; butuh privilege (`CAP_SYS_ADMIN` atau `IORING_SETUP_SQPOLL` dengan `SQ_AFF`). |
| **IORING_SETUP_IOPOLL** | Polling mode untuk `O_DIRECT` (busy-wait); latensi rendah, CPU tinggi. |
| **Registered buffers** | Buffer user-space yang di-pin di kernel (`io_uring_register`), menghilangkan biaya pin/unpin per operasi. |
| **Registered files** | File descriptor yang didaftarkan sehingga tidak perlu melewati fget/fput per operasi. |
| **Fixed file/buffer index** | Referensi ke fd/buffer terdaftar; satu SQE ringan. |
| **`O_DIRECT`** | Bypass page cache; menghindari double-buffering untuk cache NVMe, tapi butuh alignment (512/4096) & ukuran kelipatan. |
| **`splice` / `sendfile`** | Memindahkan data antar fd di kernel tanpa copy ke user-space; ideal untuk serve file cache. |
| **epoll** | Mekanisme readiness Linux yang dipakai `tokio`; alternatif model terhadap io_uring (readiness vs completion). |
| **Readiness vs completion** | epoll memberi tahu "siap", io_uring memberi tahu "selesai" — perbedaan model mendasar. |
| **Proactor** | Model I/O di mana operasi diserahkan ke kernel dan hasil dilaporkan saat selesai (io_uring, IOCP). Kebalikan dari reactor/readiness (epoll). |
| **Fusion driver** | Mekanisme `compio` yang memilih driver saat runtime (io_uring vs polling) bila kedua feature build diaktifkan; bisa dipaksa lewat `ProactorBuilder`. |
| **`ProactorBuilder`** | API `compio` untuk mengonfigurasi proactor: pilih driver, ukuran ring, opsi io_uring. |
| **`SendWrapper`** | Pembungkus yang menembus requirement `Send`; dipakai `cyper-core` agar tipe `hyper` bisa hidup di runtime single-thread. Bila nilainya menyeberang thread → panic, bukan error kompilasi. |
| **`IoBuf` / `IoBufMut`** | Trait buffer `compio-buf`: buffer **dimiliki** dan dipindahkan ke operasi I/O, lalu dikembalikan lewat `BufResult`. Kebalikan dari `&mut [u8]` di tokio. |
| **`BufResult<T, B>`** | Hasil operasi I/O yang membawa kembali buffer. Tidak bisa dipakai dengan `?` di stable → makro `buf_try!`. |
| **`Dispatcher`** | Pengelola banyak runtime `compio` lintas thread (satu runtime per thread) dengan thread pool bersama; dipakai untuk server multi-worker. |
| **Driver `polling` (batasan)** | Tidak mendukung file I/O asynchronous: operasi file di-spawn ke thread pool yang punya batas keras, dan bisa gagal segera saat jenuh. |
| **Driver `polling`** | Fallback non-io_uring di `compio` (berbasis readiness); dipakai bila io_uring diblokir seccomp atau tidak tersedia. |
| **Thread-per-core** | Arsitektur satu thread per CPU core tanpa migrasi task, memaksimalkan cache locality; dipakai `monoio`, `compio`. Konsekuensi: task `!Send` dan tidak ada `tokio::spawn`. |
| **`compio`** | Runtime Rust thread-per-core dengan driver **io_uring** (fallback IOCP/polling). Proactor model: operasi disubmit ke kernel, hasil datang sebagai completion. |
| **`cyper`** | HTTP client ala `reqwest` di atas `compio` + `hyper`; pendukung utama jalur fast path. |
| **`cyper-axum`** | Adaptor yang menyediakan `serve()` sebagai pengganti `axum::serve`; menjalankan `axum::Router` di atas `compio`/io_uring. Inilah yang membuat "axum + io_uring" mungkin. |
| **`monoio`** | Runtime Rust thread-per-core berbasis io_uring (fallback epoll). Tidak kompatibel langsung dengan `tokio`/`hyper`. |
| **`compio`** | Runtime Rust completion-based (io_uring) dengan dukungan io_uring lebih luas; alternatif `monoio`. |
| **`tokio-uring`** | Eksperimen runtime io_uring di ekosistem tokio; API terpisah dan belum matang → **tidak** dipakai sebagai dasar HTTP stack. |
| **seccomp** | Filter syscall Linux; menentukan apakah io_uring boleh dipakai di container. |
| **`RuntimeDefault`** | Profil seccomp default Kubernetes yang memblokir io_uring. |
| **Hugepages** | Alokasi memori halaman besar untuk mengurangi TLB miss pada buffer cache. |
| **NUMA** | Non-Uniform Memory Access; pinning thread & buffer ke NUMA node mengurangi latensi. |
| **CPU pinning / `SO_REUSEPORT`** | Teknik distribusi koneksi ke banyak core tanpa lock. |
| **kTLS** | TLS di kernel; menghindari salinan user-space saat enkripsi (eksperimental di ioimg). |

## 6. Keamanan

| Istilah | Definisi |
| --- | --- |
| **SSRF** | Server-Side Request Forgery — penyerang memaksa server fetch URL internal. Dicegah allowlist + IP filtering + DNS pinning. |
| **Open proxy** | Proxy yang menerima URL sembarang; ioimg tidak boleh menjadi open proxy (default deny). |
| **HMAC-SHA256** | Konstruksi tanda tangan; dipakai untuk signed URL. |
| **Base64url** | Encoding tanpa `+`/`/` agar aman di path URL. |
| **Token bucket** | Algoritma rate limiting (kapasitas + refill rate). |
| **Argon2id** | KDF untuk hash API key/password. |
| **Open redirect** | Redirect ke domain tak terduga; ioimg membatasi redirect ke ≤ 3 dan memvalidasi ulang allowlist. |
| **Supply chain** | Risiko dependency (CVE, typosquatting); dimitigasi `cargo-deny`, `cargo-audit`, lockfile. |
| **Fuzzing** | Pengujian otomatis dengan input acak pada parser/decoder (cargo-fuzz). |

## 7. Operasional

| Istilah | Definisi |
| --- | --- |
| **SLO / SLI** | Service Level Objective (target) / Indicator (pengukuran aktual). |
| **Error budget** | Toleransi kegagalan yang diturunkan dari SLO (1 − SLO). |
| **p50 / p95 / p99** | Persentil latensi. p95 = 95% request lebih cepat dari nilai ini. |
| **RED metrics** | Rate, Errors, Duration — tiga metrik dasar service. |
| **USE metrics** | Utilization, Saturation, Errors — untuk resource (disk, CPU, ring). |
| **Cardinality** | Jumlah kombinasi label metrik; harus dibatasi (jangan label per URL). |
| **Runbook** | Prosedur operasional saat alert/insiden. |
| **Blue-green / canary** | Strategi deploy bertahap dengan rollback cepat. |
| **Capacity planning** | Perhitungan CPU/bandwidth/disk dari profil trafik. |
| **Distroless image** | Container image tanpa shell/package manager untuk permukaan serangan minimal. |
