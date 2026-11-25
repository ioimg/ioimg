//! libvips dari Rust lewat FFI langsung (tanpa binding crate), supaya
//! perbandingan dengan govips (Go) memakai pustaka C yang sama persis.
//!
//! Tanda tangan asli (lihat /usr/include/vips/resample.h & foreign.h):
//!   int vips_thumbnail_buffer(void *buf, size_t len, VipsImage **out, int width, ...);
//!   int vips_jpegsave_buffer(VipsImage *in, void **buf, size_t *len, ...);
//! Opsi diteruskan sebagai varargs diakhiri NULL.

use libloading::{Library, Symbol};
use std::ffi::{CStr, CString, c_char, c_int, c_void};
use std::ptr;
use std::time::Instant;

type VipsInit = unsafe extern "C" fn(*const c_char) -> c_int;
type VipsThumbBuf = unsafe extern "C" fn(*mut c_void, usize, *mut *mut c_void, c_int, ...) -> c_int;
type VipsSaveBuf = unsafe extern "C" fn(*mut c_void, *mut *mut c_void, *mut usize, ...) -> c_int;
type GObjectUnref = unsafe extern "C" fn(*mut c_void);
type VipsErrBuf = unsafe extern "C" fn() -> *const c_char;
type GFree = unsafe extern "C" fn(*mut c_void);

struct Vips {
    thumb: VipsThumbBuf,
    save: VipsSaveBuf,
    unref: GObjectUnref,
    err: VipsErrBuf,
    g_free: GFree,
    _lib: Library,
    _glib: Library,
}

impl Vips {
    fn new() -> Self {
        unsafe {
            let lib = Library::new("libvips.so.42").expect("libvips.so.42");
            let glib = Library::new("libglib-2.0.so.0").expect("libglib");
            let gobj = Library::new("libgobject-2.0.so.0").expect("libgobject");
            let init: Symbol<VipsInit> = lib.get(b"vips_init").unwrap();
            let name = CString::new("ioimg-poc").unwrap();
            let rc = init(name.as_ptr());
            if rc != 0 {
                let e: Symbol<VipsErrBuf> = lib.get(b"vips_error_buffer").unwrap();
                panic!("vips_init gagal: {}", CStr::from_ptr(e()).to_string_lossy());
            }
            Self {
                thumb: *lib.get(b"vips_thumbnail_buffer").unwrap(),
                save: *lib.get(b"vips_jpegsave_buffer").unwrap(),
                unref: *gobj.get(b"g_object_unref").unwrap(),
                err: *lib.get(b"vips_error_buffer").unwrap(),
                g_free: *glib.get(b"g_free").unwrap(),
                _lib: lib,
                _glib: glib,
            }
        }
    }

    fn last_error(&self) -> String {
        unsafe { CStr::from_ptr((self.err)()) }.to_string_lossy().to_string()
    }

    fn transform(&self, src: &[u8], width: i32, height: i32, quality: i32) -> Vec<u8> {
        unsafe {
            let mut img: *mut c_void = ptr::null_mut();
            let k_height = CString::new("height").unwrap();
            let rc = (self.thumb)(
                src.as_ptr() as *mut c_void,
                src.len(),
                &mut img,
                width,
                k_height.as_ptr(),
                height,
                ptr::null::<c_char>(),
            );
            if rc != 0 || img.is_null() {
                panic!("thumbnail gagal: {}", self.last_error());
            }
            let mut out: *mut c_void = ptr::null_mut();
            let mut len: usize = 0;
            let k_q = CString::new("Q").unwrap();
            let k_strip = CString::new("strip").unwrap();
            let rc = (self.save)(
                img,
                &mut out,
                &mut len,
                k_q.as_ptr(),
                quality,
                k_strip.as_ptr(),
                1i32,
                ptr::null::<c_char>(),
            );
            (self.unref)(img);
            if rc != 0 {
                panic!("jpegsave gagal: {}", self.last_error());
            }
            let v = std::slice::from_raw_parts(out as *const u8, len).to_vec();
            (self.g_free)(out);
            v
        }
    }
}

fn main() {
    let path = std::env::args().nth(1).unwrap_or_else(|| "../../artifacts/datasets/src/photo00.jpg".into());
    let src = std::fs::read(&path).expect("baca sumber");
    let vips = Vips::new();
    println!("sumber: {path} ({} KiB) — libvips via FFI", src.len() / 1024);

    let cases: [(&str, i32, i32, i32); 3] = [
        ("vips w400 jpeg q80", 400, 267, 80),
        ("vips w800 jpeg q80", 800, 533, 80),
        ("vips w1600 jpeg q85", 1600, 1067, 85),
    ];
    for (label, w, h, q) in cases {
        let _ = vips.transform(&src, w, h, q);
        let n = 20;
        let t = Instant::now();
        let mut out = Vec::new();
        for _ in 0..n {
            out = vips.transform(&src, w, h, q);
        }
        let ms = t.elapsed().as_secs_f64() * 1000.0 / n as f64;
        println!(
            "{label:<30} {ms:7.2} ms  out={:>6} KiB  {:6.1} img/s/core",
            out.len() / 1024,
            1000.0 / ms
        );
    }
}
