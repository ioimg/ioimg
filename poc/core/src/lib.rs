//! Inti PoC ioimg: parsing ops, cache key, transformasi gambar, counter.
//! Semua di sini sinkron & runtime-agnostik supaya bisa dipakai baik oleh
//! server tokio maupun compio.

use std::fmt;
use std::io::Cursor;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use anyhow::{anyhow, Context, Result};
use image::codecs::jpeg::JpegEncoder;
use image::imageops::FilterType;
use image::{DynamicImage, ExtendedColorType, ImageFormat};

// ---------------------------------------------------------------- config

#[derive(Clone, Debug)]
pub struct Config {
    pub src_dir: PathBuf,
    pub cache_dir: PathBuf,
    pub port: u16,
    /// `IOIMG_NOCACHE=1`: selalu transform, tanpa baca/tulis cache.
    /// Dipakai untuk mengukur kapasitas CPU tanpa pengaruh cache.
    pub nocache: bool,
}

impl Config {
    pub fn from_env() -> Self {
        Self {
            src_dir: std::env::var("IOIMG_SRC").unwrap_or_else(|_| "data/src".into()).into(),
            cache_dir: std::env::var("IOIMG_CACHE").unwrap_or_else(|_| "data/cache".into()).into(),
            port: std::env::var("IOIMG_PORT").ok().and_then(|p| p.parse().ok()).unwrap_or(8080),
            nocache: std::env::var("IOIMG_NOCACHE").map(|v| v == "1").unwrap_or(false),
        }
    }
}

// ---------------------------------------------------------------- ops

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Format {
    Jpeg,
    Png,
    Webp,
}

impl Format {
    pub fn parse(s: &str) -> Option<Self> {
        match s {
            "jpeg" | "jpg" => Some(Format::Jpeg),
            "png" => Some(Format::Png),
            "webp" => Some(Format::Webp),
            _ => None,
        }
    }

    pub fn ext(self) -> &'static str {
        match self {
            Format::Jpeg => "jpg",
            Format::Png => "png",
            Format::Webp => "webp",
        }
    }

    pub fn content_type(self) -> &'static str {
        match self {
            Format::Jpeg => "image/jpeg",
            Format::Png => "image/png",
            Format::Webp => "image/webp",
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Fit {
    /// pertahankan rasio, muat di dalam w x h
    Inside,
    /// potong tengah supaya persis w x h
    Cover,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Ops {
    pub w: Option<u32>,
    pub h: Option<u32>,
    pub fit: Fit,
    pub format: Format,
    pub quality: u8,
}

impl Default for Ops {
    fn default() -> Self {
        Self { w: None, h: None, fit: Fit::Inside, format: Format::Jpeg, quality: 80 }
    }
}

impl Ops {
    /// Format path: `w_800,h_600,fit_cover,f_webp,q_75`
    pub fn parse(s: &str) -> Result<Self> {
        let mut ops = Ops::default();
        let mut seen_format = false;
        for part in s.split(',').filter(|p| !p.is_empty()) {
            let (k, v) = part
                .split_once('_')
                .ok_or_else(|| anyhow!("ops tidak valid: `{part}` (harus k_v)"))?;
            match k {
                "w" => ops.w = Some(v.parse().context("w harus angka")?),
                "h" => ops.h = Some(v.parse().context("h harus angka")?),
                "q" => {
                    let q: u8 = v.parse().context("q harus angka")?;
                    ops.quality = q.clamp(1, 100);
                }
                "fit" => {
                    ops.fit = match v {
                        "inside" => Fit::Inside,
                        "cover" => Fit::Cover,
                        _ => return Err(anyhow!("fit harus inside|cover")),
                    }
                }
                "f" => {
                    ops.format = Format::parse(v).ok_or_else(|| anyhow!("format tidak dikenal: {v}"))?;
                    seen_format = true;
                }
                other => return Err(anyhow!("ops tidak dikenal: {other}")),
            }
        }
        if ops.w.is_none() && ops.h.is_none() && !seen_format {
            return Err(anyhow!("minimal satu operasi (w/h/f)"));
        }
        if ops.w == Some(0) || ops.h == Some(0) {
            return Err(anyhow!("w/h tidak boleh 0"));
        }
        Ok(ops)
    }

    /// Bentuk kanonik untuk cache key — urutan field tetap, bukan urutan input.
    pub fn canonical(&self) -> String {
        format!(
            "w={:?};h={:?};fit={:?};f={};q={}",
            self.w, self.h, self.fit, self.format.ext(), self.quality
        )
    }

    pub fn resize(&self, img: DynamicImage) -> DynamicImage {
        match (self.w, self.h) {
            (None, None) => img,
            (Some(w), None) => {
                let ratio = w as f64 / img.width() as f64;
                let h = (img.height() as f64 * ratio).round().max(1.0) as u32;
                img.resize_exact(w, h, FilterType::Lanczos3)
            }
            (None, Some(h)) => {
                let ratio = h as f64 / img.height() as f64;
                let w = (img.width() as f64 * ratio).round().max(1.0) as u32;
                img.resize_exact(w, h, FilterType::Lanczos3)
            }
            (Some(w), Some(h)) => match self.fit {
                Fit::Inside => img.resize(w, h, FilterType::Lanczos3),
                Fit::Cover => img.resize_to_fill(w, h, FilterType::Lanczos3),
            },
        }
    }
}

impl fmt::Display for Ops {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.canonical())
    }
}

// ---------------------------------------------------------------- cache

/// `blake3_128(v1|ops|source)` — 16 byte pertama, hex.
pub fn cache_key(ops: &Ops, source: &str) -> String {
    let mut h = blake3::Hasher::new();
    h.update(b"v1|");
    h.update(ops.canonical().as_bytes());
    h.update(b"|");
    h.update(source.as_bytes());
    h.finalize().to_hex().chars().take(32).collect()
}

pub fn cache_path(cache_dir: &Path, key: &str, fmt: Format) -> PathBuf {
    cache_dir.join(format!("{key}.{}", fmt.ext()))
}

// ---------------------------------------------------------------- transform

pub fn transform(src: &[u8], ops: &Ops) -> Result<Vec<u8>> {
    let img = image::load_from_memory(src).context("decode gagal")?;
    let img = ops.resize(img);
    encode(&img, ops.format, ops.quality)
}

pub fn encode(img: &DynamicImage, fmt: Format, quality: u8) -> Result<Vec<u8>> {
    let mut out = Vec::with_capacity(256 * 1024);
    match fmt {
        Format::Jpeg => {
            let rgb = img.to_rgb8();
            let mut enc = JpegEncoder::new_with_quality(&mut out, quality);
            enc.encode(&rgb, rgb.width(), rgb.height(), ExtendedColorType::Rgb8)?;
        }
        Format::Png => {
            img.write_to(&mut Cursor::new(&mut out), ImageFormat::Png)?;
        }
        Format::Webp => {
            // image 0.25 hanya punya encoder WebP lossless.
            let rgba = img.to_rgba8();
            let (w, h) = (rgba.width(), rgba.height());
            let enc = image::codecs::webp::WebPEncoder::new_lossless(&mut out);
            enc.encode(rgba.as_raw(), w, h, ExtendedColorType::Rgba8)?;
        }
    }
    Ok(out)
}

// ---------------------------------------------------------------- stats

#[derive(Default)]
pub struct Stats {
    pub requests: AtomicU64,
    pub hits: AtomicU64,
    pub misses: AtomicU64,
    pub errors: AtomicU64,
    pub bytes_out: AtomicU64,
    pub transform_ns: AtomicU64,
    pub cache_read_ns: AtomicU64,
}

impl Stats {
    pub fn inc(&self, f: impl Fn(&Self) -> &AtomicU64) {
        f(self).fetch_add(1, Ordering::Relaxed);
    }
    pub fn add(&self, f: impl Fn(&Self) -> &AtomicU64, v: u64) {
        f(self).fetch_add(v, Ordering::Relaxed);
    }
    pub fn get(&self, f: impl Fn(&Self) -> &AtomicU64) -> u64 {
        f(self).load(Ordering::Relaxed)
    }
    pub fn render(&self) -> String {
        format!(
            "# HELP ioimg_requests_total Total request\n\
             # TYPE ioimg_requests_total counter\n\
             ioimg_requests_total {}\n\
             ioimg_cache_hits_total {}\n\
             ioimg_cache_misses_total {}\n\
             ioimg_errors_total {}\n\
             ioimg_bytes_out_total {}\n\
             ioimg_transform_ns_total {}\n\
             ioimg_cache_read_ns_total {}\n",
            self.get(|s| &s.requests),
            self.get(|s| &s.hits),
            self.get(|s| &s.misses),
            self.get(|s| &s.errors),
            self.get(|s| &s.bytes_out),
            self.get(|s| &s.transform_ns),
            self.get(|s| &s.cache_read_ns),
        )
    }
}

pub type SharedStats = Arc<Stats>;

/// Jalankan pekerjaan CPU-bound tanpa mengikat runtime (dipakai sama di
/// tokio & compio). PoC: satu thread per miss; produksi: pool terbatas.
pub async fn run_blocking<T, F>(f: F) -> Result<T>
where
    T: Send + 'static,
    F: FnOnce() -> Result<T> + Send + 'static,
{
    let (tx, rx) = futures_channel::oneshot::channel();
    std::thread::Builder::new()
        .name("ioimg-transform".into())
        .spawn(move || {
            let _ = tx.send(f());
        })
        .context("gagal spawn thread transform")?;
    rx.await.context("thread transform hilang")?
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_ops() {
        let o = Ops::parse("w_800,h_600,fit_cover,f_webp,q_75").unwrap();
        assert_eq!(o.w, Some(800));
        assert_eq!(o.format, Format::Webp);
        assert_eq!(o.quality, 75);
        assert_eq!(o.fit, Fit::Cover);
    }

    #[test]
    fn canonical_is_order_independent() {
        let a = Ops::parse("w_800,f_webp").unwrap();
        let b = Ops::parse("f_webp,w_800").unwrap();
        assert_eq!(cache_key(&a, "x"), cache_key(&b, "x"));
    }

    #[test]
    fn rejects_bad_ops() {
        assert!(Ops::parse("zzz_1").is_err());
        assert!(Ops::parse("f_gif").is_err());
        assert!(Ops::parse("").is_err());
    }
}
