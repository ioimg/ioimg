# PRD — ioimg


> **Catatan implementasi (2026-11-25):** stack final adalah **Go** (`service/`),
bukan Rust/axum/io_uring. Lihat [ADR-0008](adr/0008-go-data-plane.md) dan
[service/README.md](../service/README.md). Dokumen ini tetap menjadi sumber
requirements produk; bagian stack/io_uring adalah riwayat desain.

| Field | Value |
| --- | --- |
| Produk | ioimg |
| Versi dokumen | 1.0 (draft) |
| Status | Untuk review |
| Pemilik | Product / Eng |
| Terakhir diperbarui | 2025-11 |

---

## 1. Ringkasan

ioimg adalah **image proxy dan transformation service** berbasis URL. Pengguna menaruh URL gambar asli di belakang ioimg, menambahkan parameter transformasi pada path, dan ioimg menangani fetch → transform → cache → delivery.

Nilai utama:

1. **Cepat** — data plane Rust dengan `io_uring` untuk I/O cache dan body, target p95 < 15 ms untuk cache hit.
2. **Gampang dipakai** — tanpa upload, tanpa migrasi storage. Satu baris URL atau satu plugin framework.
3. **Portabel** — jalan di semua platform: Node/Next.js, Nuxt, PHP/Laravel/WordPress, Python/Django, Ruby/Rails, Go, Shopify, Ghost, maupun HTML statis.
4. **Aman** — signed URL, allowlist origin, proteksi SSRF, decoder memory-safe.
5. **Hemat** — AVIF/WebP otomatis, penghematan bandwidth 50–80% pada tipikal web.

## 2. Problem statement

Tim produk web menghadapi tiga masalah berulang:

- **Gambar tidak dioptimasi.** Origin menyajikan JPEG/PNG resolusi penuh (1–5 MB). LCP buruk, biaya egress membengkak.
- **Optimasi ad-hoc tersebar.** `next/image`, plugin WordPress, script resize manual, dan bucket S3 berisi 5 varian per aset — tidak konsisten, sulit diaudit.
- **Solusi yang ada mahal atau mengunci.** Cloud managed (Cloudflare Images, ImageKit) per-transform pricing dan vendor lock-in; self-host (imgproxy, Thumbor) cepat tapi ops-heavy dan konfigurasinya tidak ramah untuk tim non-infra.

Celar yang tidak tergarap: **image proxy yang mudah dipakai lintas platform sekaligus cukup cepat dan murah untuk di-self-host.**

## 3. Persona & jobs-to-be-done

### P1 — Frontend engineer (Next.js/Nuxt/SvelteKit)
- JTBD: "Saya mau `<img>`/`<Image>` saya otomatis pakai AVIF dan ukuran tepat, tanpa memikirkan storage."
- Butuh: plugin framework, `srcset` helper, dev-mode jalan lokal.

### P2 — Solo dev / agency (WordPress, Laravel, Shopify)
- JTBD: "Saya mau website klien lebih cepat dalam 10 menit, tanpa mengubah theme secara besar."
- Butuh: satu baris konfigurasi, plugin/extension, tanpa server sendiri.

### P3 — Platform/infra engineer (self-host)
- JTBD: "Saya mau image proxy yang bisa saya deploy di k8s bare-metal dengan isolasi & observability yang jelas, dan tidak mengejutkan di audit keamanan."
- Butuh: binary tunggal, Docker image, Helm chart, metrics Prometheus, config file, seccomp profile.

### P4 — Media/e-commerce ops
- JTBD: "Katalog 500 ribu SKU harus punya thumbnail konsisten dan hemat bandwidth."
- Butuh: batch/prefetch, preset, laporan penghematan bandwidth, webhook.

## 4. Goals

| ID | Goal | Ukuran |
| --- | --- | --- |
| G1 | Latensi kelas CDN untuk aset ter-cache | p95 service time < 15 ms (cache hit, ≤ 256 KB) |
| G2 | Throughput satu node tinggi | ≥ 20k RPS cache hit 64 KB pada 8 vCPU; ≥ 8 Gbps serving |
| G3 | Onboarding < 10 menit | Dari daftar sampai gambar pertama ter-transform lewat URL |
| G4 | Portabilitas platform | ≥ 8 integrasi resmi pada GA |
| G5 | Penghematan bandwidth terbukti | Median ≥ 55% byte reduction pada trafik pelanggan |
| G6 | Biaya per request rendah | ≥ 3× lebih murah per 1M transform vs managed competitor tier setara |
| G7 | Portabilitas infra | ≥ 6 backend storage & ≥ 4 CDN lulus contract test; berjalan di 8 platform deploy (k8s, Docker, PaaS, bare metal) |

## 5. Non-goals (v1)

- Bukan **image hosting/CDN storage** — ioimg tidak menyimpan master asset. Origin tetap milik pelanggan. (Rencana: "Asset Store" opsional di fase 4.)
- Bukan **video transcoding** (fase lanjut, bukan v1).
- Bukan **DAM / media library** dengan UI manajemen aset.
- Bukan **WYSIWYG editor**; transformasi didefinisikan via URL/params.
- Bukan **image generation** (AI upscale, background removal) di v1.
- Tidak menargetkan Windows sebagai server produksi (client SDK tetap lintas OS).

## 6. Functional requirements

Prioritas: `P0` = wajib untuk GA, `P1` = GA jika memungkinkan, `P2` = pasca-GA.

### 6.1 Sumber gambar (source)

| ID | Requirement | Prioritas |
| --- | --- | --- |
| FR-SRC-01 | Fetch source via HTTP(S) dengan timeout, redirect limit (≤ 3), dan batas ukuran (default 20 MB, configurable). | P0 |
| FR-SRC-02 | Allowlist host per project (wildcard `*.acme.com`), termasuk skema & port. Request ke host di luar allowlist ditolak `source_not_allowed`. | P0 |
| FR-SRC-03 | Resolusi DNS dipin/di-cache; IP privat/loopback/link-local/metadata (169.254.169.254) ditolak secara default (anti-SSRF). | P0 |
| FR-SRC-04 | Dukungan `Authorization`/header kustom ke origin (disimpan terenkripsi per project). | P1 |
| FR-SRC-05 | Backend source: HTTP(S), S3-compatible (presigned internal), GCS, Azure Blob. | P1 |
| FR-SRC-06 | Source lokal (direktori mount / NFS) untuk use case self-host. | P1 |
| FR-SRC-07 | Conditional request ke origin (`If-None-Match`/`If-Modified-Since`) memakai ETag/`Last-Modified` yang di-cache. | P1 |
| FR-SRC-08 | Negative caching untuk 4xx origin (short TTL) agar tidak hammer origin. | P0 |

### 6.2 Transformasi

| ID | Requirement | Prioritas |
| --- | --- | --- |
| FR-TRF-01 | Resize (`w`,`h`,`fit`,`fill`,`crop`,`pad`), gravity (9 posisi + `auto` via saliency). | P0 |
| FR-TRF-02 | Format output: AVIF, WebP, JPEG, PNG, GIF (static), dan auto-negotiation dari header `Accept`. | P0 |
| FR-TRF-03 | Kualitas (`q`) per format dengan default terkalibrasi (AVIF 50, WebP 75, JPEG 82). | P0 |
| FR-TRF-04 | `dpr` (1,2,3) untuk layar retina; `w=auto` mengikuti lebar container? → tidak, `auto` hanya pada integrasi framework yang tahu layout. | P0 |
| FR-TRF-05 | Efek: blur, sharpen, rotate, flip, background color, trim. | P1 |
| FR-TRF-06 | Watermark teks/gambar opsional. | P2 |
| FR-TRF-07 | `strip` metadata (EXIF/GPS) default **on**; orientasi EXIF diterapkan sebelum transform. | P0 |
| FR-TRF-08 | Animated GIF/WebP/APNG: pass-through atau frame-0; transcode animasi ke WebP/AVIF. | P1 |
| FR-TRF-09 | SVG: sanitasi (hapus script/`<foreignObject>`/external refs) + rasterisasi opsional. | P0 |
| FR-TRF-10 | Batas dimensi input & output (default 8192×8192, total pixel 40 MP) untuk mencegah decompression bomb. | P0 |
| FR-TRF-11 | Preset bernama (`t_thumbnail`, `t_hero`) yang memperluas ke set parameter. | P1 |

### 6.3 Cache & delivery

| ID | Requirement | Prioritas |
| --- | --- | --- |
| FR-CACHE-01 | Cache key deterministik dari (project, ops ternormalisasi, source URL ternormalisasi, format hasil, versi transformasi). | P0 |
| FR-CACHE-02 | L1 in-memory LRU (default 512 MB) untuk objek kecil (< 256 KB). | P0 |
| FR-CACHE-03 | L2 NVMe dengan `O_DIRECT` + backend `io_uring` bila tersedia; `tokio::fs` fallback. | P0 |
| FR-CACHE-04 | Single-flight / request coalescing: N request identik untuk key yang sama hanya memicu 1 transformasi. | P0 |
| FR-CACHE-05 | Streaming response body (tanpa buffer penuh di RAM) untuk objek besar. | P0 |
| FR-CACHE-06 | `ETag` kuat + `304 Not Modified`; `Cache-Control: public, max-age, s-maxage, immutable` untuk varian immutable. | P0 |
| FR-CACHE-07 | Purge by URL, by prefix, by project. | P1 |
| FR-CACHE-08 | `stale-while-revalidate` + serve-stale saat origin error. | P1 |
| FR-CACHE-09 | Integrasi CDN eksternal (Cloudflare, Fastly, Bunny): cache key versi publik, `Vary: Accept`, debug header. | P0 |
| FR-CACHE-10 | Prefetch/warm untuk daftar URL (batch API). | P2 |

### 6.4 Keamanan & akses

| ID | Requirement | Prioritas |
| --- | --- | --- |
| FR-SEC-01 | Signed URL: HMAC-SHA256 atas `path + exp`, base64url, opsi `key_id`. | P0 |
| FR-SEC-02 | Mode "open" (hanya allowlist, tanpa signature) dan "signed-only" per project. | P0 |
| FR-SEC-03 | API key per project (hashed at rest), rotasi, scope (`read`, `purge`, `admin`). | P0 |
| FR-SEC-04 | Rate limit per project/key/IP (token bucket, distributed via Redis opsional). | P0 |
| FR-SEC-05 | Quota bulanan (jumlah transformasi + byte keluar) dengan header `X-IOIMG-Quota-Remaining`. | P1 |
| FR-SEC-06 | Audit log untuk perubahan konfigurasi & purge. | P1 |

### 6.5 Integrasi (lihat `INTEGRATIONS.md`)

| ID | Requirement | Prioritas |
| --- | --- | --- |
| FR-INT-01 | URL scheme stabil & didokumentasikan; helper builder di semua SDK. | P0 |
| FR-INT-02 | Next.js: `imageLoader` + komponen `IoImage` + `remotePatterns` doc. | P0 |
| FR-INT-03 | Nuxt: module `@ioimg/nuxt` dengan provider `ioimg` untuk `<NuxtImg>`. | P0 |
| FR-INT-04 | SvelteKit & Astro: plugin/loader resmi. | P1 |
| FR-INT-05 | WordPress plugin (rewrite `src`/`srcset` + WebP negotiation). | P0 |
| FR-INT-06 | Laravel package (Blade directive + facade). | P1 |
| FR-INT-07 | Django/Rails/Go helper. | P2 |
| FR-INT-08 | Shopify: app proxy / theme snippet. | P1 |
| FR-INT-09 | Signing helper CLI + library (`npm`, `pip`, `composer`, `cargo`, `go`). | P0 |
| FR-INT-10 | Webhook `transform.completed`, `quota.threshold`, `origin.error`. | P2 |

### 6.6 Integrasi infrastruktur (detail: `INFRA_INTEGRATIONS.md`)

| ID | Requirement | Prioritas |
| --- | --- | --- |
| FR-INF-01 | `StorageBackend` dengan dispatch skema URI: `http(s)`, `s3`, `r2`, `gs`, `az`, `file`. | P0 |
| FR-INF-02 | Varian S3 teruji: AWS S3, Cloudflare R2 (region `auto`), MinIO (`path_style`), Backblaze B2, Wasabi, DO Spaces, S3-compatible lokal. | P0 |
| FR-INF-03 | Kredensial storage per project, read-only, wajib `prefix_allow`, terenkripsi at rest. | P0 |
| FR-INF-04 | Dukungan IRSA (EKS) & Workload Identity (GKE) tanpa secret statis. | P1 |
| FR-INF-05 | `LocalOrigin` dengan sandbox root + penolakan path traversal/symlink keluar root. | P0 |
| FR-INF-06 | `CdnInvalidator` untuk Cloudflare, Bunny, CloudFront (P0); Fastly, Akamai, Gcore, Azure Front Door, GCP CDN (P1). | P0 |
| FR-INF-07 | Purge fan-out: `/v1/purge` menghapus L1/L2 lalu memanggil semua CDN project; kegagalan per-provider dicatat sebagai hasil parsial. | P0 |
| FR-INF-08 | Response mengirim `Cache-Tag`/`Surrogate-Key` (`project:<id>`, `src:<hash>`) agar purge selektif murah. | P1 |
| FR-INF-09 | Peringatan konfigurasi bila `f_auto` dipakai sementara `cdn.vary_accept = false`. | P0 |
| FR-INF-10 | Manifest deploy: Dockerfile distroless, Docker Compose, Helm chart, systemd unit, Nomad job. | P0 |
| FR-INF-11 | Helm chart menyediakan profil seccomp `uring` dan `runtimeDefault`, dengan `backend=auto` sebagai default. | P0 |
| FR-INF-12 | `/metrics` + ServiceMonitor + dashboard Grafana JSON bawaan (latensi per fase, hit ratio, SQ/CQ, egress). | P0 |
| FR-INF-13 | OTLP exporter (trace/metric) dengan sampling tail untuk error & latensi tinggi. | P1 |
| FR-INF-14 | Secret provider: env, file, Vault, AWS/GCP Secret Manager, SOPS. | P1 |
| FR-INF-15 | Admin API mendukung OIDC (JWKS cache) di samping API key. | P1 |
| FR-INF-16 | Purge event fan-out lintas node via Redis Pub/Sub atau NATS (default: single-node). | P2 |
| FR-INF-17 | `probe-uring` sebagai subcommand dan di CI; melaporkan **driver efektif** (`io_uring` vs `polling`, via `Proactor::driver_type`) bukan sekadar ketersediaan syscall; hasil dicatat di tabel kompatibilitas platform. | P0 |
| FR-INF-18 | Contoh edge compute: Cloudflare Workers, Lambda@Edge, Fastly Compute (rewrite + sign). | P1 |
| FR-INF-19 | Contract test adaptor dengan testcontainer (MinIO, fake-gcs-server, azurite) + mock CDN purge. | P0 |

### 6.7 Observability & operasi

| ID | Requirement | Prioritas |
| --- | --- | --- |
| FR-OPS-01 | Metrics Prometheus: RPS, hit ratio, latensi per fase (fetch/decode/encode/io), error rate, queue depth, io_uring SQ/CQ stats. | P0 |
| FR-OPS-02 | Structured JSON logs + trace sampling (OpenTelemetry). | P0 |
| FR-OPS-03 | `GET /healthz`, `GET /readyz`, `GET /metrics`, `GET /debug/vars` (auth). | P0 |
| FR-OPS-04 | Konfigurasi via file (TOML) + env override + hot reload (SIGHUP). | P0 |
| FR-OPS-05 | Auto-detect dukungan io_uring saat startup; fallback ke `tokio` dengan warning + metric `ioimg_io_backend_info`. | P0 |
| FR-OPS-06 | Single static binary + Docker image (distroless) + Helm chart. | P0 |
| FR-OPS-07 | Binary dapat di-build dengan `runtime-tokio` (default) atau `runtime-compio` (axum di atas io_uring via `cyper-axum`); profil aktif dilaporkan di `/debug/vars` & metric. | P1 |

## 7. Non-functional requirements

### 7.1 Performa (detail di `PERFORMANCE.md`)

| Metrik | Target | Kondisi |
| --- | --- | --- |
| p50 cache hit | ≤ 3 ms | 64 KB, keep-alive, 8 vCPU |
| p95 cache hit | ≤ 15 ms | idem |
| p99 cache hit | ≤ 40 ms | idem |
| p95 cold transform | ≤ 250 ms | 3000×3000 JPEG → 1280 px WebP q75 |
| p95 AVIF cold | ≤ 600 ms | idem, AVIF q50 |
| Throughput cache hit | ≥ 20k RPS | 64 KB object |
| Egress | ≥ 8 Gbps | 1 node |
| TTFB | ≤ 50 ms | p95, region sama |

### 7.2 Ketersediaan & reliabilitas

- SLO availability: **99.95%** per bulan (single region), 99.99% dengan multi-region.
- Error budget 0.05% → ~21,6 menit downtime/bulan.
- Degradasi graceful: origin down → serve stale; decoder OOM → `decode_failed` (bukan crash); io_uring gagal → fallback backend.

### 7.3 Skala

- Per project: 10 M transformasi/hari, 5 TB egress/hari.
- Cache entry: 500 juta objek per cluster (sharded).
- Konfigurasi tenant: 100 k project.

### 7.4 Lain-lain

- **Kompatibilitas**: Linux x86_64 & aarch64 (produksi), macOS arm64 (dev), Docker.
- **Bahasa produk**: UI & dokumentasi EN; dokumentasi ID sebagai terjemahan.
- **Privasi**: tidak menyimpan konten gambar pelanggan lebih lama dari TTL cache; opsi "no-store" untuk gambar sensitif.
- **Kepatuhan**: GDPR-ready (data residency pilihan region, DPA), audit log 90 hari (enterprise).

## 8. User stories & acceptance criteria

### US-1 — Next.js
> Sebagai frontend engineer, saya mau mengganti `next/image` loader ke ioimg agar gambar otomatis AVIF + ukuran tepat.

**AC**
- [ ] Tambah `images.loader: 'custom'`, `loaderFile: './ioimg-loader.ts'` → build sukses.
- [ ] `srcset` yang dihasilkan berisi URL ioimg dengan `w` sesuai `deviceSizes`.
- [ ] Gambar ter-render di dev (tanpa internet ke production) dengan mock/local mode.
- [ ] Lighthouse LCP membaik ≥ 20% pada halaman contoh.

### US-2 — WordPress
> Sebagai agency, saya mau install plugin, isi API key, dan semua gambar tersaji via ioimg tanpa ubah theme.

**AC**
- [ ] Aktivasi + isi key → gambar `.jpg` di post di-rewrite ke URL ioimg.
- [ ] `srcset` asli tetap dipertahankan dan dipetakan ke param `w`.
- [ ] Kalau ioimg error/timeout, plugin fallback ke URL asli (tanpa broken image).
- [ ] Ada tombol "Test connection" dan "Purge cache".

### US-3 — Self-host
> Sebagai infra engineer, saya mau deploy image proxy di k8s bare-metal dan melihat metrik di Grafana.

**AC**
- [ ] `helm install` dengan `values.yaml` minimal (allowlist, storage) → pod Ready.
- [ ] `/metrics` mengembalikan `ioimg_io_backend_info{backend="io_uring"}`.
- [ ] io_uring diblokir seccomp → pod tetap Ready, log warning, backend `tokio`.
- [ ] Dashboard Grafana (JSON) tersedia di repo.

### US-4 — Signed URL untuk private asset
> Sebagai engineer konten berbayar, saya mau gambar hanya bisa diakses dengan link bertanda tangan yang kedaluwarsa.

**AC**
- [ ] Server helper menghasilkan URL `/{sig}/...?exp=...`.
- [ ] URL tanpa/tanda tangan salah → `401 invalid_signature` (JSON).
- [ ] URL setelah `exp` → `410 signature_expired`.
- [ ] Query tambahan tidak mengubah validitas signature (kecuali parameter yang ditandatangani).

### US-5 — Batch migrasi
> Sebagai ops e-commerce, saya mau me-render ulang 500 ribu thumbnail sebelum peluncuran.

**AC**
- [ ] Endpoint batch menerima daftar URL + preset, mengembalikan job id.
- [ ] Progress dapat dipantau; throughput ≥ 200 transformasi/detik per node.
- [ ] Hasil masuk cache sehingga request user pertama = cache hit.

## 9. Metrik keberhasilan

**North star:** *byte yang dioptimasi per bulan* (byte keluar via ioimg yang menggantikan byte asli lebih besar).

| Kategori | Metrik | Target 6 bulan pasca-GA |
| --- | --- | --- |
| Adopsi | Project aktif (≥ 1k req/bulan) | 500 |
| Adopsi | Waktu onboarding p50 | < 10 menit |
| Produk | Cache hit ratio | ≥ 90% |
| Produk | Byte reduction median | ≥ 55% |
| Reliabilitas | Availability | ≥ 99.95% |
| Reliabilitas | Error rate 5xx | < 0.1% |
| Performa | p95 cache hit | < 15 ms |
| Retensi | Project aktif bulan ke-2 | ≥ 70% |
| Bisnis | Gross margin (self-host vs managed) | ≥ 70% pada managed |

Counter-metrics (jangan dioptomasi buta): kualitas gambar yang dirasakan (SSIM/butteraugli threshold pada preset), dan jumlah tiket support integrasi.

## 10. Paket & monetisasi (hipotesis, perlu validasi)

| Paket | Harga | Kuota | Fitur |
| --- | --- | --- | --- |
| Free | $0 | 10k transformasi/bln, 10 GB egress, watermark ioimg | Signed URL, allowlist, 3 preset |
| Pro | $19/bln | 500k transformasi, 200 GB | Tanpa watermark, custom domain, purge API, 20 preset |
| Scale | $99/bln + metered | 5 M, 2 TB, lalu $0.6/100k + $0.02/GB | Multi-region, SLA 99.95%, webhook, audit log |
| Enterprise | kontrak | custom | Self-host license, SSO, VPC peering, support |
| Self-host OSS | gratis | — | Core AGPL; fitur enterprise (SSO, cluster, audit) berbayar |

## 11. Risiko

| Risiko | Dampak | Mitigasi |
| --- | --- | --- |
| `io_uring` diblokir seccomp di Docker/k8s/managed platform | Nilai jual utama hilang di sebagian besar deploy | Backend pluggable + fallback otomatis; dokumentasi seccomp eksplisit; benchmark kedua backend (ADR-0002) |
| `runtime-compio` (axum di atas io_uring) belum matang | Service bergantung pada runtime pre-1.0 | Default tetap `tokio`; dipakai hanya setelah gate ADR-0007; handler di belakang trait `Rt` agar bisa kembali |
| Klaim performa tidak terwujud karena bottleneck CPU codec, bukan syscall | Kredibilitas produk | Benchmark per-fase sejak M0; posisikan io_uring sebagai keunggulan di serving/cache, bukan transformasi |
| CVE pada decoder image (RCE di service yang fetch dari internet) | Kritis | Prefer decoder Rust memory-safe; proses sandbox/worker terpisah; patch SLA; fuzz test |
| SSRF / jadi open proxy | Kritis | Allowlist default-deny, IP filtering, signed URL, rate limit |
| Biaya egress cloud (bukan CPU) mendominasi | Margin rendah | Dorong self-host, jual lisensi; optimasi kompresi |
| Kompetitor managed menurunkan harga | Daya tarik komersial | Fokus pada self-host + DX lintas platform, bukan perang harga |
| Scope creep (video, DAM, AI) | Rilis terlambat | Non-goals dikunci di §5 |
| Lisensi AGPL menakutkan perusahaan | Adopsi enterprise lambat | Dual license + komponen SDK berlisensi MIT |

## 12. Milestone (ringkas — detail di `ROADMAP.md`)

| Fase | Isi | Exit criteria |
| --- | --- | --- |
| M0 Spike (4 minggu) | Fetch+resize+cache, benchmark epoll vs io_uring | Ada angka per-fase yang membenarkan/tidak membenarkan io_uring |
| M1 MVP (8 minggu) | Signed URL, L1/L2 cache, 5 integrasi, Docker + Helm | p95 cache hit < 20 ms; 3 design partner live |
| M2 GA (8 minggu) | Multi-tenant, billing/quota, admin API, docs penuh | 99.9% uptime 30 hari; 8 integrasi |
| M3 Scale (12 minggu) | Multi-region, cluster cache, AVIF tuning, webhook | 99.95%; 50k RPS/cluster |
| M4 Platform (—) | Asset store opsional, video, AI effects (evaluasi) | — |

## 13. Pertanyaan terbuka

1. Apakah io_uring dipakai sebagai default bila tersedia, atau opt-in? (Keputusan awal: opt-in dengan auto-detect; lihat ADR-0002.) Dan apakah `runtime-compio` (HTTP juga di io_uring) dipromosikan setelah M0? (ADR-0007)
2. Format URL: path-based (dipilih) vs query-based. Perlu dukungan query-based untuk kompatibilitas CDN lama?
3. Multi-tenant di satu proses vs proses per tenant untuk isolasi codec/CPU?
4. Apakah master asset perlu disimpan (Asset Store) untuk menghindari dependency ke origin?
5. Model lisensi final: AGPL + komersial, atau Apache-2.0 dengan open-core?
6. Region awal: siapa penyedia infra (Hetzner/Bunny/AWS) untuk margin terbaik?
