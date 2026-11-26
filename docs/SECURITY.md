# Security — ioimg

ioimg adalah service yang **mengambil URL dari internet lalu men-decode file biner dari pihak tak dikenal**. Ini salah satu profil risiko tertinggi di sebuah backend. Dokumen ini menetapkan model ancaman dan kontrolnya.

---

## 1. Model ancaman

| # | Ancaman | Aktor | Dampak | Kontrol |
| --- | --- | --- | --- | --- |
| T1 | SSRF: fetch `http://169.254.169.254/latest/meta-data/` | Penyalahguna | Kredensial cloud bocor | Allowlist default-deny, IP filtering, DNS pinning, no redirect ke IP privat |
| T2 | Open proxy / bandwidth theft | Penyalahguna | Biaya egress, reputasi | Signed URL, allowlist, rate limit, kuota |
| T3 | Decoder RCE (CVE di libjpeg/libwebp/libavif) | Penyerang | RCE di server | Decoder Rust memory-safe bila ada, worker terpisah, seccomp ketat, patch SLA |
| T4 | Decompression bomb | Penyerang | OOM / DoS | Batas dimensi & MP sebelum decode penuh, limit memori per transformasi |
| T5 | Abuse compute (AVIF encode mahal) | Penyalahguna | CPU exhaustion | Semaphore AVIF, per-tenant concurrency limit, kuota |
| T6 | Cache poisoning | Penyerang | User menerima gambar salah | Key deterministik + signature, `Vary: Accept`, tidak cache error user-specific |
| T7 | Cross-tenant data leak | Bug internal | Kebocoran aset privat | `project_id` di cache key, scoped purge, audit |
| T8 | Key/salt bocor | Insider/leak | Akses aset privat | Rotasi + `kid`, hash API key (Argon2id), secret manager |
| T9 | SVG XSS | Penyerang | XSS di domain pelanggan | Sanitasi SVG, `Content-Security-Policy: default-src 'none'`, `X-Content-Type-Options: nosniff` |
| T10 | Supply chain | Penyerang | Backdoor | `cargo-deny`, `cargo-audit`, lockfile, build reproducible, SBOM |
| T11 | Container escape via io_uring | Penyerang | Host compromise | io_uring opsional & dimatikan di lingkungan multi-tenant tak tepercaya; seccomp; patch kernel |
| T12 | Log injection / PII di log | Internal | Kebocoran URL privat | Redaksi query sensitif, sampling, retensi terbatas |
| T13 | URL sumber membocorkan origin privat | Penyadap log/CDN | Kebocoran aset/tenant | `es_` AES-256-GCM, key dari HKDF signing secret, allowlist tetap berlaku |
| T14 | Kredensial upstream bocor ke host lain/log | Penyalahguna redirect | Kredensial origin dicuri | `IOIMG_SOURCE_AUTH` exact-host, strip saat redirect lintas host, tidak pernah di-log |
| T15 | Request/response header injection | Penyerang | Splitting/poisoning | Allowlist header + validasi CR/LF/ukuran + validasi token RFC 7230 |

Asumsi: penyerang dapat mengirim request HTTP sembarang ke endpoint publik dan dapat mengontrol isi gambar (bila punya akun/free tier).

## 2. SSRF & fetch hardening

### 2.1 Validasi berlapis

```
1. Parse URL              → wajib absolut, skema ∈ {http, https}
2. Host allowlist         → exact atau wildcard satu label (*.acme.com tidak cocok acme.com.evil.io)
3. Tolak host literal     → IP, localhost, *.local, *.internal, metadata hostname
4. Resolve DNS            → ambil semua A/AAAA
5. Filter hasil resolve   → tolak loopback, private (RFC1918), link-local (169.254/16),
                             CGNAT (100.64/10), multicast, unspecified, IPv6 ULA (fc00::/7),
                             IPv4-mapped IPv6 (::ffff:10.x)
6. Konek ke IP hasil resolve (bukan resolve ulang) → mencegah DNS rebinding
7. Validasi setiap redirect (≤ 3) dengan langkah 1–6
8. Timeout: connect 1 s, total 5 s; batas ukuran 20 MB (streaming, abort melebihi)

Catatan per runtime: di profil `compio`, resolusi DNS lewat `ToSocketAddrsAsync` di-spawn ke thread pool (atau fitur `hickory` bawaan `cyper`). Untuk guard SSRF, **resolve sendiri** lewat resolver yang kita kontrol agar hasil IP yang divalidasi adalah IP yang benar-benar dipakai untuk konek.
```

### 2.2 Detail penting

- Wildcard harus cocok pada **label boundary**: `*.acme.com` cocok `a.acme.com`, tidak `evilacme.com` maupun `acme.com`.
- Tolak userinfo di URL (`http://user:pass@host/`) — sering dipakai untuk trik parser.
- Reject URL dengan port non-standar kecuali diizinkan eksplisit.
- Untuk self-host, `allow_private_ips = true` harus disertai peringatan di docs dan log.
- Jangan mengikuti redirect ke skema non-HTTP (`ftp:`, `file:`).

## 3. Decoder & transformasi

### 3.1 Pilihan decoder

| Format | Decoder | Alasan |
| --- | --- | --- |
| JPEG | `zune-jpeg` (Rust) | Cepat, tanpa C; batasi `mozjpeg` hanya untuk encode |
| PNG | `image`/`png` crate (Rust) | Memory-safe; hati-hati `zlib` bomb → batas ukuran terdekompresi |
| WebP | `libwebp` binding | Kualitas/kecepatan terbaik; mitigasi via proses isolasi & patch |
| AVIF | `ravif`/`libaom` | Encode mahal → semaphore |
| GIF | `gif` crate | Batasi jumlah frame & total pixel |
| SVG | `resvg` + sanitizer | Sanitasi wajib sebelum rasterisasi |

Kebijakan: **prefer Rust decoder**; jika memakai C/C++ (libwebp, libavif), jalankan transformasi di **worker process terpisah** (opsional `sandbox = true`) dengan seccomp minimal dan tanpa akses network — sehingga RCE tidak langsung memberi akses ke kredensial/tenant lain.

### 3.2 Batas keras (default, configurable)

| Batas | Nilai |
| --- | --- |
| Ukuran source | 20 MB |
| Dimensi input | 8192 × 8192 |
| Total pixel input | 40 MP |
| Total pixel output | 40 MP |
| Memori per transformasi | 256 MB (estimasi: w×h×4 × faktor) |
| Jumlah frame animasi | 500 |
| Waktu transformasi | 2 s (timeout → `504`/`422`) |

### 3.3 Metadata

- `strip = 1` default: EXIF/GPS/XMP/ICC (ICC dipertahankan bila `keep_profile=1`).
- `auto-orient` diterapkan **sebelum** transform agar EXIF tidak diperlukan lagi.
- Konten yang tidak dikenali → `415`, jangan "coba saja decode".

## 4. SVG

1. Parse dengan `usvg` (strict).
2. Tolak/abaikan: `<script>`, `on*` attributes, `<foreignObject>`, `xlink:href` eksternal, `data:` selain raster image, `@import`, `url(http…)`.
3. Output:
   - `image/svg+xml` + `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`
   - atau rasterisasi (default untuk `f_auto` bila client tidak mendukung).
4. `X-Content-Type-Options: nosniff` selalu.

## 5. Autentikasi & otorisasi

### 5.1 Signed URL

- HMAC-SHA256, perbandingan constant-time (`subtle`).
- `exp` disertakan dalam canonical string (tidak bisa diubah).
- `kid` untuk rotasi; key lama tetap valid 7 hari setelah rotasi (grace), lalu dihapus.
- Signature di **path**, bukan query, agar CDN cache key alami dan log proxy tetap konsisten.
- Rate limit tetap berlaku untuk signed URL (signature ≠ izin DoS).

### 5.2 API key

- Format `ioimg_<env>_<id>_<secret>`; disimpan `argon2id(id = hash(secret))`.
- Scope: `read`, `purge`, `admin`; default least privilege.
- Rotasi tanpa downtime (dua key aktif).
- Audit: siapa membuat/mencabut key, kapan.

### 5.3 Tenant isolation

- `project_id` masuk cache key & path L2 (opsional subdir untuk attachment).
- Admin API memfilter by organization; tidak ada endpoint lintas-tenant.
- Quota & rate limit per project, dienforce di data plane dengan state lokal (eventually consistent lintas node).

### 5.4 Kerahasiaan sumber, kredensial upstream, dan header

Semua fitur di bawah **default off**; tanpa konfigurasi, perilaku dan output
tidak berubah.

**Source terenkripsi (`es_`).** URL sumber dapat diganti token `es_` berisi
ciphertext AES-256-GCM (base64url) sehingga URL tidak membocorkan origin. Kunci
32 byte diturunkan dengan HKDF-SHA256 dari setiap secret `IOIMG_SIGNING_KEYS`
dengan info `ioimg-source-enc-v1`; AES-GCM memberi autentikasi sehingga
ciphertext yang ditamper ditolak. Rotasi didukung: server mencoba semua key yang
dikonfigurasi dan menolak key id tak dikenal dengan `400
invalid_encrypted_source`. Key id pemenang hanya muncul di header
`X-IOIMG-Source-Key` dan log debug saat `IOIMG_LOG_LEVEL=debug`. Batas: token
tidak menyembunyikan URL dari operator/CDN yang melihat plaintext setelah
dekripsi, dan URL hasil dekripsi **selalu** melewati kanonisasi, allowlist, dan
guard SSRF yang sama — enkripsi bukan bypass. Plaintext token tetap harus
ditandatangani bila `IOIMG_SIGNED_ONLY=true`; signature menandatangani URL
hasil dekripsi.

**`IOIMG_SOURCE_AUTH`.** Daftar `host=Header:Value` (comma separated) untuk
authorization upstream. Pencocokan **exact host** saja: tidak ada wildcard,
suffix, atau port matching. Value boleh mengandung spasi dan titik dua setelah
titik dua pertama. Header hanya dikirim ke host tersebut, tidak pernah ke host
lain saat redirect, tidak di-log, dan tidak pernah muncul di body error. Batas:
satu header per host; value yang mengandung koma tidak dapat ditulis (pemisah
entry), dan secret tetap terlihat oleh proses/secret manager operator.

**`IOIMG_PASSTHROUGH_HEADERS`.** Daftar nama header request yang diteruskan ke
upstream. Nilai hanya diteruskan bila tidak mengandung CR/LF dan panjangnya
≤ 1 KiB; nilai yang gagal validasi di-drop, bukan menggagalkan request.
`Authorization` dan `Cookie` hanya diteruskan bila **secara eksplisit** ada di
daftar. Semua header yang diteruskan dihapus saat redirect ke host berbeda.
Batas: header hop-by-hop/framing (`Host`, `Content-Length`, `Transfer-Encoding`,
`Connection`, `Upgrade`, `TE`, `Trailer`, `Proxy-Connection`) ditolak saat
startup; pass-through tidak menggantikan `IOIMG_SOURCE_AUTH`.

**`IOIMG_RESPONSE_HEADERS`.** Daftar `Name:Value` yang ditambahkan ke respons
gambar. Nama divalidasi sebagai token RFC 7230; value dengan CR/LF (atau NUL)
ditolak saat startup, sehingga header splitting/injection tidak mungkin.
Header bawaan (`Content-Type`, `Content-Length`, `ETag`, dan seterusnya) menang
bila namanya sama. Batas: hanya respons gambar sukses; respons error tidak
mendapat header kustom.

**Cookie pass-through tidak didukung** dan sengaja tidak ditambahkan. Cookie
sering berisi sesi pengguna; meneruskannya ke origin pihak ketiga memperluas
permukaan kebocoran tanpa kebutuhan yang jelas. Gunakan `IOIMG_SOURCE_AUTH`
untuk kredensial upstream yang memang diperlukan.

## 6. Abuse control

| Kontrol | Nilai default |
| --- | --- |
| Rate limit anonim (free tier) | 60 RPS/project, burst 120 |
| Rate limit per IP | 300 RPS |
| Concurrent transform per project | 4 (Free), 32 (Pro), 128 (Scale) |
| AVIF encode global | `min(4, n_cpus/2)` |
| Max distinct cache keys/project/hari | 500k (mencegah cache flooding) |
| Blocklist hash gambar | Enterprise (mis. CSAM/doxing), via integrasi penyedia |

Catatan etika/legal: sediakan jalur pelaporan abuse dan proses takedown; jangan jadikan ioimg proxy anonim untuk konten ilegal (perlu ToS + tombol report).

## 7. Hardening deployment

### 7.1 Container

```yaml
securityContext:
  runAsNonRoot: true
  runAsUser: 65532
  readOnlyRootFilesystem: true
  allowPrivilegeEscalation: false
  capabilities: { drop: ["ALL"] }
  seccompProfile:
    type: Localhost
    localhostProfile: profiles/ioimg-uring.json   # hanya bila io_uring diaktifkan
```

- Root filesystem read-only; writable hanya `/data/cache` (emptyDir/PVC).
- Tidak ada shell di image (distroless).
- Network policy: egress hanya ke port 80/443 (dan host storage bila ada).

### 7.2 seccomp

Profil default Docker/K8s memblokir `io_uring_enter`. Untuk mode io_uring, sediakan profil kustom yang mengizinkan `io_uring_setup`, `io_uring_enter`, `io_uring_register` dan tetap memblokir syscall berbahaya (`bpf`, `userfaultfd` bila tak perlu, `kexec_load`, `perf_event_open` bila tak perlu).

**Rekomendasi:** di lingkungan multi-tenant yang tidak sepenuhnya dipercaya, jalankan `backend = "tokio"`. io_uring punya sejarah CVE yang memungkinkan escape; trade-off keamanan vs ~25% throughput harus diputuskan operator (didokumentasikan).

### 7.3 Jaringan

- TLS di ioimg atau di terminator (nginx/Envoy/ALB). `splice` hanya efektif bila TLS diterminasi di depan.
- HTTP/2 aktif; HTTP/3 (QUIC) di depan (CDN/Envoy) untuk mobile.
- Header keamanan default:
  - `X-Content-Type-Options: nosniff`
  - `Content-Security-Policy: default-src 'none'` (khusus SVG)
  - `Cross-Origin-Resource-Policy: cross-origin` (agar bisa dipakai `<img>` lintas situs)
  - `Referrer-Policy: no-referrer` untuk respons gambar? (tidak berlaku; abaikan)

## 8. Supply chain & SDLC

- `cargo-deny` (license, advisory, banned crates), `cargo-audit` di CI.
- `cargo-fuzz` untuk `ioimg-ops` (parser URL/ops) dan decoder wrapper.
- Dependency pinning via `Cargo.lock`; update otomatis dengan PR (Dependabot/Renovate), dengan gate benchmark untuk crate panas.
- SBOM (CycloneDX) per rilis; image ditandatangani (cosign) + provenance.
- Secrets: tidak pernah di config file yang di-commit; env/secret manager; di memori `secrecy`/`zeroize`.
- Patch SLA: kritis 24 jam (decoder CVE), tinggi 7 hari, sedang 30 hari.

## 9. Logging & privasi

- Log URL **di-redact**: query string sensitif (`token`, `sig`, `X-Amz-Signature`) diganti `***`.
- Jangan log isi gambar, EXIF, atau header origin `Authorization`.
- `request_id` untuk korelasi; retensi log 30 hari (90 hari enterprise).
- Data residency: pilih region storage cache; tidak ada replikasi lintas region tanpa opt-in.

## 10. Checklist review keamanan (per rilis)

- [ ] Tidak ada endpoint yang fetch URL tanpa allowlist check
- [ ] Semua redirect divalidasi ulang
- [ ] Batas ukuran/dimensi berlaku sebelum alokasi besar
- [ ] Fuzz target hijau 24 jam
- [ ] `cargo-audit` bersih (atau diterima dengan justifikasi)
- [ ] Image container tanpa shell, non-root, read-only FS
- [ ] Signature diverifikasi constant-time, `exp` di canonical
- [ ] Tidak ada secret di log/metric
- [ ] Test untuk T1–T9 ada di `tests/security/`
