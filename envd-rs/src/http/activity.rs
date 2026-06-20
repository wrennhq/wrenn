use std::sync::Arc;

use axum::Json;
use axum::extract::State;
use axum::http::header;
use axum::response::IntoResponse;
use serde::Serialize;

use crate::state::AppState;

/// Liveness snapshot the host activity sampler polls to decide whether a
/// sandbox is doing real work. All fields are served straight from atomics
/// updated by the 1s sampler thread — no syscalls per request, so the host
/// can poll cheaply at a few-second cadence.
#[derive(Serialize)]
pub struct Activity {
    cpu_count: u32,
    cpu_used_pct: f32,
    net_bps: u64,
    disk_bps: u64,
}

pub async fn get_activity(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    tracing::trace!("get activity");

    let body = Activity {
        cpu_count: state.cpu_count(),
        cpu_used_pct: state.cpu_used_pct(),
        net_bps: state.net_bps(),
        disk_bps: state.disk_bps(),
    };

    (
        [(header::CACHE_CONTROL, "no-store")],
        Json(body),
    )
}
