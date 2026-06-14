//! # bridge
//!
//! HTTP+SSE bridge that exposes a `/v1/messages`-compatible API backed by a
//! [`ProcessDriver`].
//!
//! # Purpose
//! Accepts `POST /v1/messages` requests in Anthropic Messages API format,
//! forwards the last `user` message to the `ProcessDriver`, and streams the
//! response back as SSE (`text/event-stream`) with the standard
//! `content_block_delta` event shape.
//!
//! # Request format (subset of Anthropic Messages API)
//!
//! ```json
//! {
//!   "model": "claude-code",
//!   "messages": [{"role": "user", "content": "Hello"}],
//!   "stream": true
//! }
//! ```
//!
//! # Response format (SSE)
//!
//! ```text
//! event: content_block_delta
//! data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}
//!
//! event: message_stop
//! data: {"type":"message_stop"}
//! ```
//!
//! # Inputs
//! - `BridgeConfig` — bind address, port, RPM quota
//! - `Arc<dyn ProcessDriver>` — the driver to forward requests to
//!
//! # Outputs
//! - SSE stream on `POST /v1/messages`
//! - `GET /v1/status` — JSON status object
//!
//! # Constraints
//! - Binds to `127.0.0.1` by default (never `0.0.0.0`) for security.
//! - No authentication on the local port — intended for local use only.
//! - Driver is called sequentially within a session (no concurrent prompts).

use std::convert::Infallible;
use std::net::SocketAddr;
use std::sync::Arc;

use axum::{
    extract::State,
    http::StatusCode,
    response::{
        sse::{Event, KeepAlive, Sse},
        IntoResponse, Response,
    },
    routing::{get, post},
    Json, Router,
};
use serde::{Deserialize, Serialize};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;

use crate::{driver::ProcessDriver, quota::QuotaGuard};

// ── BridgeConfig ──────────────────────────────────────────────────────────────

/// Configuration for the HTTP+SSE bridge.
#[derive(Debug, Clone)]
pub struct BridgeConfig {
    /// Bind address (default: `127.0.0.1`). Never bind to `0.0.0.0` in production.
    pub host: String,
    /// Port number (default: 7190).
    pub port: u16,
    /// Token-bucket quota guard.
    pub quota: Arc<QuotaGuard>,
}

impl Default for BridgeConfig {
    fn default() -> Self {
        Self {
            host: "127.0.0.1".into(),
            port: 7190,
            quota: QuotaGuard::default_quota(),
        }
    }
}

impl BridgeConfig {
    /// Return the socket address derived from host + port.
    pub fn socket_addr(&self) -> SocketAddr {
        format!("{}:{}", self.host, self.port)
            .parse()
            .expect("BridgeConfig host:port is not a valid socket address")
    }
}

// ── Request / Response types ──────────────────────────────────────────────────

/// A single message in the request.
#[derive(Debug, Deserialize)]
pub struct RequestMessage {
    pub role: String,
    pub content: String,
}

/// `POST /v1/messages` request body (subset of Anthropic Messages API).
#[derive(Debug, Deserialize)]
pub struct MessagesRequest {
    #[allow(dead_code)]
    pub model: Option<String>,
    pub messages: Vec<RequestMessage>,
    #[serde(default)]
    pub stream: bool,
}

/// `GET /v1/status` response.
#[derive(Debug, Serialize)]
pub struct StatusResponse {
    pub status: String,
    pub driver: String,
    pub quota_rpm: u32,
}

/// Error response body.
#[derive(Debug, Serialize)]
struct ErrorBody {
    error: String,
}

// ── Shared state ──────────────────────────────────────────────────────────────

#[derive(Clone)]
pub(crate) struct AppState {
    pub driver: Arc<dyn ProcessDriver>,
    pub quota: Arc<QuotaGuard>,
    pub driver_name: String,
}

// ── Router ────────────────────────────────────────────────────────────────────

/// Build the axum Router with shared state.
pub fn build_router(
    driver: Arc<dyn ProcessDriver>,
    config: &BridgeConfig,
    driver_name: impl Into<String>,
) -> Router {
    let state = AppState {
        driver,
        quota: config.quota.clone(),
        driver_name: driver_name.into(),
    };

    Router::new()
        .route("/v1/messages", post(handle_messages))
        .route("/v1/status", get(handle_status))
        .with_state(state)
}

// ── Handlers ──────────────────────────────────────────────────────────────────

/// `POST /v1/messages` — forward to driver, return SSE stream.
async fn handle_messages(
    State(state): State<AppState>,
    Json(req): Json<MessagesRequest>,
) -> Response {
    // 1. Quota check (non-blocking atomic).
    if let Err(e) = state.quota.check() {
        let body = serde_json::to_string(&ErrorBody {
            error: e.to_string(),
        })
        .unwrap_or_default();
        return (StatusCode::TOO_MANY_REQUESTS, body).into_response();
    }

    // 2. Extract the last user message.
    let prompt = req
        .messages
        .iter()
        .rev()
        .find(|m| m.role == "user")
        .map(|m| m.content.clone())
        .unwrap_or_default();

    if prompt.is_empty() {
        let body = serde_json::to_string(&ErrorBody {
            error: "no user message found in request".into(),
        })
        .unwrap_or_default();
        return (StatusCode::BAD_REQUEST, body).into_response();
    }

    // 3. Drive the process driver.
    let chunks = match state.driver.send_prompt(&prompt).await {
        Ok(c) => c,
        Err(e) => {
            let body = serde_json::to_string(&ErrorBody {
                error: e.to_string(),
            })
            .unwrap_or_default();
            return (StatusCode::INTERNAL_SERVER_ERROR, body).into_response();
        }
    };

    // 4. Build SSE stream from chunks via mpsc channel.
    let (tx, rx) = mpsc::channel::<Result<Event, Infallible>>(32);

    tokio::spawn(async move {
        for chunk in chunks {
            if chunk.done {
                let stop_event = Event::default()
                    .event("message_stop")
                    .data(r#"{"type":"message_stop"}"#);
                let _ = tx.send(Ok(stop_event)).await;
            } else {
                let delta = serde_json::json!({
                    "type": "content_block_delta",
                    "delta": {
                        "type": "text_delta",
                        "text": chunk.text
                    }
                });
                let event = Event::default()
                    .event("content_block_delta")
                    .data(delta.to_string());
                let _ = tx.send(Ok(event)).await;
            }
        }
        // tx is dropped here → stream closes.
    });

    let stream = ReceiverStream::new(rx);
    Sse::new(stream)
        .keep_alive(KeepAlive::new())
        .into_response()
}

/// `GET /v1/status` — return bridge status as JSON.
async fn handle_status(State(state): State<AppState>) -> Json<StatusResponse> {
    Json(StatusResponse {
        status: "running".into(),
        driver: state.driver_name.clone(),
        quota_rpm: state.quota.rpm(),
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::driver::MockDriver;
    use axum::{body::Body, http::Request};
    use tower::ServiceExt; // for `oneshot`

    fn build_test_router(driver: MockDriver) -> Router {
        let config = BridgeConfig {
            host: "127.0.0.1".into(),
            port: 7190,
            quota: QuotaGuard::new(60),
        };
        build_router(Arc::new(driver), &config, "MockDriver")
    }

    #[tokio::test]
    async fn status_endpoint_returns_running() {
        let router = build_test_router(MockDriver::default());
        let req = Request::builder()
            .method("GET")
            .uri("/v1/status")
            .body(Body::empty())
            .unwrap();
        let resp = router.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body_bytes = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .unwrap();
        let json: serde_json::Value = serde_json::from_slice(&body_bytes).unwrap();
        assert_eq!(json["status"], "running");
        assert_eq!(json["driver"], "MockDriver");
    }

    #[tokio::test]
    async fn messages_endpoint_returns_sse() {
        let router = build_test_router(MockDriver::with_response("hello world"));
        let body = serde_json::json!({
            "messages": [{"role": "user", "content": "hi"}],
            "stream": true
        });
        let req = Request::builder()
            .method("POST")
            .uri("/v1/messages")
            .header("content-type", "application/json")
            .body(Body::from(body.to_string()))
            .unwrap();
        let resp = router.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let content_type = resp
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        assert!(
            content_type.contains("text/event-stream"),
            "response should be SSE, got: {content_type}"
        );
    }

    #[tokio::test]
    async fn messages_no_user_message_returns_400() {
        let router = build_test_router(MockDriver::default());
        let body = serde_json::json!({
            "messages": [{"role": "assistant", "content": "I am the assistant"}],
        });
        let req = Request::builder()
            .method("POST")
            .uri("/v1/messages")
            .header("content-type", "application/json")
            .body(Body::from(body.to_string()))
            .unwrap();
        let resp = router.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::BAD_REQUEST);
    }

    #[tokio::test]
    async fn quota_exhausted_returns_429() {
        // Build a quota guard with burst=1 and manually drain it.
        let quota = QuotaGuard::new(1); // burst = min(1,5) = 1
        let _ = quota.check(); // consume the single token

        let driver: Arc<dyn ProcessDriver> = Arc::new(MockDriver::default());
        let state = AppState {
            driver,
            quota: quota.clone(),
            driver_name: "mock".into(),
        };
        let router = Router::new()
            .route("/v1/messages", post(handle_messages))
            .with_state(state);

        let body = serde_json::json!({
            "messages": [{"role": "user", "content": "hi"}],
        });
        let req = Request::builder()
            .method("POST")
            .uri("/v1/messages")
            .header("content-type", "application/json")
            .body(Body::from(body.to_string()))
            .unwrap();
        let resp = router.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::TOO_MANY_REQUESTS);
    }

    #[tokio::test]
    async fn driver_error_returns_500() {
        let router = build_test_router(MockDriver::failing());
        let body = serde_json::json!({
            "messages": [{"role": "user", "content": "hi"}],
        });
        let req = Request::builder()
            .method("POST")
            .uri("/v1/messages")
            .header("content-type", "application/json")
            .body(Body::from(body.to_string()))
            .unwrap();
        let resp = router.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::INTERNAL_SERVER_ERROR);
    }

    #[test]
    fn bridge_config_socket_addr() {
        let cfg = BridgeConfig::default();
        let addr = cfg.socket_addr();
        assert_eq!(addr.port(), 7190);
        assert_eq!(addr.ip().to_string(), "127.0.0.1");
    }
}
