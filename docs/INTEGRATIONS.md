# Integrations — ioimg

Prinsip: **tidak ada yang wajib diinstall.** URL ioimg bisa dipakai di mana saja. SDK/plugin hanya mempercepat dan menangani signing + `srcset`.

> Dokumen ini membahas integrasi **aplikasi/framework**. Untuk integrasi **infrastruktur** (object storage, CDN, edge compute, Kubernetes, observability, secrets, event bus) lihat [`INFRA_INTEGRATIONS.md`](INFRA_INTEGRATIONS.md).

---

## 1. Tingkat integrasi

| Tingkat | Cara | Cocok untuk |
| --- | --- | --- |
| **L0 — URL** | Ganti `src` menjadi URL ioimg | HTML statis, email, CMS apa pun |
| **L1 — Helper lib** | Fungsi `ioimg(src, {w,h,f})` + signing | Kode aplikasi sendiri |
| **L2 — Plugin framework** | Loader/provider resmi | Next.js, Nuxt, SvelteKit, Astro |
| **L3 — Proxy/rewrite** | Middleware / plugin yang mengubah HTML di runtime | WordPress, Laravel, Shopify, Ghost |
| **L4 — Origin integration** | ioimg di depan bucket (S3/R2/GCS/Azure) atau di belakang CDN/reverse proxy | Infra → [INFRA_INTEGRATIONS.md](INFRA_INTEGRATIONS.md) |

## 2. Matriks platform

| Platform | Bentuk | Paket | Prioritas |
| --- | --- | --- | --- |
| Next.js | `imageLoader` + `IoImage` | `@ioimg/next` | P0 |
| Nuxt | module + provider `ioimg` | `@ioimg/nuxt` | P0 |
| SvelteKit | Vite plugin + loader | `@ioimg/sveltekit` | P1 |
| Astro | image service | `@ioimg/astro` | P1 |
| WordPress | plugin PHP (filter `wp_get_attachment_image_src`) | `ioimg-wordpress` | P0 |
| Laravel | service provider + `@ioimg()` Blade | `ioimg/laravel` | P1 |
| Django | template tag + storage backend | `ioimg-django` | P2 |
| Rails | helper + ActiveStorage variant | `ioimg` gem | P2 |
| Go | `net/http` handler helper | `github.com/ioimg/go` | P2 |
| Rust | `ioimg-url` builder | `ioimg-url` | P0 |
| Shopify | app proxy + theme snippet | app | P1 |
| Ghost | Code injection + helper | snippet | P1 |
| Webflow/Framer | URL manual / custom code | — | P2 |
| PHP generic | `ioimg_url()` single-file | composer | P1 |

## 3. URL builder (semua bahasa)

Aturan: bangun ops dari objek, urutkan kanonik, lalu sign bila perlu.

### 3.1 JavaScript / TypeScript

```ts
import { buildUrl, signUrl } from "@ioimg/core";

const url = buildUrl({
  base: "https://acme.ioimg.dev",
  source: "https://cdn.acme.com/hero.jpg",
  ops: { w: 800, h: 600, c: "fill", f: "auto", q: "auto" },
  // → https://acme.ioimg.dev/w_800,h_600,c_fill,f_auto,q_auto/https://cdn.acme.com/hero.jpg
});

// signed (server-side only!)
const signed = await signUrl(url, { secret: process.env.IOIMG_SALT, expiresIn: 3600 });
```

```ts
// srcset helper
buildSrcset({ base, source, widths: [320, 640, 960, 1280, 1920], ops: { f: "auto", q: "auto" } });
// → "…w_320… 320w, …w_640… 640w, …"
```

### 3.2 PHP

```php
use Ioimg\Url;

$url = Url::make('https://acme.ioimg.dev', 'https://cdn.acme.com/hero.jpg')
    ->width(800)->height(600)->fill()->auto()->quality('auto')
    ->signed(getenv('IOIMG_SALT'), 3600)
    ->toString();
```

### 3.3 Python

```python
from ioimg import Url
url = (Url("https://acme.ioimg.dev", "https://cdn.acme.com/hero.jpg")
       .w(800).h(600).crop("fill").f("auto").signed(secret, ttl=3600))
```

## 4. Next.js

`next.config.js`:

```js
module.exports = {
  images: {
    loader: "custom",
    loaderFile: "./ioimg.loader.ts",
    deviceSizes: [320, 420, 640, 768, 1024, 1280, 1600, 1920, 2560],
    imageSizes: [16, 32, 48, 64, 96, 128, 256, 384],
  },
};
```

`ioimg.loader.ts`:

```ts
export default function ioimgLoader({ src, width, quality }) {
  const base = process.env.NEXT_PUBLIC_IOIMG_URL!;
  const ops = ["f_auto", `q_${quality ?? "auto"}`, `w_${width}`];
  if (process.env.NEXT_PUBLIC_IOIMG_DPR) ops.push(`dpr_${process.env.NEXT_PUBLIC_IOIMG_DPR}`);
  return `${base}/${ops.join(",")}/${src.replace(/^https?:\/\//, "$&")}`;
}
```

Catatan:
- `src` absolut lebih aman (ioimg butuh URL lengkap). For relative, prefix dengan origin origin-asset.
- Untuk signed URL, loader harus berjalan di server → gunakan `IoImage` (route handler `/api/img`) atau `unoptimized` + URL yang sudah di-sign saat render (RSC).
- Tambahkan `cdn.acme.com` ke allowlist project di dashboard.

## 5. Nuxt

```bash
npm i @ioimg/nuxt
```

```ts
// nuxt.config.ts
export default defineNuxtConfig({
  modules: ["@ioimg/nuxt"],
  ioimg: {
    baseUrl: "https://acme.ioimg.dev",
    salt: process.env.IOIMG_SALT,      // hanya server
    defaults: { format: "auto", quality: "auto" },
  },
  image: {
    providers: {
      ioimg: { name: "ioimg", provider: "@ioimg/nuxt/provider" },
    },
  },
});
```

```vue
<NuxtImg provider="ioimg" src="https://cdn.acme.com/hero.jpg" width="800" height="600" fit="cover" />
```

## 6. SvelteKit

```js
// svelte.config.js
import { ioimgLoader } from "@ioimg/sveltekit";
export default {
  kit: { adapter: adapter(), images: { loader: ioimgLoader({ base: "https://acme.ioimg.dev" }) } },
};
```

```svelte
<img src={ioimg("https://cdn.acme.com/hero.jpg", { w: 800, f: "auto" })} srcset={srcset(...)} alt="" />
```

## 7. Astro

```js
// astro.config.mjs
import ioimg from "@ioimg/astro";
export default defineConfig({
  image: { service: ioimg({ baseUrl: "https://acme.ioimg.dev" }) },
});
```

## 8. WordPress

Plugin `ioimg-wordpress`:

1. Settings: `Base URL`, `Signing salt` (opsional), `Enable for sizes`, `Fallback on error`.
2. Hook `wp_get_attachment_image_src` dan `wp_calculate_image_srcset` → rewrite host ke ioimg, pertahankan `w`/`h`.
3. Filter `the_content` untuk gambar eksternal (opsional, default off).
4. Tombol **Test connection** + **Purge cache** (memanggil `/v1/purge`).
5. Fallback: bila `<img>` gagal load (`onerror`), kembalikan `src` asli — via atribut `data-orig`.
6. Konversi format otomatis terjadi di server (ioimg `f_auto`), **tanpa** menulis file `.webp` ke `wp-content` (hindari duplikasi & masalah regenerasi).

Batasan yang didokumentasikan: gambar yang dilindungi hotlink protection, atau di-serve dari domain tanpa akses publik (mis. `localhost` dev), harus masuk allowlist / dilewati.

## 9. Laravel

```php
// config/ioimg.php
return ['base' => env('IOIMG_URL'), 'salt' => env('IOIMG_SALT')];
```

```blade
<img src="{{ ioimg($post->cover_url)->w(800)->auto() }}"
     srcset="{{ ioimg_srcset($post->cover_url, [320,640,1280]) }}" alt="">
```

Middleware opsional `IoimgRewrite` mengubah HTML response untuk aset dari host tertentu (mirip WordPress).

## 10. Shopify

Batas: Shopify memaksa aset lewat `cdn.shopify.com`, dan `img_url` filter hanya mendukung parameter Shopify. Pola yang dipakai:

1. **App proxy**: `/apps/ioimg/*` → ioimg dengan signature yang dibuat app (server-side).
2. **Theme snippet** `ioimg-img.liquid`:

```liquid
{%- assign u = image.src | ioimg_url: width: 800, format: 'auto' -%}
<img src="{{ u }}" width="{{ 800 }}" height="{{ image.height | times: 800 | divided_by: image.width }}" loading="lazy" alt="{{ image.alt | escape }}">
```

3. Perhatian: jangan proxy checkout/aset yang butuh cookie; batasi ke collection/product image.

## 11. Ghost / CMS lain

- **Ghost**: Settings → Code injection → footer snippet yang menulis ulang `img[src^="https://cdn.example"]` ke ioimg (progressive enhancement, `<noscript>` tetap URL asli).
- **Hugo/Jekyll**: shortcode `{{< img src="..." w="800" >}}`.
- **Webflow**: custom code embed; atau gunakan Custom Domain + Cloudflare Worker rewrite.

## 12. Pola rewrite HTML (universal)

Untuk platform tanpa plugin: reverse proxy yang mengubah HTML.

```
nginx: sub_filter 'https://cdn.acme.com/' 'https://acme.ioimg.dev/w_auto,f_auto/https://cdn.acme.com/';
Cloudflare Worker: HTMLRewriter → element 'img' → setAttribute('src', transform(...))
```

Aturan aman:
- Hanya rewrite host yang ada di allowlist.
- Pertahankan `width`/`height` untuk mencegah CLS.
- Jangan rewrite gambar di dalam `<noscript>` dua kali.
- Sediakan kill-switch (env var) untuk mematikan rewrite tanpa deploy ulang.

## 13. Integrasi origin storage

| Sumber | Cara |
| --- | --- |
| S3 / R2 / MinIO | `source = s3://bucket/key` dengan kredensial per project (read-only, prefix-scoped) |
| GCS | `gs://bucket/key` via service account |
| Local/NFS | `file:///mnt/media/...` dengan root terbatas |
| HTTP dengan auth | header kustom terenkripsi per project (`Authorization`, `X-Api-Key`) |
| Private + signed | ioimg membuat presigned request ke origin, bukan menyimpan cookie |

## 14. Testing & DX

- **Local dev**: `docker run -p 8080:8080 ghcr.io/ioimg/ioimg:dev --config ./ioimg.toml` dengan `allow_private_ips = true`.
- **Mock mode**: SDK `buildUrl` murni fungsi string → tidak butuh server untuk unit test.
- **Contract test**: setiap SDK punya golden-file test yang membandingkan output URL dengan fixture di `spec/url-fixtures.json` (single source of truth).
- **Versioning**: SDK major mengikuti API major; `X-IOIMG-Api-Version` dikirim SDK.

## 15. Checklist integrasi baru

- [ ] URL builder + signing helper (test vector dari `spec/url-fixtures.json`)
- [ ] `srcset`/`dpr` handling
- [ ] Fallback ke URL asli saat error (kecuali aplikasi butuh fail-closed)
- [ ] Dokumentasi allowlist + contoh config
- [ ] Contoh aplikasi (repo `examples/<platform>`)
- [ ] E2E test terhadap instance lokal
- [ ] Catatan batasan platform
