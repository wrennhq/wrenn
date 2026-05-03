use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::Ordering;

use axum::Json;
use axum::extract::State;
use axum::http::{StatusCode, header};
use axum::response::IntoResponse;
use serde::Deserialize;

use crate::crypto;
use crate::host::mmds;
use crate::state::AppState;

#[derive(Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct InitRequest {
    pub access_token: Option<String>,
    pub default_user: Option<String>,
    pub default_workdir: Option<String>,
    pub env_vars: Option<HashMap<String, String>>,
    pub hyperloop_ip: Option<String>,
    pub timestamp: Option<String>,
    pub volume_mounts: Option<Vec<VolumeMount>>,
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
            let mut defaults = state.defaults.clone();
            defaults.user = user.clone();
            // Note: In Rust we'd need interior mutability for this.
            // For now, env_vars (DashMap) handles concurrent access.
            // User/workdir mutation deferred to full state refactor.
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

    // Re-poll MMDS in background
    if state.is_fc {
        let env_vars = Arc::clone(&state.defaults.env_vars);
        let cancel = tokio_util::sync::CancellationToken::new();
        let cancel_clone = cancel.clone();
        tokio::spawn(async move {
            tokio::time::timeout(std::time::Duration::from_secs(60), async {
                mmds::poll_for_opts(env_vars, cancel_clone).await;
            })
            .await
            .ok();
        });
    }

    trigger_restore_and_respond(&state).await
}

async fn trigger_restore_and_respond(state: &AppState) -> axum::response::Response {
    // Safety net: if health check's postRestoreRecovery hasn't run yet
    if state
        .needs_restore
        .compare_exchange(true, false, Ordering::AcqRel, Ordering::Relaxed)
        .is_ok()
    {
        post_restore_recovery(state);
    }

    state.conn_tracker.restore_after_snapshot();
    if let Some(ref ps) = state.port_subsystem {
        ps.restart();
    }

    (
        StatusCode::NO_CONTENT,
        [(header::CACHE_CONTROL, "no-store")],
    )
        .into_response()
}

fn post_restore_recovery(state: &AppState) {
    tracing::info!("restore: post-restore recovery (no GC needed in Rust)");

    state.snapshot_in_progress.store(false, std::sync::atomic::Ordering::Release);

    state.conn_tracker.restore_after_snapshot();

    if let Some(ref ps) = state.port_subsystem {
        ps.restart();
        tracing::info!("restore: port subsystem restarted");
    }
}

async fn validate_init_access_token(state: &AppState, request_token: &str) -> Result<(), String> {
    // Fast path: matches existing token
    if state.access_token.is_set() && !request_token.is_empty() && state.access_token.equals(request_token) {
        return Ok(());
    }

    // Check MMDS hash
    if state.is_fc {
        if let Ok(mmds_hash) = mmds::get_access_token_hash().await {
            if !mmds_hash.is_empty() {
                if request_token.is_empty() {
                    let empty_hash = crypto::sha512::hash_access_token("");
                    if mmds_hash == empty_hash {
                        return Ok(());
                    }
                } else {
                    let token_hash = crypto::sha512::hash_access_token(request_token);
                    if mmds_hash == token_hash {
                        return Ok(());
                    }
                }
                return Err("access token validation failed".into());
            }
        }
    }

    // First-time setup: no existing token and no MMDS
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

fn chrono_parse_to_nanos(ts: &str) -> Result<i64, ()> {
    // Parse RFC3339 timestamp to nanoseconds since epoch
    // Simple approach: parse as seconds + fractional
    let secs = ts.parse::<f64>().ok();
    if let Some(s) = secs {
        return Ok((s * 1_000_000_000.0) as i64);
    }
    // Try RFC3339 format
    // For now, fall back to allowing the update
    Err(())
}
