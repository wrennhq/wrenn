use std::io::Write as _;
use std::path::Path;
use std::sync::Arc;

use axum::body::Body;
use axum::extract::{FromRequest, Query, Request, State};
use axum::http::{StatusCode, header};
use axum::response::{IntoResponse, Response};
use serde::{Deserialize, Serialize};

use crate::auth::signing;
use crate::execcontext;
use crate::http::encoding;
use crate::permissions::path::{ensure_dirs, expand_and_resolve};
use crate::permissions::user::lookup_user;
use crate::state::AppState;

const ACCESS_TOKEN_HEADER: &str = "x-access-token";

#[derive(Deserialize)]
pub struct FileParams {
    pub path: Option<String>,
    pub username: Option<String>,
    pub signature: Option<String>,
    pub signature_expiration: Option<i64>,
}

#[derive(Serialize)]
struct EntryInfo {
    path: String,
    name: String,
    r#type: &'static str,
}

fn json_error(status: StatusCode, msg: &str) -> Response {
    let body = serde_json::json!({ "code": status.as_u16(), "message": msg });
    (status, axum::Json(body)).into_response()
}

fn extract_header_token(req: &Request) -> Option<&str> {
    req.headers()
        .get(ACCESS_TOKEN_HEADER)
        .and_then(|v| v.to_str().ok())
}

fn validate_file_signing(
    state: &AppState,
    header_token: Option<&str>,
    params: &FileParams,
    path: &str,
    operation: &str,
    username: &str,
) -> Result<(), String> {
    signing::validate_signing(
        &state.access_token,
        header_token,
        params.signature.as_deref(),
        params.signature_expiration,
        username,
        path,
        operation,
    )
}

/// GET /files — download a file
pub async fn get_files(
    State(state): State<Arc<AppState>>,
    Query(params): Query<FileParams>,
    req: Request,
) -> Response {
    let path_str = params.path.as_deref().unwrap_or("");
    let header_token = extract_header_token(&req);

    let username = match execcontext::resolve_default_username(
        params.username.as_deref(),
        &state.defaults.user,
    ) {
        Ok(u) => u.to_string(),
        Err(e) => return json_error(StatusCode::BAD_REQUEST, e),
    };

    if let Err(e) = validate_file_signing(
        &state,
        header_token,
        &params,
        path_str,
        signing::READ_OPERATION,
        &username,
    ) {
        return json_error(StatusCode::UNAUTHORIZED, &e);
    }

    let user = match lookup_user(&username) {
        Ok(u) => u,
        Err(e) => return json_error(StatusCode::UNAUTHORIZED, &e),
    };

    let home_dir = format!("/home/{}", user.name);
    let resolved = match expand_and_resolve(path_str, &home_dir, state.defaults.workdir.as_deref())
    {
        Ok(p) => p,
        Err(e) => return json_error(StatusCode::BAD_REQUEST, &e),
    };

    let meta = match std::fs::metadata(&resolved) {
        Ok(m) => m,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return json_error(
                StatusCode::NOT_FOUND,
                &format!("path '{}' does not exist", resolved),
            );
        }
        Err(e) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                &format!("error checking path: {e}"),
            );
        }
    };

    if meta.is_dir() {
        return json_error(
            StatusCode::BAD_REQUEST,
            &format!("path '{}' is a directory", resolved),
        );
    }

    if !meta.file_type().is_file() {
        return json_error(
            StatusCode::BAD_REQUEST,
            &format!("path '{}' is not a regular file", resolved),
        );
    }

    let accept_enc = match encoding::parse_accept_encoding(&req) {
        Ok(e) => e,
        Err(e) => return json_error(StatusCode::NOT_ACCEPTABLE, &e),
    };

    let has_range_or_conditional = req.headers().get("range").is_some()
        || req.headers().get("if-modified-since").is_some()
        || req.headers().get("if-none-match").is_some()
        || req.headers().get("if-range").is_some();

    let use_encoding = if has_range_or_conditional {
        if !encoding::is_identity_acceptable(&req) {
            return json_error(
                StatusCode::NOT_ACCEPTABLE,
                "identity encoding not acceptable for Range or conditional request",
            );
        }
        "identity"
    } else {
        accept_enc
    };

    let file_data = match std::fs::read(&resolved) {
        Ok(d) => d,
        Err(e) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                &format!("error reading file: {e}"),
            );
        }
    };

    let filename = Path::new(&resolved)
        .file_name()
        .map(|n| n.to_string_lossy().to_string())
        .unwrap_or_default();

    let content_disposition = format!("inline; filename=\"{}\"", filename);
    let content_type = mime_guess::from_path(&resolved)
        .first_raw()
        .unwrap_or("application/octet-stream");

    if use_encoding == "gzip" {
        let mut encoder =
            flate2::write::GzEncoder::new(Vec::new(), flate2::Compression::default());
        if let Err(e) = encoder.write_all(&file_data) {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                &format!("gzip encoding error: {e}"),
            );
        }
        let compressed = match encoder.finish() {
            Ok(d) => d,
            Err(e) => {
                return json_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    &format!("gzip finish error: {e}"),
                );
            }
        };

        return Response::builder()
            .status(StatusCode::OK)
            .header(header::CONTENT_TYPE, content_type)
            .header(header::CONTENT_ENCODING, "gzip")
            .header(header::CONTENT_DISPOSITION, content_disposition)
            .header(header::VARY, "Accept-Encoding")
            .body(Body::from(compressed))
            .unwrap();
    }

    Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, content_type)
        .header(header::CONTENT_DISPOSITION, content_disposition)
        .header(header::VARY, "Accept-Encoding")
        .header(header::CONTENT_LENGTH, file_data.len())
        .body(Body::from(file_data))
        .unwrap()
}

/// POST /files — upload file(s) via multipart
pub async fn post_files(
    State(state): State<Arc<AppState>>,
    Query(params): Query<FileParams>,
    req: Request,
) -> Response {
    let path_str = params.path.as_deref().unwrap_or("");
    let header_token = extract_header_token(&req);

    let username = match execcontext::resolve_default_username(
        params.username.as_deref(),
        &state.defaults.user,
    ) {
        Ok(u) => u.to_string(),
        Err(e) => return json_error(StatusCode::BAD_REQUEST, e),
    };

    if let Err(e) = validate_file_signing(
        &state,
        header_token,
        &params,
        path_str,
        signing::WRITE_OPERATION,
        &username,
    ) {
        return json_error(StatusCode::UNAUTHORIZED, &e);
    }

    let user = match lookup_user(&username) {
        Ok(u) => u,
        Err(e) => return json_error(StatusCode::UNAUTHORIZED, &e),
    };

    let home_dir = format!("/home/{}", user.name);
    let uid = user.uid;
    let gid = user.gid;

    let content_enc = match encoding::parse_content_encoding(&req) {
        Ok(e) => e,
        Err(e) => return json_error(StatusCode::BAD_REQUEST, &e),
    };

    let mut multipart = match axum::extract::Multipart::from_request(req, &()).await {
        Ok(m) => m,
        Err(e) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                &format!("error parsing multipart: {e}"),
            );
        }
    };

    let mut uploaded: Vec<EntryInfo> = Vec::new();

    while let Ok(Some(field)) = multipart.next_field().await {
        let field_name = field.name().unwrap_or("").to_string();
        if field_name != "file" {
            continue;
        }

        let file_path = if !path_str.is_empty() {
            match expand_and_resolve(path_str, &home_dir, state.defaults.workdir.as_deref()) {
                Ok(p) => p,
                Err(e) => return json_error(StatusCode::BAD_REQUEST, &e),
            }
        } else {
            let fname = field
                .file_name()
                .unwrap_or("upload")
                .to_string();
            match expand_and_resolve(&fname, &home_dir, state.defaults.workdir.as_deref()) {
                Ok(p) => p,
                Err(e) => return json_error(StatusCode::BAD_REQUEST, &e),
            }
        };

        if uploaded.iter().any(|e| e.path == file_path) {
            return json_error(
                StatusCode::BAD_REQUEST,
                &format!("cannot upload multiple files to same path '{}'", file_path),
            );
        }

        let raw_bytes = match field.bytes().await {
            Ok(b) => b,
            Err(e) => {
                return json_error(
                    StatusCode::INTERNAL_SERVER_ERROR,
                    &format!("error reading field: {e}"),
                );
            }
        };

        let data = if content_enc == "gzip" {
            use std::io::Read;
            let mut decoder = flate2::read::GzDecoder::new(&raw_bytes[..]);
            let mut buf = Vec::new();
            match decoder.read_to_end(&mut buf) {
                Ok(_) => buf,
                Err(e) => {
                    return json_error(
                        StatusCode::BAD_REQUEST,
                        &format!("gzip decompression failed: {e}"),
                    );
                }
            }
        } else {
            raw_bytes.to_vec()
        };

        if let Err(e) = process_file(&file_path, &data, uid, gid) {
            let (status, msg) = e;
            return json_error(status, &msg);
        }

        let name = Path::new(&file_path)
            .file_name()
            .map(|n| n.to_string_lossy().to_string())
            .unwrap_or_default();

        uploaded.push(EntryInfo {
            path: file_path,
            name,
            r#type: "file",
        });
    }

    axum::Json(uploaded).into_response()
}

fn process_file(
    path: &str,
    data: &[u8],
    uid: nix::unistd::Uid,
    gid: nix::unistd::Gid,
) -> Result<(), (StatusCode, String)> {
    let dir = Path::new(path)
        .parent()
        .map(|p| p.to_string_lossy().to_string())
        .unwrap_or_default();

    if !dir.is_empty() {
        ensure_dirs(&dir, uid, gid).map_err(|e| {
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                format!("error ensuring directories: {e}"),
            )
        })?;
    }

    let can_pre_chown = match std::fs::metadata(path) {
        Ok(meta) => {
            if meta.is_dir() {
                return Err((
                    StatusCode::BAD_REQUEST,
                    format!("path is a directory: {path}"),
                ));
            }
            true
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => false,
        Err(e) => {
            return Err((
                StatusCode::INTERNAL_SERVER_ERROR,
                format!("error getting file info: {e}"),
            ))
        }
    };

    let mut chowned = false;
    if can_pre_chown {
        match std::os::unix::fs::chown(path, Some(uid.as_raw()), Some(gid.as_raw())) {
            Ok(()) => chowned = true,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => {
                return Err((
                    StatusCode::INTERNAL_SERVER_ERROR,
                    format!("error changing ownership: {e}"),
                ))
            }
        }
    }

    let mut file = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o666)
        .open(path)
        .map_err(|e| {
            if e.raw_os_error() == Some(libc::ENOSPC) {
                return (
                    StatusCode::INSUFFICIENT_STORAGE,
                    "not enough disk space available".to_string(),
                );
            }
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                format!("error opening file: {e}"),
            )
        })?;

    if !chowned {
        std::os::unix::fs::chown(path, Some(uid.as_raw()), Some(gid.as_raw())).map_err(|e| {
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                format!("error changing ownership: {e}"),
            )
        })?;
    }

    file.write_all(data).map_err(|e| {
        if e.raw_os_error() == Some(libc::ENOSPC) {
            return (
                StatusCode::INSUFFICIENT_STORAGE,
                "not enough disk space available".to_string(),
            );
        }
        (
            StatusCode::INTERNAL_SERVER_ERROR,
            format!("error writing file: {e}"),
        )
    })?;

    Ok(())
}

use std::os::unix::fs::OpenOptionsExt;
