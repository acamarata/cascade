//! TogetherAdapter — api.together.xyz/v1 (OpenAI-compatible).
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for Together.ai using the OpenAI-compatible
//! chat completions endpoint at `api.together.xyz/v1/chat/completions`.
//! Together.ai hosts open-source models including Llama, Mistral, and others.
//!
//! # Inputs / Outputs
//!
//! - `complete`: POST `/v1/chat/completions` with `stream: false`.
//! - `complete_stream`: POST with `stream: true`, parse OpenAI-style SSE.
//!   Returns `ProviderError::UnsupportedTaskType` for models in the streaming
//!   denylist (e.g. legacy GPT-J variants not supported for streaming).
//! - `available_models`: returns the top 5 popular Together.ai model IDs.
//! - `health_check`: verifies credential validity with a minimal request.
//!
//! # Constraints
//!
//! - API key is retrieved from the keychain service `"cascade.together"`.
//! - Auth: `Authorization: Bearer <api_key>` (no extra provider-specific headers).
//! - `supports_streaming` is `false` for models in `STREAMING_DENYLIST`.
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
    types::{
        CompletionRequest, CompletionResponse, MessageRole, ModelInfo, StreamChunk, TokenUsage,
    },
};

// ── Constants ─────────────────────────────────────────────────────────────────

const TOGETHER_BASE_URL: &str = "https://api.together.xyz/v1";
const KEYCHAIN_SERVICE: &str = "cascade.together";
const PROVIDER_ID: &str = "together";

/// Models that Together.ai does not support for streaming.
///
/// These legacy models are served over blocking HTTP only; `complete_stream()`
/// returns `UnsupportedTaskType` when one of these model IDs is requested.
const STREAMING_DENYLIST: &[&str] = &[
    "EleutherAI/gpt-j-6b",
    "EleutherAI/gpt-neox-20b",
    "togethercomputer/GPT-JT-6B-v1",
    "togethercomputer/GPT-NeoXT-Chat-Base-20B",
];

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

// ── TogetherAdapter ───────────────────────────────────────────────────────────

/// `ProviderAdapter` for Together.ai.
///
/// Targets `api.together.xyz/v1` with `Authorization: Bearer` auth.
/// Streaming is blocked for models in `STREAMING_DENYLIST`.
pub struct TogetherAdapter {
    api_key: String,
    base_url: String,
    http: CascadeHttpClient,
}

impl TogetherAdapter {
    /// Construct using the provided API key and default base URL.
    pub fn new(api_key: impl Into<String>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: TOGETHER_BASE_URL.to_string(),
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
    pub fn from_keychain() -> Result<Self, ProviderError> {
        let key = cascade_keychain::platform_keychain()
            .get_key(KEYCHAIN_SERVICE, PROVIDER_ID)
            .map_err(|_| ProviderError::CredentialNotFound(KEYCHAIN_SERVICE.to_string()))?;
        Ok(Self::new(key))
    }

    fn completions_url(&self) -> String {
        format!("{}/chat/completions", self.base_url.trim_end_matches('/'))
    }

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

    /// Returns `true` when the model is on the streaming denylist.
    fn streaming_denied(model_id: &str) -> bool {
        STREAMING_DENYLIST.contains(&model_id)
    }
}

impl Default for TogetherAdapter {
    fn default() -> Self {
        Self::from_keychain().unwrap_or_else(|_| Self::new(String::new()))
    }
}

#[async_trait]
impl ProviderAdapter for TogetherAdapter {
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        let body = Self::build_request(&req, false);
        let url = self.completions_url();
        let key = self.api_key.clone();

        debug!(provider = PROVIDER_ID, model = %req.model, "sending completion request");

        let resp: OaiResponse = self
            .http
            .post_json(&url, &body, move |b| {
                CascadeHttpClient::apply_bearer(b, &key)
            })
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
        let cost_usd = compute_cost("together", &resp.model, &token_usage);
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
    ) -> Result<Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>, ProviderError>
    {
        // Reject models that Together.ai does not support for streaming.
        if Self::streaming_denied(&req.model) {
            return Err(ProviderError::UnsupportedTaskType(format!(
                "model '{}' does not support streaming on Together.ai",
                req.model
            )));
        }

        let body = Self::build_request(&req, true);
        let url = self.completions_url();
        let key = self.api_key.clone();

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
                id: "meta-llama/Llama-3.1-70B-Instruct-Turbo".into(),
                name: "Llama 3.1 70B Instruct Turbo".into(),
                context_window: 131_072,
                supports_streaming: true,
            },
            ModelInfo {
                id: "meta-llama/Llama-3.1-8B-Instruct-Turbo".into(),
                name: "Llama 3.1 8B Instruct Turbo".into(),
                context_window: 131_072,
                supports_streaming: true,
            },
            ModelInfo {
                id: "mistralai/Mixtral-8x7B-Instruct-v0.1".into(),
                name: "Mixtral 8x7B Instruct".into(),
                context_window: 32_768,
                supports_streaming: true,
            },
            ModelInfo {
                id: "Qwen/Qwen2.5-72B-Instruct-Turbo".into(),
                name: "Qwen2.5 72B Instruct Turbo".into(),
                context_window: 32_768,
                supports_streaming: true,
            },
            ModelInfo {
                id: "deepseek-ai/DeepSeek-R1".into(),
                name: "DeepSeek R1".into(),
                context_window: 163_840,
                supports_streaming: true,
            },
        ])
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        let req = CompletionRequest::simple("meta-llama/Llama-3.1-8B-Instruct-Turbo", "ping");
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
            name: "Together.ai".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: TOGETHER_BASE_URL.into(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: false,
                max_context_tokens: 131_072,
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
    async fn together_complete_happy_path() {
        let ctx = MockProviderServer::start("together").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            200,
            fixture_json("together_complete"),
        )
        .await;

        let adapter = TogetherAdapter::with_base_url(ctx.base_url(), "ta-test-key");
        let req = CompletionRequest::simple(
            "meta-llama/Llama-3.1-70B-Instruct-Turbo",
            "What is the capital of France?",
        );
        let resp = adapter.complete(req).await.expect("complete failed");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(!resp.model.is_empty(), "model must not be empty");
        assert!(
            resp.usage.prompt_tokens > 0,
            "prompt_tokens must be positive"
        );
    }

    #[tokio::test]
    async fn together_complete_auth_error() {
        let ctx = MockProviderServer::start("together_auth").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            401,
            serde_json::json!({"error": {"message": "Invalid API key"}}),
        )
        .await;

        let adapter = TogetherAdapter::with_base_url(ctx.base_url(), "bad-key");
        let req = CompletionRequest::simple("meta-llama/Llama-3.1-8B-Instruct-Turbo", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    #[tokio::test]
    async fn together_stream_yields_chunks() {
        let ctx = MockProviderServer::start("together_stream").await;
        let sse_body = make_openai_sse(&["Hello", " world"]);
        ctx.mount_raw(
            HttpMethod::Post,
            "/chat/completions",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let adapter = TogetherAdapter::with_base_url(ctx.base_url(), "ta-key");
        let req = CompletionRequest::simple("meta-llama/Llama-3.1-70B-Instruct-Turbo", "ping");
        let stream = adapter.complete_stream(req).await.expect("stream failed");

        let chunks: Vec<_> = stream.collect().await;
        let non_err: Vec<_> = chunks.into_iter().filter_map(Result::ok).collect();
        assert!(!non_err.is_empty(), "expected at least one chunk");
    }

    #[tokio::test]
    async fn together_stream_denylist_returns_unsupported() {
        let adapter = TogetherAdapter::new("key");
        let req = CompletionRequest::simple("EleutherAI/gpt-j-6b", "test");
        let result = adapter.complete_stream(req).await;

        assert!(result.is_err(), "expected Err for denylisted model");
        if let Err(err) = result {
            assert!(
                matches!(err, ProviderError::UnsupportedTaskType(_)),
                "expected UnsupportedTaskType, got {err:?}"
            );
        }
    }

    #[test]
    fn together_streaming_denied_matches_denylist() {
        assert!(TogetherAdapter::streaming_denied("EleutherAI/gpt-j-6b"));
        assert!(TogetherAdapter::streaming_denied("EleutherAI/gpt-neox-20b"));
        assert!(!TogetherAdapter::streaming_denied(
            "meta-llama/Llama-3.1-70B-Instruct-Turbo"
        ));
    }

    #[test]
    fn together_provider_info() {
        let adapter = TogetherAdapter::new("key");
        let info = adapter.provider_info();
        assert_eq!(info.id, "together");
        assert!(info.capabilities.supports_streaming);
        assert_eq!(info.base_url, TOGETHER_BASE_URL);
    }

    #[tokio::test]
    async fn together_available_models_returns_five() {
        let adapter = TogetherAdapter::new("key");
        let models = adapter.available_models().await.unwrap();
        assert_eq!(models.len(), 5, "expected 5 models, got {}", models.len());
        assert!(models.iter().any(|m| m.id.contains("Llama")));
    }
}
