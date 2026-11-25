<?php
/**
 * Plugin Name: ioimg
 * Description: Serve WordPress attachment images through an ioimg image proxy.
 * Version: 0.1.0
 * Requires PHP: 7.4
 *
 * Configure in wp-config.php:
 *
 *   define('IOIMG_BASE_URL', 'https://img.example.com');
 *   // Optional (keeps signing on the server):
 *   define('IOIMG_SIGNING_KEY', 'kid1:secret');
 *   define('IOIMG_SIGNING_TTL', 3600);
 *
 * Only public attachment URLs are rewritten; admin URLs and non-image files
 * are left untouched.
 */

if (!defined('ABSPATH')) {
    exit;
}

function ioimg_base_url(): string
{
    return rtrim((string) (defined('IOIMG_BASE_URL') ? IOIMG_BASE_URL : ''), '/');
}

/**
 * Mirror of the server's canonical ops order.
 */
function ioimg_canonical_ops(array $ops): string
{
    $defaults = ['c' => 'fit', 'f' => 'auto', 'g' => 'center', 'strip' => '1', 'dpr' => '1'];
    $ops = array_merge($defaults, array_filter($ops, static fn($v) => $v !== null && $v !== ''));
    $order = ['bg', 'blur', 'c', 'dpr', 'enlarge', 'f', 'flip', 'g', 'h', 'palette', 'q', 'rot', 'sharp', 'strip', 'trim', 'w'];
    $parts = [];
    foreach ($order as $key) {
        if (!array_key_exists($key, $ops)) {
            continue;
        }
        $value = $ops[$key];
        if ($key === 'f' && $value === 'jpg') {
            $value = 'jpeg';
        }
        if ($key === 'strip' && (string) $value === '1') {
            $parts[] = 'strip_1';
            continue;
        }
        $parts[] = $key . '_' . $value;
    }
    return implode(',', $parts);
}

function ioimg_canonical_source(string $url): ?string
{
    $u = wp_parse_url($url);
    if (!$u || empty($u['scheme']) || empty($u['host'])) {
        return null;
    }
    $scheme = strtolower($u['scheme']);
    if ($scheme !== 'http' && $scheme !== 'https') {
        return null;
    }
    $host = strtolower($u['host']);
    $port = '';
    if (!empty($u['port'])) {
        $isDefault = ($scheme === 'http' && (int) $u['port'] === 80) || ($scheme === 'https' && (int) $u['port'] === 443);
        if (!$isDefault) {
            $port = ':' . (int) $u['port'];
        }
    }
    $path = preg_replace('#/+#', '/', $u['path'] ?? '/');
    $query = isset($u['query']) ? '?' . $u['query'] : '';
    return $scheme . '://' . $host . $port . $path . $query;
}

function ioimg_url(string $source, array $ops = [], ?int $exp = null): string
{
    $base = ioimg_base_url();
    if ($base === '') {
        return $source;
    }
    $canonicalSource = ioimg_canonical_source($source);
    if (!$canonicalSource) {
        return $source;
    }
    $canonicalOps = ioimg_canonical_ops($ops);
    $url = $base . '/' . $canonicalOps . '/' . $canonicalSource;

    $key = defined('IOIMG_SIGNING_KEY') ? (string) IOIMG_SIGNING_KEY : '';
    if ($key === '') {
        return $url;
    }
    $secret = strpos($key, ':') !== false ? substr($key, strpos($key, ':') + 1) : $key;
    $ttl = defined('IOIMG_SIGNING_TTL') ? (int) IOIMG_SIGNING_TTL : 3600;
    $exp = $exp ?? (time() + $ttl);
    $sep = strpos($canonicalSource, '?') !== false ? '&' : '?';
    $body = $canonicalOps . '/' . $canonicalSource . $sep . 'exp=' . $exp;
    $sig = rtrim(strtr(base64_encode(hash_hmac('sha256', $body, $secret, true)), '+/', '-_'), '=');
    return $base . '/' . $sig . '/' . $canonicalOps . '/' . $canonicalSource . $sep . 'exp=' . $exp;
}

/**
 * Rewrite attachment image URLs in content and srcset.
 */
function ioimg_filter_attachment_url($url, $attachment_id, $size)
{
    if (is_admin() || !wp_attachment_is_image($attachment_id)) {
        return $url;
    }
    $width = null;
    $meta = wp_get_attachment_metadata($attachment_id);
    if (!empty($meta['sizes'][$size]['width'])) {
        $width = (int) $meta['sizes'][$size]['width'];
    }
    $ops = $width ? ['w' => $width, 'f' => 'auto', 'q' => 'auto'] : ['f' => 'auto', 'q' => 'auto'];
    return ioimg_url($url, $ops);
}
add_filter('wp_get_attachment_image_src', 'ioimg_filter_attachment_url', 10, 3);

/**
 * Shortcode: [ioimg src="https://..." w="800" f="auto" q="auto"].
 */
function ioimg_shortcode($atts)
{
    $atts = shortcode_atts(['src' => '', 'w' => '', 'h' => '', 'c' => '', 'f' => 'auto', 'q' => 'auto', 'alt' => ''], $atts);
    if (!$atts['src']) {
        return '';
    }
    $ops = array_filter(['w' => $atts['w'], 'h' => $atts['h'], 'c' => $atts['c'], 'f' => $atts['f'], 'q' => $atts['q']]);
    $url = ioimg_url(esc_url_raw($atts['src']), $ops);
    $alt = esc_attr($atts['alt']);
    return '<img src="' . esc_url($url) . '" alt="' . $alt . '" loading="lazy" decoding="async">';
}
add_shortcode('ioimg', 'ioimg_shortcode');
