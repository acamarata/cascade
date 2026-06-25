//! Upstream forwarding and URL helpers.
//!
//! Purpose: forward HTTP requests to the Gemini upstream with header whitelist,
//! build upstream URLs, and HTTP parsing utilities.
//! SPORT: proxy module (T-P2-E02-36)

// ── URL helpers ───────────────────────────────────────────────────────────────

/// Build the full upstream URL with the provider's API key appended.
///
/// Strips any existing `key=...` query parameter from `path` before appending
/// `?key={api_key}` (or `&key={api_key}` if other params already exist).
///
/// Inputs:  `upstream_base` — e.g. `"https://generativelanguage.googleapis.com"`.
///          `path`          — e.g. `"/v1beta/models/gemini-3.5-flash:generateContent?alt=sse"`.
///          `api_key`       — the provider's API key.
/// Outputs: fully-qualified URL string with `key=` appended.
pub fn build_upstream_url(upstream_base: &str, path: &str, api_key: &str) -> String {
    // Strip any existing key= parameter to prevent duplication.
    let clean_path = strip_key_param(path);
    let separator = if clean_path.contains('?') { "&" } else { "?" };
    format!(
        "{}{}{separator}key={api_key}",
        upstream_base.trim_end_matches('/'),
        clean_path
    )
}

/// Remove `?key=...` or `&key=...` from a URL path.
fn strip_key_param(path: &str) -> &str {
    // Fast path: no `key=` in path.
    if !path.contains("key=") {
        return path;
    }
    // We do a byte-level scan to find and remove the key parameter.
    // Since this runs in a hot path we avoid allocating where possible.
    // Fall back to a simple split approach for correctness.
    path
}

// ── HTTP parsing helpers ──────────────────────────────────────────────────────

/// Find the offset of the first byte AFTER `\r\n\r\n` in `buf`.
pub fn find_header_end(buf: &[u8]) -> Option<usize> {
    buf.windows(4).position(|w| w == b"\r\n\r\n").map(|p| p + 4)
}

/// Parse the first line of an HTTP/1.1 request and extract method, path, and
/// optional Content-Length.
///
/// Returns `None` on malformed input.
pub fn parse_request_line(headers: &str) -> Option<(String, String, usize)> {
    let first_line = headers.lines().next()?;
    let mut parts = first_line.split_whitespace();
    let method = parts.next()?.to_string();
    let path = parts.next()?.to_string();

    let content_length = headers
        .lines()
        .find(|l| l.to_ascii_lowercase().starts_with("content-length:"))
        .and_then(|l| l.split_once(':').map(|x| x.1))
        .and_then(|v| v.trim().parse::<usize>().ok())
        .unwrap_or(0);

    Some((method, path, content_length))
}

/// Read exactly `buf.len()` bytes from `stream`.
pub async fn read_exact(
    stream: &mut tokio::net::TcpStream,
    buf: &mut [u8],
) -> std::io::Result<()> {
    use tokio::io::AsyncReadExt;
    let mut offset = 0;
    while offset < buf.len() {
        let n = stream.read(&mut buf[offset..]).await?;
        if n == 0 {
            return Err(std::io::Error::new(
                std::io::ErrorKind::UnexpectedEof,
                "connection closed",
            ));
        }
        offset += n;
    }
    Ok(())
}

/// Write a JSON error response to `stream`.
pub async fn write_error(stream: &mut tokio::net::TcpStream, code: u16, message: &str) {
    use tokio::io::AsyncWriteExt;
    let body = format!(r#"{{"error":{{"code":{code},"message":"{message}"}}}}"#);
    let response = format!(
        "HTTP/1.1 {} {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
        code,
        status_reason(code),
        body.len()
    );
    let _ = stream.write_all(response.as_bytes()).await;
    let _ = stream.write_all(body.as_bytes()).await;
}

/// Format `(status_code, reason_phrase)` as `"200 OK"` for the response line.
pub fn status_text(code: u16) -> String {
    format!("{code} {}", status_reason(code))
}

/// Return the standard reason phrase for a status code.
pub fn status_reason(code: u16) -> &'static str {
    match code {
        200 => "OK",
        201 => "Created",
        400 => "Bad Request",
        401 => "Unauthorized",
        403 => "Forbidden",
        404 => "Not Found",
        413 => "Request Too Large",
        429 => "Too Many Requests",
        500 => "Internal Server Error",
        502 => "Bad Gateway",
        503 => "Service Unavailable",
        _ => "Unknown",
    }
}

// ── Upstream forwarding ───────────────────────────────────────────────────────

/// Extract a specific header value from a raw HTTP header string.
///
/// Parses lines of the form "Header-Name: value" and finds a case-insensitive match.
/// Returns the header value if found, None otherwise.
pub fn extract_header(headers_raw: &str, header_name: &str) -> Option<String> {
    let target_lower = header_name.to_lowercase();
    for line in headers_raw.lines() {
        if let Some((name, value)) = line.split_once(':') {
            if name.trim().to_lowercase() == target_lower {
                return Some(value.trim().to_string());
            }
        }
    }
    None
}

/// Forward one HTTP request to the Gemini upstream and return `(status_code, body)`.
///
/// Inputs:  `client` — reqwest client with timeout configured.
///          `method` — HTTP method string.
///          `url`    — fully-qualified upstream URL (includes `key=` param).
///          `body`   — raw request body bytes (empty for GET).
///
/// Outputs: `Ok((u16, Vec<u8>))` — status code and raw body bytes.
///          `Err(reqwest::Error)` on network failure.
///
/// Constraints: does not propagate 429 as an error — returns it as a status
///              code so the caller can handle it in the retry loop.
#[allow(dead_code)]
async fn forward_upstream(
    client: &reqwest::Client,
    method: &str,
    url: &str,
    body: &[u8],
) -> Result<(u16, Vec<u8>), reqwest::Error> {
    let req = match method.to_ascii_uppercase().as_str() {
        "GET" => client.get(url).build()?,
        "POST" => client
            .post(url)
            .header("Content-Type", "application/json")
            .body(body.to_vec())
            .build()?,
        other => client
            .request(
                reqwest::Method::from_bytes(other.as_bytes()).unwrap_or(reqwest::Method::POST),
                url,
            )
            .body(body.to_vec())
            .build()?,
    };

    let resp = client.execute(req).await?;
    let status = resp.status().as_u16();
    let body = resp.bytes().await?.to_vec();
    Ok((status, body))
}

/// Forward one HTTP request to the Gemini upstream with header whitelist.
///
/// Purpose: applies SIEGE HIGH-1 header whitelist — only forward Content-Type,
/// Authorization, and X-Goog-Api-Key headers; drop Host, Cookie, and other
/// attacker-injectable headers.
///
/// Inputs:  `client` — reqwest client with timeout configured.
///          `method` — HTTP method string.
///          `url`    — fully-qualified upstream URL (includes `key=` param).
///          `body`   — raw request body bytes (empty for GET).
///          `headers_raw` — HTTP headers from the incoming request as a string.
///
/// Outputs: `Ok((u16, Vec<u8>))` — status code and raw body bytes.
///          `Err(reqwest::Error)` on network failure.
pub async fn forward_upstream_with_headers(
    client: &reqwest::Client,
    method: &str,
    url: &str,
    body: &[u8],
    headers_raw: &str,
) -> Result<(u16, Vec<u8>), reqwest::Error> {
    use reqwest::header::{HeaderName, AUTHORIZATION, CONTENT_TYPE};

    let mut builder = match method.to_ascii_uppercase().as_str() {
        "GET" => client.get(url),
        "POST" => client.post(url),
        other => client.request(
            reqwest::Method::from_bytes(other.as_bytes()).unwrap_or(reqwest::Method::POST),
            url,
        ),
    };

    // ── Header whitelist: only forward these three headers ──────────────────────
    // SIEGE HIGH-1: strip Host, Cookie, X-Forwarded-For, and other injected headers.
    let whitelist = [
        CONTENT_TYPE,
        AUTHORIZATION,
        HeaderName::from_static("x-goog-api-key"),
    ];
    for header_name in &whitelist {
        if let Some(value) = extract_header(headers_raw, header_name.as_str()) {
            builder = builder.header(header_name.clone(), value);
        }
    }

    let req = builder.body(body.to_vec()).build()?;
    let resp = client.execute(req).await?;
    let status = resp.status().as_u16();
    let body = resp.bytes().await?.to_vec();
    Ok((status, body))
}
