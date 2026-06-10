//! OpenRouterAdapter — api.openrouter.ai/api/v1 (OpenAI-compatible).
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for OpenRouter using the OpenAI-compatible
//! chat completions endpoint at `api.openrouter.ai/api/v1/chat/completions`.
//! OpenRouter is a unified API gateway that routes requests to hundreds of
//! upstream models from different providers.
//!
//! # Inputs / Outputs
//!
//! - `complete`: POST `/api/v1/chat/completions` with `stream: false`.
//! - `complete_stream`: POST with `stream: true`, parse OpenAI-style SSE.
//! - `available_models`: returns the top 5 popular OpenRouter model IDs.
//! - `health_check`: verifies credential validity with a minimal request.
//!
//! # Constraints
//!
//! - API key is retrieved from the keychain service `"cascade.openrouter"`.
//! - OpenRouter requires `HTTP-Referer` and `X-Title` headers per its
//!   documentation (https://openrouter.ai/docs#quick-start).
//! - Model IDs use the `"provider/model"` format (e.g. `"openai/gpt-4o"`).
//! - No credential value is ever written to a log line or error message.

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

const OPENROUTER_BASE_URL: &str = "https://api.openrouter.ai/api/v1";
const KEYCHAIN_SERVICE: &str = "cascade.openrouter";
const PROVIDER_ID: &str = "openrouter";

/// Package version for the required `HTTP-Referer` and `X-Title` headers.
const PKG_VERSION: &str = env!("CARGO_PKG_VERSION");

// ── Wire types (OpenAI-compatible) ────────────────────────────────────────────

#[derive(Debug, Serialize)]
struct OaiRequest {
    model: String,
    messages: Vec<OaiMessage>,
    #[serde(skip_serializing_if = "Option::is_none")]
    max_tokens: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    temperature: Option<f32>,
    stream: bool,
}

#[derive(Debug, Serialize, Deserialize)]
struct OaiMessage {
    role: String,
    content: String,
}

#[derive(Debug, Deserialize)]
struct OaiResponse {
    model: String,
    choices: Vec<OaiChoice>,
    #[serde(default)]
    usage: Option<OaiUsage>,
}

#[derive(Debug, Deserialize)]
struct OaiChoice {
    message: OaiChoiceMessage,
}

#[derive(Debug, Deserialize)]
struct OaiChoiceMessage {
    #[serde(default)]
    content: String,
}

#[derive(Debug, Deserialize)]
struct OaiUsage {
    #[serde(default)]
    prompt_tokens: u32,
    #[serde(default)]
    completion_tokens: u32,
    #[serde(default)]
    total_tokens: u32,
}

#[derive(Debug, Deserialize)]
struct OaiStreamChunk {
    choices: Vec<OaiStreamChoice>,
}

#[derive(Debug, Deserialize)]
struct OaiStreamChoice {
    delta: OaiStreamDelta,
    #[serde(default)]
    finish_reason: Option<String>,
}

#[derive(Debug, Deserialize, Default)]
struct OaiStreamDelta {
    #[serde(default)]
    content: Option<String>,
}

// ── OpenRouterAdapter ─────────────────────────────────────────────────────────

/// `ProviderAdapter` for OpenRouter.
///
/// Routes requests to `api.openrouter.ai/api/v1` and injects the required
/// `HTTP-Referer` and `X-Title` headers per OpenRouter documentation.
pub struct OpenRouterAdapter {
    api_key: String,
    base_url: String,
    http: CascadeHttpClient,
}

impl OpenRouterAdapter {
    /// Construct using the provided API key and default base URL.
    pub fn new(api_key: impl Into<String>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: OPENROUTER_BASE_URL.to_string(),
            http: CascadeHttpClient::new(),
        }
    }

    /// Construct with a custom base URL (used in tests with a mock server).
    pub fn with_base_url(base_url: impl Into<String>, api_key: impl Into<String>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: base_url.into(),
            http: CascadeHttpClient::new(),
        }
    }

    /// Load the API key from the OS keychain.
    ///
    /// Returns `ProviderError::CredentialNotFound` when the key is absent.
    pub fn from_keychain() -> Result<Self, ProviderError> {
        let key = cascade_keychain::platform_keychain()
            .get_key(KEYCHAIN_SERVICE, PROVIDER_ID)
            .map_err(|_| ProviderError::CredentialNotFound(KEYCHAIN_SERVICE.to_string()))?;
        Ok(Self::new(key))
    }

    fn completions_url(&self) -> String {
        format!("{}/chat/completions", self.base_url.trim_end_matches('/'))
    }

    /// Build the OpenAI-compatible request body.
    fn build_request(req: &CompletionRequest, stream: bool) -> OaiRequest {
        let messages = req
            .messages
            .iter()
            .map(|m| OaiMessage {
                role: match m.role {
                    MessageRole::System => "system",
                    MessageRole::User => "user",
                    MessageRole::Assistant => "assistant",
                }
                .to_owned(),
                content: m.content.clone(),
            })
            .collect();

        OaiRequest {
            model: req.model.clone(),
            messages,
            max_tokens: req.max_tokens,
            temperature: req.temperature,
            stream,
        }
    }

    /// Build the auth closure with OpenRouter-required extra headers.
    ///
    /// `HTTP-Referer`: identifies the application making the request.
    /// `X-Title`:      human-readable name shown in OpenRouter's dashboard.
    fn make_auth_fn(key: String) -> impl Fn(reqwest::RequestBuilder) -> reqwest::RequestBuilder {
        move |b| {
            CascadeHttpClient::apply_bearer(b, &key)
                .header(
                    "HTTP-Referer",
                    "https://github.com/acamarata/cascade",
                )
                .header("X-Title", format!("Cascade v{PKG_VERSION}"))
        }
    }
}

impl Default for OpenRouterAdapter {
    fn default() -> Self {
        Self::from_keychain().unwrap_or_else(|_| Self::new(String::new()))
    }
}

#[async_trait]
impl ProviderAdapter for OpenRouterAdapter {
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        let body = Self::build_request(&req, false);
        let url = self.completions_url();
        let key = self.api_key.clone();

        debug!(provider = PROVIDER_ID, model = %req.model, "sending completion request");

        let resp: OaiResponse = self
            .http
            .post_json(&url, &body, Self::make_auth_fn(key))
            .await?;

        let content = resp
            .choices
            .into_iter()
            .next()
            .map(|c| c.message.content)
            .unwrap_or_default();

        let usage = resp.usage.unwrap_or(OaiUsage {
            prompt_tokens: 0,
            completion_tokens: 0,
            total_tokens: 0,
        });

        let token_usage = TokenUsage {
            prompt_tokens: usage.prompt_tokens,
            completion_tokens: usage.completion_tokens,
            total_tokens: usage.total_tokens,
        };
        let cost_usd = compute_cost("openrouter", &resp.model, &token_usage);
        Ok(CompletionResponse {
            content,
            model: resp.model,
            usage: token_usage,
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
        let body = Self::build_request(&req, true);
        let url = self.completions_url();
        let key = self.api_key.clone();

        let response = self
            .http
            .post_sse(&url, &body, Self::make_auth_fn(key))
            .await?;

        // Collect the full SSE body then parse lines.
        // (Production streaming via bytes_stream is tracked in T-P3-E04-08.)
        let bytes = response
            .bytes()
            .await
            .map_err(|e| ProviderError::NetworkError(e.to_string()))?;

        let text = String::from_utf8_lossy(&bytes).into_owned();

        let chunks: Vec<Result<StreamChunk, ProviderError>> = text
            .lines()
            .filter_map(CascadeHttpClient::parse_sse_line)
            .filter_map(|payload| {
                let chunk: OaiStreamChunk = serde_json::from_str(payload).ok()?;
                let choice = chunk.choices.into_iter().next()?;
                let delta = choice.delta.content.unwrap_or_default();
                Some(Ok(StreamChunk {
                    delta,
                    finish_reason: choice.finish_reason,
                }))
            })
            .collect();

        Ok(Box::pin(futures::stream::iter(chunks)))
    }

    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![
            ModelInfo {
                id: "openai/gpt-4o".into(),
                name: "GPT-4o (via OpenRouter)".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "anthropic/claude-3.5-sonnet".into(),
                name: "Claude 3.5 Sonnet (via OpenRouter)".into(),
                context_window: 200_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "meta-llama/llama-3.1-70b-instruct".into(),
                name: "Llama 3.1 70B Instruct (via OpenRouter)".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "google/gemini-2.0-flash-001".into(),
                name: "Gemini 2.0 Flash (via OpenRouter)".into(),
                context_window: 1_048_576,
                supports_streaming: true,
            },
            ModelInfo {
                id: "deepseek/deepseek-r1".into(),
                name: "DeepSeek R1 (via OpenRouter)".into(),
                context_window: 163_840,
                supports_streaming: true,
            },
        ])
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        let req = CompletionRequest::simple("openai/gpt-4o-mini", "ping");
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
            name: "OpenRouter".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: OPENROUTER_BASE_URL.into(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: true,
                max_context_tokens: 200_000,
                supports_function_calling: true,
            },
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::test_support::{
        fixture_json, make_openai_sse, HttpMethod, MockProviderServer,
    };
    use futures::StreamExt as _;

    #[tokio::test]
    async fn openrouter_complete_happy_path() {
        let ctx = MockProviderServer::start("openrouter").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            200,
            fixture_json("openrouter_complete"),
        )
        .await;

        let adapter = OpenRouterAdapter::with_base_url(ctx.base_url(), "or-test-key");
        let req = CompletionRequest::simple("openai/gpt-4o", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("complete failed");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(!resp.model.is_empty(), "model must not be empty");
        assert!(resp.usage.prompt_tokens > 0, "prompt_tokens must be positive");
    }

    #[tokio::test]
    async fn openrouter_complete_auth_error() {
        let ctx = MockProviderServer::start("openrouter_auth").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            401,
            serde_json::json!({"error": {"message": "Invalid API key"}}),
        )
        .await;

        let adapter = OpenRouterAdapter::with_base_url(ctx.base_url(), "bad-key");
        let req = CompletionRequest::simple("openai/gpt-4o", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    #[tokio::test]
    async fn openrouter_stream_yields_chunks() {
        let ctx = MockProviderServer::start("openrouter_stream").await;
        let sse_body = make_openai_sse(&["Hello", " world"]);
        ctx.mount_raw(
            HttpMethod::Post,
            "/chat/completions",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let adapter = OpenRouterAdapter::with_base_url(ctx.base_url(), "or-key");
        let req = CompletionRequest::simple("openai/gpt-4o", "ping");
        let stream = adapter.complete_stream(req).await.expect("stream failed");

        let chunks: Vec<_> = stream.collect().await;
        let non_err: Vec<_> = chunks.into_iter().filter_map(Result::ok).collect();
        assert!(!non_err.is_empty(), "expected at least one chunk");
    }

    #[test]
    fn openrouter_provider_info() {
        let adapter = OpenRouterAdapter::new("key");
        let info = adapter.provider_info();
        assert_eq!(info.id, "openrouter");
        assert!(info.capabilities.supports_streaming);
        assert_eq!(info.base_url, OPENROUTER_BASE_URL);
    }

    #[tokio::test]
    async fn openrouter_available_models_returns_five() {
        let adapter = OpenRouterAdapter::new("key");
        let models = adapter.available_models().await.unwrap();
        assert_eq!(models.len(), 5, "expected 5 models, got {}", models.len());
        assert!(models.iter().any(|m| m.id.contains("gpt-4o")));
        assert!(models.iter().any(|m| m.id.contains("claude")));
    }
}
