# ADR-0009 — Control plane multi-tenant, konfigurasi per-project

- Status: **Accepted** (2026-11-25)
- Melanjutkan: [ADR-0008](0008-go-data-plane.md)

## Konteks

Data plane versi pertama membaca konfigurasi dari environment: satu set
signing key, allowlist, preset, dan kuota untuk seluruh proses. Model itu
memadai untuk self-host satu domain, tetapi tidak untuk SaaS:

- Setiap tenant butuh signing key sendiri (rotasi independen).
- Allowlist, preset, domain, dan kuota berbeda per tenant.
- Menambah tenant tidak boleh berarti restart/redeploy data plane.
- Tenant tidak boleh memegang secret HMAC; mereka butuh cara meminta URL
  bertanda tangan dari server.
- Data plane harus tetap cepat: konfigurasi tidak boleh dibaca dari DB pada
  setiap request.

## Keputusan

1. **Control plane terpisah** (`cmd/ioimg-control`, SQLite) menyimpan project,
   signing key (terenkripsi AES-GCM dengan master key), API key (hash),
   allowlist, preset, limit, dan usage bulanan.
2. **Snapshot pull**: data plane menarik `GET /v1/config/snapshot` setiap
   `IOIMG_CONTROL_SYNC` (default 30s), menyimpannya sebagai snapshot immutable
   di memori, dan melayani request dari snapshot itu. Tidak ada DB di hot path.
3. **Resolusi tenant** lewat `Host` (domain project, wildcard didukung) atau
   `X-IOIMG-Project` (hanya bila diaktifkan, untuk dev). Host tidak dikenal →
   `404 project_not_found`, **tanpa fallback lintas tenant**.
4. **Namespace cache**: key L1/L2, purge, dan warm menyertakan project ID.
5. **Signing tanpa distribusi secret**: endpoint
   `POST /v1/projects/{id}/sign` memakai API key scope `sign`. Secret HMAC
   tidak perlu ditanam di aplikasi tenant.
6. **Usage & kuota**: data plane mengirim batch counter tiap 10 detik; control
   plane menyimpan counter bulanan. Data plane menegakkan limit dari snapshot
   + counter lokal (`X-IOIMG-Quota-Remaining`).
7. **Dashboard embedded** (`/`) tanpa build step untuk operasi dasar: project,
   key, allowlist, preset, usage, dan generator signed URL.

## Konsekuensi

- Control plane adalah komponen stateful; butuh backup DB dan proteksi token.
  Snapshot membawa signing secret, jadi endpoint-nya hanya untuk jaringan
  privat/TLS dan token data-plane.
- SQLite cukup untuk MVP/self-host. Untuk HA multi-replica, pindahkan store ke
  Postgres (interface store sengaja kecil) atau jalankan control plane tunggal
  dengan replika read-only di belakang LB.
- Ada jendela propagasi hingga `IOIMG_CONTROL_SYNC`; perubahan allowlist/rotasi
  key tidak instan. Untuk rotasi darurat, kurangi interval atau restart node.
- Quota bersifat *best effort*: laporan usage tiap 10s dan snapshot tiap 30s,
  jadi overrun singkat mungkin terjadi.
- Billing/invoicing belum termasuk; butuh keputusan produk (plan, overage,
  payment provider).

## Alternatif yang ditolak

| Alternatif | Alasan |
| --- | --- |
| Env per tenant + restart | Operasional tidak skalabel, downtime, rawan salah konfigurasi |
| Baca DB langsung dari data plane | Menambah latency/ketergantungan DB di hot path; gagal DB = gagal serving |
| Verifikasi signature via RPC ke control plane | Satu round-trip tambahan per request pada jalur terpanas |
| JWT/gRPC config stream | Kompleksitas belum sepadan; snapshot pull + ETag/304 cukup |
| Menaruh secret HMAC di aplikasi tenant | Rotasi sulit, kebocoran sulit dibatasi; signing endpoint lebih aman |
