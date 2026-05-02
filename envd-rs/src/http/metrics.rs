use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use axum::Json;
use axum::extract::State;
use axum::http::{StatusCode, header};
use axum::response::IntoResponse;
use serde::Serialize;

use crate::state::AppState;

#[derive(Serialize)]
pub struct Metrics {
    ts: i64,
    cpu_count: u32,
    cpu_used_pct: f32,
    mem_total_mib: u64,
    mem_used_mib: u64,
    mem_total: u64,
    mem_used: u64,
    disk_used: u64,
    disk_total: u64,
}

pub async fn get_metrics(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    tracing::trace!("get metrics");

    match collect_metrics(&state) {
        Ok(m) => (
            StatusCode::OK,
            [(header::CACHE_CONTROL, "no-store")],
            Json(m),
        )
            .into_response(),
        Err(e) => {
            tracing::error!(error = %e, "failed to get metrics");
            StatusCode::INTERNAL_SERVER_ERROR.into_response()
        }
    }
}

fn collect_metrics(state: &AppState) -> Result<Metrics, String> {
    let cpu_count = state.cpu_count();
    let cpu_used_pct_rounded = state.cpu_used_pct();

    let mut sys = sysinfo::System::new();
    sys.refresh_memory();
    let mem_total = sys.total_memory();
    let mem_used = sys.used_memory();
    let mem_total_mib = mem_total / 1024 / 1024;
    let mem_used_mib = mem_used / 1024 / 1024;

    let (disk_total, disk_used) = disk_stats("/").map_err(|e| e.to_string())?;

    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;

    Ok(Metrics {
        ts,
        cpu_count,
        cpu_used_pct: cpu_used_pct_rounded,
        mem_total_mib,
        mem_used_mib,
        mem_total,
        mem_used,
        disk_used,
        disk_total,
    })
}

fn disk_stats(path: &str) -> Result<(u64, u64), nix::Error> {
    use std::ffi::CString;

    let c_path = CString::new(path).unwrap();
    let mut stat: libc::statfs = unsafe { std::mem::zeroed() };
    let ret = unsafe { libc::statfs(c_path.as_ptr(), &mut stat) };
    if ret != 0 {
        return Err(nix::Error::last());
    }

    let block = stat.f_bsize as u64;
    let total = stat.f_blocks * block;
    let available = stat.f_bavail * block;

    Ok((total, total - available))
}
