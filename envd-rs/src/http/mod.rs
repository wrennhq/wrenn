pub mod encoding;
pub mod envs;
pub mod error;
pub mod files;
pub mod health;
pub mod init;
pub mod memory;
pub mod metrics;
pub mod snapshot;

use std::sync::Arc;
use std::time::Duration;

use axum::Router;
use axum::routing::{get, post};
use http::Method;
use http::header::{CACHE_CONTROL, HeaderName};
use tower_http::cors::{AllowHeaders, AllowMethods, AllowOrigin, CorsLayer};

use crate::config::CORS_MAX_AGE;
use crate::state::AppState;

pub fn router(state: Arc<AppState>) -> Router {
    let cors = CorsLayer::new()
        .allow_origin(AllowOrigin::any())
        .allow_methods(AllowMethods::list([
            Method::HEAD,
            Method::GET,
            Method::POST,
            Method::PUT,
            Method::PATCH,
            Method::DELETE,
        ]))
        .allow_headers(AllowHeaders::any())
        .expose_headers([
            HeaderName::from_static("location"),
            CACHE_CONTROL,
            HeaderName::from_static("x-content-type-options"),
            HeaderName::from_static("connect-content-encoding"),
            HeaderName::from_static("connect-protocol-version"),
            HeaderName::from_static("grpc-encoding"),
            HeaderName::from_static("grpc-message"),
            HeaderName::from_static("grpc-status"),
            HeaderName::from_static("grpc-status-details-bin"),
        ])
        .max_age(Duration::from_secs(CORS_MAX_AGE.as_secs()));

    Router::new()
        .route("/health", get(health::get_health))
        .route("/metrics", get(metrics::get_metrics))
        .route("/envs", get(envs::get_envs))
        .route("/init", post(init::post_init))
        .route("/snapshot/prepare", post(snapshot::post_snapshot_prepare))
        .route(
            "/memory/preload",
            get(memory::get_memory_preload).post(memory::post_memory_preload),
        )
        .route(
            "/memory/preload/cancel",
            post(memory::post_memory_preload_cancel),
        )
        .route("/files", get(files::get_files).post(files::post_files))
        .layer(cors)
        .with_state(state)
}
