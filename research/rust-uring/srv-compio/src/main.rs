use poc_core::Config;
use poc_http::{serve_compio, AppState};
use poc_io::{StdIo, UringIo};

#[compio::main]
async fn main() -> anyhow::Result<()> {
    let cfg = Config::from_env();
    std::fs::create_dir_all(&cfg.cache_dir)?;
    std::fs::create_dir_all(&cfg.src_dir)?;
    let which = std::env::var("IOIMG_IO").unwrap_or_else(|_| "uring".into());
    match which.as_str() {
        "std" => {
            println!("[io] std::fs");
            serve_compio(AppState::new(cfg, StdIo)).await
        }
        "uring" => {
            let n = std::env::var("IOIMG_URING_THREADS").ok().and_then(|v| v.parse().ok()).unwrap_or(1);
            let io = UringIo::with_threads(n)?;
            println!("[io] io_uring ({n} ring thread)");
            serve_compio(AppState::new(cfg, io)).await
        }
        other => anyhow::bail!("IOIMG_IO tidak dikenal: {other}"),
    }
}
