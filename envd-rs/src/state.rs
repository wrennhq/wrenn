use std::sync::atomic::AtomicBool;
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
}

impl AppState {
    pub fn new(
        defaults: Defaults,
        version: String,
        commit: String,
        is_fc: bool,
        port_subsystem: Option<Arc<PortSubsystem>>,
    ) -> Arc<Self> {
        Arc::new(Self {
            defaults,
            version,
            commit,
            is_fc,
            needs_restore: AtomicBool::new(false),
            last_set_time: AtomicMax::new(),
            access_token: SecureToken::new(),
            conn_tracker: ConnTracker::new(),
            port_subsystem,
        })
    }
}
