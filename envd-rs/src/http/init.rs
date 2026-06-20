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
    /// Public proxy domain (e.g. "wrenn.dev"). Used by `envd ports` to build
    /// the {port}-{sandbox_id}.{domain} URLs.
    pub proxy_domain: Option<String>,
    /// New lifecycle identifier for this resume. When it changes between
    /// /init calls, envd treats the call as a post-resume hook: port
    /// forwarder is restarted and NFS mounts are refreshed.
    pub lifecycle_id: Option<String>,
}

#[derive(Deserialize)]
pub struct VolumeMount {
    pub nfs_target: String,
    pub path: String,
}

/// POST /init — called by host agent after boot.
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

    // Post-resume lifecycle hook: restart port forwarder so socat children
    // are reaped + respawned against the new wall clock and any rotated
    // listeners. Must run BEFORE the stale-timestamp early-return so a
    // resume with an out-of-order timestamp still refreshes the subsystem.
    let lifecycle_changed = if let Some(ref new_id) = init_req.lifecycle_id {
        state.bump_lifecycle(new_id)
    } else {
        false
    };
    if lifecycle_changed {
        // Each new lifecycle (i.e. a snapshot restore) requires a fresh memory
        // preload pass — pages materialised before the previous pause are now
        // back in the source memory-ranges file as the host re-restored them
        // lazily. Reset the flags so the next POST /memory/preload kicks off
        // a new loader instead of returning the stale "already-done".
        use std::sync::atomic::Ordering;
        state.mem_preload_cancel.store(false, Ordering::SeqCst);
        state.mem_preload_done.store(false, Ordering::SeqCst);
        state.mem_preload_started.store(false, Ordering::SeqCst);
        state.mem_preload_regions.store(0, Ordering::SeqCst);
        state.mem_preload_pages.store(0, Ordering::SeqCst);
        state.mem_preload_bytes.store(0, Ordering::SeqCst);
        state.mem_preload_elapsed_us.store(0, Ordering::SeqCst);
        state.mem_preload_source.store(0, Ordering::SeqCst);
        *state.mem_preload_error.lock().unwrap() = None;

        if let Some(ref port_sub) = state.port_subsystem {
            tracing::info!("lifecycle changed, restarting port subsystem");
            port_sub.restart();
        }
        // Instant wall-clock step on resume. The host wall time arrives in
        // init_req.timestamp; chrony's PHC refclock needs several poll cycles
        // (poll 2 = 4s) before it has a valid offset to step to, so makestep
        // alone leaves the clock stale for seconds after a resume. Set
        // CLOCK_REALTIME directly here for an immediate jump, then let chronyd
        // keep disciplining drift against /dev/ptp0.
        if let Some(ref ts_str) = init_req.timestamp {
            if let Ok(nanos) = parse_timestamp_to_nanos(ts_str) {
                step_realtime_clock(nanos);
            }
        }
        // Also nudge chrony to re-sync its internal offset against the
        // now-correct clock + PHC immediately, bypassing its slew period.
        // Best effort — the direct step above already corrected wall time.
        tokio::spawn(async {
            match tokio::process::Command::new("chronyc")
                .args(["makestep"])
                .output()
                .await
            {
                Ok(out) if out.status.success() => {
                    tracing::info!("chronyc makestep ok");
                }
                Ok(out) => {
                    let stderr = String::from_utf8_lossy(&out.stderr);
                    tracing::warn!(stderr = %stderr, "chronyc makestep failed");
                }
                Err(e) => {
                    tracing::warn!(error = %e, "chronyc makestep spawn failed");
                }
            }
        });
    }

    // Idempotent timestamp check. Run after lifecycle handling so a
    // stale-timestamp /init still gets to refresh ports + step clock.
    // The actual clock step happens in the lifecycle block above; this
    // only gates the rest of the apply path on monotonic timestamps.
    if let Some(ref ts_str) = init_req.timestamp {
        if let Ok(ts) = parse_timestamp_to_nanos(ts_str) {
            if !state.last_set_time.set_to_greater(ts) {
                return (
                    StatusCode::NO_CONTENT,
                    [(header::CACHE_CONTROL, "no-store")],
                )
                    .into_response();
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

    // Hyperloop /etc/hosts setup. Awaited so callers that immediately
    // resolve events.wrenn.local see the entry. Cheap (two file ops).
    if let Some(ref ip) = init_req.hyperloop_ip {
        setup_hyperloop(ip, &state.defaults.env_vars).await;
    }

    // NFS mounts. Awaited in parallel so callers that immediately access the
    // mount path don't race the mount(2). Previously these were detached via
    // tokio::spawn, which let /init return success before mounts existed.
    if let Some(ref mounts) = init_req.volume_mounts {
        let futs = mounts.iter().map(|m| {
            let target = m.nfs_target.clone();
            let path = m.path.clone();
            async move {
                setup_nfs(&target, &path).await;
            }
        });
        futures::future::join_all(futs).await;
    }

    // Set sandbox/template metadata from request body.
    if let Some(ref id) = init_req.sandbox_id {
        tracing::debug!(sandbox_id = %id, "setting sandbox ID from init request");
        // SAFETY: envd is single-threaded at init time; no concurrent env reads.
        unsafe { std::env::set_var("WRENN_SANDBOX_ID", id) };
        write_run_file(".WRENN_SANDBOX_ID", id);
        state
            .defaults
            .env_vars
            .insert("WRENN_SANDBOX_ID".into(), id.clone());
    }
    if let Some(ref id) = init_req.template_id {
        tracing::debug!(template_id = %id, "setting template ID from init request");
        // SAFETY: envd is single-threaded at init time; no concurrent env reads.
        unsafe { std::env::set_var("WRENN_TEMPLATE_ID", id) };
        write_run_file(".WRENN_TEMPLATE_ID", id);
        state
            .defaults
            .env_vars
            .insert("WRENN_TEMPLATE_ID".into(), id.clone());
    }
    if let Some(ref domain) = init_req.proxy_domain {
        if !domain.is_empty() {
            tracing::debug!(proxy_domain = %domain, "setting proxy domain from init request");
            // SAFETY: envd is single-threaded at init time; no concurrent env reads.
            unsafe { std::env::set_var("WRENN_PROXY_DOMAIN", domain) };
            write_run_file(".WRENN_PROXY_DOMAIN", domain);
            state
                .defaults
                .env_vars
                .insert("WRENN_PROXY_DOMAIN".into(), domain.clone());
        }
    }

    (
        StatusCode::NO_CONTENT,
        [(header::CACHE_CONTROL, "no-store")],
    )
        .into_response()
}

async fn validate_init_access_token(state: &AppState, request_token: &str) -> Result<(), String> {
    // Fast path: matches existing token
    if state.access_token.is_set()
        && !request_token.is_empty()
        && state.access_token.equals(request_token)
    {
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

    env_vars.insert("WRENN_EVENTS_ADDRESS".into(), format!("http://{address}"));
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
    let dir = std::path::Path::new(crate::config::WRENN_RUN_DIR);
    if let Err(e) = std::fs::create_dir_all(dir) {
        tracing::warn!(error = %e, "failed to create /run/wrenn");
        return;
    }
    if let Err(e) = std::fs::write(dir.join(name), value) {
        tracing::warn!(error = %e, name, "failed to write run file");
    }
}

/// Hard-steps CLOCK_REALTIME to `nanos` since the Unix epoch. Requires
/// CAP_SYS_TIME, which envd has as PID 1 in the guest. Best effort — on
/// failure the clock is left for chrony to discipline against the PHC.
// libc::time_t is deprecated pending musl 1.2's 64-bit switch, but the
// timespec.tv_sec field is still typed as time_t on this target.
#[allow(deprecated)]
fn step_realtime_clock(nanos: i64) {
    let ts = libc::timespec {
        tv_sec: (nanos / 1_000_000_000) as libc::time_t,
        tv_nsec: (nanos % 1_000_000_000) as libc::c_long,
    };
    // SAFETY: ts is a valid timespec; CLOCK_REALTIME is settable as root.
    let rc = unsafe { libc::clock_settime(libc::CLOCK_REALTIME, &ts) };
    if rc != 0 {
        tracing::warn!(error = %std::io::Error::last_os_error(),
            "clock_settime(CLOCK_REALTIME) failed");
    } else {
        tracing::info!(nanos, "stepped CLOCK_REALTIME from host timestamp on resume");
    }
}

/// Parses a host-provided timestamp into nanoseconds since the Unix epoch.
/// Accepts either RFC3339 (`2026-05-17T16:13:03.123456Z`) or a float-seconds
/// string (legacy callers).
fn parse_timestamp_to_nanos(ts: &str) -> Result<i64, ()> {
    if let Ok(parsed) = chrono::DateTime::parse_from_rfc3339(ts) {
        return Ok(parsed.timestamp_nanos_opt().ok_or(())?);
    }
    if let Ok(secs) = ts.parse::<f64>() {
        return Ok((secs * 1_000_000_000.0) as i64);
    }
    Err(())
}
