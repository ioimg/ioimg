# ioimg

**Image proxy & transformation service** — URL-first. Tempel satu URL, dapat
gambar yang sudah di-resize/convert/cache. Tidak perlu upload, SDK wajib, atau
mengubah pipeline build.

```
https://acme.ioimg.dev/w_800,f_auto,q_auto/https://cdn.acme.com/hero.jpg
```

---

## Status

`alpha` — desain selesai, **data plane M1 MVP sudah berjalan** di
[`service/`](service/README.md) (Go): L1+L2 cache (S3/R2), purge CDN fan-out,
warm/batch API, backend pure Go maupun libvips (`-tags vips`), dan
**control plane multi-tenant** (SQLite, dashboard, signing endpoint, usage).
Billing/payment provider dan hardening produksi masih M2. Lihat
[ROADMAP](docs/ROADMAP.md).

```
service/          # data plane (Go, net/http) + control plane (SQLite) — MVP
integrations/     # URL builder JS, loader Next.js, plugin WordPress
poc/              # PoC Rust (riwayat): tokio vs io_uring, benchmark
poc-go/           # PoC Go: Fiber + prefork + govips
bench-kinstack/   # salinan starterkit kinstack untuk bench middleware
docs/             # PRD, API, arsitektur, keamanan, operasi, ADR
```

## Mulai cepat

```bash
cd service
docker compose up --build
curl -o /tmp/hero.webp -H 'Accept: image/webp' \
  'http://localhost:8080/w_400,f_auto/http://origin/photo00.jpg'
```

Tanpa Docker (Go ≥ 1.26, tanpa cgo/libvips/Postgres/Valkey):

```bash
cd service
go build -o bin/ioimg ./cmd/ioimg
go build -o bin/ioimgctl ./cmd/ioimgctl
IOIMG_ALLOW_ANY_HOST=true IOIMG_ALLOW_PRIVATE=true \
IOIMG_CACHE_DIR=./cache IOIMG_SIGNING_KEYS=demo:secret ./bin/ioimg
```

Detail konfigurasi, endpoint, dan batasan: [`service/README.md`](service/README.md).
Tes: `cd service && go test ./...`.

## Dokumen

| Dokumen | Isi |
| --- | --- |
| [service/README.md](service/README.md) | **Implementasi MVP**: quickstart, config, endpoint, keamanan, batasan |
| [docs/PRD.md](docs/PRD.md) | Product requirements: problem, persona, metrik, functional & non-functional requirements, pricing, rilis |
| [docs/GLOSSARY.md](docs/GLOSSARY.md) | Kamus istilah produk, image processing, jaringan, dan io_uring |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Arsitektur sistem, komponen, alur request, strategi cache, layout crate (desain awal; lihat catatan Go) |
| [docs/PERFORMANCE.md](docs/PERFORMANCE.md) | Kenapa io_uring, desain data plane, metodologi benchmark, target angka |
| [docs/API.md](docs/API.md) | Spesifikasi URL, parameter transformasi, signing, error model, admin API |
| [docs/INTEGRATIONS.md](docs/INTEGRATIONS.md) | Integrasi aplikasi: Next.js, Nuxt, SvelteKit, Astro, WordPress, Laravel, Shopify, dll. |
| [docs/INFRA_INTEGRATIONS.md](docs/INFRA_INTEGRATIONS.md) | Integrasi infrastruktur: S3/R2/GCS/MinIO, Cloudflare/CloudFront/Fastly/Bunny, edge compute, k8s, observability, secrets, event bus |
| [docs/SECURITY.md](docs/SECURITY.md) | Model ancaman, SSRF, decoder hardening, isolasi tenant, hardening deploy |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Topologi deploy, requirement kernel, observability, SLO, runbook |
| [docs/COMPETITIVE.md](docs/COMPETITIVE.md) | Peta kompetitor & positioning |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Milestone M0–M4 dengan exit criteria |
| [docs/RESEARCH-compio.md](docs/RESEARCH-compio.md) | Riset compio/cyper-axum: fakta terverifikasi, koreksi klaim, risk register produksi, 12 eksperimen prasyarat |
| [poc/README.md](poc/README.md) | **PoC Rust terukur**: server tokio & compio, backend std vs io_uring, hasil benchmark + temuan |
| [poc-go/README.md](poc-go/README.md) | **PoC Go (Fiber + prefork)**: benchmark pembanding + temuan bahwa libvips 3–5× lebih menentukan daripada pilihan bahasa |
| [docs/BENCH-GO-STARTERKIT.md](docs/BENCH-GO-STARTERKIT.md) | **Bench starterkit `kinstack`**: overhead middleware nyata (HIT −69%, p50 3,5×), penyebab per-lapisan, temuan 2 key Valkey/request, rekomendasi pemisahan data plane |
| [integrations/README.md](integrations/README.md) | URL builder JS, loader Next.js, plugin WordPress |
| [docs/adr/](docs/adr/) | Architecture Decision Records (0008: data plane Go; 0009: control plane multi-tenant; 0001/0007 historis) |

## Ringkasan keputusan teknis

1. **Data plane: Go `net/http`** tanpa framework/middleware session. Lihat
   [ADR-0008](docs/adr/0008-go-data-plane.md).
2. **Imaging**: pure Go (portabel, `CGO_ENABLED=0`) dengan WebP/AVIF WASM;
   jalur produksi cepat adalah govips/libvips di belakang antarmuka paket yang
   sama.
3. **Cache L1 disk** dengan key deterministik (versi pipeline + ops kanonik +
   source kanonik + format), single-flight, ETag/304.
4. **URL path-based + signed HMAC** — cacheable oleh CDN apa pun, tanpa state.
   `f_auto` memakai `Vary: Accept`; CDN yang mengabaikan `Vary` bisa memakai
   format eksplisit.
5. **Control plane terpisah** (kinstack, Go/Fiber) — auth, API key, billing,
   admin; data plane tidak boleh melewati rantai middleware dashboard.
6. **Infra sebagai adaptor** (`StorageBackend`, `CdnInvalidator`) tetap target
   M2; lihat [ADR-0006](docs/adr/0006-infra-adapters.md).

## Lisensi

TBD (kandidat: AGPL-3.0 untuk core self-host + lisensi komersial untuk fitur enterprise).

### Mode multi-tenant

```bash
cd service
docker compose -f compose.control.yml up --build
# dashboard: http://localhost:8081 (token demo-bootstrap)
```

Atau lokal: `make run-control` lalu `IOIMG_CONTROL_URL=http://localhost:8081
IOIMG_CONTROL_TOKEN=data make run`. Detail di
[`service/README.md`](service/README.md#multi-tenant-control-plane) dan
[ADR-0009](docs/adr/0009-control-plane.md).
