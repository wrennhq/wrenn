use std::ffi::CString;
use std::time::{SystemTime, UNIX_EPOCH};

use serde::Serialize;

#[derive(Serialize)]
pub struct Metrics {
    pub ts: i64,
    pub cpu_count: u32,
    pub cpu_used_pct: f32,
    pub mem_total_mib: u64,
    pub mem_used_mib: u64,
    pub mem_total: u64,
    pub mem_used: u64,
    pub disk_used: u64,
    pub disk_total: u64,
}

pub fn get_metrics() -> Result<Metrics, String> {
    use sysinfo::System;

    let mut sys = System::new();
    sys.refresh_memory();
    sys.refresh_cpu_all();

    std::thread::sleep(std::time::Duration::from_millis(100));
    sys.refresh_cpu_all();

    let cpu_count = sys.cpus().len() as u32;
    let cpu_used_pct = sys.global_cpu_usage();
    let cpu_used_pct_rounded = if cpu_used_pct > 0.0 {
        (cpu_used_pct * 100.0).round() / 100.0
    } else {
        0.0
    };

    let mem_total = sys.total_memory();
    let mem_used = sys.used_memory();

    let (disk_total, disk_used) = disk_stats("/")?;

    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs() as i64;

    Ok(Metrics {
        ts,
        cpu_count,
        cpu_used_pct: cpu_used_pct_rounded,
        mem_total_mib: mem_total / 1024 / 1024,
        mem_used_mib: mem_used / 1024 / 1024,
        mem_total,
        mem_used,
        disk_used,
        disk_total,
    })
}

fn disk_stats(path: &str) -> Result<(u64, u64), String> {
    let c_path = CString::new(path).unwrap();
    let mut stat: libc::statfs = unsafe { std::mem::zeroed() };
    let ret = unsafe { libc::statfs(c_path.as_ptr(), &mut stat) };
    if ret != 0 {
        return Err(format!("statfs failed: {}", std::io::Error::last_os_error()));
    }

    let block = stat.f_bsize as u64;
    let total = stat.f_blocks * block;
    let available = stat.f_bavail * block;

    Ok((total, total - available))
}
