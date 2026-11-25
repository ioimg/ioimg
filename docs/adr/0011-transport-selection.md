# ADR-0011 — Pemilihan transport: celeris io_uring → fiber → fiber prefork

- Status: **Accepted** (2026-11-25)
- Memperjelas: [ADR-0008](0008-go-data-plane.md) ("tanpa framework")
- Melengkapi: [ADR-0010](0010-io-uring-scope.md) (cakupan io_uring)
- Data: [`../benchmarks/io-uring.md`](../benchmarks/io-uring.md)
- Kode: `service/internal/transport/`

## Konteks

ADR-0008 memilih `net/http` karena framework dianggap tidak menambah nilai dan
mengundang middleware mahal. Setelah transport alternatif diukur dengan
`handleImage` yang sama persis (bukan server hello-world), asumsi itu tidak lagi
benar untuk transport: **engine, bukan syscall baca, yang menentukan**.

HIT 17.751 B, 8 keep-alive, cache di ext4, 16 core, 5 putaran round-robin:

| Transport | RPS | p50 | p90 | µs CPU/req |
| --- | --- | --- | --- | --- |
| `net/http` + `os.ReadFile` | 29.898 | 245 | 408 | 232 |
| Fiber v3 + `os.ReadFile` | 42.608 | 140 | 321 | 160 |
| Fiber v3 + io_uring (baca cache) | 54.728 | 130 | 173 | 125 |
| celeris `epoll` | 54.328 | 104 | 299 | 106 |
| celeris `std` (bridge net/http) | 23.019 | 330 | 531 | 290 |
| **celeris `io_uring` + satu fd** | **65.442** | **97** | **149** | **88** |

catatan penting: io_uring untuk **baca cache per-request** tidak selalu kalah.
Di celeris (loop di-pin) ia rugi 26%; di Fiber justru menang 28% dengan p90
321 → 173 µs; di `net/http` netral. Karena itu pilihan baca cache harus
mengikuti transport, bukan sebaliknya.

## Keputusan

Urutan tier, dipilih otomatis saat start:

1. **celeris + engine `io_uring`, baca cache satu fd** (`open`+`fstat`+`read`).
   Default. Socket lewat io_uring, baca cache tetap blocking karena loop-nya
   di-pin.
2. **Fiber v3 tanpa prefork + baca cache io_uring** — dipakai bila engine
   io_uring celeris gagal start (kernel < 5.19, `io_uring_setup` diblok untuk
   socket, kebijakan seccomp) tetapi file masih bisa dibaca lewat io_uring.
3. **Fiber v3 prefork + baca cache satu fd** — dipakai bila io_uring tidak
   tersedia sama sekali (`IOIMG_CACHE_READ` tetap bisa memaksa mode lain).

`net/http` tetap ada sebagai tier eksplisit (`IOIMG_TRANSPORT=nethttp`) dan
sebagai referensi/rollback.

Override: `IOIMG_TRANSPORT=celeris|fiber|nethttp` mematikan rantai fallback,
`IOIMG_PREFORK=1|0` memaksa/mematikan prefork, `IOIMG_CACHE_READ=os|openstat|uring|uringlink`
memaksa mode baca.

Kegagalan start (bukan kegagalan bind port) ditandai `errEngineUnavailable` agar
rantai turun ke tier berikutnya; kegagalan bind tidak diulang di tier berikutnya
karena tidak mungkin berhasil.

## Alasan

- 2,19× `net/http` tanpa mengubah satu baris handler: adapter transport menulis
  langsung ke konteks engine (`celeris.Context`, `fiber.Ctx`), bukan lewat
  `celeris.Adapt`/`net/http` bridge yang menyalin body.
- Semua transport memakai `Server.dispatch` yang sama, jadi `/healthz`,
  `/readyz`, `/metrics`, `/v1/*` dan penanganan error berperilaku identik di
  ketiga tier; body dan ETag byte-identik (diuji dengan sha256).
- Prefork memberi 72,7k RPS per host pada tier terakhir, tetapi memecah state
  per proses; ia sengaja diletakkan paling bawah dan memperingatkan saat aktif.
- Build tetap jalan tanpa cgo; io_uring diakses lewat `ringo`/celeris (Linux),
  dan probe `UringAvailable()` (membuat ring kecil sekali) membuat binary yang
  sama aman di kernel tanpa io_uring.

## Konsekuensi

- **Ketergantungan baru**: `github.com/goceleris/celeris` (Apache-2.0),
  `github.com/gofiber/fiber/v3`, `github.com/miretskiy/dio/v2`. Semua
  Linux-oriented untuk io_uring, tetapi `IOIMG_TRANSPORT=nethttp` tetap
  portabel; `go` directive naik ke versi yang diminta celeris.
- **Prefork memecah state in-process**: `usage`, sisa kuota, limiter per-IP, dan
  single-flight menjadi per proses. Di tier 3 log memperingatkan hal ini;
  produksi dengan kuota aktif harus memakai tier 1/2 atau memindahkan counter
  ke control plane.
- Dua adapter transport = dua tempat untuk bug header/ETag. Mitigasi: keduanya
  hanya menerjemahkan `http.ResponseWriter`/`*http.Request` ke engine; seluruh
  logika tetap di `handleImage`, dan `go test ./internal/server` menguji jalur
  net/http.
- fasthttp menormalkan `//` di path; adapter Fiber membaca
  `Request().Header.RequestURI()` mentah agar `/{ops}/https://host/...` tidak
  rusak. Wajib ada regression test bila adapter disentuh.
- Benchmark transport/engine harus diulang setiap kali salah satu engine naik
  versi (celeris masih pra-2.0 dan perilakunya bisa berubah antar minor).

## Alternatif yang ditolak

- **Tetap `net/http`** — 2,19× lebih lambat pada beban yang sama, dan tidak ada
  jalur io_uring sama sekali.
- **Fiber sebagai default** — 1,20× di bawah celeris io_uring, dan prefork
  memecah state.
- **celeris `Adapt`/bridge `net/http`** — menyalin seluruh body per request;
  celeris `std` terukur 23,0k vs 29,9k `net/http` langsung.
- **io_uring untuk semua jalur** — baca cache lewat io_uring merugikan 26% di
  celeris (lihat ADR-0010).
