# ioimg service (M1 MVP)

Data plane image proxy: URL masuk → fetch origin → transform → cache → serve.
Implementasi Go dari kontrak di [`../docs/API.md`](../docs/API.md).

```
GET /{signature}/{ops}/{source_url}
GET /{ops}/{source_url}                 # open mode
GET /img?src=...&w=...&f=...&sig=...    # query mode
```

Contoh:

```
/w_800,f_auto,q_auto/https://cdn.acme.com/hero.jpg
/t_thumb/https://cdn.acme.com/p/123.jpg
```

## Status

| Area | Status |
| --- | --- |
| Parse ops P0, canonical, preset | ✅ |
| Signed URL HMAC + expiry + key rotation | ✅ |
| Fetch origin + SSRF guard + size/timeout | ✅ |
| Resize `fit/fill/force/pad/crop/scale`, gravity, DPR | ✅ |
| Format `jpeg/png/webp/avif/gif/json`, `f_auto` | ✅ (AVIF = WASM, lambat) |
| Input `jpeg/png/webp/avif/gif/bmp/tiff` | ✅ (TIFF/BMP decode-only) |
| Rotate, flip, blur, sharpen, trim, strip, palette, bg | ✅ |
| Color `bri/con/sat/mono` + `pixelate` | ✅ |
| L1 cache disk + single-flight + ETag/304 | ✅ |
| L2 cache S3/R2/MinIO (read-through + write-through) | ✅ |
| Purge lokal + fan-out Cloudflare/Bunny/Fastly | ✅ |
| Warm/batch API + status job | ✅ |
| Multi-tenant control plane (projects, keys, allowlist, presets, usage) | ✅ |
| Signed-URL minting endpoint + embedded dashboard | ✅ |
| `Vary: Accept`, `Cache-Tag`, `Server-Timing`, error model | ✅ |
| Metrics, health/ready, purge lokal, rate limit per-IP | ✅ |
| Quota, billing, project model (butuh control plane) | ⛔ (M2) |
| libvips/govips (native, cepat) | ✅ `-tags vips` / `Dockerfile.vips` |

## Quickstart (Docker, demo origin)

Dari direktori ini:

```bash
docker compose up --build
# gambar pertama (open mode, origin container):
curl -o /tmp/hero.webp -H 'Accept: image/webp' \
  'http://localhost:8080/w_400,f_auto/http://origin/photo00.jpg'
```

`docker compose` sengaja menyalakan `IOIMG_ALLOW_PRIVATE` karena origin demo ada
di jaringan privat container. **Jangan** pakai setting itu di produksi.

## Quickstart (lokal, Windows/macOS/Linux)

Butuh Go ≥ 1.26. Tidak perlu cgo, libvips, Postgres, atau Valkey.

```bash
go build -o bin/ioimg ./cmd/ioimg
go build -o bin/ioimgctl ./cmd/ioimgctl

# terminal 1: origin statis (contoh memakai bun; apa pun bisa)
bun -e "const http=require('http'),fs=require('fs');http.createServer((q,s)=>{fs.readFile('../artifacts/datasets/src'+q.url,(e,d)=>{e?(s.writeHead(404),s.end()):(s.writeHead(200,{'Content-Type':'image/jpeg'}),s.end(d))})}).listen(8099)"

# terminal 2: ioimg
IOIMG_ALLOW_ANY_HOST=true IOIMG_ALLOW_PRIVATE=true \
IOIMG_CACHE_DIR=./cache IOIMG_SIGNING_KEYS=demo:secret \
./bin/ioimg

# terminal 3: URL bertanda tangan + request
URL=$(./bin/ioimgctl sign -base http://localhost:8080 \
  -ops 'w_400,f_auto,q_80' -source 'http://127.0.0.1:8099/photo00.jpg' \
  -key demo:secret -exp 1h)
curl -D - -H 'Accept: image/webp,image/jpeg' "$URL"
```

Uji cepat:

```bash
go test ./...
```

Tes integrasi menyalakan origin `httptest`, memakai cache sementara, dan
memeriksa signature, ETag/304, `f_auto`, preset, query mode, SSRF, error model,
dan purge.

## Konfigurasi

Semua lewat environment variable. Tabel lengkap ada di
[`.env.example`](.env.example); yang paling penting:

| Variable | Default | Catatan |
| --- | --- | --- |
| `IOIMG_ALLOWLIST` | kosong | `cdn.example.com,*.example.com` |
| `IOIMG_ALLOW_ANY_HOST` | `false` | bila allowlist kosong, ini otomatis `true` |
| `IOIMG_ALLOW_PRIVATE` | `false` | **dev only**; memblokir loopback/metadata/CGNAT |
| `IOIMG_SIGNED_ONLY` | `false` | produksi: `true` + `IOIMG_SIGNING_KEYS` |
| `IOIMG_SIGNING_KEYS` | — | `kid:secret[,kid2:secret2]`; key pertama untuk sign |
| `IOIMG_SIGNATURE_EXPIRY_REQUIRED` | `false` | rekomendasi produksi: `true` |
| `IOIMG_PRESETS` | `{}` | JSON `{"thumb":"h_320,c_fill,f_auto,q_auto"}` |
| `IOIMG_CACHE_DIR` | `./cache` | L1 disk; aman dipakai beberapa replika |
| `IOIMG_CACHE_MAX_MB` | `4096` | eviction LRU per proses |
| `IOIMG_CONCURRENCY` | `NumCPU*4` | batas fetch+transform bersamaan |
| `IOIMG_DISABLE_AVIF` | `false` | AVIF WASM ~0,5–2 s/gambar |
| `IOIMG_L2_*` | mati | L2 S3/R2/MinIO: endpoint, bucket, key, prefix |
| `IOIMG_CDN_PROVIDERS` | kosong | `cloudflare,bunny,fastly` + credential masing-masing |
| `IOIMG_CONTROL_URL` | kosong | control plane endpoint; mengganti mode env |
| `IOIMG_CONTROL_TOKEN` | — | data-plane token untuk snapshot + usage |
| `IOIMG_CONTROL_SYNC` | `30s` | interval refresh snapshot |
| `IOIMG_ALLOW_PROJECT_HEADER` | `false` | dev: pilih tenant via `X-IOIMG-Project` |

Signature mengikuti `docs/API.md §4`:

```
canonical = "{ops_canonical}/{source_canonical}?exp={unix}"   # include_exp default true
signature = base64url_hmac_sha256(key, canonical)             # tanpa padding
```

`ops_canonical` adalah ops ternormalisasi (urutan tetap, preset sudah
diekspansi) dan `source_canonical` menormalkan skema/host/port/fragment.
Keduanya bisa dilihat dengan:

```bash
./bin/ioimgctl canonical -ops 'q_80,w_400,f_auto' -source 'HTTPS://CDN.ACME.COM:443/a//b.jpg'
```

## Endpoint

| Method | Path | Keterangan |
| --- | --- | --- |
| GET/HEAD | `/{sig}/{ops}/{source}` | data plane |
| GET | `/img?src=&w=&f=&sig=&exp=` | kompatibilitas query |
| GET | `/healthz` | liveness |
| GET | `/readyz` | cache dir writable |
| GET | `/metrics` | Prometheus (opsional `IOIMG_METRICS_TOKEN`) |
| GET | `/v1/stats` | JSON runtime |
| POST | `/v1/purge` | `{ "urls": [...], "tags": [...], "prefix": "...", "all": false }`; butuh `IOIMG_ADMIN_TOKEN` |
| POST | `/v1/warm` | `{ "targets": [{ "source": "...", "ops": "w_320,f_webp" }] }` → `202 {jobId}` |
| GET | `/v1/jobs/{id}` | status job warm |

Header respons mengikuti `docs/API.md §5`: `Content-Type`, `Content-Length`,
`ETag`, `Last-Modified`, `Cache-Control`, `Vary: Accept` (untuk `f_auto`),
`Cache-Tag`, `X-IOIMG-Cache`, `X-IOIMG-Format`, `X-IOIMG-Backend`,
`X-IOIMG-Transform-Ms`, `X-IOIMG-Clamped`, `Server-Timing`, `X-Request-Id`.

## Keamanan

- **SSRF**: allowlist host + cek IP setelah DNS resolution (bukan hanya sebelum)
  sehingga DNS rebinding tidak lolos; redirect diikuti maksimal 5 hop dan
  dievaluasi ulang; IP privat/loopback/link-local/metadata/CGNAT ditolak.
- **Batas**: ukuran source, timeout, 40 MP / 8192 px, `w/h` maksimum 8192.
- **Decoder**: hanya JPEG/PNG/GIF/WebP/AVIF/BMP/TIFF; SVG tidak diterima. Metadata
  (EXIF/GPS) dibuang oleh encoder Go.
- **Signed URL**: HMAC constant-time, expiry, rotasi key dengan grace (semua key
  diverifikasi; key pertama menandatangani).
- **Rate limit**: token bucket per IP opsional (`IOIMG_RATE_RPS`); untuk skala
  nyata taruh limit di CDN/LB.

## Catatan performa

Angka bench ada di [`../docs/BENCH-GO-STARTERKIT.md`](../docs/BENCH-GO-STARTERKIT.md)
dan [`../research/go-fiber/README.md`](../research/go-fiber/README.md). Yang relevan untuk service ini:

- Data plane sengaja **tidak** memakai session/CSRF/Inertia/access-log sinkron.
  Jalur HIT hanya parse → baca disk → tulis response.
- Prefork tidak dipakai (Go `net/http`); scaling = beberapa replika/container di
  belakang LB, cache disk per replika atau volume bersama.
- Encode AVIF memakai libavif WASM (pure Go, portabel) pada build default dan
  saat ini **jauh lebih lambat** dari WebP/JPEG. Di produksi: build dengan
  `-tags vips` (libvips native) dan/atau `IOIMG_DISABLE_AVIF=true`.
- Transform CPU-bound. Backend dipilih saat compile: pure Go (portabel) atau
  libvips (`-tags vips`), keduanya di belakang antarmuka `imaging.Processor`.

## Struktur

```
cmd/ioimg        server
cmd/ioimgctl     sign / verify / canonical
internal/ops     parse + canonical + f_auto negotiation
internal/sign    HMAC, expiry, canonical source
internal/source  fetch + SSRF guard
internal/imaging decode/transform/encode
internal/cache   L1 disk + L2 S3 + LRU + atomic write
internal/tenant  snapshot, tenant resolution, control client
internal/control control plane: SQLite, admin API, signing, dashboard
internal/purge   Cloudflare/Bunny/Fastly adapters
internal/server  HTTP contract, errors, metrics, purge
```

## Roadmap berikutnya (M2)

Quota & billing (butuh model project/control plane), integrasi resmi tambahan
(Nuxt/SvelteKit/Astro/Laravel/Shopify), Helm chart, dan hardening produksi.
Lihat [`../docs/ROADMAP.md`](../docs/ROADMAP.md).

## Batasan yang diketahui

- **AVIF** memakai libavif WASM. Pada mesin uji, encode 320 px ≈ 1,3 s; ini
  bukan jalur produksi. Set `IOIMG_DISABLE_AVIF=true` atau tambahkan backend
  libvips native sebelum mengaktifkan `f_auto` AVIF untuk trafik nyata.
- **Animated source**: hanya frame pertama yang diproses. `f_auto` bisa memilih
  AVIF untuk GIF animasi (spesifikasi meminta fallback); belum diimplementasikan.
- **Gravity `auto`/`face`** saat ini jatuh ke `center` (saliency/face detection
  belum ada).
- **`palette`** memakai kuantisasi uniform 3-3-2, bukan median-cut; cukup untuk
  ikon/placeholder, bukan fotografi.
- **Query source di path mode**: `?exp`, `?kid`, dan `?sig` dicadangkan untuk
  signature. Parameter source dengan nama tersebut harus di-percent-encode atau
  memakai query mode (`/img?src=...`).
- **Cache L1 bersifat per-proses**; beberapa replika tidak berbagi index LRU
  (file tetap aman karena write atomik). L2 S3/R2 tersedia dan menghilangkan
  cold-start antar replika, tetapi tetap bukan cluster cache terkoordinasi.
- **Purge** lokal + CDN fan-out sudah ada; belum ada retry/queue persisten.
- **Rate limit** per-IP per-proses, bukan terdistribusi.
- **Quota/billing** belum ada karena membutuhkan model project di control plane.

## L2 cache, purge, dan warm

**L2 (S3/R2/MinIO)** — aktifkan dengan `IOIMG_L2_ENABLED=true` plus endpoint,
bucket, dan kredensial. Alurnya: L1 miss → L2 get (jika hit, L1 diisi ulang dan
respons `HIT-L2`) → L2 miss → fetch+transform → tulis L1 + tulis L2 async.
Key sama dengan L1, jadi purge menghapus keduanya. Bucket dibuat otomatis bila
belum ada. Contoh R2:

```bash
IOIMG_L2_ENABLED=true
IOIMG_L2_ENDPOINT=https://<account>.r2.cloudflarestorage.com
IOIMG_L2_BUCKET=ioimg-cache
IOIMG_L2_ACCESS_KEY=...
IOIMG_L2_SECRET_KEY=...
IOIMG_L2_REGION=auto
```

**Purge** (`POST /v1/purge`, butuh `IOIMG_ADMIN_TOKEN`) menghapus L1+L2 lalu
memanggil CDN secara best-effort:

```bash
curl -X POST localhost:8080/v1/purge -H "Authorization: Bearer $TOKEN" \
  -d '{"urls":["https://img.example.com/w_400,f_auto/https://cdn.example.com/a.jpg"],"tags":["src:abc123"]}'
```

Cloudflare memakai `files`/`tags`; Bunny hanya URL; Fastly memakai URL atau
`Surrogate-Key`. Kegagalan CDN tidak membatalkan purge lokal dan dilaporkan di
`cdn[].result`.

**Warm/batch** (`POST /v1/warm`) memvalidasi target, lalu mengisi cache di
belakang layar. Status lewat `GET /v1/jobs/{id}`. Maksimum 1.000 target/job;
job disimpan in-memory (100 terakhir).

## Build libvips (produksi)

Default build portabel (pure Go/WASM). Untuk throughput transform terbaik:

```bash
# Linux: butuh libvips + header
sudo apt-get install libvips-dev        # Debian/Ubuntu
go build -tags vips -o bin/ioimg ./cmd/ioimg
go test -tags vips ./...                # butuh libvips terpasang

# Docker
docker build -f Dockerfile.vips -t ioimg:vips .
```

Backend dipilih saat compile: `-tags vips` memakai govips; tanpa tag memakai
pure Go. Keduanya mengimplementasikan `imaging.Processor`, jadi server, cache,
dan test kontrak tidak berubah.

## Multi-tenant control plane

Env-based config is fine for a single self-hosted instance, but not for SaaS:
every tenant needs its own signing keys, allowlist, presets, and quota. The
control plane (`cmd/ioimg-control`) stores that in SQLite and serves a snapshot
to data planes.

```bash
# 1. control plane
IOIMG_CONTROL_DB=./control.db \
IOIMG_CONTROL_MASTER_KEY=$(openssl rand -base64 32) \
IOIMG_BOOTSTRAP_TOKEN=boot \
IOIMG_CONTROL_TOKEN=data \
go run ./cmd/ioimg-control
# dashboard: http://localhost:8081  (token: boot)

# 2. data plane (no tenant env required)
IOIMG_CONTROL_URL=http://localhost:8081 \
IOIMG_CONTROL_TOKEN=data \
IOIMG_ALLOW_PRIVATE=true \
IOIMG_CACHE_DIR=./cache \
go run ./cmd/ioimg
```

Create a project (or use the dashboard), then sign URLs without ever shipping
the HMAC secret to the tenant's app:

```bash
# tenant API key with scope "sign"
curl -s -X POST localhost:8081/v1/projects/acme/api-keys \
  -H 'Authorization: Bearer boot' -H 'Content-Type: application/json' \
  -d '{"name":"web","scopes":["sign"]}'

# server-side signing endpoint
curl -s -X POST localhost:8081/v1/projects/acme/sign \
  -H "Authorization: Bearer ioimg_..." -H 'Content-Type: application/json' \
  -d '{"ops":"w_800,f_auto,q_auto","source":"https://cdn.acme.com/a.jpg","expiresIn":3600}'
```

Data plane resolution:

- `Host: img.acme.com` → project whose `domains` contains that host (wildcards
  `*.acme.com` supported).
- `X-IOIMG-Project: acme` when `IOIMG_ALLOW_PROJECT_HEADER=true` (local/dev).
- Unknown host/project → `404 project_not_found`; there is **no** cross-tenant
  fallback.
- Snapshot refresh every `IOIMG_CONTROL_SYNC` (default 30s); the last good
  snapshot survives control-plane downtime.
- Cache keys and purge/warm keys are namespaced by project.
- Usage is batched to the control plane every 10s; `X-IOIMG-Quota-Remaining`
  reflects the snapshot + pending counters.

API summary:

| Endpoint | Auth | Purpose |
| --- | --- | --- |
| `GET /v1/projects` | bootstrap | list tenants |
| `POST /v1/projects` | bootstrap | create + first signing key |
| `GET/PATCH/DELETE /v1/projects/{id}` | bootstrap or project `read`/`admin` | manage tenant |
| `GET/POST/DELETE /v1/projects/{id}/signing-keys` | admin | rotation |
| `POST .../signing-keys/{kid}/reveal` | admin | fetch secret |
| `GET/PUT .../allowlist`, `.../presets` | read/admin | policy |
| `GET/POST/DELETE .../api-keys` | read/admin | scoped tokens (`read`,`admin`,`purge`,`sign`) |
| `POST /v1/projects/{id}/sign` | `sign` | mint signed URL server-side |
| `GET /v1/projects/{id}/usage` | read | monthly counters |
| `GET /v1/config/snapshot` | data token | data-plane config |
| `POST /v1/usage` | data token | usage reports |

The dashboard (embedded, no build step) supports project creation, signing key
rotation/reveal, API keys, allowlist/presets, usage, and a signed-URL generator.

> **Quota/billing:** usage counters and per-project monthly transform limits
> are implemented, but invoicing/payment provider integration is not. That needs
> a billing decision (Stripe, plan tiers, overage) before code.
