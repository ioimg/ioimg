//! Ukur biaya transformasi CPU (tanpa HTTP, tanpa cache) — ini yang
//! menentukan kapasitas nyata ioimg.
use poc_core::{Ops, Format};
use std::time::Instant;

fn main() -> anyhow::Result<()> {
    let path = std::env::args().nth(1).unwrap_or_else(|| "../../artifacts/datasets/src/photo00.jpg".into());
    let src = std::fs::read(&path)?;
    println!("sumber: {path} ({} KiB)", src.len() / 1024);
    {
        let t = Instant::now();
        for _ in 0..5 { let _ = image::load_from_memory(&src)?; }
        println!("decode-only             {:>8.1} ms  {:>6.1} img/s/core",
            t.elapsed().as_secs_f64() * 1000.0 / 5.0, 5000.0 / (t.elapsed().as_secs_f64() * 1000.0));
    }
    let cases = [
        ("w400 jpeg q80", "w_400,f_jpeg,q_80"),
        ("w400 jpeg q80 triangle", "w_400,f_jpeg,q_80"),
        ("w800 jpeg q80", "w_800,f_jpeg,q_80"),
        ("w400 webp lossless", "w_400,f_webp"),
        ("w1600 jpeg q85", "w_1600,f_jpeg,q_85"),
        ("w400,h300 cover png", "w_400,h_300,fit_cover,f_png"),
    ];
    for (label, ops_str) in cases {
        let ops = Ops::parse(ops_str)?;
        // warmup
        let _ = poc_core::transform(&src, &ops)?;
        let iters = 5;
        let t = Instant::now();
        let mut out = Vec::new();
        for _ in 0..iters {
            out = poc_core::transform(&src, &ops)?;
        }
        let ms = t.elapsed().as_secs_f64() * 1000.0 / iters as f64;
        println!(
            "{:<26} {:>8.1} ms  out={:>6} KiB  {:>6.1} img/s/core",
            label, ms, out.len() / 1024, 1000.0 / ms
        );
    }
    Ok(())
}
