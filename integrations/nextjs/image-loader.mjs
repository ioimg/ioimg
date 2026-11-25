// Next.js custom image loader for ioimg (unsigned mode).
//
// next.config.js:
//   images: {
//     loader: require.resolve('../integrations/nextjs/image-loader.mjs'),
//     path: 'https://img.example.com',
//   }
//
// The loader runs on the client, so it must never hold a signing secret.
// For signed URLs, generate them on the server and render <Image unoptimized />.

export default function ioimgLoader({ src, width, quality }) {
  const base = (process.env.NEXT_PUBLIC_IOIMG_BASE || '').replace(/\/+$/, '');
  const source = /^https?:\/\//i.test(src)
    ? src
    : new URL(src, process.env.NEXT_PUBLIC_SITE_URL || 'http://localhost:3000').toString();

  const ops = [
    'c_fit',
    'f_auto',
    'g_center',
    quality ? `q_${quality}` : 'q_auto',
    'strip_1',
    `w_${width}`,
  ].join(',');

  return `${base}/${ops}/${source}`;
}
