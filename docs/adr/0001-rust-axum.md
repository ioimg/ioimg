# ADR-0001 — Rust + axum sebagai fondasi service

- **Status:** Accepted
- **Tanggal:** 2025-11
- **Konteks:** PRD (performa, keamanan decoder, single binary), tim kecil.

## Konteks

Image proxy adalah beban campuran: banyak koneksi I/O (fetch, cache, kirim body) + ledakan CPU (decode/encode). Service ini juga men-decode file dari pihak tak dikenal, jadi memory safety punya nilai keamanan langsung.

Butuh: latensi rendah dengan tail yang stabil, konsumsi memori terprediksi, single binary untuk self-host, dan ekosistem image processing yang memadai.

## Keputusan

Gunakan **Rust** dengan **axum** (di atas hyper + tokio + tower).

Rincian:
- `axum` untuk routing & middleware; `tower` untuk timeout, rate limit, load shed.
- Pekerjaan CPU (decode/resize/encode) di thread pool dengan antrean bounded, bukan di reactor.
- Library image: `image`/`zune-jpeg`, `fast_image_resize`, `ravif`, `libwebp` binding.
- Workspace crate terpisah per tanggung jawab (lihat `ARCHITECTURE.md §6`).

## Konsekuensi

**Positif**
- Tanpa GC → latensi p99 stabil, memori terprediksi.
- `Send + Sync` + ownership memaksa buffer aman di shared cache.
- Binary tunggal (musl) → deploy sederhana, image kecil.
- Ekosistem HTTP matang dan teruji (`hyper` dipakai di banyak produk besar).

**Negatif**
- Waktu pengembangan lebih lama vs Go/Python; tim harus nyaman dengan borrow checker.
- Binding C (libwebp/libavif) tetap membawa risiko memori → perlu isolasi/patch.
- Ekosistem image Rust belum sekaya `libvips` (mis. beberapa format/langka filter).

## Alternatif yang ditolak

| Alternatif | Alasan ditolak |
| --- | --- |
| Go + libvips (seperti imgproxy) | Perf bagus, tapi memori/GC tail kurang, dan kita kalah jika hanya meniru imgproxy. |
| Node + sharp | Cepat untuk transform (libvips), tetapi throughput HTTP dan memori kurang; keunggulan io_uring tidak bisa dieksploitasi. |
| Python (Thumbor-style) | Latensi & memori jauh lebih buruk; GIL menyulitkan paralelisme. |
| C++/C | Risiko memori pada decoder yang menerima input adversarial. |
