// ioimg URL builder (browser + Node 18+).
//
// This file mirrors the canonicalisation rules of the Go service:
//   - op order: bg, blur, bri, c, con, dpr, enlarge, f, flip, g, h, mono,
//     palette, pixel, q, rot, sat, sharp, strip, trim, w
//   - defaults: c=fit, f=auto, g=center, strip=1, dpr=1
//   - source: lowercase scheme/host, drop default port/fragment, collapse //

const DEFAULTS = { c: 'fit', f: 'auto', g: 'center', strip: 1, dpr: 1 };
const ORDER = ['bg', 'blur', 'bri', 'c', 'con', 'dpr', 'enlarge', 'f', 'flip', 'g', 'h', 'mono', 'palette', 'pixel', 'q', 'rot', 'sat', 'sharp', 'strip', 'trim', 'w'];
const FORMAT_ALIASES = { jpg: 'jpeg' };

/** Normalise a source URL exactly like the server does. */
export function canonicalSource(raw) {
  const u = new URL(raw); // throws on relative/invalid
  if (u.protocol !== 'http:' && u.protocol !== 'https:') throw new Error('source must be http(s)');
  const port = u.port; // already empty for the default port
  let pathname = u.pathname.replace(/\/{2,}/g, '/');
  if (pathname === '') pathname = '/';
  return `${u.protocol}//${u.hostname}${port ? ':' + port : ''}${pathname}${u.search}`;
}

function normaliseOps(ops) {
  if (typeof ops === 'string') return canonicalOpsFromString(ops);
  const merged = { ...DEFAULTS, ...ops };
  if (merged.f) merged.f = FORMAT_ALIASES[merged.f] || merged.f;
  return canonicalOpsFromObject(merged);
}

function canonicalOpsFromString(raw) {
  const obj = {};
  for (const part of raw.split(',')) {
    if (!part) continue;
    const i = part.indexOf('_');
    if (i < 0) throw new Error(`invalid op: ${part}`);
    obj[part.slice(0, i)] = part.slice(i + 1);
  }
  return canonicalOpsFromObject({ ...DEFAULTS, ...obj });
}

function canonicalOpsFromObject(o) {
  const out = [];
  for (const key of ORDER) {
    let v = o[key];
    if (v === undefined || v === null || v === '') continue;
    if (key === 'dpr' && Number(v) === 1) continue;
    if (key === 'strip' && String(v) === '1') {
      out.push('strip_1');
      continue;
    }
    if (key === 'f') v = FORMAT_ALIASES[v] || v;
    if (key === 'bg' && v !== 'transparent') v = String(v).replace(/^#/, '').toLowerCase();
    if (typeof v === 'number') v = Number.isInteger(v) ? String(v) : String(Number(v.toFixed(2)));
    out.push(`${key}_${v}`);
  }
  return out.join(',');
}

/** Build an unsigned ioimg URL. */
export function imageUrl({ base, source, ops }) {
  const o = normaliseOps(ops);
  return `${String(base).replace(/\/+$/, '')}/${o}/${canonicalSource(source)}`;
}

/**
 * Build a signed ioimg URL.
 *
 * `key` is "kid:secret" or just "secret". Never call this in a browser with a
 * real secret: sign on the server and cache the resulting URLs.
 */
export async function signedImageUrl({ base, source, ops, key, expSeconds = 3600, includeExp = true }) {
  if (!globalThis.crypto?.subtle) throw new Error('Web Crypto is required');
  const o = normaliseOps(ops);
  const canonical = canonicalSource(source);
  const exp = expSeconds > 0 ? Math.floor(Date.now() / 1000) + expSeconds : 0;
  const secret = String(key).includes(':') ? String(key).split(':').slice(1).join(':') : String(key);
  const body = includeExp && exp ? `${o}/${canonical}?exp=${exp}` : `${o}/${canonical}`;
  const enc = new TextEncoder();
  const cryptoKey = await crypto.subtle.importKey('raw', enc.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const mac = new Uint8Array(await crypto.subtle.sign('HMAC', cryptoKey, enc.encode(body)));
  const sig = base64url(mac);
  const suffix = exp && includeExp ? `?exp=${exp}` : '';
  return `${String(base).replace(/\/+$/, '')}/${sig}/${o}/${canonical}${suffix}`;
}

/** Verify a signed URL (useful in tests). */
export async function verifySignedUrl(rawUrl, key, includeExp = true) {
  const u = new URL(rawUrl);
  const marker = u.pathname.indexOf('http://') >= 0 ? u.pathname.indexOf('http://') : u.pathname.indexOf('https://');
  if (marker < 0) return false;
  const prefix = u.pathname.slice(1, marker).replace(/\/$/, '');
  const [sig, ops] = prefix.split('/');
  const canonical = canonicalSource(u.pathname.slice(marker));
  const exp = Number(u.searchParams.get('exp') || 0);
  const body = includeExp && exp ? `${ops}/${canonical}?exp=${exp}` : `${ops}/${canonical}`;
  const secret = String(key).includes(':') ? String(key).split(':').slice(1).join(':') : String(key);
  const enc = new TextEncoder();
  const cryptoKey = await crypto.subtle.importKey('raw', enc.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const mac = new Uint8Array(await crypto.subtle.sign('HMAC', cryptoKey, enc.encode(body)));
  return base64url(mac) === sig;
}

function base64url(bytes) {
  let bin = '';
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export { normaliseOps as canonicalOps };
