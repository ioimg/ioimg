# Benchmark

Ringkasan hasil yang layak di-track. Data mentah, log, dan dataset ada di
`artifacts/` (gitignored); lihat [`ARTIFACTS.md`](../ARTIFACTS.md).

| Topik | Dokumen | Ringkasan |
| --- | --- | --- |
| Rust: tokio vs io_uring | [`../research/rust-uring/README.md`](../../research/rust-uring/README.md) | io_uring naif tidak menang untuk objek kecil; CPU transformasi adalah bottleneck |
| Go: Fiber + prefork + libvips | [`../research/go-fiber/README.md`](../../research/go-fiber/README.md) | Fiber prefork ±67% throughput Rust tokio; libvips 3–5× lebih menentukan dari bahasa |
| Middleware starterkit | [`../BENCH-GO-STARTERKIT.md`](../BENCH-GO-STARTERKIT.md) | Middleware global menurunkan HIT ~69%; access log + session/CSRF penyebab utama |
| io_uring: socket vs cache read vs batch | [`io-uring.md`](io-uring.md) | Socket 2× hemat CPU; baca cache per-request **kalah 22–29%**; batch warming 2× lebih cepat |
| Target & metode data plane | [`../PERFORMANCE.md`](../PERFORMANCE.md) | Metodologi, target angka, dan batasan pengukuran |

## Aturan

- Setiap angka harus punya perintah reproduksi dan versi kode.
- Simpan log mentah di `artifacts/reports/`, bukan di git.
- Tulis kondisi mesin, jumlah koneksi, payload, dan median/rentang.
- Jangan membandingkan angka dari sesi/mesin berbeda tanpa menyebutkan rasio.
