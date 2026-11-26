# Verifikasi: fuzz, kualitas encoder, soak, memori, multi-replika

Pemeriksaan yang selama ini hanya disebut "belum diukur". Angka di bawah
dari binary hasil build (`-tags "vips libavif"`, commit `9301dfb`), bukan dari
`go test` yang dijadikan tolok ukur performa.

## 1. Fuzz parser URL

Target baru: `service/internal/server/fuzz_test.go` (`FuzzParseTarget`). Parser
ini dilewati setiap request, jadi panic di sini adalah insiden produksi.

| Metrik | Nilai |
| --- | --- |
| Durasi | 60 s |
| Eksekusi | 1.952.999 (≈32.500/s) |
| Crash / panic | 0 |
| Input menarik | 219 |

Invarian yang diuji: tidak pernah panic, dan target yang diterima tanpa error
tidak boleh punya source kosong.

## 2. Kualitas AVIF: libavif `speed 9` vs heifsave `effort 1`

Sumber DIV2K `0801.png`, fit 512, q65, dua binary dengan
`IOIMG_AVIF_ENCODER=libavif` dan `=vips`.

| Encoder | ukuran | SSIM vs PNG | PSNR |
| --- | --- | --- | --- |
| libavif speed 9 | 36.950 B | 0,9678 | 32,22 dB |
| heifsave effort 1 | 36.950 B | 0,9678 | 32,22 dB |

Keduanya **byte-identik** (md5 `95c026df…`), jadi pilihan encoder tidak
mengubah kualitas maupun ukuran pada setelan ini — hanya waktu encode.
Kontrol: dengan `IOIMG_AVIF_EFFORT=9` jalur heifsave memang aktif (7,6 s untuk
satu gambar), jadi kesamaan di atas bukan karena env diabaikan.

## 3. Soak HIT 90 detik

Satu proses, 4 koneksi, 200 URL yang sudah hangat (semua HIT), cache 512 MB.

| Metrik | Nilai |
| --- | --- |
| Request | 3.122.495 (34.694 rps) |
| p50 / p90 / p99 | 89 µs / 157 µs / 390 µs |
| max | 2,5 ms |
| Error | 25 transport (8×10⁻⁶), bukan 5xx |
| RSS | 532–538 MB, datar |

RSS tidak tumbuh selama 90 detik; 25 error berasal dari sisi klien (reset
koneksi), bukan dari server (`codes=200:3122495` = seluruh respons).

## 4. Soak MISS — 5 menit, tanpa HIT

Loadgen sempat punya bug sendiri: `SetDeadline` dipasang sekali saat dial,
sehingga setiap koneksi mulai gagal `i/o timeout` setelah 10 detik dan tampak
seperti server stall (58 error palsu di run pertama). Deadline sekarang dipasang
per request.

| Metrik | Nilai |
| --- | --- |
| Request | 34.573 (0 error) |
| p50 / p90 / p99 | 17,0 ms / 20,6 ms / 25,8 ms |
| max | 60,4 ms |
| `cachefiles == n` | tepat (semua MISS benar-benar menulis) |
| RSS | 138 → 153 MB, datar |

## 5. Memori per jenis sumber, dan bug arena glibc

RSS puncak (MB) saat N request paralel, cache kosong:

| Sumber | t=1 | t=2 | t=4 | t=8 |
| --- | --- | --- | --- | --- |
| JPEG 2400×1600 (853 KB) | 138 | 171 | 223 | 303 |
| PNG 2040×1356 (4,7 MB) | 159 | 192 | 271 | 375 |
| AVIF 2040×1356 | 536 | 599 | 630 | 762 |

14 request AVIF **sekuensial** menaikkan RSS ~20 MB per request (83 → 360 MB),
sementara JPEG datar. Bukan kebocoran C: buffer AVIF dibebaskan di thread
berbeda, dan glibc membuat arena baru untuk tiap thread. `MALLOC_ARENA_MAX=2`
membuat urutan yang sama datar (141/119/144 MB).

Perbaikan: `internal/imaging/alloc_glibc.go` memanggil `mallopt(M_ARENA_MAX, 2)`
di constructor cgo bila env belum diset (override tetap dihormati). Biaya yang
terukur: AVIF t=8 turun 11% (68,4 → 61,0 img/s) dan RSS turun 55% (700–735 →
283–333 MB); PNG dan JPEG tidak berubah di dalam noise.

## 6. Multi-replika: kuota dan kapasitas

Stack: Postgres 16 + Valkey 8 + control plane (`:3010`) + dua replika data
plane dengan cache terpisah, origin nginx lokal, korpus DIV2K.

### 6.1 Kuota fail-closed lintas replika

Tiga bug ditemukan saat menguji, semuanya nyata:

1. **Transport hanya meneruskan tiga header.** `X-IOIMG-Project`,
   pass-through header, cookie, `Range`, dan `X-Forwarded-For` hilang di jalur
   celeris/fiber. Akibatnya rate limit per-IP di belakang CDN salah dan header
   proyek tidak berfungsi.
2. **Usage report tanpa ID.** Data plane mengirim `id` kosong; kontrol plane
   memakai ID itu sebagai kunci idempotensi, jadi semua report setelah yang
   pertama dianggap replay dan dibuang. 238 transform tercatat sebagai 1.
3. **Lease dihitung dari sisa, bukan dari jumlah.** Reservasi hanya
   `SUM(amount - used)`, sehingga lease yang sudah dihabiskan tapi belum
   dilaporkan tidak lagi dihitung — kontrol plane terus memberi lease baru.

Ditambah satu bug di sisi data plane: budget menambahkan sisa kuota snapshot
(lag 10 detik) **di atas** lease, dan saat kehabisan lease replika tidak
menunggu refresh yang sedang berjalan — ia langsung menolak, sehingga satu
round trip 35 ms menjadi ratusan 429.

Hasil uji 600 request dibagi dua replika, plan 200 transform:

| | Sebelum | Sesudah |
| --- | --- | --- |
| Diterima | 238 (plan 120) | **199 (plan 200)** |
| 429 | 462 | 401 |
| `usage_monthly` | 1 | **199** |
| `SUM(lease.amount)` | — | 200 (tidak melebihi plan) |

Tidak ada usage yang hilang atau dobel: `/v1/stats` = jumlah respons 200.

### 6.2 Kapasitas agregat (laptop 16 core, tanpa pin CPU)

| Konfigurasi | Total img/s | p50 |
| --- | --- | --- |
| 1 replika × 2 koneksi | 133 | 14,9 ms |
| 2 replika × 1 koneksi | 65 + 65 = **130** | 15,0 ms |
| 2 replika × 2 koneksi | 106 + 106 = **212** | 18,5 ms |

Dua replika × 1 koneksi menghasilkan total yang sama dengan satu proses × 2
koneksi: kapasitas mengikuti jumlah transform paralel, bukan jumlah proses.
Menambah replika tidak menambah throughput per core — yang bertambah adalah
ketersediaan. Pada 4 transform paralel totalnya 212 img/s, konsisten dengan
sweep konkurensi di `concurrency-and-memory.md`.

## 7. Yang masih belum diverifikasi

- **Dua mesin sungguhan**: semua angka di atas dari satu laptop. Dua replika di
  mesin berbeda (network hop ke kontrol plane, latensi lease nyata) belum diuji.
- **Penerbitan sertifikat ACME nyata**: policy, cache, dan redirect sudah diuji
  offline; belum pernah meminta sertifikat dari Let's Encrypt.
- **Dampak latensi kontrol plane**: lease butuh ~35 ms di laptop ini; di mesin
  lain angka itu menentukan seberapa sering 429 muncul saat kuota habis.
