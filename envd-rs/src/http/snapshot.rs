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

    // sync(2) flushes the in-memory FS state. Done before drop_caches so the
    // pages we drop are clean.
    sync();

    // Drop the VFS page cache + dentries/inodes. Reduces snapshot size by
    // ensuring CH only persists memory pages that the guest actually needs.
    if let Err(e) = std::fs::write("/proc/sys/vm/drop_caches", "3") {
        tracing::warn!(error = %e, "drop_caches (first pass) failed (continuing)");
    }

    // Best-effort fstrim on the rootfs so unused blocks are returned to the
    // dm-snapshot, keeping CoW size minimal.
    let _ = tokio::process::Command::new("fstrim")
        .arg("/")
        .output()
        .await;

    // Second drop_caches pass after fstrim: fstrim re-reads superblock /
    // group descriptor pages that we just evicted, putting them back in the
    // page cache. A second pass drops those and any other late readers (e.g.
    // sync flushers).
    sync();
    if let Err(e) = std::fs::write("/proc/sys/vm/drop_caches", "3") {
        tracing::warn!(error = %e, "drop_caches (second pass) failed (continuing)");
    }

    // Free-page reporting drains asynchronously: the balloon driver hands
    // freed pages to the host in batches and CH punches holes in the backing
    // memfile. Without a brief settle window most of the pages freed by the
    // drop_caches passes above would still be present in the snapshot.
    tokio::time::sleep(std::time::Duration::from_secs(1)).await;

    tracing::info!("snapshot/prepare: quiesced");
    (
        StatusCode::NO_CONTENT,
        [(header::CACHE_CONTROL, "no-store")],
    )
}
