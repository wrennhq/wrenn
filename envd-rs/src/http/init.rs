use std::collections::HashMap;
use std::sync::Arc;

use axum::Json;
use axum::extract::State;
use axum::http::{StatusCode, header};
use axum::response::IntoResponse;
use serde::Deserialize;

use crate::state::AppState;

#[derive(Deserialize, Default)]
pub struct InitRequest {
    #[serde(rename = "access_token")]
    pub access_token: Option<String>,
    #[serde(rename = "defaultUser")]
    pub default_user: Option<String>,
    #[serde(rename = "defaultWorkdir")]
    pub default_workdir: Option<String>,
    #[serde(rename = "envVars")]
    pub env_vars: Option<HashMap<String, String>>,
    #[serde(rename = "hyperloop_ip")]
    pub hyperloop_ip: Option<String>,
    pub timestamp: Option<String>,
    #[serde(rename = "volume_mounts")]
    pub volume_mounts: Option<Vec<VolumeMount>>,
    pub sandbox_id: Option<String>,
    pub template_id: Option<String>,
}

#[derive(Deserialize)]
pub struct VolumeMount {
    pub nfs_target: String,
    pub path: String,
}

/// POST /init — called by host agent after boot and after every resume.
pub async fn post_init(
    State(state): State<Arc<AppState>>,
    body: Option<Json<InitRequest>>,
) -> impl IntoResponse {
    let init_req = body.map(|b| b.0).unwrap_or_default();

    // Validate access token if provided
    if let Some(ref token_str) = init_req.access_token {
        if let Err(e) = validate_init_access_token(&state, token_str).await {
            tracing::error!(error = %e, "init: access token validation failed");
            return (StatusCode::UNAUTHORIZED, e).into_response();
        }
    }

    // Idempotent timestamp check
    if let Some(ref ts_str) = init_req.timestamp {
        if let Ok(ts) = chrono_parse_to_nanos(ts_str) {
            if !state.last_set_time.set_to_greater(ts) {
                // Stale request, skip data updates
                return trigger_restore_and_respond(&state).await;
            }
        }
    }

    // Apply env vars
    if let Some(ref vars) = init_req.env_vars {
        tracing::debug!(count = vars.len(), "setting env vars");
        for (k, v) in vars {
            state.defaults.env_vars.insert(k.clone(), v.clone());
        }
    }

    // Set access token
    if let Some(ref token_str) = init_req.access_token {
        if !token_str.is_empty() {
            tracing::debug!("setting access token");
            let _ = state.access_token.set(token_str.as_bytes());
        } else if state.access_token.is_set() {
            tracing::debug!("clearing access token");
            state.access_token.destroy();
        }
    }

    // Set default user
    if let Some(ref user) = init_req.default_user {
        if !user.is_empty() {
            tracing::debug!(user = %user, "setting default user");
            state.defaults.set_user(user.clone());
        }
    }

    // Set default workdir
    if let Some(ref workdir) = init_req.default_workdir {
        if !workdir.is_empty() {
            tracing::debug!(workdir = %workdir, "setting default workdir");
            state.defaults.set_workdir(Some(workdir.clone()));
        }
    }

    // Hyperloop /etc/hosts setup
    if let Some(ref ip) = init_req.hyperloop_ip {
        let ip = ip.clone();
        let env_vars = Arc::clone(&state.defaults.env_vars);
        tokio::spawn(async move {
            setup_hyperloop(&ip, &env_vars).await;
        });
    }

    // NFS mounts
    if let Some(ref mounts) = init_req.volume_mounts {
        for mount in mounts {
            let target = mount.nfs_target.clone();
            let path = mount.path.clone();
            tokio::spawn(async move {
                setup_nfs(&target, &path).await;
            });
        }
    }

    // Set sandbox/template metadata from request body.
    if let Some(ref id) = init_req.sandbox_id {
        tracing::debug!(sandbox_id = %id, "setting sandbox ID from init request");
        // SAFETY: envd is single-threaded at init time; no concurrent env reads.
        unsafe { std::env::set_var("WRENN_SANDBOX_ID", id) };
        write_run_file(".WRENN_SANDBOX_ID", id);
        state.defaults.env_vars.insert("WRENN_SANDBOX_ID".into(), id.clone());
    }
    if let Some(ref id) = init_req.template_id {
        tracing::debug!(template_id = %id, "setting template ID from init request");
        // SAFETY: envd is single-threaded at init time; no concurrent env reads.
        unsafe { std::env::set_var("WRENN_TEMPLATE_ID", id) };
        write_run_file(".WRENN_TEMPLATE_ID", id);
        state.defaults.env_vars.insert("WRENN_TEMPLATE_ID".into(), id.clone());
    }

    trigger_restore_and_respond(&state).await
}

async fn trigger_restore_and_respond(state: &AppState) -> axum::response::Response {
    state.try_restore_recovery();

    (
        StatusCode::NO_CONTENT,
        [(header::CACHE_CONTROL, "no-store")],
    )
        .into_response()
}

async fn validate_init_access_token(state: &AppState, request_token: &str) -> Result<(), String> {
    // Fast path: matches existing token
    if state.access_token.is_set() && !request_token.is_empty() && state.access_token.equals(request_token) {
        return Ok(());
    }

    // First-time setup: no existing token
    if !state.access_token.is_set() {
        return Ok(());
    }

    if request_token.is_empty() {
        return Err("access token reset not authorized".into());
    }

    Err("access token validation failed".into())
}

async fn setup_hyperloop(address: &str, env_vars: &dashmap::DashMap<String, String>) {
    // Write to /etc/hosts: events.wrenn.local → address
    let entry = format!("{address} events.wrenn.local\n");

    match std::fs::read_to_string("/etc/hosts") {
        Ok(contents) => {
            let filtered: String = contents
                .lines()
                .filter(|line| !line.contains("events.wrenn.local"))
                .collect::<Vec<_>>()
                .join("\n");
            let new_contents = format!("{filtered}\n{entry}");
            if let Err(e) = std::fs::write("/etc/hosts", new_contents) {
                tracing::error!(error = %e, "failed to modify hosts file");
                return;
            }
        }
        Err(e) => {
            tracing::error!(error = %e, "failed to read hosts file");
            return;
        }
    }

    env_vars.insert(
        "WRENN_EVENTS_ADDRESS".into(),
        format!("http://{address}"),
    );
}

async fn setup_nfs(nfs_target: &str, path: &str) {
    let mkdir = tokio::process::Command::new("mkdir")
        .args(["-p", path])
        .output()
        .await;
    if let Err(e) = mkdir {
        tracing::error!(error = %e, path, "nfs: mkdir failed");
        return;
    }

    let mount = tokio::process::Command::new("mount")
        .args([
            "-v",
            "-t",
            "nfs",
            "-o",
            "mountproto=tcp,mountport=2049,proto=tcp,port=2049,nfsvers=3,noacl",
            nfs_target,
            path,
        ])
        .output()
        .await;

    match mount {
        Ok(output) => {
            let stdout = String::from_utf8_lossy(&output.stdout);
            let stderr = String::from_utf8_lossy(&output.stderr);
            if output.status.success() {
                tracing::info!(nfs_target, path, stdout = %stdout, "nfs: mount success");
            } else {
                tracing::error!(nfs_target, path, stderr = %stderr, "nfs: mount failed");
            }
        }
        Err(e) => {
            tracing::error!(error = %e, nfs_target, path, "nfs: mount command failed");
        }
    }
}

fn write_run_file(name: &str, value: &str) {
    let dir = std::path::Path::new("/run/wrenn");
    if let Err(e) = std::fs::create_dir_all(dir) {
        tracing::warn!(error = %e, "failed to create /run/wrenn");
        return;
    }
    if let Err(e) = std::fs::write(dir.join(name), value) {
        tracing::warn!(error = %e, name, "failed to write run file");
    }
}

fn chrono_parse_to_nanos(ts: &str) -> Result<i64, ()> {
    let secs = ts.parse::<f64>().ok();
    if let Some(s) = secs {
        return Ok((s * 1_000_000_000.0) as i64);
    }
    Err(())
}
