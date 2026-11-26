# io_uring di ioimg — di mana menang, di mana kalah

Diukur 2026-11-25 pada satu mesin. Semua angka punya perintah reproduksi di
[`research/io-uring/README.md`](../../research/io-uring/README.md).

| Item | Nilai |
| --- | --- |
| CPU | 16 core (amd64), tanpa pinning untuk loadgen |
| Kernel / Go | Linux 6.x, **go1.27.4** (`GOEXPERIMENT=nodwarf5`) |
| Disk | NVMe `ADATA LEGEND 710` (ext4 di `/`), **bukan** tmpfs |
| libvips | 8.18.7 |
| Harness | `/tmp/...` → disalin ke `research/io-uring/`; adapter celeris di `service/internal/server/celeris_variant.go` |

Ringkasan satu baris: **io_uring menang saat banyak operasi bisa masuk satu
`io_uring_enter` (socket, batch file). io_uring kalah saat satu operasi per
enter (baca cache per-request).**

---

## 1. Jalur HTTP HIT — framework, bukan io_uring

Handler produksi (`handleImage`: parse, ETag, cache L1, 12 header) tanpa
perubahan; hanya transport diganti. Body 12.804 B, **8 keep-alive conns**, 8 s,
3 ulangan, loadgen co-located.

| Transport | RPS | p50 | p90 | µs CPU/req |
| --- | --- | --- | --- | --- |
| `net/http` | 29.800 | 234 µs | 401 µs | 291 |
| Fiber v3, 1 proses | 45.800 | 138 µs | 299 µs | 165 |
| celeris, engine epoll | 48.500 | 123 µs | 297 µs | 128 |
| **celeris, engine io_uring** | **58.000** | **117 µs** | **172 µs** | **110** |

Diturunkan ke biaya: untuk saturasi 10 GbE pada body 12,8 KB (≈97.600 rps)
dibutuhkan **28 core** dengan `net/http` dan **10,7 core** dengan celeris
io_uring.

Catatan: celeris mengklaim engine epoll dan io_uring "at parity". Pada beban
8 conns body kecil, io_uring menang **+18% RPS / −14% CPU/req** atas epoll
celeris sendiri.

Pada body 92.894 B, c=8, urutannya berubah: `net/http` 17.535 rps,
celeris epoll 27.116, celeris io_uring 26.702 — io_uring **kalah tipis** dari
epoll, karena biayanya pindah ke copy memori (bandwidth), bukan syscall.

## 2. Baca cache per-request — io_uring kalah 22–29%

`internal/cache/disk.go` L1 adalah cache **disk** (index LRU di RAM, file di
disk) dan setiap HIT melakukan `os.ReadFile` + `os.Stat` (dua kali buka).
Cache dir di **ext4/NVMe** (bukan tmpfs — lihat catatan di bawah).

| Varian baca | RPS | p50 | µs CPU/req |
| --- | --- | --- | --- |
| `os.ReadFile` + `os.Stat` (kode saat itu) | 65.876 | 104 µs | 105 |
| `os.Open`+`f.Stat`+`io.ReadFull` (satu fd) | 65.849 | 105 µs | **101** |
| io_uring, `openat`+`read`+`close` (3 enter) | 46.909 | 145 µs | 138 |
| io_uring, linked `openat→read→close` + fixed files + fixed buffer (**1 enter**) | 51.374 | 139 µs | 139 |

Mikro, baca file saja tanpa HTTP (8 goroutine, 3 s, file cache 12.804 B,
2 ulangan):

| | ops/s | ns/op |
| --- | --- | --- |
| `os.ReadFile` | **406.694–425.341** | **2.351–2.459** |
| io_uring 3× enter | 324.786–330.875 | 3.022–3.079 |
| io_uring linked + fixed | 331.514–333.574 | 2.998–3.016 |

Diagnosis: selisih mikro hanya **+0,6 µs/read**, tetapi CPU per request naik
**+34 µs** (105 → 139). Sisanya adalah **wakeup thread**: setiap read
membangunkan worker ring, dan worker itu merebut CPU dari loop celeris yang
di-pin (`sched_setaffinity`). Menghapus syscall tidak menolong karena syscall
bukan biayanya — page-cached read pada dasarnya `memcpy`; yang mahal adalah
memindahkan pekerjaan ke thread lain.

> **Jangan menaruh cache di tmpfs bila memakai io_uring.** Pada pengukuran
> pertama, cache dir berada di `/tmp` (tmpfs): io_uring menjadi **2,1× lebih
> lambat per read** (4,9 µs vs 2,5 µs) dan end-to-end −33% (41k vs 62k rps),
> karena baca tmpfs lewat io_uring jatuh ke workqueue. Setelah cache
> dipindahkan ke ext4, selisihnya menyusut menjadi 22–29%. Angka di tabel
> adalah yang ext4.

Mikro juga mengungkap bug pada varian linked (slot fixed dipakai ulang sebelum
`close` selesai): memperbaiki dengan menunggu **semua** completion membuat
`bad` turun dari 374 ke 0. Angka di atas sudah setelah perbaikan.

## 3. Batch cache-warming — io_uring menang 2×, dan kalah saat device-bound

Dataset 1.024 file × 16 KB di ext4/NVMe, 10 ronde, 3 ulangan. io_uring:
linked `openat→read→close` per file, fixed slots + fixed buffers, **satu
`io_uring_enter` per batch**.

**Page cache (file panas):**

| Mode | files/s | MB/s |
| --- | --- | --- |
| `os.ReadFile` sequential | 190.900–215.500 | 3,1–3,5 |
| `os.ReadFile` 16 goroutine | 211.900 | 3,47 |
| io_uring batch 16 | 428.100 | 7,01 |
| io_uring batch 32 | 429.900 | 7,04 |
| io_uring batch 64 | 388.100–419.900 | 6,4–6,9 |
| io_uring batch 128 | 469.400 | 7,69 |

**≈2,0× lebih cepat**, dan batch 16 sudah memberi 2,1× — depth besar tidak
wajib.

**Device (O_DIRECT, page cache dilewati; `drop_caches` butuh root):**

| Mode | files/s | MB/s |
| --- | --- | --- |
| `os` sequential + O_DIRECT | 32.300 | 0,53 |
| **`os` 16 goroutine + O_DIRECT** | **88.100–137.700** | **2,24** |
| io_uring batch 16 | 59.400 | 0,97 |
| io_uring batch 32 | 93.400 | 1,53 |
| io_uring batch 64 | 96.600–107.000 | 1,73 |
| io_uring batch 128 | 112.100 | 1,84 |

io_uring **3,5× lebih cepat dari sequential**, tetapi **29% kalah dari 16
goroutine blocking**: 16 syscall paralel sudah memberi queue depth yang cukup,
dan io_uring menambah overhead ring + reap per batch.

## 4. Konteks: transformasi tetap batas atas

Bukan io_uring, tapi menentukan mengapa io_uring tidak bisa menjadi nilai jual
utama untuk MISS. libvips (`thumbnail_buffer` → JPEG q80), sumber 2400×1600,
`vips_concurrency=1`:

| Konkurensi | img/s | per core | p99 |
| --- | --- | --- | --- |
| 1 | 40,0 | 2,5 | 32 ms |
| 8 | 181,7 | 11,4 | 62 ms |
| **16 (= jumlah core)** | **219,3** | **13,7** | **105 ms** |
| 32 | 212,7 | 13,3 | 248 ms |

`vips_concurrency=1` (pool sendiri) mengalahkan default libvips
(`vips_concurrency=0`): 219,3 vs 167,0 img/s di puncak (−24%).

Jadi: tier HTTP sanggup ~58.000 rps, tier transform ~220 img/s — jarak **260×**.
Optimasi I/O tidak akan pernah menggeser kapasitas MISS; yang menggeser adalah
libvips, `strip`, format (WebP/AVIF), dan hit ratio.

## 5. Kontrak yang berlaku

| Jalur | io_uring | Bukti |
| --- | --- | --- |
| accept / recv / send (HIT) | **ya** | 58.000 vs 29.800 rps; 110 vs 291 µs CPU/req |
| baca cache per-request | **tidak** | 51.374 vs 65.876 rps; 139 vs 105 µs CPU/req |
| batch cache-warming (page cache) | **ya** | 428.000 vs 205.000 files/s |
| batch cold read (O_DIRECT) | **tidak** | 106.000 vs 137.000 files/s |
| fetch upstream (TLS) | tidak mungkin di Go | `crypto/tls` + `net/http` |
| transform libvips | tidak relevan | CPU, 219 img/s |

## Batasan pengukuran

- Satu mesin, satu NVMe, dataset kecil (16 MB); sel pertama tiap mode kadang
  15–20% lebih rendah (warmup) — dibuang dengan 3 ulangan + median.
- Loadgen berjalan di mesin yang sama. Di c≥512 semua transport mentok ~30k
  rps; itu **batas loadgen**, bukan batas server.
- HTTP A/B memakai adapter (`http.ResponseWriter` di atas `*celeris.Context`,
  `*fiber.Ctx`), bukan integrasi penuh; `handleImage` tidak diubah.
- Tidak ada `drop_caches` (tanpa root): "device" disimulasikan dengan
  O_DIRECT, sesuai untuk membandingkan read path, bukan untuk mengukur
  throughput disk absolut.

---

## 5. Verifikasi independen: hasil baca cache bergantung transport

Diukur ulang pada mesin lain (16 core, Linux 7.2.15, Go 1.27.4, cache di ext4),
kemudian diimplementasikan di `service/internal/transport` (ADR-0011). HIT
17.751 B, 8 keep-alive, 5 putaran round-robin, semua transport memakai
`handleImage` yang sama:

| Transport | Baca cache | RPS | p50 | p90 | vs net/http+os |
| --- | --- | --- | --- | --- | --- |
| `net/http` | `os.ReadFile`+`Stat` | 29.898 | 245 µs | 408 µs | 100% |
| `net/http` | satu fd | 29.590 | 249 µs | 414 µs | 99% |
| `net/http` | io_uring 3 enter | 29.471 | 250 µs | 414 µs | 99% |
| `net/http` | io_uring linked+fixed | 29.878 | 246 µs | 408 µs | 100% |
| Fiber v3 | `os.ReadFile`+`Stat` | 42.608 | 140 µs | 321 µs | 143% |
| Fiber v3 | satu fd | 44.240 | 133 µs | 310 µs | 148% |
| **Fiber v3** | **io_uring 3 enter** | **54.728** | 130 µs | **173 µs** | **183%** |
| Fiber v3 | io_uring linked+fixed | 54.448 | 131 µs | 173 µs | 182% |
| celeris `epoll` | satu fd | 54.328 | 104 µs | 299 µs | 182% |
| celeris `io_uring` | `os.ReadFile` | 65.106 | 99 µs | 155 µs | 218% |
| **celeris `io_uring`** | **satu fd** | **65.442** | **97 µs** | **149 µs** | **219%** |
| celeris `io_uring` | io_uring linked+fixed | 47.993 | 140 µs | 214 µs | 161% |

Kesimpulan yang berubah:

- **§2 di atas benar untuk celeris**, bukan untuk semua transport. Baca cache
  lewat io_uring merugikan **26%** di celeris (loop di-pin: wakeup worker
  merebut CPU dari loop) dan **netral** di `net/http`, tetapi **menang 28%** di
  Fiber (p90 321 → 173 µs) karena fasthttp tidak mem-pin loop.
- Karena itu **pilihan baca cache mengikuti transport**, bukan sebaliknya:
  celeris/`net/http` → blocking satu fd, Fiber → io_uring. Ini yang
  diimplementasikan di `service/internal/transport` (ADR-0011).
- Transport tetap faktor terbesar: `net/http` 29,9k → Fiber 42,6k →
  celeris io_uring 65,4k (**2,19×**), sebelum satu byte pun diubah di handler.
- Mikro `readbench` di mesin ini memberi urutan terbalik dari §2 (`os` 4.166 ns,
  `uring` 3.523, `linked` 3.607 per baca 16 KB), jadi selisih mikro **tidak**
  bisa dipakai sebagai dasar keputusan; end-to-end yang mengikat.

## 6. Prefork vs satu proses: diukur berpasangan, bukan lintas waktu

Angka prefork (72,7k) dan celeris (65,4k) di §5 datang dari jendela waktu yang
berbeda, jadi tidak boleh dibandingkan langsung. Diulang berpasangan pada mesin
yang sama, satu server aktif pada satu waktu, 3 putaran × 3 s, client 8
keep-alive:

| Konfigurasi | RPS (median) | p50 | p90 | proses |
| --- | --- | --- | --- | --- |
| **celeris `io_uring`, 1 proses** | **79.206** | 80 µs | 115 µs | 1 |
| celeris `io_uring`, 2 proses (SO_REUSEPORT) | 75.415 | 77 µs | 132 µs | 2 |
| Fiber v3 prefork 16, satu fd | 74.507 | 89 µs | 146 µs | 17 |
| Fiber v3 1 proses, io_uring cache read | 65.358 | 109 µs | 140 µs | 1 |
| celeris `io_uring`, 1 proses (ulangan) | 76.620 | 81 µs | 123 µs | 1 |

Kesimpulan:

- **Prefork bukan pemenang throughput.** Satu proses celeris tetap paling cepat
  (~6% di atas prefork) dan menang di tail (p90 115 vs 146 µs). Prefork hanya
  menang dibanding Fiber satu proses, karena Fiber satu proses memang lebih
  lambat.
- **Dua proses celeris tidak menambah throughput** di sini (75,4k): mesin sudah
  jenuh oleh kombinasi 16 worker + 8 thread client pada 16 core. Uji scaling
  proses butuh load generator di host terpisah.
- Karena itu tier prefork di ADR-0011 tetap ada sebagai **jalur
  ketersediaan** (io_uring diblok total), bukan sebagai jalur performa. Kalau
  throughput yang dicari, tambah replika/container dengan celeris, bukan
  prefork.

## 7. Re-run 2026-11-26 (setelah gate concurrency + kuota lifetime)

Benchmark diulang setelah tiga perubahan: gate concurrency per proyek
(`projectGate`, di jalur miss saja), kuota berbasis counter lifetime, dan
penghapusan `poc/` (client Rust lama ikut hilang). Karena itu harness-nya
dibangun ulang: client Go dengan request yang dibangun sekali per thread
(`bench/loadgen`, buffer 64 KiB, keep-alive) dan sumber sintetis 2400×1600
(`bench/genimg`), cache tetap di ext4 `~/.cache/ioimg-benchcache`.

Metodologi: 8 koneksi keep-alive, 3 s per putaran, 3–4 putaran, median. Mesin
dipakai bersama proses lain (load average 1,7–7 sepanjang sesi), jadi **angka
absolut tidak bisa dibandingkan dengan §5/§6** — yang stabil adalah urutan dan
rasio.

### 7.1 Satu server, mesin tanpa kontensi (HIT 17.751 B)

| Konfigurasi | RPS (3 putaran) | p50 | p99 |
| --- | --- | --- | --- |
| celeris `io_uring` 1 proses | 79.703 / 75.036 / 74.894 | 84–90 µs | 364–391 µs |

Median 75,0k — sama dengan 79,2k di §6 (selisihnya kontensi sesi, bukan
regresi). Gate concurrency tidak menambah biaya: `ioimg_concurrency_rejected_total 0`
dan HIT tidak pernah melewati gate.

### 7.2 Empat transport berdampingan (interleaved, 4 putaran)

Keempat server hidup bersamaan di port berbeda, diukur bergantian supaya drift
mesin saling meniadakan — jadi angka di sini **lebih rendah** dari 7.1 karena
saling berebut CPU, dan perbandingannya yang bermakna:

| Transport | RPS (median) | p50 | Rasio vs `net/http` |
| --- | --- | --- | --- |
| celeris `io_uring` 1 proses | **56.304** | 119 µs | **1,72×** |
| Fiber v3 prefork 16 | 52.024 | 130 µs | 1,59× |
| Fiber v3 1 proses | 47.475 | 156 µs | 1,45× |
| `net/http` | 32.812 | 200 µs | 1,00× |

Prefork tetap di bawah satu proses celeris (+9,5% di atas Fiber satu proses,
bukan 1,8× seperti klaim lama). Konsisten dengan §6.

### 7.3 Cache MISS (vips, 1 thread, 8 s, sumber 853 KB)

| Opsi | RPS | ms/gambar |
| --- | --- | --- |
| `w_400,f_jpg,q_80` (`c_fit`) | 59 | 14,0 |
| `w_800,c_fill,f_jpg,q_80` | 40 | 21,6 |
| `w_400,f_jpg,q_80,dt_…` (duotone) | 39 | 22,6 |
| `w_400,f_webp,q_80,webpo_6` | 24 | 37,1 |
| `w_400,f_avif,q_50` | 5 | 184,0 |

Dua catatan yang tetap berlaku: **`c_fill` masih jalan tanpa shrink-on-load**
(21,6 ms vs 14,0 ms untuk lebar yang sama), dan efek per-piksel (`dt`) menambah
~8 ms karena memaksa round-trip `vips.ImageRef` → `image.Image`. Angka absolut
tidak sebanding dengan tabel MISS sebelumnya (sumber 853 KB vs 373 KB, mesin
lebih ramai).
