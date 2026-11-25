# ADR-0010 — Cakupan io_uring: socket dan batch, bukan baca cache per-request

- Status: **Accepted** (2026-11-25)
- Memperjelas: [ADR-0008](0008-go-data-plane.md) ("io_uring tidak terbukti
  mengalahkan `std::fs` untuk objek cache kecil")
- Data: [`../benchmarks/io-uring.md`](../benchmarks/io-uring.md)
- Harness: [`../../research/io-uring/`](../../research/io-uring/)

## Konteks

Nama produk berasal dari io_uring, sehingga ada tekanan untuk memakainya di
semua jalur panas — termasuk baca cache L1 per-request. Sebelum itu
dijadikan klaim, tiga jalur diukur dengan handler produksi yang sama:

1. **Transport HTTP** (HIT-L1, body 12,8 KB, 8 keep-alive conns).
2. **Baca cache per-request** — L1 adalah cache disk, jadi setiap HIT
   melakukan `os.ReadFile` + `os.Stat`.
3. **Batch cache-warming** — 1.024 file × 16 KB, satu `io_uring_enter` per
   batch, fixed files + fixed buffers.

Percobaan kedua diuji pada konfigurasi paling agresif yang tersedia di Go:
linked `openat → read → close` (satu enter), slot file teregistrasi, buffer
teregistrasi, tanpa copy perantara di adapter.

## Keputusan

1. **io_uring dipakai di jalur network** (accept/recv/send) dengan engine
   native; epoll tetap *fallback* wajib saat `io_uring_setup` diblok
   (seccomp/gVisor/k8s policy) — probe dijalankan di child process.
2. **Baca cache per-request tetap blocking**, dan memakai **satu fd**:
   `os.Open` + `f.Stat` + `io.ReadFull`, bukan `os.ReadFile` + `os.Stat`.
3. **io_uring untuk file hanya dipakai untuk batch job** (warm/pre-warm,
   reindex, migrasi L2→L1) dengan depth 16–128 dan satu enter per batch.
4. **Cold read ber-`O_DIRECT` tidak memakai io_uring**; cukup N goroutine
   blocking.

## Alasan

- Transport: celeris io_uring **58.000 rps / 110 µs CPU per request** vs
  `net/http` **29.800 / 291** — 10 GbE tercapai dengan 10,7 core vs 28 core.
- Baca cache per-request (cache dir di ext4): io_uring linked+fixed
  **51.374 rps** vs `os.ReadFile` **65.876** (−22%; varian 3-enter −29%),
  dan CPU per request 139 µs vs 105 µs. Selisih mikro hanya +0,6 µs/read
  (3,0 vs 2,4 µs), sementara CPU per request naik 34 µs: biayanya **wakeup
  thread**, bukan syscall — worker ring merebut CPU dari loop yang di-pin.
  Di tmpfs selisihnya memburuk menjadi −33% / 2,1× per read (workqueue).
- Batch page-cache: io_uring **428.000 files/s** vs **205.000** (2,0×).
- Batch device-bound: io_uring **106.000** vs **137.000 files/s** dengan 16
  goroutine (−29%). Queue depth sudah dicapai tanpa io_uring.
- Satu fd (`os.Open`+`f.Stat`+`io.ReadFull`) menghapus satu pembukaan per HIT:
  CPU/req 101 µs vs 105 µs, satu syscall lebih sedikit, nol dependensi,
  nol risiko.

## Konsekuensi

- Klaim produk yang boleh dipublikasikan: *"completion I/O di jalur network
  dan batch cache-warming; baca cache per-request sengaja blocking karena
  22–29% lebih cepat — terdokumentasi, bukan kebetulan."* Klaim "semua hot path
  memakai io_uring" **tidak** dipakai.
- Tidak ada dependensi io_uring di jalur produksi per-request, jadi build tetap
  `CGO_ENABLED=0` dan jalan di platform yang memblok `io_uring_setup`.
- Batch job memakai `github.com/miretskiy/dio/v2/ringo` (Linux-only, butuh
  kernel dengan `IORING_FEAT_LINKED_FILE`, ≥5.18) — dipisah di binary
  `ioimgctl`, bukan di server.
- Regression: setiap perubahan pada cache read atau transport harus diuji ulang
  dengan harness di `research/io-uring/`; angka di ADR ini adalah baseline.

## Alternatif yang ditolak

- **io_uring di semua jalur** — kalah 22–29% di jalur yang paling sering dieksekusi
  (HIT-L1) dan mustahil untuk `crypto/tls`.
- **Ring per loop, submit inline** (model `compio`) — itu satu-satunya
  konfigurasi yang masih bisa membalik hasil baca per-request, tetapi
  memerlukan HTTP stack yang menguasai ring-nya sendiri; tidak tersedia di
  `net/http` maupun lewat API handler celeris.
- **Fiber prefork sebagai jalan hemat core** — menggandakan cache L1 dan cold
  start 16×; celeris multi-loop dalam satu proses menyimpan satu cache.
  Keputusan framework/transport dicatat di ADR terpisah.
