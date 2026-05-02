use sha2::{Digest, Sha512};

pub fn hash_access_token(token: &str) -> String {
    let h = Sha512::digest(token.as_bytes());
    hex::encode(h)
}

pub fn hash_access_token_bytes(token: &[u8]) -> String {
    let h = Sha512::digest(token);
    hex::encode(h)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_hash_access_token() {
        let h1 = hash_access_token("test");
        let h2 = hash_access_token_bytes(b"test");
        assert_eq!(h1, h2);
        assert_eq!(h1.len(), 128); // SHA-512 hex = 128 chars
    }
}
