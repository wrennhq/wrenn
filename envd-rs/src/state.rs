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

    /// Memory preload coordination. The host agent POSTs /memory/preload after
    /// a snapshot restore to materialise every physical page (so the next
    /// ch.snapshot writes a self-contained memory-ranges). `mem_preload_started`
    /// ensures only one loader runs; `mem_preload_done` lets concurrent callers
    /// rendezvous; `mem_preload_cancel` lets a teardown abort the loader.
    pub mem_preload_started: AtomicBool,
    pub mem_preload_done: AtomicBool,
    pub mem_preload_cancel: AtomicBool,
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
            mem_preload_started: AtomicBool::new(false),
            mem_preload_done: AtomicBool::new(false),
            mem_preload_cancel: AtomicBool::new(false),
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
            cpu_sampler(state_clone);
        });

        state
    }

    pub fn cpu_used_pct(&self) -> f32 {
        f32::from_bits(self.cpu_used_pct.load(Ordering::Relaxed))
    }

    pub fn cpu_count(&self) -> u32 {
        self.cpu_count.load(Ordering::Relaxed)
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

fn cpu_sampler(state: Arc<AppState>) {
    use sysinfo::System;

    let mut sys = System::new();
    sys.refresh_cpu_all();

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
    }
}
