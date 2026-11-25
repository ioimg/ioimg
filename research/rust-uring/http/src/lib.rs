//! Satu router, satu handler, dipakai dua runtime.

use std::path::PathBuf;
use std::sync::Arc;
use std::time::Instant;

use axum::extract::{Path, State};
use axum::http::{header, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::get;
use axum::Router;

use poc_core::{cache_path, run_blocking, Config, Ops, SharedStats, Stats};
use poc_io::CacheIo;

#[derive(Clone)]
pub struct AppState<B: CacheIo> {
    pub cfg: Arc<Config>,
    pub io: B,
    pub stats: SharedStats,
}

impl<B: CacheIo> AppState<B> {
    pub fn new(cfg: Config, io: B) -> Self {
        Self { cfg: Arc::new(cfg), io, stats: Arc::new(Stats::default()) }
    }
}

pub fn router<B: CacheIo>(state: AppState<B>) -> Router {
    Router::new()
        .route("/healthz", get(|| async { "ok" }))
        .route("/metrics", get(metrics::<B>))
        .route("/{ops}/{name}", get(handle::<B>))
        .with_state(state)
}

async fn metrics<B: CacheIo>(State(st): State<AppState<B>>) -> String {
    st.stats.render()
}

async fn handle<B: CacheIo>(
    State(st): State<AppState<B>>,
    Path((ops_raw, name)): Path<(String, String)>,
) -> Response {
    st.stats.add(|s| &s.requests, 1);

    if name.contains("..") || name.contains('/') {
        st.stats.add(|s| &s.errors, 1);
        return (StatusCode::BAD_REQUEST, "nama sumber tidak valid").into_response();
    }

    let ops = match Ops::parse(&ops_raw) {
        Ok(o) => o,
        Err(e) => {
            st.stats.add(|s| &s.errors, 1);
            return (StatusCode::BAD_REQUEST, e.to_string()).into_response();
        }
    };

    let key = poc_core::cache_key(&ops, &name);
    let out_path = cache_path(&st.cfg.cache_dir, &key, ops.format);

    // --- cache lookup
    let t0 = Instant::now();
    let hit = if st.cfg.nocache {
        Ok(None)
    } else {
        st.io.read(&out_path).await
    };
    st.stats.add(|s| &s.cache_read_ns, t0.elapsed().as_nanos() as u64);

    match hit {
        Ok(Some(bytes)) => {
            st.stats.add(|s| &s.hits, 1);
            st.stats.add(|s| &s.bytes_out, bytes.len() as u64);
            return respond(ops.format.content_type(), "HIT", bytes);
        }
        Ok(None) => {}
        Err(e) => {
            st.stats.add(|s| &s.errors, 1);
            return (StatusCode::INTERNAL_SERVER_ERROR, format!("cache read: {e}")).into_response();
        }
    }

    // --- miss: baca sumber, transform, tulis cache
    st.stats.add(|s| &s.misses, 1);
    let src_path: PathBuf = st.cfg.src_dir.join(&name);
    let src = match st.io.read(&src_path).await {
        Ok(Some(v)) => v,
        Ok(None) => return (StatusCode::NOT_FOUND, "sumber tidak ada").into_response(),
        Err(e) => {
            st.stats.add(|s| &s.errors, 1);
            return (StatusCode::INTERNAL_SERVER_ERROR, format!("src read: {e}")).into_response();
        }
    };

    let ops2 = ops.clone();
    let t1 = Instant::now();
    let encoded = match run_blocking(move || poc_core::transform(&src, &ops2)).await {
        Ok(v) => v,
        Err(e) => {
            st.stats.add(|s| &s.errors, 1);
            return (StatusCode::UNPROCESSABLE_ENTITY, format!("transform: {e:#}")).into_response();
        }
    };
    st.stats.add(|s| &s.transform_ns, t1.elapsed().as_nanos() as u64);

    let len = encoded.len();
    if !st.cfg.nocache {
    if let Err(e) = st.io.write(&out_path, encoded.clone()).await {
        // cache write gagal bukan alasan menggagalkan request
        eprintln!("warn: cache write {out_path:?}: {e}");
    }
    }

    st.stats.add(|s| &s.bytes_out, len as u64);
    respond(ops.format.content_type(), "MISS", encoded)
}

fn respond(ct: &'static str, cache: &'static str, body: Vec<u8>) -> Response {
    (
        [
            (header::CONTENT_TYPE, ct),
            (header::CACHE_CONTROL, "public, max-age=31536000, immutable"),
            (header::HeaderName::from_static("x-ioimg-cache"), cache),
            (header::VARY, "Accept"),
        ],
        body,
    )
        .into_response()
}

// ------------------------------------------------------------- servers

#[cfg(feature = "tokio-serve")]
pub async fn serve_tokio<B: CacheIo>(state: AppState<B>) -> anyhow::Result<()> {
    let port = state.cfg.port;
    let app = router(state);
    let listener = tokio::net::TcpListener::bind(("0.0.0.0", port)).await?;
    println!("[tokio] listening on :{port}");
    axum::serve(listener, app).await?;
    Ok(())
}

#[cfg(feature = "compio-serve")]
pub async fn serve_compio<B: CacheIo>(state: AppState<B>) -> anyhow::Result<()> {
    let port = state.cfg.port;
    let app = router(state);
    let listener = compio::net::TcpListener::bind(("0.0.0.0", port)).await?;
    println!("[compio] listening on :{port}");
    cyper_axum::serve(listener, app).await?;
    Ok(())
}
