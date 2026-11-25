# Integrasi — URL builder & loader

Semua helper di sini hanya membangun URL; tidak ada SDK runtime yang perlu
di-embed di halaman. Untuk mode bertanda tangan, **jangan pernah** menaruh
secret di browser — buat URL di server (route handler, SSR, plugin, cron) atau
pakai endpoint signing milik control plane.

## JavaScript / TypeScript

`js/ioimg.mjs`:

```js
import { imageUrl, signedImageUrl, canonicalSource } from './integrations/js/ioimg.mjs';

imageUrl({
  base: 'https://img.example.com',
  source: 'https://cdn.example.com/hero.jpg',
  ops: { w: 800, f: 'auto', q: 'auto' },
});
// https://img.example.com/c_fit,f_auto,g_center,q_auto,strip_1,w_800/https://cdn.example.com/hero.jpg

await signedImageUrl({
  base: 'https://img.example.com',
  source: 'https://cdn.example.com/hero.jpg',
  ops: { w: 800, f: 'auto' },
  key: 'kid1:secret',       // server-side only
  expSeconds: 3600,
});
```

## Next.js

`nextjs/image-loader.ts` adalah loader unsigned (aman untuk client):

```js
// next.config.js
module.exports = {
  images: {
    loader: require.resolve('./integrations/nextjs/image-loader.ts'),
    path: 'https://img.example.com',
  },
};
```

Untuk signed mode, buat komponen server yang menandatangani URL lalu teruskan
`src` hasilnya ke `<Image unoptimized />` atau `<img>`. Loader Next berjalan di
client dan tidak boleh memegang secret.

## WordPress

`wordpress/ioimg.php` adalah plugin drop-in:

1. copy ke `wp-content/plugins/ioimg/ioimg.php`;
2. aktifkan di wp-admin;
3. isi `IOIMG_BASE_URL` dan (opsional) `IOIMG_SIGNING_KEY` di `wp-config.php`.

Plugin memfilter URL lampiran menjadi URL ioimg (unsigned secara default).
Karena WordPress adalah server, signing bisa diaktifkan dengan mengisi
`IOIMG_SIGNING_KEY` (format `kid:secret`).
