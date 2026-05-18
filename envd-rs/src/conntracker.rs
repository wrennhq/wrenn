use std::collections::HashSet;
use std::sync::Mutex;

/// Tracks active TCP connections.
pub struct ConnTracker {
    inner: Mutex<ConnTrackerInner>,
}

struct ConnTrackerInner {
    active: HashSet<u64>,
    next_id: u64,
}

impl ConnTracker {
    pub fn new() -> Self {
        Self {
            inner: Mutex::new(ConnTrackerInner {
                active: HashSet::new(),
                next_id: 0,
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
}
