# ADR-0005 — Skema URL path-based sebagai kontrak utama

- **Status:** Accepted
- **Tanggal:** 2025-11

## Konteks

Dua bentuk umum di pasar:

- **imgproxy/Cloudinary:** `/{sig}/{ops}/{source}` — path-based.
- **ImageKit/wsrv.nl/Next.js:** `/img?url=...&w=...` — query-based.

Pilihan ini memengaruhi caching CDN, kemudahan integrasi, dan kejelasan semantik.

## Keputusan

**Path-based adalah kontrak utama**, dengan dukungan query-based sebagai bentuk alternatif yang setara (untuk platform yang sulit menulis path).

```
/{signature?}/{ops}/{source_url}
/img?src=...&w=...&h=...&c=...&f=...&sig=...
```

- `ops` berupa `nama_nilai` dipisah koma, urutan bebas, dinormalkan sebelum signature & cache key.
- `source_url` absolut (wajib); skema default `https` bila dihilangkan? **Tidak** — wajib eksplisit untuk menghindari ambiguitas dan SSRF lewat skema.
- Kedua bentuk menghasilkan canonical yang sama → satu cache namespace.

## Konsekuensi

**Positif**
- CDN caching alami: tidak ada parameter query "liar" yang memecah cache.
- URL mudah dibaca & di-debug, cocok untuk dokumentasi dan support.
- Immutable: ops mengubah path, bukan header → aman `immutable, max-age=31536000`.
- Lebih sulit disalahgunakan sebagai open proxy (path memaksa bentuk source yang eksplisit).

**Negatif**
- URL panjang (source URL ikut di path) → beberapa WAF/middleware punya batas panjang; mitigasi: batas 2048 char, dokumentasi, opsi query-based.
- Platform yang memaksa query param (Shopify, beberapa page builder) perlu mode alternatif → karena itu query-based tetap didukung.
- Encoding ganda (source URL di dalam path) rawan bug → ada test vector & fuzz target untuk kanonikalisasi.

## Alternatif yang ditolak

| Alternatif | Alasan |
| --- | --- |
| Query-based saja | Mudah tapi cache key rapuh, parameter ops bercampur dengan param analitik (`utm_*`), dan signature sulit dipisahkan. |
| Hash-based (`/{hash}` tanpa source terlihat) | Tidak bisa di-debug/dibaca; butuh penyimpanan mapping tambahan (state). |
| Header untuk ops | Tidak cacheable, tidak bisa dipakai `<img src>`. |
| JSON body / POST | Tidak bisa dipakai `<img>`, tidak cacheable, butuh CORS. |
