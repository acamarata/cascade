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

use axum::{
    Json, Router,
    body::Body,
    extract::State,
    http::{StatusCode, header},
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
/// - `claude-haiku-*` → `gemini-2.0-flash-lite`
/// - `claude-sonnet-*` → `gemini-2.0-flash`
/// - anything else → `gemini-2.0-flash` (default)
fn map_model(anthropic_model: &str) -> &'static str {
    if anthropic_model.starts_with("claude-haiku") {
        "gemini-2.0-flash-lite"
    } else if anthropic_model.starts_with("claude-sonnet") {
        "gemini-2.0-flash"
    } else {
        "gemini-2.0-flash"
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

#[derive(Clone)]
struct AdapterState {
    /// Base URL of the upstream Gemini GP proxy (e.g. `http://127.0.0.1:3761`).
    upstream_url: String,
    client: reqwest::Client,
}

// ── Route handlers ────────────────────────────────────────────────────────────

/// GET /v1/health
async fn health(State(state): State<AdapterState>) -> Json<Value> {
    Json(json!({ "status": "ok", "upstream": state.upstream_url }))
}

/// POST /v1/messages
async fn messages(
    State(state): State<AdapterState>,
    Json(body): Json<Value>,
) -> Response {
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
            client: reqwest::Client::new(),
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
