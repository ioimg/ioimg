//! probe-uring: jawab "io_uring di mesin/container ini benar-benar bisa apa?"
//! Bukan cek "tersedia", tapi cek: setup, read, write, dan O_DIRECT.

use std::io;
use std::os::fd::AsRawFd;
use std::time::Instant;

fn main() {
    println!("=== probe-uring ===");
    println!("kernel      : {}", kernel());
    println!("cores       : {}", std::thread::available_parallelism().map(|v| v.get()).unwrap_or(0));

    let mut params = [0u32; 30];
    let fd = unsafe { libc::syscall(libc::SYS_io_uring_setup, 64u32, params.as_mut_ptr()) };
    if fd < 0 {
        println!("io_uring_setup: GAGAL {:?} → backend uring tidak bisa dipakai", io::Error::last_os_error());
        std::process::exit(1);
    }
    println!("io_uring_setup: OK (features=0x{:x}, sq_entries={})", params[5], params[0]);
    unsafe { libc::close(fd as i32) };

    let dir = std::env::temp_dir().join("ioimg-probe");
    std::fs::create_dir_all(&dir).unwrap();
    let big = dir.join("big.bin");
    let data = vec![0xA5u8; 64 * 1024 * 1024];
    std::fs::write(&big, &data).unwrap();

    bench("std read  64MiB", 20, || {
        let v = std::fs::read(&big).unwrap();
        std::hint::black_box(v.len());
    });
    bench("uring read 64MiB", 20, || {
        let v = uring_read(&big, false).unwrap();
        std::hint::black_box(v.len());
    });

    println!("--- O_DIRECT");
    match uring_read(&big, true) {
        Ok(v) => println!("O_DIRECT + buffer aligned 4096 : OK ({} byte)", v.len()),
        Err(e) => println!("O_DIRECT + buffer aligned 4096 : GAGAL {e:?}"),
    }
    match uring_read_unaligned(&big) {
        Ok(_) => println!("O_DIRECT + buffer TIDAK aligned: OK (mengejutkan)"),
        Err(e) => println!("O_DIRECT + buffer TIDAK aligned: GAGAL {e:?} (sesuai harapan)"),
    }

    let w = dir.join("out.bin");
    let chunk = vec![0x5Au8; 16 * 1024 * 1024];
    bench("std write 16MiB", 20, || {
        std::fs::write(&w, &chunk).unwrap();
    });
    bench("uring write 16MiB", 20, || {
        uring_write(&w, &chunk).unwrap();
    });

    let _ = std::fs::remove_dir_all(&dir);
}

fn kernel() -> String {
    std::fs::read_to_string("/proc/sys/kernel/osrelease").unwrap_or_default().trim().to_string()
}

fn bench(name: &str, iters: usize, mut f: impl FnMut()) {
    // warmup
    f();
    let t = Instant::now();
    for _ in 0..iters {
        f();
    }
    let d = t.elapsed();
    println!("{name}: {:>7.1} ms/op  {:>7.2} GiB/s", d.as_secs_f64() * 1000.0 / iters as f64,
        (iters as f64 * 64.0 * 1024.0 * 1024.0 / d.as_secs_f64()) / (1024.0 * 1024.0 * 1024.0));
}

fn uring_read(path: &std::path::Path, direct: bool) -> io::Result<Vec<u8>> {
    let mut ring = io_uring::IoUring::new(8)?;
    let file = if direct {
        use std::os::unix::fs::OpenOptionsExt;
        std::fs::OpenOptions::new().read(true).custom_flags(libc::O_DIRECT).open(path)?
    } else {
        std::fs::File::open(path)?
    };
    let len = file.metadata()?.len() as usize;
    let mut buf = vec![0u8; len];
    let ptr = buf.as_mut_ptr();
    let (slice_ptr, slice_len) = if direct {
        // O_DIRECT butuh buffer & ukuran selaras 4096.
        let aligned_len = len.next_multiple_of(4096);
        let mut p: *mut libc::c_void = std::ptr::null_mut();
        let rc = unsafe { libc::posix_memalign(&mut p, 4096, aligned_len) };
        if rc != 0 {
            return Err(io::Error::from_raw_os_error(rc));
        }
        (p as *mut u8, aligned_len)
    } else {
        (ptr, len)
    };
    let fd = io_uring::types::Fd(file.as_raw_fd());
    let mut off = 0usize;
    let mut total = 0usize;
    while off < len {
        let chunk = (len - off).min(1024 * 1024);
        let entry = io_uring::opcode::Read::new(fd, slice_ptr.wrapping_add(off) as *mut u8, chunk as u32)
            .offset(off as u64)
            .build();
        let res = submit_one(&mut ring, entry)?;
        if res <= 0 {
            break;
        }
        total += res as usize;
        off += res as usize;
    }
    if direct {
        unsafe { libc::free(slice_ptr as *mut libc::c_void) };
        Ok(vec![0u8; total])
    } else {
        buf.truncate(total);
        Ok(buf)
    }
}

fn uring_read_unaligned(path: &std::path::Path) -> io::Result<Vec<u8>> {
    use std::os::unix::fs::OpenOptionsExt;
    let mut ring = io_uring::IoUring::new(8)?;
    let file = std::fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECT)
        .open(path)?;
    let mut buf = vec![0u8; 65536];
    let entry = io_uring::opcode::Read::new(
        io_uring::types::Fd(file.as_raw_fd()),
        buf.as_mut_ptr().wrapping_add(1),
        4096,
    )
    .offset(0)
    .build();
    submit_one(&mut ring, entry)?;
    Ok(buf)
}

fn uring_write(path: &std::path::Path, data: &[u8]) -> io::Result<()> {
    let mut ring = io_uring::IoUring::new(8)?;
    let file = std::fs::File::create(path)?;
    let fd = io_uring::types::Fd(file.as_raw_fd());
    let mut off = 0usize;
    while off < data.len() {
        let chunk = (data.len() - off).min(1024 * 1024);
        let entry = io_uring::opcode::Write::new(fd, data[off..].as_ptr(), chunk as u32)
            .offset(off as u64)
            .build();
        let res = submit_one(&mut ring, entry)?;
        if res <= 0 {
            return Err(io::Error::new(io::ErrorKind::WriteZero, "write 0"));
        }
        off += res as usize;
    }
    Ok(())
}

fn submit_one(ring: &mut io_uring::IoUring, entry: io_uring::squeue::Entry) -> io::Result<i32> {
    unsafe {
        ring.submission()
            .push(&entry)
            .map_err(|_| io::Error::new(io::ErrorKind::Other, "SQ penuh"))?;
    }
    ring.submit_and_wait(1)?;
    let cqe = ring.completion().next().ok_or_else(|| io::Error::new(io::ErrorKind::Other, "no cqe"))?;
    let res = cqe.result();
    if res < 0 {
        Err(io::Error::from_raw_os_error(-res))
    } else {
        Ok(res)
    }
}
