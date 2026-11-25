//! Load generator minimal, tanpa dependency: RPS + latensi persentil.
//!
//! `{i}` di path = request unik per iterasi (memaksa cache miss).
//! `{i:N}` = nilai `200 + (n % N)` (beban konstan, key tetap unik).
//! Path tanpa placeholder memakai request yang sudah dibangun sekali,
//! supaya client tidak menjadi bottleneck.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpStream;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

fn main() {
    let a: Vec<String> = std::env::args().collect();
    if a.len() < 5 {
        eprintln!("usage: bench <host:port> <path> <threads> <secs> [nokeep]");
        std::process::exit(2);
    }
    let addr = a[1].clone();
    let path = a[2].clone();
    let threads: usize = a[3].parse().unwrap();
    let secs: u64 = a[4].parse().unwrap();
    let keep = a.get(5).map(|s| s != "nokeep").unwrap_or(true);
    let end = Instant::now() + Duration::from_secs(secs);
    let count = Arc::new(AtomicU64::new(0));
    let errors = Arc::new(AtomicU64::new(0));
    let mut hs = vec![];

    fn build(path: &str, n: u64) -> String {
        let p = if let Some(rest) = path.split("{i:").nth(1) {
            let m: u64 = rest.split('}').next().unwrap_or("1").parse().unwrap_or(1);
            let head = path.split("{i:").next().unwrap_or("");
            let tail = rest.split('}').nth(1).unwrap_or("");
            format!("{head}{}{tail}", 200 + n % m.max(1))
        } else {
            path.replace("{i}", &n.to_string())
        };
        format!("GET {p} HTTP/1.1\r\nHost: bench\r\nConnection: keep-alive\r\n\r\n")
    }
    let dynamic = path.contains("{i}");
    let static_req = if dynamic { None } else { Some(build(&path, 0)) };
    let counter = Arc::new(AtomicU64::new(0));

    for _ in 0..threads {
        let (addr, count, errors, counter) = (addr.clone(), count.clone(), errors.clone(), counter.clone());
        let static_req = static_req.clone();
        let tpl = path.clone();
        hs.push(std::thread::spawn(move || {
            let mut lat: Vec<u32> = Vec::with_capacity(1 << 20);
            let mut local = 0u64;
            while Instant::now() < end {
                let s = match TcpStream::connect(&addr) {
                    Ok(s) => s,
                    Err(_) => {
                        errors.fetch_add(1, Ordering::Relaxed);
                        continue;
                    }
                };
                s.set_nodelay(true).ok();
                let mut rdr = BufReader::with_capacity(64 * 1024, s.try_clone().unwrap());
                let mut w = s;
                loop {
                    let t = Instant::now();
                    let dyn_req;
                    let req: &[u8] = match &static_req {
                        Some(r) => r.as_bytes(),
                        None => {
                            dyn_req = build(&tpl, counter.fetch_add(1, Ordering::Relaxed));
                            dyn_req.as_bytes()
                        }
                    };
                    if w.write_all(req).is_err() {
                        errors.fetch_add(1, Ordering::Relaxed);
                        break;
                    }
                    let mut len = 0usize;
                    let mut line = String::new();
                    let mut bad = false;
                    loop {
                        line.clear();
                        match rdr.read_line(&mut line) {
                            Ok(0) => {
                                bad = true;
                                break;
                            }
                            Ok(_) => {}
                            Err(_) => {
                                bad = true;
                                break;
                            }
                        }
                        let t2 = line.trim_end();
                        if t2.is_empty() {
                            break;
                        }
                        let lt = t2.to_ascii_lowercase();
                        if let Some(v) = lt.strip_prefix("content-length:") {
                            len = v.trim().parse().unwrap_or(0);
                        }
                    }
                    if bad {
                        errors.fetch_add(1, Ordering::Relaxed);
                        break;
                    }
                    let mut body = vec![0u8; len];
                    if rdr.read_exact(&mut body).is_err() {
                        errors.fetch_add(1, Ordering::Relaxed);
                        break;
                    }
                    lat.push(t.elapsed().as_micros().min(u32::MAX as u128) as u32);
                    local += 1;
                    if !keep || Instant::now() >= end {
                        break;
                    }
                }
            }
            count.fetch_add(local, Ordering::Relaxed);
            lat
        }));
    }
    let mut all: Vec<u32> = Vec::new();
    for h in hs {
        all.extend(h.join().unwrap());
    }
    all.sort_unstable();
    let pct = |p: f64| -> u32 {
        if all.is_empty() {
            return 0;
        }
        all[((all.len() as f64 - 1.0) * p).round() as usize]
    };
    let c = count.load(Ordering::Relaxed);
    println!(
        "rps={:.0} n={} err={} p50={}us p90={}us p99={}us max={}us",
        c as f64 / secs as f64,
        c,
        errors.load(Ordering::Relaxed),
        pct(0.50),
        pct(0.90),
        pct(0.99),
        all.last().copied().unwrap_or(0)
    );
}
