use std::sync::Arc;
use std::sync::atomic::Ordering;

use axum::Json;
use axum::extract::State;
use axum::http::header;
use axum::response::IntoResponse;
use serde_json::json;

use crate::state::AppState;

pub async fn get_health(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    if state
        .needs_restore
        .compare_exchange(true, false, Ordering::AcqRel, Ordering::Relaxed)
        .is_ok()
    {
        post_restore_recovery(&state);
    }

    tracing::trace!("health check");

    (
        [(header::CACHE_CONTROL, "no-store")],
        Json(json!({ "version": state.version })),
    )
}

fn post_restore_recovery(state: &AppState) {
    tracing::info!("restore: post-restore recovery (no GC needed in Rust)");

    state.conn_tracker.restore_after_snapshot();
    tracing::info!("restore: zombie connections closed");

    if let Some(ref ps) = state.port_subsystem {
        ps.restart();
        tracing::info!("restore: port subsystem restarted");
    }
}
