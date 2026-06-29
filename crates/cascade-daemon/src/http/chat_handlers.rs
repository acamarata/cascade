//! Purpose: POST /api/chat SSE handler — routes streaming chat requests through
//!   the ProviderRegistry instead of a hardcoded Gemini proxy URL.
//!
//! # Selection order (E-P7-07)
//!
//!   1. `provider` field in the request body — explicit user selection.
//!   2. `ProviderRegistry::default_for_task(Chat)` — routing-table default.
//!   3. First healthy cloud provider (health-checked concurrently).
//!   4. First registered local:<id> provider — offline fallback.
//!   5. Typed SSE error event — nothing available.
//!
//! # Outputs (text/event-stream)
//!
//!   - `event: served_by` — which provider backend served the request.
//!   - `event: token`     — streamed text fragment `{"text":"…"}`.
//!   - `event: tool_result` — tool execution result (T-P3-E02-19 seam).
//!   - `event: error`    — provider or routing error (not HTTP 500).
//!   - `data: [DONE]`    — final sentinel (no event type).
//!
//! # Constraints
//!
//!   - Never exposes vault keys or API keys to the browser.
//!   - DashboardState.provider_registry == None → typed error SSE (no panic).
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
use cascade_providers::{CompletionRequest, Message, MessageRole};
use futures_util::StreamExt;
use serde::Deserialize;
use serde_json::{json, Value};
use std::convert::Infallible;
use std::sync::Arc;
use tokio_stream::wrappers::ReceiverStream;

use crate::dashboard::DashboardState;

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
    /// Optional explicit provider id (e.g. "gemini", "anthropic", "local:ollama").
    /// When present and registered, this provider is used directly without
    /// consulting the routing table.
    pub provider: Option<String>,
    /// Optional model override within the selected provider.
    pub model: Option<String>,
}

// ── Tool dispatch seam (implemented in T-P3-E02-19) ─────────────────────────

/// Dispatch a tool call by name with its arguments.
/// Delegates to the chat_tools catalog (T-P3-E02-19).  Returns a JSON Value
/// so the SSE handler can embed the result verbatim in a `tool_result` event.
// Tool dispatch seam — wired into chat_handler in T-P3-E02-19.
#[allow(dead_code)]
async fn dispatch_tool(name: &str, args: &Value) -> Value {
    let result = super::chat_tools::dispatch(name, args).await;
    serde_json::to_value(&result)
        .unwrap_or_else(|e| json!({ "error": format!("serialisation error: {e}"), "tool": name }))
}

// ── Router ───────────────────────────────────────────────────────────────────

/// Returns the router for /api/chat.
pub fn router() -> Router<DashboardState> {
    Router::new().route("/", post(chat_handler))
}

// ── Handler ───────────────────────────────────────────────────────────────────

pub async fn chat_handler(
    State(state): State<DashboardState>,
    Json(body): Json<ChatRequest>,
) -> impl IntoResponse {
    // Channel capacity: 256 buffered SSE events is sufficient for burst text.
    let (tx, rx) = tokio::sync::mpsc::channel::<Result<Event, Infallible>>(256);

    tokio::spawn(async move {
        // ── Step 1: resolve the provider registry ─────────────────────────────
        let registry = match state.provider_registry {
            Some(ref r) => Arc::clone(r),
            None => {
                let ev = Event::default()
                    .event("error")
                    .data(json!({ "message": "no provider registry configured" }).to_string());
                let _ = tx.send(Ok(ev)).await;
                let _ = tx.send(Ok(Event::default().data("[DONE]"))).await;
                return;
            }
        };

        // ── Step 2: pick provider via routing chain ───────────────────────────
        let preferred = body.provider.as_deref();
        let picked = registry.pick_for_chat(preferred).await;

        let (adapter, provider_id) = match picked {
            Some(pair) => pair,
            None => {
                let ev = Event::default()
                    .event("error")
                    .data(
                        json!({ "message": "no provider available — configure a provider or start a local model" })
                            .to_string(),
                    );
                let _ = tx.send(Ok(ev)).await;
                let _ = tx.send(Ok(Event::default().data("[DONE]"))).await;
                return;
            }
        };

        // ── Step 3: emit served_by event so the UI can display the backend ────
        let served_ev = Event::default()
            .event("served_by")
            .data(json!({ "provider": provider_id }).to_string());
        let _ = tx.send(Ok(served_ev)).await;

        // ── Step 4: build CompletionRequest from the chat body ────────────────
        let model = body
            .model
            .clone()
            .unwrap_or_else(|| default_model_for(&provider_id));

        let messages: Vec<Message> = body
            .messages
            .iter()
            .map(|m| {
                let role = match m.role.as_str() {
                    "assistant" => MessageRole::Assistant,
                    "system" => MessageRole::System,
                    _ => MessageRole::User,
                };
                Message {
                    role,
                    content: m.content.clone(),
                }
            })
            .collect();

        let completion_req = CompletionRequest {
            model,
            messages,
            max_tokens: None,
            temperature: None,
            stream: true,
            system: None,
        };

        // ── Step 5: stream from the adapter ──────────────────────────────────
        let mut stream = match adapter.complete_stream(completion_req).await {
            Ok(s) => s,
            Err(e) => {
                let ev = Event::default()
                    .event("error")
                    .data(json!({ "message": format!("provider error: {e}") }).to_string());
                let _ = tx.send(Ok(ev)).await;
                let _ = tx.send(Ok(Event::default().data("[DONE]"))).await;
                return;
            }
        };

        while let Some(chunk_result) = stream.next().await {
            match chunk_result {
                Err(e) => {
                    let ev = Event::default()
                        .event("error")
                        .data(json!({ "message": format!("stream error: {e}") }).to_string());
                    let _ = tx.send(Ok(ev)).await;
                    break;
                }
                Ok(chunk) => {
                    if !chunk.delta.is_empty() {
                        let ev = Event::default()
                            .event("token")
                            .data(json!({ "text": chunk.delta }).to_string());
                        let _ = tx.send(Ok(ev)).await;
                    }
                    if chunk.finish_reason.is_some() {
                        break;
                    }
                }
            }
        }

        // Final sentinel — always emitted.
        let _ = tx.send(Ok(Event::default().data("[DONE]"))).await;
    });

    let sse_stream = ReceiverStream::new(rx);
    Sse::new(sse_stream).keep_alive(KeepAlive::default())
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Return a sensible default model string for the given provider id.
///
/// This is a best-effort hint used when no model is specified in an incoming
/// chat request; the provider adapter may override it. These IDs are provider
/// API defaults, NOT fleet routing IDs — they intentionally differ from the
/// fleet model matrix in `cascade_core::model_ids` (different purpose: API
/// negotiation vs. agent harness generation).
fn default_model_for(provider_id: &str) -> String {
    match provider_id {
        "gemini" => "gemini-2.0-flash".into(),
        "anthropic" => "claude-3-5-haiku-20241022".into(),
        "openai" => "gpt-4o-mini".into(),
        _ if provider_id.starts_with("local") => "default".into(),
        _ => "default".into(),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use async_trait::async_trait;
    use axum::{
        body::{to_bytes, Body},
        http::{Request, StatusCode},
    };
    use cascade_providers::{
        AuthMethod, CompletionResponse, ModelInfo, NoopProvider, ProviderAdapter,
        ProviderCapabilities, ProviderError, ProviderInfo, ProviderRegistry, StreamChunk,
        TokenUsage,
    };
    use futures_util::stream::Stream;
    use std::pin::Pin;
    use tower::ServiceExt;

    // ── Mock adapters ─────────────────────────────────────────────────────────

    /// A mock adapter that streams a fixed set of chunks then finishes.
    struct StreamingMockProvider {
        id: &'static str,
        chunks: Vec<&'static str>,
        /// If true, health_check returns Ok; otherwise Err.
        healthy: bool,
    }

    impl StreamingMockProvider {
        fn new(id: &'static str, chunks: Vec<&'static str>, healthy: bool) -> Self {
            Self {
                id,
                chunks,
                healthy,
            }
        }
    }

    #[async_trait]
    impl ProviderAdapter for StreamingMockProvider {
        async fn complete(
            &self,
            _req: CompletionRequest,
        ) -> Result<CompletionResponse, ProviderError> {
            Ok(CompletionResponse {
                content: self.chunks.join(""),
                model: self.id.into(),
                usage: TokenUsage::default(),
                cost_usd: None,
            })
        }

        async fn complete_stream(
            &self,
            _req: CompletionRequest,
        ) -> Result<
            Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
            ProviderError,
        > {
            let chunks: Vec<Result<StreamChunk, ProviderError>> = self
                .chunks
                .iter()
                .enumerate()
                .map(|(i, &text)| {
                    Ok(StreamChunk {
                        delta: text.to_string(),
                        finish_reason: if i == self.chunks.len() - 1 {
                            Some("stop".into())
                        } else {
                            None
                        },
                    })
                })
                .collect();
            // SAFETY: `futures_util::stream::iter` returns a `Send` stream.
            let s = futures_util::stream::iter(chunks);
            Ok(Box::pin(s))
        }

        async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
            Ok(vec![ModelInfo {
                id: self.id.into(),
                name: self.id.into(),
                context_window: 4096,
                supports_streaming: true,
            }])
        }

        async fn health_check(&self) -> Result<(), ProviderError> {
            if self.healthy {
                Ok(())
            } else {
                Err(ProviderError::NetworkError("mock unhealthy".into()))
            }
        }

        fn provider_info(&self) -> ProviderInfo {
            ProviderInfo {
                id: self.id.into(),
                name: format!("Mock({})", self.id),
                auth_method: AuthMethod::None,
                base_url: String::new(),
                capabilities: ProviderCapabilities {
                    supports_streaming: true,
                    supports_vision: false,
                    max_context_tokens: 4096,
                    supports_function_calling: false,
                },
            }
        }
    }

    // ── State builders ────────────────────────────────────────────────────────

    fn state_no_registry() -> DashboardState {
        DashboardState {
            token: Arc::new("test-token".into()),
            provider_registry: None,
            routing_ring: None,
        }
    }

    fn state_with_registry(registry: Arc<ProviderRegistry>) -> DashboardState {
        DashboardState {
            token: Arc::new("test-token".into()),
            provider_registry: Some(registry),
            routing_ring: None,
        }
    }

    fn make_app(state: DashboardState) -> axum::Router {
        router().with_state(state)
    }

    fn chat_body(provider: Option<&str>) -> String {
        let mut obj = serde_json::json!({
            "messages": [{ "role": "user", "content": "hello" }]
        });
        if let Some(p) = provider {
            obj["provider"] = serde_json::Value::String(p.into());
        }
        obj.to_string()
    }

    async fn collect_sse(body: axum::body::Body) -> String {
        let bytes = to_bytes(body, 65536).await.unwrap();
        String::from_utf8_lossy(&bytes).to_string()
    }

    // ── Test 1: no registry → typed error event ───────────────────────────────

    #[tokio::test]
    async fn no_registry_yields_error_event() {
        let app = make_app(state_no_registry());
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = collect_sse(resp.into_body()).await;
        assert!(body.contains("event: error"), "body: {body}");
        assert!(body.contains("no provider registry"), "body: {body}");
        assert!(body.contains("[DONE]"), "body: {body}");
    }

    // ── Test 2: no provider available → typed error event ─────────────────────

    #[tokio::test]
    async fn no_provider_available_yields_error_event() {
        let registry = Arc::new(ProviderRegistry::new());
        // Register a noop that never matches (not in routing table)
        // by using an id not in the default Chat priority list.
        registry
            .register("noop-x".into(), Arc::new(NoopProvider))
            .unwrap();

        let app = make_app(state_with_registry(registry));
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = collect_sse(resp.into_body()).await;
        assert!(body.contains("event: error"), "body: {body}");
        assert!(body.contains("no provider available"), "body: {body}");
    }

    // ── Test 3: routing-table default → streams tokens ────────────────────────

    #[tokio::test]
    async fn routing_table_default_streams_tokens() {
        let registry = Arc::new(ProviderRegistry::new());
        // "openai" is the first in the Chat priority list.
        registry
            .register(
                "openai".into(),
                Arc::new(StreamingMockProvider::new(
                    "openai",
                    vec!["hello", " world"],
                    true,
                )),
            )
            .unwrap();

        let app = make_app(state_with_registry(registry));
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = collect_sse(resp.into_body()).await;

        // served_by event
        assert!(body.contains("event: served_by"), "body: {body}");
        assert!(body.contains("\"openai\""), "body: {body}");
        // token events
        assert!(body.contains("event: token"), "body: {body}");
        assert!(body.contains("hello"), "body: {body}");
        assert!(body.contains("[DONE]"), "body: {body}");
    }

    // ── Test 4: user-selected provider wins over default ──────────────────────

    #[tokio::test]
    async fn user_selected_provider_wins_over_routing_default() {
        let registry = Arc::new(ProviderRegistry::new());
        // "openai" is routing default; register "anthropic" as explicit choice.
        registry
            .register(
                "openai".into(),
                Arc::new(StreamingMockProvider::new(
                    "openai",
                    vec!["from-openai"],
                    true,
                )),
            )
            .unwrap();
        registry
            .register(
                "anthropic".into(),
                Arc::new(StreamingMockProvider::new(
                    "anthropic",
                    vec!["from-anthropic"],
                    true,
                )),
            )
            .unwrap();

        let app = make_app(state_with_registry(registry));
        // Explicitly request "anthropic"
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(Some("anthropic"))))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        let body = collect_sse(resp.into_body()).await;

        assert!(
            body.contains("\"anthropic\""),
            "served_by must say anthropic: {body}"
        );
        assert!(
            body.contains("from-anthropic"),
            "token must come from anthropic: {body}"
        );
        assert!(!body.contains("from-openai"), "must not use openai: {body}");
    }

    // ── Test 5: local fallback when no cloud provider is healthy ──────────────

    #[tokio::test]
    async fn local_fallback_when_no_cloud_healthy() {
        let registry = Arc::new(ProviderRegistry::new());
        // Cloud provider is unhealthy and not in the routing table.
        registry
            .register(
                "cloud-broken".into(),
                Arc::new(StreamingMockProvider::new("cloud-broken", vec![], false)),
            )
            .unwrap();
        // Local provider — id starts with "local".
        registry
            .register(
                "local:ollama".into(),
                Arc::new(StreamingMockProvider::new(
                    "local:ollama",
                    vec!["local-response"],
                    false, // local models don't need health_check
                )),
            )
            .unwrap();

        let app = make_app(state_with_registry(registry));
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = collect_sse(resp.into_body()).await;

        // Local provider served the request
        assert!(body.contains("\"local:ollama\""), "body: {body}");
        assert!(body.contains("local-response"), "body: {body}");
    }

    // ── Test 6: served_by provider id is reported ─────────────────────────────

    #[tokio::test]
    async fn served_by_is_reported_in_sse() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "gemini".into(),
                Arc::new(StreamingMockProvider::new("gemini", vec!["tok"], true)),
            )
            .unwrap();

        let app = make_app(state_with_registry(registry));
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(Some("gemini"))))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        let body = collect_sse(resp.into_body()).await;

        // The served_by event appears before token events.
        let served_pos = body.find("event: served_by").unwrap_or(usize::MAX);
        let token_pos = body.find("event: token").unwrap_or(0);
        assert!(
            served_pos < token_pos,
            "served_by must precede first token: {body}"
        );
        assert!(body.contains("\"gemini\""), "body: {body}");
    }

    // ── Test 7: SSE response always has text/event-stream content type ─────────

    #[tokio::test]
    async fn response_is_sse_content_type() {
        let app = make_app(state_no_registry());
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let ct = resp
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        assert!(ct.starts_with("text/event-stream"), "got: {ct}");
    }

    // ── Test 8: invalid JSON → 400 ────────────────────────────────────────────

    #[tokio::test]
    async fn invalid_json_returns_4xx() {
        let app = make_app(state_no_registry());
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from("not json"))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert!(
            resp.status() == StatusCode::BAD_REQUEST
                || resp.status() == StatusCode::UNPROCESSABLE_ENTITY,
            "expected 400 or 422, got {}",
            resp.status()
        );
    }

    // ── Test 9: pick_for_chat routing order verified directly ─────────────────

    #[tokio::test]
    async fn pick_for_chat_prefers_explicit_over_routing_default() {
        let registry = ProviderRegistry::new();
        registry
            .register(
                "openai".into(), // routing default for Chat
                Arc::new(StreamingMockProvider::new("openai", vec![], true)),
            )
            .unwrap();
        registry
            .register(
                "mymodel".into(),
                Arc::new(StreamingMockProvider::new("mymodel", vec![], true)),
            )
            .unwrap();

        let (_, id) = registry
            .pick_for_chat(Some("mymodel"))
            .await
            .expect("should pick mymodel");
        assert_eq!(id, "mymodel");
    }

    #[tokio::test]
    async fn pick_for_chat_routing_default_when_no_preferred() {
        let registry = ProviderRegistry::new();
        // Only "anthropic" registered — priority 2 in Chat routing.
        registry
            .register(
                "anthropic".into(),
                Arc::new(StreamingMockProvider::new("anthropic", vec![], true)),
            )
            .unwrap();

        let (_, id) = registry
            .pick_for_chat(None)
            .await
            .expect("should pick anthropic");
        assert_eq!(id, "anthropic");
    }

    #[tokio::test]
    async fn pick_for_chat_local_fallback_when_no_cloud() {
        let registry = ProviderRegistry::new();
        // No cloud providers in routing list; one local.
        registry
            .register(
                "local:test".into(),
                Arc::new(StreamingMockProvider::new("local:test", vec![], false)),
            )
            .unwrap();

        let (_, id) = registry
            .pick_for_chat(None)
            .await
            .expect("should pick local fallback");
        assert_eq!(id, "local:test");
    }

    #[tokio::test]
    async fn pick_for_chat_none_when_nothing_registered() {
        let registry = ProviderRegistry::new();
        let result = registry.pick_for_chat(None).await;
        assert!(result.is_none(), "empty registry must return None");
    }
}
