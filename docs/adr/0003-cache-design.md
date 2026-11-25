# ADR-0003 — Cache dua lapis dengan key deterministik

- **Status:** Accepted
- **Tanggal:** 2025-11
- **Konteks:** butuh hit ratio tinggi, latensi rendah, dan invalidation yang bisa dipertanggungjawabkan.

## Konteks

Image proxy menghasilkan banyak varian per aset (lebar × format × dpr × quality). Jumlah varian bisa meledak (combinatorial). Cache harus:

- cepat untuk lookup (hot path),
- deterministik (URL sama → key sama, di node mana pun),
- bisa di-invalidate per aset/tenant,
- aman terhadap tabrakan antar tenant.

## Keputusan

**Dua lapis utama + CDN di depan:**

| Layer | Media | Isi |
| --- | --- | --- |
| CDN (opsional) | Edge eksternal | Semua varian publik, `Vary: Accept` |
| L1 | RAM, sharded LRU (moka) | Objek ≤ 256 KB, TTL pendek |
| L2 | NVMe lokal, file per key | Semua objek, TTL panjang, diakses via `IoBackend` |
| L3 | Object storage (fase 3) | Opsional, lintas node |

**Key:**

```
blake3_128(v1 | project_id | ops_fingerprint | source_canonical | output_format)
```

- `ops_fingerprint` = parameter diurutkan kanonik + nilai ter-resolve (`q_auto` → angka).
- `output_format` eksplisit (hasil negosiasi `Accept`) sehingga L2 tidak perlu `Vary`.
- `v1` = `transform_version`, dinaikkan saat algoritma codec berubah.

**Layout file:** `ab/cd/<hex>.bin` + `<hex>.meta` (header 64 byte), tulis `tmp → rename` atomik.

## Konsekuensi

**Positif**
- Deterministik → bisa dihitung ulang di mana pun, cocok untuk prerender/sitemap.
- `output_format` di key menghindari racun `Vary` di L2 sambil tetap benar di HTTP.
- Tanpa direktori per tenant → tidak ada inode blowup; isolasi lewat prefix key.
- Metadata terpisah dari body → body bisa di-`splice` langsung ke socket.

**Negatif**
- Purge by source URL butuh index tambahan (`source_canonical → keys`) yang harus dijaga konsisten.
- `transform_version` bump = cache dingin (lonjakan origin) → butuh prosedur deploy (canary/prewarm).
- Varian meledak bila integrasi mengirim `w` acak; mitigasi: batas jumlah key baru/project/hari + dokumentasi `deviceSizes` standar.

## Alternatif yang ditolak

| Alternatif | Alasan |
| --- | --- |
| Satu lapis (disk saja) | Terlalu banyak syscall untuk objek kecil; latensi p99 naik. |
| Satu lapis (RAM saja) | Memori tidak cukup untuk working set 100 GB; biaya meledak. |
| Cache key = full URL mentah | Tidak kanonik: `?a=1&b=2` vs `?b=2&a=1` jadi dua entri; rawan tabrakan parameter. |
| Direktori per tenant | Inode & operasi directory berat; sulit di-scale ke 100k tenant. |
| Redis sebagai L2 | Latensi jaringan + biaya memori; io_uring/local NVMe lebih cepat dan murah. |
