# Competitive Landscape — ioimg

Dokumen kerja, bukan materi pemasaran. Tujuannya: tahu di mana ioimg bisa menang dan di mana tidak usah bertarung.

---

## 1. Peta produk

| Produk | Model | Stack | Kekuatan | Kelemahan |
| --- | --- | --- | --- | --- |
| **imgproxy** | Self-host (Pro berbayar) | Go, libvips | Sangat cepat, matang, signed URL, ops lengkap | DX: butuh infra; tidak ada plugin framework; konfigurasi env-heavy |
| **Thumbor** | OSS self-host | Python | Fleksibel, filter kaya, komunitas lama | Latensi & memori lebih tinggi; maintenance lambda; performa di bawah Rust/Go |
| **Imagor** | OSS self-host (fork Thumbor di Go) | Go | Lebih cepat dari Thumbor, drop-in-ish | Fitur & komunitas lebih kecil; docs terbatas |
| **Cloudflare Images / Polish** | Managed | — | Zero ops, edge global, murah-ish | Lock-in, fitur terbatas, debugging sulit, harga naik per-transform |
| **ImageKit** | Managed | — | DX bagus, CDN + DAM, transform URL | Mahal pada volume, vendor lock, limitasi plan |
| **Bunny Optimizer** | Managed | — | Sangat murah, CDN cepat | Transform terbatas, tidak self-host |
| **wsrv.nl (dulu images.weserv.nl)** | Free/OSS | Node + sharp | Gratis, URL simpel, fitur dasar | Bukan untuk produksi serius (tanpa SLA, open proxy) |
| **Cloudinary** | Managed | — | Paling lengkap (video, AI) | Mahal, lock-in ekstrem |
| **next/image** (built-in) | Library | Node + sharp | Nol konfigurasi di Next | Hanya Next, cache di instance, cold start |
| **Vercel Image Optimization** | Managed | — | Terintegrasi Next | Mahal per 1k, batas ketat |

## 2. Di mana ioimg menang

1. **Performa serving + DX** — kombinasi "cepat seperti imgproxy" + "mudah seperti Cloudflare". Pasar self-host saat ini memilih salah satu.
2. **io_uring** — satu-satunya di kategori ini yang memakai completion I/O secara serius (imgproxy/Go masih netpoller). Ini pembeda teknis yang bisa dibuktikan lewat benchmark.
3. **Satu URL untuk semua platform** — plugin resmi lintas stack, bukan hanya satu framework.
4. **Transparansi biaya** — self-host gratis + managed dengan harga per GB yang jelas; tanpa biaya "per transformasi unik" yang mengejutkan.
5. **Observability kelas produk** — metrics/trace per fase, dashboard bawaan; imgproxy/Thumbor menyerahkan ke user.

## 3. Di mana ioimg kalah (jujur)

- **Ekosistem & kepercayaan**: imgproxy punya 10k+ bintang dan bertahun-tahun produksi. ioimg baru.
- **Fitur**: tidak ada video, DAM, AI transform, atau editor visual seperti Cloudinary/ImageKit.
- **Edge global**: Cloudflare/Bunny punya ratusan PoP; ioimg butuh CDN di depan (kami bergantung pada mitra).
- **Kematangan codec**: binding C (libwebp/libavif) butuh waktu untuk diuji; sharp/libvips sudah teruji di jutaan situs.

## 4. Positioning statement

> Untuk tim engineering yang ingin gambar cepat tanpa memindahkan storage ke vendor, **ioimg** adalah image proxy self-host-first yang memberi kecepatan kelas datacenter (Rust + io_uring) dengan kemudahan integrasi satu-URL di framework apa pun — berbeda dari imgproxy yang infra-heavy dan Cloudflare Images yang mengunci.

Tagline kandidat:
- "Fast images, one URL."
- "Bring your own storage. Keep your own speed."
- "The image proxy that doesn't get in the way."

## 5. Strategi

**Beachhead:** agency & SaaS kecil-menengah dengan Next.js/Nuxt + WordPress, yang sudah punya S3/CDN tapi belum punya image pipeline.

**Wedge:** plugin gratis + free tier managed → upgrade ke Pro/Scale; self-host untuk yang tidak mau data keluar.

**Moat (jangka panjang):** benchmark publik yang reproducible + integrasi paling lengkap + operasional yang membosankan (boring = dipercaya).

## 6. Riset yang perlu dilakukan

- [ ] Benchmark head-to-head: imgproxy vs ioimg (W1–W6) — publikasikan metodologi.
- [ ] Wawancara 10 agency: apa yang menghalangi mereka pakai imgproxy?
- [ ] Harga pesaing saat ini (per 1k transform, per GB) — tabel hidup.
- [ ] Cek permintaan nyata untuk `io_uring` di komunitas Rust (apakah ini nilai jual yang dipahami pembeli?).
