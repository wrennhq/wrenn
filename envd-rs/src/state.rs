use std::sync::atomic::{AtomicBool, AtomicU8, AtomicU32, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};

use crate::auth::token::SecureToken;
use crate::conntracker::ConnTracker;
use crate::execcontext::Defaults;
use crate::port::subsystem::PortSubsystem;
use crate::util::AtomicMax;

pub struct AppState {
    pub defaults: Defaults,
    pub version: String,
    pub commit: String,
    pub last_set_time: AtomicMax,
    pub access_token: SecureToken,
    pub conn_tracker: ConnTracker,
    pub port_subsystem: Option<Arc<PortSubsystem>>,
    pub cpu_used_pct: AtomicU32,
    pub cpu_count: AtomicU32,
    /// Whole-VM IO throughput, bytes/sec, sampled over the last 1s tick. Used
    /// by the host activity sampler to keep IO-bound-but-CPU-idle workloads
    /// (e.g. a long download) from being mistaken for inactive.
    pub net_bps: AtomicU64,
    pub disk_bps: AtomicU64,

    /// Memory preload coordination. The host agent POSTs /memory/preload after
    /// a snapshot restore to materialise every physical page (so the next
    /// ch.snapshot writes a self-contained memory-ranges). `mem_preload_started`
    /// ensures only one loader runs; `mem_preload_done` lets concurrent callers
    /// rendezvous; `mem_preload_cancel` lets a teardown abort the loader.
    pub mem_preload_started: AtomicBool,
    pub mem_preload_done: AtomicBool,
    pub mem_preload_cancel: AtomicBool,
    /// Bumped by every /init lifecycle change. A loader thread captures the
    /// value at spawn and refuses to run — or to publish results — once it no
    /// longer matches, so a thread that survived a pause/resume (the VM can be
    /// frozen mid-walk) cannot store a stale `done=true` for the NEXT
    /// lifecycle's preload. Publication happens under `mem_preload_error`'s
    /// mutex, which /init also holds while bumping, closing the
    /// freeze-between-check-and-store window.
    pub mem_preload_generation: AtomicU64,
    pub mem_preload_regions: AtomicU64,
    pub mem_preload_pages: AtomicU64,
    pub mem_preload_bytes: AtomicU64,
    pub mem_preload_elapsed_us: AtomicU64,
    /// 0 = unset, 1 = /dev/mem, 2 = /proc/kcore.
    pub mem_preload_source: AtomicU8,
    pub mem_preload_error: Mutex<Option<String>>,

    /// Last lifecycle ID seen on /init. Used to detect post-resume calls so
    /// envd can refresh port forwarders and remount NFS volumes.
    lifecycle_id: Mutex<Option<String>>,
}

impl AppState {
    pub fn new(
        defaults: Defaults,
        version: String,
        commit: String,
        port_subsystem: Option<Arc<PortSubsystem>>,
    ) -> Arc<Self> {
        let state = Arc::new(Self {
            defaults,
            version,
            commit,
            last_set_time: AtomicMax::new(),
            access_token: SecureToken::new(),
            conn_tracker: ConnTracker::new(),
            port_subsystem,
            cpu_used_pct: AtomicU32::new(0),
            cpu_count: AtomicU32::new(0),
            net_bps: AtomicU64::new(0),
            disk_bps: AtomicU64::new(0),
            mem_preload_started: AtomicBool::new(false),
            mem_preload_done: AtomicBool::new(false),
            mem_preload_cancel: AtomicBool::new(false),
            mem_preload_generation: AtomicU64::new(0),
            mem_preload_regions: AtomicU64::new(0),
            mem_preload_pages: AtomicU64::new(0),
            mem_preload_bytes: AtomicU64::new(0),
            mem_preload_elapsed_us: AtomicU64::new(0),
            mem_preload_source: AtomicU8::new(0),
            mem_preload_error: Mutex::new(None),
            lifecycle_id: Mutex::new(None),
        });

        let state_clone = Arc::clone(&state);
        std::thread::spawn(move || {
            activity_sampler(state_clone);
        });

        state
    }

    pub fn cpu_used_pct(&self) -> f32 {
        f32::from_bits(self.cpu_used_pct.load(Ordering::Relaxed))
    }

    pub fn cpu_count(&self) -> u32 {
        self.cpu_count.load(Ordering::Relaxed)
    }

    pub fn net_bps(&self) -> u64 {
        self.net_bps.load(Ordering::Relaxed)
    }

    pub fn disk_bps(&self) -> u64 {
        self.disk_bps.load(Ordering::Relaxed)
    }

    /// Records a new lifecycle ID, returning true if it changed (i.e. this
    /// is the first /init since a resume). First-ever call returns false:
    /// boot-time /init doesn't need port-subsystem restart since the
    /// subsystem hasn't been started yet by anything else.
    pub fn bump_lifecycle(&self, new_id: &str) -> bool {
        let mut guard = self.lifecycle_id.lock().unwrap();
        let changed = match guard.as_deref() {
            Some(existing) => existing != new_id,
            None => false,
        };
        *guard = Some(new_id.to_owned());
        changed
    }
}

fn activity_sampler(state: Arc<AppState>) {
    use sysinfo::System;

    let mut sys = System::new();
    sys.refresh_cpu_all();

    // Cumulative IO counters from the previous tick. None until the first read.
    let mut prev_net: Option<u64> = read_net_bytes();
    let mut prev_disk: Option<u64> = read_disk_bytes();

    loop {
        std::thread::sleep(std::time::Duration::from_secs(1));

        sys.refresh_cpu_all();

        let pct = sys.global_cpu_usage();
        let rounded = if pct > 0.0 {
            (pct * 100.0).round() / 100.0
        } else {
            0.0
        };

        state
            .cpu_used_pct
            .store(rounded.to_bits(), Ordering::Relaxed);
        state
            .cpu_count
            .store(sys.cpus().len() as u32, Ordering::Relaxed);

        // Throughput = cumulative-counter delta over the ~1s tick. Counters can
        // reset across a snapshot restore; a wrapped/negative delta reads as 0.
        let cur_net = read_net_bytes();
        let net_bps = match (prev_net, cur_net) {
            (Some(p), Some(c)) => c.saturating_sub(p),
            _ => 0,
        };
        prev_net = cur_net;

        let cur_disk = read_disk_bytes();
        let disk_bps = match (prev_disk, cur_disk) {
            (Some(p), Some(c)) => c.saturating_sub(p),
            _ => 0,
        };
        prev_disk = cur_disk;

        state.net_bps.store(net_bps, Ordering::Relaxed);
        state.disk_bps.store(disk_bps, Ordering::Relaxed);
    }
}

/// Sum of rx+tx bytes across all non-loopback interfaces, from /proc/net/dev.
/// Returns None if the file can't be read/parsed.
fn read_net_bytes() -> Option<u64> {
    let content = std::fs::read_to_string("/proc/net/dev").ok()?;
    let mut total: u64 = 0;
    // First two lines are headers.
    for line in content.lines().skip(2) {
        let Some((iface, rest)) = line.split_once(':') else {
            continue;
        };
        if iface.trim() == "lo" {
            continue;
        }
        let fields: Vec<&str> = rest.split_whitespace().collect();
        // Column 0 = rx bytes, column 8 = tx bytes.
        if let Some(rx) = fields.first().and_then(|v| v.parse::<u64>().ok()) {
            total = total.saturating_add(rx);
        }
        if let Some(tx) = fields.get(8).and_then(|v| v.parse::<u64>().ok()) {
            total = total.saturating_add(tx);
        }
    }
    Some(total)
}

/// Sum of sectors read+written across all block devices, ×512, from
/// /proc/diskstats. Skips partitions and loop/ram devices to avoid double
/// counting. Returns None if the file can't be read/parsed.
fn read_disk_bytes() -> Option<u64> {
    let content = std::fs::read_to_string("/proc/diskstats").ok()?;
    let mut sectors: u64 = 0;
    for line in content.lines() {
        let fields: Vec<&str> = line.split_whitespace().collect();
        // 0=major 1=minor 2=name ... 5=sectors read ... 9=sectors written.
        if fields.len() < 10 {
            continue;
        }
        let name = fields[2];
        if name.starts_with("loop") || name.starts_with("ram") {
            continue;
        }
        let read = fields[5].parse::<u64>().unwrap_or(0);
        let written = fields[9].parse::<u64>().unwrap_or(0);
        sectors = sectors.saturating_add(read).saturating_add(written);
    }
    // Linux reports diskstats sectors in fixed 512-byte units.
    Some(sectors.saturating_mul(512))
}
