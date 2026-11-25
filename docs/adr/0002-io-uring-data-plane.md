# ADR-0002 — io_uring diisolasi di data plane, bukan HTTP stack

- **Status:** Revised — lihat [ADR-0007](0007-axum-on-compio.md)
- **Tanggal:** 2025-11 (revisi 2025-11)
- **Konteks:** permintaan produk "Rust + axum + io_uring enabled", target latensi agresif.

> **Revisi penting.** Butir 4 di bawah ("`hyper`+`axum` di atas runtime completion-based butuh port non-trivial") **sudah tidak akurat**: `cyper-axum` menyediakan `axum::Router` di atas `hyper` dengan driver io_uring (`compio`), via `cyper_axum::serve()` sebagai pengganti `axum::serve`. Keputusan "io_uring diisolasi di data plane" **tetap berlaku sebagai default produksi**, tetapi kini menjadi *satu dari dua profil runtime* — bukan satu-satunya jalan. Jalur fast path dievaluasi di [ADR-0007](0007-axum-on-compio.md).

## Konteks

Keinginan awal: seluruh service berjalan di atas io_uring agar "cepat". Kenyataan teknis:

1. **`axum` bukan runtime.** Ia berjalan di atas `hyper`, yang berjalan di atas `tokio`.
2. **`tokio` memakai epoll** (readiness model), bukan io_uring (completion model).
3. **`tokio-uring` bukan `tokio` + io_uring.** Ia runtime terpisah dengan API sendiri (buffer harus hidup sampai completion), masih berstatus muda, dan tidak drop-in untuk `hyper`.
4. **Runtime io_uring-native (`monoio`, `compio`)** memakai thread-per-core dan API completion-based. `monoio` tidak punya adaptor `hyper`/`axum` resmi; untuk `compio` ada (`cyper-axum`) — lihat catatan revisi di atas. Yang tetap benar: berpindah ke `compio` bukan sekadar menukar runtime, melainkan kehilangan ekosistem tokio (`aws-sdk-*`, `sqlx`, timer `tower`) dan mengubah model eksekusi menjadi `!Send` thread-per-core.
5. **io_uring sering diblokir** oleh seccomp default Docker/Kubernetes (`io_uring_enter` diblokir karena risiko escape container). Banyak platform managed tidak mengizinkannya sama sekali.

Artinya: "axum + io_uring" sebagai satu stack **bukan** hal yang bisa diaktifkan dengan flag. Memaksakannya berarti mengganti HTTP stack dan menerima risiko + kehilangan portabilitas.

Di sisi lain, profil beban ioimg menunjukkan bahwa io_uring memang berguna di **sebagian** jalur:

- Baca/tulis cache NVMe pada cache hit (banyak operasi kecil, `statx`+`read`+`write`).
- Pengiriman body besar ke socket (zero-copy `splice`).
- Menghindari `spawn_blocking` per operasi file yang dipakai `tokio::fs`.

## Keputusan

**Pisahkan dua lapisan:**

1. **HTTP/API plane:** `axum` + `hyper` + `tokio` (epoll). Stabil, portabel, ekosistem penuh.
2. **I/O data plane:** trait `IoBackend` dengan dua implementasi:
   - `TokioBackend` (default portabel, `tokio::fs` + body stream),
   - `UringBackend` (opt-in, `io-uring` crate langsung: `openat`/`statx`/`read`/`write`/`splice`/`fsync`, `O_DIRECT`, registered buffers & files).

Pemilihan backend:
- `io.backend = "auto"` → probe di **child process** (karena kegagalan seccomp bisa berupa SIGSYS, bukan errno), dengan timeout; fallback otomatis ke `tokio` + log + metric.
- `io.backend = "tokio"` → paksa portabel.
- `io.backend = "uring"` → gagal saat startup bila tidak tersedia (untuk operator yang ingin jaminan).

io_uring **tidak** dipakai untuk accept/recv HTTP pada profil `runtime-tokio` (default). Untuk "full io_uring HTTP" tersedia profil terpisah `runtime-compio` dengan `cyper-axum`, dinilai di [ADR-0007](0007-axum-on-compio.md) dan hanya dipromosikan setelah gate benchmark M0 terpenuhi.

## Konsekuensi

**Positif**
- Nilai io_uring tetap didapat di jalur yang benar-benar diuntungkan (cache I/O + egress besar) tanpa mengganti HTTP stack.
- Satu binary berjalan di mana saja; container dengan seccomp ketat tetap berfungsi.
- Risiko stabilitas terkurung di satu modul (`ioimg-io`), dengan implementasi pembanding sebagai safety net.
- Bisa mengukur A/B secara jujur: backend yang sama, workload yang sama.

**Negatif**
- Tidak ada klaim "seluruh service io_uring". Materi pemasaran harus presisi.
- Dua implementasi I/O = dua jalur bug. Mitigasi: test kontrak yang sama dijalankan untuk kedua backend.
- Keuntungan io_uring untuk workload CPU-bound (transformasi) tetap nol; ekpektasi harus dikelola.

## Metrik keputusan (dari M0)

Keputusan "auto = uring bila tersedia" **hanya** dipertahankan bila:
- syscall/request pada cache hit turun ≥ 30%, **dan**
- p99 cache-hit turun ≥ 15% pada W2 (1 MB), **dan**
- tidak ada regresi throughput > 5% pada W4/W5 (CPU-bound),
- serta crash/errno rate pada 24 jam soak = 0.

Bila tidak terpenuhi → `auto` menjadi `tokio`, `uring` tetap tersedia sebagai opt-in.

## Alternatif yang ditolak

| Alternatif | Alasan |
| --- | --- |
| Ganti seluruh stack ke `monoio`/`compio` | Kehilangan `axum`/`tower` ecosystem; menulis ulang middleware auth/rate-limit; risiko tinggi, manfaat belum terbukti. **Catatan revisi:** untuk `compio`, `axum` kini tersedia via `cyper-axum`; penolakan permanen tidak tepat → dievaluasi di ADR-0007. |
| Pakai `tokio-uring` sebagai runtime | API terpisah, kematangan terbatas, tidak kompatibel dengan `hyper`; tidak memenuhi janji "axum + io_uring". |
| io_uring untuk HTTP accept/recv (multishot) | Nilai kecil pada beban gambar (koneksi keep-alive, body request kecil) dibanding cache/egress. |
| Hanya epoll, tanpa io_uring | Kehilangan diferensiasi dan keuntungan I/O cache; permintaan produk tidak terpenuhi. |
| `spawn_blocking` + `pread` | Sederhana, tetapi thread blokir & context switch mahal pada egress tinggi (baseline pembanding). |
