use std::sync::Arc;
use std::sync::atomic::Ordering;

use axum::extract::State;
use axum::http::{StatusCode, header};
use axum::response::IntoResponse;

use crate::state::AppState;

/// POST /snapshot/prepare — quiesce subsystems before Firecracker snapshot.
///
/// In Rust there is no GC dance. We just:
/// 1. Stop port subsystem
/// 2. Close idle connections via conntracker
/// 3. Set needs_restore flag
pub async fn post_snapshot_prepare(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    // Block memory reclaimer before anything else — prevents drop_caches
    // from running mid-freeze which would corrupt kernel page table state.
    state.snapshot_in_progress.store(true, Ordering::Release);

    if let Some(ref ps) = state.port_subsystem {
        ps.stop();
        tracing::info!("snapshot/prepare: port subsystem stopped");
    }

    state.conn_tracker.prepare_for_snapshot();
    tracing::info!("snapshot/prepare: connections prepared");

    // Sync filesystem buffers so dirty pages are flushed before freeze.
    unsafe { libc::sync(); }

    state.needs_restore.store(true, Ordering::Release);
    tracing::info!("snapshot/prepare: ready for freeze");

    (
        StatusCode::NO_CONTENT,
        [(header::CACHE_CONTROL, "no-store")],
    )
}
