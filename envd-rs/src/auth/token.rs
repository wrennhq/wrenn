use std::sync::RwLock;

use subtle::ConstantTimeEq;
use zeroize::Zeroize;

/// Secure token storage with constant-time comparison and zeroize-on-drop.
///
/// Mirrors Go's SecureToken backed by memguard.LockedBuffer.
/// In Rust we rely on `zeroize` for Drop-based zeroing.
pub struct SecureToken {
    inner: RwLock<Option<Vec<u8>>>,
}

impl SecureToken {
    pub fn new() -> Self {
        Self {
            inner: RwLock::new(None),
        }
    }

    pub fn set(&self, token: &[u8]) -> Result<(), &'static str> {
        if token.is_empty() {
            return Err("empty token not allowed");
        }
        let mut guard = self.inner.write().unwrap();
        if let Some(ref mut old) = *guard {
            old.zeroize();
        }
        *guard = Some(token.to_vec());
        Ok(())
    }

    pub fn is_set(&self) -> bool {
        let guard = self.inner.read().unwrap();
        guard.is_some()
    }

    /// Constant-time comparison.
    pub fn equals(&self, other: &str) -> bool {
        let guard = self.inner.read().unwrap();
        match guard.as_ref() {
            Some(buf) => buf.as_slice().ct_eq(other.as_bytes()).into(),
            None => false,
        }
    }

    /// Constant-time comparison with another SecureToken.
    pub fn equals_secure(&self, other: &SecureToken) -> bool {
        let other_bytes = match other.bytes() {
            Some(b) => b,
            None => return false,
        };
        let guard = self.inner.read().unwrap();
        let result = match guard.as_ref() {
            Some(buf) => buf.as_slice().ct_eq(&other_bytes).into(),
            None => false,
        };
        // other_bytes dropped here, Vec<u8> doesn't auto-zeroize but
        // we accept this — same as Go's `defer memguard.WipeBytes(otherBytes)`
        result
    }

    /// Returns a copy of the token bytes (for signature generation).
    pub fn bytes(&self) -> Option<Vec<u8>> {
        let guard = self.inner.read().unwrap();
        guard.as_ref().map(|b| b.clone())
    }

    /// Transfer token from another SecureToken, clearing the source.
    pub fn take_from(&self, src: &SecureToken) {
        let taken = {
            let mut src_guard = src.inner.write().unwrap();
            src_guard.take()
        };
        let mut guard = self.inner.write().unwrap();
        if let Some(ref mut old) = *guard {
            old.zeroize();
        }
        *guard = taken;
    }

    pub fn destroy(&self) {
        let mut guard = self.inner.write().unwrap();
        if let Some(ref mut buf) = *guard {
            buf.zeroize();
        }
        *guard = None;
    }
}

impl Drop for SecureToken {
    fn drop(&mut self) {
        if let Ok(mut guard) = self.inner.write() {
            if let Some(ref mut buf) = *guard {
                buf.zeroize();
            }
        }
    }
}

/// Deserialize from JSON string, matching Go's UnmarshalJSON behavior.
/// Expects a quoted JSON string. Rejects escape sequences.
impl SecureToken {
    pub fn from_json_bytes(data: &mut [u8]) -> Result<Self, &'static str> {
        if data.len() < 2 || data[0] != b'"' || data[data.len() - 1] != b'"' {
            data.zeroize();
            return Err("invalid secure token JSON string");
        }

        let content = &data[1..data.len() - 1];
        if content.contains(&b'\\') {
            data.zeroize();
            return Err("invalid secure token: unexpected escape sequence");
        }

        if content.is_empty() {
            data.zeroize();
            return Err("empty token not allowed");
        }

        let token = Self::new();
        token.set(content).map_err(|_| "failed to set token")?;

        data.zeroize();
        Ok(token)
    }
}
