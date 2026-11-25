# Evaluasi Topologi: Cloudflare + R2 + Worker VPS

Usulan: 1 node untuk DB + control plane, 1 VPS shared untuk worker Free, 1 VPS
dedicated untuk worker Pro, R2 sebagai cache L2 (bucket terpisah Free/Pro), dan
semua trafik lewat Cloudflare dengan upgrade plan untuk pelanggan berbayar.

Verdikt: **arahnya benar dan biayanya jauh lebih murah** (~$180/bln vs $607
pada rencana sebelumnya), karena egress pindah ke Cloudflare/R2. Tapi ada tiga
hal yang bisa menggagalkan rencana ini secara teknis atau legal, dan tiga
penyederhanaan yang menghemat uang tanpa mengorbankan apa pun.

---

## 1. Yang harus dikoreksi

### 1.1 ⚠️ ToS Cloudflare §2.8 — ini risiko utama, bukan detail

Self-serve Cloudflare (Free, Pro, Business) punya pasal **"Limitation on Serving
Non-HTML Content"**: melayani video atau **proporsi besar file gambar/audio**
lewat proxy tidak diizinkan kecuali memakai produk yang memang dirancang untuk
itu. Jadi "semua trafik lewat Cloudflare, upgrade untuk yang bayar" **tidak
menyelesaikan** masalah: §2.8 berlaku juga untuk Pro dan Business.

Yang **dikecualikan** dari §2.8: **R2** dan produk media resmi (Images, Stream).
Itu mengubah desain, bukan hanya harga:

| Jalur | Status ToS | Biaya egress |
| --- | --- | --- |
| CF proxy → origin ioimg (transform) | §2.8 berisiko kalau volume gambar besar | $0 |
| **R2 public bucket + custom domain** | **diizinkan** (R2 exempt) | **$0** |
| Worker → origin, body dikirim dari R2 | aman (konten keluar dari R2) | $0 |
| Bunny CDN | aman, tidak ada batasan jenis konten | $0,005–0,03/GB |

Rekomendasi konkret:

- **Pro**: boleh CF proxy (pelanggan bayar, volume per pelanggan wajar) **atau**
  lebih aman: Worker + R2 sebagai cache edge (lihat §3).
- **Free**: jangan bertumpu pada CF proxy untuk volume besar — pakai **Bunny
  volume network** ($0,005/GB, 10 TB = $50/bln) atau Worker+R2. Ini menghindari
  skenario terburuk: akun Free yang tumbuh lalu kena penegakan ToS, dan seluruh
  tier ikut mati.

### 1.2 ✗ Satu node untuk DB + control plane = satu titik gagal untuk *uang*

Kalau node ini mati: tidak ada login, tidak ada purge, tidak ada billing, dan
**usage report tidak tercatat** → pendapatan hilang tanpa gejala. Serving gambar
harus tetap jalan (snapshot config sudah disinkronkan ke worker), tapi billing
tidak boleh bergantung pada node yang bisa mati.

Wajib sebelum produksi:

1. **Spool usage lokal di worker** (append-only file + retry) supaya laporan
   usage tidak hilang saat control plane down. Saat ini `ReportUsage` langsung
   HTTP; kalau gagal, angka hilang.
2. **Backup harian + WAL** Postgres ke R2/B2 (`pgBackRest`/WAL-G), storage
   $0,015/GB. Uji restore, bukan hanya backup.
3. Snapshot config **persisted ke disk** di worker, supaya restart saat control
   plane mati tetap melayani dengan policy terakhir.

### 1.3 ⚠️ Satu worker per tier = satu titik gagal untuk *layanan*

1 VPS Free mati → Free down (boleh, best-effort). **1 VPS Pro mati → SLA
99,9% tidak mungkin**. Minimum Pro: **2 replika** di 2 host (boleh 1 region),
di belakang CF load balancing atau DNS round-robin dengan health check.

### 1.4 ✓ Bucket per tenant: benar, tapi alasan hematnya salah

R2 memberi **100.000 bucket gratis** per akun; bucket bukan driver biaya.
Berbagi 1 bucket untuk 2–5 pelanggan Pro **tidak menghemat apa pun** dan
menghilangkan tiga hal yang justru penting: purge per tenant (lifecycle),
penghapusan data saat pelanggan berhenti (GDPR), dan metrik storage per tenant.

- **Free**: 1 bucket bersama, prefix per proyek (`free/<project>/...`), lifecycle
  delete 7 hari.
- **Pro**: **1 bucket per pelanggan**, lifecycle 30 hari. Kalau pelanggan >500,
  baru pertimbangkan bucket per region, bukan per 2–5 pelanggan.

Catatan biaya R2: pakai **Standard**, jangan Infrequent Access — IA punya
minimum storage 30 hari dan biaya retrieval $0,01/GB, mematikan untuk cache yang
sering dihapus. Biaya Real: storage $0,015/GB-bln, Class B $0,36/juta read.

### 1.5 ⚠️ Cache key Cloudflare vs `exp`/`sig` — bisa membunuh hit ratio

URL bertanda tangan membawa `sig` dan `exp`. Kalau `exp` di query string ikut
masuk cache key CF, **setiap URL dengan exp berbeda menjadi objek cache baru** →
hit ratio edge jatuh ke ~0, dan origin menerima seluruh trafik. Ini bukan teori:
cache key ioimg sendiri sengaja tidak memasukkan sig/exp.

Perbaikan (semua tersedia di plan Free, 10 Cache Rules):

```
Cache Rule: path matches /v1/* and /*/*/*
  Cache eligibility: Eligible for cache
  Edge TTL: 1 year (override origin)
  Cache key → Query string: include all EXCEPT (exp, sig, kid)
```

Tambahan: pakai signature di **path** (`/{sig}/{ops}/{source}`), bukan query,
dan `exp` panjang (≥24 jam) supaya URL tidak unik per request. Verifikasi dengan
`curl -I` dua kali dan lihat `cf-cache-status: HIT`.

### 1.6 ✓ Cloudflare memang sekarang menghormati `Vary`

Sudah dikoreksi di dokumen lain: CF mendukung `Vary` lewat Cache Rules (aksi
normalize/passthrough/bypass). Untuk `f_auto` yang bergantung `Accept`, set
aturan: **cache key tidak menyertakan `Accept`**, dan hasilkan URL terpisah per
format (`f_webp`, `f_avif`) — paling aman dan paling murah. Jangan mengandalkan
`Vary: Accept` untuk varian format; itu sumber cache miss dan salah saji.

---

## 2. Hardware: harga per performa (dicek Nov 2026)

Kapasitas acuan repo ini: satu proses celeris = **79,2k rps HIT**, **76 MISS
JPEG/s per core**. Dengan CF di depan, origin hanya melayani MISS (≈5%), jadi
**egress origin menjadi tidak relevan** dan VPS murah sudah cukup. Ini
membalik rekomendasi sebelumnya: "unlimited traffic" Hetzner bukan lagi alasan
utama memilih dedicated.

| Peran | Kandidat | Spek | Harga | Catatan |
| --- | --- | --- | --- | --- |
| **Control plane + DB** | Hetzner CPX31 (SG) | 4C/16 GB/160 GB NVMe | ~€15 | shared vCPU cukup; NVMe untuk Postgres |
| alternatif lokal | Biznet Gio NEO Lite 4C/8 GB | 4C/8 GB/60 GB | Rp269 rb (~$17) | Jakarta; disk 60 GB sempit untuk DB+backup lokal |
| **Worker Free** | Hetzner CPX31 (SG) | 4C/16 GB | ~€15 | ~30–60 MISS/s nyata; tier tanpa SLA |
| alternatif | Vultr High Frequency 3C/8 GB | 3C/8 GB/256 GB | $48 | NVMe besar untuk L1 |
| **Worker Pro** | **Vultr High Performance Jakarta** | 8C/16 GB/350 GB NVMe, 9 TB | **$96** | latency ID terbaik; 350 GB L1 |
| alternatif Pro (EU) | **Hetzner AX42** | 8C/64 GB/2×512 NVMe, 1 Gbit/s | **€99** + €49 setup | core dedicated, traffic tak dibatasi; tanpa Jakarta |
| alternatif Pro (dedicated vCPU cloud) | Hetzner CCX23 (4C/16 GB) | 4C/16 GB | €85,99 | **lebih mahal per core dari AX42** — hindari |
| **L2 cache** | Cloudflare R2 | 2 TB + Class B | $30/bln | egress gratis; bucket per tenant gratis |

Catatan harga Hetzner yang berubah April 2026 (+25–33%): CCX13 €42,99, CCX23
€85,99, CCX33 €138,49 per bulan (cap). Bandingkan: **AX42 dedicated 8C/64 GB
€99** mengalahkan CCX33 (8C/32 GB €138) — kalau butuh 8 core dedicated, AX42
lebih murah dan tanpa batas traffic, dengan konsekuensi hanya tersedia di EU/US.

---

## 3. Topologi rekomendasi (versi R2-as-edge, ToS-clean)

```
                    ┌──────────────────────────────────────┐
   client ─────────▶│ Cloudflare (DNS, TLS, WAF, Cache Rule)│
                    │  hit  → R2 (egress gratis, exempt 2.8)│
                    └───────────────┬──────────────────────┘
                            miss    │  (Worker opsional:
                                    │   ambil dari origin,
                                    ▼   simpan ke R2, sajikan)
                    ┌───────────────────────────────┐
                    │ origin ioimg (worker VPS)     │
                    │  Free: 1 shared VPS           │
                    │  Pro : 2 dedicated VPS        │
                    └──────────┬────────────────────┘
                               │ L2 write / read
                    ┌──────────▼────────────────────┐
                    │ R2: free/<project>/…           │
                    │     pro-<tenant>/…             │
                    └───────────────────────────────┘
                               │
                    ┌──────────▼────────────────────┐
                    │ control plane + Postgres      │
                    │ (1 node, backup ke R2 harian) │
                    └───────────────────────────────┘
```

Dua varian implementasi, pilih satu:

| Varian | Cara kerja | Kelebihan | Kekurangan |
| --- | --- | --- | --- |
| **A. CF proxy → origin** (paling sederhana) | CF cache seperti biasa, origin hanya MISS | nol kode baru | §2.8 berisiko untuk Free volume besar |
| **B. Worker + R2** | Worker cek R2; kalau ada, sajikan; kalau tidak, ambil ke origin, tulis ke R2 (TTL), sajikan | patuh ToS, egress $0, hit ratio penuh | Worker $5/bln + 10 jt req, ~10–30 ms tambahan, perlu invalidasi R2 saat purge |

Rekomendasi saya: **B untuk Free dan Pro**, dengan A hanya sebagai fallback
darurat. Alasan: satu jalur, patuh ToS, dan biaya egress tetap nol. Worker Paid
$5/bln sudah termasuk 10 juta request; di atas itu $0,30/juta — untuk 50 juta
request/bulan = $17, masih jauh lebih murah daripada Bunny Asia ($0,03/GB).

---

## 4. Biaya bulanan untuk usulan ini

| Item | Biaya |
| --- | --- |
| Control plane + Postgres (CPX31 SG atau Biznet Gio 4C) | $15–17 |
| Worker Free (1 shared VPS) | $15 |
| Worker Pro (2× Vultr Jakarta 8C/16 GB, untuk SLA) | $192 |
| Worker Pro (alternatif 2× AX42 EU) | $214 |
| R2 2 TB (storage) + Class B | ~$32 |
| Cloudflare Free (3 zona) | $0 |
| Cloudflare Pro (1 zona untuk Pro, WAF/analytics) | $20 |
| Worker Paid (jalur B) | $5 |
| Backup/monitoring/domain | ~$15 |
| **Total (Pro di Jakarta)** | **≈ $294/bln** |
| **Total (hemat: 1× Vultr Pro + 1× AX42)** | **≈ $200/bln** |

Break-even: **7 pelanggan Pro ($29)** untuk versi $200/bln, **11 pelanggan**
untuk versi $294/bln. Ini jauh lebih sehat daripada rencana lama ($607, 21
pelanggan).

---

## 5. Yang tetap harus dibangun sebelum ini jalan

1. **Spool usage lokal + retry** di worker (billing tidak boleh hilang saat
   control plane down). Ini yang paling mendesak.
2. **Purge dua lapis**: purge CF/R2 (edge) dan L2 R2 per tenant, plus L1 ttl.
   Tanpa ini pelanggan melihat gambar lama sampai 1 tahun.
3. **Cache key: `tier` + `project`**, supaya objek Free tidak pernah tersaji ke
   Pro walaupun ada satu R2 bersama.
4. **Aturan Cache Rules** dengan pengecualian `exp/sig/kid` (lihat §1.5) —
   10 menit kerja, dampaknya besar.
5. **`IOIMG_PUBLIC_URL`/`IOIMG_CDN_PREFIX`** supaya URL yang dikembalikan ke
   client menunjuk ke edge, bukan ke origin.

---

## 6. Verifikasi harga sebelum dipakai

- Hetzner: `/cloud/general-purpose/` (CCX), `/cloud/cost-optimized/` (CX),
  `/dedicated-rootserver/ax42/` — harga berubah April 2026.
- Biznet Gio: `biznetgio.com/product/neo-lite` (harga IDR, termasuk diskon).
- Vultr: `vultr.com/pricing` (High Performance 8C/16 GB, tersedia Jakarta).
- Cloudflare: R2 pricing (egress gratis, bucket gratis 100k), Workers Paid,
  Cache Rules per plan (Free: 10 rules), ToS §2.8 + Supplemental Terms (R2 dan
  produk media dikecualikan).
