use std::sync::atomic::{AtomicI64, Ordering};

pub struct AtomicMax {
    val: AtomicI64,
}

impl AtomicMax {
    pub fn new() -> Self {
        Self {
            val: AtomicI64::new(i64::MIN),
        }
    }

    pub fn get(&self) -> i64 {
        self.val.load(Ordering::Acquire)
    }

    /// Sets the stored value to `new` if `new` is strictly greater than
    /// the current value. Returns `true` if the value was updated.
    pub fn set_to_greater(&self, new: i64) -> bool {
        loop {
            let current = self.val.load(Ordering::Acquire);
            if new <= current {
                return false;
            }
            match self
                .val
                .compare_exchange_weak(current, new, Ordering::Release, Ordering::Relaxed)
            {
                Ok(_) => return true,
                Err(_) => continue,
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Arc;

    #[test]
    fn initial_value_is_i64_min() {
        let m = AtomicMax::new();
        assert_eq!(m.get(), i64::MIN);
    }

    #[test]
    fn updates_when_larger() {
        let m = AtomicMax::new();
        assert!(m.set_to_greater(0));
        assert_eq!(m.get(), 0);
        assert!(m.set_to_greater(100));
        assert_eq!(m.get(), 100);
    }

    #[test]
    fn returns_false_when_equal() {
        let m = AtomicMax::new();
        m.set_to_greater(42);
        assert!(!m.set_to_greater(42));
        assert_eq!(m.get(), 42);
    }

    #[test]
    fn returns_false_when_smaller() {
        let m = AtomicMax::new();
        m.set_to_greater(100);
        assert!(!m.set_to_greater(50));
        assert_eq!(m.get(), 100);
    }

    #[test]
    fn concurrent_convergence() {
        let m = Arc::new(AtomicMax::new());
        let threads: Vec<_> = (0..8)
            .map(|t| {
                let m = Arc::clone(&m);
                std::thread::spawn(move || {
                    for i in (t * 100)..((t + 1) * 100) {
                        m.set_to_greater(i);
                    }
                })
            })
            .collect();
        for t in threads {
            t.join().unwrap();
        }
        assert_eq!(m.get(), 799);
    }

    #[test]
    fn i64_max_boundary() {
        let m = AtomicMax::new();
        assert!(m.set_to_greater(i64::MAX));
        assert!(!m.set_to_greater(i64::MAX));
        assert!(!m.set_to_greater(0));
        assert_eq!(m.get(), i64::MAX);
    }
}
