use std::sync::Arc;

use axum::Json;
use axum::extract::State;
use axum::http::header;
use axum::response::IntoResponse;
use serde_json::json;

use crate::state::AppState;

pub async fn get_health(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    state.try_restore_recovery();

    tracing::trace!("health check");

    (
        [(header::CACHE_CONTROL, "no-store")],
        Json(json!({ "version": state.version })),
    )
}
