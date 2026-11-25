# ioimg PoC versi Go

PoC paralel dari [`../rust-uring`](../rust-uring/README.md): URL, ops, cache key, dan dataset **sama**, supaya angkanya bisa dibandingkan langsung. Dua mode HTTP: **Fiber** (fasthttp) dan **net/http** standar, plus opsi **prefork** (`SO_REUSEPORT`).

```bash
go build -o bin/ioimg-go .
./bin/ioimg-go -mode fiber -prefork -addr :8080          # gofiber + prefork
./bin/ioimg-go -mode http -addr :8080                    # net/http
./bin/ioimg-go -mode fiber -nocache -filter bilinear     # ukur kapasitas CPU
go run ./cmd/benchtransform ../../artifacts/datasets/src/photo00.jpg
go test ./...
```

Backend cache: `os.ReadFile` / `os.WriteFile` + rename atomik. Tidak ada io_uring (Go std tidak mengeksposnya) — dan menurut hasil di bawah, itu **bukan** masalah.

## 1. Kebenaran dulu

`go test ./...` → lulus: parsing ops, kanonikalisasi cache key (urutan input tidak berpengaruh), penolakan ops invalid, transform menghasilkan byte.

Endpoint: `/healthz`, `/metrics`, `/{ops}/{name}`; `400` untuk ops invalid, `404` untuk sumber hilang, `X-Ioimg-Cache: HIT|MISS`.

## 2. HTTP hot path (HIT 30 KiB, 8 koneksi keep-alive)


Metodologi: **3 putaran per konfigurasi, ambil median**; 8 koneksi keep-alive; 3 s per putaran; load average mesin 2–5 (ada browser berjalan), jadi angka absolut bisa bergeser ±10% antar sesi — **rasio** antar konfigurasi stabil. Client benchmark membangun request sekali (bukan `format!` per request); versi sebelumnya sempat membatasi throughput client dan membuat angka terlalu rendah.

| Implementasi | RPS (median) | Rentang | p50 |
| --- | --- | --- | --- |
| Rust `tokio` + `std::fs` | **118.010** | 103.399–122.985 | 63 µs |
| **Go Fiber (prefork, 16 proses)** | **79.419** | 77.062–82.368 | 90 µs |
| Go Fiber (1 proses) | 55.152 | 53.736–57.119 | 106 µs |
| Go `net/http` | 37.544 | 30.009–38.403 | 169 µs |

Prefork memberi **+44%** (55k → 79k) dan 16 listener aktif. Go Fiber ≈ 67% throughput Rust tokio pada beban ini.

## 3. Yang menentukan: biaya transformasi (1 core)

2400×1600 → w400 JPEG q80, tanpa HTTP & tanpa cache. Median 3 putaran:

| Pipeline | ms/gambar | Rentang | img/s/core | Output |
| --- | --- | --- | --- | --- |
| **govips `NewThumbnailFromBuffer`** (shrink-on-load) | **9,62** | 9,43–10,14 | **104** | 17 KiB |
| Rust + libvips FFI (`vips_thumbnail_buffer`) | 14,54 | 14,48–14,85 | 69 | 17 KiB |
| govips `NewImageFromBuffer` + `ThumbnailWithSize` | 21,70 | 19,90–23,50 | 46 | 17 KiB |
| Rust `image` (Lanczos3) | 52,90 | 52,20–53,50 | 19 | 30 KiB |
| Go `x/image/draw` Bilinear | 57,00 | 56,30–57,20 | 18 | 17 KiB |
| Go `x/image/draw` CatmullRom | 135,70 | 133,70–151,70 | 7 | 17 KiB |

libvips juga memberi WebP lossy yang lebih kecil dari JPEG (12 vs 17 KiB) — tidak tersedia di `x/image` (tanpa encoder WebP) maupun `image` crate (hanya lossless, 122 KiB).

End-to-end dengan transform tiap request (`-nocache`, 8 koneksi):

| Konfigurasi | RPS | p50 |
| --- | --- | --- |
| Rust tokio + Lanczos | 86 | 92 ms |
| Go Fiber + CatmullRom | 37 | 226 ms |

**libvips 5,5× lebih cepat dari Go `x/image` CatmullRom, dan 2,3× lebih cepat dari jalur govips yang decode-dulu.** Itu sebabnya pemilihan fungsi (`NewThumbnailFromBuffer`) sama pentingnya dengan pemilihan pustaka.

## 4. Kesimpulan

**GoFiber + prefork layak.** Hot path 87k RPS, deploy satu binary, prefork memberi multi-core tanpa konfigurasi. Tapi ada tiga syarat:

1. **Wajib libvips** (`govips`, cgo) **dan pakai jalur shrink-on-load** (`NewThumbnailFromBuffer`, bukan `NewImageFromBuffer` + `ThumbnailWithSize`) — kalau pakai `x/image/draw`, kapasitas CPU 5× lebih rendah dan WebP lossy tidak ada. Ini bukan detail; ini penentu kapasitas.
2. **Prefork + cgo**: Fiber prefork me-*re-exec* binary (bukan fork setelah inisialisasi), jadi `vips.Startup` aman dijalankan di tiap proses anak. Jangan mengganti mekanisme prefork dengan `fork()` manual setelah vips hidup.
3. **Jangan berharap io_uring di Go.** Tidak ada di std; untungnya transformasi CPU mendominasi, jadi keuntungan I/O memang kecil (terbukti di PoC Rust: ring naif kalah dari `std::fs`).

**Rust vs Go, ringkas:**

| Aspek | Rust `tokio` | Go Fiber + prefork |
| --- | --- | --- |
| Hot path HIT | **103k RPS** | 87k RPS |
| Transform | `image` 14 img/s/core → **butuh vips juga** | `x/image` 7,8 → **butuh vips** |
| io_uring | bisa (trait `IoBackend`) | tidak realistis |
| Deploy | 1 binary statis | 1 binary statis |
| Ekosistem image | vips binding / `fast_image_resize` | govips / bimg (matang) |
| Risiko | dua runtime, `SendWrapper` panic, churn pre-1.0 | cgo, `CGO_ENABLED` di build, GC saat transform |

**Rekomendasi:** untuk produk ini, keunggulan Rust (io_uring) **tidak** terwujud karena bottleneck-nya CPU + cache I/O kecil. Kalau tim lebih nyaman di Go, GoFiber + govips + prefork adalah pilihan yang sah — dengan catatan kapasitas ditentukan libvips, bukan bahasa. Kalau tetap di Rust, ganti `image` dengan binding libvips sebelum bicara kapasitas.

## 5. Belum dibuktikan di sisi Go

- govips di dalam server HTTP (PoC ini mengukur govips terpisah; integrasi butuh `vips.Startup` + concurrency tuning)
- AVIF (libvips bisa, butuh modul heif — tersedia di mesin ini)
- Eviction/LRU, signed URL, rate limit
- Prefork + graceful shutdown, dan perilaku saat prefork di container (CPU limit, `GOMAXPROCS`)
- Soak 72 jam dan kebocoran memori govips

## 5. Bench di atas starterkit `kinstack`

PoC di direktori ini polos (Fiber langsung). Varian yang memakai starterkit
`kinstack` (Fiber v3 + Postgres + Valkey + helmet/CSP + session + CSRF +
Inertia + access log) diukur terpisah, dengan handler yang sama persis:

- hot path HIT: **19.692 RPS** vs **63.791 RPS** baseline (31%, p50 351 vs 99 µs)
- tanpa session (route publik dipasang sebelum session): 47.094 RPS (72%)
- tanpa session + tanpa access log: 70.628 RPS (**101%** — setara baseline)
- cache MISS: rasio 1,01 (overhead middleware hilang di bawah biaya transform)
- temuan operasional: session+CSRF menulis **2 key Valkey per request** (TTL 24h)

Detail, metodologi, dan cara reproduksi: [`../../docs/BENCH-GO-STARTERKIT.md`](../../docs/BENCH-GO-STARTERKIT.md).
