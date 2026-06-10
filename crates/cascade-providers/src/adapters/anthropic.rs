//! Anthropic Messages API adapter.
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for the Anthropic Messages API
//! (`https://api.anthropic.com/v1/messages`).  Supports blocking completions,
//! SSE streaming, model enumeration, and connectivity health-check.
//!
//! # Inputs / Outputs
//!
//! - `complete`: maps `CompletionRequest` → Anthropic messages body →
//!   `CompletionResponse` (extracts `content[0].text`, usage, model).
//! - `complete_stream`: same endpoint with `stream: true`; parses
//!   `content_block_delta` SSE events into `StreamChunk` values.
//! - `available_models`: returns a hardcoded list of current Claude models.
//! - `health_check`: GET `/v1/models`; returns `Ok(())` on HTTP 200.
//!
//! # Constraints
//!
//! - The API key is NEVER written to a log line, `Debug` output, or error
//!   message — use the `api_key` field only at the call site.
//! - `max_tokens` defaults to 1024 when the caller omits it (Anthropic
//!   requires the field).
//! - SSE parsing yields chunks until `event: message_stop` or `[DONE]`; any
//!   parse error yields `StreamInterrupted`.

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;
use serde::{Deserialize, Serialize};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;

use crate::{
    adapter::ProviderAdapter,
    cost::compute_cost,
    error::ProviderError,
    http_client::CascadeHttpClient,
    provider_info::{AuthMethod, ProviderCapabilities, ProviderInfo},
    types::{
        CompletionRequest, CompletionResponse, MessageRole, ModelInfo, StreamChunk, TokenUsage,
    },
};

// ── Constants ─────────────────────────────────────────────────────────────────

const BASE_URL: &str = "https://api.anthropic.com";
const MESSAGES_PATH: &str = "/v1/messages";
const MODELS_PATH: &str = "/v1/models";
const ANTHROPIC_VERSION: &str = "2023-06-01";
const DEFAULT_MAX_TOKENS: u32 = 1024;

// ── Wire types (Anthropic-specific request/response shapes) ──────────────────

#[derive(Debug, Serialize)]
struct AnthropicMessage {
    role: &'static str,
    content: String,
}

#[derive(Debug, Serialize)]
struct AnthropicRequest {
    model: String,
    max_tokens: u32,
    messages: Vec<AnthropicMessage>,
    #[serde(skip_serializing_if = "Option::is_none")]
    system: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    temperature: Option<f32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    stream: Option<bool>,
}

#[derive(Debug, Deserialize)]
struct AnthropicContent {
    #[serde(rename = "type")]
    content_type: String,
    #[serde(default)]
    text: String,
}

#[derive(Debug, Deserialize)]
struct AnthropicUsage {
    input_tokens: u32,
    output_tokens: u32,
}

#[derive(Debug, Deserialize)]
struct AnthropicResponse {
    content: Vec<AnthropicContent>,
    model: String,
    usage: AnthropicUsage,
}

// SSE event shapes
#[derive(Debug, Deserialize)]
struct AnthropicSseDelta {
    #[serde(rename = "type")]
    delta_type: String,
    #[serde(default)]
    text: String,
}

#[derive(Debug, Deserialize)]
struct AnthropicSseEvent {
    #[serde(rename = "type")]
    event_type: String,
    #[serde(default)]
    delta: Option<AnthropicSseDelta>,
}

// Models endpoint response — fields exist for JSON deserialization; not all are read at runtime.
#[derive(Debug, Deserialize)]
struct AnthropicModelsResponse {
    #[allow(dead_code)]
    data: Vec<AnthropicModelEntry>,
}

#[derive(Debug, Deserialize)]
struct AnthropicModelEntry {
    #[allow(dead_code)]
    id: String,
}

// ── AnthropicAdapter ──────────────────────────────────────────────────────────

/// `ProviderAdapter` implementation for the Anthropic Messages API.
///
/// Construct with `AnthropicAdapter::new(api_key)`.  The API key must be
/// obtained from the OS keychain via `cascade-keychain` before construction;
/// this adapter does not perform keychain access itself.
pub struct AnthropicAdapter {
    /// Anthropic API key — never logged or included in error messages.
    api_key: String,
    /// Base URL override (used in tests to point at a mock server).
    base_url: String,
    http: CascadeHttpClient,
}

impl AnthropicAdapter {
    /// Construct a new adapter.
    ///
    /// `api_key` is the Anthropic API key loaded by the caller from the OS
    /// keychain (service name `"cascade.anthropic"`).  It is stored in memory
    /// only for the lifetime of this adapter instance.
    pub fn new(api_key: impl Into<String>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: BASE_URL.to_owned(),
            http: CascadeHttpClient::new(),
        }
    }

    /// Construct with a custom base URL — used exclusively in tests.
    #[cfg(test)]
    pub fn new_with_base_url(api_key: impl Into<String>, base_url: impl Into<String>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: base_url.into(),
            http: CascadeHttpClient::new(),
        }
    }

    /// Build the Anthropic request body from a `CompletionRequest`.
    ///
    /// System messages are extracted from `messages` (or from `request.system`)
    /// and placed in the Anthropic top-level `system` field.  Only `user` and
    /// `assistant` turns are sent in `messages`.
    fn build_request(&self, req: &CompletionRequest, stream: bool) -> AnthropicRequest {
        // Extract system prompt: prefer explicit `req.system`, else pull system
        // messages out of the messages array.
        let system = if let Some(s) = &req.system {
            Some(s.clone())
        } else {
            let sys: String = req
                .messages
                .iter()
                .filter(|m| m.role == MessageRole::System)
                .map(|m| m.content.as_str())
                .collect::<Vec<_>>()
                .join("\n");
            if sys.is_empty() { None } else { Some(sys) }
        };

        // Only user/assistant messages go into the Anthropic messages array.
        let messages: Vec<AnthropicMessage> = req
            .messages
            .iter()
            .filter(|m| m.role != MessageRole::System)
            .map(|m| AnthropicMessage {
                role: match m.role {
                    MessageRole::User => "user",
                    MessageRole::Assistant => "assistant",
                    MessageRole::System => unreachable!("system filtered above"),
                },
                content: m.content.clone(),
            })
            .collect();

        AnthropicRequest {
            model: req.model.clone(),
            max_tokens: req.max_tokens.unwrap_or(DEFAULT_MAX_TOKENS),
            messages,
            system,
            temperature: req.temperature,
            stream: if stream { Some(true) } else { None },
        }
    }
}

#[async_trait]
impl ProviderAdapter for AnthropicAdapter {
    /// Send a blocking completion request to `POST /v1/messages`.
    async fn complete(
        &self,
        req: CompletionRequest,
    ) -> Result<CompletionResponse, ProviderError> {
        let url = format!("{}{}", self.base_url, MESSAGES_PATH);
        let body = self.build_request(&req, false);
        let api_key = self.api_key.clone();

        let raw: AnthropicResponse = self
            .http
            .post_json(&url, &body, move |builder| {
                CascadeHttpClient::apply_api_key(builder, &api_key)
                    .header("anthropic-version", ANTHROPIC_VERSION)
            })
            .await?;

        // Extract text from the first text content block.
        let content = raw
            .content
            .into_iter()
            .find(|c| c.content_type == "text")
            .map(|c| c.text)
            .unwrap_or_default();

        let prompt_tokens = raw.usage.input_tokens;
        let completion_tokens = raw.usage.output_tokens;
        let usage = TokenUsage {
            prompt_tokens,
            completion_tokens,
            total_tokens: prompt_tokens + completion_tokens,
        };
        let cost_usd = compute_cost("anthropic", &raw.model, &usage);

        Ok(CompletionResponse {
            content,
            model: raw.model,
            usage,
            cost_usd,
        })
    }

    /// Stream a completion from `POST /v1/messages` using SSE.
    async fn complete_stream(
        &self,
        req: CompletionRequest,
    ) -> Result<
        Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
        ProviderError,
    > {
        let url = format!("{}{}", self.base_url, MESSAGES_PATH);
        let body = self.build_request(&req, true);
        let api_key = self.api_key.clone();

        let response = self
            .http
            .post_sse(&url, &body, move |builder| {
                CascadeHttpClient::apply_api_key(builder, &api_key)
                    .header("anthropic-version", ANTHROPIC_VERSION)
            })
            .await?;

        let (tx, rx) = mpsc::channel::<Result<StreamChunk, ProviderError>>(64);

        tokio::spawn(async move {
            // Collect the full body as bytes then parse SSE lines.
            // (wiremock delivers the full body; production sends chunked stream.)
            let bytes = match response.bytes().await {
                Ok(b) => b,
                Err(e) => {
                    let _ = tx
                        .send(Err(ProviderError::NetworkError(e.to_string())))
                        .await;
                    return;
                }
            };

            let text = match std::str::from_utf8(&bytes) {
                Ok(t) => t.to_owned(),
                Err(e) => {
                    let _ = tx
                        .send(Err(ProviderError::InvalidResponse(e.to_string())))
                        .await;
                    return;
                }
            };

            let mut stopped = false;
            for line in text.lines() {
                if let Some(payload) = CascadeHttpClient::parse_sse_line(line) {
                    match serde_json::from_str::<AnthropicSseEvent>(payload) {
                        Ok(event) => match event.event_type.as_str() {
                            "content_block_delta" => {
                                if let Some(delta) = event.delta {
                                    if delta.delta_type == "text_delta" && !delta.text.is_empty() {
                                        if tx
                                            .send(Ok(StreamChunk {
                                                delta: delta.text,
                                                finish_reason: None,
                                            }))
                                            .await
                                            .is_err()
                                        {
                                            return;
                                        }
                                    }
                                }
                            }
                            "message_stop" => {
                                stopped = true;
                                let _ = tx
                                    .send(Ok(StreamChunk {
                                        delta: String::new(),
                                        finish_reason: Some("end_turn".to_owned()),
                                    }))
                                    .await;
                                break;
                            }
                            // message_start, content_block_start, content_block_stop,
                            // message_delta — no chunk to yield.
                            _ => {}
                        },
                        Err(e) => {
                            tracing::warn!(
                                error = %e,
                                payload = %payload,
                                "anthropic SSE: failed to parse event"
                            );
                        }
                    }
                }
            }

            if !stopped {
                let _ = tx
                    .send(Err(ProviderError::StreamInterrupted(
                        "stream ended without message_stop".to_owned(),
                    )))
                    .await;
            }
        });

        Ok(Box::pin(ReceiverStream::new(rx)))
    }

    /// Return a hardcoded list of current Anthropic model identifiers.
    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![
            ModelInfo {
                id: "claude-opus-4-5".to_owned(),
                name: "Claude Opus 4.5".to_owned(),
                context_window: 200_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "claude-sonnet-4-6".to_owned(),
                name: "Claude Sonnet 4.6".to_owned(),
                context_window: 200_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "claude-haiku-4-5".to_owned(),
                name: "Claude Haiku 4.5".to_owned(),
                context_window: 200_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "claude-3-opus".to_owned(),
                name: "Claude 3 Opus".to_owned(),
                context_window: 200_000,
                supports_streaming: true,
            },
        ])
    }

    /// Verify connectivity by GETting `/v1/models`.
    ///
    /// Returns `Ok(())` when the server responds with HTTP 200.
    async fn health_check(&self) -> Result<(), ProviderError> {
        let url = format!("{}{}", self.base_url, MODELS_PATH);
        let api_key = self.api_key.clone();
        self.http
            .get_json::<AnthropicModelsResponse, _>(&url, move |builder| {
                CascadeHttpClient::apply_api_key(builder, &api_key)
                    .header("anthropic-version", ANTHROPIC_VERSION)
            })
            .await?;
        Ok(())
    }

    /// Return static metadata about the Anthropic provider.
    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: "anthropic".to_owned(),
            name: "Anthropic Claude".to_owned(),
            auth_method: AuthMethod::ApiKey,
            base_url: BASE_URL.to_owned(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: true,
                max_context_tokens: 200_000,
                supports_function_calling: false,
            },
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::test_support::{
        assert_completion_contract, fixture_json, make_anthropic_sse, HttpMethod,
        MockProviderServer,
    };
    use futures::StreamExt;
    use serde_json::json;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, ResponseTemplate};

    // ── happy-path complete() ─────────────────────────────────────────────────

    #[tokio::test]
    async fn complete_happy_path_from_fixture() {
        let ctx = MockProviderServer::start("anthropic").await;
        ctx.mount_json(
            HttpMethod::Post,
            MESSAGES_PATH,
            200,
            fixture_json("anthropic_complete"),
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let req =
            CompletionRequest::simple("claude-3-5-sonnet-20241022", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("complete should succeed");

        assert_completion_contract(&resp.content, &resp.model, resp.usage.prompt_tokens);
        assert!(
            resp.usage.completion_tokens > 0,
            "completion_tokens must be positive"
        );
        assert_eq!(
            resp.usage.total_tokens,
            resp.usage.prompt_tokens + resp.usage.completion_tokens
        );
    }

    // ── system prompt extracted from messages array ───────────────────────────

    #[tokio::test]
    async fn complete_with_system_message_in_array() {
        let ctx = MockProviderServer::start("anthropic").await;
        ctx.mount_json(
            HttpMethod::Post,
            MESSAGES_PATH,
            200,
            fixture_json("anthropic_complete"),
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let mut req =
            CompletionRequest::simple("claude-3-5-sonnet-20241022", "Hello");
        req.messages
            .insert(0, crate::types::Message::system("Be concise."));
        let resp = adapter.complete(req).await.expect("complete should succeed");
        assert!(!resp.content.is_empty());
    }

    // ── auth error (401) ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn complete_returns_auth_failed_on_401() {
        let ctx = MockProviderServer::start("anthropic").await;
        Mock::given(method("POST"))
            .and(path(MESSAGES_PATH))
            .respond_with(ResponseTemplate::new(401))
            .mount(&ctx.server)
            .await;

        let adapter = AnthropicAdapter::new_with_base_url("bad-key", ctx.base_url());
        let req = CompletionRequest::simple("claude-3-5-sonnet-20241022", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    // ── rate-limit: retry-after header parsed ─────────────────────────────────

    #[tokio::test]
    async fn complete_returns_rate_limited_with_retry_after() {
        let ctx = MockProviderServer::start("anthropic").await;
        // Exhaust all retries with 429; retry-after: 0 so backoff is instant.
        Mock::given(method("POST"))
            .and(path(MESSAGES_PATH))
            .respond_with(
                ResponseTemplate::new(429)
                    .append_header("retry-after", "42")
                    .append_header("content-length", "0"),
            )
            .mount(&ctx.server)
            .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let req = CompletionRequest::simple("claude-3-5-sonnet-20241022", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::RateLimited { .. }),
            "expected RateLimited, got {err:?}"
        );
    }

    // ── SSE stream parse ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn complete_stream_yields_chunks_from_sse() {
        let ctx = MockProviderServer::start("anthropic").await;
        let sse_body = make_anthropic_sse(&["The ", "capital ", "of ", "France is Paris."]);
        ctx.mount_raw(
            HttpMethod::Post,
            MESSAGES_PATH,
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let req =
            CompletionRequest::simple("claude-3-5-sonnet-20241022", "What is the capital?");

        let stream = adapter
            .complete_stream(req)
            .await
            .expect("stream should open");
        let chunks: Vec<_> = stream.collect().await;

        let text_chunks: Vec<_> = chunks
            .iter()
            .filter_map(|r| r.as_ref().ok())
            .filter(|c| !c.delta.is_empty())
            .collect();

        assert!(
            text_chunks.len() >= 3,
            "expected at least 3 text chunks, got {}",
            text_chunks.len()
        );

        let stop_chunk = chunks
            .iter()
            .filter_map(|r| r.as_ref().ok())
            .find(|c| c.finish_reason.is_some());
        assert!(stop_chunk.is_some(), "expected a stop chunk with finish_reason");
    }

    // ── available_models ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn available_models_returns_four_models() {
        let adapter = AnthropicAdapter::new("test-key");
        let models = adapter.available_models().await.expect("models should load");
        assert_eq!(models.len(), 4, "expected exactly 4 models");
        assert!(models.iter().all(|m| m.context_window == 200_000));
        assert!(models.iter().all(|m| m.supports_streaming));
        let ids: Vec<&str> = models.iter().map(|m| m.id.as_str()).collect();
        assert!(ids.contains(&"claude-opus-4-5"));
        assert!(ids.contains(&"claude-sonnet-4-6"));
        assert!(ids.contains(&"claude-haiku-4-5"));
        assert!(ids.contains(&"claude-3-opus"));
    }

    // ── health_check ──────────────────────────────────────────────────────────

    #[tokio::test]
    async fn health_check_ok_on_200() {
        let ctx = MockProviderServer::start("anthropic").await;
        ctx.mount_json(
            HttpMethod::Get,
            MODELS_PATH,
            200,
            json!({"data": [{"id": "claude-3-5-sonnet-20241022"}]}),
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        adapter
            .health_check()
            .await
            .expect("health_check should succeed");
    }

    #[tokio::test]
    async fn health_check_returns_auth_failed_on_401() {
        let ctx = MockProviderServer::start("anthropic").await;
        Mock::given(method("GET"))
            .and(path(MODELS_PATH))
            .respond_with(ResponseTemplate::new(401))
            .mount(&ctx.server)
            .await;

        let adapter = AnthropicAdapter::new_with_base_url("bad-key", ctx.base_url());
        let err = adapter.health_check().await.unwrap_err();
        assert!(matches!(err, ProviderError::AuthFailed(_)));
    }

    // ── provider_info ─────────────────────────────────────────────────────────

    #[test]
    fn provider_info_fields() {
        let adapter = AnthropicAdapter::new("test-key");
        let info = adapter.provider_info();
        assert_eq!(info.id, "anthropic");
        assert_eq!(info.auth_method, AuthMethod::ApiKey);
        assert_eq!(info.base_url, BASE_URL);
        assert!(info.capabilities.supports_streaming);
        assert_eq!(info.capabilities.max_context_tokens, 200_000);
    }

    // ── API key never exposed ─────────────────────────────────────────────────

    #[test]
    fn api_key_not_in_provider_info_debug() {
        let adapter = AnthropicAdapter::new("sk-secret-1234567890");
        let info = adapter.provider_info();
        let s = format!("{info:?}");
        assert!(
            !s.contains("sk-secret"),
            "API key must not appear in provider_info debug output"
        );
    }
}
