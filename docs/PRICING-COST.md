# Biaya Infrastruktur & Rencana Harga

Dokumen ini menjawab dua hal: **berapa biaya nyata melayani satu juta gambar**,
dan **harga jual yang masih menyisakan margin**. Semua biaya compute dihitung
dari pengukuran repo ini, bukan asumsi vendor.

Sumber harga dikutip per halaman pada bagian 2–3 dan berlaku saat dokumen ini
ditulis; harga cloud berubah tanpa pemberitahuan, jadi angka wajib diverifikasi
ulang sebelum dipakai untuk kontrak.

---

## 1. Biaya nyata per 1 juta gambar

Beban acuan: HIT 17,7 KiB (keluaran nyata `w_400,f_jpg,q_80`), MISS `w_400` JPEG
lewat libvips shrink-on-load. Hit ratio 95%.

Kapasitas terukur (satu proses celeris, 16 core):

| Beban | Terukur | Core-detik per 1.000 gambar |
| --- | --- | --- |
| HIT | 79.200 rps @ ~6 core | 0,076 core-s |
| MISS JPEG | 76 img/s per core | 13,2 core-s |
| MISS WebP | 50 img/s per core | 20,0 core-s |
| MISS AVIF | 7 img/s per core | 142,9 core-s |
| MISS + watermark | 49 img/s per core | 20,4 core-s |

Harga core acuan: **Hetzner AX42** (8 core dedicated, €99/bln) =
`99 / (8 × 730) = €0,0169/core-jam` ≈ **$0,0183**.

| Komposisi 1 juta gambar | Compute | Bandwidth (20 KiB/gambar = 20 GB) |
| --- | --- | --- |
| 95% HIT + 5% MISS JPEG | **$0,07** | $0–0,60 (tergantung jalur, lihat §3) |
| 50% HIT + 50% MISS JPEG | $0,67 | $0–0,60 |
| 95% HIT + 5% MISS AVIF | $0,73 | $0–0,60 |

**Kesimpulan: bandwidth 75–90% biaya, compute nyaris gratis.** Untuk 1 juta
gambar, compute < $1. Ini yang membentuk seluruh strategi harga: jual
*bandwidth + fitur*, bukan CPU.

---

## 2. Kandidat compute

| Penyedia / paket | Spek | Harga | Lokasi | Catatan untuk ioimg |
| --- | --- | --- | --- | --- |
| **Hetzner AX42** (dedicated) | 8C/64 GB/2×512 NVMe, 1 Gbit/s | **€99/bln** + €49 setup | DE/FI/US saja | **Traffic tidak dibatasi**, core dedicated. Pilihan termurah per gambar. Tidak ada Jakarta. |
| Hetzner CCX (cloud, dedicated vCPU) | mulai 2C/8 GB | mulai **€36,99/bln** | DE/FI/US/**Singapore** | Bisa autoscale/billing jam; Singapore = ~40 ms ke Jakarta |
| Hetzner CPX (shared vCPU) | mulai 2C/4 GB | **€5,49/bln** | termasuk Singapore | Cukup untuk free tier; shared core = noisy neighbor milik orang lain |
| **Biznet Gio NEO Lite** | 4C/8 GB/60 GB | **Rp269.000/bln** (~$17) | Jakarta | Lokal, 10 Gbps, harga IDR. Disk kecil untuk L1 |
| Biznet Gio NEO Lite | 8C/8 GB/60 GB | ~Rp400–500 rb (~$25–31) | Jakarta | Perlu konfirmasi sales; disk tetap 60 GB |
| **Vultr High Performance** | 8C/16 GB/350 GB NVMe, 9 TB | **$96/bln** | termasuk Jakarta | NVMe 350 GB bagus untuk L1; 9 TB ≈ 450 juta gambar 20 KiB |
| Vultr High Frequency | 3C/8 GB/256 GB, 4 TB | **$48/bln** | termasuk Jakarta | Alternatif kecil untuk Jakarta |
| Alibaba Cloud ECS | 2C/4 GB | ~$21–55/bln | Jakarta | Egress $0,08–0,09/GB → mahal untuk keluaran besar |
| AWS/GCP Jakarta | 2C/4 GB | ~$30–60/bln | Jakarta | Egress $0,09/GB, hanya kalau butuh ekosistem |

**Rekomendasi compute**

- **Pool Free**: 1× AX42 (€99) di Finlandia + CDN di depan. Murah, bandwidth
  tak terbatas, cocok untuk tier yang tidak dijanjikan latency.
- **Pool Pro**: 2× AX42 (€198) untuk kapasitas + redundansi, **atau** 2× Vultr
  Jakarta 8C/16 GB ($192) kalau SLO latency ke Indonesia (p95 < 150 ms) masuk
  kontrak. AX42 hanya ada di Eropa/Amerika, jadi Pro untuk pelanggan Indonesia
  sebaiknya di Jakarta.
- **Jakarta edge/API**: 1× Biznet Gio 8C (~$30) untuk control plane ringan +
  origin lokal, atau Vultr Jakarta kalau butuh NVMe besar.
- **Jangan** taruh origin di AWS/Alibaba Jakarta selama egress $0,08–0,09/GB;
  untuk 1 TB/bulan itu $80–90, lebih mahal daripada seluruh server.

---

## 3. Kandidat bandwidth / CDN (ini yang menentukan margin)

| Jalur | Harga egress | 100 GB | 1 TB | 10 TB | Catatan |
| --- | --- | --- | --- | --- | --- |
| Hetzner AX42 langsung | **€0** (unlimited, 1 Gbit/s) | $0 | $0 | $0 | Tanpa CDN; latency Eropa, TTFB dari Jakarta ~180 ms |
| **Bunny CDN Asia/Oceania** | $0,03/GB | $3 | $30 | $300 | PoP Jakarta tersedia |
| Bunny CDN EU/NA | $0,01/GB | $1 | $10 | $100 | termurah untuk audiens Barat |
| **Bunny Volume network** | $0,005/GB (10 PoP) | $0,50 | $5 | $50 | satu harga global, PoP lebih sedikit |
| Cloudflare (free/Pro, proxy) | $0 (plan $0–20/bln/zona) | $0 | $0 | $0 | TOS membatasi pemakaian non-HTML skala besar; hati-hati |
| **Cloudflare R2 sebagai L2** | $0 egress, $0,36/juta Class B | ~$0,04 | $0,36 | $3,6 | storage $0,015/GB-bln; egress gratis |
| Alibaba Jakarta | $0,08/GB | $8 | $80 | $800 | 2,7× Bunny Asia |
| AWS (CloudFront/Jakarta) | $0,085–0,09/GB | $8,5 | $85 | $850 | paling mahal |

**Rekomendasi bandwidth**

1. **Pro**: Bunny CDN sebagai jalur utama (Asia $0,03; volume $0,005 untuk
   pelanggan besar), origin Hetzner/Vultr. Egress Bunny = biaya variabel utama,
   jadi masuk ke harga per GB.
2. **Free**: Cloudflare proxy di depan origin Hetzner (egress origin €0), dengan
   aturan cache agresif dan rate limit. Kalau ada keluhan TOS/滥用, pindah ke
   Bunny volume ($0,005/GB) — 10 TB/bulan hanya $50.
3. **L2 cache**: Cloudflare R2 (egress gratis). Ini memotong biaya baca L2 dari
   S3 biasa secara signifikan; untuk 1 TB cache, storage $15/bulan.

---

## 4. Baseline bulanan (fase 1: Free + Pro terpisah)

| Item | Biaya/bulan |
| --- | --- |
| 1× AX42 pool Free | €99 ≈ $107 |
| 2× AX42 pool Pro | €198 ≈ $214 |
| 1× Vultr Jakarta 8C/16 GB (origin Pro Indonesia) | $96 |
| 1× Biznet Gio 8C (control plane + origin Free ID) | ~$30 |
| Cloudflare R2 (500 GB L2 + operasi) | ~$10 |
| Bunny CDN (asumsi 5 TB campuran, $0,02 rata-rata) | $100 |
| Cloudflare Pro 2 zona (WAF, cache rules) | $40 |
| Monitoring/log (Grafana Cloud free tier + object storage) | $0–25 |
| Domain, TLS (ACME gratis), email transaksional | ~$10 |
| **Total** | **≈ $607/bulan** |

Dengan harga Pro $29/bln, **21 pelanggan Pro** menutup seluruh biaya termasuk
tier Free. Itu angka break-even yang sehat untuk produk infra.

---

## 5. Rencana harga

Prinsip: gratis harus benar-benar gratis (tapi dibatasi bandwidth), berbayar
membeli **bandwidth + isolasi + jaminan**, bukan CPU.

| Plan | Harga | Bandwidth | Request | Domain | Fitur kunci |
| --- | --- | --- | --- | --- | --- |
| **Free** | $0 | 10 GB/bln | 100 rb/bln | 1 (`*.ioimg.net`) | WebP/JPEG, resize, 3 proyek, AVIF & watermark tidak tersedia, pool bersama, best-effort, log 24 jam |
| **Starter** | **$9**/bln | 100 GB | 1 juta | 1 custom | + AVIF, watermark gambar, hashsum, fallback, presets, log 7 hari |
| **Pro** | **$29**/bln | 500 GB | 10 juta | 2 custom | + **pool Pro terpisah**, purge API, 99,9% SLA, log 30 hari, prioritas transform |
| **Business** | **$99**/bln | 2 TB | 50 juta | 5 custom | + 99,95% SLA, dedicated L2 prefix, webhook, SSO, invoices |
| **Scale** | mulai **$299**/bln | 5 TB | 100 juta | banyak | kontrak, kapasitas terjadwal, opsi region Jakarta/EU, support 8×5 |

**Overage**: $0,02/GB (Starter/Pro), $0,015/GB (Business), negosiasi di Scale.
Biaya nyata $0,005–0,03/GB → margin overage 25–75%, dan tidak pernah negatif
selama trafik Pro lewat Bunny volume atau origin Hetzner.

**Diskon tahunan**: 2 bulan gratis (≈17%) untuk semua plan berbayar.

---

## 6. Margin per pelanggan

Asumsi Pro: 500 GB/bulan (25 juta gambar 20 KiB), hit ratio 95%, campuran
JPEG/WebP.

| Komponen | Biaya |
| --- | --- |
| Bandwidth 500 GB via Bunny Asia @ $0,03 | $15,00 |
| Compute (25 juta gambar, 95% HIT) | $1,75 |
| L2 storage (asumsi 100 GB R2) | $1,50 |
| Alokasi kapasitas server (2× AX42 / 40 pelanggan) | $5,35 |
| **Total biaya** | **$23,60** |
| Harga | **$29,00** |
| **Margin kotor** | **$5,40 (19%)** |

Margin 19% tipis, dan itu memang konsekuensinya menjual bandwidth dengan harga
Bunny Asia. Tiga cara memperbaikinya, berurutan:

1. **Dorong trafik ke Bunny volume ($0,005/GB)** atau origin Hetzner langsung
   untuk pelanggan yang tidak butuh edge → biaya bandwidth turun ke $2,50,
   margin naik ke **$11,75 (41%)**.
2. **Turunkan bandwidth included** di Pro jadi 300 GB ($18 → margin 30%), dan
   biarkan overage yang menanggung heavy user.
3. **Naikkan harga Pro ke $39** — masih di bawah imgproxy Pro ($49 untuk 16
   worker, lisensi software tanpa bandwidth) dan jauh di bawah Cloudinary.

**Rekomendasi akhir**: Pro **$29 dengan 300 GB** (bukan 500 GB), overage
$0,02/GB, dan dorong pelanggan besar ke volume network. Margin jadi ~30% di
konsumsi tipikal, ~45% di pelanggan hemat bandwidth.

---

## 7. Perbandingan harga pasar

| Produk | Harga masuk | Model | Catatan |
| --- | --- | --- | --- |
| **imgproxy Pro** | $49/bln | per 16 worker, software self-host | Tanpa bandwidth; kita lebih murah untuk < 500 GB, mereka menang di skala besar self-host |
| Cloudinary | $0 → ~$99/bln | credit (transformasi/GB) | Mahal untuk trafik gambar besar; unggul di fitur AI/DAM |
| ImageKit | $0 → $49/bln | bandwidth + request | Pesaing terdekat; kita bisa lebih murah di IDR dan on-prem |
| Bunny Optimizer | $9,5/bln/zona + $0,01/GB | bandwidth | Paling murah di edge, fitur transformasi terbatas |
| Cloudflare Images | $5 / 100 rb gambar | per gambar disimpan | Bagus untuk storage, bukan transformasi dinamis |
| Fastly IO / CloudFront | ~$0,05–0,09/GB + biaya fitur | bandwidth | Mahal untuk volume gambar |

**Posisi kita**: $9–29 entry (di bawah imgproxy Pro), harga IDR, latensi
Jakarta, dan transformasi yang 12 di antaranya masih ditandai **Pro** oleh
imgproxy (ra, bri/con/sat/mono, sharp, dt, col, gr, dpi, autoquality, codec
options, watermark).

---

## 8. Risiko biaya yang harus dipantau

| Risiko | Dampak | Mitigasi |
| --- | --- | --- |
| Pelanggan Free memakai `f_auto` + AVIF masif | Compute 14× | AVIF ditolak di Free; rate limit edge |
| Cache miss storm (URL unik per request) | Compute naik 20× | `cb_` tidak masuk cache key; deteksi hit ratio per proyek < 80% → peringatan |
| Egress tidak terkendali | Biaya variabel | Cap bandwidth per plan + auto-suspend |
| Hetzner "unlimited" fair use | Bisa ditagih kalau >30 TB | Monitor egress bulanan; pindah ke volume network sebelum 20 TB |
| Harga cloud naik (Hetzner naik 25–33% April 2026) | Margin turun | Kontrak tahunan, overage menyerap |
| Kurs USD/IDR | Harga IDR | Tagih IDR dengan buffer 5%, review kuartalan |

---

## 9. Langkah berikutnya

1. **Selesaikan metering durabel** (prasyarat billing, lihat `TOPOLOGY-CLOUD.md`
   §5) — tanpa ini overage tidak bisa ditagih.
2. Jalankan fase 1 topologi (dua Deployment) di 1× AX42 + 1× Vultr Jakarta.
3. Ukur egress nyata 2 minggu, lalu kunci angka "included bandwidth" di plan.
4. Baru buka pendaftaran Free dengan cap 10 GB dan antrean prioritas rendah.

---

## 7. Rencana low-cost: adopsi dulu, margin kemudian

Kesimpulan dari model di atas: dengan Cloudflare/R2 di depan, **egress = $0** dan
biaya marginal per gambar tinggal compute ($0,07–0,73 per juta gambar). Artinya
harga tidak perlu disandera bandwidth — yang mahal justru (a) AVIF, (b) abuse,
(c) support. Maka strategi "harga murah, adopsi besar" masuk akal secara
ekonomi, dengan tiga syarat di §7.4.

### 7.1 Infrastruktur fase adopsi: satu kotak, ±$10/bulan

| Item | Pilihan | Biaya |
| --- | --- | --- |
| Control plane + Postgres + worker Free (satu box) | **Biznet Gio NEO Lite 2C/4 GB** (Rp139.000, Jakarta) | ~$8,6 |
| alternatif | Hetzner CX23 2C/4 GB (EU, €5,99) | ~$6,5 |
| L2 cache | R2 **free tier** (10 GB, 1 jt Class A, 10 jt Class B) | $0 |
| CDN/DNS/TLS | Cloudflare Free | $0 |
| Backup | R2 (di paket yang sama) | $0 |
| **Total** | | **±$9–10** |

Kapasitas kotak ini: HIT >20k rps (bukan bottleneck), MISS JPEG ~50–80/s pada
2 vCPU shared. Itu ~4–7 juta transform/hari — jauh di atas kebutuhan ratusan
pelanggan pertama. **Tidak perlu** Vultr $96 atau AX42 €99 sebelum ada
pelanggan yang membayar untuk itu.

Naik tangga hanya kalau angka memaksa: worker Free tetap 1 kotak, worker Pro
dipisah saat ada ≥20 pelanggan berbayar, dan pool Pro dipecah per region saat
p95 MISS >250 ms.

### 7.2 Harga dalam IDR (anchor: imgproxy Pro $49 ≈ Rp800rb/bulan)

| Tier | Harga | Kuota | Fitur | Biaya layanan (estimasi) | Margin |
| --- | --- | --- | --- | --- | --- |
| **Free** | Rp0 | 5 GB, 50 rb req, 1 domain | JPEG/WebP, tanpa AVIF/watermark | ~Rp300 | — (akuisisi) |
| **Starter** | **Rp 29 rb** | 25 GB, 250 rb req | + AVIF, 3 domain | ~Rp600 | ~97% |
| **Pro** | **Rp 99 rb** | 150 GB, 2 jt req | + watermark, custom domain, purge API, 99,5% | ~Rp2.000 | ~97% |
| **Business** | **Rp 299 rb** | 750 GB, 10 jt req | + pool terpisah, SLA 99,9%, webhook | ~Rp8.000 | ~96% |

Overage Rp 50/GB (setara $0,003/GB — masih di bawah Bunny volume $0,005).

Perbandingan yang bisa dipakai di landing page: imgproxy Pro **Rp800rb** untuk 16
worker; ioimg Pro **Rp99rb** termasuk CDN. Cloudinary/ImageKit kelas $49–99.

### 7.3 Apa yang harus dimatikan di tier murah (cost control, bukan fitur)

1. **AVIF di Free/Starter.** MISS AVIF 142 ms vs JPEG 10,3 ms = **14× CPU**;
   satu pengguna AVIF setara 14 pengguna JPEG. Aktifkan di Pro ke atas, dan
   batasi `q` maksimum untuk AVIF.
2. **Watermark** (round-trip + composite) di Pro ke atas — 1,7× biaya MISS.
3. **`dpr_` > 2 dan `w_` > 4096** di Free; `MaxSide` 4096 px, 12 MP.
4. **Transform tanpa cache**: tolak `cb_` unik berlebihan dari satu proyek
   (cache-buster abuse = MISS tak terbatas). Batas MISS/proyek/menit.

### 7.4 Tiga syarat sebelum Free dibuka lebar

1. **Admission control per proyek** (sekarang limiter per-IP): batas MISS/menit
   per project ID, ditolak dengan 429 + `Retry-After`, bukan dengan menambah
   worker.
2. **Metering durabel**: spool lokal di worker + retry, idempoten per
   `(project, jam)`. Kalau ini tidak ada, Free akan menghabiskan kuota tanpa
   tercatat dan tidak ada dasar untuk upsell.
3. **Domain kustom & TLS**: tanpa ini, pelanggan Indonesia tidak bisa memakai
   `img.tokonya.id`, dan itu alasan nomor satu orang membayar.

### 7.5 Kapan menaikkan harga

Jangan naikkan harga; naikkan **kuota** sambil mempertahankan harga sampai
konversi Free→Starter >3%. Yang dinaikkan setelah itu adalah tier atas (Business
ke atas), bukan Starter — pelanggan Indonesia sensitif pada harga masuk, bukan
pada batas pemakaian.
