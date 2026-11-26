# Verifikasi: fuzz, kualitas encoder, soak HIT

Tiga pemeriksaan yang selama ini hanya disebut "belum diukur". Angka di bawah
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

## 4. Yang masih belum diverifikasi

- **Multi-replika**: belum. Uji dua proses berdampingan di laptop ini hanya
  mengukur kontensi CPU, bukan kapasitas; angka kapasitas yang ada tetap
  single-process. Uji sebenarnya butuh dua mesin (atau container dengan kuota
  CPU terpisah) dan kontrol plane hidup.
- **Soak MISS**: yang diukur di atas jalur HIT. MISS berjalan menit-an dengan
  beban transformasi belum diuji.
- **Memori per request untuk sumber besar**: model 12 MB/request berasal dari
  sumber JPEG 2400×1600; PNG 4,7 MB dan AVIF belum diukur terpisah.
