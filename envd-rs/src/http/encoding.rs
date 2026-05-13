use axum::http::Request;

const ENCODING_GZIP: &str = "gzip";
const ENCODING_IDENTITY: &str = "identity";
const ENCODING_WILDCARD: &str = "*";

const SUPPORTED_ENCODINGS: &[&str] = &[ENCODING_GZIP];

struct EncodingWithQuality {
    encoding: String,
    quality: f64,
}

fn parse_encoding_with_quality(value: &str) -> EncodingWithQuality {
    let value = value.trim();
    let mut quality = 1.0;

    if let Some(idx) = value.find(';') {
        let params = &value[idx + 1..];
        let enc = value[..idx].trim();
        for param in params.split(';') {
            let param = param.trim();
            if let Some(stripped) = param.strip_prefix("q=").or_else(|| param.strip_prefix("Q=")) {
                if let Ok(q) = stripped.parse::<f64>() {
                    quality = q;
                }
            }
        }
        return EncodingWithQuality {
            encoding: enc.to_ascii_lowercase(),
            quality,
        };
    }

    EncodingWithQuality {
        encoding: value.to_ascii_lowercase(),
        quality,
    }
}

fn parse_accept_encoding_header(header: &str) -> (Vec<EncodingWithQuality>, bool) {
    if header.is_empty() {
        return (Vec::new(), false);
    }

    let encodings: Vec<EncodingWithQuality> =
        header.split(',').map(|v| parse_encoding_with_quality(v)).collect();

    let mut identity_rejected = false;
    let mut identity_explicitly_accepted = false;
    let mut wildcard_rejected = false;

    for eq in &encodings {
        match eq.encoding.as_str() {
            ENCODING_IDENTITY => {
                if eq.quality == 0.0 {
                    identity_rejected = true;
                } else {
                    identity_explicitly_accepted = true;
                }
            }
            ENCODING_WILDCARD => {
                if eq.quality == 0.0 {
                    wildcard_rejected = true;
                }
            }
            _ => {}
        }
    }

    if wildcard_rejected && !identity_explicitly_accepted {
        identity_rejected = true;
    }

    (encodings, identity_rejected)
}

pub fn is_identity_acceptable<B>(r: &Request<B>) -> bool {
    let header = r
        .headers()
        .get("accept-encoding")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    let (_, rejected) = parse_accept_encoding_header(header);
    !rejected
}

pub fn parse_accept_encoding<B>(r: &Request<B>) -> Result<&'static str, String> {
    let header = r
        .headers()
        .get("accept-encoding")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");

    if header.is_empty() {
        return Ok(ENCODING_IDENTITY);
    }

    let (mut encodings, identity_rejected) = parse_accept_encoding_header(header);
    encodings.sort_by(|a, b| b.quality.partial_cmp(&a.quality).unwrap_or(std::cmp::Ordering::Equal));

    for eq in &encodings {
        if eq.quality == 0.0 {
            continue;
        }
        if eq.encoding == ENCODING_IDENTITY {
            return Ok(ENCODING_IDENTITY);
        }
        if eq.encoding == ENCODING_WILDCARD {
            if identity_rejected && !SUPPORTED_ENCODINGS.is_empty() {
                return Ok(SUPPORTED_ENCODINGS[0]);
            }
            return Ok(ENCODING_IDENTITY);
        }
        if eq.encoding == ENCODING_GZIP {
            return Ok(ENCODING_GZIP);
        }
    }

    if !identity_rejected {
        return Ok(ENCODING_IDENTITY);
    }

    Err(format!("no acceptable encoding found, supported: {SUPPORTED_ENCODINGS:?}"))
}

pub fn parse_content_encoding<B>(r: &Request<B>) -> Result<&'static str, String> {
    let header = r
        .headers()
        .get("content-encoding")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");

    if header.is_empty() {
        return Ok(ENCODING_IDENTITY);
    }

    let encoding = header.trim().to_ascii_lowercase();
    if encoding == ENCODING_IDENTITY {
        return Ok(ENCODING_IDENTITY);
    }
    if SUPPORTED_ENCODINGS.contains(&encoding.as_str()) {
        return Ok(ENCODING_GZIP);
    }

    Err(format!("unsupported Content-Encoding: {header}, supported: {SUPPORTED_ENCODINGS:?}"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::Request;

    fn req_with_accept(v: &str) -> Request<()> {
        Request::builder()
            .header("accept-encoding", v)
            .body(())
            .unwrap()
    }

    fn req_with_content(v: &str) -> Request<()> {
        Request::builder()
            .header("content-encoding", v)
            .body(())
            .unwrap()
    }

    fn req_no_headers() -> Request<()> {
        Request::builder().body(()).unwrap()
    }

    // parse_encoding_with_quality

    #[test]
    fn encoding_quality_default_1() {
        let eq = parse_encoding_with_quality("gzip");
        assert_eq!(eq.encoding, "gzip");
        assert_eq!(eq.quality, 1.0);
    }

    #[test]
    fn encoding_quality_explicit() {
        let eq = parse_encoding_with_quality("gzip;q=0.8");
        assert_eq!(eq.encoding, "gzip");
        assert_eq!(eq.quality, 0.8);
    }

    #[test]
    fn encoding_quality_case_insensitive() {
        let eq = parse_encoding_with_quality("GZIP;Q=0.5");
        assert_eq!(eq.encoding, "gzip");
        assert_eq!(eq.quality, 0.5);
    }

    #[test]
    fn encoding_quality_zero() {
        let eq = parse_encoding_with_quality("gzip;q=0");
        assert_eq!(eq.quality, 0.0);
    }

    #[test]
    fn encoding_quality_whitespace_trimmed() {
        let eq = parse_encoding_with_quality("  gzip ; q=0.9  ");
        assert_eq!(eq.encoding, "gzip");
        assert_eq!(eq.quality, 0.9);
    }

    // parse_accept_encoding_header

    #[test]
    fn accept_header_empty() {
        let (encs, rejected) = parse_accept_encoding_header("");
        assert!(encs.is_empty());
        assert!(!rejected);
    }

    #[test]
    fn accept_header_identity_q0_rejects() {
        let (_, rejected) = parse_accept_encoding_header("identity;q=0");
        assert!(rejected);
    }

    #[test]
    fn accept_header_wildcard_q0_rejects_identity() {
        let (_, rejected) = parse_accept_encoding_header("*;q=0");
        assert!(rejected);
    }

    #[test]
    fn accept_header_wildcard_q0_but_identity_explicit_accepted() {
        let (_, rejected) = parse_accept_encoding_header("*;q=0, identity");
        assert!(!rejected);
    }

    // parse_accept_encoding (full)

    #[test]
    fn accept_encoding_no_header_returns_identity() {
        assert_eq!(parse_accept_encoding(&req_no_headers()).unwrap(), "identity");
    }

    #[test]
    fn accept_encoding_gzip() {
        assert_eq!(parse_accept_encoding(&req_with_accept("gzip")).unwrap(), "gzip");
    }

    #[test]
    fn accept_encoding_identity_explicit() {
        assert_eq!(parse_accept_encoding(&req_with_accept("identity")).unwrap(), "identity");
    }

    #[test]
    fn accept_encoding_gzip_higher_quality() {
        assert_eq!(
            parse_accept_encoding(&req_with_accept("identity;q=0.1, gzip;q=0.9")).unwrap(),
            "gzip"
        );
    }

    #[test]
    fn accept_encoding_wildcard_returns_identity() {
        assert_eq!(parse_accept_encoding(&req_with_accept("*")).unwrap(), "identity");
    }

    #[test]
    fn accept_encoding_wildcard_identity_rejected_returns_gzip() {
        assert_eq!(
            parse_accept_encoding(&req_with_accept("identity;q=0, *")).unwrap(),
            "gzip"
        );
    }

    #[test]
    fn accept_encoding_all_rejected_errors() {
        assert!(parse_accept_encoding(&req_with_accept("identity;q=0, *;q=0")).is_err());
    }

    #[test]
    fn accept_encoding_unsupported_only_falls_to_identity() {
        assert_eq!(parse_accept_encoding(&req_with_accept("br")).unwrap(), "identity");
    }

    // is_identity_acceptable

    #[test]
    fn identity_acceptable_no_header() {
        assert!(is_identity_acceptable(&req_no_headers()));
    }

    #[test]
    fn identity_acceptable_gzip_only() {
        assert!(is_identity_acceptable(&req_with_accept("gzip")));
    }

    #[test]
    fn identity_not_acceptable_identity_q0() {
        assert!(!is_identity_acceptable(&req_with_accept("identity;q=0")));
    }

    #[test]
    fn identity_not_acceptable_wildcard_q0() {
        assert!(!is_identity_acceptable(&req_with_accept("*;q=0")));
    }

    #[test]
    fn identity_acceptable_wildcard_q0_but_identity_explicit() {
        assert!(is_identity_acceptable(&req_with_accept("*;q=0, identity")));
    }

    // parse_content_encoding

    #[test]
    fn content_encoding_empty_returns_identity() {
        assert_eq!(parse_content_encoding(&req_no_headers()).unwrap(), "identity");
    }

    #[test]
    fn content_encoding_gzip() {
        assert_eq!(parse_content_encoding(&req_with_content("gzip")).unwrap(), "gzip");
    }

    #[test]
    fn content_encoding_identity_explicit() {
        assert_eq!(parse_content_encoding(&req_with_content("identity")).unwrap(), "identity");
    }

    #[test]
    fn content_encoding_unsupported_errors() {
        assert!(parse_content_encoding(&req_with_content("br")).is_err());
    }

    #[test]
    fn content_encoding_case_insensitive() {
        assert_eq!(parse_content_encoding(&req_with_content("GZIP")).unwrap(), "gzip");
    }
}
