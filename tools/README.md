# Tools

Skrip repo-level. Semua output ditulis ke `artifacts/` (gitignored).

| Skrip | Fungsi |
| --- | --- |
| [`fetch-kinstack.sh`](fetch-kinstack.sh) | Salin/clone starterkit `kinstack` ke `artifacts/snapshots/kinstack` untuk bench middleware |

Contoh:

```bash
tools/fetch-kinstack.sh ~/Projects/work/oss/kinstack
cd artifacts/snapshots/kinstack
patch -p1 < ../../../docs/BENCH-kinstack.patch
```

Generator dataset ada di `research/rust-uring` (`gen-images`); lihat
[`docs/ARTIFACTS.md`](../docs/ARTIFACTS.md).
