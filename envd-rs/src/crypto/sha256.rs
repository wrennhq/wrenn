use base64::Engine;
use base64::engine::general_purpose::STANDARD_NO_PAD;
use sha2::{Digest, Sha256};

pub fn hash(data: &[u8]) -> String {
    let h = Sha256::digest(data);
    let encoded = STANDARD_NO_PAD.encode(h);
    format!("$sha256${encoded}")
}

pub fn hash_without_prefix(data: &[u8]) -> String {
    let h = Sha256::digest(data);
    STANDARD_NO_PAD.encode(h)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_hash_format() {
        let result = hash(b"test");
        assert!(result.starts_with("$sha256$"));
        assert!(!result.contains('='));
    }

    #[test]
    fn test_hash_without_prefix() {
        let result = hash_without_prefix(b"test");
        assert!(!result.starts_with("$sha256$"));
        assert!(!result.contains('='));
    }
}
