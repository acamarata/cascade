//! HTTP/1.1 MCP transport — POST /mcp with Bearer auth (T-P4-E02-16).
//!
//! ## Overview
//!
//! Two roles:
//! - **`HttpTransport`** (legacy P2 stub): channel-backed low-level `Transport`.
//!   Kept for backward compatibility with code that constructs `HttpTransport` directly.
//! - **`HttpServer`** (P4 server-side): axum-based HTTP server implementing `McpTransport`.
//!   Binds `127.0.0.1:7722` (loopback only), handles POST /mcp (stateless JSON-RPC)
//!   and GET /mcp/health.
//!
//! ## Routes
//!
//! | Method | Path | Description |
//! |--------|------|-------------|
//! | `POST` | `/mcp` | JSON-RPC message; returns JSON-RPC response |
//! | `GET`  | `/mcp/health` | Unauthenticated health check |
//!
//! ## Auth
//!
//! Bearer token in `Authorization` header. Validated by `McpAuth::validate_token`.
//! Missing or invalid token → 401.
//!
//! ## Constraints
//! - Bind: 127.0.0.1 only (never 0.0.0.0).
//! - Request body limit: 4 MB.
//! - Request timeout: 30 s.
//! - Each request is stateless — fresh `ConnectionContext` per request.
//!
//! ## SPORT
//! MASTER-TRANSPORTS.md: HTTP transport row

use std::net::SocketAddr;
use std::sync::Arc;

use async_trait::async_trait;
use axum::extract::State;
use axum::http::{HeaderMap, StatusCode};
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::{Json, Router};
use tokio::sync::mpsc;
use tower_http::limit::RequestBodyLimitLayer;
use tower_http::timeout::TimeoutLayer;
use tracing::{debug, error, info, warn};

use cascade_types::error::{CascadeError, Result};

use crate::auth::McpAuth;
use crate::server::{McpServer, McpServerConfig};
use crate::transport::McpTransport;

use super::Transport;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Default HTTP port (loopback only).
pub const DEFAULT_HTTP_PORT: u16 = 7722;

/// Body size limit: 4 MB (shared with sse.rs).
pub const BODY_LIMIT: usize = 4 * 1024 * 1024;

/// Request timeout: 30 seconds.
const REQUEST_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(30);

// ── HttpTransport (legacy P2 channel-backed stub) ─────────────────────────────

/// Configuration for the HTTP streaming transport.
#[derive(Debug, Clone)]
pub struct HttpTransportConfig {
    /// TCP port to bind (default: 7722).
    pub port: u16,
    /// Host to bind (default: 127.0.0.1).
    pub host: String,
    /// Max response chunk size in bytes.
    pub chunk_size: usize,
}

impl Default for HttpTransportConfig {
    fn default() -> Self {
        Self {
            port: DEFAULT_HTTP_PORT,
            host: "127.0.0.1".into(),
            chunk_size: 65_536,
        }
    }
}

/// MCP transport over HTTP — legacy channel-backed stub (P2).
///
/// For the full axum-based HTTP server, use [`HttpServer`].
pub struct HttpTransport {
    _config: HttpTransportConfig,
    send_tx: mpsc::Sender<String>,
    recv_rx: mpsc::Receiver<String>,
}

impl HttpTransport {
    /// Create an HTTP transport backed by mpsc channels.
    pub fn new(
        config: HttpTransportConfig,
    ) -> (Self, mpsc::Receiver<String>, mpsc::Sender<String>) {
        let (send_tx, send_rx) = mpsc::channel(128);
        let (recv_tx, recv_rx) = mpsc::channel(128);
        let transport = Self {
            _config: config,
            send_tx,
            recv_rx,
        };
        (transport, send_rx, recv_tx)
    }
}

#[async_trait]
impl Transport for HttpTransport {
    async fn send(&mut self, message: &str) -> Result<()> {
        debug!(bytes = message.len(), "http-stream send");
        self.send_tx
            .send(message.to_owned())
            .await
            .map_err(|_| CascadeError::Io {
                path: "<http-channel>".into(),
                operation: "send",
                source: std::io::Error::new(
                    std::io::ErrorKind::BrokenPipe,
                    "HTTP send channel closed",
                ),
            })
    }

    async fn recv(&mut self) -> Result<Option<String>> {
        Ok(self.recv_rx.recv().await)
    }

    async fn close(&mut self) -> Result<()> {
        Ok(())
    }

    fn name(&self) -> &str {
        "http-stream"
    }
}

// ── HttpServer (P4 axum-based server) ────────────────────────────────────────

/// Shared state passed into axum handlers.
#[derive(Clone)]
struct AppState {
    auth: Arc<McpAuth>,
    config: McpServerConfig,
    server_version: String,
}

/// Server-side HTTP MCP transport (T-P4-E02-16).
///
/// Binds `127.0.0.1:{port}` (loopback only). Handles POST /mcp with Bearer auth
/// and GET /mcp/health without auth. Each POST request is a stateless MCP exchange.
pub struct HttpServer {
    port: u16,
    auth: Arc<McpAuth>,
    config: McpServerConfig,
    shutdown: Arc<tokio::sync::Notify>,
}

impl HttpServer {
    /// Create a new `HttpServer`.
    ///
    /// - `port`: TCP port to bind (default: 7722).
    /// - `auth`: auth instance for Bearer token validation.
    /// - `config`: `McpServerConfig` template for per-request servers.
    pub fn new(port: Option<u16>, auth: Arc<McpAuth>, config: McpServerConfig) -> Self {
        Self {
            port: port.unwrap_or(DEFAULT_HTTP_PORT),
            auth,
            config,
            shutdown: Arc::new(tokio::sync::Notify::new()),
        }
    }

    /// Return the bind address (always loopback).
    pub fn bind_addr(&self) -> SocketAddr {
        format!("127.0.0.1:{}", self.port)
            .parse()
            .expect("valid loopback addr")
    }
}

#[async_trait(?Send)]
impl McpTransport for HttpServer {
    async fn listen(&self) -> Result<()> {
        let addr = self.bind_addr();

        // Runtime loopback guard — fires in all build profiles including release.
        if !addr.ip().is_loopback() {
            return Err(CascadeError::Io {
                path: format!("{}", addr).into(),
                operation: "loopback_check",
                source: std::io::Error::new(
                    std::io::ErrorKind::PermissionDenied,
                    "MCP HTTP transport must bind to loopback only",
                ),
            });
        }

        let state = AppState {
            auth: Arc::clone(&self.auth),
            config: self.config.clone(),
            server_version: self.config.server_version.clone(),
        };

        let app = Router::new()
            .route("/mcp", post(handle_mcp_post))
            .route("/mcp/health", get(handle_health))
            .layer(RequestBodyLimitLayer::new(BODY_LIMIT))
            .layer(TimeoutLayer::new(REQUEST_TIMEOUT))
            .with_state(state);

        let listener = tokio::net::TcpListener::bind(addr)
            .await
            .map_err(|e| CascadeError::Io {
                path: format!("{}", addr).into(),
                operation: "http_bind",
                source: e,
            })?;

        info!(addr = %addr, "HTTP MCP server listening (POST /mcp)");

        let shutdown = Arc::clone(&self.shutdown);
        axum::serve(listener, app)
            .with_graceful_shutdown(async move { shutdown.notified().await })
            .await
            .map_err(|e| CascadeError::Io {
                path: format!("{}", addr).into(),
                operation: "http_serve",
                source: std::io::Error::other(e.to_string()),
            })?;

        Ok(())
    }

    async fn stop(&self) -> Result<()> {
        self.shutdown.notify_waiters();
        // Sticky permit: notify_waiters only wakes CURRENT waiters; if stop()
        // fires while the accept loop is mid-iteration the wakeup is lost and
        // shutdown hangs (observed). notify_one stores a permit consumed by
        // the next notified() poll.
        self.shutdown.notify_one();
        Ok(())
    }
}

// ── Axum handlers ─────────────────────────────────────────────────────────────

/// POST /mcp — stateless JSON-RPC endpoint with Bearer auth.
///
/// Body is read as raw bytes; the 4 MB limit is enforced by `RequestBodyLimitLayer`.
async fn handle_mcp_post(
    State(state): State<AppState>,
    headers: HeaderMap,
    request: axum::extract::Request,
) -> impl IntoResponse {
    // ── 0. Origin / Host guard (CSRF) ─────────────────────────────────────────
    if let Err(status) = crate::security::validate_local_origin(&headers) {
        warn!("POST /mcp: foreign Origin/Host rejected");
        return (
            status,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            r#"{"error":"forbidden_origin"}"#,
        )
            .into_response();
    }

    // ── 1. Read body (needed for cap gate; body consumed once) ───────────────
    let bytes = match axum::body::to_bytes(request.into_body(), BODY_LIMIT).await {
        Ok(b) => b,
        Err(_) => {
            warn!("POST /mcp: body read error (too large or IO error)");
            return (
                StatusCode::PAYLOAD_TOO_LARGE,
                [(axum::http::header::CONTENT_TYPE, "application/json")],
                r#"{"error":"body_too_large"}"#,
            )
                .into_response();
        }
    };

    let body = match std::str::from_utf8(&bytes) {
        Ok(s) => s,
        Err(_) => {
            return (
                StatusCode::BAD_REQUEST,
                [(axum::http::header::CONTENT_TYPE, "application/json")],
                r#"{"error":"invalid_utf8"}"#,
            )
                .into_response();
        }
    };

    // ── 1b. Capability gate (fires before auth; cap is a deny rule, not grant) ─
    let client_cap = parse_cap_header(&headers);
    if let Some(tool_name) = extract_tools_call_name(body) {
        if crate::security::tool_requires_personal_data(&tool_name)
            && !client_cap.contains(crate::security::CapabilitySet::PERSONAL_DATA)
        {
            warn!(
                tool = tool_name,
                "POST /mcp: PersonalData capability required"
            );
            let err_json = serde_json::json!({
                "jsonrpc": "2.0",
                "id": null,
                "error": {
                    "code": -32001,
                    "message": "capability PersonalData required",
                    "data": { "tool": tool_name }
                }
            })
            .to_string();
            return (
                StatusCode::FORBIDDEN,
                [(axum::http::header::CONTENT_TYPE, "application/json")],
                err_json,
            )
                .into_response();
        }
    }

    // ── 2. Auth ───────────────────────────────────────────────────────────────
    let token = match extract_bearer(&headers) {
        Some(t) => t,
        None => {
            warn!("POST /mcp: missing Authorization header");
            return auth_error_response();
        }
    };

    if let Err(e) = state.auth.validate_token(&token) {
        warn!(error = %e, "POST /mcp: token rejected");
        return auth_error_response();
    }

    // ── 3. Dispatch through a fresh McpServer ─────────────────────────────────
    let response = dispatch_single_request(body, &state.config).await;
    match response {
        Ok(json) => (
            StatusCode::OK,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            json,
        )
            .into_response(),
        Err(e) => {
            error!(error = %e, "POST /mcp: dispatch error");
            internal_error_response()
        }
    }
}

/// GET /mcp/health — unauthenticated health check (still subject to Origin guard).
async fn handle_health(State(state): State<AppState>, headers: HeaderMap) -> impl IntoResponse {
    if let Err(status) = crate::security::validate_local_origin(&headers) {
        warn!("GET /mcp/health: foreign Origin/Host rejected");
        return (
            status,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            r#"{"error":"forbidden_origin"}"#,
        )
            .into_response();
    }
    Json(serde_json::json!({
        "status": "ok",
        "version": state.server_version,
    }))
    .into_response()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Dispatch a single JSON-RPC message through a new McpServer and return the serialized response.
///
/// `McpServer` is not `Sync` (holds `Box<dyn Transport>`), so its `run()` future is not `Send`.
/// We run it on a dedicated single-threaded Tokio runtime inside `spawn_blocking` to avoid
/// polluting the axum multi-thread executor with non-Send futures.
async fn dispatch_single_request(
    body: &str,
    config: &McpServerConfig,
) -> std::result::Result<String, String> {
    use crate::transport::connection::ChannelTransportPub;

    let body_owned = body.to_owned();
    let config_owned = config.clone();

    tokio::task::spawn_blocking(move || {
        // New single-threaded runtime — safe for non-Send McpServer::run().
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e| e.to_string())?;

        rt.block_on(async move {
            let (msg_tx, msg_rx) = mpsc::channel::<String>(2);
            let (resp_tx, mut resp_rx) = mpsc::channel::<String>(2);

            let server_transport = ChannelTransportPub {
                recv: msg_rx,
                send: resp_tx,
            };

            let server = McpServer::new(config_owned, Box::new(server_transport));

            // Send the request and signal EOF.
            msg_tx.send(body_owned).await.map_err(|e| e.to_string())?;
            drop(msg_tx); // EOF signal to McpServer::run()

            // Run to completion (processes one message then exits on EOF).
            let _ = server.run().await;

            // Return the response.
            resp_rx.recv().await.ok_or_else(|| "no response".to_owned())
        })
    })
    .await
    .map_err(|e| e.to_string())?
}

/// Parse the optional `X-Cascade-Cap` header into a `CapabilitySet`.
///
/// If the header is absent or malformed, returns `CapabilitySet::Standard`.
pub(super) fn parse_cap_header_pub(headers: &HeaderMap) -> crate::security::CapabilitySet {
    parse_cap_header(headers)
}

fn parse_cap_header(headers: &HeaderMap) -> crate::security::CapabilitySet {
    headers
        .get("x-cascade-cap")
        .and_then(|v| v.to_str().ok())
        .and_then(|s| s.parse::<u32>().ok())
        .map(crate::security::CapabilitySet::from_bits)
        .unwrap_or_default()
}

/// If the JSON-RPC body is a `tools/call` request, return the tool name.
///
/// Returns `None` for any other method or malformed JSON.
pub(super) fn extract_tools_call_name_pub(body: &str) -> Option<String> {
    extract_tools_call_name(body)
}

fn extract_tools_call_name(body: &str) -> Option<String> {
    let v: serde_json::Value = serde_json::from_str(body).ok()?;
    let method = v.get("method")?.as_str()?;
    if method != "tools/call" {
        return None;
    }
    v.get("params")?.get("name")?.as_str().map(|s| s.to_owned())
}

/// Build the axum `Router` used by `HttpServer::listen`.
///
/// Exposed for integration tests (`tests/transport_security.rs`) so they can
/// call `tower::ServiceExt::oneshot` without starting a real TCP listener.
#[cfg(any(test, feature = "test-utils"))]
pub fn build_http_app_for_test(auth: std::sync::Arc<McpAuth>, config: McpServerConfig) -> Router {
    let state = AppState {
        server_version: config.server_version.clone(),
        auth,
        config,
    };
    Router::new()
        .route("/mcp", post(handle_mcp_post))
        .route("/mcp/health", get(handle_health))
        .layer(RequestBodyLimitLayer::new(BODY_LIMIT))
        .with_state(state)
}

fn extract_bearer(headers: &HeaderMap) -> Option<String> {
    let value = headers
        .get(axum::http::header::AUTHORIZATION)?
        .to_str()
        .ok()?;
    value.strip_prefix("Bearer ").map(|s| s.to_owned())
}

fn auth_error_response() -> axum::response::Response {
    (
        StatusCode::UNAUTHORIZED,
        [(axum::http::header::CONTENT_TYPE, "application/json")],
        r#"{"error":"invalid_token"}"#,
    )
        .into_response()
}

fn internal_error_response() -> axum::response::Response {
    (
        StatusCode::INTERNAL_SERVER_ERROR,
        [(axum::http::header::CONTENT_TYPE, "application/json")],
        r#"{"error":"internal_error"}"#,
    )
        .into_response()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use axum::http::Request;
    use serde_json::Value;
    use tower::ServiceExt;

    fn make_auth() -> Arc<McpAuth> {
        Arc::new(McpAuth::from_secret([99u8; 32]))
    }

    fn make_app(auth: Arc<McpAuth>) -> Router {
        let config = McpServerConfig::default();
        let state = AppState {
            server_version: config.server_version.clone(),
            auth,
            config,
        };
        Router::new()
            .route("/mcp", post(handle_mcp_post))
            .route("/mcp/health", get(handle_health))
            .layer(RequestBodyLimitLayer::new(BODY_LIMIT))
            .with_state(state)
    }

    #[tokio::test(flavor = "current_thread")]
    async fn health_endpoint_returns_200() {
        let auth = make_auth();
        let app = make_app(auth);

        let req = Request::builder()
            .method("GET")
            .uri("/mcp/health")
            .body(Body::empty())
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        let body = axum::body::to_bytes(resp.into_body(), 1024).await.unwrap();
        let val: Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(val["status"], "ok");
    }

    #[tokio::test(flavor = "current_thread")]
    async fn post_mcp_without_auth_returns_401() {
        let auth = make_auth();
        let app = make_app(auth);

        let req = Request::builder()
            .method("POST")
            .uri("/mcp")
            .header("content-type", "application/json")
            .body(Body::from(
                r#"{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}"#,
            ))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
    }

    #[tokio::test(flavor = "current_thread")]
    async fn post_mcp_with_invalid_token_returns_401() {
        let auth = make_auth();
        let app = make_app(auth);

        let req = Request::builder()
            .method("POST")
            .uri("/mcp")
            .header("Authorization", "Bearer cascade-mcp-invalid")
            .header("content-type", "application/json")
            .body(Body::from(
                r#"{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}"#,
            ))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
    }

    #[tokio::test(flavor = "current_thread")]
    async fn post_mcp_with_valid_token_returns_response() {
        let auth = make_auth();
        let token = auth.generate_token();
        let app = make_app(Arc::clone(&auth));

        let init_body = r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}"#;

        let req = Request::builder()
            .method("POST")
            .uri("/mcp")
            .header("Authorization", format!("Bearer {}", token))
            .header("content-type", "application/json")
            .body(Body::from(init_body))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        let body = axum::body::to_bytes(resp.into_body(), 65536).await.unwrap();
        let val: Value = serde_json::from_slice(&body).expect("valid JSON response");
        assert_eq!(val["jsonrpc"], "2.0");
    }

    #[tokio::test(flavor = "current_thread")]
    async fn http_server_bind_addr_is_loopback() {
        let auth = make_auth();
        let server = HttpServer::new(None, auth, McpServerConfig::default());
        let addr = server.bind_addr();
        assert!(addr.ip().is_loopback(), "bind addr must be loopback");
        assert_eq!(addr.port(), DEFAULT_HTTP_PORT);
    }
}
