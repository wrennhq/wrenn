use std::collections::HashMap;
use std::sync::Arc;

use axum::Json;
use axum::extract::State;
use axum::http::header;
use axum::response::IntoResponse;

use crate::state::AppState;

pub async fn get_envs(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    tracing::debug!("getting env vars");

    let envs: HashMap<String, String> = state
        .defaults
        .env_vars
        .iter()
        .map(|entry| (entry.key().clone(), entry.value().clone()))
        .collect();

    (
        [(header::CACHE_CONTROL, "no-store")],
        Json(envs),
    )
}
