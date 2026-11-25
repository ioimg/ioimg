# Roadmap — ioimg

> **Status 2026-11-25:** M0 selesai (PoC Rust + Go, bench kinstack).
> M1 MVP data plane sudah diimplementasikan di [`../service/`](../service/README.md)
> (ops P0, signed URL, SSRF guard, L1+L2 cache, ETag, f_auto, metrics,
> purge CDN fan-out, warm/batch API, Docker, backend libvips (`-tags vips`).
> Item M1 yang belum: 3 design partner produksi, plugin
> Nuxt/SvelteKit/Astro/Laravel/Shopify, Helm, dan `ioimg check-config`.
> Keputusan stack: [ADR-0008](adr/0008-go-data-plane.md).

Setiap milestone punya **exit criteria** yang bisa diukur. Jika criteria tidak terpenuhi, milestone tidak "selesai".

---

## M0 — Spike & validasi teknis (4 minggu)

**Tujuan:** membuktikan (atau membatalkan) asumsi performa sebelum berinvestasi di produk.

> **Prasyarat:** 12 eksperimen di [`RESEARCH-compio.md §5`](RESEARCH-compio.md) harus dijawab lebih dulu. U1 (keep-alive) adalah gerbang: gagal → profil `compio` dibekukan, ioimg tetap `tokio + IoBackend::Uring`.
>
> **Status PoC (sudah dijalankan):** lihat [`poc/README.md`](../poc/README.md). Ringkas: U1/U2 terjawab, io_uring naif **belum** mengalahkan `std::fs` untuk objek kecil, dan bottleneck nyata adalah **CPU transformasi** (72 ms/gambar → 14 img/s/core), bukan I/O. Urutan prioritas M0 berubah: codec dulu, baru I/O.

| # | Deliverable |
| --- | --- |
| 1 | Binary Rust: fetch HTTP → decode (zune-jpeg/image) → resize (`fast_image_resize`) → encode WebP/AVIF → L1 cache |
| 2 | `IoBackend` trait + implementasi `tokio` dan `uring` (read/write cache, fixed buffers, `O_DIRECT`) |
| 3 | Spike `runtime-compio`: `cyper-axum` + `cyper` untuk fetch, satu endpoint `/w_/f_` di atas io_uring (ADR-0007) |
| 4 | Benchmark harness W1–W5 + hasil per-fase (fetch/decode/resize/encode/io) untuk **tiga** konfigurasi: `tokio+Tokio`, `tokio+Uring`, `compio` |
| 5 | `probe-uring` di child process + perilaku SIGSYS/EPERM terdokumentasi |
| 6 | Doc keputusan: apakah io_uring default-on? Apakah `compio` dipromosikan? |

**Exit criteria**
- [ ] Ada tabel perbandingan epoll vs io_uring dengan syscall/request & p99, 3 run, mesin terdokumentasi.
- [ ] Diketahui dengan pasti berapa % waktu di CPU decode/encode vs I/O untuk W4/W5.
- [ ] p95 cache-hit 64 KB < 20 ms (single node, TLS off) tercapai.
- [ ] Hasil A/B `tokio+Uring` vs `compio+cyper-axum` terdokumentasi (p99, CPU, syscall), termasuk catatan kegagalan integrasi (`aws-sdk`, timer `tower`).
- [ ] Keputusan ADR-0002 & ADR-0007 dikonfirmasi atau direvisi dengan data.

**Risiko utama:** io_uring tidak memberi keuntungan berarti karena CPU-bound → pivot positioning ke "DX + integrasi", io_uring jadi fitur opsional.

---

## M1 — MVP (8 minggu)

**Tujuan:** bisa dipakai 3 design partner end-to-end.

| # | Deliverable |
| --- | --- |
| 1 | URL ops lengkap P0 (resize/crop/gravity/format/quality/dpr/strip/svg sanitize) |
| 2 | Signed URL + allowlist + rate limit |
| 3 | L1 + L2 cache, single-flight, ETag/304, `Vary: Accept` |
| 4 | `f_auto` negotiation (AVIF/WebP/JPEG) |
| 5 | Integrasi: Next.js loader, Nuxt module, WordPress plugin, URL builder JS/PHP |
| 6 | Docker image + seccomp profile + Helm chart dasar |
| 7 | `/metrics`, `/healthz`, logs JSON, `ioimg check-config` |
| 8 | Infra P0: `StorageBackend` (HTTP, local, S3/R2/MinIO), seccomp profile, `probe-uring` subcommand |
| 8 | Docs: README, API, quickstart 3 platform |

**Exit criteria**
- [ ] 3 design partner memakai di produksi ≥ 2 minggu tanpa insiden P1.
- [ ] p95 cache-hit < 15 ms; cold WebP < 250 ms (mesin uji M0).
- [ ] Cache hit ratio ≥ 80% pada salah satu partner.
- [ ] Onboarding terukur: < 15 menit dari nol ke gambar pertama.
- [ ] Tidak ada open SSRF/decoder RCE di review keamanan internal.

---

## M2 — GA publik (8 minggu)

| # | Deliverable |
| --- | --- |
| 1 | Control plane: project, API key, allowlist, preset, quota, billing (Stripe) |
| 2 | Admin API + dashboard minimal (usage, purge, preset, keys) |
| 3 | Integrasi tambahan: SvelteKit, Astro, Laravel, Shopify |
| 4 | Purge API + webhook dasar |
| 5 | Managed hosting (1 region) + status page |
| 6 | Docs penuh + 5 guide + example repos |
| 7 | Fuzzing 24 jam hijau, SBOM, image signing |
| 8 | Infra: `GcsOrigin`/`AzureBlobOrigin`, purge Cloudflare+Bunny+CloudFront, `Cache-Tag`, Helm lengkap, OTel, OIDC admin |

**Exit criteria**
- [ ] 99.9% uptime 30 hari di managed.
- [ ] 8 integrasi resmi, semua punya test vector URL yang sama.
- [ ] 4 CDN + 6 storage backend lulus contract test (MinIO/fake-gcs/azurite + mock purge).
- [ ] 100 project aktif; retensi bulan-2 ≥ 60%.
- [ ] Error budget & alert on-call berjalan (ada 1 insiden simulasi).

---

## M3 — Scale & diferensiasi performa (12 minggu)

| # | Deliverable |
| --- | --- |
| 1 | Benchmark publik reproducible + halaman hasil (imgproxy vs ioimg) |
| 2 | Multi-region + regional cache residency |
| 3 | Cluster cache (L3 object storage) & consistent hashing opsional |
| 4 | AVIF tuning (rav1e params, tile/thread), encode budget adaptif |
| 5 | `splice`/zero-copy production-ready + tuning SQPOLL/IOPOLL terdokumentasi |
| 6 | Batch/warm API untuk migrasi katalog |
| 7 | Enterprise: SSO, audit log, SLA 99.95% |
| 8 | Infra lanjut: purge fan-out NATS/Redis, ClickHouse log sink, Fastly/Akamai, Vault/IRSA, Pyroscope |

**Exit criteria**
- [ ] 50k RPS/cluster pada W1, p99 < 40 ms.
- [ ] Byte reduction median ≥ 55% terukur dari trafik nyata.
- [ ] Biaya per 1M transform (managed) ≤ 1/3 tier setara kompetitor.
- [ ] 0 regresi keamanan; patch SLA decoder terpenuhi.

---

## M4 — Platform (eksplorasi)

Kandidat (pilih berdasarkan data, bukan antusiasme):
- Asset store opsional (master + varian) untuk pelanggan tanpa origin stabil.
- Video thumbnail/first-frame (bukan transcoding penuh).
- AI ops: background removal, upscale, smart crop (biaya GPU → tier mahal).
- Signed upload + webhook pipeline (image ingestion).
- Edge runtime (WASM) untuk validasi signature di CDN.

---

## Backlog lintas fase (prioritas turun)

1. HEIC decode (isu paten → tanya legal).
2. Watermark dinamis per tenant.
3. Image diff/regression test untuk kualitas (butteraugli).
4. LQIP/blurhash generator endpoint.
5. Signed upload + transform pipeline.
6. HTTP/3 native (QUIC) di ioimg (bukan hanya di CDN).
7. Profil `runtime-compio` matang: native SigV4 + `cyper` (tanpa `aws-sdk`), soak 72 jam, evaluasi gate promosi ADR-0007.

## Definisi "done" umum

- Ada test (unit + integrasi + security) untuk jalur baru.
- Ada metric/trace untuk jalur baru.
- Ada dokumentasi (API + ops + integrasi bila relevan).
- Ada entri ADR bila mengubah keputusan arsitektur.
- Benchmark tidak regresi > 10% pada W1/W6.
