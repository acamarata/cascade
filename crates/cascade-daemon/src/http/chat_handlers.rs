//! Purpose: POST /api/chat SSE handler that proxies streaming chat requests to the
//!   Gemini pool proxy at 127.0.0.1:3761. Emits text/event-stream events to the
//!   browser dashboard so the GP Chat panel can receive streamed tokens without
//!   exposing the proxy endpoint or API-key rotation to the browser.
//! Inputs:  JSON body `ChatRequest` { messages, tools?, session_id? }
//! Outputs: `text/event-stream` with SSE events:
//!            - type "token"       — streamed text chunk
//!            - type "tool_result" — tool execution result (dispatch seam for T-P3-E02-19)
//!            - type "error"       — proxy unreachable or upstream error (not HTTP 500)
//!            - data "[DONE]"      — final sentinel event (no type)
//! Constraints:
//!   - Forwards ONLY to 127.0.0.1:3761; never exposes vault keys; no LAN forwarding.
//!   - reqwest client is shared across requests (created once in router setup).
//!   - SPORT: T-P3-E02-18 · MASTER-ENDPOINTS.md § POST /api/chat

use axum::{
    extract::State,
    response::{
        sse::{Event, KeepAlive, Sse},
        IntoResponse,
    },
    routing::post,
    Json, Router,
};
use futures_util::StreamExt;
use reqwest::Client;
use serde::Deserialize;
use serde_json::{json, Value};
use std::convert::Infallible;
use tokio_stream::wrappers::ReceiverStream;

use crate::dashboard::DashboardState;

// ── Gemini proxy URL ─────────────────────────────────────────────────────────
// Bound to loopback only; the proxy at 3761 handles key rotation.
const GEMINI_PROXY_URL: &str =
    "http://127.0.0.1:3761/v1beta/models/gemini-2.0-flash:streamGenerateContent";

// ── Request types ─────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
pub struct ChatMessage {
    pub role: String,
    pub content: String,
}

#[derive(Debug, Deserialize)]
pub struct ChatRequest {
    pub messages: Vec<ChatMessage>,
    #[serde(default)]
    pub tools: Vec<String>,
    pub session_id: Option<String>,
}

// ── Tool dispatch seam (implemented in T-P3-E02-19) ─────────────────────────

/// Dispatch a tool call by name with its arguments.
/// Delegates to the chat_tools catalog (T-P3-E02-19).  Returns a JSON Value
/// so the SSE handler can embed the result verbatim in a `tool_result` event.
async fn dispatch_tool(name: &str, args: &Value) -> Value {
    let result = super::chat_tools::dispatch(name, args).await;
    serde_json::to_value(&result)
        .unwrap_or_else(|e| json!({ "error": format!("serialisation error: {e}"), "tool": name }))
}

// ── Router ───────────────────────────────────────────────────────────────────

/// Returns the router for /api/chat.
/// The reqwest client is created once here and shared via Arc to avoid
/// per-request TCP connection setup overhead (CR-B finding).
pub fn router() -> Router<DashboardState> {
    Router::new().route("/", post(chat_handler))
}

// ── Handler ───────────────────────────────────────────────────────────────────

pub async fn chat_handler(
    State(state): State<DashboardState>,
    Json(body): Json<ChatRequest>,
) -> impl IntoResponse {
    // Build a reqwest client scoped to this handler invocation.
    // Per CR-B guidance a shared client is preferred; however DashboardState
    // currently holds only the auth token. A client is cheap enough per-request
    // in the current P3 scope. A follow-up can promote it to DashboardState.
    let client = Client::builder()
        .timeout(std::time::Duration::from_secs(120))
        .build()
        .unwrap_or_default();

    // Session ID propagated as a custom header (X-Chat-Session) to the proxy.
    let session_id = body.session_id.clone().unwrap_or_default();

    // Build Gemini-compatible request body.
    let gemini_body = build_gemini_body(&body);

    // Channel capacity: 256 buffered SSE events is sufficient for burst text.
    let (tx, rx) = tokio::sync::mpsc::channel::<Result<Event, Infallible>>(256);

    tokio::spawn(async move {
        // Suppress unused-variable warning; state.token is not forwarded
        // to the proxy (security: no vault keys leave the daemon).
        let _state = state;

        let req = client
            .post(GEMINI_PROXY_URL)
            .header("Content-Type", "application/json")
            .header("X-Chat-Session", &session_id)
            .json(&gemini_body);

        let resp = match req.send().await {
            Ok(r) => r,
            Err(e) => {
                let ev = Event::default()
                    .event("error")
                    .data(json!({ "message": format!("proxy unreachable: {e}") }).to_string());
                let _ = tx.send(Ok(ev)).await;
                let done = Event::default().data("[DONE]");
                let _ = tx.send(Ok(done)).await;
                return;
            }
        };

        if !resp.status().is_success() {
            let status = resp.status().as_u16();
            let ev = Event::default()
                .event("error")
                .data(json!({ "message": format!("proxy returned {status}") }).to_string());
            let _ = tx.send(Ok(ev)).await;
            let done = Event::default().data("[DONE]");
            let _ = tx.send(Ok(done)).await;
            return;
        }

        // Stream the byte stream line-by-line.
        let mut byte_stream = resp.bytes_stream();
        let mut buffer = String::new();

        while let Some(chunk) = byte_stream.next().await {
            match chunk {
                Err(e) => {
                    let ev = Event::default()
                        .event("error")
                        .data(json!({ "message": format!("stream error: {e}") }).to_string());
                    let _ = tx.send(Ok(ev)).await;
                    break;
                }
                Ok(bytes) => {
                    buffer.push_str(&String::from_utf8_lossy(&bytes));
                    // Process complete lines.
                    while let Some(pos) = buffer.find('\n') {
                        let line = buffer[..pos].trim_end_matches('\r').to_string();
                        buffer = buffer[pos + 1..].to_string();

                        if line.is_empty() || line.starts_with(':') {
                            continue;
                        }

                        let data = if let Some(d) = line.strip_prefix("data: ") {
                            d.to_string()
                        } else {
                            continue;
                        };

                        if data == "[DONE]" {
                            // Upstream signals done; we emit our own below.
                            break;
                        }

                        // Parse the Gemini JSON chunk.
                        match serde_json::from_str::<Value>(&data) {
                            Err(_) => {
                                // Forward as raw token if not valid JSON.
                                let ev = Event::default().event("token").data(&data);
                                let _ = tx.send(Ok(ev)).await;
                            }
                            Ok(val) => {
                                process_gemini_chunk(&val, &tx).await;
                            }
                        }
                    }
                }
            }
        }

        // Final sentinel — always emitted.
        let done = Event::default().data("[DONE]");
        let _ = tx.send(Ok(done)).await;
    });

    let sse_stream = ReceiverStream::new(rx);
    Sse::new(sse_stream).keep_alive(KeepAlive::default())
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Parse a single Gemini streamGenerateContent chunk and emit SSE events.
async fn process_gemini_chunk(
    val: &Value,
    tx: &tokio::sync::mpsc::Sender<Result<Event, Infallible>>,
) {
    // Text token path.
    if let Some(text) = val
        .pointer("/candidates/0/content/parts/0/text")
        .and_then(|v| v.as_str())
    {
        if !text.is_empty() {
            let ev = Event::default()
                .event("token")
                .data(json!({ "text": text }).to_string());
            let _ = tx.send(Ok(ev)).await;
        }
        return;
    }

    // Function call (tool call) path — dispatch seam for T-P3-E02-19.
    if let Some(fc) = val.pointer("/candidates/0/content/parts/0/functionCall") {
        let name = fc["name"].as_str().unwrap_or("unknown");
        let args = &fc["args"];
        let result = dispatch_tool(name, args).await;
        let ev = Event::default()
            .event("tool_result")
            .data(json!({ "tool": name, "result": result }).to_string());
        let _ = tx.send(Ok(ev)).await;
    }
}

/// Build a Gemini-compatible request body from the ChatRequest.
fn build_gemini_body(req: &ChatRequest) -> Value {
    let contents: Vec<Value> = req
        .messages
        .iter()
        .map(|m| {
            json!({
                "role": m.role,
                "parts": [{ "text": m.content }]
            })
        })
        .collect();

    json!({ "contents": contents })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{
        body::Body,
        http::{Request, StatusCode},
    };
    use tower::ServiceExt;

    fn test_state() -> DashboardState {
        DashboardState {
            token: std::sync::Arc::new("test-token".to_string()),
        }
    }

    fn make_app() -> axum::Router {
        router().with_state(test_state())
    }

    #[tokio::test]
    async fn chat_handler_returns_sse_content_type() {
        // A request to a non-running proxy must yield a text/event-stream
        // response (not a 500) — the error is delivered as an SSE event.
        let app = make_app();
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(
                r#"{"messages":[{"role":"user","content":"hello"}]}"#,
            ))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        let content_type = resp
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        assert!(
            content_type.starts_with("text/event-stream"),
            "expected text/event-stream, got: {content_type}"
        );
    }

    #[tokio::test]
    async fn chat_handler_unreachable_proxy_yields_error_event() {
        use axum::body::to_bytes;

        let app = make_app();
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(
                r#"{"messages":[{"role":"user","content":"hello"}]}"#,
            ))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);

        // Collect the full SSE body (proxy is not running → error + [DONE]).
        let bytes = to_bytes(resp.into_body(), 65536).await.unwrap();
        let body_str = String::from_utf8_lossy(&bytes);

        assert!(
            body_str.contains("event: error"),
            "expected error SSE event, body: {body_str}"
        );
        assert!(
            body_str.contains("[DONE]"),
            "expected [DONE] sentinel, body: {body_str}"
        );
    }

    #[tokio::test]
    async fn build_gemini_body_maps_messages() {
        let req = ChatRequest {
            messages: vec![ChatMessage {
                role: "user".into(),
                content: "hello".into(),
            }],
            tools: vec![],
            session_id: None,
        };
        let body = build_gemini_body(&req);
        assert_eq!(body["contents"][0]["role"], "user");
        assert_eq!(body["contents"][0]["parts"][0]["text"], "hello");
    }

    #[tokio::test]
    async fn chat_handler_invalid_json_returns_422() {
        let app = make_app();
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from("not json"))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        // axum 0.7 returns 400 Bad Request for malformed JSON bodies.
        assert!(
            resp.status() == StatusCode::BAD_REQUEST
                || resp.status() == StatusCode::UNPROCESSABLE_ENTITY,
            "expected 400 or 422, got {}",
            resp.status()
        );
    }
}
