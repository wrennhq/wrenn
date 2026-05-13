use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::Arc;

use crate::auth::token::SecureToken;
use crate::conntracker::ConnTracker;
use crate::execcontext::Defaults;
use crate::port::subsystem::PortSubsystem;
use crate::util::AtomicMax;

pub struct AppState {
    pub defaults: Defaults,
    pub version: String,
    pub commit: String,
    pub is_fc: bool,
    pub needs_restore: AtomicBool,
    pub last_set_time: AtomicMax,
    pub access_token: SecureToken,
    pub conn_tracker: ConnTracker,
    pub port_subsystem: Option<Arc<PortSubsystem>>,
    pub cpu_used_pct: AtomicU32,
    pub cpu_count: AtomicU32,
    pub snapshot_in_progress: AtomicBool,
}

impl AppState {
    pub fn new(
        defaults: Defaults,
        version: String,
        commit: String,
        is_fc: bool,
        port_subsystem: Option<Arc<PortSubsystem>>,
    ) -> Arc<Self> {
        let state = Arc::new(Self {
            defaults,
            version,
            commit,
            is_fc,
            needs_restore: AtomicBool::new(false),
            last_set_time: AtomicMax::new(),
            access_token: SecureToken::new(),
            conn_tracker: ConnTracker::new(),
            port_subsystem,
            cpu_used_pct: AtomicU32::new(0),
            cpu_count: AtomicU32::new(0),
            snapshot_in_progress: AtomicBool::new(false),
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
