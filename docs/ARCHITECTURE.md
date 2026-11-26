# Architecture — ioimg

> **Implementasi saat ini (2026-11-25): Go.** Dokumen ini mempertahankan
desain awal Rust/axum/io_uring sebagai riwayat keputusan. Implementasi yang
berjalan ada di [`../service/`](../service/README.md) dan mengikuti
[ADR-0008](adr/0008-go-data-plane.md): `net/http`, imaging pure Go (WASM
WebP/AVIF) dengan jalur libvips native, cache disk L1, dan signed URL HMAC.
> Angka bench dan alasan pivot ada di [`BENCH-GO-STARTERKIT.md`](BENCH-GO-STARTERKIT.md).

Dokumen ini menjelaskan arsitektur sistem, alur request, strategi cache, dan pembagian crate.

---

## 1. Prinsip desain

1. **Hot path tanpa alokasi berlebih.** Parsing URL/ops ke struct `Copy`-friendly; hindari `String` di tengah loop.
2. **Stateless di data plane.** Semua state (config tenant, cache) eksternal atau read-only snapshot → mudah scale horizontal.
3. **Backend I/O dapat ditukar.** `io_uring` adalah optimasi, bukan ketergantungan keras. Tanpa io_uring, sistem tetap benar dan cepat.
4. **Cache-first, origin-second.** Origin hanya disentuh saat miss; origin error tidak boleh jadi error user (serve stale).
5. **Fail closed untuk keamanan, fail open untuk delivery.** URL tidak sah → tolak. Decoder gagal → error jelas. Origin lambat → serve stale.
6. **Observable by default.** Setiap fase punya timing & counter.

## 2. Diagram komponen

```mermaid
flowchart LR
    subgraph Client
        B[Browser / App]
    end
    subgraph "Edge (opsional)"
        CDN[CDN / Origin Shield]
    end
    subgraph ioimg["ioimg node"]
        H[HTTP Server<br/>axum + hyper (tokio)]
        MW[Middleware<br/>auth, sign, ratelimit, tracing]
        P[Pipeline<br/>parse ops -> plan]
        C1[L1 Cache<br/>in-mem LRU]
        C2[L2 Cache<br/>NVMe + io_uring]
        SF[Single-flight]
        F[Fetcher<br/>HTTP/S3/local]
        D[Decoder pool]
        T[Transform<br/>resize/effect]
        E[Encoder pool<br/>AVIF/WebP/JPEG]
        OBS[Metrics / Trace]
    end
    subgraph Origin
        OG[(Origin storage)]
    end

    B --> CDN --> H
    B --> H
    H --> MW --> P
    P --> C1
    C1 -- miss --> C2
    C2 -- miss --> SF --> F --> OG
    SF --> D --> T --> E --> C2
    C2 --> H
    C1 --> H
    H --> OBS
```

## 3. Alur request (cache miss, lengkap)

```
1. Terima request  GET /{sig?}/{ops}/{source_url}
2. Middleware
   a. resolve project (dari host / X-IOIMG-Project)
   b. verifikasi signature (jika mode signed) + exp
   c. rate limit (token bucket, in-memory shard + Redis opsional)
   d. tracing span, start timer
3. Parse & normalize
   a. ops → OpsPlan { width, height, fit, gravity, format, quality, dpr, ... }
   b. source_url → canonical form (lowercase host, normalize default port, percent-decode path, buang fragment)
   c. validasi allowlist (host, scheme, port) + IP guard
4. Cache key = blake3(project_id | ops_fingerprint | source_canonical | accept_format | transform_version)
5. L1 lookup (moka/LRU, objek < 256 KB)  → HIT: serve (goto 10)
6. L2 lookup (NVMe, key → shard/2-level dir)
   - io_uring backend: `openat`+`statx`+`read` terdaftar, atau index mmap + `read` fixed buffer
   - HIT: hydrate header, masukkan L1 bila kecil, serve (goto 10)
7. Single-flight: acquire per key (dashmap<key, OnceCell>)
   - jika ada pemilik lain → tunggu, lalu goto 5
8. Fetch origin (timeout 5s, max 20 MB, redirect ≤ 3, conditional headers)
   - simpan ETag/Last-Modified untuk revalidasi
   - deteksi content-type & benar-benar-decode (magic bytes, bukan hanya header)
9. Transformasi
   a. decode header dulu (dimensi) → hitung target (hindari decode penuh bila bisa downscale di decoder, e.g. JPEG DCT scaling)
   b. decode di worker pool (CPU-bound, `spawn_blocking` atau thread-per-core shard)
   c. resize (fast_image_resize), effect, strip metadata, auto-orient
   d. encode ke format target (AVIF paling mahal → budget/circuit breaker)
   e. tulis L2 (write-behind, atomic rename dari .tmp)
10. Response
   - header: Content-Type, Content-Length, ETag, Cache-Control, Vary: Accept,
     X-IOIMG-Cache, X-IOIMG-Backend, Server-Timing
   - body: streaming; dari L2 besar memakai `splice`/`sendfile` bila TLS tidak aktif,
     atau write dari fixed buffer bila TLS aktif
11. Metrics: hit/miss, duration per fase, bytes in/out
```

## 4. Model konkurensi

Dua kelas pekerjaan dengan karakter berbeda:

| Kelas | Sifat | Penanganan |
| --- | --- | --- |
| **I/O bound** (fetch, baca/tulis cache, kirim body) | Menunggu, banyak paralel | Async. `tokio` (epoll) default; `io_uring` untuk cache & body |
| **CPU bound** (decode, resize, encode) | Menghitung, butuh core | Thread pool terpisah, jumlah = `n_cpus` (atau `n_cpus - 1`), antrean bounded dengan load-shedding |

Batas antrean penting: bila antrean transformasi penuh, kembalikan `503 overloaded` + `Retry-After`, bukan menambah latensi semua request (load shedding).

Bentuk worker per profil:

- **`runtime-tokio`**: satu proses, `n_cpus` worker thread, listener di-share (`SO_REUSEPORT`), transformasi di `spawn_blocking` pool terpisah.
- **`runtime-compio`**: satu **runtime per thread** (single-threaded by design). Multi-worker memakai `compio::dispatcher::Dispatcher` (`worker_threads(n)`) — dispatcher membagi task ke runtime tiap thread dan **berbagi satu thread pool** (yang juga dipakai file I/O pada driver `polling`, proses, dan DNS). Alternatifnya `SO_REUSEPORT` per proses, yang menurut dokumentasi compio "might be more efficient".

> Konsekuensi penting: thread pool bersama itu **punya batas jumlah thread**, dan dokumentasi `compio-driver` menyatakan operasi **bisa langsung gagal** bila batas terlampaui. Jangan pernah menaruh pekerjaan blocking di pool bersama; transformasi image harus punya pool sendiri.

### 4.1 Kenapa bukan "semua io_uring" (dan kenapa sekarang itu mungkin)

`axum` bukan runtime. Ia berjalan di atas `hyper`, dan `hyper` di atas `tokio`, yang memakai **epoll** (readiness model). `io_uring` adalah **completion model**: buffer harus hidup sampai CQE datang, dan kepemilikan buffer harus dijamin (`&'static`/pinned).

Namun `hyper` sendiri tidak terikat pada `tokio`; ia butuh `AsyncRead + AsyncWrite` (futures-io) dan timer. Karena itu **`hyper` bisa dijalankan di atas runtime lain**. Itulah yang dilakukan `cyper-axum`:

```rust
let listener = compio::net::TcpListener::bind("0.0.0.0:8080").await?;
cyper_axum::serve(listener, router).await?;   // router: axum::Router — sama persis
```

Di dalamnya: `compio` (driver io_uring) + `hyper_util::server::conn::auto::Builder` + `TowerToHyperService` + `compio::net::TcpListener`. Jadi klaim "`axum` tidak bisa di atas io_uring" **salah** — versi awal dokumen ini keliru dan sudah direvisi (lihat [ADR-0007](adr/0007-axum-on-compio.md)).

Yang tetap benar adalah **biayanya**: model thread-per-core + task `!Send` + hilangnya ekosistem tokio (`aws-sdk-*`, `sqlx`, timer `tower`) adalah perubahan besar untuk service yang punya banyak integrasi infra.

**Resolusi:** dua profil build.

| Profil | Runtime | HTTP | File/cache I/O | Integrasi |
| --- | --- | --- | --- | --- |
| `runtime-tokio` (default) | tokio/epoll | `axum::serve` | `IoBackend::Tokio` / `::Uring` | Semua (ADR-0006) |
| `runtime-compio` (eksperimen) | compio/io_uring | `cyper_axum::serve` | `IoBackend::Compio` | Terbatas: native SigV4 + bridge |

Handler ditulis di belakang trait `Rt` (`ioimg-runtime`) agar bisa dikompilasi untuk keduanya. Detail dan gate promosi: [ADR-0007](adr/0007-axum-on-compio.md).

## 5. Cache

### 5.1 Lapisan

| Layer | Media | Ukuran default | Isi | Eviction |
| --- | --- | --- | --- | --- |
| L0 | CDN (opsional, eksternal) | — | Semua varian publik | TTL/CDN policy |
| L1 | RAM (sharded LRU) | 512 MB | Objek ≤ 256 KB | LRU + TTL |
| L2 | NVMe lokal | 50–500 GB | Semua varian | LRU (segtree/clock) + TTL |
| L3 | Object storage (opsional, cluster) | TB | Varian "hot" lintas node | TTL |

### 5.2 Spesifikasi cache key

```
key = blake3_128(
  v1                                  // transform_version, bump saat algoritma berubah
  | project_id
  | ops_fingerprint                   // urutan kanonik: c_fill,dpr_2,f_webp,h_600,q_75,w_800
  | source_canonical                  // scheme://host:port/path?query (fragment dibuang)
  | output_format                     // hasil negosiasi Accept, eksplisit
)
```

Catatan:
- `output_format` masuk key, bukan header, agar `Vary` tidak diperlukan di L2 (L1/CDN tetap memakai `Vary: Accept`).
- Parameter yang tidak mempengaruhi output (mis. `utm_*` di source) dibuang opsional via config `source.normalize_query`.
- **Jangan** memasukkan `q_auto` sebagai nilai mentah; resolusi `auto` → angka dulu agar stabil.

### 5.3 Layout L2 di disk

```
/data/cache/
  ab/cd/abcdef...bin          # 2-level fanout, 256 dirs per level
  ab/cd/abcdef...meta         # 64-byte header (ops fingerprint, length, created, ttl, etag, format)
  tmp/                        # write lalu rename atomik
  _lru                        # log/clock bit (opsional, mmap)
```

- Nama file = hex dari key → file 40 char, mudah `statx` tanpa cari metadata.
- Header di file `meta` terpisah agar `read` body bisa langsung `splice` ke socket tanpa menyalin header.
- Tulis selalu `tmp → rename` (atomik dalam satu filesystem).

### 5.4 Invalidasi

| Pemicu | Aksi |
| --- | --- |
| `PURGE` by URL | Hapus semua varian: butuh index `source_canonical → [keys]` (store terpisah, mis. Redis/rocksdb). |
| `PURGE` by prefix | Scan index; batasi ukuran operasi (async job). |
| Deploy config berubah (allowlist, preset) | Bump `config_epoch` → masuk ke key → varian lama expire natural. |
| Transform engine upgrade | Bump `transform_version` saat startup → cache dingin untuk node itu (atau cluster-wide, configurable). |
| TTL habis | Lazy delete saat miss + background sweeper. |

## 6. Modul & crate

```
ioimg/
├── Cargo.toml                  # workspace
├── crates/
│   ├── ioimg-core/             # domain: Ops, OpsPlan, CacheKey, Project, error types
│   ├── ioimg-ops/              # parser + normalisasi ops dari path/query (fuzz target)
│   ├── ioimg-cache/            # trait CacheStore, L1 (moka), L2 (file), key index
│   ├── ioimg-io/               # trait IoBackend; backend tokio & uring; alignment, buffers
│   ├── ioimg-runtime/          # trait Rt: spawn, spawn_blocking, sleep, now (tokio | compio)
│   ├── ioimg-cdn/              # trait CdnInvalidator: cloudflare/bunny/cloudfront/fastly
│   ├── ioimg-fetch/            # HTTP client, allowlist, DNS guard
│   ├── ioimg-storage/          # trait StorageBackend: s3/r2/gs/az/file/http, S3/GCS/local
│   ├── ioimg-codec/            # decode/encode, format negotiation, limits
│   ├── ioimg-transform/        # resize, crop, effects, watermark
│   ├── ioimg-http/             # axum router, middleware, error mapping, response streaming
│   ├── ioimg-admin/            # admin API, config CRUD, purge, quota
│   ├── ioimg-config/           # TOML + env, hot reload, validation
│   ├── ioimg-telemetry/        # tracing, metrics, OTel
│   └── ioimg-server/           # binary: wiring, startup, io_uring detect, signals
├── xtask/                      # build/bench/fuzz helpers
└── deploy/
    ├── docker/                 # Dockerfile, seccomp profile
    └── helm/
```

### 6.1 Trait inti

```rust
// ioimg-io
//
// Profil tokio:        `&mut BytesMut` (readiness, buffer dipinjam).
// Profil compio:       buffer dipindahkan (ownership) → `BufResult`, karena io_uring
//                      membutuhkan buffer hidup sampai CQE dan tidak boleh di-free oleh caller.
pub trait IoBackend: 'static {
    fn name(&self) -> &'static str;              // "tokio" | "uring" | "compio"
    /// `aligned=true` → buffer 4096-aligned untuk `O_DIRECT`.
    fn alloc(&self, cap: usize, aligned: bool) -> CacheBuf;
    fn read_at(&self, file: &CacheFile, buf: CacheBuf, off: u64) -> impl Future<Output = BufResult<usize, CacheBuf>>;
    fn write_at(&self, file: &CacheFile, buf: CacheBuf, off: u64) -> impl Future<Output = BufResult<(), CacheBuf>>;
    /// Kirim isi file ke sink; backend boleh memakai splice (`cache_hit_body`).
    fn send_file(&self, file: &CacheFile, range: Option<Range>, out: BodySink) -> impl Future<Output = Result<u64>>;
}
```

Catat tiga perbedaan model yang wajib dihormati implementasi `compio`:

1. **Buffer berpindah kepemilikan** (`IoBuf`/`IoBufMut`, `BufResult<T, B>`). Buffer kembali lewat nilai, bukan lewat `&mut`; di stable `?` tidak bisa dipakai pada `BufResult` (pakai `buf_try!`). Semua buffer L2 harus bisa di-`into_inner` untuk dikembalikan ke pool.
2. **Drop ≠ cancel.** Dokumentasi `compio-io`: *"the read or write operations could not be cancelled by simply dropping them. The inner buffer may have transferred the ownership to the driver. Were the future dropped, the buffer would never come back."* → timeout & client-disconnect **tidak boleh** hanya menjatuhkan future I/O; harus ada jalur cancel + reclaim.
3. **Pool buffer bawaan**: pakai `AsyncReadManaged`/`AsyncReadManagedAt` ("specially optimized when using compio with io-uring driver") daripada menulis pool sendiri.

```rust
// ioimg-cache
pub trait CacheStore: Send + Sync + 'static {
    fn get(&self, key: &CacheKey) -> impl Future<Output = Option<CacheEntry>>;
    fn put(&self, key: &CacheKey, entry: CacheEntry) -> impl Future<Output = Result<()>>;
    fn invalidate(&self, spec: InvalidateSpec) -> impl Future<Output = Result<u64>>;
}

// ioimg-codec
pub trait Codec: Send + Sync {
    fn sniff(&self, head: &[u8]) -> Option<Format>;
    fn dimensions(&self, buf: &[u8]) -> Result<(u32, u32)>;
    fn decode(&self, buf: &[u8], hints: DecodeHints) -> Result<DynamicImage>;
    fn encode(&self, img: &DynamicImage, fmt: Format, opts: EncodeOpts) -> Result<Vec<u8>>;
}
```

## 7. Backend I/O: desain

### 7.1 Deteksi & fallback

```
startup:
  if cfg.io.backend == "auto":
     probe: syscall io_uring_setup(entries=8) → ok?
        ok  → try register buffers/files, submit NOP, wait CQE (timeout 50ms)
                 ok  → UringBackend
                 err → warn + TokioBackend  (metric io_backend_fallback_total{reason})
        errno EPERM/ENOSYS/ENOSYS → TokioBackend (seccomp/kernel terlalu tua)
  log + expose ioimg_io_backend_info{backend=...}
```

Probe harus berjalan di **thread terpisah** dan **dibatasi timeout**, karena kegagalan seccomp bisa muncul sebagai SIGSYS (proses mati) alih-alih errno. Karena itu probe dijalankan di child process (fork+exec self dengan `--probe-uring`) sehingga container tidak crash.

### 7.2 Backend uring: apa yang dipercepat

Berlaku untuk **profil `runtime-tokio`** (backend `io-uring` mentah). Profil `compio` **tidak** memiliki semua kemampuan ini — lihat catatan di bawah.

| Operasi | Teknik |
| --- | --- |
| Baca cache entry kecil | `openat2` + `read` ke fixed buffer, 2 SQE, 1 `io_uring_enter` |
| Baca body besar | `read` dengan fixed buffer 256 KB, lalu `write`/`send` ke socket |
| Tulis cache | `write` `O_DIRECT` (buffer aligned 4096), `fsync` opsional (boleh di-skip, cache loss acceptable) |
| Stat/validasi | `statx` dalam batch bersama `read` (linked SQE) |
| Metadata | mmap, tanpa syscall |

Batasan `O_DIRECT`: ukuran & offset harus kelipatan logical block (512/4096) dan buffer aligned. Karena itu layer `ioimg-io` menyediakan `AlignedBuf` (`#[repr(align(4096))]`) dan menyimpan body dengan padding di L2.

**Koreksi penting (hasil riset `compio`, lihat [`RESEARCH-compio.md`](RESEARCH-compio.md)):**

| Kemampuan | `io-uring` mentah (profil tokio) | `compio` (profil compio) |
| --- | --- | --- |
| Registered files | ya | ya (`register_files`, io-uring hanya) |
| Registered buffers | ya | **tidak ada API** |
| `splice` file→socket | ya | **tidak ada opcode** |
| `send_zc` | via crate `io-uring` | ya (`SendZc`, butuh handling notifikasi) |
| `statx` | ya | ya |
| `O_DIRECT` | via `OpenOptionsExt` | via `OpenOptions::custom_flags` (alignment milik kita) |
| Deteksi driver efektif | milik kita | `Proactor::driver_type` |

Konsekuensi: klaim "zero-copy body dari L2 ke socket" hanya mungkin di profil tokio dengan backend `io-uring` mentah. Di profil compio, egress = salin buffer (atau `send_zc` sebagai optimasi lanjutan).

### 7.3 Backend tokio (fallback & default portabel)

`tokio::fs` (blocking pool) untuk file, `hyper` body stream untuk pengiriman. Untuk objek > 1 MB, gunakan `spawn_blocking` + `File::read` guna menghindari blocking reactor.

### 7.4 Storage origin & CDN awareness

Dua adaptor infra hidup di luar hot path transformasi tetapi menentukan bentuk request:

- **`ioimg-storage`** — `StorageBackend` dipilih dari skema URL (`s3://`, `r2://`, `gs://`, `az://`, `file://`, `http(s)://`) atau alias project (`media/...`). Fetch dari storage memakai kredensial project (bukan URL publik), tetapi tetap terkena prefix allow + batas ukuran.
- **`ioimg-cdn`** — `CdnInvalidator` dipakai saat purge. Response menyertakan `Cache-Tag`/`Surrogate-Key` agar purge selektif.

Konsekuensi pada cache: **`f_auto` + `Vary: Accept` hanya aman bila CDN di depan menghormati `Vary`**. Konfigurasi project menyimpan `cdn.vary_accept`; bila `false`, ioimg mengeluarkan warning saat `f_auto` dipakai dan dokumentasi mendorong format eksplisit. Detail: [`INFRA_INTEGRATIONS.md §4`](INFRA_INTEGRATIONS.md), [ADR-0006](adr/0006-infra-adapters.md).

## 8. Multi-tenancy

- **Config**: snapshot `Arc<ConfigSnapshot>` di-swap atomik (arc-swap) saat config berubah. Data plane tidak pernah menunggu lock config.
- **Isolasi cache**: `project_id` masuk key → tidak perlu direktori per tenant (menghindari inode blowup), tetapi purge tetap scoped.
- **Isolasi CPU**: transformasi berjalan di pool bersama dengan **per-tenant concurrency limit** (mis. Pro: 4 concurrent, Scale: 32). Mencegah noisy neighbor.
- **Isolasi kegagalan**: circuit breaker per (project, origin host) — origin yang lambat tidak menghabiskan worker global.

## 9. Konfigurasi

```toml
[server]
bind = "0.0.0.0:8080"
workers = 0                    # 0 = n_cpus
max_body_mb = 20
graceful_shutdown_secs = 20

[io]
backend = "auto"               # "auto" | "tokio" | "uring" | "compio" (profil runtime-compio)
driver = "auto"                # compio: "auto" (fusion) | "uring" | "polling" → ProactorBuilder
uring_entries = 4096
uring_sqpoll = false
uring_iopoll = false
direct_io = true
fixed_buffer_kb = 256

[transform]
max_input_mp = 40
max_output_dim = 8192
default_quality = { avif = 50, webp = 75, jpeg = 82, png = 100 }
avif_concurrency = 2           # encode AVIF mahal
resize_filter = "lanczos3"

[cache]
l1_mb = 512
l2_dir = "/data/cache"
l2_max_gb = 200
ttl_default_secs = 86400
ttl_404_secs = 30
serve_stale_secs = 86400

[fetch]
timeout_ms = 5000
connect_timeout_ms = 1000
max_redirects = 3
allow_private_ips = false

[cdn]
vary_accept = false            # true hanya bila CDN (mis. Fastly) menghormati Vary: Accept

[[project.storage]]
alias  = "media"
kind   = "s3"
bucket = "acme-media"
region = "auto"
endpoint = "https://<account>.r2.cloudflarestorage.com"
prefix_allow = ["public/"]

[[project.cdn]]
provider  = "cloudflare"
zone_id   = "abc123"
api_token = { type = "env", var = "CF_PURGE_TOKEN" }

[security]
signed_url = "per-project"     # "off" | "per-project" | "always"

[[project]]
id = "acme"
hosts = ["acme.ioimg.net"]
allow = ["cdn.acme.com", "*.acme.com"]
mode = "signed"
```

## 10. Failure modes

| Kegagalan | Perilaku |
| --- | --- |
| Origin timeout / 5xx | Serve stale jika ada; else `502 origin_error` |
| Origin 404 | Cache negatif 30 s, `404 source_not_found` |
| Decode gagal | `415 unsupported_format` / `422 decode_failed`, tidak di-cache (atau negatif pendek) |
| Disk cache penuh | Evict LRU; bila gagal tulis → tetap serve dari memori (degradasi) |
| io_uring error saat runtime | Circuit breaker → switch backend `tokio` sampai restart, metric naik |
| Worker pool jenuh | `503 overloaded` + `Retry-After: 1` |
| Config invalid saat reload | Tolak reload, pertahankan snapshot lama, log error |

## 11. Referensi internal

- [`PERFORMANCE.md`](PERFORMANCE.md) — angka target & metodologi benchmark
- [`API.md`](API.md) — kontrak HTTP
- [`INFRA_INTEGRATIONS.md`](INFRA_INTEGRATIONS.md) — storage, CDN, platform, observability
- [`SECURITY.md`](SECURITY.md) — model ancaman
- [`adr/`](adr/) — keputusan dan alasannya


### Batas ukuran respons per transport

Engine `celeris` (io_uring/epoll) menutup koneksi bila byte respons yang belum
di-ACK melewati **4 MiB** (`maxSendQueueBytes`), dan `Blob` tidak bisa
memindahkan badan sebesar itu. Karena `raw_` boleh mengirim sumber sampai
`IOIMG_SOURCE_RAW_MAX_BYTES` (default 64 MiB), adapter di
`service/internal/server/transport_celeris.go` memakai dua jalur:

| Ukuran badan | Jalur |
| --- | --- |
| ≤ 1 MiB | `Context.Blob` (satu buffer, satu syscall) |
| > 1 MiB | `Context.Detach` + `StreamWriter`, potongan 256 KiB, `Flush` per potongan |

`Detach` menaikkan plafon antrean kirim ke 64 MiB, dan `Flush` per potongan
menjaga jendela pending tetap kecil. Kalau engine tidak punya `Streamer`,
adapter mengembalikan 500 alih-alih mengiklankan `Content-Length` yang tidak
bisa dikirim — truncation diam-diam adalah bug yang pernah terjadi di sini.

Diverifikasi manual (binary `-tags vips`, transport celeris): `raw_1` untuk
sumber 4 MB, 5 MB, dan PNG DIV2K 4.717.456 byte — semuanya utuh byte-per-byte.
