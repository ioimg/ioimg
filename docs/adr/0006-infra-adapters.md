# ADR-0006 — Abstraksi storage & CDN sebagai adaptor (bukan asumsi)

- **Status:** Accepted
- **Tanggal:** 2025-11
- **Konteks:** integrasi infra (S3/R2/GCS/Azure/MinIO/local) + CDN (Cloudflare/CloudFront/Fastly/Bunny) yang berperilaku tidak seragam.

## Konteks

Dua kesalahan umum pada produk sejenis:

1. **Mengasumsikan origin = HTTP.** Pelanggan justru menyimpan aset di object storage. Memaksa mereka membuka bucket publik (atau membuat CDN di depan bucket) menambah biaya, latensi, dan risiko.
2. **Mengasumsikan CDN menghormati `Vary`.** CloudFront dan Cloudflare (umumnya) mengabaikan `Vary` selain `Accept-Encoding`. Bila ioimg mengirim `Vary: Accept` untuk `f_auto`, CDN akan menyimpan satu varian dan menyajikannya ke browser yang tidak mendukungnya → gambar rusak, tiket support, kepercayaan hilang.

Selain itu, purge harus efektif lintas lapisan: menghapus L1/L2 ioimg tidak menyentuh cache CDN.

## Keputusan

**1. Storage sebagai trait + skema URI.**

```rust
pub trait StorageBackend { fn scheme(&self) -> &'static str; /* get, stat, presign_get */ }
```

- Skema: `http(s)://`, `s3://`, `r2://`, `gs://`, `az://`, `file://`, `smb://`, `webdav://`.
- Alias per project (`media`, `products`) agar URL publik tidak membocorkan detail vendor.
- `prefix_allow` wajib; kredensial read-only per project, terenkripsi.
- `LocalOrigin` wajib sandbox root (anti path traversal).

**2. CDN sebagai konfigurasi eksplisit + trait `CdnInvalidator`.**

- Project mendeklarasikan CDN yang dipakai (`provider`, `zone_id`, `purge` mode, token).
- ioimg mengirim `Cache-Tag` / `Surrogate-Key` sejak v1 agar purge selektif mungkin di masa depan.
- `POST /v1/purge` = purge lokal + fan-out best-effort ke semua CDN project; kegagalan dicatat per-provider.
- **`f_auto` dinyatakan tidak kompatibel dengan CDN yang mengabaikan `Vary`.** Untuk mode tersebut, dokumentasi & SDK mendorong format eksplisit (`f_avif`/`f_webp`/`f_jpeg`) dengan `<picture>`.
- `X-IOIMG-CDN-Awareness` tidak dipakai; sebagai gantinya ada field config `cdn.vary_accept = true|false` yang mempengaruhi dokumentasi & warning di dashboard: bila `false` dan `f_auto` dipakai, tampilkan peringatan konfigurasi.

## Konsekuensi

**Positif**
- Pelanggan tidak perlu memindahkan aset atau membuka bucket; adopsi lebih cepat.
- Purge menjadi benar lintas lapisan (ini sumber bug klasik di image proxy).
- Satu API (`/v1/purge`) untuk semua vendor; operator tidak perlu script per CDN.
- Kredensial per project memungkinkan model SaaS multi-tenant yang aman.

**Negatif**
- Banyak adaptor = permukaan bug & biaya maintenance. Mitigasi: contract test wajib + hanya 3 vendor P0 (Cloudflare, Bunny, CloudFront).
- Perbedaan perilaku vendor tidak bisa disembunyikan sepenuhnya → dokumentasi harus jujur per vendor (tabel §4.1 INFRA_INTEGRATIONS).
- Purge CloudFront berbiaya dan lambat → strategi utama tetap **versioned path**, purge sebagai jalur darurat.
- `Vary`-awareness menambah cabang di SDK/dokumentasi (dua mode format).

## Alternatif yang ditolak

| Alternatif | Alasan |
| --- | --- |
| Hanya HTTP origin | Memaksa bucket publik; menambah satu lapisan CDN; tidak bisa menjangkau pelanggan S3-native. |
| Satu SDK storage per vendor (tanpa trait) | Duplikasi logika retry/limit/observability; sulit menambah vendor. |
| Andalkan `Vary: Accept` di semua CDN | Salah secara faktual; menghasilkan gambar rusak di CloudFront/Cloudflare. |
| Purge seluruh CDN (`purge everything`) sebagai default | Menghapus hit ratio pelanggan lain di shared zone; mahal dan berisiko. |
| Signed URL CDN-only (bypass ioimg) | Kehilangan transformasi & observability; tetap butuh ioimg untuk variant. |
