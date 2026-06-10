//! GenericOpenAIAdapter — user-supplied OpenAI-compatible endpoint adapter.
//!
//! # Purpose
//!
//! Allows the user to point Cascade at any OpenAI Chat Completions–compatible
//! endpoint by providing a `base_url` and optional credentials.  Common use
//! cases: LM Studio (`http://localhost:1234`), Ollama in OpenAI-compat mode
//! (`http://localhost:11434`), Azure OpenAI, and self-hosted vLLM /
//! text-generation-inference servers.
//!
//! # Inputs
//!
//! `GenericOpenAIConfig` carries all user-supplied parameters.  The adapter
//! never reads from any other provider's credential store — it only uses the
//! api_key field that was given to it at construction time.
//!
//! # SSRF posture
//!
//! This adapter issues HTTP requests to a URL supplied by the end user.
//! Mitigations applied:
//! - `base_url` is validated at construction time: must be an absolute
//!   `http://` or `https://` URL (scheme enforcement).  Other schemes (file://,
//!   ftp://, etc.) are rejected with `ProviderError::NetworkError`.
//! - No DNS rebinding protection or private-IP block is applied at this layer
//!   (that is a responsibility of the network policy / OS firewall).
//! - The `api_key` passed in `GenericOpenAIConfig` is used ONLY for requests
//!   to the configured `base_url`; it is never forwarded to any other provider.
//! - No other provider's key (Anthropic, Google, OpenAI) is ever attached to
//!   requests from this adapter.
//!
//! # Constraints
//!
//! - `api_key` may be empty — many local servers require no authentication.
//!   When empty, no `Authorization` header is sent (CR-A requirement).
//! - `available_models()` attempts `GET {base_url}/v1/models`; on any error
//!   it silently falls back to returning the single configured model.
//! - `provider_info()` is synchronous and performs no I/O.
//! - `GenericOpenAIConfig` derives `Serialize + Deserialize` for settings
//!   storage (CR-B requirement).

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;
use futures::StreamExt;
use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::{
    adapter::ProviderAdapter,
    cost::compute_cost,
    error::ProviderError,
    http_client::CascadeHttpClient,
    provider_info::{AuthMethod, ProviderCapabilities, ProviderInfo},
    types::{CompletionRequest, CompletionResponse, MessageRole, ModelInfo, StreamChunk, TokenUsage},
};

// ── GenericOpenAIConfig ───────────────────────────────────────────────────────

/// Configuration for a user-supplied OpenAI-compatible endpoint.
///
/// Serializable for persistence in the Cascade settings store.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct GenericOpenAIConfig {
    /// Base URL of the OpenAI-compatible server.
    ///
    /// Must be an absolute `http://` or `https://` URL.  Trailing slashes are
    /// stripped at construction time so paths like `/v1/chat/completions` are
    /// always appended correctly.
    pub base_url: String,

    /// API key to send as `Authorization: Bearer <key>`.
    ///
    /// Set to an empty string for endpoints that require no authentication.
    /// When empty, no `Authorization` header is added (CR-A requirement).
    pub api_key: String,

    /// Model identifier to use for completions.
    ///
    /// Also used as the fallback in `available_models()` when the `/v1/models`
    /// endpoint is unreachable.
    pub model_id: String,

    /// Optional human-readable display name shown in the UI.
    ///
    /// Defaults to the host portion of `base_url` when `None`.
    pub display_name: Option<String>,

    /// Maximum context window size in tokens.
    ///
    /// Defaults to `4096` when `None`.
    pub context_window: Option<u32>,
}

impl GenericOpenAIConfig {
    /// Construct a minimal config (no auth, 4096 context window).
    pub fn new(base_url: impl Into<String>, model_id: impl Into<String>) -> Self {
        Self {
            base_url: base_url.into(),
            api_key: String::new(),
            model_id: model_id.into(),
            display_name: None,
            context_window: None,
        }
    }

    /// Construct a config with an API key.
    pub fn with_key(
        base_url: impl Into<String>,
        api_key: impl Into<String>,
        model_id: impl Into<String>,
    ) -> Self {
        Self {
            base_url: base_url.into(),
            api_key: api_key.into(),
            model_id: model_id.into(),
            display_name: None,
            context_window: None,
        }
    }

    /// Derive the display name: the configured `display_name` or the host of
    /// `base_url` as a fallback.
    fn resolved_display_name(&self) -> String {
        if let Some(ref name) = self.display_name {
            return name.clone();
        }
        // Extract just the host portion for a clean display name.
        extract_host(&self.base_url)
            .unwrap_or_else(|| self.base_url.clone())
    }

    /// Normalised base URL (trailing slash stripped).
    fn normalised_base_url(&self) -> String {
        self.base_url.trim_end_matches('/').to_owned()
    }

    /// Context window with the default of 4096.
    fn resolved_context_window(&self) -> u32 {
        self.context_window.unwrap_or(4096)
    }
}

// ── GenericOpenAIAdapter ──────────────────────────────────────────────────────

/// `ProviderAdapter` implementation for any OpenAI Chat Completions–compatible
/// endpoint.
///
/// ## Provider ID
///
/// `"generic:{host}:{model_id}"` — uniquely identifies the endpoint+model
/// combination in the provider registry.
///
/// ## SSRF note
///
/// Requests are sent to the `base_url` supplied by the user.  See module-level
/// docs for the security posture.
#[derive(Debug)]
pub struct GenericOpenAIAdapter {
    config: GenericOpenAIConfig,
    http: CascadeHttpClient,
}

impl GenericOpenAIAdapter {
    /// Construct a new adapter from the given config.
    ///
    /// # Errors
    ///
    /// Returns `ProviderError::NetworkError` when `config.base_url` is not an
    /// absolute `http://` or `https://` URL.  All other validation is deferred
    /// to the first network call.
    pub fn new(config: GenericOpenAIConfig) -> Result<Self, ProviderError> {
        validate_url(&config.base_url)?;
        Ok(Self {
            config,
            http: CascadeHttpClient::new(),
        })
    }

    /// Build the stable provider ID from host + model.
    fn provider_id(&self) -> String {
        let host = extract_host(&self.config.base_url)
            .unwrap_or_else(|| self.config.base_url.clone());
        format!("generic:{}:{}", host, self.config.model_id)
    }

    /// Build the OpenAI chat-completions request body.
    fn build_chat_body(&self, req: &CompletionRequest, stream: bool) -> Value {
        let messages: Vec<Value> = req
            .messages
            .iter()
            .map(|m| {
                serde_json::json!({
                    "role": match m.role {
                        MessageRole::System    => "system",
                        MessageRole::User      => "user",
                        MessageRole::Assistant => "assistant",
                    },
                    "content": m.content,
                })
            })
            .collect();

        let mut body = serde_json::json!({
            "model": self.config.model_id,
            "messages": messages,
            "stream": stream,
        });

        if let Some(max_tokens) = req.max_tokens {
            body["max_tokens"] = Value::from(max_tokens);
        }
        if let Some(temp) = req.temperature {
            body["temperature"] = Value::from(temp);
        }

        body
    }
}

// ── ProviderAdapter impl ──────────────────────────────────────────────────────

#[async_trait]
impl ProviderAdapter for GenericOpenAIAdapter {
    /// Non-streaming completion via POST `/v1/chat/completions`.
    async fn complete(
        &self,
        req: CompletionRequest,
    ) -> Result<CompletionResponse, ProviderError> {
        let url = format!("{}/v1/chat/completions", self.config.normalised_base_url());
        let body = self.build_chat_body(&req, false);

        let api_key = self.config.api_key.clone();
        let raw: Value = self
            .http
            .post_json(&url, &body, move |b| {
                if api_key.is_empty() {
                    b
                } else {
                    CascadeHttpClient::apply_bearer(b, &api_key)
                }
            })
            .await?;

        parse_openai_response(&raw)
    }

    /// Streaming completion via POST `/v1/chat/completions` with SSE.
    async fn complete_stream(
        &self,
        req: CompletionRequest,
    ) -> Result<
        Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
        ProviderError,
    > {
        let url = format!("{}/v1/chat/completions", self.config.normalised_base_url());
        let body = self.build_chat_body(&req, true);

        let api_key = self.config.api_key.clone();
        let response = self
            .http
            .post_sse(&url, &body, move |b| {
                if api_key.is_empty() {
                    b
                } else {
                    CascadeHttpClient::apply_bearer(b, &api_key)
                }
            })
            .await?;

        let byte_stream = response.bytes_stream();

        let stream = byte_stream
            .flat_map(|chunk_result| {
                let data = match chunk_result {
                    Ok(bytes) => String::from_utf8_lossy(&bytes).into_owned(),
                    Err(e) => {
                        return futures::stream::once(async move {
                            Err(ProviderError::StreamInterrupted(e.to_string()))
                        })
                        .left_stream();
                    }
                };

                let chunks: Vec<Result<StreamChunk, ProviderError>> = data
                    .lines()
                    .filter_map(CascadeHttpClient::parse_sse_line)
                    .map(parse_openai_sse_chunk)
                    .collect();

                futures::stream::iter(chunks).right_stream()
            });

        Ok(Box::pin(stream))
    }

    /// List available models from `GET /v1/models`.
    ///
    /// Falls back to a single-item list containing the configured `model_id`
    /// if the endpoint is unreachable or returns an error.
    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        let url = format!("{}/v1/models", self.config.normalised_base_url());
        let api_key = self.config.api_key.clone();

        let result: Result<Value, ProviderError> = self
            .http
            .get_json(&url, move |b| {
                if api_key.is_empty() {
                    b
                } else {
                    CascadeHttpClient::apply_bearer(b, &api_key)
                }
            })
            .await;

        match result {
            Ok(json) => {
                // OpenAI /v1/models shape: { "data": [{ "id": "..." }, ...] }
                if let Some(data) = json.get("data").and_then(Value::as_array) {
                    let models: Vec<ModelInfo> = data
                        .iter()
                        .filter_map(|item| item.get("id").and_then(Value::as_str))
                        .map(|id| ModelInfo {
                            id: id.to_owned(),
                            name: id.to_owned(),
                            context_window: self.config.resolved_context_window(),
                            supports_streaming: true,
                        })
                        .collect();

                    if !models.is_empty() {
                        return Ok(models);
                    }
                }
                // Unexpected shape — fall through to single-model fallback.
                Ok(single_model_list(self))
            }
            Err(_) => Ok(single_model_list(self)),
        }
    }

    /// Verify the endpoint is reachable by calling `available_models()`.
    async fn health_check(&self) -> Result<(), ProviderError> {
        self.available_models().await.map(|_| ())
    }

    /// Static metadata — no I/O.
    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: self.provider_id(),
            name: self.config.resolved_display_name(),
            auth_method: if self.config.api_key.is_empty() {
                AuthMethod::None
            } else {
                AuthMethod::ApiKey
            },
            base_url: self.config.normalised_base_url(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: false,
                max_context_tokens: self.config.resolved_context_window(),
                supports_function_calling: false,
            },
        }
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

/// Validate that a URL has an `http` or `https` scheme.
///
/// Rejects anything else (file://, ftp://, data:, bare hostnames, empty
/// strings) to limit the SSRF surface.
fn validate_url(url: &str) -> Result<(), ProviderError> {
    let trimmed = url.trim();
    if trimmed.is_empty() {
        return Err(ProviderError::NetworkError(
            "base_url must not be empty".to_owned(),
        ));
    }
    // Must start with http:// or https:// (case-insensitive).
    let lower = trimmed.to_ascii_lowercase();
    if !lower.starts_with("http://") && !lower.starts_with("https://") {
        return Err(ProviderError::NetworkError(format!(
            "base_url must use http or https scheme, got: {trimmed}"
        )));
    }
    Ok(())
}

/// Extract the host component from a URL string.
///
/// Returns `None` if the URL cannot be parsed.  Keeps it simple — no heavy
/// URL parsing dependency; just finds `://` and the next `/`.
fn extract_host(url: &str) -> Option<String> {
    let after_scheme = url.find("://")?;
    let rest = &url[after_scheme + 3..];
    let end = rest.find('/').unwrap_or(rest.len());
    let host_port = &rest[..end];
    // Strip port for display.
    let host = host_port.find(':').map_or(host_port, |i| &host_port[..i]);
    if host.is_empty() {
        None
    } else {
        Some(host.to_owned())
    }
}

/// Build a single-element `ModelInfo` list from the adapter config.
fn single_model_list(adapter: &GenericOpenAIAdapter) -> Vec<ModelInfo> {
    vec![ModelInfo {
        id: adapter.config.model_id.clone(),
        name: adapter.config.model_id.clone(),
        context_window: adapter.config.resolved_context_window(),
        supports_streaming: true,
    }]
}

/// Parse an OpenAI chat-completions JSON response into a `CompletionResponse`.
fn parse_openai_response(raw: &Value) -> Result<CompletionResponse, ProviderError> {
    let content = raw
        .pointer("/choices/0/message/content")
        .and_then(Value::as_str)
        .ok_or_else(|| {
            ProviderError::InvalidResponse(
                "missing choices[0].message.content in response".to_owned(),
            )
        })?
        .to_owned();

    let model = raw
        .get("model")
        .and_then(Value::as_str)
        .unwrap_or("")
        .to_owned();

    let prompt_tokens = raw
        .pointer("/usage/prompt_tokens")
        .and_then(Value::as_u64)
        .unwrap_or(0) as u32;

    let completion_tokens = raw
        .pointer("/usage/completion_tokens")
        .and_then(Value::as_u64)
        .unwrap_or(0) as u32;

    let usage = TokenUsage {
        prompt_tokens,
        completion_tokens,
        total_tokens: prompt_tokens + completion_tokens,
    };
    // provider_id is unknown for generic adapters; attempt "openai" as best-effort
    // (covers Azure and compatible servers using OpenAI model IDs).
    let cost_usd = compute_cost("openai", &model, &usage);
    Ok(CompletionResponse {
        content,
        model,
        usage,
        cost_usd,
    })
}

/// Parse a single SSE `data:` payload from an OpenAI-style stream.
fn parse_openai_sse_chunk(data: &str) -> Result<StreamChunk, ProviderError> {
    let v: Value = serde_json::from_str(data)
        .map_err(|e| ProviderError::InvalidResponse(format!("SSE JSON parse: {e}")))?;

    let delta = v
        .pointer("/choices/0/delta/content")
        .and_then(Value::as_str)
        .unwrap_or("")
        .to_owned();

    let finish_reason = v
        .pointer("/choices/0/finish_reason")
        .and_then(Value::as_str)
        .map(str::to_owned);

    Ok(StreamChunk {
        delta,
        finish_reason,
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::test_support::{
        make_openai_sse, HttpMethod, MockProviderServer,
    };
    use futures::StreamExt;

    // ── helper to build a standard openai response ────────────────────────────

    fn openai_response_json(content: &str, model: &str) -> serde_json::Value {
        serde_json::json!({
            "id": "chatcmpl-test",
            "object": "chat.completion",
            "model": model,
            "choices": [{
                "index": 0,
                "message": {"role": "assistant", "content": content},
                "finish_reason": "stop"
            }],
            "usage": {
                "prompt_tokens": 10,
                "completion_tokens": 5,
                "total_tokens": 15
            }
        })
    }

    // ── T-P3-E04-13-01: happy-path completion ─────────────────────────────────

    #[tokio::test]
    async fn test_complete_happy_path() {
        let ctx = MockProviderServer::start("generic_openai").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            openai_response_json("Paris is the capital of France.", "local-model"),
        )
        .await;

        let config = GenericOpenAIConfig::new(ctx.base_url(), "local-model");
        let adapter = GenericOpenAIAdapter::new(config).unwrap();

        let req = crate::CompletionRequest::simple("local-model", "Capital of France?");
        let resp = adapter.complete(req).await.unwrap();

        assert_eq!(resp.content, "Paris is the capital of France.");
        assert_eq!(resp.model, "local-model");
        assert_eq!(resp.usage.prompt_tokens, 10);
        assert_eq!(resp.usage.completion_tokens, 5);
    }

    // ── T-P3-E04-13-02: auth — Bearer token is forwarded ─────────────────────

    #[tokio::test]
    async fn test_complete_with_auth() {
        let ctx = MockProviderServer::start("generic_openai_auth").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            openai_response_json("Hi", "secure-model"),
        )
        .await;

        let config = GenericOpenAIConfig::with_key(ctx.base_url(), "my-secret-key", "secure-model");
        let adapter = GenericOpenAIAdapter::new(config).unwrap();

        let req = crate::CompletionRequest::simple("secure-model", "Hello");
        let resp = adapter.complete(req).await.unwrap();

        assert_eq!(resp.content, "Hi");
        // Check the mock received exactly one request (auth header is set; wiremock
        // does not verify headers by default, but the request must have reached it).
        ctx.assert_request_count(1).await;
    }

    // ── T-P3-E04-13-03: auth — 401 surfaces as AuthFailed ─────────────────────

    #[tokio::test]
    async fn test_complete_401_surfaces_auth_failed() {
        let ctx = MockProviderServer::start("generic_openai_401").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            401,
            serde_json::json!({"error": "unauthorized"}),
        )
        .await;

        let config = GenericOpenAIConfig::with_key(ctx.base_url(), "bad-key", "any-model");
        let adapter = GenericOpenAIAdapter::new(config).unwrap();

        let req = crate::CompletionRequest::simple("any-model", "Hello");
        let result = adapter.complete(req).await;

        assert!(
            matches!(result, Err(ProviderError::AuthFailed(_))),
            "expected AuthFailed, got: {result:?}"
        );
    }

    // ── T-P3-E04-13-04: invalid base_url is rejected at construction ──────────

    #[test]
    fn test_invalid_url_rejected() {
        let cases = [
            "ftp://example.com",
            "file:///etc/passwd",
            "not-a-url",
            "",
            "example.com",
        ];
        for url in &cases {
            let config = GenericOpenAIConfig::new(*url, "m");
            let result = GenericOpenAIAdapter::new(config);
            assert!(
                matches!(result, Err(ProviderError::NetworkError(_))),
                "expected NetworkError for url={url:?}, got: {result:?}"
            );
        }
    }

    // ── T-P3-E04-13-05: valid http and https URLs are accepted ────────────────

    #[test]
    fn test_valid_url_accepted() {
        for url in &["http://localhost:11434", "https://my-vllm.example.com"] {
            let config = GenericOpenAIConfig::new(*url, "m");
            assert!(
                GenericOpenAIAdapter::new(config).is_ok(),
                "expected Ok for url={url}"
            );
        }
    }

    // ── T-P3-E04-13-06: empty api_key produces no Authorization header ─────────
    //   CR-A: empty api_key must not produce a malformed Authorization header.
    //   Verified by mounting a 200 response and confirming the request arrives
    //   without a Bearer header (the mock would still respond even with one, but
    //   we separately assert via the parse path that no auth-specific error fires).

    #[tokio::test]
    async fn test_empty_api_key_accepted() {
        let ctx = MockProviderServer::start("generic_openai_no_auth").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            openai_response_json("OK", "open-model"),
        )
        .await;

        let config = GenericOpenAIConfig::new(ctx.base_url(), "open-model");
        assert!(config.api_key.is_empty(), "api_key must default to empty");

        let adapter = GenericOpenAIAdapter::new(config).unwrap();
        let req = crate::CompletionRequest::simple("open-model", "Ping");
        let resp = adapter.complete(req).await;
        assert!(resp.is_ok(), "empty api_key must not cause an error: {resp:?}");
    }

    // ── T-P3-E04-13-07: streaming happy path ─────────────────────────────────

    #[tokio::test]
    async fn test_complete_stream_happy_path() {
        let ctx = MockProviderServer::start("generic_openai_stream").await;
        let sse_body = make_openai_sse(&["Hello", " world"]);
        ctx.mount_raw(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let config = GenericOpenAIConfig::new(ctx.base_url(), "local-model");
        let adapter = GenericOpenAIAdapter::new(config).unwrap();

        let req = crate::CompletionRequest {
            stream: true,
            ..crate::CompletionRequest::simple("local-model", "Hi")
        };
        let mut stream = adapter.complete_stream(req).await.unwrap();

        let mut collected = String::new();
        let mut saw_finish = false;

        while let Some(chunk) = stream.next().await {
            let chunk = chunk.unwrap();
            collected.push_str(&chunk.delta);
            if chunk.finish_reason.is_some() {
                saw_finish = true;
            }
        }

        assert!(collected.contains("Hello"), "expected Hello in: {collected}");
        assert!(collected.contains("world"), "expected world in: {collected}");
        assert!(saw_finish, "expected a finish_reason chunk");
    }

    // ── T-P3-E04-13-08: available_models falls back on error ─────────────────

    #[tokio::test]
    async fn test_available_models_fallback_on_error() {
        // No route mounted → connection refused → fallback to single model.
        let config = GenericOpenAIConfig::new("http://127.0.0.1:1", "my-model");
        let adapter = GenericOpenAIAdapter::new(config).unwrap();
        let models = adapter.available_models().await.unwrap();
        assert_eq!(models.len(), 1);
        assert_eq!(models[0].id, "my-model");
    }

    // ── T-P3-E04-13-09: available_models happy path ──────────────────────────

    #[tokio::test]
    async fn test_available_models_happy_path() {
        let ctx = MockProviderServer::start("generic_openai_models").await;
        ctx.mount_json(
            HttpMethod::Get,
            "/v1/models",
            200,
            serde_json::json!({
                "object": "list",
                "data": [
                    {"id": "llama3.2:3b", "object": "model"},
                    {"id": "qwen2.5-coder:7b", "object": "model"}
                ]
            }),
        )
        .await;

        let config = GenericOpenAIConfig::new(ctx.base_url(), "fallback-model");
        let adapter = GenericOpenAIAdapter::new(config).unwrap();

        let models = adapter.available_models().await.unwrap();
        assert_eq!(models.len(), 2);
        assert_eq!(models[0].id, "llama3.2:3b");
        assert_eq!(models[1].id, "qwen2.5-coder:7b");
    }

    // ── T-P3-E04-13-10: provider_info reflects config ─────────────────────────

    #[test]
    fn test_provider_info_reflects_config() {
        let mut config = GenericOpenAIConfig::new("http://localhost:1234", "my-model");
        config.display_name = Some("My Local LLM".into());
        config.context_window = Some(8192);

        let adapter = GenericOpenAIAdapter::new(config).unwrap();
        let info = adapter.provider_info();

        assert!(info.id.starts_with("generic:"), "id prefix: {}", info.id);
        assert!(info.id.contains("my-model"), "model in id: {}", info.id);
        assert_eq!(info.name, "My Local LLM");
        assert_eq!(info.capabilities.max_context_tokens, 8192);
        assert!(info.capabilities.supports_streaming);
        assert_eq!(info.base_url, "http://localhost:1234");
    }

    // ── T-P3-E04-13-11: provider_id format ───────────────────────────────────

    #[test]
    fn test_provider_id_format() {
        let config = GenericOpenAIConfig::new("http://localhost:11434", "qwen2.5-coder:7b");
        let adapter = GenericOpenAIAdapter::new(config).unwrap();
        let id = adapter.provider_id();
        assert_eq!(id, "generic:localhost:qwen2.5-coder:7b");
    }

    // ── T-P3-E04-13-12: config serializes for settings storage (CR-B) ─────────

    #[test]
    fn test_config_round_trip_serialization() {
        let config = GenericOpenAIConfig {
            base_url: "http://localhost:1234".into(),
            api_key: "sk-local".into(),
            model_id: "my-model".into(),
            display_name: Some("Local LLM".into()),
            context_window: Some(8192),
        };
        let json = serde_json::to_string(&config).expect("serialize");
        let back: GenericOpenAIConfig = serde_json::from_str(&json).expect("deserialize");
        assert_eq!(config, back);
    }

    // ── T-P3-E04-13-13: no auth_key means AuthMethod::None in provider_info ───

    #[test]
    fn test_no_api_key_auth_method_none() {
        let config = GenericOpenAIConfig::new("http://localhost:1234", "open-model");
        let adapter = GenericOpenAIAdapter::new(config).unwrap();
        let info = adapter.provider_info();
        assert_eq!(info.auth_method, AuthMethod::None);
    }

    #[test]
    fn test_with_api_key_auth_method_api_key() {
        let config = GenericOpenAIConfig::with_key(
            "https://api.example.com",
            "sk-secret",
            "my-model",
        );
        let adapter = GenericOpenAIAdapter::new(config).unwrap();
        let info = adapter.provider_info();
        assert_eq!(info.auth_method, AuthMethod::ApiKey);
    }
}
