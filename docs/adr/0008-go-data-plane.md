# ADR-0008 — Implementasi data plane dalam Go (pivot dari Rust)

- Status: **Accepted** (2026-11-25)
- Menggantikan sebagian: [ADR-0001](0001-rust-axum.md), [ADR-0007](0007-axum-on-compio.md)
- Konteks: keputusan pengguna setelah PoC Rust vs Go dan bench starterkit
  [`kinstack`](../BENCH-GO-STARTERKIT.md)

## Keputusan

1. **Data plane ioimg diimplementasikan dalam Go** (`service/`), bukan Rust.
   HTTP stack memakai `net/http` standar, tanpa framework dan tanpa middleware
   session/CSRF/Inertia.
2. **Imaging default adalah pure Go** (`image`, `x/image/draw`,
   `gen2brain/webp`, `gen2brain/avif` berbasis WASM) supaya build portabel
   (Windows/macOS/Linux, `CGO_ENABLED=0`). Backend libvips native
   (`govips`) tetap menjadi jalur performa produksi dan dapat ditambahkan di
   belakang antarmuka paket `internal/imaging` yang kecil.
3. **Cache L1 = disk lokal** dengan key hash dari versi pipeline, ops
   kanonik, source kanonik, dan format output. Single-flight per key
   mencegah thundering herd.
4. **Signed URL = HMAC-SHA256** di path, constant-time verify, expiry, dan
   rotasi key. Tidak ada cookie/session di data plane.
5. **Control plane tetap di `kinstack`** (Go, Fiber v3). Data plane dipisah
   sebagai proses/binary sendiri dan tidak melewati rantai middleware dashboard.

## Alasan

- Bench: data plane HIT kehilangan **69% throughput** dan p50 naik **3,5×**
  bila route gambar lewat session+CSRF+access-log kinstack; 2 key Valkey
  dibuat per request (TTL 24 jam) meski client tidak pernah mengirim cookie.
- libvips (bukan bahasa) yang menentukan kecepatan transform: govips
  9,62 ms/gambar vs Rust FFI 14,54 ms vs `image` Rust 52,9 ms. Go + libvips
  adalah pilihan tercepat yang terukur.
- Go memberi deployment satu binary, `CGO_ENABLED=0`, dan tooling yang sudah
  dipakai tim (kinstack).
- io_uring (alasan awal memilih Rust) tidak terbukti mengalahkan `std::fs`
  untuk objek cache kecil; bottleneck ada di CPU transform.

## Konsekuensi

- Dokumen PRD/ARCHITECTURE/PERFORMANCE yang menyebut Rust/axum/io_uring tetap
  sebagai riwayat desain; implementasi aktual mengikuti ADR ini dan
  [`service/README.md`](../../service/README.md).
- AVIF via WASM lambat (0,5–2 s/gambar pada mesin uji). Produksi:
  `IOIMG_DISABLE_AVIF=true` sampai backend native tersedia.
- L2 S3/R2, purge CDN fan-out, dan warm/batch API sudah diimplementasikan.
  Quota/billing dan control plane project/API key tetap M2.
- Backend libvips tersedia via build tag `vips` (`Dockerfile.vips`); default
  tetap pure Go agar portabel.
- Prefork tidak dipakai; scaling horizontal lewat replika di belakang LB.

## Alternatif yang ditolak

| Alternatif | Alasan ditolak |
| --- | --- |
| Rust + axum + io_uring (ADR-0001/0002) | Lebih lambat pada transform nyata, biaya implementasi/operasional lebih tinggi, io_uring tidak memberi keuntungan terukur untuk cache kecil |
| axum di atas compio (ADR-0007) | Risiko produksi tinggi, ekosistem belum matang, tidak menjawab bottleneck CPU |
| Go + Fiber untuk data plane | Framework tidak menambah nilai pada hot path dan mengundang middleware yang mahal; `net/http` cukup dan lebih mudah diaudit |
| Satu proses kinstack untuk semua | Terbukti 3,35× lebih lambat di HIT dan membuat kapasitas Valkey tumbuh tanpa batas |
