# Architecture Decision Records

Format ringkas: **Konteks → Keputusan → Konsekuensi → Alternatif yang ditolak.**

| ID | Judul | Status |
| --- | --- | --- |
| [0001](0001-rust-axum.md) | Rust + axum sebagai fondasi service | Superseded by 0008 |
| [0002](0002-io-uring-data-plane.md) | io_uring diisolasi di data plane, bukan HTTP stack | Revised by 0007 |
| [0003](0003-cache-design.md) | Cache dua lapis dengan key deterministik | Accepted |
| [0004](0004-signed-url.md) | Signed URL path-based HMAC, exp di canonical | Accepted |
| [0005](0005-url-scheme.md) | Skema URL path-based sebagai kontrak utama | Accepted |
| [0006](0006-infra-adapters.md) | Storage & CDN sebagai adaptor, `Vary` tidak diasumsikan | Accepted |
| [0007](0007-axum-on-compio.md) | `axum` di atas `compio` (`cyper-axum`) sebagai fast path | Rejected by 0008 |
| [0008](0008-go-data-plane.md) | Data plane Go (`net/http`), imaging pure Go + jalur libvips, cache disk L1 | Accepted |
| [0009](0009-control-plane.md) | Control plane multi-tenant, snapshot pull, signing endpoint, dashboard | Accepted |
