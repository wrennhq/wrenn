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
