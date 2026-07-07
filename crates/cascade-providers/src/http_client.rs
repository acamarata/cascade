//! Shared HTTP client utility for cascade provider adapters.
//!
//! # Purpose
//!
//! Provides a single `CascadeHttpClient` wrapper around `reqwest::Client` with
//! consistent configuration (30 s timeout, user-agent, connection pool) and
//! automatic retry on 429/503 responses (up to 3 retries, exponential backoff
//! starting at 1 s, reading `Retry-After` when present).
//!
//! # Design
//!
//! - One shared `CascadeHttpClient` per adapter instance; clone it cheaply
//!   (`reqwest::Client` is `Arc`-backed internally).
//! - Credentials are passed at call time, never stored in this struct.
//! - Error variants follow `ProviderError` — callers do not inspect
//!   raw `reqwest::Error` or HTTP status codes.
//!
//! # Constraints
//!
//! - No credential value is ever written to a log line or `Debug` output.
//! - `Retry-After` is parsed as seconds (integer) or HTTP-date; if parsing
//!   fails, the wait is derived from the soonest Anthropic
//!   `anthropic-ratelimit-{requests,tokens}-reset` header (RFC 3339
//!   timestamp or bare seconds); if that also fails the default exponential
//!   backoff is used. Header parsing never panics — malformed or absent
//!   headers always fall back safely.
//! - SSE helpers yield raw `data:` payload strings; JSON decoding is the
//!   caller's responsibility.

use std::time::Duration;

use reqwest::{
    header::{self, HeaderMap, HeaderValue},
    Client, RequestBuilder, Response, StatusCode,
};
use serde::Serialize;
use tokio::time::sleep;
use tracing::warn;

use crate::error::ProviderError;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Package version injected via `CARGO_PKG_VERSION` at compile time.
const PKG_VERSION: &str = env!("CARGO_PKG_VERSION");

/// HTTP timeout applied to every request.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(30);

/// Maximum number of retries on 429/503.
const MAX_RETRIES: u32 = 3;

/// Base backoff delay (doubles each retry: 1 s, 2 s, 4 s).
const BASE_BACKOFF_SECS: u64 = 1;

// ── CascadeHttpClient ─────────────────────────────────────────────────────────

/// A shared HTTP client with retry logic for AI provider adapters.
///
/// ## Usage
///
/// ```rust,ignore
/// let client = CascadeHttpClient::new();
/// let resp: MyStruct = client.post_json(
///     "https://api.example.com/v1/complete",
///     Some("sk-my-secret-key"),
///     AuthStyle::Bearer,
///     &request_body,
/// ).await?;
/// ```
///
/// ## Thread safety
///
/// `CascadeHttpClient` is `Clone + Send + Sync` — safe to share across
/// async tasks via `Arc` or direct cloning.
#[derive(Clone, Debug)]
pub struct CascadeHttpClient {
    inner: Client,
}

impl CascadeHttpClient {
    /// Construct a new client with cascade-standard configuration.
    ///
    /// Panics only if the TLS backend cannot be initialised (which implies a
    /// mis-linked binary — not a runtime condition).
    pub fn new() -> Self {
        let mut default_headers = HeaderMap::new();
        let ua = format!("cascade/{PKG_VERSION}");
        default_headers.insert(
            header::USER_AGENT,
            HeaderValue::from_str(&ua).expect("user-agent is valid ASCII"),
        );

        let inner = Client::builder()
            .timeout(REQUEST_TIMEOUT)
            .default_headers(default_headers)
            .build()
            .expect("reqwest Client construction failed — TLS backend missing?");

        Self { inner }
    }

    // ── Auth header helpers ───────────────────────────────────────────────────

    /// Apply a `Authorization: Bearer <token>` header.
    ///
    /// The token value is *not* logged at any tracing level.
    pub fn apply_bearer(builder: RequestBuilder, token: &str) -> RequestBuilder {
        builder.header(header::AUTHORIZATION, format!("Bearer {token}"))
    }

    /// Apply a `x-api-key: <key>` header (used by Anthropic).
    ///
    /// The key value is *not* logged at any tracing level.
    pub fn apply_api_key(builder: RequestBuilder, key: &str) -> RequestBuilder {
        builder.header("x-api-key", key)
    }

    // ── Core send + retry ─────────────────────────────────────────────────────

    /// Execute a prepared `RequestBuilder` with automatic retry on 429/503.
    ///
    /// The builder must be cloneable (i.e. no streaming body). Callers that
    /// need to attach a body should use `post_json` which handles cloning.
    ///
    /// Returns the final successful `Response`, or a `ProviderError` if all
    /// retries are exhausted or a non-retryable error is received.
    async fn send_with_retry(
        &self,
        make_req: impl Fn() -> RequestBuilder,
    ) -> Result<Response, ProviderError> {
        let mut attempt = 0u32;

        loop {
            let response = make_req().send().await.map_err(|e| {
                if e.is_timeout() {
                    ProviderError::Timeout {
                        secs: REQUEST_TIMEOUT.as_secs(),
                    }
                } else {
                    ProviderError::NetworkError(e.to_string())
                }
            })?;

            let status = response.status();

            // ── Success ───────────────────────────────────────────────────────
            if status.is_success() {
                return Ok(response);
            }

            // ── Auth errors ───────────────────────────────────────────────────
            if status == StatusCode::UNAUTHORIZED || status == StatusCode::FORBIDDEN {
                return Err(ProviderError::AuthFailed(format!(
                    "HTTP {status} from provider"
                )));
            }

            // ── Rate-limit / server errors (retryable) ────────────────────────
            let is_rate_limit = status == StatusCode::TOO_MANY_REQUESTS;
            let is_server_err = status.is_server_error();

            if (is_rate_limit || is_server_err) && attempt < MAX_RETRIES {
                let wait = if is_rate_limit {
                    parse_retry_after(&response).unwrap_or_else(|| backoff_duration(attempt))
                } else {
                    backoff_duration(attempt)
                };

                warn!(
                    attempt = attempt + 1,
                    max = MAX_RETRIES,
                    status = status.as_u16(),
                    wait_secs = wait.as_secs_f32(),
                    "retryable HTTP error; backing off",
                );

                sleep(wait).await;
                attempt += 1;
                continue;
            }

            // ── Non-retryable server error ────────────────────────────────────
            if is_server_err {
                return Err(ProviderError::NetworkError(format!(
                    "server error HTTP {status} after {attempt} retries"
                )));
            }

            // ── Rate-limit exhausted ──────────────────────────────────────────
            if is_rate_limit {
                let retry_after = parse_retry_after(&response);
                return Err(ProviderError::RateLimited {
                    retry_after_secs: retry_after.map(|d| d.as_secs()),
                });
            }

            // ── Other 4xx ────────────────────────────────────────────────────
            return Err(ProviderError::NetworkError(format!(
                "unexpected HTTP {status}"
            )));
        }
    }

    // ── JSON POST ─────────────────────────────────────────────────────────────

    /// POST a JSON-serializable body and deserialize the JSON response.
    ///
    /// `auth_fn` is called with the `RequestBuilder` to attach credentials —
    /// use [`Self::apply_bearer`] or [`Self::apply_api_key`].  The closure
    /// must not capture the secret by reference across an `await`; own it.
    ///
    /// # Errors
    ///
    /// Returns `ProviderError::InvalidResponse` if the body cannot be
    /// deserialized into `R`.
    pub async fn post_json<B, R, F>(
        &self,
        url: &str,
        body: &B,
        auth_fn: F,
    ) -> Result<R, ProviderError>
    where
        B: Serialize + Sync,
        R: serde::de::DeserializeOwned,
        F: Fn(RequestBuilder) -> RequestBuilder,
    {
        let body_bytes =
            serde_json::to_vec(body).map_err(|e| ProviderError::InvalidResponse(e.to_string()))?;

        let response = self
            .send_with_retry(|| {
                let builder = self
                    .inner
                    .post(url)
                    .header(header::CONTENT_TYPE, "application/json")
                    .body(body_bytes.clone());
                auth_fn(builder)
            })
            .await?;

        let bytes = response
            .bytes()
            .await
            .map_err(|e| ProviderError::NetworkError(e.to_string()))?;

        serde_json::from_slice::<R>(&bytes)
            .map_err(|e| ProviderError::InvalidResponse(e.to_string()))
    }

    /// GET a URL and deserialize the JSON response.
    pub async fn get_json<R, F>(&self, url: &str, auth_fn: F) -> Result<R, ProviderError>
    where
        R: serde::de::DeserializeOwned,
        F: Fn(RequestBuilder) -> RequestBuilder,
    {
        let response = self
            .send_with_retry(|| auth_fn(self.inner.get(url)))
            .await?;

        let bytes = response
            .bytes()
            .await
            .map_err(|e| ProviderError::NetworkError(e.to_string()))?;

        serde_json::from_slice::<R>(&bytes)
            .map_err(|e| ProviderError::InvalidResponse(e.to_string()))
    }

    // ── SSE streaming ─────────────────────────────────────────────────────────

    /// POST a JSON body and return the raw `Response` for SSE streaming.
    ///
    /// Callers consume the body as an async byte stream and parse SSE lines
    /// themselves.  This method performs no retry — streaming requests are
    /// inherently non-idempotent after the first byte.
    pub async fn post_sse<B, F>(
        &self,
        url: &str,
        body: &B,
        auth_fn: F,
    ) -> Result<Response, ProviderError>
    where
        B: Serialize + Sync,
        F: Fn(RequestBuilder) -> RequestBuilder,
    {
        let body_bytes =
            serde_json::to_vec(body).map_err(|e| ProviderError::InvalidResponse(e.to_string()))?;

        let response = auth_fn(
            self.inner
                .post(url)
                .header(header::CONTENT_TYPE, "application/json")
                .header(header::ACCEPT, "text/event-stream")
                .body(body_bytes),
        )
        .send()
        .await
        .map_err(|e| {
            if e.is_timeout() {
                ProviderError::Timeout {
                    secs: REQUEST_TIMEOUT.as_secs(),
                }
            } else {
                ProviderError::NetworkError(e.to_string())
            }
        })?;

        let status = response.status();
        if status == StatusCode::UNAUTHORIZED || status == StatusCode::FORBIDDEN {
            return Err(ProviderError::AuthFailed(format!("HTTP {status}")));
        }
        if status == StatusCode::TOO_MANY_REQUESTS {
            let retry_after = parse_retry_after(&response);
            return Err(ProviderError::RateLimited {
                retry_after_secs: retry_after.map(|d| d.as_secs()),
            });
        }
        if status.is_server_error() {
            return Err(ProviderError::NetworkError(format!(
                "server error HTTP {status}"
            )));
        }
        if !status.is_success() {
            return Err(ProviderError::NetworkError(format!("HTTP {status}")));
        }

        Ok(response)
    }

    /// Parse a raw SSE line and return the `data:` payload, if any.
    ///
    /// - `"data: [DONE]"` returns `None` (stream-end sentinel).
    /// - Comment lines (`":..."`) and empty lines return `None`.
    /// - All other `"data: <payload>"` lines return `Some(payload)`.
    pub fn parse_sse_line(line: &str) -> Option<&str> {
        let line = line.trim_end_matches(['\r', '\n']);
        if line.is_empty() || line.starts_with(':') {
            return None;
        }
        let payload = line.strip_prefix("data: ")?;
        if payload == "[DONE]" {
            None
        } else {
            Some(payload)
        }
    }
}

impl Default for CascadeHttpClient {
    fn default() -> Self {
        Self::new()
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

/// Names of the Anthropic rate-limit reset headers, checked in order when
/// `retry-after` is absent. The soonest (minimum) parseable reset wins.
const RATELIMIT_RESET_HEADERS: [&str; 2] = [
    "anthropic-ratelimit-requests-reset",
    "anthropic-ratelimit-tokens-reset",
];

/// Derive the wait duration for a rate-limited response.
///
/// Prefers an explicit `Retry-After` header (seconds or HTTP-date). If that
/// header is absent or unparseable, falls back to the soonest of the
/// Anthropic `anthropic-ratelimit-{requests,tokens}-reset` headers (RFC 3339
/// timestamp or bare seconds). Returns `None` if no header yields a usable,
/// positive wait — callers fall back to exponential backoff. Never panics on
/// malformed input.
fn parse_retry_after(response: &Response) -> Option<Duration> {
    parse_retry_after_headers(response.headers())
}

/// Header-map-driven core of [`parse_retry_after`], split out for direct
/// unit testing without constructing a live `reqwest::Response`.
fn parse_retry_after_headers(headers: &HeaderMap) -> Option<Duration> {
    if let Some(wait) = parse_retry_after_header(headers) {
        return Some(wait);
    }
    parse_ratelimit_reset_headers(headers)
}

/// Parse the `Retry-After` header as a `Duration`.
///
/// Handles both integer-seconds (`Retry-After: 30`) and HTTP-date formats
/// (`Retry-After: Wed, 21 Oct 2015 07:28:00 GMT`).  Returns `None` if the
/// header is absent or unparseable.
fn parse_retry_after_header(headers: &HeaderMap) -> Option<Duration> {
    let header_val = headers.get("retry-after")?;
    let s = header_val.to_str().ok()?;

    // Integer seconds path.
    if let Ok(secs) = s.trim().parse::<u64>() {
        return Some(Duration::from_secs(secs));
    }

    // HTTP-date path — parse with `chrono`.
    if let Ok(dt) = chrono::DateTime::parse_from_rfc2822(s) {
        let now = chrono::Utc::now();
        let wait = dt.with_timezone(&chrono::Utc) - now;
        if wait.num_seconds() > 0 {
            return Some(Duration::from_secs(wait.num_seconds() as u64));
        }
    }

    None
}

/// Derive a wait duration from the soonest Anthropic rate-limit reset header.
///
/// Checks each header in [`RATELIMIT_RESET_HEADERS`] and returns the
/// smallest positive duration found, or `None` if none parse. Each header
/// value may be an RFC 3339 timestamp (`reset_at - now`) or a bare integer
/// number of seconds to wait.
fn parse_ratelimit_reset_headers(headers: &HeaderMap) -> Option<Duration> {
    RATELIMIT_RESET_HEADERS
        .iter()
        .filter_map(|name| headers.get(*name))
        .filter_map(|v| v.to_str().ok())
        .filter_map(parse_reset_header_value)
        .min()
}

/// Parse a single rate-limit reset header value into a wait `Duration`.
///
/// Accepts a bare integer number of seconds or an RFC 3339 timestamp
/// (wait = timestamp - now). Returns `None` for malformed input or a
/// timestamp that has already passed.
fn parse_reset_header_value(s: &str) -> Option<Duration> {
    let s = s.trim();

    if let Ok(secs) = s.parse::<u64>() {
        return Some(Duration::from_secs(secs));
    }

    if let Ok(dt) = chrono::DateTime::parse_from_rfc3339(s) {
        let now = chrono::Utc::now();
        let wait = dt.with_timezone(&chrono::Utc) - now;
        if wait.num_seconds() > 0 {
            return Some(Duration::from_secs(wait.num_seconds() as u64));
        }
    }

    None
}

/// Exponential backoff: attempt 0 → 1 s, 1 → 2 s, 2 → 4 s, …
fn backoff_duration(attempt: u32) -> Duration {
    Duration::from_secs(BASE_BACKOFF_SECS << attempt.min(10))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    // ── parse_sse_line ────────────────────────────────────────────────────────

    #[test]
    fn sse_data_line_extracted() {
        assert_eq!(
            CascadeHttpClient::parse_sse_line("data: {\"delta\":\"hello\"}"),
            Some("{\"delta\":\"hello\"}")
        );
    }

    #[test]
    fn sse_done_sentinel_returns_none() {
        assert_eq!(CascadeHttpClient::parse_sse_line("data: [DONE]"), None);
    }

    #[test]
    fn sse_empty_line_returns_none() {
        assert_eq!(CascadeHttpClient::parse_sse_line(""), None);
    }

    #[test]
    fn sse_comment_line_returns_none() {
        assert_eq!(CascadeHttpClient::parse_sse_line(": keep-alive"), None);
    }

    #[test]
    fn sse_non_data_line_returns_none() {
        assert_eq!(
            CascadeHttpClient::parse_sse_line("event: content_block_delta"),
            None
        );
    }

    // ── parse_retry_after_headers ────────────────────────────────────────────

    #[test]
    fn retry_after_seconds_preferred_over_reset_headers() {
        let mut headers = HeaderMap::new();
        headers.insert("retry-after", HeaderValue::from_static("42"));
        // Far-future reset header — must be ignored since retry-after wins.
        headers.insert(
            "anthropic-ratelimit-requests-reset",
            HeaderValue::from_static("9999-01-01T00:00:00Z"),
        );
        assert_eq!(
            parse_retry_after_headers(&headers),
            Some(Duration::from_secs(42))
        );
    }

    #[test]
    fn reset_header_used_when_retry_after_absent() {
        let mut headers = HeaderMap::new();
        let soon = chrono::Utc::now() + chrono::Duration::seconds(30);
        headers.insert(
            "anthropic-ratelimit-requests-reset",
            HeaderValue::from_str(&soon.to_rfc3339()).unwrap(),
        );
        let wait =
            parse_retry_after_headers(&headers).expect("expected derived wait from reset header");
        // Allow slack for test execution time between now() calls.
        assert!(
            wait.as_secs() <= 30 && wait.as_secs() >= 28,
            "wait={wait:?}"
        );
    }

    #[test]
    fn soonest_of_two_reset_headers_wins() {
        let mut headers = HeaderMap::new();
        let soon = chrono::Utc::now() + chrono::Duration::seconds(10);
        let later = chrono::Utc::now() + chrono::Duration::seconds(100);
        headers.insert(
            "anthropic-ratelimit-requests-reset",
            HeaderValue::from_str(&later.to_rfc3339()).unwrap(),
        );
        headers.insert(
            "anthropic-ratelimit-tokens-reset",
            HeaderValue::from_str(&soon.to_rfc3339()).unwrap(),
        );
        let wait = parse_retry_after_headers(&headers).expect("expected derived wait");
        assert!(
            wait.as_secs() <= 10,
            "expected soonest reset header to win, got {wait:?}"
        );
    }

    #[test]
    fn bare_seconds_reset_header_supported() {
        let mut headers = HeaderMap::new();
        headers.insert(
            "anthropic-ratelimit-tokens-reset",
            HeaderValue::from_static("15"),
        );
        assert_eq!(
            parse_retry_after_headers(&headers),
            Some(Duration::from_secs(15))
        );
    }

    #[test]
    fn no_headers_present_falls_back_to_none() {
        let headers = HeaderMap::new();
        assert_eq!(parse_retry_after_headers(&headers), None);
    }

    #[test]
    fn malformed_headers_fall_back_safely_without_panic() {
        let mut headers = HeaderMap::new();
        headers.insert("retry-after", HeaderValue::from_static("not-a-number"));
        headers.insert(
            "anthropic-ratelimit-requests-reset",
            HeaderValue::from_static("garbage"),
        );
        headers.insert(
            "anthropic-ratelimit-tokens-reset",
            HeaderValue::from_static("also-garbage"),
        );
        assert_eq!(parse_retry_after_headers(&headers), None);
    }

    // ── backoff_duration ──────────────────────────────────────────────────────

    #[test]
    fn backoff_sequence() {
        assert_eq!(backoff_duration(0), Duration::from_secs(1));
        assert_eq!(backoff_duration(1), Duration::from_secs(2));
        assert_eq!(backoff_duration(2), Duration::from_secs(4));
    }

    // ── Error mapping — table-driven ──────────────────────────────────────────

    #[tokio::test]
    async fn get_json_returns_auth_failed_on_401() {
        let server = MockServer::start().await;
        Mock::given(method("GET"))
            .and(path("/"))
            .respond_with(ResponseTemplate::new(401))
            .mount(&server)
            .await;

        let client = CascadeHttpClient::new();
        let err = client
            .get_json::<serde_json::Value, _>(&server.uri(), |b| b)
            .await
            .unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    #[tokio::test]
    async fn get_json_returns_auth_failed_on_403() {
        let server = MockServer::start().await;
        Mock::given(method("GET"))
            .and(path("/"))
            .respond_with(ResponseTemplate::new(403))
            .mount(&server)
            .await;

        let client = CascadeHttpClient::new();
        let err = client
            .get_json::<serde_json::Value, _>(&server.uri(), |b| b)
            .await
            .unwrap_err();

        assert!(matches!(err, ProviderError::AuthFailed(_)));
    }

    #[tokio::test]
    async fn get_json_returns_rate_limited_with_retry_after_on_429() {
        let server = MockServer::start().await;
        // Mount one mock that always returns 429 with retry-after: 0
        // (instant retry). After MAX_RETRIES+1 attempts it will still be 429
        // so the client must return RateLimited.
        Mock::given(method("GET"))
            .and(path("/"))
            .respond_with(
                ResponseTemplate::new(429)
                    .append_header("retry-after", "0")
                    .append_header("content-length", "0"),
            )
            .mount(&server)
            .await;

        let client = CascadeHttpClient::new();
        let err = client
            .get_json::<serde_json::Value, _>(&server.uri(), |b| b)
            .await
            .unwrap_err();

        assert!(
            matches!(err, ProviderError::RateLimited { .. }),
            "expected RateLimited, got {err:?}"
        );
    }

    // ── Retry test: 429 twice then 200 ────────────────────────────────────────

    /// Core acceptance criterion: two 429s → eventually one 200 → Ok.
    ///
    /// Uses `retry-after: 0` on the 429 responses so the exponential backoff
    /// honours the header and sleeps for 0 seconds, making the test fast
    /// without requiring `tokio::time::pause()` (which conflicts with
    /// reqwest's internal clock).
    #[tokio::test]
    async fn retries_on_429_twice_then_succeeds() {
        let server = MockServer::start().await;

        // Two 429s with retry-after: 0 (instant resume).
        Mock::given(method("GET"))
            .and(path("/check"))
            .respond_with(
                ResponseTemplate::new(429)
                    .append_header("retry-after", "0")
                    .append_header("content-length", "0"),
            )
            .up_to_n_times(2)
            .mount(&server)
            .await;

        // Then a 200 with a JSON body.
        Mock::given(method("GET"))
            .and(path("/check"))
            .respond_with(ResponseTemplate::new(200).set_body_json(json!({"ok": true})))
            .mount(&server)
            .await;

        let client = CascadeHttpClient::new();
        let url = format!("{}/check", server.uri());
        let result = client.get_json::<serde_json::Value, _>(&url, |b| b).await;

        assert!(result.is_ok(), "expected Ok after retries, got {result:?}");
        assert_eq!(result.unwrap()["ok"], true);
    }

    // ── post_json happy path ──────────────────────────────────────────────────

    #[tokio::test]
    async fn post_json_deserializes_response() {
        let server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/infer"))
            .respond_with(
                ResponseTemplate::new(200)
                    .set_body_json(json!({"content": "hello", "model": "test-model", "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}})),
            )
            .mount(&server)
            .await;

        let client = CascadeHttpClient::new();
        let url = format!("{}/infer", server.uri());

        #[derive(serde::Deserialize, Debug, PartialEq)]
        struct Resp {
            content: String,
            model: String,
        }

        let resp = client
            .post_json::<_, Resp, _>(&url, &json!({"x": 1}), |b| b)
            .await
            .unwrap();

        assert_eq!(resp.content, "hello");
        assert_eq!(resp.model, "test-model");
    }
}
