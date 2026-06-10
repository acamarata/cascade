//! # oauth::client
//!
//! Generic OAuth 2.0 PKCE client parameterized by `OAuthProviderConfig`.
//!
//! ## Purpose
//! Provides the generic `OAuthClient` used by all provider-specific OAuth
//! implementations. Handles:
//! - Consent URL construction with PKCE S256 challenge and CSRF state.
//! - Loopback redirect listener (one-shot TCP server on a random port).
//! - Authorization code → token exchange.
//! - Token refresh.
//! - Token revocation.
//!
//! ## Constraints
//! - Loopback only: redirect_uri always `http://127.0.0.1:{port}/callback`.
//! - PKCE S256 only — plain method disallowed.
//! - Token values NEVER appear in log output at any level.
//! - One-shot redirect server: binds, accepts ONE connection, drops listener.

use std::io::{BufRead, BufReader, Write as IoWrite};
use std::net::{TcpListener, TcpStream};
use std::time::Duration;

use serde::Deserialize;

use crate::oauth::pkce::{generate_code_challenge, generate_code_verifier};
use crate::oauth::token_store::OAuthTokens;

/// Configuration for a specific OAuth 2.0 provider.
///
/// ## Purpose
/// Carries all provider-specific parameters needed by `OAuthClient` to perform
/// the full PKCE authorization code flow.
///
/// ## Constraints
/// - `redirect_uri` is always `http://127.0.0.1:{port}/callback` — callers
///   set the port via `OAuthClient::authorize_url` which allocates a free port.
/// - `scopes` is a list of space-separated scope strings per provider convention.
#[derive(Debug, Clone)]
pub struct OAuthProviderConfig {
    /// OAuth 2.0 client ID issued by the provider.
    pub client_id: String,
    /// OAuth 2.0 client secret (empty string for public/PKCE-only clients).
    pub client_secret: String,
    /// Authorization endpoint URL (e.g. `https://accounts.google.com/o/oauth2/v2/auth`).
    pub auth_url: String,
    /// Token endpoint URL (e.g. `https://oauth2.googleapis.com/token`).
    pub token_url: String,
    /// Requested OAuth scopes.
    pub scopes: Vec<String>,
}

/// PKCE flow state returned by `OAuthClient::authorize_url`.
///
/// ## Purpose
/// Bundles the auth URL (to open in a browser), the PKCE verifier (for code
/// exchange), the CSRF state nonce, and the loopback callback port (for the
/// redirect listener).
///
/// ## Constraints
/// - `verifier` must be passed unchanged to `exchange_code`.
/// - `state` must be passed to `await_callback` for CSRF validation.
pub struct AuthorizeResult {
    /// Fully-constructed authorization URL to open in the user's browser.
    pub auth_url: String,
    /// PKCE code_verifier — consume once in `exchange_code`.
    pub verifier: String,
    /// CSRF state nonce — pass to `await_callback` for validation.
    pub state: String,
    /// Loopback callback port — pass to `await_callback`.
    pub port: u16,
}

/// Error variants for the generic OAuth PKCE flow.
#[derive(Debug, thiserror::Error)]
pub enum OAuthClientError {
    /// HTTP transport or response error.
    #[error("HTTP error: {0}")]
    Http(String),
    /// Token exchange or refresh returned an unexpected response.
    #[error("token exchange failed: {0}")]
    TokenExchange(String),
    /// OAuth callback state mismatch (CSRF protection).
    #[error("OAuth state mismatch")]
    StateMismatch,
    /// I/O error from the callback listener.
    #[error("callback server error: {0}")]
    CallbackIo(String),
    /// No free TCP port available for the loopback callback server.
    #[error("no free port available")]
    NoFreePort,
    /// Token revocation endpoint returned a failure.
    #[error("token revocation failed: {0}")]
    RevocationFailed(String),
}

/// Wire shape of the token endpoint response body.
#[derive(Debug, Deserialize)]
struct TokenResponse {
    access_token: String,
    refresh_token: Option<String>,
    #[serde(default = "default_expires_in")]
    expires_in: u64,
    #[serde(default = "default_token_type")]
    token_type: String,
}

fn default_expires_in() -> u64 {
    3600
}
fn default_token_type() -> String {
    "Bearer".to_owned()
}

/// HTTP timeout for all OAuth token endpoint calls.
const HTTP_TIMEOUT_SECS: u64 = 10;

/// Generic OAuth 2.0 PKCE client.
///
/// ## Purpose
/// Provides the full PKCE authorization code flow over any provider described
/// by an `OAuthProviderConfig`. Provider-specific configs (Google, GitHub, etc.)
/// are built in `oauth/providers/`.
///
/// ## Constraints
/// - Token values MUST NOT be logged at any tracing level.
/// - Loopback only: redirect URI always uses `127.0.0.1`.
/// - Redirect listener is one-shot: closes after the first accepted connection.
///
/// ## SPORT
/// MASTER-COMPONENTS.md: cascade-providers / OAuthClient
pub struct OAuthClient {
    config: OAuthProviderConfig,
}

impl OAuthClient {
    /// Create a new `OAuthClient` for the given provider config.
    ///
    /// # Inputs
    /// - `config`: `OAuthProviderConfig` describing the provider's endpoints and client id.
    pub fn new(config: OAuthProviderConfig) -> Self {
        Self { config }
    }

    /// Build the authorization URL for the PKCE flow.
    ///
    /// # Purpose
    /// Generates a PKCE verifier and challenge, selects a random loopback port,
    /// constructs the full authorization URL with required parameters, and returns
    /// everything needed to complete the flow.
    ///
    /// # Outputs
    /// An `AuthorizeResult` containing the auth_url, verifier, state, and port.
    ///
    /// # Constraints
    /// - PKCE S256 only.
    /// - State is 32 random bytes encoded as hex (CSRF protection).
    /// - redirect_uri is always `http://127.0.0.1:{port}/callback`.
    pub async fn authorize_url(&self) -> Result<AuthorizeResult, OAuthClientError> {
        let verifier = generate_code_verifier();
        let challenge = generate_code_challenge(&verifier);

        // 32-byte random hex state for CSRF.
        let state = {
            let mut raw = [0u8; 32];
            rand::fill(&mut raw);
            hex_encode(&raw)
        };

        // Pick an unused ephemeral port.
        let port = portpicker::pick_unused_port().ok_or(OAuthClientError::NoFreePort)?;
        let redirect_uri = format!("http://127.0.0.1:{port}/callback");

        let scope = self.config.scopes.join(" ");

        let auth_url = format!(
            "{base}?client_id={client_id}\
             &redirect_uri={redirect_uri}\
             &response_type=code\
             &scope={scope}\
             &code_challenge={challenge}\
             &code_challenge_method=S256\
             &state={state}\
             &access_type=offline\
             &prompt=consent",
            base = self.config.auth_url,
            client_id = url_encode(&self.config.client_id),
            redirect_uri = url_encode(&redirect_uri),
            scope = url_encode(&scope),
            challenge = url_encode(&challenge),
            state = url_encode(&state),
        );

        Ok(AuthorizeResult { auth_url, verifier, state, port })
    }

    /// Await the OAuth callback on `port`, validate CSRF `expected_state`, return auth code.
    ///
    /// # Purpose
    /// Binds a one-shot TCP listener on `127.0.0.1:{port}`. Accepts exactly ONE
    /// connection, reads the `GET /callback?code=...&state=...` request line,
    /// validates the state via constant-time comparison, writes a success page,
    /// and returns the authorization code.
    ///
    /// # Inputs
    /// - `port`: returned by `authorize_url`.
    /// - `expected_state`: the state nonce from `AuthorizeResult`.
    ///
    /// # Outputs
    /// The authorization `code` to pass to `exchange_code`.
    ///
    /// # Constraints
    /// - Loopback only: never binds `0.0.0.0`.
    /// - One-shot: listener is dropped after first accepted connection.
    pub async fn await_callback(
        &self,
        port: u16,
        expected_state: &str,
    ) -> Result<String, OAuthClientError> {
        let listener = TcpListener::bind(format!("127.0.0.1:{port}"))
            .map_err(|e| OAuthClientError::CallbackIo(format!("bind: {e}")))?;

        let expected_state = expected_state.to_owned();
        let (code, _state) =
            tokio::task::spawn_blocking(move || handle_callback(listener, &expected_state))
                .await
                .map_err(|e| OAuthClientError::CallbackIo(format!("join: {e}")))??;

        Ok(code)
    }

    /// Exchange an authorization code for `OAuthTokens`.
    ///
    /// # Purpose
    /// POSTs to `config.token_url` with `grant_type=authorization_code`, the
    /// auth code, the PKCE verifier, and client credentials.
    ///
    /// # Inputs
    /// - `code`: authorization code from the OAuth callback.
    /// - `verifier`: the verifier string from `AuthorizeResult`.
    /// - `port`: the callback port (used to reconstruct redirect_uri).
    ///
    /// # Outputs
    /// `OAuthTokens` with access_token, optional refresh_token, and lifetime.
    ///
    /// # Constraints
    /// - Token values are never logged.
    pub async fn exchange_code(
        &self,
        code: &str,
        verifier: &str,
        port: u16,
    ) -> Result<OAuthTokens, OAuthClientError> {
        let redirect_uri = format!("http://127.0.0.1:{port}/callback");

        let client = self.http_client()?;

        let params = [
            ("grant_type", "authorization_code"),
            ("code", code),
            ("code_verifier", verifier),
            ("redirect_uri", redirect_uri.as_str()),
            ("client_id", self.config.client_id.as_str()),
            ("client_secret", self.config.client_secret.as_str()),
        ];

        let resp = client
            .post(&self.config.token_url)
            .form(&params)
            .send()
            .await
            .map_err(|e| OAuthClientError::Http(e.to_string()))?;

        let status = resp.status();
        if !status.is_success() {
            let body = resp.text().await.unwrap_or_default();
            tracing::warn!(status = status.as_u16(), "OAuth code exchange failed");
            return Err(OAuthClientError::TokenExchange(format!("HTTP {status}: {body}")));
        }

        let tr: TokenResponse = resp
            .json()
            .await
            .map_err(|e| OAuthClientError::TokenExchange(format!("parse: {e}")))?;

        Ok(OAuthTokens {
            access_token: tr.access_token,
            refresh_token: tr.refresh_token,
            expires_in: tr.expires_in,
            token_type: tr.token_type,
        })
    }

    /// Refresh an expired access token using a refresh token.
    ///
    /// # Purpose
    /// POSTs `grant_type=refresh_token` to `config.token_url`.
    ///
    /// # Inputs
    /// - `refresh_token`: the refresh token from a prior `exchange_code` call.
    ///
    /// # Outputs
    /// A new `OAuthTokens` set. `refresh_token` may be absent if the provider
    /// does not rotate it.
    ///
    /// # Constraints
    /// - Token values are never logged.
    pub async fn refresh_token(
        &self,
        refresh_token: &str,
    ) -> Result<OAuthTokens, OAuthClientError> {
        let client = self.http_client()?;

        let params = [
            ("grant_type", "refresh_token"),
            ("refresh_token", refresh_token),
            ("client_id", self.config.client_id.as_str()),
            ("client_secret", self.config.client_secret.as_str()),
        ];

        let resp = client
            .post(&self.config.token_url)
            .form(&params)
            .send()
            .await
            .map_err(|e| OAuthClientError::Http(e.to_string()))?;

        let status = resp.status();
        if !status.is_success() {
            let body = resp.text().await.unwrap_or_default();
            tracing::warn!(status = status.as_u16(), "OAuth token refresh failed");
            return Err(OAuthClientError::TokenExchange(format!("HTTP {status}: {body}")));
        }

        let tr: TokenResponse = resp
            .json()
            .await
            .map_err(|e| OAuthClientError::TokenExchange(format!("parse: {e}")))?;

        Ok(OAuthTokens {
            access_token: tr.access_token,
            refresh_token: tr.refresh_token,
            expires_in: tr.expires_in,
            token_type: tr.token_type,
        })
    }

    /// Revoke a token via the provider's revocation endpoint, if configured.
    ///
    /// # Purpose
    /// POSTs `token` to `revocation_url`. This is a best-effort operation;
    /// callers should delete the stored token regardless of this call's result.
    ///
    /// # Inputs
    /// - `token`: the access or refresh token to revoke.
    /// - `revocation_url`: the provider's token revocation endpoint.
    ///
    /// # Constraints
    /// - Token value is NEVER logged.
    pub async fn revoke_token(
        &self,
        token: &str,
        revocation_url: &str,
    ) -> Result<(), OAuthClientError> {
        let client = self.http_client()?;
        let params = [("token", token)];

        let resp = client
            .post(revocation_url)
            .form(&params)
            .send()
            .await
            .map_err(|e| OAuthClientError::Http(e.to_string()))?;

        let status = resp.status();
        if !status.is_success() {
            let body = resp.text().await.unwrap_or_default();
            tracing::warn!(status = status.as_u16(), "token revocation failed (non-fatal)");
            return Err(OAuthClientError::RevocationFailed(format!("HTTP {status}: {body}")));
        }

        Ok(())
    }

    /// Build a configured `reqwest::Client`.
    fn http_client(&self) -> Result<reqwest::Client, OAuthClientError> {
        reqwest::Client::builder()
            .timeout(Duration::from_secs(HTTP_TIMEOUT_SECS))
            .build()
            .map_err(|e| OAuthClientError::Http(e.to_string()))
    }
}

// ── Internal helpers ───────────────────────────────────────────────────────────

/// Handle one incoming TCP connection for the OAuth callback.
///
/// Parses the HTTP GET request line, extracts `code` and `state`, validates
/// state, writes a success page, returns `(code, state)`.
/// Called inside `spawn_blocking` — must not use async.
fn handle_callback(
    listener: TcpListener,
    expected_state: &str,
) -> Result<(String, String), OAuthClientError> {
    let (mut stream, _peer) = listener
        .accept()
        .map_err(|e| OAuthClientError::CallbackIo(format!("accept: {e}")))?;

    let request_line = read_request_line(&stream)?;
    let (code, state) = parse_callback_params(&request_line)?;

    if !constant_time_eq(state.as_bytes(), expected_state.as_bytes()) {
        write_response(&mut stream, "400 Bad Request", "State mismatch — please retry.");
        return Err(OAuthClientError::StateMismatch);
    }

    write_response(
        &mut stream,
        "200 OK",
        "Authorization successful! You may close this tab.",
    );

    Ok((code, state))
}

/// Read the first line of an HTTP request from a TCP stream.
fn read_request_line(stream: &TcpStream) -> Result<String, OAuthClientError> {
    let mut reader = BufReader::new(stream);
    let mut line = String::new();
    reader
        .read_line(&mut line)
        .map_err(|e| OAuthClientError::CallbackIo(format!("read: {e}")))?;
    Ok(line.trim_end().to_owned())
}

/// Extract `code` and `state` from a GET request line such as:
/// `GET /callback?code=abc&state=xyz HTTP/1.1`
pub(crate) fn parse_callback_params(
    request_line: &str,
) -> Result<(String, String), OAuthClientError> {
    let path_and_query = request_line
        .strip_prefix("GET ")
        .and_then(|s| s.split(' ').next())
        .ok_or_else(|| {
            OAuthClientError::CallbackIo(format!("unexpected request line: {request_line}"))
        })?;

    let query = path_and_query.split_once('?').map(|(_, q)| q).unwrap_or("");

    let mut code = None;
    let mut state = None;

    for pair in query.split('&') {
        if let Some(v) = pair.strip_prefix("code=") {
            code = Some(percent_decode(v));
        } else if let Some(v) = pair.strip_prefix("state=") {
            state = Some(percent_decode(v));
        }
    }

    match (code, state) {
        (Some(c), Some(s)) => Ok((c, s)),
        (None, _) => {
            Err(OAuthClientError::CallbackIo("missing 'code' in callback".to_owned()))
        }
        (_, None) => {
            Err(OAuthClientError::CallbackIo("missing 'state' in callback".to_owned()))
        }
    }
}

/// Minimal percent-decode for OAuth callback query parameters.
fn percent_decode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    let mut chars = s.chars().peekable();
    while let Some(c) = chars.next() {
        if c == '%' {
            let h1 = chars.next();
            let h2 = chars.next();
            if let (Some(h1), Some(h2)) = (h1, h2) {
                if let Ok(b) = u8::from_str_radix(&format!("{h1}{h2}"), 16) {
                    out.push(b as char);
                    continue;
                }
            }
            out.push('%');
        } else if c == '+' {
            out.push(' ');
        } else {
            out.push(c);
        }
    }
    out
}

/// Write a minimal HTTP/1.1 response to the callback connection.
fn write_response(stream: &mut TcpStream, status: &str, body: &str) {
    let response = format!(
        "HTTP/1.1 {status}\r\nContent-Type: text/html; charset=utf-8\r\n\
         Content-Length: {len}\r\nConnection: close\r\n\r\n{body}",
        len = body.len()
    );
    let _ = stream.write_all(response.as_bytes());
}

/// Encode bytes as a lowercase hex string.
fn hex_encode(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

/// Percent-encode a string for use in a query parameter value (RFC 3986).
pub(crate) fn url_encode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            'A'..='Z' | 'a'..='z' | '0'..='9' | '-' | '_' | '.' | '~' => out.push(c),
            ':' | '/' | '@' | '+' => out.push(c),
            c => {
                let mut buf = [0u8; 4];
                let encoded = c.encode_utf8(&mut buf);
                for byte in encoded.bytes() {
                    out.push_str(&format!("%{byte:02X}"));
                }
            }
        }
    }
    out
}

/// Constant-time byte-slice comparison (CSRF state validation).
pub(crate) fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    a.iter().zip(b.iter()).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    fn test_config(token_url: &str) -> OAuthProviderConfig {
        OAuthProviderConfig {
            client_id: "test-client-id".to_owned(),
            client_secret: "test-secret".to_owned(),
            auth_url: "https://example.com/oauth/authorize".to_owned(),
            token_url: token_url.to_owned(),
            scopes: vec!["read".to_owned(), "write".to_owned()],
        }
    }

    // ── authorize_url shape ────────────────────────────────────────────────────

    #[tokio::test]
    async fn authorize_url_contains_required_params() {
        let config = OAuthProviderConfig {
            client_id: "my-client".to_owned(),
            client_secret: "".to_owned(),
            auth_url: "https://example.com/auth".to_owned(),
            token_url: "https://example.com/token".to_owned(),
            scopes: vec!["email".to_owned()],
        };
        let client = OAuthClient::new(config);
        let result = client.authorize_url().await.unwrap();

        assert!(result.auth_url.starts_with("https://example.com/auth"), "auth_url base");
        assert!(result.auth_url.contains("client_id=my-client"), "client_id");
        assert!(result.auth_url.contains("response_type=code"), "response_type");
        assert!(result.auth_url.contains("code_challenge_method=S256"), "S256 method");
        assert!(result.auth_url.contains(&format!("state={}", result.state)), "state in url");
        assert!(
            result.auth_url.contains(&format!("127.0.0.1:{}", result.port)),
            "loopback port in url"
        );
        assert!(!result.auth_url.contains("0.0.0.0"), "no 0.0.0.0");
    }

    // ── state mismatch CSRF rejection ──────────────────────────────────────────

    #[test]
    fn parse_callback_state_mismatch() {
        let line = "GET /callback?code=abc&state=tampered HTTP/1.1";
        let (code, state) = parse_callback_params(line).unwrap();
        assert_eq!(code, "abc");
        let expected = "correct-state";
        assert!(!constant_time_eq(state.as_bytes(), expected.as_bytes()));
    }

    #[test]
    fn parse_callback_correct_state() {
        let expected = "abc123";
        let line = format!("GET /callback?code=code999&state={expected} HTTP/1.1");
        let (code, state) = parse_callback_params(&line).unwrap();
        assert_eq!(code, "code999");
        assert!(constant_time_eq(state.as_bytes(), expected.as_bytes()));
    }

    #[test]
    fn parse_callback_missing_code_errors() {
        let line = "GET /callback?state=abc HTTP/1.1";
        assert!(matches!(
            parse_callback_params(line),
            Err(OAuthClientError::CallbackIo(_))
        ));
    }

    #[test]
    fn parse_callback_percent_decode() {
        let line = "GET /callback?code=hello+world&state=ok HTTP/1.1";
        let (code, _) = parse_callback_params(line).unwrap();
        assert_eq!(code, "hello world");
    }

    // ── exchange_code — wiremock ───────────────────────────────────────────────

    #[tokio::test]
    async fn exchange_code_returns_tokens() {
        let mock_server = MockServer::start().await;

        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "access_token": "at-123",
                "refresh_token": "rt-456",
                "expires_in": 3600,
                "token_type": "Bearer"
            })))
            .mount(&mock_server)
            .await;

        let token_url = format!("{}/token", mock_server.uri());
        let client = OAuthClient::new(test_config(&token_url));
        let tokens = client.exchange_code("auth-code", "verifier-xyz", 9999).await.unwrap();

        assert_eq!(tokens.access_token, "at-123");
        assert_eq!(tokens.refresh_token.as_deref(), Some("rt-456"));
        assert_eq!(tokens.expires_in, 3600);
        assert_eq!(tokens.token_type, "Bearer");
    }

    #[tokio::test]
    async fn exchange_code_http_error_returns_err() {
        let mock_server = MockServer::start().await;

        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(401).set_body_string("Unauthorized"))
            .mount(&mock_server)
            .await;

        let token_url = format!("{}/token", mock_server.uri());
        let client = OAuthClient::new(test_config(&token_url));
        let result = client.exchange_code("bad-code", "ver", 9999).await;
        assert!(matches!(result, Err(OAuthClientError::TokenExchange(_))));
    }

    // ── refresh_token — wiremock ───────────────────────────────────────────────

    #[tokio::test]
    async fn refresh_token_returns_new_tokens() {
        let mock_server = MockServer::start().await;

        Mock::given(method("POST"))
            .and(path("/token"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "access_token": "new-at",
                "expires_in": 1800,
                "token_type": "Bearer"
            })))
            .mount(&mock_server)
            .await;

        let token_url = format!("{}/token", mock_server.uri());
        let client = OAuthClient::new(test_config(&token_url));
        let tokens = client.refresh_token("old-refresh").await.unwrap();

        assert_eq!(tokens.access_token, "new-at");
        assert!(tokens.refresh_token.is_none());
        assert_eq!(tokens.expires_in, 1800);
    }

    // ── constant_time_eq ───────────────────────────────────────────────────────

    #[test]
    fn constant_time_eq_same() {
        assert!(constant_time_eq(b"hello", b"hello"));
    }

    #[test]
    fn constant_time_eq_different() {
        assert!(!constant_time_eq(b"hello", b"world"));
    }

    #[test]
    fn constant_time_eq_length_mismatch() {
        assert!(!constant_time_eq(b"abc", b"abcd"));
    }

    // ── url_encode ─────────────────────────────────────────────────────────────

    #[test]
    fn url_encode_space() {
        let encoded = url_encode("hello world");
        assert_eq!(encoded, "hello%20world");
    }

    #[test]
    fn url_encode_passthrough_unreserved() {
        let s = "abcABC123-_.~";
        assert_eq!(url_encode(s), s);
    }
}
