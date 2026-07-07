use std::sync::Arc;

use axum::extract::State;
use axum::http::{StatusCode, header};
use axum::response::IntoResponse;
use nix::unistd::sync;

use crate::state::AppState;

/// POST /snapshot/prepare — called by the host agent immediately before it
/// invokes vm.pause + vm.snapshot. The handler quiesces guest state so the
/// resulting snapshot is clean: outstanding writes are flushed to disk, the
/// VFS page cache is dropped (so the dm-snapshot CoW is the source of truth),
/// and the port forwarder is stopped to prevent socat children from being
/// frozen mid-handshake.
pub async fn post_snapshot_prepare(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    // Stop port forwarder + scanner so no socat process is captured in the
    // snapshot with a half-open TCP connection. /init on resume restarts it.
    if let Some(ref port_sub) = state.port_subsystem {
        port_sub.stop();
    }

    // Flush in-memory FS state, then drop the VFS page cache +
    // dentries/inodes. sync first so the pages we drop are clean; dropping
    // reduces snapshot size by ensuring CH only persists memory pages the
    // guest actually needs.
    flush_and_drop_caches("first pass").await;

    // Best-effort fstrim on the rootfs so unused blocks are returned to the
    // dm-snapshot, keeping CoW size minimal.
    let _ = tokio::process::Command::new("fstrim")
        .arg("/")
        .output()
        .await;

    // Second pass after fstrim: fstrim re-reads superblock / group descriptor
    // pages that we just evicted, putting them back in the page cache. This
    // drops those and any other late readers (e.g. sync flushers).
    flush_and_drop_caches("second pass").await;

    // No balloon settle window here: free-page reporting drains asynchronously,
    // so any pages not yet hole-punched by the host at snapshot time are written
    // verbatim — but with init_on_free=1 the guest zeroes them on free, and the
    // host-side background zero-page punch reclaims them off the pause critical
    // path. Trading a fixed ~1s of pause latency for a slightly larger artifact
    // that the async punch later shrinks anyway.

    tracing::info!("snapshot/prepare: quiesced");
    (
        StatusCode::NO_CONTENT,
        [(header::CACHE_CONTROL, "no-store")],
    )
}

// sync(2) can block for seconds flushing dirty pages, and the drop_caches
// write blocks while the kernel evicts — both run on the blocking pool or
// they starve every async task sharing the worker thread (health probes,
// exec streams) for the whole quiesce window.
async fn flush_and_drop_caches(pass: &'static str) {
    let _ = tokio::task::spawn_blocking(move || {
        sync();
        if let Err(e) = std::fs::write("/proc/sys/vm/drop_caches", "3") {
            tracing::warn!(error = %e, pass, "drop_caches failed (continuing)");
        }
    })
    .await;
}
