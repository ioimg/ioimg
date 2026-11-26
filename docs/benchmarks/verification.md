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

Empat bug ditemukan saat menguji, semuanya nyata:

1. **Transport hanya meneruskan tiga header.** `X-IOIMG-Project`,
   pass-through header, cookie, `Range`, dan `X-Forwarded-For` hilang di jalur
   celeris/fiber. Rate limit per-IP di belakang CDN jadi salah dan header
   proyek tidak berfungsi.
2. **Usage report tanpa ID.** Data plane mengirim `id` kosong; kontrol plane
   memakai ID itu sebagai kunci idempotensi, jadi semua report setelah yang
   pertama dianggap replay dan dibuang. 238 transform tercatat sebagai 1.
3. **Lease dihitung dari sisa, bukan dari jumlah.** Reservasi hanya
   `SUM(amount - used)`, sehingga lease yang sudah dihabiskan tapi belum
   dilaporkan tidak lagi dihitung — kontrol plane terus memberi lease baru.
4. **Replika menolak alih-alih menunggu.** Saat lease habis dan refresh sedang
   berjalan, request langsung 429. Karena 429 murah (~30 µs), satu round trip
   kontrol plane 35 ms menjadi ratusan penolakan. Sekarang request menunggu
   refresh (maks `leaseWait`) lalu lanjut.

Hasil uji (data plane final, `ioimgC`):

| Uji | Plan | Diterima | 429 | `usage_monthly` | `SUM(lease.amount)` |
| --- | --- | --- | --- | --- | --- |
| 1 replika, 8 koneksi | 400 | **400** | 568 | 400 | 400 |
| 2 replika, 1 koneksi tiap replika | 200 | 95 + 94 = **189** | 6400 | 189 | 200 |

Tidak ada overshoot. Yang perlu diketahui: pada uji dua replika 11 unit tidak
terpakai, karena lease diberikan per blok (sepersepuluh plan) dan sisa blok baru
kembali setelah lease kedaluwarsa 5 menit. Jadi saat plan hampir habis, ekspektasi
yang benar adalah "tidak pernah lebih dari plan", bukan "selalu tepat plan".

Sebelum perbaikan, uji yang sama (plan 120) menerima **238**.

### 6.2 Sensitivitas latensi kontrol plane

Kontrol plane disalip proxy yang menambah jeda, sehingga lease punya biaya
round trip realistis. Plan 400, 2 koneksi, semua MISS:

| Latensi lease | Diterima | 429 | p50 | p99 | Grant |
| --- | --- | --- | --- | --- | --- |
| ~45 ms (lokal) | 401 → 400 setelah fix | 104 | 17,5 ms | 101 ms | 10 |
| ~350 ms | 607 (plan 1000) | 18 | 16,4 ms | **379 ms** | 7 |

Kesimpulan: setelah perbaikan, latensi kontrol plane muncul sebagai **ekor
latensi**, bukan hujan error — satu round trip lease terlihat sebagai p99, dan
p50 tidak berubah. Sebelum perbaikan, kondisi yang sama menghasilkan ratusan 429
dalam 35 ms.

Catatan: baris pertama diukur sebelum kredit bootstrap dihapus. Setelah itu
replika tidak lagi boleh memakai sisa kuota snapshot sebelum lease pertama
tiba; biayanya satu round trip pada request pertama per proses, dan bila
kontrol plane tidak bisa dihubungi sama sekali, replika menolak melayani
(fail-closed) alih-alih menebak sisa kuota.

### 6.3 Kapasitas agregat (laptop 16 core, tanpa pin CPU)

| Konfigurasi | Total img/s | p50 |
| --- | --- | --- |
| 1 replika × 2 koneksi | 133 | 14,9 ms |
| 2 replika × 1 koneksi | 65 + 65 = **130** | 15,0 ms |
| 2 replika × 2 koneksi | 106 + 106 = **212** | 18,5 ms |

Dua replika × 1 koneksi menghasilkan total yang sama dengan satu proses × 2
koneksi: kapasitas mengikuti jumlah transform paralel, bukan jumlah proses.
Menambah replika tidak menambah throughput per core — yang bertambah adalah
ketersediaan.

## 7. ACME: penerbitan sertifikat sungguhan

Selama ini hanya policy/cache/redirect yang diuji. Sekarang alurnya dijalankan
sungguhan terhadap **Pebble** (ACME server uji milik Let's Encrypt), dengan
`IOIMG_TLS_CA` menunjuk directory Pebble dan `SSL_CERT_FILE` mempercayai CA-nya.

```bash
docker run -d --name pebble -p 14000:14000 -p 15000:15000 \
  -e PEBBLE_VA_ALWAYS_VALID=1 -e PEBBLE_VA_NOSLEEP=1 \
  ghcr.io/letsencrypt/pebble:latest
# data plane
IOIMG_TLS=auto IOIMG_TLS_ADDR=:8443 IOIMG_TLS_HTTP_ADDR=:10380 \
IOIMG_TLS_CACHE_DIR=/tmp/acme-cache IOIMG_TLS_CA=https://localhost:14000/dir \
IOIMG_TLS_HOSTS=bench.local SSL_CERT_FILE=/tmp/pebble-ca.pem \
IOIMG_ADDR=:10331 /tmp/ioimgB
```

Hasil:

| Pemeriksaan | Hasil |
| --- | --- |
| Sertifikat diterbitkan | issuer `CN=Pebble Intermediate CA`, masa berlaku 6 hari |
| HTTPS melayani gambar | `200`, 55.329 B (identik dengan jalur plain) |
| Cache sertifikat | `/tmp/acme-cache/bench.local` + `acme_account+key` |
| Restart proses | sertifikat dipakai ulang (mtime tidak berubah → tidak menerbitkan ulang) |
| HTTP → HTTPS | `308` ke `https://<host>:8443/...` |
| SNI tidak dikenal | handshake ditolak (`tlsv1 alert internal error`) |
| `/healthz` di port plain | tetap `200` (load balancer tidak perlu sertifikat) |

Yang **belum** diuji: penerbitan dari Let's Encrypt sungguhan, yang butuh domain
publik, port 80/443 terbuka, dan kuota rate limit CA nyata. Semua logika di atas
identik, hanya directory URL dan kepercayaan CA yang berbeda.

## 8. Yang masih belum diverifikasi

- **Dua mesin fisik**: semua angka dari satu laptop. Dua replika di mesin
  berbeda (network hop nyata, jam berbeda, kegagalan jaringan) belum diuji;
  latensi lease disimulasikan lewat proxy, bukan lewat jaringan sungguhan.
- **Let's Encrypt produksi**: lihat §7.
- **Dampak pemisahan jaringan pada single-flight cache L2**: dua replika masih
  berbagi cache dir lokal masing-masing; koordinasi penulisan L2 antar replika
  belum diuji.
