use axum::Json;
use axum::http::StatusCode;
use axum::response::IntoResponse;
use serde::Serialize;

#[derive(Serialize)]
struct ErrorBody {
    code: u16,
    message: String,
}

pub fn json_error(status: StatusCode, message: &str) -> impl IntoResponse {
    (
        status,
        Json(ErrorBody {
            code: status.as_u16(),
            message: message.to_string(),
        }),
    )
}
