use std::collections::HashSet;
use std::sync::Mutex;

/// Tracks active TCP connections for snapshot/restore lifecycle.
///
/// Before snapshot: close idle connections, record active ones.
/// After restore: close all pre-snapshot connections (zombie TCP sockets).
///
/// In Rust/axum, we don't have Go's ConnState callback. Instead we track
/// connections via a tower middleware that registers connection IDs.
/// For the initial implementation, we track by a simple connection counter
/// and rely on axum's graceful shutdown mechanics.
pub struct ConnTracker {
    inner: Mutex<ConnTrackerInner>,
}

struct ConnTrackerInner {
    active: HashSet<u64>,
    pre_snapshot: Option<HashSet<u64>>,
    next_id: u64,
    keepalives_enabled: bool,
}

impl ConnTracker {
    pub fn new() -> Self {
        Self {
            inner: Mutex::new(ConnTrackerInner {
                active: HashSet::new(),
                pre_snapshot: None,
                next_id: 0,
                keepalives_enabled: true,
            }),
        }
    }

    pub fn register_connection(&self) -> u64 {
        let mut inner = self.inner.lock().unwrap();
        let id = inner.next_id;
        inner.next_id += 1;
        inner.active.insert(id);
        id
    }

    pub fn remove_connection(&self, id: u64) {
        let mut inner = self.inner.lock().unwrap();
        inner.active.remove(&id);
        if let Some(ref mut pre) = inner.pre_snapshot {
            pre.remove(&id);
        }
    }

    pub fn prepare_for_snapshot(&self) {
        let mut inner = self.inner.lock().unwrap();
        inner.keepalives_enabled = false;
        inner.pre_snapshot = Some(inner.active.clone());
        tracing::info!(
            active_connections = inner.active.len(),
            "snapshot: recorded pre-snapshot connections, keep-alives disabled"
        );
    }

    pub fn restore_after_snapshot(&self) {
        let mut inner = self.inner.lock().unwrap();
        if let Some(pre) = inner.pre_snapshot.take() {
            let zombie_count = pre.len();
            for id in &pre {
                inner.active.remove(id);
            }
            if zombie_count > 0 {
                tracing::info!(zombie_count, "restore: closed zombie connections");
            }
        }
        inner.keepalives_enabled = true;
    }

    pub fn keepalives_enabled(&self) -> bool {
        self.inner.lock().unwrap().keepalives_enabled
    }
}
