use std::sync::Arc;

use axum::extract::Request;
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use serde_json::json;

use crate::auth::token::SecureToken;

const ACCESS_TOKEN_HEADER: &str = "x-access-token";

/// Paths excluded from general token auth.
/// Format: "METHOD/path"
const AUTH_EXCLUDED: &[&str] = &[
    "GET/health",
    "GET/files",
    "POST/files",
    "POST/init",
    "POST/snapshot/prepare",
];

/// Axum middleware that checks X-Access-Token header.
pub async fn auth_layer(
    request: Request,
    next: Next,
    access_token: Arc<SecureToken>,
) -> Response {
    if access_token.is_set() {
        let method = request.method().as_str();
        let path = request.uri().path();
        let key = format!("{method}{path}");

        let is_excluded = AUTH_EXCLUDED.iter().any(|p| *p == key);

        let header_val = request
            .headers()
            .get(ACCESS_TOKEN_HEADER)
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");

        if !access_token.equals(header_val) && !is_excluded {
            tracing::error!("unauthorized access attempt");
            return (
                StatusCode::UNAUTHORIZED,
                axum::Json(json!({
                    "code": 401,
                    "message": "unauthorized access, please provide a valid access token or method signing if supported"
                })),
            )
                .into_response();
        }
    }

    next.run(request).await
}
