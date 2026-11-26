# Konkurensi, ekor latensi, dan memori per request

Dokumen ini menjawab tiga pertanyaan kapasitas yang tidak terjawab oleh
benchmark throughput: **berapa koneksi yang sehat, seberapa panjang ekor
latensinya, dan berapa RAM yang harus disediakan per request in-flight.**

Semua angka diukur lewat binary produksi (image `ioimg:vips-prod`, libvips
8.18.7 + zlib-ng + libaom 3.15.3) di dalam container `--cpus=2`, origin nginx
lokal, korpus DIV2K, transform `w_512,h_512,c_fit,f_jpg,q_80`, latensi diukur
**setelah body selesai dibaca** (bukan time-to-first-byte).

## 1. Metode

- Loadgen: `/tmp/loadgen` (`-path ...?i={i}` dengan counter global, `-threads N`,
  `-secs 8`).
- Setiap run memakai container baru dan port baru, port dicek kosong lebih dulu
  (`ss -ltn`). Ini wajib: celeris memakai `SO_REUSEPORT`, jadi container lama
  yang belum benar-benar mati tetap melayani dan mencampur hasil.
- Semua request MISS diverifikasi: jumlah file yang dibuat di cache dir harus
  **sama** dengan jumlah request yang dilaporkan loadgen. Kalau tidak sama,
  angka dibuang.
- Memori dibaca dari `memory.stat` cgroup v2 (`anon`, bukan `file`, supaya page
  cache tidak ikut terhitung).

## 2. Hasil (MISS, 2 core)

| Koneksi | img/s | p50 | p90 | p99 | max | anon setelah run |
| --- | --- | --- | --- | --- | --- | --- |
| 2 | **133** | 14,4 ms | 17,6 ms | 20,2 ms | 49,3 ms | 75 MB |
| 8 | 101 | 92,7 ms | 101,9 ms | 172,6 ms | 183,8 ms | 173 MB |
| 32 | 56 | 502,7 ms | 1.099 ms | 1.303 ms | 1.398 ms | 401 MB |

Titik optimal di 2 core adalah **2 koneksi**. Di atas itu throughput turun dan
latensi tumbuh hampir linier: 8 koneksi sudah 10x p50, 32 koneksi 35x, tanpa
tambahan kapasitas. Ini bukan bug server, tapi konsekuensi pipeline yang sudah
memakai ~1,4 core per request (lihat `pinning-and-zlib-ng.md` §2): menyuntik
lebih banyak request hanya menambah antrean.

## 3. HIT (cache hidup, 2 core)

| Koneksi | img/s | p50 | p90 | p99 | max |
| --- | --- | --- | --- | --- | --- |
| 8 | **31.667** | 210 µs | 455 µs | 777 µs | 1,4 ms |

HIT tidak menyentuh pipeline gambar sama sekali (hanya baca file + tulis
socket), sehingga skalanya mengikuti core, bukan memori. Di 16 core angka ini
menjadi ~77k img/s (lihat `PERFORMANCE.md`).

## 4. Memori per request in-flight

Dari tabel di atas, `anon` naik dari 27 MB (idle) menjadi:

| In-flight | anon | Selisih | Per request |
| --- | --- | --- | --- |
| 2 | 75 MB | +48 MB | ~24 MB |
| 8 | 173 MB | +146 MB | ~18 MB |
| 32 | 401 MB | +374 MB | ~12 MB |

Angka per request turun seiring konkurensi karena arena allocator dipakai
bersama, jadi model yang dipakai untuk kapasitas adalah **~12 MB per request
MISS in-flight** (sumber 2400×1600 JPEG 853 KB: decode RGB ~11,5 MB + keluaran
+ buffer encoder).

Konsekuensi praktis:

- Container 1 GB sehat untuk ~60 request MISS in-flight sekaligus, tapi CPU
  sudah mentok jauh sebelum itu. Batas nyata adalah CPU, bukan RAM, **kecuali**
  untuk sumber besar (PNG 4,7 MB DIV2K: decode 2040×1356 RGB ~8 MB, ditambah
  buffer inflate zlib).
- Karena itu `IOIMG_CONCURRENCY` (gate per proses) dan gate per proyek wajib
  diisi berdasarkan `RAM / 12 MB`, bukan berdasarkan jumlah core.
- HIT hampir gratis: +8 MB untuk 250 ribu request.

## 5. Yang belum diukur

- Memori untuk sumber besar (>40 MP) dan AVIF (encoder libaom menyimpan frame
  penuh + reference buffer; perkiraan 2-3x JPEG, belum diverifikasi).
- Tail latency di mesin produksi (8 core, Jakarta) — rasio yang bisa dibawa
  hanya bentuk kurvanya, bukan angka absolutnya.
- Perilaku saat cache penuh (eviction) di bawah beban tulis bersamaan.
