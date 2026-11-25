# Topologi Cloud: tier Free vs Pro

Tujuan: pelanggan **Pro tidak boleh merasakan noise tier Free**. Itu keputusan
yang mengubah topologi, bukan sekadar konfigurasi limit — dua tier harus berbagi
sesedikit mungkin sumber daya yang bisa jenuh (CPU transform, disk cache, antrean
request, kuota CDN, dan Valkey/PG control plane).

Angka di bawah berasal dari pengukuran repo ini (`docs/benchmarks/io-uring.md`,
`docs/benchmarks/shrink-on-load.md`).

Ringkasan kapasitas terukur per **satu** proses celeris:

| Beban | per proses | Catatan |
| --- | --- | --- |
| HIT 17 KiB (8 keep-alive) | **79,2k rps**, p50 80 µs | ~6 core CPU |
| MISS JPEG `w_400` (vips, shrink-on-load) | **76 img/s**, 10,3 ms/img | 1 core ≈ 97 img/s |
| MISS + watermark | 49 img/s, 17,4 ms/img | |
| MISS AVIF `w_400` | **7 img/s**, 142 ms/img | 14× lebih mahal dari JPEG |

Konsekuensi penting: **tier Free tidak dibatasi oleh HIT, tapi oleh MISS.**
Cache hit 80k rps hampir gratis; 100 rps MISS AVIF menghabiskan 14 core. Jadi
isolasi harus terjadi di jalur MISS, bukan di jalur HIT.

---

## 1. Diagram

```
                        ┌──────────────────────────────┐
   pelanggan ──────────▶│  Edge / anycast (Cloudflare, │
                        │  Bunny, Fastly) — 2 zona     │
                        └──────┬───────────────┬───────┘
                    free zone  │               │  pro zone
                 (rate limit   │               │  (WAF, cache rules,
                  ketat, cache │               │   purge terpisah,
                  rule murah)  │               │   stale-while-revalidate)
                               ▼               ▼
                    ┌──────────────┐   ┌──────────────────┐
                    │ ingress-free │   │ ingress-pro      │
                    │ (shared)     │   │ (dedicated, TLS) │
                    └──────┬───────┘   └────────┬─────────┘
                           ▼                    ▼
              ┌────────────────────┐  ┌─────────────────────────┐
              │ nodepool FREE       │  │ nodepool PRO            │
              │ spot/preemptible    │  │ on-demand, taint=pro    │
              │ 1 vCPU per pod      │  │ 8 vCPU per pod          │
              │ burstable, limits<  │  │ Guaranteed QoS          │
              │ requests, 3–20 repl │  │ requests==limits, 2–N   │
              │ HPA: rps + queue    │  │ HPA: rps + queue + p95  │
              └──────┬─────────────┘  └────────┬────────────────┘
                     │                         │
        L1: emptyDir 1–2 GiB        L1: local NVMe 32–64 GiB
        L2: s3://ioimg-cache-free   L2: s3://ioimg-cache-pro
            (shared, LRU, TTL 7d)       (per-tenant prefix, TTL 30d,
                                         opsional bucket per tenant)
                     │                         │
                     └──────────┬──────────────┘
                                ▼
                  ┌───────────────────────────────┐
                  │ control plane (kinstack)      │
                  │  · snapshot config per tier   │
                  │  · signing keys, presets      │
                  │  · metering durabel → billing │
                  │  · purge orchestration        │
                  └───────────────────────────────┘
```

Zona CDN dipisah **per tier**. Ini bukan kosmetik: purge storm dari user Free
(purge bisa dipicu siapa saja yang punya key) akan mengosongkan cache Pro kalau
zonanya sama, dan cache Pro adalah alasan mereka membayar.

---

## 2. Perbedaan tier

| Aspek | Free | Pro |
| --- | --- | --- |
| Nodepool | spot/preemptible, campur dengan beban lain | on-demand, taint `tier=pro:NoSchedule`, tidak ada beban lain |
| QoS pod | Burstable, `limits` = 1 vCPU | **Guaranteed**, `requests == limits` |
| Proses per pod | 1 proses celeris | 1 proses celeris (prefork tetap tidak dipakai) |
| HPA | 3→20 replika, target 60% CPU | 2→N, target 50% CPU + p95 latency |
| PDB | `maxUnavailable: 50%` | `minAvailable: 90%`, `terminationGracePeriod 30s` |
| Concurrency admission | semaphore per proyek 2 | semaphore per proyek 32 |
| MISS budget | 60 img/s per proyek | 1.000 img/s per proyek (kontraktual) |
| AVIF | **ditolak** (`422 unsupported_format`) | diizinkan |
| Watermark | tidak diizinkan | diizinkan |
| Max dimension | 4096 px / 20 MP | 8192 px / 40 MP |
| Rate limit edge | 10 rps per IP, 50 rps per proyek | 500 rps per proyek |
| L1 cache | 1 GiB, evict agresif | 32 GiB NVMe |
| L2 TTL | 7 hari | 30 hari |
| Purge | antrean rendah, batched | prioritas, per-URL |
| SLO | best effort, tidak ada kredit | 99,9% availability, p95 < 250 ms MISS |
| Observability | metrik agregat | metrik per tenant + log 30 hari |
| Harga | gratis | per GB optimized + per request |

Semua pembatasan di atas **sudah bisa** diekspresikan lewat `tenant.Policy`
(`Limits`, `SignedOnly`, `Allowlist`, `Presets`) — hanya `tier` yang perlu
ditambahkan sebagai field eksplisit supaya data plane tidak menebak dari nama
proyek.

---

## 3. Kenapa bukan satu pool dengan limit saja

Satu pool + `Limits` per proyek gagal pada tiga hal:

1. **Head-of-line blocking di CPU**: 1 vCPU pod dipakai satu MISS AVIF (142 ms)
   memblok satu slot dari 8 slot. Tenant lain di pod itu ikut menunggu. Dengan
   pod terpisah, dampaknya terkurung di nodepool Free.
2. **Disk cache saling mengusir**: satu volume 50 GiB dipakai Free (hit ratio
   rendah, banyak varian) akan mengusir entri Pro. Memisahkan bucket L2 dan
   volume L1 menghilangkan ini sepenuhnya.
3. **Deploy & gangguan**: rollout versi baru di Free tidak boleh ikut
   me-restart Pro. Dua Deployment = dua jadwal rilis (Free bisa canary lebih
   agresif karena tanpa SLO).

Yang **tidak** perlu dipisah: control plane (satu, dengan kolom `tier`),
Postgres, Valkey (kecuali rate-limit counter Free, lihat §5), dan bucket S3
untuk metering.

---

## 4. Sketsa manifest

```yaml
# Nodepool: dua pool, bukan nodeSelector di satu pool.
apiVersion: karpenter.sh/v1
kind: NodePool
metadata: { name: dp-pro }
spec:
  template:
    spec:
      requirements:
        - { key: karpenter.sh/capacity-type, operator: In, values: [on-demand] }
        - { key: kubernetes.io/arch, operator: In, values: [amd64] }
      taints: [{ key: tier, value: pro, effect: NoSchedule }]
  limits: { cpu: "256", memory: 512Gi }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: ioimg-pro }
spec:
  replicas: 3
  strategy: { rollingUpdate: { maxUnavailable: 0, maxSurge: 1 } }
  template:
    spec:
      nodeSelector: { tier: pro }
      tolerations: [{ key: tier, operator: Equal, value: pro, effect: NoSchedule }]
      containers:
        - name: ioimg
          image: ghcr.io/ioimg/ioimg:<sha>
          resources:
            requests: { cpu: "8", memory: 4Gi }   # Guaranteed: sama dengan limits
            limits:   { cpu: "8", memory: 4Gi }
          env:
            - { name: IOIMG_TIER, value: pro }
            - { name: IOIMG_ADDR, value: ":8080" }
            - { name: IOIMG_CACHE_DIR, value: /var/cache/ioimg }
            - { name: IOIMG_CACHE_MAX_MB, value: "32768" }
            - { name: IOIMG_L2_ENABLED, value: "true" }
            - { name: IOIMG_L2_BUCKET, value: ioimg-cache-pro }
            - { name: IOIMG_CONCURRENCY, value: "8" }      # vips, bukan NumCPU
            - { name: IOIMG_CONTROL_URL, value: https://control.ioimg.net }
            - { name: IOIMG_CDN_PROVIDERS, value: cloudflare }
          volumeMounts: [{ name: cache, mountPath: /var/cache/ioimg }]
          readinessProbe: { httpGet: { path: /readyz, port: 8080 }, periodSeconds: 5 }
          lifecycle: { preStop: { exec: { command: ["sleep","10"] } } }
      volumes:
        - name: cache
          ephemeral: { volumeClaimTemplate: { spec: { storageClassName: local-nvme, resources: { requests: { storage: 64Gi } } } } }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: ioimg-free }
spec:
  replicas: 6
  template:
    spec:
      nodeSelector: { tier: free }
      containers:
        - name: ioimg
          resources:
            requests: { cpu: "500m", memory: 512Mi }
            limits:   { cpu: "1", memory: 1Gi }     # Burstable, sengaja
          env:
            - { name: IOIMG_TIER, value: free }
            - { name: IOIMG_CACHE_MAX_MB, value: "1024" }
            - { name: IOIMG_L2_BUCKET, value: ioimg-cache-free }
            - { name: IOIMG_CONCURRENCY, value: "2" }
            - { name: IOIMG_DISABLE_AVIF, value: "true" }
```

`IOIMG_TIER` harus ikut ke cache key (`cache.KeyVersion|tier|...`), supaya
entri hasil kebijakan Free tidak pernah tersaji ke Pro — walaupun nanti ada
skenario L2 bersama.

---

## 5. Batas isolasi yang masih harus dibangun

Topologi di atas hanya bekerja kalau empat hal ini ada di kode. Saat ini belum:

1. **Admission control per proyek** (bukan per IP). Sekarang `limiter` per IP;
   satu tenant Free dengan 10.000 IP tetap bisa memenuhi pod. Perlu token
   bucket per `project.ID` di dalam proses — murah, in-memory, 16 shard.
2. **`tier` pada snapshot + policy** → dipakai untuk menolak AVIF/watermark dan
   memilih anggaran konkurensi.
3. **Rate limit Free di Valkey terpisah** atau di edge saja. Kalau counter Free
   menumpang Valkey yang sama dengan session Pro, lonjakan Free = latensi Pro.
   Rekomendasi: limit Free ditegakkan di CDN/edge, bukan di origin.
4. **Metering durabel bertanda tier** — usage report harus membawa `tier` dan
   `project` supaya billing dan kapasitas per tier bisa dihitung ulang.

Tanpa nomor 1, pemisahan nodepool hanya memindahkan masalah: satu tenant Free
tetap bisa menghabiskan satu pod Free, dan HPA akan menambah pod — mahal, tapi
Pro tetap aman. Jadi nomor 1 bukan prasyarat absolute untuk isolasi Pro, tapi
prasyarat untuk biaya Free yang terkendali.

---

## 6. Rencana bertahap

| Fase | Isi | Alasan |
| --- | --- | --- |
| 0 | Satu pool, tier hanya sebagai label metrik | buktikan demand dulu |
| 1 | Dua Deployment (`free`/`pro`) di satu cluster, `tier` di snapshot, AVIF/watermark ditolak di Free | isolasi CPU & policy, biaya rendah |
| 2 | Nodepool terpisah (spot vs on-demand), bucket L2 terpisah, zona CDN terpisah | isolasi cache & purge |
| 3 | Bucket L2 / volume NVMe per tenant Pro, prioritas purge, metrik per tenant | diferensiasi yang bisa dijual |
| 4 | Multi-region, failover, SLO kontraktual | setelah ada pelanggan yang menuntut |

Fase 1 bisa dikerjakan tanpa mengubah data plane selain menambah `tier` — dan
itu juga fondasi untuk billing per tier.
