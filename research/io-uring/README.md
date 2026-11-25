# research/io-uring — harness bukti cakupan io_uring

Harness untuk tiga percobaan yang hasilnya dicatat di
[`docs/benchmarks/io-uring.md`](../../docs/benchmarks/io-uring.md) dan dipakai
[ADR-0010](../../docs/adr/0010-io-uring-scope.md).

Ringkasan hasil: **socket menang, baca cache per-request kalah, batch warming
menang 2×.**

| Isi | Fungsi |
| --- | --- |
| `internal/uringread` | Dua pembaca file lewat io_uring: `ReadFile` (openat→read→close, 3 enter) dan `ReadFileLinked` (linked, **1 enter**, fixed files + fixed buffer) |
| `cmd/readbench` | Mikro: `os.ReadFile` vs `uring` vs `linked`, tanpa HTTP |
| `cmd/batchbench` | Batch: `os-seq`, `os-par`, `os-direct`, `os-par-direct`, `uring`, `uring-direct` |

Dependensi: `github.com/miretskiy/dio/v2` (binding io_uring lifetime-safe,
Linux-only) dan `golang.org/x/sys`.

## Prasyarat

- Linux **≥ 5.18** (`IORING_FEAT_LINKED_FILE`, wajib oleh ringo) dan
  `io_uring_setup` tidak diblok seccomp.
- Go 1.27.3+.
- **Dataset di disk nyata, bukan tmpfs.** `/tmp` di banyak mesin adalah tmpfs;
  baca tmpfs lewat io_uring jatuh ke workqueue dan hasilnya menyesatkan
  (lihat catatan di [`docs/benchmarks/io-uring.md`](../../docs/benchmarks/io-uring.md) §2).

## Reproduksi

Dataset 1.024 file × 16 KB (16 KB = kelipatan 4096 supaya O_DIRECT valid):

```bash
export BBDATA=~/.cache/ioimg-benchdata
mkdir -p $BBDATA
head -c 16384 /dev/urandom > /tmp/seed.bin
for i in $(seq 0 1023); do cp /tmp/seed.bin $BBDATA/f$(printf %04d $i).bin; done
```

Batch, page cache (file panas) — harapan: io_uring ≈ 2× lebih cepat:

```bash
cd research/io-uring
go run ./cmd/batchbench -dir $BBDATA -mode os-seq      -rounds 10
go run ./cmd/batchbench -dir $BBDATA -mode os-par      -rounds 10
go run ./cmd/batchbench -dir $BBDATA -mode uring -batch 64 -rounds 10
```

Batch, device (`O_DIRECT`, page cache dilewati; tidak butuh root) — harapan:
goroutine blocking menang ~29%:

```bash
go run ./cmd/batchbench -dir $BBDATA -mode os-direct      -rounds 10
go run ./cmd/batchbench -dir $BBDATA -mode os-par-direct  -rounds 10
go run ./cmd/batchbench -dir $BBDATA -mode uring-direct -batch 64 -rounds 10
```

Mikro per-request read (pakai file cache 12–16 KB yang sudah ada di disk):

```bash
F=$BBDATA/f0000.bin
for m in os uring linked; do
  go run ./cmd/readbench -f $F -c 8 -d 3s -mode $m
done
```

## A/B end-to-end (tidak ada di modul ini)

Percobaan transport dan baca-cache-per-request memakai handler produksi
(`service/internal/server.handleImage`), jadi harness-nya adalah patch pada
modul `service`, bukan modul terpisah:

1. `service/internal/server/celeris_variant.go` — adapter
   `*celeris.Context → http.ResponseWriter` (padanan `fiber_variant.go`;
   tulis body langsung ke context, tanpa buffer perantara, supaya tidak
   menambah satu copy 12 KB per request).
2. `service/cmd/celbench/main.go` — server yang sama dengan
   `-engine iouring|epoll|adaptive|std`.
3. `service/internal/cache/disk.go` — tiga flag env untuk A/B baca cache:
   `IOIMG_CACHE_URING` (3 enter), `IOIMG_CACHE_URING_LINKED` (linked + fixed),
   `IOIMG_CACHE_OPENSTAT` (`os.Open`+`f.Stat`+`io.ReadFull`).
4. `service/cmd/readbench` → pindah ke modul ini sebagai `cmd/readbench`.

Menjalankan satu sel: server dinyalakan dengan `IOIMG_CACHE_DIR` di **ext4**,
cache dihangatkan dengan satu request, lalu loadgen keep-alive 8 koneksi
mengukur 8 detik setelah 2 detik warmup.

## Temuan yang harus diingat saat mengubah harness

- **Bug slot fixed (sudah diperbaiki).** Varian linked mula-mula menunggu
  hanya completion `read`, lalu memakai ulang slot sementara `close` masih
  in-flight → `openat` gagal dan `bad` naik (374 pada 4 detik). Sekarang
  menunggu **semua** completion dalam rantai.
- **Kedalaman ring.** Batch 16 sudah memberi 2,1×; 128 memberi 2,3×. Tidak
  ada alasan memakai depth besar untuk warming.
- **Biaya sebenarnya bukan syscall** melainkan perpindahan thread: handoff ke
  worker ring merebut CPU dari loop server yang di-pin.
- **Satu mesin, satu NVMe.** Angka absolut tidak boleh dipindah tanpa rasio;
  sel pertama tiap mode bisa 15–20% lebih rendah (warmup) — pakai ≥3 ulangan
  dan median.
