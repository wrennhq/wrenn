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

    /// Sets the stored value to `new` if `new` is strictly greater than
    /// the current value. Returns `true` if the value was updated.
    pub fn set_to_greater(&self, new: i64) -> bool {
        loop {
            let current = self.val.load(Ordering::Acquire);
            if new <= current {
                return false;
            }
            match self.val.compare_exchange_weak(
                current,
                new,
                Ordering::Release,
                Ordering::Relaxed,
            ) {
                Ok(_) => return true,
                Err(_) => continue,
            }
        }
    }
}
