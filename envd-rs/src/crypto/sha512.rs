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

    const VECTORS: &[(&str, &str)] = &[
        ("", "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"),
        ("abc", "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f"),
        ("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq", "204a8fc6dda82f0a0ced7beb8e08a41657c16ef468b228a8279be331a703c33596fd15c13b1b07f9aa1d3bea57789ca031ad85c7a71dd70354ec631238ca3445"),
    ];

    #[test]
    fn known_answer() {
        for (input, expected) in VECTORS {
            assert_eq!(hash_access_token(input), *expected, "input: {input:?}");
        }
    }

    #[test]
    fn str_and_bytes_agree() {
        for (input, _) in VECTORS {
            assert_eq!(hash_access_token(input), hash_access_token_bytes(input.as_bytes()));
        }
    }

    #[test]
    fn output_is_lowercase_hex_128_chars() {
        let h = hash_access_token("anything");
        assert_eq!(h.len(), 128);
        assert!(h.chars().all(|c| c.is_ascii_hexdigit() && !c.is_ascii_uppercase()));
    }
}
