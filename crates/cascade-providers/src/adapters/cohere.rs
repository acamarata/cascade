//! CohereAdapter — api.cohere.com/v2/chat (Cohere native format).
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for Cohere using the native v2 Chat endpoint.
//! The Cohere API is NOT OpenAI-compatible: it uses a different request shape
//! (`messages[]` array with `{role, content}`) and a different response shape
//! (`text` field at the top level, usage under `meta.tokens`).
//!
//! # Request mapping
//!
//! `CompletionRequest` → Cohere v2 request:
//! - `model` → `model`
//! - `messages` → `messages[]` with each `{role, content}` directly
//! - `system` → prepended as `{role: "system", content: <value>}` in messages
//! - `stream` → `stream`
//! - `max_tokens` → `max_tokens`
//! - `temperature` → `temperature`
//!
//! # Response mapping
//!
//! Cohere non-streaming response:
//! ```json
//! {
//!   "id": "...",
//!   "message": {"role": "assistant", "content": [{"type": "text", "text": "..."}]},
//!   "finish_reason": "COMPLETE",
//!   "usage": {"billed_units": {...}, "tokens": {"input_tokens": 14, "output_tokens": 9}},
//!   "model": "command-r-plus-08-2024"
//! }
//! ```
//!
//! # Streaming
//!
//! Cohere SSE events:
//! - `event:stream-start` → metadata (no text)
//! - `event:text-generation` `data:{"text":"..."}` → delta content
//! - `event:stream-end` → terminal event; `data.finish_reason` and usage
//!
//! # Constraints
//!
//! - API key from keychain service `"cascade.cohere"`.
//! - No credential value in logs or error messages.

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;
use serde::{Deserialize, Serialize};
use tracing::debug;

use crate::{
    adapter::ProviderAdapter,
    cost::compute_cost,
    error::ProviderError,
    http_client::CascadeHttpClient,
    provider_info::{AuthMethod, ProviderCapabilities, ProviderInfo},
    types::{CompletionRequest, CompletionResponse, MessageRole, ModelInfo, StreamChunk, TokenUsage},
};

// ── Constants ─────────────────────────────────────────────────────────────────

const COHERE_BASE_URL: &str = "https://api.cohere.com";
const KEYCHAIN_SERVICE: &str = "cascade.cohere";
const PROVIDER_ID: &str = "cohere";

// ── Wire types — Cohere v2 Chat request ───────────────────────────────────────

/// Cohere v2 Chat request body.
#[derive(Debug, Serialize)]
struct CohereRequest {
    model: String,
    messages: Vec<CohereMessage>,
    #[serde(skip_serializing_if = "Option::is_none")]
    max_tokens: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    temperature: Option<f32>,
    stream: bool,
}

/// A single message in the Cohere v2 messages array.
#[derive(Debug, Serialize, Deserialize)]
struct CohereMessage {
    role: String,
    content: String,
}

// ── Wire types — Cohere v2 Chat non-streaming response ────────────────────────

/// Top-level non-streaming Cohere v2 chat response.
#[derive(Debug, Deserialize)]
struct CohereResponse {
    #[serde(default)]
    model: Option<String>,
    message: Option<CohereResponseMessage>,
    #[serde(default)]
    finish_reason: Option<String>,
    usage: Option<CohereUsage>,
}

#[derive(Debug, Deserialize)]
struct CohereResponseMessage {
    #[serde(default)]
    content: Vec<CohereContent>,
}

#[derive(Debug, Deserialize)]
struct CohereContent {
    #[serde(rename = "type")]
    content_type: String,
    text: Option<String>,
}

#[derive(Debug, Deserialize)]
struct CohereUsage {
    tokens: Option<CohereTokenUsage>,
}

#[derive(Debug, Deserialize)]
struct CohereTokenUsage {
    input_tokens: Option<u32>,
    output_tokens: Option<u32>,
}

// ── Wire types — Cohere SSE streaming ─────────────────────────────────────────

/// Parsed from `data:` payload on `event:text-generation` lines.
#[derive(Debug, Deserialize)]
struct CohereStreamTextEvent {
    text: Option<String>,
}

/// Parsed from `data:` payload on `event:stream-end` lines.
#[derive(Debug, Deserialize)]
struct CohereStreamEndEvent {
    finish_reason: Option<String>,
}

// ── CohereAdapter ─────────────────────────────────────────────────────────────

/// Provider adapter for Cohere (api.cohere.com/v2/chat).
///
/// ## Authentication
///
/// Reads the API key from the keychain service `"cascade.cohere"`.
/// The key is fetched on every call — rotation takes effect immediately.
///
/// ## Models
///
/// | Model | Notes |
/// |---|---|
/// | `command-r-plus-08-2024` | Flagship RAG + tool use |
/// | `command-r-08-2024` | Fast, cost-efficient |
pub struct CohereAdapter {
    http: CascadeHttpClient,
    base_url: String,
    api_key: Option<String>,
}

impl CohereAdapter {
    /// Construct a production adapter pointing at the real Cohere API.
    pub fn new() -> Self {
        Self {
            http: CascadeHttpClient::new(),
            base_url: COHERE_BASE_URL.to_owned(),
            api_key: None,
        }
    }

    /// Construct a test adapter with an injected base URL and key.
    ///
    /// Gated behind `test` or `integration_tests` — not compiled in production.
    #[cfg(any(test, feature = "integration_tests"))]
    pub fn with_base_url(base_url: impl Into<String>, api_key: impl Into<String>) -> Self {
        Self {
            http: CascadeHttpClient::new(),
            base_url: base_url.into(),
            api_key: Some(api_key.into()),
        }
    }

    fn api_key(&self) -> Result<String, ProviderError> {
        if let Some(ref key) = self.api_key {
            return Ok(key.clone());
        }
        cascade_keychain::platform_keychain()
            .get_key(KEYCHAIN_SERVICE, PROVIDER_ID)
            .map_err(|_| {
                ProviderError::CredentialNotFound(format!(
                    "no Cohere API key in keychain service '{KEYCHAIN_SERVICE}'"
                ))
            })
    }

    /// Map a `CompletionRequest` to a Cohere v2 request body.
    ///
    /// System prompts are prepended as a `{role: "system", content: ...}` message,
    /// which Cohere v2 supports directly in the messages array.
    fn build_request(req: &CompletionRequest, stream: bool) -> CohereRequest {
        let mut messages: Vec<CohereMessage> = Vec::new();

        if let Some(ref sys) = req.system {
            messages.push(CohereMessage {
                role: "system".to_owned(),
                content: sys.clone(),
            });
        }

        for msg in &req.messages {
            let role = match msg.role {
                MessageRole::System => "system",
                MessageRole::User => "user",
                MessageRole::Assistant => "assistant",
            };
            messages.push(CohereMessage {
                role: role.to_owned(),
                content: msg.content.clone(),
            });
        }

        CohereRequest {
            model: req.model.clone(),
            messages,
            max_tokens: req.max_tokens,
            temperature: req.temperature,
            stream,
        }
    }

    /// Chat endpoint URL.
    fn chat_url(&self) -> String {
        format!("{}/v2/chat", self.base_url)
    }

    /// Extract text from a Cohere v2 response message content array.
    ///
    /// Cohere returns content as `[{"type": "text", "text": "..."}]`.
    /// We concatenate all text-type blocks.
    fn extract_text(msg: &CohereResponseMessage) -> String {
        msg.content
            .iter()
            .filter(|c| c.content_type == "text")
            .filter_map(|c| c.text.as_deref())
            .collect::<Vec<_>>()
            .join("")
    }

    /// Parse Cohere SSE lines from a raw SSE body string.
    ///
    /// Cohere uses named events (`event:text-generation`, `event:stream-end`)
    /// rather than the OpenAI `data: [DONE]` sentinel. We scan for event
    /// name + data pairs.
    ///
    /// Returns a list of `StreamChunk` values in order.
    fn parse_cohere_sse(body: &str) -> Vec<StreamChunk> {
        let mut chunks: Vec<StreamChunk> = Vec::new();
        let mut current_event: Option<&str> = None;

        for line in body.lines() {
            let line = line.trim_end_matches(['\r', '\n']);

            if line.is_empty() {
                current_event = None;
                continue;
            }

            if let Some(event_name) = line.strip_prefix("event:") {
                current_event = Some(event_name.trim());
                continue;
            }

            if let Some(payload) = line.strip_prefix("data:") {
                let payload = payload.trim();

                match current_event {
                    Some("text-generation") => {
                        if let Ok(ev) = serde_json::from_str::<CohereStreamTextEvent>(payload) {
                            if let Some(text) = ev.text {
                                if !text.is_empty() {
                                    chunks.push(StreamChunk {
                                        delta: text,
                                        finish_reason: None,
                                    });
                                }
                            }
                        }
                    }
                    Some("stream-end") => {
                        let finish_reason = serde_json::from_str::<CohereStreamEndEvent>(payload)
                            .ok()
                            .and_then(|ev| ev.finish_reason);
                        // Push a terminal chunk with finish_reason set.
                        chunks.push(StreamChunk {
                            delta: String::new(),
                            finish_reason: finish_reason.or_else(|| Some("stop".to_owned())),
                        });
                    }
                    _ => {}
                }
            }
        }

        chunks
    }
}

impl Default for CohereAdapter {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl ProviderAdapter for CohereAdapter {
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        let key = self.api_key()?;
        let body = Self::build_request(&req, false);
        let url = self.chat_url();

        debug!(provider = PROVIDER_ID, model = %req.model, "sending completion request");

        let resp: CohereResponse = self
            .http
            .post_json(&url, &body, move |b| {
                CascadeHttpClient::apply_bearer(b, &key)
            })
            .await?;

        // Extract text from the message content array.
        let content = resp
            .message
            .as_ref()
            .map(Self::extract_text)
            .unwrap_or_default();

        let model = resp.model.unwrap_or_else(|| req.model.clone());

        let (prompt_tokens, completion_tokens) = resp
            .usage
            .and_then(|u| u.tokens)
            .map(|t| (t.input_tokens.unwrap_or(0), t.output_tokens.unwrap_or(0)))
            .unwrap_or((0, 0));

        let usage = TokenUsage {
            prompt_tokens,
            completion_tokens,
            total_tokens: prompt_tokens + completion_tokens,
        };
        let cost_usd = compute_cost("cohere", &model, &usage);
        Ok(CompletionResponse {
            content,
            model,
            usage,
            cost_usd,
        })
    }

    async fn complete_stream(
        &self,
        req: CompletionRequest,
    ) -> Result<
        Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
        ProviderError,
    > {
        let key = self.api_key()?;
        let body = Self::build_request(&req, true);
        let url = self.chat_url();

        let response = self
            .http
            .post_sse(&url, &body, move |b| {
                CascadeHttpClient::apply_bearer(b, &key)
            })
            .await?;

        let bytes = response
            .bytes()
            .await
            .map_err(|e| ProviderError::NetworkError(e.to_string()))?;

        let text = String::from_utf8_lossy(&bytes).into_owned();
        let chunks = Self::parse_cohere_sse(&text);

        Ok(Box::pin(futures::stream::iter(
            chunks.into_iter().map(Ok).collect::<Vec<_>>(),
        )))
    }

    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![
            ModelInfo {
                id: "command-r-plus-08-2024".into(),
                name: "Command R+ (Aug 2024)".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "command-r-08-2024".into(),
                name: "Command R (Aug 2024)".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
        ])
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        let req = CompletionRequest::simple("command-r-08-2024", "ping");
        match self.complete(req).await {
            Ok(_) => Ok(()),
            Err(ProviderError::AuthFailed(msg)) => Err(ProviderError::AuthFailed(msg)),
            Err(ProviderError::CredentialNotFound(msg)) => {
                Err(ProviderError::CredentialNotFound(msg))
            }
            Err(_) => Ok(()),
        }
    }

    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: PROVIDER_ID.into(),
            name: "Cohere".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: COHERE_BASE_URL.into(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: false,
                max_context_tokens: 128_000,
                supports_function_calling: true,
            },
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::test_support::{fixture_json, HttpMethod, MockProviderServer};

    /// Happy-path: Cohere v2 native response shape is mapped to CompletionResponse.
    #[tokio::test]
    async fn cohere_complete_happy_path() {
        let ctx = MockProviderServer::start("cohere").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v2/chat",
            200,
            fixture_json("cohere_complete"),
        )
        .await;

        let adapter = CohereAdapter::with_base_url(ctx.base_url(), "test-key");
        let req = CompletionRequest::simple("command-r-plus-08-2024", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("should succeed");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert_eq!(
            resp.content, "The capital of France is Paris.",
            "content must match fixture"
        );
        assert!(!resp.model.is_empty(), "model must not be empty");
        assert!(
            resp.usage.prompt_tokens > 0,
            "prompt_tokens must be positive"
        );
    }

    /// Cohere shape mapping: the `messages[]` format is used correctly.
    #[tokio::test]
    async fn cohere_request_shape_uses_messages_array() {
        let req = CompletionRequest {
            model: "command-r-plus-08-2024".into(),
            messages: vec![
                crate::types::Message::user("What is 2+2?"),
            ],
            max_tokens: Some(100),
            temperature: Some(0.7),
            stream: false,
            system: Some("You are a helpful assistant.".into()),
        };

        let body = CohereAdapter::build_request(&req, false);

        // System prompt should be the first message.
        assert_eq!(body.messages[0].role, "system");
        assert_eq!(body.messages[0].content, "You are a helpful assistant.");
        // User message follows.
        assert_eq!(body.messages[1].role, "user");
        assert_eq!(body.messages[1].content, "What is 2+2?");
        assert_eq!(body.model, "command-r-plus-08-2024");
        assert_eq!(body.max_tokens, Some(100));
    }

    /// Auth error: 401 → ProviderError::AuthFailed.
    #[tokio::test]
    async fn cohere_complete_auth_error() {
        let ctx = MockProviderServer::start("cohere").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v2/chat",
            401,
            serde_json::json!({"message": "invalid api token"}),
        )
        .await;

        let adapter = CohereAdapter::with_base_url(ctx.base_url(), "bad-key");
        let req = CompletionRequest::simple("command-r-plus-08-2024", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    /// Cohere SSE stream-end event terminates the stream with a finish_reason.
    #[test]
    fn cohere_sse_stream_end_terminates() {
        let sse = concat!(
            "event:stream-start\n",
            "data:{\"is_finished\":false,\"event_type\":\"stream-start\"}\n\n",
            "event:text-generation\n",
            "data:{\"text\":\"The capital\"}\n\n",
            "event:text-generation\n",
            "data:{\"text\":\" of France is Paris.\"}\n\n",
            "event:stream-end\n",
            "data:{\"is_finished\":true,\"finish_reason\":\"COMPLETE\",\"event_type\":\"stream-end\"}\n\n",
        );

        let chunks = CohereAdapter::parse_cohere_sse(sse);

        // Should have two text chunks + one terminal chunk.
        assert_eq!(chunks.len(), 3, "expected 3 chunks, got {}", chunks.len());

        let text: String = chunks
            .iter()
            .filter(|c| c.finish_reason.is_none())
            .map(|c| c.delta.as_str())
            .collect();
        assert_eq!(text, "The capital of France is Paris.");

        let last = chunks.last().unwrap();
        assert!(
            last.finish_reason.is_some(),
            "last chunk must have a finish_reason"
        );
        assert_eq!(last.finish_reason.as_deref(), Some("COMPLETE"));
    }

    #[test]
    fn cohere_provider_info() {
        let adapter = CohereAdapter::new();
        let info = adapter.provider_info();
        assert_eq!(info.id, "cohere");
        assert!(info.capabilities.supports_streaming);
    }

    #[tokio::test]
    async fn cohere_available_models() {
        let adapter = CohereAdapter::new();
        let models = adapter.available_models().await.unwrap();
        assert_eq!(models.len(), 2);
        assert!(models.iter().any(|m| m.id == "command-r-plus-08-2024"));
        assert!(models.iter().any(|m| m.id == "command-r-08-2024"));
    }
}
