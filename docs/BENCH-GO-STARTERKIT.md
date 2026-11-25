# Bench: ioimg di atas starterkit `kinstack`

Status: selesai, 2026-11-24. Menjawab pertanyaan: **kalau ioimg dibangun di atas
starterkit `kinstack`, berapa penurunan throughput/latency-nya?**

TL;DR — untuk hot path (cache HIT), starterkit menurunkan throughput **~69%**
(63,8k → 19,7k RPS) dan menaikkan p50 **3,5×** (99 µs → 351 µs). Penyebabnya
bukan Fiber, bukan handler, tapi **rantai middleware global**: access log
sinkron (+50% CPU) dan session+CSRF yang menulis **2 key Valkey per request**
(+60% CPU, +200 µs latency). Kalau route gambar dipasang **sebelum** session
(mount `early`), hasilnya 47k RPS (72%); kalau access log juga dimatikan,
70,6k RPS — **setara** baseline Fiber polos.

---

## 1. Setup

`kinstack` **tidak diubah**. Repo asli di `~/Projects/work/oss/kinstack` disalin
ke `artifacts/snapshots/kinstack/` (gitignored) lewat `tools/fetch-kinstack.sh`,
lalu tambahan bench ditambahkan **di salinan**:

| File di `artifacts/snapshots/kinstack/` | Isi |
| --- | --- |
| `internal/benchioimg/` | handler ioimg PoC, disalin apa adanya dari `research/go-fiber/internal/ioimg` (URL/ops/cache key identik) |
| `cmd/ioimgkstack/` | mount handler ke app kinstack yang asli (`bootstrap.Run`) |
| `cmd/ioimgv3/` | baseline Fiber v3 polos, tanpa middleware kinstack |
| `internal/bootstrap/{app,bootstrap,middleware,middleware_security}.go` | hook `ExtraRoutes`, `KSTACK_EARLY_ROUTES`, dan toggle `KSTACK_SKIP_*` untuk atribusi |

Diff lengkap: [`BENCH-kinstack.patch`](./BENCH-kinstack.patch).

Infrastruktur pendukung (container sementara, bukan milik kinstack):
Postgres `127.0.0.1:55433`, Valkey `127.0.0.1:56379` (password
`kinstack-local-password`), migrasi dijalankan lewat `go run ./cmd/migrate up`.
Config dipakai apa adanya dari `.env` kinstack; hanya `MEDIA_ENABLED=false`,
`UMAMI_ENABLED=false`, `OPENOBSERVE_ENABLED=false`, `GLITCHTIP_DSN=` yang
dimatikan agar tidak ada trafik keluar saat bench.

Beban kerja: `GET /w_400,f_jpg,q_80/photo00.jpg` → **HIT, 17.751 byte**
(salinan persis benchmark PoC). Client: `research/rust-uring/target/release/bench`, 8 koneksi
keep-alive.

## 2. Metodologi

Mesin ini dipakai bersama browser (load average berayun 1–8), jadi **RPS absolut
tidak bisa dipercaya antar waktu**. Karena itu setiap varian diukur
**interleaved A/B** terhadap baseline di jendela waktu yang sama:

```
for round in 1..N:  bench(v3); bench(kinstack)   # bergantian
```

Plus metrik kedua yang tahan noise: **CPU per request** dari
`/proc/<pid>/stat` (`utime+stime` sebelum/sesudah run, dibagi jumlah request).
Server di-pin ke CPU 0–7, client ke 8–15 (`taskset`), warmup 3 s, 4 putaran ×
3 s, diambil median. Valkey di-`FLUSHALL` sebelum tiap varian supaya
key-expiry tidak ikut terukur.

Baseline `v3`: Fiber v3, `Config` sama dengan kinstack (AppName, BodyLimit 12 MB,
Read/Write/Idle timeout), hanya 3 route (`/healthz`, `/metrics`, `/:ops/:name`).

## 3. Hasil hot path (HIT 17.751 B)

Kolom `cpu×` = CPU per request relatif terhadap baseline v3 di jendela yang sama.

| Varian | cpu/req | RPS | p50 | cpu× | RPS |
| --- | --- | --- | --- | --- | --- |
| baseline Fiber v3 | 99 µs | 63.791 | 99 µs | 1,00 | 100% |
| kinstack transport saja¹ | 94 µs | 70.628 | 95 µs | **1,03** | **101%** |
| transport + access log | 143 µs | 47.094 | 140 µs | 1,52 | 72% |
| transport + session | 188 µs | 25.838 | 291 µs | 1,68 | 46% |
| + CSRF + limiter | 197 µs | 25.250 | 292 µs | 1,91 | 41% |
| + Inertia/audit context | 199 µs | 24.613 | 283 µs | 2,12 | 38% |
| **full kinstack** | **270 µs** | **19.692** | **351 µs** | **2,73** | **31%** |

¹ helmet + recoverer + requestid, tanpa access log. Artinya **header keamanan,
panic recovery, dan request-id gratis**. Yang mahal adalah log dan session.

Ladder-nya monoton dan cukup aditif (±0,1× noise):

- access log (`log.HTTPMiddleware`) = **+0,5× CPU**. Dua baris JSON zerolog per
  request, ditulis sinkron ke file (lumberjack) sebelum response dikirim.
- session middleware = **+0,65× CPU dan +190 µs p50**. Bukan CPU-nya yang
  dominan, tapi **round-trip Valkey yang memblokir**.
- CSRF + limiter = +0,23×.
- Inertia + audit context (`ShareWith` 7 props, resolusi permission, touch
  session) = +0,21×.

Full stack = **2,73× CPU, 31% throughput, p50 3,5×**.

## 4. Temuan yang paling penting: 2 key Valkey per request

Bukan sekadar lambat — ini bug kapasitas. Session middleware Fiber v3
**selalu** memanggil `Storage.Set()` di akhir request, termasuk untuk request
tanpa cookie yang sama sekali tidak butuh session. CSRF menambah satu tulis
lagi. Terbukti:

```
FLUSHALL → dbsize=1.920
bench 3 s → n=39.165 request, rps=13.055
dbsize=81.548          # ≈ 2 key × 39.165 request
TTL key              = 86.400 s (SESSION_IDLE_TIMEOUT=24h)
memori               ≈ 280 B/key  (1.589.334 key = 446 MB)
```

Konsekuensi kalau route gambar publik ikut rantai global kinstack:

| Trafik | Key/hari | Memori Valkey/hari |
| --- | --- | --- |
| 100 rps | 17,3 juta | ~4,8 GB |
| 1.000 rps | 172,8 juta | **~48 GB** |
| 10.000 rps | 1,7 miliar | mustahil |

Semua key itu *orphan*: client tidak pernah mengirim balik cookie-nya, jadi
tidak ada satu pun yang berguna. Di bench ini Valkey sempat menampung 1,59 juta
key/446 MB hanya dari ~20 menit pengujian.

## 5. Cache MISS (transform ikut dihitung)

`-nocache`, 8 koneksi, transform `x/image` CatmullRom (jalur lambat, sengaja):

| Varian | cpu/req | RPS | p50 |
| --- | --- | --- | --- |
| baseline v3 | 447 ms | 19 | 447 ms |
| kinstack full | 454 ms | 19 | 420 ms |

**Rasio 1,01 / 100%.** Saat transform jalan, biaya middleware hilang dalam
noise. Jadi overhead starterkit hanya relevan pada traffic yang seharusnya
murah — justru traffic mayoritas produk image proxy (hit ratio 95–99%).

## 6. Fiber v2 vs v3, dan prefork

| Konfigurasi (1 proses, HIT) | cpu/req | RPS |
| --- | --- | --- |
| Fiber **v2** (PoC `research/go-fiber`) | 132 µs | 47.333 |
| Fiber **v3** (baseline) | 125 µs | 51.560 |

v3 ~9% lebih cepat dari v2 pada beban sama — versi framework bukan faktor.

| Prefork 16 proses | RPS (median 3) | p50 |
| --- | --- | --- |
| Fiber v3 | **91.249** (86.589–92.746) | 75 µs |
| Fiber v2 | **90.619** (86.383–93.060) | 77 µs |

Prefork ≈ 1,8× proses tunggal. **kinstack tidak memakai prefork**:
`bootstrap.listen` memanggil `application.Listen(addr)` tanpa
`ListenConfig.EnablePrefork`. Untuk produksi artinya skalanya horizontal
(banyak replika/container di belakang LB), bukan satu proses multi-core.

## 7. Jawaban untuk ioimg

**Starterkit ini bagus untuk control plane, salah untuk data plane.**

Angka di atas adalah *satu proses* kinstack: 19,7k RPS HIT. Dengan 4 replika
saja (LB) itu ~79k RPS — cukup untuk hampir semua kasus. Jadi pilihannya bukan
"buang kinstack", tapi **jangan lewatkan trafik gambar ke rantai middleware
dashboard**:

1. **Mount route publik sebelum session.** `KSTACK_EARLY_ROUTES=1` di bench
   membuktikannya: 47k RPS (72%) dengan access log tetap hidup, 70,6k RPS
   (101%) tanpa log. Di kinstack asli, posisi ini persis setelah
   `registerTransportMiddleware` — sama seperti aset statis (`/build/*`) yang
   memang sudah sengaja dipasang sebelum session.
2. **Jangan pakai CSRF/session/Inertia untuk `GET /{sig}/{ops}/{source}`.**
   Ketiganya tidak punya makna untuk signed URL dari CDN. Kalau perlu auth,
   gunakan HMAC di path (sudah ada di desain ioimg), bukan cookie.
3. **Access log: async atau sampling.** Ini item termahal setelah session
   (+50% CPU, 2 baris JSON sinkron per request). Untuk data plane cukup
   counter metrik + log error; akses sukses tidak perlu per-request.
4. **Data plane sebaiknya binary/proses terpisah** dari dashboard
   (kinstack untuk auth, API key, billing, admin; ioimg untuk
   fetch→transform→cache). Dua proses = dua profil latency, dua skala, dan
   dashboard tidak bisa menjatuhkan serving gambar.
5. **Valkey bukan hot path.** Kalau tetap satu proses, batasi middleware
   dengan `Next` berdasarkan prefix path, bukan menambah storage.
6. **Prefork/SO_REUSEPORT** belum ada di kinstack; kalau butuh multi-core per
   host, tambahkan `EnablePrefork` (aman: re-exec, bukan fork setelah init) atau
   jalankan N container.

Estimasi kapasitas dengan route publik di depan session (satu proses, 17 KiB,
mesin 16 core ini): **~47k RPS dengan access log, ~70k tanpa log**, p50
140 µs / 95 µs. Cache miss tetap ditentukan libvips (~104 img/s/core), bukan
HTTP stack.

## 8. Batasan pengukuran

- Satu host, dipakai bersama browser (load average 1–8). RPS absolut bergeser
  ±30% antar jam; **rasio interleaved** stabil dan itu yang dipakai.
- Payload HIT 17 KiB (JPEG q80). Payload lebih besar → biaya I/O naik, rasio
  middleware turun.
- Hit ratio 100%; campuran HIT/MISS akan mengecilkan dampak middleware.
- Jalur govips/libvips **tidak** diuji di varian kinstack (hanya `x/image`
  CatmullRom), karena tujuannya mengukur overhead HTTP, bukan codec.
- `-race`/pprof tidak dipakai; atribusi CPU dari `/proc`, bukan sampling
  profiler. Angka per-middleware ±0,1×.

## 9. Reproduksi

```bash
# 1. salinan starterkit + tambahan bench (repo asli tidak disentuh)
tools/fetch-kinstack.sh ~/Projects/work/oss/kinstack
cd artifacts/snapshots/kinstack
patch -p1 < ../../../docs/BENCH-kinstack.patch  # hook ExtraRoutes + toggle KSTACK_*
ln -s ~/Projects/work/oss/kinstack/node_modules node_modules && bun x vite build

# 2. infra
docker run -d --rm --name ioimg-bench-pg -e POSTGRES_USER=kinstack \
  -e POSTGRES_PASSWORD=kinstack -e POSTGRES_DB=kinstack \
  -p 127.0.0.1:55433:5432 postgres:17-alpine
docker run -d --rm --name ioimg-bench-valkey -p 127.0.0.1:56379:6379 \
  valkey/valkey:8-alpine valkey-server --requirepass kinstack-local-password
DATABASE_URL='postgres://kinstack:kinstack@127.0.0.1:55433/kinstack?sslmode=disable' \
  go run ./cmd/migrate up

# 3. build
go build -o /tmp/ioimgkstack ./cmd/ioimgkstack
go build -o /tmp/ioimgv3    ./cmd/ioimgv3

# 4. jalankan varian
APP_PORT=18081 DATABASE_URL=... VALKEY_PORT=56379 \
VALKEY_PASSWORD=kinstack-local-password MEDIA_ENABLED=false \
UMAMI_ENABLED=false OPENOBSERVE_ENABLED=false GLITCHTIP_DSN= \
KSTACK_EARLY_ROUTES=1 /tmp/ioimgkstack \
  -src ../../../artifacts/datasets/src -cache ../../../artifacts/cache/go

/tmp/ioimgv3 -addr :18080 -src ../../../artifacts/datasets/src -cache ../../../artifacts/cache/go
../../../research/rust-uring/target/release/bench 127.0.0.1:18081 /w_400,f_jpg,q_80/photo00.jpg 8 3
```

Toggle yang tersedia di salinan: `KSTACK_EARLY_ROUTES`, `KSTACK_SKIP_LOG`,
`KSTACK_SKIP_CONTEXT`, `KSTACK_SKIP_SESSION`, `KSTACK_SKIP_CSRF`,
`KSTACK_SKIP_LIMITER`, `KSTACK_SKIP_OTEL`.
