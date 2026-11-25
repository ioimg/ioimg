# ADR-0004 — Signed URL path-based dengan `exp` di dalam canonical

- **Status:** Accepted
- **Tanggal:** 2025-11

## Konteks

Sebagian pelanggan menyajikan aset privat (konten berbayar, dokumen pelanggan, foto acara). Mereka butuh URL yang:

- tidak bisa ditebak/dimodifikasi (tidak bisa mengubah `w`/`h` untuk menyalahgunakan compute),
- bisa kedaluwarsa,
- tetap **cacheable** oleh CDN (tanpa cookie/session),
- mudah dibuat di server aplikasi (bahasa apa pun).

## Keputusan

```
/{signature}/{ops}/{source}?exp=<unix>&kid=<key_id>
signature = base64url(HMAC-SHA256(signing_key, "{ops}/{source}?exp={exp}"))
```

- Signature di **path**, bukan query — cache key CDN alami, cocok dengan pola imgproxy yang sudah dikenal; log proxy tidak memisahkan signature dari path.
- `exp` **disertakan dalam canonical string** sehingga tidak bisa diubah tanpa merusak signature.
- Verifikasi **constant-time** (`subtle::ConstantTimeEq`), dan memakai key berdasarkan `kid` (rotasi: key lama valid 7 hari).
- Mode per project: `off` (publik + allowlist), `per-project` (default saat ada aset privat), `always`.
- Signature **tidak** menggantikan rate limit/kuota.

## Konsekuensi

**Positif**
- Tanpa state: verifikasi murni HMAC → bisa dilakukan di edge/worker.
- URL tetap immutable & cacheable; `exp` memberi TTL alami pada cache (bisa dipetakan ke `s-maxage`).
- Implementasi trivial di semua bahasa (HMAC-SHA256 + base64url), test vector tunggal.

**Negatif**
- Rotasi key memerlukan masa grace atau URL lama mati.
- Signature panjang (43 char) memperpanjang URL; tidak masalah untuk `<img>`.
- Pengguna bisa membagikan URL yang sudah di-sign sampai `exp` — sama seperti presigned URL pada umumnya (diterima, didokumentasikan).
- Bila `include_exp`, cache key berubah setiap `exp` berbeda untuk aset yang sama → fragmentasi cache. Mitigasi: `exp` dibulatkan ke bucket (mis. per jam) di helper SDK, dan `exp` tidak masuk ke cache key L2 (hanya divalidasi) — variasi ini dicatat sebagai trade-off; lihat catatan di bawah.

### Catatan implementasi (penting)

Signature memvalidasi `exp`, tetapi **cache key L2 tidak** menyertakan `exp`. Artinya: URL dengan `exp` berbeda untuk aset+ops yang sama berbagi entry cache yang sama. Ini disengaja (menghindari fragmentasi) dan aman karena otorisasi terjadi sebelum lookup cache. Risiko: user A yang punya URL valid bisa "menghangatkan" cache untuk user B — cache bersifat publik per URL-ops, jadi tidak ada kebocoran konten (konten sama).

## Alternatif yang ditolak

| Alternatif | Alasan |
| --- | --- |
| Signature di query (`?sig=`) | Bercampur dengan parameter ops, lebih mudah salah urut/kanonik; beberapa CDN memakai query untuk cache key. |
| JWT di header `Authorization` | Tidak cacheable oleh CDN, tidak bisa dipakai langsung di `<img>`. |
| mTLS / cookie | Tidak cocok untuk `<img>` lintas domain. |
| AES-GCM terenkripsi di path | Overhead & URL jauh lebih panjang; HMAC cukup karena tidak butuh kerahasiaan. |
| `exp` di luar canonical | `exp` bisa diubah bebas → signature berlaku selamanya. |
