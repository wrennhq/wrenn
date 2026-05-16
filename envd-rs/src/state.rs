use std::sync::atomic::{AtomicBool, AtomicU32, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use crate::auth::token::SecureToken;
use crate::conntracker::ConnTracker;
use crate::execcontext::Defaults;
use crate::port::subsystem::PortSubsystem;
use crate::util::AtomicMax;

pub struct AppState {
    pub defaults: Defaults,
    pub version: String,
    pub commit: String,
    pub needs_restore: AtomicBool,
    pub last_set_time: AtomicMax,
    pub access_token: SecureToken,
    pub conn_tracker: ConnTracker,
    pub port_subsystem: Option<Arc<PortSubsystem>>,
    pub cpu_used_pct: AtomicU32,
    pub cpu_count: AtomicU32,
    pub snapshot_in_progress: AtomicBool,
    pub last_health_epoch: AtomicU64,
    pub restore_epoch: AtomicU64,
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
            needs_restore: AtomicBool::new(false),
            last_set_time: AtomicMax::new(),
            access_token: SecureToken::new(),
            conn_tracker: ConnTracker::new(),
            port_subsystem,
            cpu_used_pct: AtomicU32::new(0),
            cpu_count: AtomicU32::new(0),
            snapshot_in_progress: AtomicBool::new(false),
            last_health_epoch: AtomicU64::new(0),
            restore_epoch: AtomicU64::new(0),
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

    /// Runs post-restore recovery if `needs_restore` is set OR a wall-clock
    /// gap is detected (catches restores where snapshot/prepare never ran).
    pub fn try_restore_recovery(&self) {
        let now_epoch = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_secs();
        let prev_epoch = self.last_health_epoch.swap(now_epoch, Ordering::AcqRel);

        // Detect restore via wall-clock gap: if >3s passed since last health
        // check, the VM was frozen and restored. Catches the case where
        // snapshot/prepare timed out and needs_restore was never set.
        let gap_detected = prev_epoch > 0 && now_epoch.saturating_sub(prev_epoch) > 3;

        let flag_set = self
            .needs_restore
            .compare_exchange(true, false, Ordering::AcqRel, Ordering::Relaxed)
            .is_ok();

        if !flag_set && !gap_detected {
            return;
        }

        if gap_detected && !flag_set {
            tracing::info!(
                gap_secs = now_epoch.saturating_sub(prev_epoch),
                "restore: detected via wall-clock gap (needs_restore was not set)"
            );
        }

        tracing::info!("restore: post-restore recovery");
        self.snapshot_in_progress.store(false, Ordering::Release);
        self.restore_epoch.store(now_epoch, Ordering::Release);
        self.conn_tracker.restore_after_snapshot();

        if let Some(ref ps) = self.port_subsystem {
            ps.restart();
            tracing::info!("restore: port subsystem restarted");
        }
    }
}

fn cpu_sampler(state: Arc<AppState>) {
    use sysinfo::System;

    let mut sys = System::new();
    sys.refresh_cpu_all();

    loop {
        std::thread::sleep(std::time::Duration::from_secs(1));

        if state.needs_restore.load(Ordering::Acquire) {
            // After snapshot restore, sysinfo's internal CPU counters are stale.
            // Reinitialize to get a fresh baseline.
            sys = System::new();
            sys.refresh_cpu_all();
            continue;
        }

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
