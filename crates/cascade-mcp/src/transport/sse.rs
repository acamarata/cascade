//! SSE MCP transport — GET /mcp/events Server-Sent Events stream (T-P4-E02-17).
//!
//! ## Overview
//!
//! Two roles:
//! - **`SseTransport`** (legacy P2 stub): channel-backed low-level `Transport`.
//!   Kept for backward compatibility.
//! - **`SseServer`** (P4 server-side): adds `GET /mcp/events` SSE route to the
//!   existing HTTP axum router. Clients use POST /mcp for requests (stateless)
//!   and GET /mcp/events for server-initiated notifications (streaming).
//!
//! ## Routes
//!
//! | Method | Path | Description |
//! |--------|------|-------------|
//! | `GET`  | `/mcp/events` | SSE notification stream (Bearer auth required) |
//!
//! Combined with `HttpServer` routes (POST /mcp, GET /mcp/health) on the same bind port.
//!
//! ## Protocol
//!
//! Each notification is sent as `data: {json}\n\n`.
//! Heartbeat: `: heartbeat\n\n` every 30 seconds.
//!
//! ## Auth
//!
//! Bearer token in `Authorization` header (same as POST /mcp).
//!
//! ## Constraints
//! - Bind: 127.0.0.1 only (shared with HttpServer on port 7722).
//! - Backpressure: NotificationBus uses try_recv to avoid blocking on slow clients.
//! - Heartbeat: 30s interval to prevent proxy timeout.
//!
//! ## SPORT
//! MASTER-TRANSPORTS.md: SSE transport row

use std::net::SocketAddr;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use axum::extract::State;
use axum::http::{HeaderMap, StatusCode};
use axum::response::sse::{Event, KeepAlive, Sse};
use axum::response::IntoResponse;
use axum::routing::{get, post};
use axum::Router;
use tokio::sync::mpsc;
use tokio_stream::wrappers::BroadcastStream;
use tokio_stream::StreamExt as _;
use tracing::{debug, info, warn};

use cascade_types::error::{CascadeError, Result};

use crate::auth::McpAuth;
use crate::notification::NotificationBus;
use crate::server::McpServerConfig;
use crate::transport::http::BODY_LIMIT;
use crate::transport::McpTransport;

use super::Transport;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Heartbeat interval for SSE (prevents proxy timeout).
const HEARTBEAT_INTERVAL: Duration = Duration::from_secs(30);

// ── SseTransport (legacy P2 channel-backed stub) ──────────────────────────────

/// Configuration for the SSE transport.
#[derive(Debug, Clone)]
pub struct SseConfig {
    /// TCP port to bind (default: 7722).
    pub port: u16,
    /// Host to bind (default: 127.0.0.1).
    pub host: String,
    /// Maximum event queue depth before back-pressure.
    pub queue_depth: usize,
}

impl Default for SseConfig {
    fn default() -> Self {
        Self {
            port: crate::transport::http::DEFAULT_HTTP_PORT,
            host: "127.0.0.1".into(),
            queue_depth: 256,
        }
    }
}

/// MCP transport over HTTP SSE — legacy channel-backed stub (P2).
///
/// For the full P4 SSE server, use [`SseServer`].
pub struct SseTransport {
    _config: SseConfig,
    send_tx: mpsc::Sender<String>,
    recv_rx: mpsc::Receiver<String>,
}

impl SseTransport {
    /// Create an SSE transport backed by mpsc channels.
    pub fn new(config: SseConfig) -> (Self, mpsc::Receiver<String>, mpsc::Sender<String>) {
        let (send_tx, send_rx) = mpsc::channel(config.queue_depth);
        let (recv_tx, recv_rx) = mpsc::channel(config.queue_depth);
        let transport = Self {
            _config: config,
            send_tx,
            recv_rx,
        };
        (transport, send_rx, recv_tx)
    }
}

#[async_trait]
impl Transport for SseTransport {
    async fn send(&mut self, message: &str) -> Result<()> {
        debug!(bytes = message.len(), "sse send");
        self.send_tx
            .send(message.to_owned())
            .await
            .map_err(|_| CascadeError::Io {
                path: "<sse-channel>".into(),
                operation: "send",
                source: std::io::Error::new(
                    std::io::ErrorKind::BrokenPipe,
                    "SSE send channel closed",
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
        "sse"
    }
}

// ── SseServer (P4 axum-based) ─────────────────────────────────────────────────

/// Server-side SSE MCP transport (T-P4-E02-17).
///
/// Extends the HTTP router (POST /mcp) with GET /mcp/events SSE stream.
/// Clients connect with a Bearer token; the server pushes `NotificationBus`
/// events as SSE `data:` frames plus heartbeat comments every 30 seconds.
pub struct SseServer {
    port: u16,
    auth: Arc<McpAuth>,
    config: McpServerConfig,
    bus: NotificationBus,
    shutdown: Arc<tokio::sync::Notify>,
}

impl SseServer {
    /// Create a new `SseServer` that combines HTTP + SSE on one port.
    pub fn new(
        port: Option<u16>,
        auth: Arc<McpAuth>,
        config: McpServerConfig,
        bus: NotificationBus,
    ) -> Self {
        Self {
            port: port.unwrap_or(crate::transport::http::DEFAULT_HTTP_PORT),
            auth,
            config,
            bus,
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
impl McpTransport for SseServer {
    async fn listen(&self) -> Result<()> {
        let addr = self.bind_addr();
        // Runtime loopback guard — fires in all build profiles including release.
        if !addr.ip().is_loopback() {
            return Err(CascadeError::Io {
                path: format!("{}", addr).into(),
                operation: "loopback_check",
                source: std::io::Error::new(
                    std::io::ErrorKind::PermissionDenied,
                    "MCP SSE transport must bind to loopback only",
                ),
            });
        }

        let state = SseAppState {
            auth: Arc::clone(&self.auth),
            config: self.config.clone(),
            server_version: self.config.server_version.clone(),
            bus: self.bus.clone(),
        };

        let app = Router::new()
            .route("/mcp", post(handle_mcp_post_sse))
            .route("/mcp/health", get(handle_health_sse))
            .route("/mcp/events", get(handle_sse_events))
            .layer(tower_http::limit::RequestBodyLimitLayer::new(BODY_LIMIT))
            .with_state(state);

        let listener = tokio::net::TcpListener::bind(addr)
            .await
            .map_err(|e| CascadeError::Io {
                path: format!("{}", addr).into(),
                operation: "sse_bind",
                source: e,
            })?;

        info!(addr = %addr, "SSE MCP server listening (GET /mcp/events)");

        let shutdown = Arc::clone(&self.shutdown);
        axum::serve(listener, app)
            .with_graceful_shutdown(async move { shutdown.notified().await })
            .await
            .map_err(|e| CascadeError::Io {
                path: format!("{}", addr).into(),
                operation: "sse_serve",
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

// ── Axum state + handlers ─────────────────────────────────────────────────────

#[derive(Clone)]
struct SseAppState {
    auth: Arc<McpAuth>,
    config: McpServerConfig,
    server_version: String,
    bus: NotificationBus,
}

/// GET /mcp/events — SSE notification stream with Bearer auth.
async fn handle_sse_events(
    State(state): State<SseAppState>,
    headers: HeaderMap,
) -> impl IntoResponse {
    // ── Origin guard (CSRF) ───────────────────────────────────────────────────
    if let Err(status) = crate::security::validate_local_origin(&headers) {
        warn!("GET /mcp/events: foreign Origin/Host rejected");
        return (status, "forbidden_origin").into_response();
    }

    // Auth check.
    let token = match extract_bearer_sse(&headers) {
        Some(t) => t,
        None => {
            warn!("GET /mcp/events: missing Authorization header");
            return (StatusCode::UNAUTHORIZED, "missing Authorization").into_response();
        }
    };

    if let Err(e) = state.auth.validate_token(&token) {
        warn!(error = %e, "GET /mcp/events: token rejected");
        return (StatusCode::UNAUTHORIZED, "invalid token").into_response();
    }

    // Subscribe to the notification bus.
    let rx = state.bus.subscribe();
    let stream = BroadcastStream::new(rx).filter_map(|item| match item {
        Ok(val) => match serde_json::to_string(&val) {
            Ok(json) => Some(Ok::<Event, std::convert::Infallible>(
                Event::default().data(json),
            )),
            Err(_) => None,
        },
        Err(tokio_stream::wrappers::errors::BroadcastStreamRecvError::Lagged(n)) => {
            warn!(missed = n, "SSE client lagged — missed notifications");
            None
        }
    });

    Sse::new(stream)
        .keep_alive(
            KeepAlive::new()
                .interval(HEARTBEAT_INTERVAL)
                .text("heartbeat"),
        )
        .into_response()
}

/// POST /mcp — reuse HTTP handler logic (same auth, stateless dispatch).
///
/// Body is read via `axum::extract::Request` to avoid extractor type-inference issues.
/// The 4 MB limit is enforced by `RequestBodyLimitLayer`.
async fn handle_mcp_post_sse(
    State(state): State<SseAppState>,
    headers: HeaderMap,
    request: axum::extract::Request,
) -> impl IntoResponse {
    use crate::server::McpServer;
    use crate::transport::connection::ChannelTransportPub;

    // ── Origin guard (CSRF) ───────────────────────────────────────────────────
    if let Err(status) = crate::security::validate_local_origin(&headers) {
        warn!("POST /mcp (SSE): foreign Origin/Host rejected");
        return (
            status,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            r#"{"error":"forbidden_origin"}"#,
        )
            .into_response();
    }

    // ── Read body (needed for cap gate; body consumed once) ──────────────────
    let bytes =
        match axum::body::to_bytes(request.into_body(), crate::transport::http::BODY_LIMIT).await {
            Ok(b) => b,
            Err(_) => return (StatusCode::PAYLOAD_TOO_LARGE, "body too large").into_response(),
        };
    let body_str = match std::str::from_utf8(&bytes) {
        Ok(s) => s.to_owned(),
        Err(_) => return (StatusCode::BAD_REQUEST, "invalid utf8").into_response(),
    };

    // ── Capability gate (fires before auth; cap is a deny rule, not grant) ────
    let client_cap = crate::transport::http::parse_cap_header_pub(&headers);
    if let Some(tool_name) = crate::transport::http::extract_tools_call_name_pub(&body_str) {
        if crate::security::tool_requires_personal_data(&tool_name)
            && !client_cap.contains(crate::security::CapabilitySet::PERSONAL_DATA)
        {
            warn!(
                tool = tool_name,
                "POST /mcp (SSE): PersonalData capability required"
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

    // ── Auth ──────────────────────────────────────────────────────────────────
    let token = match extract_bearer_sse(&headers) {
        Some(t) => t,
        None => {
            return (
                StatusCode::UNAUTHORIZED,
                axum::Json(serde_json::json!({"error":"missing_token"})),
            )
                .into_response();
        }
    };
    if state.auth.validate_token(&token).is_err() {
        return (
            StatusCode::UNAUTHORIZED,
            axum::Json(serde_json::json!({"error":"invalid_token"})),
        )
            .into_response();
    }

    // Dispatch via channel-backed McpServer on a dedicated single-thread runtime.
    // McpServer is not Sync (holds Box<dyn Transport>), so its future is not Send.
    // spawn_blocking isolates it from the axum multi-thread executor.
    let config_owned = state.config.clone();
    let result = tokio::task::spawn_blocking(move || {
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .map_err(|e| e.to_string())?;
        rt.block_on(async move {
            let (msg_tx, msg_rx) = tokio::sync::mpsc::channel::<String>(2);
            let (resp_tx, mut resp_rx) = tokio::sync::mpsc::channel::<String>(2);
            let transport = ChannelTransportPub {
                recv: msg_rx,
                send: resp_tx,
            };
            let server = McpServer::new(config_owned, Box::new(transport));
            msg_tx.send(body_str).await.map_err(|e| e.to_string())?;
            drop(msg_tx);
            let _ = server.run().await;
            resp_rx.recv().await.ok_or_else(|| "no response".to_owned())
        })
    })
    .await
    .map_err(|e| e.to_string());

    match result {
        Ok(Ok(resp)) => (
            StatusCode::OK,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            resp,
        )
            .into_response(),
        _ => (StatusCode::INTERNAL_SERVER_ERROR, "no response").into_response(),
    }
}

/// GET /mcp/health — unauthenticated (still subject to Origin guard).
async fn handle_health_sse(
    State(state): State<SseAppState>,
    headers: HeaderMap,
) -> impl IntoResponse {
    if let Err(status) = crate::security::validate_local_origin(&headers) {
        warn!("GET /mcp/health (SSE): foreign Origin/Host rejected");
        return (
            status,
            [(axum::http::header::CONTENT_TYPE, "application/json")],
            r#"{"error":"forbidden_origin"}"#,
        )
            .into_response();
    }
    axum::Json(serde_json::json!({
        "status": "ok",
        "version": state.server_version,
    }))
    .into_response()
}

fn extract_bearer_sse(headers: &HeaderMap) -> Option<String> {
    let value = headers
        .get(axum::http::header::AUTHORIZATION)?
        .to_str()
        .ok()?;
    value.strip_prefix("Bearer ").map(|s| s.to_owned())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use axum::http::Request;
    use tower::ServiceExt;

    fn make_auth() -> Arc<McpAuth> {
        Arc::new(McpAuth::from_secret([77u8; 32]))
    }

    fn make_sse_app(auth: Arc<McpAuth>, bus: NotificationBus) -> Router {
        let config = McpServerConfig::default();
        let state = SseAppState {
            server_version: config.server_version.clone(),
            auth,
            config,
            bus,
        };
        Router::new()
            .route("/mcp", post(handle_mcp_post_sse))
            .route("/mcp/health", get(handle_health_sse))
            .route("/mcp/events", get(handle_sse_events))
            .layer(tower_http::limit::RequestBodyLimitLayer::new(BODY_LIMIT))
            .with_state(state)
    }

    #[tokio::test(flavor = "current_thread")]
    async fn sse_events_without_auth_returns_401() {
        let auth = make_auth();
        let bus = NotificationBus::new();
        let app = make_sse_app(auth, bus);

        let req = Request::builder()
            .method("GET")
            .uri("/mcp/events")
            .body(Body::empty())
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::UNAUTHORIZED);
    }

    #[tokio::test(flavor = "current_thread")]
    async fn sse_events_with_valid_auth_returns_200_event_stream() {
        let auth = make_auth();
        let token = auth.generate_token();
        let bus = NotificationBus::new();
        let app = make_sse_app(Arc::clone(&auth), bus.clone());

        let req = Request::builder()
            .method("GET")
            .uri("/mcp/events")
            .header("Authorization", format!("Bearer {}", token))
            .body(Body::empty())
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        let content_type = resp
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        assert!(
            content_type.contains("text/event-stream"),
            "Expected text/event-stream, got: {}",
            content_type
        );
    }

    #[tokio::test(flavor = "current_thread")]
    async fn sse_notification_appears_in_stream() {
        use cascade_types::mcp::JsonRpcNotification;

        let auth = make_auth();
        let token = auth.generate_token();
        let bus = NotificationBus::new();
        let bus2 = bus.clone();

        // Use a random available port for testing.
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        drop(listener);

        let local = tokio::task::LocalSet::new();
        local
            .run_until(async {
                let server = Arc::new(SseServer::new(
                    Some(port),
                    Arc::clone(&auth),
                    McpServerConfig::default(),
                    bus2.clone(),
                ));
                let server2 = Arc::clone(&server);
                let handle = tokio::task::spawn_local(async move { server2.listen().await });

                tokio::time::sleep(std::time::Duration::from_millis(50)).await;

                let client = reqwest::Client::new();
                let resp_future = client
                    .get(format!("http://127.0.0.1:{}/mcp/events", port))
                    .header("Authorization", format!("Bearer {}", token))
                    .send();

                tokio::time::sleep(std::time::Duration::from_millis(20)).await;

                let notif = JsonRpcNotification::<serde_json::Value>::new(
                    "notifications/tools/list_changed",
                    Some(serde_json::json!({})),
                );
                bus2.broadcast(notif);

                let resp =
                    tokio::time::timeout(std::time::Duration::from_secs(2), resp_future).await;
                if let Ok(Ok(response)) = resp {
                    assert_eq!(response.status().as_u16(), 200);
                }

                server.stop().await.unwrap();
                let _ = handle.await;
            })
            .await;
    }

    #[tokio::test(flavor = "current_thread")]
    async fn sse_server_bind_addr_is_loopback() {
        let auth = make_auth();
        let bus = NotificationBus::new();
        let server = SseServer::new(None, auth, McpServerConfig::default(), bus);
        let addr = server.bind_addr();
        assert!(addr.ip().is_loopback());
    }
}
