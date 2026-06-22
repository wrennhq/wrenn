use std::os::unix::fs::OpenOptionsExt;
use std::path::Path;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

use axum::body::Body;
use axum::extract::{Query, Request, State};
use axum::http::{StatusCode, header};
use axum::response::{IntoResponse, Response};
use futures::StreamExt;
use serde::{Deserialize, Serialize};
use tokio::io::AsyncWriteExt;
use tokio_util::io::ReaderStream;

use crate::auth::signing;
use crate::execcontext;
use crate::permissions::path::{ensure_dirs, expand_and_resolve};
use crate::permissions::user::lookup_user;
use crate::state::AppState;

const ACCESS_TOKEN_HEADER: &str = "x-access-token";

/// Monotonic counter for unique temp-file names within this process. Combined
/// with the pid it guarantees concurrent uploads never collide on the staging
/// path before the atomic rename.
static UPLOAD_SEQ: AtomicU64 = AtomicU64::new(0);

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

/// GET /files — download a file, streamed from disk.
///
/// The body is streamed straight off the filesystem so large files never get
/// buffered into memory. Identity encoding only — no gzip, no range support.
pub async fn get_files(
    State(state): State<Arc<AppState>>,
    Query(params): Query<FileParams>,
    req: Request,
) -> Response {
    let path_str = params.path.as_deref().unwrap_or("");
    let header_token = extract_header_token(&req);

    let default_user = state.defaults.user();
    let username =
        match execcontext::resolve_default_username(params.username.as_deref(), &default_user) {
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

    let home_dir = user.dir.to_string_lossy().to_string();
    let default_workdir = state.defaults.workdir();
    let resolved = match expand_and_resolve(path_str, &home_dir, default_workdir.as_deref()) {
        Ok(p) => p,
        Err(e) => return json_error(StatusCode::BAD_REQUEST, &e),
    };

    let meta = match tokio::fs::metadata(&resolved).await {
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

    let file = match tokio::fs::File::open(&resolved).await {
        Ok(f) => f,
        Err(e) => {
            return json_error(
                StatusCode::INTERNAL_SERVER_ERROR,
                &format!("error opening file: {e}"),
            );
        }
    };

    let filename = Path::new(&resolved)
        .file_name()
        .map(|n| n.to_string_lossy().to_string())
        .unwrap_or_default();
    let content_disposition = format!("inline; filename=\"{}\"", filename);

    let body = Body::from_stream(ReaderStream::new(file));

    Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "application/octet-stream")
        .header(header::CONTENT_DISPOSITION, content_disposition)
        .header(header::CONTENT_LENGTH, meta.len())
        .body(body)
        .unwrap()
}

/// PUT /files — upload a single file, streamed to disk.
///
/// The request body is the raw file content (no multipart, no encoding). It is
/// streamed to a temporary staging file in the destination directory, then
/// atomically renamed into place — concurrent writers to the same path never
/// observe a torn file, and the last rename wins.
pub async fn put_files(
    State(state): State<Arc<AppState>>,
    Query(params): Query<FileParams>,
    req: Request,
) -> Response {
    let path_str = params.path.as_deref().unwrap_or("");
    if path_str.is_empty() {
        return json_error(StatusCode::BAD_REQUEST, "missing required 'path' parameter");
    }
    let header_token = extract_header_token(&req);

    let default_user = state.defaults.user();
    let username =
        match execcontext::resolve_default_username(params.username.as_deref(), &default_user) {
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

    let home_dir = user.dir.to_string_lossy().to_string();
    let uid = user.uid;
    let gid = user.gid;
    let default_workdir = state.defaults.workdir();

    let file_path = match expand_and_resolve(path_str, &home_dir, default_workdir.as_deref()) {
        Ok(p) => p,
        Err(e) => return json_error(StatusCode::BAD_REQUEST, &e),
    };

    if let Err((status, msg)) = stream_to_file(req.into_body(), &file_path, uid, gid).await {
        return json_error(status, &msg);
    }

    let name = Path::new(&file_path)
        .file_name()
        .map(|n| n.to_string_lossy().to_string())
        .unwrap_or_default();

    axum::Json(EntryInfo {
        path: file_path,
        name,
        r#type: "file",
    })
    .into_response()
}

/// Stream a request body to `path` via a temp file + atomic rename. The staging
/// file is created with mode 0o666 and chowned to (uid, gid) before the rename,
/// so the destination appears atomically with the correct owner.
async fn stream_to_file(
    body: Body,
    path: &str,
    uid: nix::unistd::Uid,
    gid: nix::unistd::Gid,
) -> Result<(), (StatusCode, String)> {
    let target = Path::new(path);

    let dir = target
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

    // Reject writing over an existing directory before staging anything.
    match std::fs::metadata(path) {
        Ok(meta) if meta.is_dir() => {
            return Err((
                StatusCode::BAD_REQUEST,
                format!("path is a directory: {path}"),
            ));
        }
        Ok(_) => {}
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => {
            return Err((
                StatusCode::INTERNAL_SERVER_ERROR,
                format!("error getting file info: {e}"),
            ));
        }
    }

    // Stage in the destination directory so the rename stays on one filesystem.
    let seq = UPLOAD_SEQ.fetch_add(1, Ordering::Relaxed);
    let tmp_name = format!(".envd-upload.{}.{}", std::process::id(), seq);
    let tmp_path = if dir.is_empty() {
        tmp_name
    } else {
        format!("{dir}/{tmp_name}")
    };

    let map_open_err = |e: std::io::Error| {
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
    };

    let std_file = std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o666)
        .open(&tmp_path)
        .map_err(map_open_err)?;

    // From here on, any failure must clean up the staging file.
    let result = write_body_to_tmp(body, std_file, &tmp_path, uid, gid).await;
    if result.is_err() {
        let _ = std::fs::remove_file(&tmp_path);
        return result.map(|_| ());
    }

    std::fs::rename(&tmp_path, path).map_err(|e| {
        let _ = std::fs::remove_file(&tmp_path);
        (
            StatusCode::INTERNAL_SERVER_ERROR,
            format!("error finalizing file: {e}"),
        )
    })?;

    Ok(())
}

async fn write_body_to_tmp(
    body: Body,
    std_file: std::fs::File,
    tmp_path: &str,
    uid: nix::unistd::Uid,
    gid: nix::unistd::Gid,
) -> Result<(), (StatusCode, String)> {
    let mut file = tokio::fs::File::from_std(std_file);

    let mut stream = body.into_data_stream();
    while let Some(chunk) = stream.next().await {
        let bytes = chunk.map_err(|e| {
            (
                StatusCode::BAD_REQUEST,
                format!("error reading request body: {e}"),
            )
        })?;
        file.write_all(&bytes).await.map_err(|e| {
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
    }

    file.flush().await.map_err(|e| {
        (
            StatusCode::INTERNAL_SERVER_ERROR,
            format!("error flushing file: {e}"),
        )
    })?;

    // chown the staging file so it lands at the destination already owned by
    // the target user.
    std::os::unix::fs::chown(tmp_path, Some(uid.as_raw()), Some(gid.as_raw())).map_err(|e| {
        (
            StatusCode::INTERNAL_SERVER_ERROR,
            format!("error changing ownership: {e}"),
        )
    })?;

    Ok(())
}
