//! Backend cache I/O yang bisa dipakai runtime apa pun.
//!
//! Trait `CacheIo` sengaja runtime-agnostik: implementasinya tidak boleh
//! menyentuh API tokio, supaya satu handler bisa jalan di tokio dan compio.

use std::io;
use std::path::{Path, PathBuf};

/// Kontrak cache. `read` mengembalikan `None` bila file tidak ada.
pub trait CacheIo: Clone + Send + Sync + 'static {
    fn read<'a>(
        &'a self,
        path: &'a Path,
    ) -> impl std::future::Future<Output = io::Result<Option<Vec<u8>>>> + Send + 'a;

    fn write<'a>(
        &'a self,
        path: &'a Path,
        data: Vec<u8>,
    ) -> impl std::future::Future<Output = io::Result<()>> + Send + 'a;
}

// ------------------------------------------------------------------ std

/// Baseline: `std::fs` langsung di dalam async task (apa yang dilakukan
/// mayoritas aplikasi axum yang tidak peduli).
#[derive(Clone, Default)]
pub struct StdIo;

impl CacheIo for StdIo {
    fn read<'a>(
        &'a self,
        path: &'a Path,
    ) -> impl std::future::Future<Output = io::Result<Option<Vec<u8>>>> + Send + 'a {
        async move {
        match std::fs::read(path) {
            Ok(v) => Ok(Some(v)),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(e),
        }
        }
    }

    fn write<'a>(
        &'a self,
        path: &'a Path,
        data: Vec<u8>,
    ) -> impl std::future::Future<Output = io::Result<()>> + Send + 'a {
        async move {
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        std::fs::write(path, data)
        }
    }
}

// ------------------------------------------------------------------ tokio

#[cfg(feature = "tokio-io")]
#[derive(Clone, Default)]
pub struct TokioIo;

#[cfg(feature = "tokio-io")]
impl CacheIo for TokioIo {
    fn read<'a>(
        &'a self,
        path: &'a Path,
    ) -> impl std::future::Future<Output = io::Result<Option<Vec<u8>>>> + Send + 'a {
        async move {
        match tokio::fs::read(path).await {
            Ok(v) => Ok(Some(v)),
            Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
            Err(e) => Err(e),
        }
        }
    }

    fn write<'a>(
        &'a self,
        path: &'a Path,
        data: Vec<u8>,
    ) -> impl std::future::Future<Output = io::Result<()>> + Send + 'a {
        async move {
        if let Some(parent) = path.parent() {
            tokio::fs::create_dir_all(parent).await?;
        }
        tokio::fs::write(path, data).await
        }
    }
}

// ------------------------------------------------------------------ uring

enum Job {
    Read {
        path: PathBuf,
        resp: futures_channel::oneshot::Sender<io::Result<Option<Vec<u8>>>>,
    },
    Write {
        path: PathBuf,
        data: Vec<u8>,
        resp: futures_channel::oneshot::Sender<io::Result<()>>,
    },
}

/// Cache I/O lewat io_uring. Satu thread pemilik ring; request dikirim lewat
/// channel dan dijawab lewat oneshot. Ring di-submit per operasi (PoC) —
/// batching & registered buffers belum ada, lihat README.
#[derive(Clone)]
pub struct UringIo {
    tx: std::sync::Arc<std::sync::Mutex<std::sync::mpsc::Sender<Job>>>,
    threads: std::sync::Arc<std::sync::atomic::AtomicUsize>,
}

impl UringIo {
    pub fn new() -> io::Result<Self> {
        Self::with_threads(1)
    }

    pub fn with_threads(n: usize) -> io::Result<Self> {
        let (tx, rx) = std::sync::mpsc::channel::<Job>();
        let rx = std::sync::Arc::new(std::sync::Mutex::new(rx));
        let threads = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
        for i in 0..n.max(1) {
            let rx = rx.clone();
            let threads2 = threads.clone();
            std::thread::Builder::new()
                .name(format!("ioimg-uring-{i}"))
                .spawn(move || {
                    // Probe ring di thread ini; kalau gagal, thread mati dan
                    // request akan mendapat error "ring thread hilang".
                    let mut ring = match io_uring::IoUring::new(16) {
                        Ok(r) => r,
                        Err(_) => return,
                    };
                    threads2.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
                    loop {
                        let job = match rx.lock().unwrap().recv() {
                            Ok(j) => j,
                            Err(_) => break,
                        };
                        match job {
                            Job::Read { path, resp } => {
                                let _ = resp.send(do_read(&mut ring, &path));
                            }
                            Job::Write { path, data, resp } => {
                                let _ = resp.send(do_write(&mut ring, &path, &data));
                            }
                        }
                    }
                })?;
        }
        // Tunggu ring siap (maks 1s), supaya startup gagal cepat & jelas.
        for _ in 0..100 {
            if threads.load(std::sync::atomic::Ordering::SeqCst) > 0 {
                return Ok(Self { tx: std::sync::Arc::new(std::sync::Mutex::new(tx)), threads });
            }
            std::thread::sleep(std::time::Duration::from_millis(10));
        }
        Err(io::Error::new(
            io::ErrorKind::Other,
            "io_uring ring gagal dibuat (seccomp? kernel?)",
        ))
    }

    pub fn ring_threads(&self) -> usize {
        self.threads.load(std::sync::atomic::Ordering::SeqCst)
    }

    fn submit(&self, job: Job) -> io::Result<()> {
        let tx = self.tx.lock().unwrap();
        tx.send(job).map_err(|_| {
            io::Error::new(io::ErrorKind::BrokenPipe, "uring ring thread sudah berhenti")
        })
    }
}

impl CacheIo for UringIo {
    fn read<'a>(
        &'a self,
        path: &'a Path,
    ) -> impl std::future::Future<Output = io::Result<Option<Vec<u8>>>> + Send + 'a {
        async move {
            let (tx, rx) = futures_channel::oneshot::channel();
            self.submit(Job::Read { path: path.to_path_buf(), resp: tx })?;
            rx.await
                .map_err(|_| io::Error::new(io::ErrorKind::BrokenPipe, "ring thread hilang"))?
        }
    }

    fn write<'a>(
        &'a self,
        path: &'a Path,
        data: Vec<u8>,
    ) -> impl std::future::Future<Output = io::Result<()>> + Send + 'a {
        async move {
            let (tx, rx) = futures_channel::oneshot::channel();
            self.submit(Job::Write { path: path.to_path_buf(), data, resp: tx })?;
            rx.await
                .map_err(|_| io::Error::new(io::ErrorKind::BrokenPipe, "ring thread hilang"))?
        }
    }
}

fn do_read(ring: &mut io_uring::IoUring, path: &Path) -> io::Result<Option<Vec<u8>>> {
    use io_uring::{opcode, types};
    use std::os::fd::AsRawFd;

    let file = match std::fs::File::open(path) {
        Ok(f) => f,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e),
    };
    let len = file.metadata()?.len() as usize;
    let mut buf: Vec<u8> = vec![0; len];
    let fd = types::Fd(file.as_raw_fd());
    let mut off = 0usize;
    while off < len {
        let chunk = (len - off).min(u32::MAX as usize);
        let entry = opcode::Read::new(fd, buf[off..].as_mut_ptr(), chunk as u32)
            .offset(off as u64)
            .build();
        let res = submit_one(ring, entry)?;
        if res <= 0 {
            break;
        }
        off += res as usize;
    }
    buf.truncate(off);
    Ok(Some(buf))
}

fn do_write(ring: &mut io_uring::IoUring, path: &Path, data: &[u8]) -> io::Result<()> {
    use io_uring::{opcode, types};
    use std::os::fd::AsRawFd;

    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let file = std::fs::File::create(path)?;
    let fd = types::Fd(file.as_raw_fd());
    let mut off = 0usize;
    while off < data.len() {
        let chunk = (data.len() - off).min(u32::MAX as usize);
        let entry = opcode::Write::new(fd, data[off..].as_ptr(), chunk as u32)
            .offset(off as u64)
            .build();
        let res = submit_one(ring, entry)?;
        if res <= 0 {
            return Err(io::Error::new(io::ErrorKind::WriteZero, "write 0 byte"));
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
    let cqe = ring
        .completion()
        .next()
        .ok_or_else(|| io::Error::new(io::ErrorKind::Other, "tidak ada CQE"))?;
    let res = cqe.result();
    if res < 0 {
        Err(io::Error::from_raw_os_error(-res))
    } else {
        Ok(res)
    }
}
