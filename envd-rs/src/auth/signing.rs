use crate::auth::token::SecureToken;
use crate::crypto;
use zeroize::Zeroize;

pub const READ_OPERATION: &str = "read";
pub const WRITE_OPERATION: &str = "write";

/// Generate a v1 signature: `v1_{sha256_base64(path:operation:username:token[:expiration])}`
pub fn generate_signature(
    token: &SecureToken,
    path: &str,
    username: &str,
    operation: &str,
    expiration: Option<i64>,
) -> Result<String, &'static str> {
    let mut token_bytes = token.bytes().ok_or("access token is not set")?;

    let payload = match expiration {
        Some(exp) => format!(
            "{}:{}:{}:{}:{}",
            path,
            operation,
            username,
            String::from_utf8_lossy(&token_bytes),
            exp
        ),
        None => format!(
            "{}:{}:{}:{}",
            path,
            operation,
            username,
            String::from_utf8_lossy(&token_bytes),
        ),
    };

    token_bytes.zeroize();

    let hash = crypto::sha256::hash_without_prefix(payload.as_bytes());
    Ok(format!("v1_{hash}"))
}

/// Validate a request's signing. Returns Ok(()) if valid.
pub fn validate_signing(
    token: &SecureToken,
    header_token: Option<&str>,
    signature: Option<&str>,
    signature_expiration: Option<i64>,
    username: &str,
    path: &str,
    operation: &str,
) -> Result<(), String> {
    if !token.is_set() {
        return Ok(());
    }

    if let Some(ht) = header_token {
        if !ht.is_empty() {
            if token.equals(ht) {
                return Ok(());
            }
            return Err("access token present in header but does not match".into());
        }
    }

    let sig = signature.ok_or("missing signature query parameter")?;

    let expected = generate_signature(token, path, username, operation, signature_expiration)
        .map_err(|e| format!("error generating signing key: {e}"))?;

    if expected != sig {
        return Err("invalid signature".into());
    }

    if let Some(exp) = signature_expiration {
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs() as i64;
        if exp < now {
            return Err("signature is already expired".into());
        }
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_token(val: &[u8]) -> SecureToken {
        let t = SecureToken::new();
        t.set(val).unwrap();
        t
    }

    fn far_future() -> i64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs() as i64
            + 3600
    }

    #[test]
    fn generate_starts_with_v1() {
        let token = test_token(b"secret");
        let sig = generate_signature(&token, "/file", "root", READ_OPERATION, None).unwrap();
        assert!(sig.starts_with("v1_"));
    }

    #[test]
    fn generate_deterministic() {
        let token = test_token(b"secret");
        let s1 = generate_signature(&token, "/file", "root", READ_OPERATION, None).unwrap();
        let s2 = generate_signature(&token, "/file", "root", READ_OPERATION, None).unwrap();
        assert_eq!(s1, s2);
    }

    #[test]
    fn generate_with_expiration_differs() {
        let token = test_token(b"secret");
        let without = generate_signature(&token, "/f", "u", READ_OPERATION, None).unwrap();
        let with = generate_signature(&token, "/f", "u", READ_OPERATION, Some(9999)).unwrap();
        assert_ne!(without, with);
    }

    #[test]
    fn generate_unset_token_errors() {
        let token = SecureToken::new();
        assert!(generate_signature(&token, "/f", "u", READ_OPERATION, None).is_err());
    }

    #[test]
    fn validate_no_token_set_passes() {
        let token = SecureToken::new();
        assert!(validate_signing(&token, None, None, None, "root", "/f", READ_OPERATION).is_ok());
    }

    #[test]
    fn validate_correct_header_token() {
        let token = test_token(b"secret");
        assert!(
            validate_signing(
                &token,
                Some("secret"),
                None,
                None,
                "root",
                "/f",
                READ_OPERATION
            )
            .is_ok()
        );
    }

    #[test]
    fn validate_wrong_header_token() {
        let token = test_token(b"secret");
        let result = validate_signing(
            &token,
            Some("wrong"),
            None,
            None,
            "root",
            "/f",
            READ_OPERATION,
        );
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("does not match"));
    }

    #[test]
    fn validate_valid_signature() {
        let token = test_token(b"secret");
        let exp = far_future();
        let sig = generate_signature(&token, "/file", "root", READ_OPERATION, Some(exp)).unwrap();
        assert!(
            validate_signing(
                &token,
                None,
                Some(&sig),
                Some(exp),
                "root",
                "/file",
                READ_OPERATION
            )
            .is_ok()
        );
    }

    #[test]
    fn validate_invalid_signature() {
        let token = test_token(b"secret");
        let result = validate_signing(
            &token,
            None,
            Some("v1_bad"),
            Some(far_future()),
            "root",
            "/f",
            READ_OPERATION,
        );
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("invalid signature"));
    }

    #[test]
    fn validate_expired_signature() {
        let token = test_token(b"secret");
        let expired: i64 = 1_000_000;
        let sig = generate_signature(&token, "/f", "root", READ_OPERATION, Some(expired)).unwrap();
        let result = validate_signing(
            &token,
            None,
            Some(&sig),
            Some(expired),
            "root",
            "/f",
            READ_OPERATION,
        );
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("expired"));
    }

    #[test]
    fn validate_missing_signature() {
        let token = test_token(b"secret");
        let result = validate_signing(&token, None, None, None, "root", "/f", READ_OPERATION);
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("missing signature"));
    }

    #[test]
    fn validate_empty_header_token_falls_through_to_signature() {
        let token = test_token(b"secret");
        let result = validate_signing(&token, Some(""), None, None, "root", "/f", READ_OPERATION);
        assert!(result.is_err());
        assert!(result.unwrap_err().contains("missing signature"));
    }

    #[test]
    fn validate_valid_signature_no_expiration() {
        let token = test_token(b"secret");
        let sig = generate_signature(&token, "/file", "root", READ_OPERATION, None).unwrap();
        assert!(
            validate_signing(
                &token,
                None,
                Some(&sig),
                None,
                "root",
                "/file",
                READ_OPERATION
            )
            .is_ok()
        );
    }

    #[test]
    fn different_operations_produce_different_signatures() {
        let token = test_token(b"secret");
        let r = generate_signature(&token, "/f", "root", READ_OPERATION, None).unwrap();
        let w = generate_signature(&token, "/f", "root", WRITE_OPERATION, None).unwrap();
        assert_ne!(r, w);
    }
}
