//! Generate dataset uji: JPEG sintetis (gradien + noise) supaya tidak ada
//! data pribadi di repo.

use image::codecs::jpeg::JpegEncoder;
use image::{ExtendedColorType, RgbImage};
use std::path::PathBuf;

fn main() -> anyhow::Result<()> {
    let mut a = std::env::args().skip(1);
    let dir: PathBuf = a.next().unwrap_or_else(|| "data/src".into()).into();
    let count: u32 = a.next().and_then(|v| v.parse().ok()).unwrap_or(8);
    let noise: bool = std::env::var("NOISE").map(|v| v != "0").unwrap_or(true);
    let w: u32 = a.next().and_then(|v| v.parse().ok()).unwrap_or(2400);
    let h: u32 = a.next().and_then(|v| v.parse().ok()).unwrap_or(1600);
    std::fs::create_dir_all(&dir)?;

    for i in 0..count {
        let mut img = RgbImage::new(w, h);
        for (x, y, px) in img.enumerate_pixels_mut() {
            let r = ((x * 255 / w.max(1)) as u8).wrapping_add((i * 17) as u8);
            let r = if noise { r } else { (r as u16 / 2) as u8 };
            let g = (y * 255 / h.max(1)) as u8;
            let b = if noise { (((x ^ y) & 0xff) as u8) ^ (i as u8 * 5) } else { ((x + y) / 8 % 256) as u8 };
            *px = image::Rgb([r, g, b]);
        }
        let path = dir.join(format!("photo{i:02}.jpg"));
        let mut out = Vec::new();
        let mut enc = JpegEncoder::new_with_quality(&mut out, 90);
        enc.encode(img.as_raw(), w, h, ExtendedColorType::Rgb8)?;
        std::fs::write(&path, &out)?;
        println!("{} {} KiB", path.display(), out.len() / 1024);
    }
    Ok(())
}
