# Artifacts (lokal, tidak di-track)

`artifacts/` adalah tempat semua **output** yang tidak layak masuk git:
dataset, cache, log benchmark, screenshot, dan snapshot pihak ketiga. Folder
ini di-ignore dan bisa dihapus kapan saja tanpa kehilangan source.

## Layout

```
artifacts/
├── datasets/
│   ├── src/                 # JPEG sintetis 2400x1600
│   └── src-smooth/          # varian untuk uji filter
├── cache/
│   ├── rust/                # cache hasil server Rust
│   └── go/                  # cache hasil server Go
├── snapshots/
│   └── kinstack/            # salinan starterkit untuk bench middleware
└── reports/                 # CSV, log mentah, screenshot, ekspor
```

## Regenerasi dataset

Rust (generator yang sama dengan benchmark asli):

```bash
cd research/rust-uring
cargo build --release
./target/release/gen-images ../../artifacts/datasets/src 8 2400 1600
```

Go memakai dataset yang sama:

```bash
cd research/go-fiber
./bin/ioimg-go -src ../../artifacts/datasets/src -cache ../../artifacts/cache/go
```

Compose service juga sudah menunjuk ke `artifacts/datasets/src`.

## Snapshot kinstack

```bash
tools/fetch-kinstack.sh ~/Projects/work/oss/kinstack
cd artifacts/snapshots/kinstack
patch -p1 < ../../../docs/BENCH-kinstack.patch
```

Pin versi upstream di issue/commit message saat mengulang benchmark, supaya
hasilnya bisa dibandingkan.

## Yang tetap di-track

- Source eksperimen (`research/`) dan harness benchmark.
- Metodologi + ringkasan hasil (`docs/BENCH-*.md`, `docs/benchmarks/`).
- Dokumen keputusan (`docs/adr/`).

Yang tidak di-track: data mentah, log, cache, snapshot, dan build output.
Kalau hasil benchmark ingin dibagikan, ringkas jadi tabel di
`docs/benchmarks/` atau lampirkan sebagai release asset.
