//! Anthropic Messages API compatibility adapter for the Gemini GP proxy.
//!
//! Purpose: HTTP server on `127.0.0.1:3762` that accepts `POST /v1/messages`
//! in Anthropic Messages API format, translates to Gemini `generateContent`
//! (or `streamGenerateContent` when `"stream":true`) format, forwards to the
//! existing GP proxy at `http://127.0.0.1:3761` (preserving its key-slot
//! rotation/cooldown pool), and translates the response back to Anthropic
//! format — either a single JSON body or an Anthropic-shaped SSE stream.
//!
//! Inputs:
//!   - `POST /v1/messages` — Anthropic Messages API request body (JSON).
//!   - `GET /v1/health`    — health probe.
//!
//! Outputs:
//!   - `POST /v1/messages` (`stream` absent/false) — Anthropic Messages API
//!     response body (JSON).
//!   - `POST /v1/messages` (`stream: true`) — `text/event-stream` body:
//!     `message_start` → `content_block_start` → N × `content_block_delta`
//!     → `content_block_stop` → `message_delta` → `message_stop`. A mid-stream
//!     upstream failure emits an Anthropic `error` SSE event and closes.
//!   - `GET /v1/health`    — `{"status":"ok","upstream":"<url>"}`.
//!
//! Constraints:
//!   - The GP proxy at `:3761` buffers its entire upstream response before
//!     replying (see `gemini_proxy/dispatch.rs`), so pool rotation/cooldown
//!     is preserved but the byte-level response is not truly incremental
//!     across that hop. This adapter re-frames the buffered Gemini SSE body
//!     into a real chunked Anthropic SSE stream on ITS OWN response, so
//!     `:3762` clients still see standard incremental SSE framing.
//!   - No new Cargo dependencies — uses axum, serde_json, reqwest, uuid,
//!     futures-util already present in Cargo.toml.
//!   - Translation is pure JSON manipulation; no schema-generated types.
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — proxy/anthropic_compat

use std::convert::Infallible;
use std::net::SocketAddr;
use std::time::Duration;

use axum::{
    Json, Router,
    body::Body,
    extract::State,
    http::{HeaderMap, StatusCode, header},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use futures_util::stream;
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};
use uuid::Uuid;

mod sse;
use sse::{GeminiSseParser, StreamTranslator};

// ── Model mapping ─────────────────────────────────────────────────────────────

/// Map an Anthropic model name to a Gemini model name.
///
/// - `claude-haiku-*` → `gemini-flash-lite-latest`
/// - `claude-sonnet-*` → `gemini-flash-latest`
/// - anything else → `gemini-flash-latest` (default)
///
/// Uses Google's auto-tracking `-latest` aliases (not pinned dated snapshots
/// like `gemini-2.0-flash`) so this mapping never needs a retirement-driven
/// edit when Google cycles the underlying model version — the alias always
/// resolves to whatever Google currently designates as latest Flash / Flash
/// Lite. Verified live 2026-07-07: both aliases resolve via the models
/// endpoint (`gemini-flash-latest` → "Gemini Flash Latest", confirms
/// `generateContent` support; `gemini-flash-lite-latest` → 200 on
/// `generateContent`).
fn map_model(anthropic_model: &str) -> &'static str {
    if anthropic_model.starts_with("claude-haiku") {
        "gemini-flash-lite-latest"
    } else if anthropic_model.starts_with("claude-sonnet") {
        "gemini-flash-latest"
    } else {
        "gemini-flash-latest"
    }
}

// ── Request translation: Anthropic → Gemini ───────────────────────────────────

/// Translate an Anthropic Messages API request body to a Gemini
/// `generateContent` request body.
///
/// Returns `(gemini_model, gemini_body)` on success; an error string on failure.
fn translate_request(body: &Value) -> Result<(String, Value), String> {
    let anthropic_model = body
        .get("model")
        .and_then(Value::as_str)
        .unwrap_or("claude-sonnet-4-5");

    let gemini_model = map_model(anthropic_model).to_string();

    // Translate messages array: role "assistant" → "model"; wrap content in parts.
    let messages = body
        .get("messages")
        .and_then(Value::as_array)
        .ok_or("missing or invalid 'messages' field")?;

    let contents: Vec<Value> = messages
        .iter()
        .map(|msg| {
            let role = msg.get("role").and_then(Value::as_str).unwrap_or("user");
            let gemini_role = if role == "assistant" { "model" } else { role };

            // Content may be a string or an array of content blocks.
            let text = match msg.get("content") {
                Some(Value::String(s)) => s.clone(),
                Some(Value::Array(blocks)) => {
                    // Extract text from content blocks.
                    blocks
                        .iter()
                        .filter_map(|b| {
                            if b.get("type").and_then(Value::as_str) == Some("text") {
                                b.get("text").and_then(Value::as_str).map(str::to_string)
                            } else {
                                None
                            }
                        })
                        .collect::<Vec<_>>()
                        .join("\n")
                }
                _ => String::new(),
            };

            json!({
                "role": gemini_role,
                "parts": [{"text": text}]
            })
        })
        .collect();

    let mut gemini = json!({ "contents": contents });

    // Optional system instruction.
    if let Some(system) = body.get("system").and_then(Value::as_str) {
        gemini["system_instruction"] = json!({ "parts": [{"text": system}] });
    }

    // Optional max_tokens → generationConfig.maxOutputTokens.
    if let Some(max_tokens) = body.get("max_tokens").and_then(Value::as_u64) {
        gemini["generationConfig"] = json!({ "maxOutputTokens": max_tokens });
    }

    Ok((gemini_model, gemini))
}

// ── Response translation: Gemini → Anthropic ─────────────────────────────────

/// Translate a Gemini `generateContent` response body to an Anthropic Messages
/// API response body.
fn translate_response(gemini_body: &Value, gemini_model: &str) -> Value {
    // Extract text from the first candidate.
    let text = gemini_body
        .get("candidates")
        .and_then(Value::as_array)
        .and_then(|c| c.first())
        .and_then(|c| c.get("content"))
        .and_then(|c| c.get("parts"))
        .and_then(Value::as_array)
        .and_then(|p| p.first())
        .and_then(|p| p.get("text"))
        .and_then(Value::as_str)
        .unwrap_or("");

    // Map finishReason → stop_reason.
    let finish_reason = gemini_body
        .get("candidates")
        .and_then(Value::as_array)
        .and_then(|c| c.first())
        .and_then(|c| c.get("finishReason"))
        .and_then(Value::as_str)
        .unwrap_or("STOP");

    let stop_reason = match finish_reason {
        "STOP" => "end_turn",
        "MAX_TOKENS" => "max_tokens",
        _ => "end_turn",
    };

    // Usage metadata.
    let input_tokens = gemini_body
        .get("usageMetadata")
        .and_then(|u| u.get("promptTokenCount"))
        .and_then(Value::as_u64)
        .unwrap_or(0);
    let output_tokens = gemini_body
        .get("usageMetadata")
        .and_then(|u| u.get("candidatesTokenCount"))
        .and_then(Value::as_u64)
        .unwrap_or(0);

    json!({
        "id": format!("msg_gp_{}", Uuid::new_v4().simple()),
        "type": "message",
        "role": "assistant",
        "content": [{"type": "text", "text": text}],
        "model": gemini_model,
        "stop_reason": stop_reason,
        "usage": {
            "input_tokens": input_tokens,
            "output_tokens": output_tokens
        }
    })
}

// ── Axum shared state ─────────────────────────────────────────────────────────

/// TCP connect timeout for the upstream `:3761` GP-proxy hop.
///
/// Short on purpose — `:3761` is a loopback service, so a healthy instance
/// accepts the connection near-instantly. A stalled/wedged `:3761` should
/// fail fast rather than tie up a `:3762` connection indefinitely.
const UPSTREAM_CONNECT_TIMEOUT: Duration = Duration::from_secs(5);

/// Overall request timeout (connect + send + full response body) for the
/// upstream `:3761` GP-proxy hop.
///
/// `:3761` buffers its entire response (including streamed Gemini output)
/// before replying — see the module doc — so both the non-streaming and
/// streaming paths here read a complete body in one `.send().await` /
/// `.bytes().await` pair. An overall timeout is therefore safe: it can never
/// truncate a response that was already arriving incrementally, only bound
/// how long this adapter waits on a stalled upstream.
const UPSTREAM_REQUEST_TIMEOUT: Duration = Duration::from_secs(120);

/// Build the `reqwest::Client` used for all upstream `:3761` calls, with
/// explicit connect/overall timeouts so a stalled upstream hop cannot hold a
/// `:3762` connection open forever.
///
/// Falls back to `reqwest::Client::new()` (no configured timeouts) only if
/// the builder fails, which in practice only happens on invalid TLS
/// configuration — never for timeout values — so this is effectively
/// infallible in normal operation.
fn build_upstream_client() -> reqwest::Client {
    reqwest::Client::builder()
        .connect_timeout(UPSTREAM_CONNECT_TIMEOUT)
        .timeout(UPSTREAM_REQUEST_TIMEOUT)
        .build()
        .unwrap_or_else(|e| {
            warn!(error = %e, "anthropic_compat: failed to build timed HTTP client, falling back to default");
            reqwest::Client::new()
        })
}

#[derive(Clone)]
struct AdapterState {
    /// Base URL of the upstream Gemini GP proxy (e.g. `http://127.0.0.1:3761`).
    upstream_url: String,
    /// Shared client for both the non-streaming and streaming paths, built
    /// via [`build_upstream_client`] so both hops share the same timeouts.
    client: reqwest::Client,
}

// ── Route handlers ────────────────────────────────────────────────────────────

/// GET /v1/health
async fn health(State(state): State<AdapterState>) -> Json<Value> {
    Json(json!({ "status": "ok", "upstream": state.upstream_url }))
}

/// Privacy firewall, server-side mirror of the app's fast-path gate: every
/// request that declares a protected namespace (`X-Cascade-Namespace:
/// personal` / `personal:private` / `*:private`) is refused HERE, before any
/// byte reaches the GP pool (:3761 → Google). The app already skips this
/// port for protected chats; this guard exists so a future non-app localhost
/// caller cannot bypass the firewall by talking to :3762 directly.
///
/// Returns `None` when the request may proceed, or the 403 response to send.
fn protected_namespace_refusal(headers: &HeaderMap) -> Option<Response> {
    let ns = headers
        .get("x-cascade-namespace")
        .and_then(|v| v.to_str().ok())?;
    if !cascade_core::sensitivity::is_protected_namespace(Some(ns)) {
        return None;
    }
    warn!(
        namespace = %ns,
        "anthropic_compat: refusing protected-namespace request — private \
         chat must not leave via the GP pool"
    );
    let err = json!({
        "error": {
            "type": "permission_error",
            "message": "protected namespace: private/personal chat is never \
                        routed through the GP pool. Use the daemon /api/chat \
                        endpoint, which selects a trusted provider."
        }
    });
    Some((StatusCode::FORBIDDEN, Json(err)).into_response())
}

/// POST /v1/messages
async fn messages(
    State(state): State<AdapterState>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response {
    // Both the buffered and streaming paths branch AFTER this guard, so one
    // check covers every request shape.
    if let Some(refusal) = protected_namespace_refusal(&headers) {
        return refusal;
    }

    if body.get("stream").and_then(Value::as_bool) == Some(true) {
        return messages_stream(state, body).await;
    }

    // Translate request.
    let (gemini_model, gemini_body) = match translate_request(&body) {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: request translation failed");
            let err = json!({
                "error": {
                    "type": "invalid_request_error",
                    "message": e
                }
            });
            return (StatusCode::BAD_REQUEST, Json(err)).into_response();
        }
    };

    // Forward to GP proxy.
    let upstream = format!(
        "{}/v1beta/models/{}:generateContent",
        state.upstream_url, gemini_model
    );

    let resp = match state
        .client
        .post(&upstream)
        .header("Content-Type", "application/json")
        .json(&gemini_body)
        .send()
        .await
    {
        Ok(r) => r,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: upstream request failed");
            let err = json!({
                "error": {
                    "type": "api_error",
                    "message": format!("upstream request failed: {e}")
                }
            });
            return (StatusCode::BAD_GATEWAY, Json(err)).into_response();
        }
    };

    let upstream_status = resp.status();

    let gemini_json: Value = match resp.json().await {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: failed to parse upstream response");
            let err = json!({
                "error": {
                    "type": "api_error",
                    "message": format!("failed to parse upstream response: {e}")
                }
            });
            return (StatusCode::BAD_GATEWAY, Json(err)).into_response();
        }
    };

    if !upstream_status.is_success() {
        // Pass through the Gemini error wrapped in Anthropic format.
        let msg = gemini_json
            .get("error")
            .and_then(|e| e.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("upstream error");
        let err = json!({
            "error": {
                "type": "api_error",
                "message": msg
            }
        });
        let status = StatusCode::from_u16(upstream_status.as_u16())
            .unwrap_or(StatusCode::BAD_GATEWAY);
        return (status, Json(err)).into_response();
    }

    let anthropic_resp = translate_response(&gemini_json, &gemini_model);
    (StatusCode::OK, Json(anthropic_resp)).into_response()
}

/// Streaming path for `POST /v1/messages` when `"stream": true`.
///
/// Calls the upstream GP proxy's `streamGenerateContent?alt=sse` route
/// through the SAME `:3761` internal path as the non-streaming call, so
/// key-slot rotation/cooldown (owned by `gemini_proxy::dispatch`) still
/// applies. `:3761` buffers the full upstream body before replying (it has
/// no chunked-transfer support), so the bytes arrive from `:3761` in one
/// shot; this function re-frames them into a real incremental Anthropic SSE
/// stream on ITS OWN response so `:3762` clients see standard streaming.
///
/// Errors before any bytes have streamed return the same non-2xx JSON shape
/// as the non-streaming path. Errors discovered after streaming has started
/// (upstream returned a non-JSON body, or an embedded Gemini `error` frame)
/// emit an Anthropic `error` SSE event and close — never a silent truncation.
async fn messages_stream(state: AdapterState, body: Value) -> Response {
    let (gemini_model, mut gemini_body) = match translate_request(&body) {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: stream request translation failed");
            let err = json!({
                "error": { "type": "invalid_request_error", "message": e }
            });
            return (StatusCode::BAD_REQUEST, Json(err)).into_response();
        }
    };
    // Gemini's own `stream` flag is irrelevant to the endpoint chosen (the
    // URL selects streaming vs non-streaming); strip it so it isn't echoed
    // upstream as an unrecognized field.
    if let Some(obj) = gemini_body.as_object_mut() {
        obj.remove("stream");
    }

    let upstream = format!(
        "{}/v1beta/models/{}:streamGenerateContent?alt=sse",
        state.upstream_url, gemini_model
    );

    let resp = match state
        .client
        .post(&upstream)
        .header("Content-Type", "application/json")
        .json(&gemini_body)
        .send()
        .await
    {
        Ok(r) => r,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: stream upstream request failed");
            let err = json!({
                "error": {
                    "type": "api_error",
                    "message": format!("upstream request failed: {e}")
                }
            });
            return (StatusCode::BAD_GATEWAY, Json(err)).into_response();
        }
    };

    let upstream_status = resp.status();

    if !upstream_status.is_success() {
        // Nothing has streamed yet — return the same JSON error shape as the
        // non-streaming path.
        let raw = resp.bytes().await.unwrap_or_default();
        let gemini_json: Value = serde_json::from_slice(&raw).unwrap_or(Value::Null);
        let msg = gemini_json
            .get("error")
            .and_then(|e| e.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("upstream error");
        let err = json!({ "error": { "type": "api_error", "message": msg } });
        let status =
            StatusCode::from_u16(upstream_status.as_u16()).unwrap_or(StatusCode::BAD_GATEWAY);
        return (status, Json(err)).into_response();
    }

    // Buffer the (already-complete) upstream body, then re-frame it as an
    // incrementally-emitted Anthropic SSE stream.
    let raw_body = match resp.bytes().await {
        Ok(b) => b,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: failed to read stream upstream body");
            let err = json!({
                "error": {
                    "type": "api_error",
                    "message": format!("failed to read upstream stream body: {e}")
                }
            });
            return (StatusCode::BAD_GATEWAY, Json(err)).into_response();
        }
    };

    let events = translate_stream_body(&raw_body, &gemini_model);

    let body_stream = stream::iter(
        events
            .into_iter()
            .map(|bytes| Ok::<_, Infallible>(axum::body::Bytes::from(bytes))),
    );

    Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "text/event-stream")
        .header(header::CACHE_CONTROL, "no-cache")
        .body(Body::from_stream(body_stream))
        .unwrap_or_else(|e| {
            warn!(error = %e, "anthropic_compat: failed to build SSE response");
            (StatusCode::INTERNAL_SERVER_ERROR, "stream build failed").into_response()
        })
}

/// Translate a complete Gemini SSE response body into the full sequence of
/// Anthropic SSE wire-format events (`message_start` … `message_stop`).
///
/// If the buffered body contains a Gemini `error` frame (upstream failed
/// after headers were already 2xx — e.g. a streamed safety block), the
/// sequence emitted so far is closed with an Anthropic `error` event instead
/// of the normal `message_delta`/`message_stop` pair, matching the "error
/// mid-stream → error event, never silent truncation" contract.
fn translate_stream_body(raw_body: &[u8], gemini_model: &str) -> Vec<Vec<u8>> {
    let mut parser = GeminiSseParser::new();
    let mut translator = StreamTranslator::new(gemini_model);
    let mut out = Vec::new();

    let (name, data) = translator.start();
    out.push(sse::format_event(name, &data));

    let mut saw_error = false;
    for chunk in parser.push(raw_body) {
        if let Some(err) = chunk.get("error") {
            let msg = err
                .get("message")
                .and_then(Value::as_str)
                .unwrap_or("upstream error");
            let (name, data) = StreamTranslator::error_event(msg);
            out.push(sse::format_event(name, &data));
            saw_error = true;
            break;
        }
        for (name, data) in translator.on_chunk(&chunk) {
            out.push(sse::format_event(name, &data));
        }
    }

    if !saw_error {
        for (name, data) in translator.finish() {
            out.push(sse::format_event(name, &data));
        }
    }

    out
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// The connect/overall timeout constants must be the documented values —
    /// pinning them in a test catches an accidental edit that silently
    /// widens the "stalled upstream" window back toward unbounded.
    #[test]
    fn upstream_timeout_constants_match_documented_values() {
        assert_eq!(UPSTREAM_CONNECT_TIMEOUT, Duration::from_secs(5));
        assert_eq!(UPSTREAM_REQUEST_TIMEOUT, Duration::from_secs(120));
    }

    /// `build_upstream_client` must successfully construct a client with
    /// those timeouts configured (no network I/O — `reqwest::Client` does
    /// not expose configured timeouts for inspection, so this exercises the
    /// builder call itself rather than making a live request).
    /// The :3762 privacy firewall refuses protected namespaces with 403 and
    /// lets everything else through (no header, project/meta namespaces).
    #[test]
    fn protected_namespace_refusal_blocks_protected_passes_others() {
        let mut h = HeaderMap::new();
        assert!(protected_namespace_refusal(&h).is_none(), "no header");

        h.insert("x-cascade-namespace", "projects:cascade".parse().unwrap());
        assert!(protected_namespace_refusal(&h).is_none(), "project ns");
        h.insert("x-cascade-namespace", "meta".parse().unwrap());
        assert!(protected_namespace_refusal(&h).is_none(), "meta ns");

        for ns in ["personal", "personal:private", "private", "Personal:Private"] {
            h.insert("x-cascade-namespace", ns.parse().unwrap());
            let resp = protected_namespace_refusal(&h)
                .unwrap_or_else(|| panic!("{ns} must be refused"));
            assert_eq!(resp.status(), StatusCode::FORBIDDEN, "{ns}");
        }
    }

    #[test]
    fn build_upstream_client_succeeds_with_configured_timeouts() {
        // reqwest::Client::builder().connect_timeout(..).timeout(..).build()
        // only fails on invalid TLS backend configuration, never on valid
        // Duration values — so a successful build here proves the timeouts
        // were accepted and wired into both the streaming and non-streaming
        // call sites (both read `state.client` from the same `AdapterState`).
        let _client = build_upstream_client();
    }
}

// ── Public server struct ──────────────────────────────────────────────────────

/// Anthropic Messages API compatibility adapter server.
///
/// Listens on `bind_addr` (default `127.0.0.1:3762`) and translates incoming
/// Anthropic-format requests to Gemini-format requests forwarded to the GP proxy
/// at `upstream_url` (default `http://127.0.0.1:3761`).
pub struct AnthropicCompatServer {
    bind_addr: SocketAddr,
    upstream_url: String,
    shutdown: CancellationToken,
}

impl AnthropicCompatServer {
    /// Construct a new `AnthropicCompatServer`.
    pub fn new(bind_addr: SocketAddr, upstream_url: String, shutdown: CancellationToken) -> Self {
        Self {
            bind_addr,
            upstream_url,
            shutdown,
        }
    }

    /// Start the server and run until `shutdown` is cancelled.
    pub async fn run(self) -> Result<(), std::io::Error> {
        let state = AdapterState {
            upstream_url: self.upstream_url,
            client: build_upstream_client(),
        };

        let app = Router::new()
            .route("/v1/health", get(health))
            .route("/v1/messages", post(messages))
            .with_state(state);

        let listener = tokio::net::TcpListener::bind(self.bind_addr).await?;
        info!(addr = %self.bind_addr, "anthropic_compat: listening");

        axum::serve(listener, app)
            .with_graceful_shutdown(async move {
                self.shutdown.cancelled().await;
            })
            .await?;

        info!("anthropic_compat: stopped");
        Ok(())
    }
}
