//! Gemini adapter — Google Generative Language API v1beta.
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for Google Gemini models via two endpoint modes:
//!
//! - **Direct** (`use_gfp_proxy = false`): targets `generativelanguage.googleapis.com`
//!   with API key authentication via the `?key=` query parameter.
//! - **Pool-proxy / GFP** (`use_gfp_proxy = true`): targets the local Gemini Fleet
//!   Proxy daemon at `http://127.0.0.1:3761`. The proxy handles key rotation; the
//!   adapter sends **no** API key in proxy mode.
//!
//! # Inputs / Outputs
//!
//! - Input: [`CompletionRequest`] — provider-agnostic request type.
//! - Output: [`CompletionResponse`] (blocking) or `Stream<StreamChunk>` (streaming).
//!
//! # Design
//!
//! - One `GeminiAdapter` per adapter instance; internally holds a `CascadeHttpClient`
//!   (cheaply cloneable) and a `GeminiConfig`.
//! - API keys are fetched from the cascade-keychain service `"cascade.gemini"` at
//!   construction time; never stored in plain logs.
//! - Streaming uses `streamGenerateContent?alt=sse`; SSE lines are parsed one-by-one.
//!
//! # Constraints
//!
//! - API key is never logged at any tracing level.
//! - `use_gfp_proxy = true` suppresses all `googleapis.com` traffic.
//! - Gemini's `usageMetadata.totalTokenCount` maps to `TokenUsage::total_tokens`.

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

/// Direct Gemini API base URL (without trailing slash).
const GEMINI_DIRECT_BASE: &str = "https://generativelanguage.googleapis.com";

/// Local pool-proxy URL — the legacy cascade-gemini-proxy daemon.
const GEMINI_PROXY_BASE: &str = "http://127.0.0.1:3761";

/// Default model when none is specified in `GeminiConfig`.
const DEFAULT_MODEL: &str = "gemini-2.0-flash";

// ── GeminiConfig ──────────────────────────────────────────────────────────────

/// Configuration for `GeminiAdapter`.
///
/// ## GFP/GPP toggle
///
/// When `use_gfp_proxy` is `true` the adapter targets the local Gemini Fleet
/// Proxy (`http://127.0.0.1:3761`) which handles key rotation itself.  No API
/// key is sent by the adapter in that mode.  When `false` (the default), a
/// valid `api_key` must be supplied and is passed as `?key=` on every request.
#[derive(Debug, Clone)]
pub struct GeminiConfig {
    /// Resolved base URL.  Overridden to `GEMINI_PROXY_BASE` when
    /// `use_gfp_proxy` is `true`.
    pub base_url: String,

    /// Route through the local GFP pool-proxy daemon instead of calling
    /// Google directly.  When `true`, `api_key` is ignored.
    pub use_gfp_proxy: bool,

    /// Google API key.  Required when `use_gfp_proxy = false`.
    /// Must come from the cascade-keychain service `"cascade.gemini"`.
    pub api_key: Option<String>,

    /// Gemini model identifier.  Defaults to `gemini-2.0-flash`.
    pub model: String,
}

impl GeminiConfig {
    /// Build a direct-mode config with the provided API key and default model.
    pub fn direct(api_key: impl Into<String>) -> Self {
        Self {
            base_url: GEMINI_DIRECT_BASE.to_string(),
            use_gfp_proxy: false,
            api_key: Some(api_key.into()),
            model: DEFAULT_MODEL.to_string(),
        }
    }

    /// Build a pool-proxy config.  No API key is sent.
    pub fn proxy() -> Self {
        Self {
            base_url: GEMINI_PROXY_BASE.to_string(),
            use_gfp_proxy: true,
            api_key: None,
            model: DEFAULT_MODEL.to_string(),
        }
    }

    /// Override the model identifier on an existing config.
    pub fn with_model(mut self, model: impl Into<String>) -> Self {
        self.model = model.into();
        self
    }
}

impl Default for GeminiConfig {
    fn default() -> Self {
        Self {
            base_url: GEMINI_DIRECT_BASE.to_string(),
            use_gfp_proxy: false,
            api_key: None,
            model: DEFAULT_MODEL.to_string(),
        }
    }
}

// ── GeminiAdapter ─────────────────────────────────────────────────────────────

/// `ProviderAdapter` for Google Gemini.
///
/// Create via [`GeminiAdapter::new`].  One instance per adapter seat in the
/// `ProviderRegistry`.
///
/// ## Thread safety
///
/// `GeminiAdapter` is `Send + Sync`; safe to wrap in `Arc`.
pub struct GeminiAdapter {
    config: GeminiConfig,
    http: CascadeHttpClient,
}

impl GeminiAdapter {
    /// Construct a new adapter from a [`GeminiConfig`].
    pub fn new(config: GeminiConfig) -> Self {
        Self {
            config,
            http: CascadeHttpClient::new(),
        }
    }

    // ── URL builders ──────────────────────────────────────────────────────────

    /// Build the `generateContent` URL.  Appends `?key=` only in direct mode.
    fn complete_url(&self, model: &str) -> String {
        let base = &self.config.base_url;
        if self.config.use_gfp_proxy {
            format!("{base}/v1beta/models/{model}:generateContent")
        } else {
            let key = self.config.api_key.as_deref().unwrap_or("");
            format!("{base}/v1beta/models/{model}:generateContent?key={key}")
        }
    }

    /// Build the `streamGenerateContent` URL with `alt=sse`.
    fn stream_url(&self, model: &str) -> String {
        let base = &self.config.base_url;
        if self.config.use_gfp_proxy {
            format!("{base}/v1beta/models/{model}:streamGenerateContent?alt=sse")
        } else {
            let key = self.config.api_key.as_deref().unwrap_or("");
            format!("{base}/v1beta/models/{model}:streamGenerateContent?key={key}&alt=sse")
        }
    }

    /// Build the `GET /v1beta/models` health-check URL.
    fn models_url(&self) -> String {
        let base = &self.config.base_url;
        if self.config.use_gfp_proxy {
            format!("{base}/v1beta/models")
        } else {
            let key = self.config.api_key.as_deref().unwrap_or("");
            format!("{base}/v1beta/models?key={key}")
        }
    }

    // ── Request mapping ───────────────────────────────────────────────────────

    /// Map a `CompletionRequest` to a `GeminiRequest` wire body.
    fn map_request(&self, req: &CompletionRequest) -> GeminiRequest {
        // Separate system prompt from user/assistant messages.
        let system_instruction = req.system.as_deref().map(|text| GeminiSystemInstruction {
            parts: vec![GeminiPart {
                text: text.to_string(),
            }],
        });

        // Filter out system-role messages (handled via systemInstruction).
        let contents: Vec<GeminiContent> = req
            .messages
            .iter()
            .filter(|m| m.role != MessageRole::System)
            .map(|m| GeminiContent {
                role: match m.role {
                    MessageRole::User => "user".to_string(),
                    MessageRole::Assistant => "model".to_string(),
                    MessageRole::System => unreachable!("filtered above"),
                },
                parts: vec![GeminiPart {
                    text: m.content.clone(),
                }],
            })
            .collect();

        let generation_config = match (req.max_tokens, req.temperature) {
            (None, None) => None,
            _ => Some(GenerationConfig {
                max_output_tokens: req.max_tokens,
                temperature: req.temperature,
            }),
        };

        GeminiRequest {
            contents,
            system_instruction,
            generation_config,
        }
    }

    // ── Auth closure ──────────────────────────────────────────────────────────

    /// Return a no-op auth closure (Gemini uses query-param auth, not headers).
    fn auth_noop() -> impl Fn(reqwest::RequestBuilder) -> reqwest::RequestBuilder {
        |b| b
    }
}

// ── ProviderAdapter impl ──────────────────────────────────────────────────────

#[async_trait]
impl ProviderAdapter for GeminiAdapter {
    async fn complete(
        &self,
        req: CompletionRequest,
    ) -> Result<CompletionResponse, ProviderError> {
        let model = if req.model.is_empty() {
            &self.config.model
        } else {
            &req.model
        };

        let url = self.complete_url(model);
        debug!(model, url_path = "/v1beta/models/…:generateContent", "gemini complete");

        let wire_req = self.map_request(&req);
        let raw: GeminiResponse = self
            .http
            .post_json(&url, &wire_req, Self::auth_noop())
            .await?;

        let content = raw
            .candidates
            .first()
            .and_then(|c| c.content.parts.first())
            .map(|p| p.text.clone())
            .unwrap_or_default();

        let usage = TokenUsage {
            prompt_tokens: raw.usage_metadata.as_ref().map(|u| u.prompt_token_count).unwrap_or(0),
            completion_tokens: raw.usage_metadata.as_ref().map(|u| u.candidates_token_count).unwrap_or(0),
            total_tokens: raw.usage_metadata.as_ref().map(|u| u.total_token_count).unwrap_or(0),
        };

        let response_model = raw
            .model_version
            .unwrap_or_else(|| model.to_string());

        let cost_usd = compute_cost("google-gemini", &response_model, &usage);
        Ok(CompletionResponse {
            content,
            model: response_model,
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
        let model = if req.model.is_empty() {
            self.config.model.clone()
        } else {
            req.model.clone()
        };

        let url = self.stream_url(&model);
        debug!(model, "gemini complete_stream");

        let wire_req = self.map_request(&req);
        let response = self
            .http
            .post_sse(&url, &wire_req, Self::auth_noop())
            .await?;

        // Spawn a task that drains the byte stream line-by-line and forwards
        // parsed StreamChunks through a bounded mpsc channel.
        let (tx, rx) = tokio::sync::mpsc::channel::<Result<StreamChunk, ProviderError>>(32);

        tokio::spawn(async move {
            use futures::StreamExt as _;

            let mut byte_stream = response.bytes_stream();
            let mut buf = String::new();

            while let Some(raw_chunk) = byte_stream.next().await {
                let bytes = match raw_chunk {
                    Ok(b) => b,
                    Err(e) => {
                        let _ = tx.send(Err(ProviderError::StreamInterrupted(e.to_string()))).await;
                        return;
                    }
                };

                let text = match std::str::from_utf8(&bytes) {
                    Ok(s) => s.to_string(),
                    Err(_) => {
                        let _ = tx.send(Err(ProviderError::InvalidResponse(
                            "non-UTF-8 SSE chunk".to_string(),
                        ))).await;
                        return;
                    }
                };

                buf.push_str(&text);

                // Consume all complete lines from the buffer.
                while let Some(pos) = buf.find('\n') {
                    let line = buf[..pos].trim_end_matches('\r').to_string();
                    buf = buf[pos + 1..].to_string();

                    if let Some(payload) = CascadeHttpClient::parse_sse_line(&line) {
                        match parse_gemini_stream_event(payload) {
                            Ok(Some(chunk)) => {
                                let done = chunk.finish_reason.is_some();
                                if tx.send(Ok(chunk)).await.is_err() {
                                    return;
                                }
                                if done {
                                    return;
                                }
                            }
                            Ok(None) => {}
                            Err(e) => {
                                let _ = tx.send(Err(e)).await;
                                return;
                            }
                        }
                    }
                }
            }
        });

        Ok(Box::pin(tokio_stream::wrappers::ReceiverStream::new(rx)))
    }

    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![
            ModelInfo {
                id: "gemini-2.0-flash".to_string(),
                name: "Gemini 2.0 Flash".to_string(),
                context_window: 1_048_576,
                supports_streaming: true,
            },
            ModelInfo {
                id: "gemini-2.0-flash-lite".to_string(),
                name: "Gemini 2.0 Flash Lite".to_string(),
                context_window: 1_048_576,
                supports_streaming: true,
            },
            ModelInfo {
                id: "gemini-1.5-pro".to_string(),
                name: "Gemini 1.5 Pro".to_string(),
                context_window: 2_097_152,
                supports_streaming: true,
            },
            ModelInfo {
                id: "gemini-1.5-flash".to_string(),
                name: "Gemini 1.5 Flash".to_string(),
                context_window: 1_048_576,
                supports_streaming: true,
            },
            ModelInfo {
                id: "gemini-1.0-pro".to_string(),
                name: "Gemini 1.0 Pro".to_string(),
                context_window: 32_760,
                supports_streaming: true,
            },
        ])
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        let url = self.models_url();
        debug!("gemini health_check");

        // Lightweight: GET models list; just verifies reachability + auth.
        self.http
            .get_json::<serde_json::Value, _>(&url, Self::auth_noop())
            .await
            .map(|_| ())
    }

    fn provider_info(&self) -> ProviderInfo {
        let base_url = self.config.base_url.clone();
        ProviderInfo {
            id: "google-gemini".to_string(),
            name: "Google Gemini".to_string(),
            auth_method: AuthMethod::ApiKey,
            base_url,
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                // Vision is supported by gemini-1.5-pro and gemini-2.0-flash
                // via multimodal parts; text-only for now (P3 scope).
                supports_vision: false,
                // gemini-1.5-pro context window (largest in the default set).
                max_context_tokens: 2_097_152,
                supports_function_calling: true,
            },
        }
    }
}

// ── Wire types (Gemini API shapes) ────────────────────────────────────────────

/// Gemini `generateContent` / `streamGenerateContent` request body.
#[derive(Debug, Serialize)]
struct GeminiRequest {
    contents: Vec<GeminiContent>,
    #[serde(skip_serializing_if = "Option::is_none")]
    #[serde(rename = "systemInstruction")]
    system_instruction: Option<GeminiSystemInstruction>,
    #[serde(skip_serializing_if = "Option::is_none")]
    #[serde(rename = "generationConfig")]
    generation_config: Option<GenerationConfig>,
}

#[derive(Debug, Serialize)]
struct GeminiContent {
    role: String,
    parts: Vec<GeminiPart>,
}

#[derive(Debug, Serialize)]
struct GeminiSystemInstruction {
    parts: Vec<GeminiPart>,
}

#[derive(Debug, Serialize, Deserialize)]
struct GeminiPart {
    text: String,
}

#[derive(Debug, Serialize)]
struct GenerationConfig {
    #[serde(skip_serializing_if = "Option::is_none")]
    #[serde(rename = "maxOutputTokens")]
    max_output_tokens: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    temperature: Option<f32>,
}

/// `generateContent` response.
#[derive(Debug, Deserialize)]
struct GeminiResponse {
    candidates: Vec<GeminiCandidate>,
    #[serde(rename = "usageMetadata")]
    usage_metadata: Option<GeminiUsageMetadata>,
    #[serde(rename = "modelVersion")]
    model_version: Option<String>,
}

#[derive(Debug, Deserialize)]
struct GeminiCandidate {
    content: GeminiContentResponse,
    #[serde(rename = "finishReason")]
    finish_reason: Option<String>,
}

#[derive(Debug, Deserialize)]
struct GeminiContentResponse {
    parts: Vec<GeminiPart>,
}

#[derive(Debug, Deserialize)]
struct GeminiUsageMetadata {
    #[serde(rename = "promptTokenCount", default)]
    prompt_token_count: u32,
    #[serde(rename = "candidatesTokenCount", default)]
    candidates_token_count: u32,
    #[serde(rename = "totalTokenCount", default)]
    total_token_count: u32,
}

// ── SSE parse helper ──────────────────────────────────────────────────────────

/// Parse one SSE `data:` payload as a Gemini stream event.
///
/// Returns `Ok(Some(chunk))` with text delta, `Ok(None)` if the event carries
/// no text (e.g. safety-only events), or `Err` on parse failure.
fn parse_gemini_stream_event(payload: &str) -> Result<Option<StreamChunk>, ProviderError> {
    let val: serde_json::Value = serde_json::from_str(payload)
        .map_err(|e| ProviderError::InvalidResponse(format!("SSE JSON: {e}")))?;

    let candidate = val
        .get("candidates")
        .and_then(|c| c.get(0));

    let text = candidate
        .and_then(|c| c.get("content"))
        .and_then(|c| c.get("parts"))
        .and_then(|p| p.get(0))
        .and_then(|p| p.get("text"))
        .and_then(|t| t.as_str())
        .unwrap_or("")
        .to_string();

    let finish_reason = candidate
        .and_then(|c| c.get("finishReason"))
        .and_then(|f| f.as_str())
        .filter(|s| *s != "FINISH_REASON_UNSPECIFIED" && !s.is_empty())
        .map(|s| s.to_lowercase());

    if text.is_empty() && finish_reason.is_none() {
        return Ok(None);
    }

    Ok(Some(StreamChunk {
        delta: text,
        finish_reason,
    }))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::test_support::{fixture_json, fixture_text, HttpMethod, MockProviderServer};
    use crate::types::CompletionRequest;
    use futures::StreamExt;
    use wiremock::matchers::{method, path, query_param};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    // ── GFP toggle: proxy mode must NOT send key in URL or headers ────────────

    #[tokio::test]
    async fn proxy_mode_no_key_in_request() {
        let server = MockServer::start().await;

        // This mock will FAIL the test if `key` query param is present.
        Mock::given(method("POST"))
            .and(path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .respond_with(
                ResponseTemplate::new(200).set_body_json(fixture_json("gemini_complete")),
            )
            .mount(&server)
            .await;

        let mut config = GeminiConfig::proxy();
        config.base_url = server.uri(); // point to mock

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "hello");
        let result = adapter.complete(req).await;
        assert!(result.is_ok(), "proxy mode should succeed: {:?}", result);

        // Verify no `key` query param was sent.
        let received = server.received_requests().await.unwrap();
        assert_eq!(received.len(), 1);
        let url = received[0].url.to_string();
        assert!(
            !url.contains("key="),
            "proxy mode MUST NOT include key in URL: {url}"
        );
        // Verify no x-goog-api-key or Authorization header.
        let headers = &received[0].headers;
        let has_auth = headers.contains_key("x-goog-api-key")
            || headers.contains_key("authorization");
        assert!(!has_auth, "proxy mode MUST NOT send auth headers");
    }

    // ── Direct mode sends key in URL ──────────────────────────────────────────

    #[tokio::test]
    async fn direct_mode_includes_key_in_url() {
        let server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .and(query_param("key", "test-api-key"))
            .respond_with(
                ResponseTemplate::new(200).set_body_json(fixture_json("gemini_complete")),
            )
            .mount(&server)
            .await;

        let mut config = GeminiConfig::direct("test-api-key");
        config.base_url = server.uri();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("direct mode complete");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(resp.usage.total_tokens > 0, "total_tokens must be > 0");
    }

    // ── Happy-path complete() from fixture ────────────────────────────────────

    #[tokio::test]
    async fn complete_happy_path() {
        let ctx = MockProviderServer::start("gemini").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1beta/models/gemini-2.0-flash:generateContent",
            200,
            fixture_json("gemini_complete"),
        )
        .await;

        let mut config = GeminiConfig::direct("test-key");
        config.base_url = ctx.base_url();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("complete");

        assert_eq!(resp.content, "The capital of France is Paris.");
        assert_eq!(resp.usage.prompt_tokens, 14);
        assert_eq!(resp.usage.completion_tokens, 9);
        assert_eq!(resp.usage.total_tokens, 23);
        assert!(!resp.model.is_empty());

        crate::test_helpers::test_support::assert_completion_contract(
            &resp.content,
            &resp.model,
            resp.usage.prompt_tokens,
        );
    }

    // ── Rate-limit error mapping ──────────────────────────────────────────────

    #[tokio::test]
    async fn complete_rate_limit_maps_to_rate_limited() {
        let server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/v1beta/models/gemini-2.0-flash:generateContent"))
            .respond_with(
                ResponseTemplate::new(429)
                    .append_header("retry-after", "0"),
            )
            .mount(&server)
            .await;

        let mut config = GeminiConfig::direct("test-key");
        config.base_url = server.uri();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "hi");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::RateLimited { .. }),
            "expected RateLimited, got {err:?}"
        );
    }

    // ── Streaming: multi-event SSE fixture yields ≥1 chunk ───────────────────

    #[tokio::test]
    async fn complete_stream_yields_chunks() {
        let ctx = MockProviderServer::start("gemini-stream").await;
        let sse_body = fixture_text("gemini_stream");
        ctx.mount_raw(
            HttpMethod::Post,
            "/v1beta/models/gemini-2.0-flash:streamGenerateContent",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let mut config = GeminiConfig::direct("test-key");
        config.base_url = ctx.base_url();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "Stream test");
        let stream = adapter.complete_stream(req).await.expect("stream start");

        let chunks: Vec<_> = stream.collect().await;
        assert!(!chunks.is_empty(), "expected at least 1 chunk");

        let ok_chunks: Vec<StreamChunk> = chunks.into_iter().map(|r| r.unwrap()).collect();
        let full_text: String = ok_chunks.iter().map(|c| c.delta.as_str()).collect();
        assert!(!full_text.is_empty(), "combined delta text must not be empty");
    }

    // ── Streaming proxy mode: no key in URL ───────────────────────────────────

    #[tokio::test]
    async fn stream_proxy_mode_no_key() {
        let ctx = MockProviderServer::start("gemini-stream-proxy").await;
        let sse_body = fixture_text("gemini_stream");
        ctx.mount_raw(
            HttpMethod::Post,
            "/v1beta/models/gemini-2.0-flash:streamGenerateContent",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let mut config = GeminiConfig::proxy();
        config.base_url = ctx.base_url();

        let adapter = GeminiAdapter::new(config);
        let req = CompletionRequest::simple("gemini-2.0-flash", "proxy stream");
        let stream = adapter.complete_stream(req).await.expect("stream");
        let chunks: Vec<_> = stream.collect().await;
        assert!(!chunks.is_empty());

        // Verify no key in URL.
        let received = ctx.server.received_requests().await.unwrap();
        assert_eq!(received.len(), 1);
        let url = received[0].url.to_string();
        assert!(!url.contains("key="), "proxy stream MUST NOT include key: {url}");
    }

    // ── provider_info contract ────────────────────────────────────────────────

    #[test]
    fn provider_info_is_valid() {
        let adapter = GeminiAdapter::new(GeminiConfig::direct("dummy"));
        let info = adapter.provider_info();
        assert_eq!(info.id, "google-gemini");
        assert!(!info.name.is_empty());
        assert!(info.capabilities.supports_streaming);
    }

    // ── available_models returns all expected entries ─────────────────────────

    #[tokio::test]
    async fn available_models_includes_flash_and_pro() {
        let adapter = GeminiAdapter::new(GeminiConfig::direct("dummy"));
        let models = adapter.available_models().await.unwrap();
        let ids: Vec<&str> = models.iter().map(|m| m.id.as_str()).collect();
        assert!(ids.contains(&"gemini-2.0-flash"), "must include gemini-2.0-flash");
        assert!(ids.contains(&"gemini-1.5-pro"), "must include gemini-1.5-pro");
        assert!(ids.contains(&"gemini-1.0-pro"), "must include gemini-1.0-pro");
    }

    // ── proxy mode changes base_url ───────────────────────────────────────────

    #[test]
    fn proxy_config_sets_base_url() {
        let config = GeminiConfig::proxy();
        assert_eq!(config.base_url, GEMINI_PROXY_BASE);
        assert!(config.use_gfp_proxy);
        assert!(config.api_key.is_none());
    }

    #[test]
    fn direct_config_sets_base_url() {
        let config = GeminiConfig::direct("my-key");
        assert_eq!(config.base_url, GEMINI_DIRECT_BASE);
        assert!(!config.use_gfp_proxy);
        assert!(config.api_key.is_some());
    }

    // ── parse_gemini_stream_event unit tests ──────────────────────────────────

    #[test]
    fn parse_event_with_text() {
        let payload = r#"{"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":""}]}"#;
        let result = parse_gemini_stream_event(payload).unwrap();
        assert!(result.is_some());
        assert_eq!(result.unwrap().delta, "Hello");
    }

    #[test]
    fn parse_event_with_stop() {
        let payload = r#"{"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"STOP"}]}"#;
        let result = parse_gemini_stream_event(payload).unwrap();
        let chunk = result.unwrap();
        assert!(chunk.finish_reason.is_some());
        assert_eq!(chunk.finish_reason.unwrap(), "stop");
    }

    #[test]
    fn parse_empty_event_returns_none() {
        let payload = r#"{"candidates":[{"content":{"parts":[{"text":""}]}}]}"#;
        let result = parse_gemini_stream_event(payload).unwrap();
        assert!(result.is_none());
    }
}
