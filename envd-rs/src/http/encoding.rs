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
