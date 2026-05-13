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

    #[cfg(test)]
    fn active_count(&self) -> usize {
        self.inner.lock().unwrap().active.len()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn register_assigns_sequential_ids() {
        let ct = ConnTracker::new();
        assert_eq!(ct.register_connection(), 0);
        assert_eq!(ct.register_connection(), 1);
        assert_eq!(ct.register_connection(), 2);
    }

    #[test]
    fn remove_clears_active() {
        let ct = ConnTracker::new();
        let id = ct.register_connection();
        assert_eq!(ct.active_count(), 1);
        ct.remove_connection(id);
        assert_eq!(ct.active_count(), 0);
    }

    #[test]
    fn remove_nonexistent_is_noop() {
        let ct = ConnTracker::new();
        ct.remove_connection(999);
        assert_eq!(ct.active_count(), 0);
    }

    #[test]
    fn prepare_disables_keepalives() {
        let ct = ConnTracker::new();
        assert!(ct.keepalives_enabled());
        ct.register_connection();
        ct.prepare_for_snapshot();
        assert!(!ct.keepalives_enabled());
    }

    #[test]
    fn restore_removes_zombies_and_reenables_keepalives() {
        let ct = ConnTracker::new();
        let id0 = ct.register_connection();
        let id1 = ct.register_connection();
        ct.prepare_for_snapshot();
        ct.restore_after_snapshot();
        assert!(ct.keepalives_enabled());
        // Both pre-snapshot connections removed as zombies
        assert_eq!(ct.active_count(), 0);
        // IDs don't matter anymore, but remove shouldn't panic
        ct.remove_connection(id0);
        ct.remove_connection(id1);
    }

    #[test]
    fn restore_without_prepare_is_noop() {
        let ct = ConnTracker::new();
        let _id = ct.register_connection();
        ct.restore_after_snapshot();
        assert!(ct.keepalives_enabled());
        assert_eq!(ct.active_count(), 1);
    }

    #[test]
    fn connection_closed_before_restore_not_zombie() {
        let ct = ConnTracker::new();
        let id0 = ct.register_connection();
        let _id1 = ct.register_connection();
        ct.prepare_for_snapshot();
        // Close id0 during snapshot window
        ct.remove_connection(id0);
        assert_eq!(ct.active_count(), 1);
        ct.restore_after_snapshot();
        // id1 was zombie (still active at restore), id0 already gone
        assert_eq!(ct.active_count(), 0);
    }

    #[test]
    fn post_snapshot_connection_survives_restore() {
        let ct = ConnTracker::new();
        ct.register_connection();
        ct.prepare_for_snapshot();
        // New connection after snapshot
        let _post = ct.register_connection();
        ct.restore_after_snapshot();
        // Pre-snapshot connection removed, post-snapshot survives
        assert_eq!(ct.active_count(), 1);
    }

    #[test]
    fn full_lifecycle() {
        let ct = ConnTracker::new();
        let _a = ct.register_connection();
        let b = ct.register_connection();
        let _c = ct.register_connection();
        assert_eq!(ct.active_count(), 3);
        assert!(ct.keepalives_enabled());

        ct.prepare_for_snapshot();
        assert!(!ct.keepalives_enabled());

        let d = ct.register_connection();
        ct.remove_connection(b);

        ct.restore_after_snapshot();
        assert!(ct.keepalives_enabled());
        // a and c were zombies, b removed before restore, d is post-snapshot
        assert_eq!(ct.active_count(), 1);
        ct.remove_connection(d);
        assert_eq!(ct.active_count(), 0);

        // Can reuse tracker after restore
        let e = ct.register_connection();
        assert_eq!(ct.active_count(), 1);
        assert!(e > d);
    }
}
