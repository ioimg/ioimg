# API — ioimg

> **Implementasi:** data plane di `service/cmd/ioimg`; admin/control plane di
> repo private `ioimg/control-plane` (`cmd/ioimg-control`) dengan endpoint
> project/signing-key/API-key/sign/usage. Lihat
> [`service/README.md`](../service/README.md) dan
> [ADR-0009](adr/0009-control-plane.md).

Kontrak HTTP publik. Versi dokumen: 1.0.

Base URL contoh:
- Managed: `https://<project>.ioimg.net`
- Self-host: `http://localhost:8080` atau domain sendiri

---

## 1. Bentuk URL

### 1.1 Path-based (utama)

```
/{signature}/{ops}/{source_url}
{ops}/{source_url}                       # mode open (tanpa signature)
```

- `ops` adalah daftar parameter dipisah koma, urutan bebas: `w_800,h_600,c_fill,f_auto,q_auto`
- `source_url` adalah URL absolut tanpa skema encoding ganda; `https://` boleh ditulis `https://` atau dipersingkat `https://` → tetap wajib absolut. Untuk menghindari `//` ganda, `https://host/path` di path URL menjadi `https://host/path` setelah slash pertama.
- Percent-encode hanya karakter yang akan ambigu (`?`, `#`, spasi). `,` dan `:` boleh literal.

Contoh:

```
/w_800,f_auto,q_auto/https://cdn.acme.com/hero.jpg
/8Kk2s.../w_320,h_320,c_fill,g_auto,f_webp/t_thumb/https://cdn.acme.com/p/123.jpg
```

### 1.2 Query-based (kompatibilitas)

```
/img?src=https%3A%2F%2Fcdn.acme.com%2Fhero.jpg&w=800&h=600&c=fill&f=auto&q=auto&sig=...
```

Keduanya setara; query-based disediakan untuk platform yang sulit menulis path (Shopify proxy, beberapa WAF).

### 1.3 Kanonisasi

Server **wajib** menormalkan sebelum menghitung signature dan cache key:

1. Skema & host di-lowercase.
2. Port default (`:80` untuk http, `:443` untuk https) dibuang.
3. Path: percent-decode lalu re-encode karakter unsafe (RFC 3986 unreserved tetap).
4. Duplikat `//` di path di-collapse (kecuali setelah skema).
5. Query source: parameter diurutkan bila `source.normalize_query = true` (default false, karena bisa mengubah semantik origin).
6. Fragment (`#...`) dibuang.
7. Spasi di ops tidak diizinkan.

## 2. Parameter transformasi

Format: `nama_nilai`. Nilai numerik tanpa satuan (piksel). Beberapa parameter menerima nilai `auto`.

| Param | Nama | Nilai | Default | Catatan |
| --- | --- | --- | --- | --- |
| `w` | width | 1..8192, `auto` | — | `auto` hanya bermakna dengan `h` pada integrasi yang tahu aspect ratio |
| `h` | height | 1..8192, `auto` | — | |
| `c` | fit/resize mode | `fit`, `fill`, `force`, `pad`, `crop`, `scale` | `fit` | |
| `g` | gravity | `center`, `north`, `south`, `east`, `west`, `north_east`, `north_west`, `south_east`, `south_west`, `auto`, `face` | `center` | `auto` = saliency, `face` = deteksi wajah |
| `f` | format | `auto`, `avif`, `webp`, `jpeg`, `png`, `gif`, `json` | `auto` | `json` mengembalikan metadata, bukan gambar |
| `q` | quality | 1..100, `auto` | per-format | `auto` = heuristic berdasarkan ukuran & konten |
| `dpr` | device pixel ratio | 1, 1.5, 2, 3 | 1 | Mengalikan `w` & `h` |
| `bg` | background | hex `RRGGBB`, `transparent` | `transparent` | Untuk `pad` dan PNG→JPEG |
| `blur` | blur | 0..100 | 0 | Gaussian sigma proporsional |
| `bri` | brightness | -100..100 | 0 | Ditambahkan di parity pack 1; alias `brightness` |
| `con` | contrast | -100..100 | 0 | Alias `contrast` |
| `sat` | saturation | -100..100 | 0 | Alias `saturation`; `-100` = grayscale |
| `pixel` | pixelate | 1..200 | — | Ukuran blok dalam piksel; alias `px` |
| `mono` | monochrome | `1` | 0 | Grayscale; alias `monochrome` |
| `sharp` | sharpen | 0..10 | 0 | Unsharp mask |
| `rot` | rotate | `90`, `180`, `270` | — | Rotasi kelipatan 90° |
| `flip` | flip | `h`, `v`, `hv` | — | |
| `trim` | trim | `1`..`40` (fuzz) | — | Auto-crop border |
| `strip` | strip metadata | `0`, `1` | 1 | Default membuang EXIF/GPS |
| `enlarge` | allow upscale | `0`, `1` | 0 | |
| `palette` | PNG palette | `1` | 0 | Kuantisasi 256 warna |
| `sharpen` | alias `sharp` | | | |

### 2.1 Preset

Preset (`t_<nama>`) memperluas ke set parameter. Parameter lain menang atas preset (override).

```
/t_thumb,w_200/https://cdn.acme.com/a.jpg    # t_thumb = h_320,c_fill,f_auto,q_auto ; w ditimpa 200
```

Preset tidak boleh berisi `t_` (tidak ada rekursi) dan divalidasi saat disimpan.

### 2.2 Konflik parameter

- `c_force` dengan satu dimensi `auto` → `422 invalid_ops`.
- `w` dan `h` keduanya tidak ada → `f_auto` saja (recompress) atau passthrough.
- `f_gif` pada source non-animasi → GIF statis (bukan error).
- `dpr>1` dengan `w` > 8192/dpr → clamp, header `X-IOIMG-Clamped: 1`.

## 3. Negosiasi format (`f_auto`)

Urutan preferensi sesuai `Accept`:

```
image/avif;q=1.0, image/webp;q=0.9, image/jpeg;q=0.8
```

Aturan:
1. Jika `Accept` tidak ada/wildcard → `webp` (fallback konservatif), atau `jpeg` bila `is_static_photo=false`.
2. AVIF hanya jika source bukan animasi dan ukuran output ≤ 4096×4096.
3. PNG dengan transparansi **tidak** dikonversi ke JPEG kecuali `bg` diberikan.
4. Respons menyertakan `Vary: Accept` dan `Content-Type` aktual.

## 4. Signed URL

### 4.1 Format

```
/{signature}/{ops}/{source}?exp=<unix>&kid=<key_id>
```

- `signature` = `base64url(HMAC-SHA256(key, canonical))` (tanpa padding)
- `canonical` = `"{ops}/{source}"` → string literal yang **sudah** dinormalkan (§1.3), tanpa leading slash, tanpa query `exp`/`kid`
- `exp` (opsional, wajib bila project `expiry_required`): unix timestamp detik
- `exp` **tidak** ikut di canonical body; ia divalidasi terhadap waktu server. Alternatif yang lebih ketat: sertakan `exp` di canonical (lihat `signature.include_exp`), direkomendasikan `true` agar `exp` tidak bisa diubah tanpa merusak signature.

> **Default v1: `include_exp = true`.** Canonical menjadi `"{ops}/{source}?exp={exp}"`.

### 4.2 Verifikasi

```
1. Ambil config project → signing key aktif (kid) & key lama (rotasi, grace 7 hari)
2. Hitung canonical, bandingkan constant-time (subtle::ConstantTimeEq)
3. Cek exp (bila ada) → 410 signature_expired
4. Gagal → 401 invalid_signature (jangan bocorkan alasan detail ke publik; log detail)
```

### 4.3 Contoh (pseudo)

```ts
const canonical = `${ops}/${source}?exp=${exp}`;
const sig = base64url(hmacSha256(secret, canonical));   // 43 chars
const url = `https://acme.ioimg.net/${sig}/${ops}/${source}?exp=${exp}`;
```

## 5. Header response

| Header | Contoh | Keterangan |
| --- | --- | --- |
| `Content-Type` | `image/avif` | Format aktual (bukan yang diminta) |
| `Content-Length` | `48213` | Kecuali streaming chunked |
| `ETag` | `"b3f1c0a2…"` | Kuat, = cache key prefix |
| `Cache-Control` | `public, max-age=31536000, immutable` | Varian immutable |
| `Vary` | `Accept` | Wajib untuk `f_auto`; dukungan per CDN berbeda — verifikasi (§INFRA_INTEGRATIONS 4.1) |
| `Cache-Tag` | `project:acme,src:a1b2c3` | Purge selektif (Cloudflare) |
| `Surrogate-Key` | `project:acme` | Fastly/Varnish |
| `X-IOIMG-Cache` | `HIT-L1`, `HIT-L2`, `MISS`, `BYPASS` | Debug |
| `X-IOIMG-Backend` | `uring`, `tokio` | Backend I/O aktif |
| `X-IOIMG-Transform-Ms` | `47` | Waktu transformasi |
| `Server-Timing` | `fetch;dur=120, decode;dur=18, resize;dur=12, encode;dur=41` | Profil fase |
| `X-IOIMG-Quota-Remaining` | `482113` | Bila quota aktif |
| `Last-Modified` | HTTP date | Dari origin atau waktu cache entry |

## 6. Error model

Semua error non-gambar mengembalikan JSON (kecuali `Accept: image/*` yang meminta fallback, lihat §6.2):

```json
{
  "error": {
    "code": "source_not_allowed",
    "message": "Host 'internal.corp' is not in the allowlist for project 'acme'.",
    "request_id": "01HQ8Y...",
    "docs": "https://ioimg.net/docs/errors#source_not_allowed"
  }
}
```

| HTTP | `code` | Penyebab | Retryable |
| --- | --- | --- | --- |
| 400 | `invalid_ops` | Param tidak dikenal / konflik | Tidak |
| 400 | `invalid_source_url` | URL source tidak absolut/invalid | Tidak |
| 401 | `invalid_signature` | Signature salah/absen | Tidak |
| 403 | `source_not_allowed` | Host di luar allowlist | Tidak |
| 403 | `private_address_blocked` | Resolusi ke IP privat/metadata | Tidak |
| 404 | `source_not_found` | Origin 404 | Tidak |
| 410 | `signature_expired` | `exp` lewat | Tidak |
| 413 | `payload_too_large` | Source > batas | Tidak |
| 415 | `unsupported_format` | Format tak dikenal/ditolak (mis. HEIC tanpa dukungan) | Tidak |
| 422 | `decode_failed` | File rusak / bukan gambar | Tidak |
| 422 | `dimension_limit_exceeded` | > 40 MP / > 8192 px | Tidak |
| 429 | `rate_limited` | Rate limit | Ya (`Retry-After`) |
| 429 | `quota_exceeded` | Kuota habis | Tidak (sampai reset) |
| 502 | `origin_error` | Origin 5xx / koneksi gagal | Ya |
| 504 | `origin_timeout` | Fetch timeout | Ya |
| 503 | `overloaded` | Worker pool jenuh | Ya |
| 500 | `internal_error` | Bug | Ya |

Kode tambahan (admin): `unauthorized`, `forbidden_scope`, `project_not_found`, `config_invalid`, `io_backend_unavailable`.

### 6.1 Kontrak stabilitas

- Penambahan `code` baru = minor. Perubahan makna = major (header `X-IOIMG-Api-Version`).
- `error.code` adalah string stabil; `message` tidak untuk di-parse.

### 6.2 Fallback untuk `<img>`

Jika request mengandung `Accept: image/*` (bukan `application/json`) **dan** project mengaktifkan `fallback_on_error = "redirect"`, server membalas `302` ke source URL asli. Default: JSON error + status asli (jujur ke monitoring).

## 7. Endpoint operasional

| Method | Path | Auth | Keterangan |
| --- | --- | --- | --- |
| GET | `/healthz` | none | `200 ok` selama proses hidup |
| GET | `/readyz` | none | Gagal bila cache L2 tidak bisa ditulis / config invalid / worker pool mati |
| GET | `/metrics` | optional bearer | Prometheus text format |
| GET | `/debug/vars` | admin | JSON runtime: versi, backend io, cache stats, uptime |
| POST | `/v1/purge` | key scope `purge` | Body: `{ "urls": [...], "prefix": "...", "all": false }` → lihat §7.1 |
| POST | `/v1/warm` | key scope `purge` | Body: `{ "targets": [{ "source": "...", "ops": "w_320,f_webp" }] }` → `202 {job_id}` |
| GET | `/v1/jobs/{id}` | key | Status job batch |
| GET | `/v1/stats` | key | Aggregat: request, hit ratio, bytes, top errors |

### 7.1 Purge & fan-out CDN

Purge menghapus L1/L2 ioimg **dan** memanggil API CDN yang dikonfigurasi untuk project. Fan-out bersifat best-effort: kegagalan CDN tidak membatalkan purge lokal.

```json
POST /v1/purge
{ "urls": ["https://acme.ioimg.net/w_800,f_auto/https://cdn.acme.com/a.jpg"],
  "tags": ["src:a1b2c3"], "all": false }
```

```json
202 Accepted
{ "jobId": "01HQ9Z...",
  "local": { "keys": 4 },
  "cdn": [ { "provider": "cloudflare", "result": "ok", "urls": 1 },
           { "provider": "bunny", "result": "failed", "error": "401 unauthorized" } ] }
```

Catatan: CloudFront invalidation berbiaya & lambat → strategi utama untuk aset immutable tetap **versioned path**, bukan purge.

## 8. Admin API (control plane)

Base: `https://api.ioimg.net/v1`

| Method | Path | Keterangan |
| --- | --- | --- |
| GET/POST | `/projects` | List / create project |
| GET/PATCH/DELETE | `/projects/{id}` | Detail, update, hapus |
| GET/POST/DELETE | `/projects/{id}/keys` | API key & signing key (rotasi) |
| GET/PUT | `/projects/{id}/allowlist` | Daftar host |
| GET/PUT | `/projects/{id}/presets` | Preset |
| GET | `/projects/{id}/usage?from=&to=` | Pemakaian & biaya |
| GET/POST | `/projects/{id}/webhooks` | Endpoint webhook |

Autentikasi: `Authorization: Bearer <api_key>`; scope `admin` untuk mutasi, `read` untuk GET. Body & respons JSON: camelCase; waktu ISO-8601 UTC.

Contoh create project:

```json
POST /v1/projects
{
  "name": "Acme Store",
  "hosts": ["acme.ioimg.net"],
  "allowlist": ["cdn.acme.com", "*.acme.com"],
  "signing": { "mode": "signed", "expiryRequired": true },
  "limits": { "concurrency": 32, "monthlyTransforms": 5000000, "monthlyEgressGb": 2048 }
}
```

## 9. Webhook

Payload ditandatangani `X-IOIMG-Signature: t=<unix>,v1=<hmac-sha256(secret, "{t}.{body}")>`.

```json
{ "event": "quota.threshold", "projectId": "acme", "at": "2025-11-20T10:00:00Z",
  "data": { "metric": "transforms", "used": 4500000, "limit": 5000000, "pct": 90 } }
```

Event: `transform.batch.completed`, `quota.threshold`, `origin.error_rate`, `key.rotated`.

## 10. Metadata & JSON mode

`f_json` mengembalikan:

```json
{
  "source": "https://cdn.acme.com/hero.jpg",
  "width": 4000, "height": 3000, "format": "jpeg",
  "bytes": 1823911, "hasAlpha": false, "isAnimated": false,
  "dominantColor": "#2b3a4a",
  "blurhash": "L6PZfSi_.AyE_3t7t7R**0o#DgR4",
  "exif": { "orientation": 1 }
}
```

Berguna untuk membangun `srcset`, placeholder, dan layout tanpa memproses gambar.
