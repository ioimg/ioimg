# Contributing

Terima kasih sudah tertarik berkontribusi ke ioimg.

## Sebelum mulai

- Baca [`README.md`](README.md), [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md),
  dan [`docs/adr/`](docs/adr/) untuk konteks keputusan.
- Untuk perubahan kode produksi, buka issue atau diskusi dulu agar arahnya
  jelas.
- Dengan mengirim pull request, kamu setuju dengan [`CLA.md`](CLA.md).

## Setup

Service (Go ≥ 1.26):

```bash
cd service
go test ./...
go vet ./...
go build ./...
```

Research (opsional):

```bash
cd research/go-fiber && go test ./...
cd research/rust-uring && cargo test
```

Dataset benchmark tidak di-track. Lihat [`docs/ARTIFACTS.md`](docs/ARTIFACTS.md)
untuk membuatnya di `artifacts/`.

## Aturan pull request

- Satu PR = satu perubahan logis.
- Sertakan test untuk perubahan perilaku.
- Jangan commit `artifacts/`, `.env`, kredensial, atau output build.
- Update dokumentasi jika kontrak/URL/parameter berubah.
- Pastikan `gofmt`, `go vet`, dan `go test ./...` lulus.

## Commit

Gunakan pesan yang menjelaskan *kenapa*, bukan hanya *apa*. Contoh:

```
fix(build): track service/internal/cache package

The cache/ ignore pattern matched internal/cache/, so the package was
never committed.
```

## Lisensi kontribusi

Core dilisensikan under FSL-1.1-MIT. Dengan berkontribusi, kamu memberi ioimg
hak untuk mendistribusikan kontribusimu di bawah lisensi tersebut dan lisensi
komersial/enterprise yang mungkin ditawarkan proyek ini.
