# Operations — ioimg


> **Catatan implementasi (2026-11-25):** stack final adalah **Go** (`service/`),
bukan Rust/axum/io_uring. Lihat [ADR-0008](adr/0008-go-data-plane.md) dan
[service/README.md](../service/README.md). Dokumen ini tetap menjadi sumber
requirements produk; bagian stack/io_uring adalah riwayat desain.

Panduan deploy, observability, SLO, kapasitas, dan runbook.

---

## 1. Topologi deploy

### 1.1 Self-host single node (mulai di sini)

```
Internet → [nginx/Envoy TLS] → ioimg (1 proses, N worker) → /data/cache (NVMe)
                                    ↓
                                origin (S3/CMS)
```

Cocok untuk < 5k RPS. Gunakan `SO_REUSEPORT` bila multi-proses.

### 1.2 Self-host multi-node + object storage

```
Internet → CDN → LB → [ioimg × N] → shared cache? (opsional)
                            ↓
                       origin
```

Cache antar node **tidak** di-share secara default (L2 lokal). Request acak bisa miss di node berbeda. Mitigasi:
- CDN di depan dengan cache key stabil → mayoritas request tidak sampai ioimg.
- Consistent hashing di LB berdasarkan cache key (bila perlu hit ratio tinggi di L2).
- L3 object storage (opsional, fase 3).

### 1.3 Managed multi-region

```
Region A: CDN(A) → ioimg(A) → origin
Region B: CDN(B) → ioimg(B) → origin
Control plane (api.ioimg.net) — global, config replicated, Postgres multi-AZ
```

## 2. Requirement sistem

| Item | Minimum | Disarankan |
| --- | --- | --- |
| Kernel | 5.15 (io_uring dasar) | 6.6+ |
| io_uring | `io_uring_setup/enter/register` diizinkan | + registered buffers/files |
| CPU | 2 vCPU | 8 vCPU, dedicated (hindari noisy neighbor) |
| RAM | 2 GB | 32 GB (L1 cache + buffer pool) |
| Disk | 20 GB | NVMe lokal ≥ 500 GB, ≥ 500k IOPS 4k |
| NIC | 1 Gbps | 10–25 Gbps |
| OS | Linux (glibc 2.31+) | Ubuntu 24.04 / Debian 12, tuned profile `network-latency` |

### 2.1 Sysctl / tuning

```bash
net.core.somaxconn=65535
net.ipv4.tcp_max_syn_backlog=65535
net.ipv4.ip_local_port_range="10240 65000"
net.ipv4.tcp_tw_reuse=1
net.core.rmem_max=16777216
net.core.wmem_max=16777216
vm.swappiness=1
fs.file-max=2000000
```

`ulimit -n` ≥ 200k. `memlock` unlimited bila memakai registered buffers besar.

### 2.2 Cek dukungan io_uring

```bash
# kernel
uname -r

# apakah syscall diblokir (SIGSYS/EPERM)?
./ioimg --probe-uring
# → {"uring":true,"sq_entries":4096,"features":["SINGLE_MMAP","SUBMIT_STABLE"]}

# seccomp container
grep Seccomp /proc/self/status     # 2 = filter aktif
docker run --rm --security-opt seccomp=unconfined ghcr.io/ioimg/ioimg probe-uring
```

Bila `probe-uring` gagal, ioimg **tetap berjalan** dengan backend `tokio` dan mengeluarkan:
```
WARN io_backend fallback backend="tokio" reason="EPERM (seccomp?)" hint="see docs/OPERATIONS.md#seccomp"
```

Pada build `runtime-compio`, fallback terjadi **di dalam compio**: dengan feature `io-uring` + `polling`, fusion driver memilih saat runtime (`io.driver = "auto"`), dan `ProactorBuilder` bisa memaksa `uring`/`polling`. Artinya container dengan seccomp ketat tidak crash, tetapi berjalan di driver polling (readiness, API compio) — **ukur dulu**, karena itu bukan `tokio`.

**Wajib dilaporkan: driver efektif, bukan "io_uring tersedia".** Ambil dari `compio::driver::Proactor::driver_type` saat startup dan ekspos sebagai metric:

```
ioimg_io_backend_info{backend="compio",driver="io_uring|polling"} 1
```

Alert bila `driver="polling"` padahal konfigurasi mengharapkan `io_uring`. Di driver `polling`, dokumentasi compio menyatakan **file I/O tidak asynchronous**: operasi file masuk ke thread pool bersama yang punya batas jumlah thread, dan operasi bisa **gagal segera** bila batas terlampaui → pada keadaan itu L2 cache bukan hanya lambat, tetapi bisa error.

### 2.3 Jumlah worker pada profil `runtime-compio`

`#[compio::main]` + `cyper_axum::serve` menjalankan **satu runtime single-thread**: hanya **1 core** yang dipakai. Ini terverifikasi (proses hanya punya 1 thread; `nproc` 16). Konfigurasi wajib salah satu dari:

| Pola | Cara | Catatan |
| --- | --- | --- |
| `N` proses + `SO_REUSEPORT` | `ioimg --workers 0` meluncurkan `N=n_cpus` proses, masing-masing bind `SO_REUSEPORT` | Direkomendasikan compio sendiri ("might be more efficient"); isolasi kegagalan per proses |
| `Dispatcher` | Satu proses, `Dispatcher::builder().worker_threads(n)`, satu runtime per thread | Thread pool bersama dipakai file I/O (polling), proses, DNS → jangan taruh transformasi image di sana |

Baseline terukur (64 KiB, loopback, io_uring, 1 core): **36,7k RPS** keep-alive / 14,0k tanpa keep-alive. Skala linear secara kasar terhadap jumlah worker sampai NIC/CPU jenuh — verifikasi di mesin target, jangan diasumsikan.

## 3. Deployment

### 3.1 Docker

```bash
docker run -d --name ioimg \
  -p 8080:8080 \
  -v $PWD/ioimg.toml:/etc/ioimg/ioimg.toml:ro \
  -v ioimg-cache:/data/cache \
  --ulimit nofile=200000:200000 \
  --security-opt seccomp=./deploy/docker/seccomp-uring.json \
  ghcr.io/ioimg/ioimg:1.0.0
```

### 3.2 Helm

```yaml
# values.yaml
image: { tag: "1.0.0" }
io: { backend: auto, directIo: true }
cache: { size: 200Gi, storageClass: local-nvme }
resources:
  requests: { cpu: "4", memory: "8Gi" }
  limits:   { cpu: "8", memory: "16Gi" }
seccomp: { profile: uring }        # atau "runtimeDefault" (io_uring dimatikan)
ingress: { enabled: true, host: img.example.com }
```

### 3.3 Upgrade

1. Deploy canary 1 pod, pantau `p95`, `error_rate`, `hit_ratio` 15 menit.
2. Naikkan bertahap (25% → 50% → 100%).
3. Bila `transform_version` berubah → cache dingin; lakukan di jam sepi atau aktifkan `prewarm`.
4. Rollback: image tag sebelumnya; cache format kompatibel (key berisi versi → tidak perlu purge).

## 4. Observability

### 4.1 Metrics (Prometheus)

| Metric | Tipe | Label | Guna |
| --- | --- | --- | --- |
| `ioimg_http_requests_total` | counter | `status`, `cache` (hit/miss/bypass) | RPS, error rate |
| `ioimg_http_request_duration_seconds` | histogram | `cache`, `format` | p50/p95/p99 |
| `ioimg_phase_duration_seconds` | histogram | `phase` (fetch/decode/resize/encode/write) | Cari bottleneck |
| `ioimg_cache_hit_ratio` | gauge | — | Efektivitas cache |
| `ioimg_cache_bytes` | gauge | `layer` (l1/l2) | Kapasitas |
| `ioimg_cache_evictions_total` | counter | `layer` | Churn |
| `ioimg_bytes_in_total` / `ioimg_bytes_out_total` | counter | `project`? (hindari high cardinality) | Bandwidth |
| `ioimg_io_backend_info` | gauge | `backend` | Apakah io_uring aktif |
| `ioimg_uring_sq_depth` / `ioimg_uring_cq_depth` | gauge | — | Saturasi ring |
| `ioimg_uring_ops_total` | counter | `op` (read/write/statx/splice) | Profil I/O |
| `ioimg_worker_queue_depth` | gauge | `pool` | Backpressure |
| `ioimg_transform_errors_total` | counter | `code` | Health codec |
| `ioimg_origin_requests_total` | counter | `status_class` | Health origin |
| `ioimg_quota_used` | gauge | `metric` (transforms/egress) | Billing |
| `ioimg_singleflight_waiters` | gauge | — | Coalescing efektif? |

**Cardinality rule:** jangan pernah memakai URL/host/project sebagai label metric (kecuali `project` di endpoint admin terpisah). Gunakan log/trace untuk dimensi itu.

### 4.2 Traces

- OpenTelemetry, span per fase: `http.request` → `cache.lookup` → `origin.fetch` → `decode` → `transform` → `encode` → `cache.write` → `response.stream`.
- Sampling: head 1%, tail 100% untuk error/latensi > 1 s.
- Atribut: `cache.key_prefix` (bukan key penuh), `format`, `bytes`, `backend`.

### 4.3 Logs

JSON ke stdout. Field wajib: `ts`, `level`, `msg`, `request_id`, `project`, `status`, `cache`, `duration_ms`, `backend`.

Contoh:
```json
{"ts":"2025-11-20T10:00:00.123Z","level":"INFO","msg":"transform","request_id":"01HQ8Y","project":"acme","status":200,"cache":"MISS","duration_ms":87,"ops":"w_800,f_auto,q_auto","backend":"uring"}
```

## 5. SLO & alert

| SLI | SLO | Alert |
| --- | --- | --- |
| Availability (non-5xx / total) | 99.95%/30d | 5xx > 1% selama 5 min |
| p95 cache-hit latency | < 15 ms | > 25 ms 5 min |
| p95 cold transform | < 250 ms (WebP) | > 500 ms 10 min |
| Cache hit ratio | > 90% | < 80% 15 min |
| Origin error rate | < 2% | > 5% 5 min |
| Worker saturation | < 80% | > 95% 2 min |
| Disk cache usage | < 85% | > 92% |

Error budget policy: bila budget < 25% tersisa, hentikan perubahan non-perbaikan sampai pulih.

## 6. Kapasitas

### 6.1 Estimasi kasar per node (8 vCPU, 10 Gbps)

| Profil | Hit | Miss | RPS | Egress |
| --- | --- | --- | --- | --- |
| Read-heavy kecil (64 KB) | 95% | 5% | ~20k | ~1.3 Gbps |
| Mixed 200 KB | 90% | 10% | ~7k | ~1.2 Gbps |
| Transform berat (AVIF) | 50% | 50% | ~600 | ~0.3 Gbps |

Bottleneck berpindah: jaringan → syscall → CPU codec → disk IOPS, tergantung campuran. Ukur dengan W6 (mixed).

### 6.2 Rumus

```
RPS_maks  ≈ min( NIC_bandwidth / avg_body_bytes, cores * rps_per_core )
rps_per_core ≈ 1 / (cpu_seconds_per_request)
disk_IOPS ≈ RPS * (1 - hit_l1) * iops_per_entry   # ~3 (statx+read+write)
cache_size ≈ working_set_entries * avg_entry_bytes * safety(1.3)
```

Contoh: 20k RPS, 5% miss L1, 3 IOPS/entry → 3k IOPS (NVMe mudah). Cache 1M entry × 120 KB × 1.3 ≈ 156 GB.

## 7. Runbook

### R1 — `5xx` tinggi mendadak
1. Cek `ioimg_transform_errors_total{code}` → decoder atau bug?
2. Cek `ioimg_origin_requests_total` → origin down? aktifkan serve-stale, hubungi pelanggan.
3. Cek disk penuh (`df`) → cache write gagal.
4. Rollback canary bila terkait deploy.

### R2 — p95 melonjak, RPS stabil
1. Cek `ioimg_phase_duration_seconds` → fase mana?
2. Encode AVIF naik? → turunkan `avif_concurrency` sementara (config reload).
3. `ioimg_uring_sq_depth` tinggi → ring kurang; naikkan `uring_entries` atau tambah node.
4. NUMA/CPU throttle (`perf`, `cpufreq`) → cek noisy neighbor.

### R3 — Cache hit ratio turun
1. Apakah `transform_version` berubah (deploy)?
2. Apakah CDN cache key berubah (config header/Vary)?
3. Apakah ada varian baru dari integrasi (preset berubah)?
4. Eviction tinggi → kapasitas L2 kurang.

### R4 — io_uring gagal saat runtime
1. `grep ioimg_io_backend_info` di `/metrics`.
2. Lihat log `uring error` → errno.
3. Server otomatis fallback; bila tidak, restart untuk re-probe.
4. Bila berulang → set `io.backend = "tokio"` dan laporkan bug (kernel/IOMMU/seccomp).

### R5 — OOM / container killed
1. Batasi `max_input_mp`, turunkan `workers`.
2. Periksa gambar raksasa dari satu project → tambahkan limit per tenant.
3. Pertimbangkan isolasi worker codec (proses terpisah) untuk membatasi RSS.

### R6 — Origin lambat memicu timeout massal
1. Naikkan `serve_stale_secs`, aktifkan `stale-if-error`.
2. Turunkan `fetch.timeout_ms`? Tidak — ini memperbanyak error; lebih baik cache negatif + backoff.
3. Circuit breaker per origin host akan mengurangi beban; pastikan aktif.

### R7 — Purge tidak efektif
1. Pastikan purge menyentuh CDN eksternal (ioimg tidak bisa memaksa CDN).
2. Gunakan `config_epoch` bump untuk invalidasi massal.
3. Verifikasi index `source → keys` konsisten (job repair).

## 8. Backup & DR

- **Config/DB (control plane)**: Postgres PITR, RPO 5 menit.
- **Cache L2**: tidak di-backup (derived data). Boleh hilang.
- **DR**: RTO 15 menit (deploy ulang dari image + config), RPO 5 menit (DB). Cache akan dingin → lonjakan origin; aktifkan rate limit sementara.
- **Secrets**: di secret manager, punya prosedur restore & rotasi terdokumentasi.

## 9. Checklist go-live

- [ ] Config divalidasi (`ioimg check-config`)
- [ ] `probe-uring` sesuai harapan, backend tercatat
- [ ] Dashboard + alert aktif (R1–R6)
- [ ] Load test W6 lulus target p95
- [ ] Cache dir writable, disk ≥ 2× target
- [ ] Signed URL diuji (valid, expired, tampered)
- [ ] Allowlist diuji (host diizinkan & ditolak)
- [ ] Backup config DB & prosedur restore diuji
- [ ] Runbook dicetak/di-pin
