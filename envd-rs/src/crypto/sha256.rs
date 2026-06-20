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

    const VECTORS: &[(&[u8], &str)] = &[
        (b"", "47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU"),
        (b"abc", "ungWv48Bz+pBQUDeXa4iI7ADYaOWF3qctBD/YfIAFa0"),
        (
            b"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq",
            "JI1qYdIGOLjlwCaTDD5gOaM85Flk/yFn9uzt1BnbBsE",
        ),
    ];

    #[test]
    fn known_answer_with_prefix() {
        for (input, expected_b64) in VECTORS {
            let result = hash(input);
            assert_eq!(
                result,
                format!("$sha256${expected_b64}"),
                "input: {:?}",
                String::from_utf8_lossy(input)
            );
        }
    }

    #[test]
    fn known_answer_without_prefix() {
        for (input, expected_b64) in VECTORS {
            let result = hash_without_prefix(input);
            assert_eq!(
                result,
                *expected_b64,
                "input: {:?}",
                String::from_utf8_lossy(input)
            );
        }
    }

    #[test]
    fn no_base64_padding() {
        for (input, _) in VECTORS {
            assert!(!hash(input).contains('='));
            assert!(!hash_without_prefix(input).contains('='));
        }
    }

    #[test]
    fn deterministic() {
        assert_eq!(hash(b"test"), hash(b"test"));
    }
}
