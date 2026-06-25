//! Anthropic Messages API compatibility adapter for the Gemini GP proxy.
//!
//! Purpose: HTTP server on `127.0.0.1:3762` that accepts `POST /v1/messages`
//! in Anthropic Messages API format, translates to Gemini `generateContent`
//! format, forwards to the existing GP proxy at `http://127.0.0.1:3761`, and
//! translates the response back to Anthropic format.
//!
//! Inputs:
//!   - `POST /v1/messages` — Anthropic Messages API request body (JSON).
//!   - `GET /v1/health`    — health probe.
//!
//! Outputs:
//!   - `POST /v1/messages` — Anthropic Messages API response body (JSON).
//!   - `GET /v1/health`    — `{"status":"ok","upstream":"<url>"}`.
//!
//! Constraints:
//!   - Streaming not supported; `"stream":true` returns HTTP 400.
//!   - No new Cargo dependencies — uses axum, serde_json, reqwest, uuid
//!     already present in Cargo.toml.
//!   - Translation is pure JSON manipulation; no schema-generated types.
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — proxy/anthropic_compat

use std::net::SocketAddr;

use axum::{
    Json, Router,
    extract::State,
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::{get, post},
};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};
use uuid::Uuid;

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
    // Reject streaming requests.
    if body.get("stream").and_then(Value::as_bool) == Some(true) {
        let err = json!({
            "error": {
                "type": "not_supported",
                "message": "Streaming not yet supported by GP adapter"
            }
        });
        return (StatusCode::BAD_REQUEST, Json(err)).into_response();
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
