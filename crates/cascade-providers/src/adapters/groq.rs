//! GroqAdapter — api.groq.com/openai/v1 (OpenAI-compatible, ultra-low latency).
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for Groq using the OpenAI-compatible chat
//! completions endpoint at `api.groq.com/openai/v1/chat/completions`.
//! Groq is optimised for ultra-low latency inference via custom LPU hardware,
//! making it suitable for real-time streaming use cases.
//!
//! # Inputs / Outputs
//!
//! - `complete`: POST `/openai/v1/chat/completions` with `stream: false`.
//! - `complete_stream`: POST with `stream: true`, parse OpenAI-style SSE.
//! - `available_models`: returns Groq's four primary chat models.
//! - `health_check`: verifies credential validity with a minimal request.
//!
//! # Constraints
//!
//! - API key is retrieved from the keychain service `"cascade.groq"`.
//! - Auth: `Authorization: Bearer <api_key>` (standard OpenAI-compat auth).
//! - No extra provider-specific headers are required.
//! - `ProviderCapabilities::supports_streaming = true`; Groq's latency profile
//!   makes streaming the preferred mode.
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

const GROQ_BASE_URL: &str = "https://api.groq.com/openai/v1";
const KEYCHAIN_SERVICE: &str = "cascade.groq";
const PROVIDER_ID: &str = "groq";

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

// ── GroqAdapter ───────────────────────────────────────────────────────────────

/// `ProviderAdapter` for Groq.
///
/// Targets `api.groq.com/openai/v1` with `Authorization: Bearer` auth.
/// Groq's LPU architecture provides ultra-low latency streaming — the
/// `ProviderCapabilities` struct reflects this with high `max_context_tokens`
/// and `supports_streaming: true`.
pub struct GroqAdapter {
    api_key: String,
    base_url: String,
    http: CascadeHttpClient,
}

impl GroqAdapter {
    /// Construct using the provided API key and default base URL.
    pub fn new(api_key: impl Into<String>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: GROQ_BASE_URL.to_string(),
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
}

impl Default for GroqAdapter {
    fn default() -> Self {
        Self::from_keychain().unwrap_or_else(|_| Self::new(String::new()))
    }
}

#[async_trait]
impl ProviderAdapter for GroqAdapter {
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
        let cost_usd = compute_cost("groq", &resp.model, &token_usage);
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
        // Groq model list as of 2026 — updated when new models are added.
        Ok(vec![
            ModelInfo {
                id: "llama-3.1-70b-versatile".into(),
                name: "Llama 3.1 70B Versatile".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "llama-3.1-8b-instant".into(),
                name: "Llama 3.1 8B Instant".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "mixtral-8x7b-32768".into(),
                name: "Mixtral 8x7B".into(),
                context_window: 32_768,
                supports_streaming: true,
            },
            ModelInfo {
                id: "gemma2-9b-it".into(),
                name: "Gemma2 9B IT".into(),
                context_window: 8_192,
                supports_streaming: true,
            },
        ])
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        let req = CompletionRequest::simple("llama-3.1-8b-instant", "ping");
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
            name: "Groq".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: GROQ_BASE_URL.into(),
            capabilities: ProviderCapabilities {
                // Groq's LPU hardware makes streaming the preferred mode.
                supports_streaming: true,
                supports_vision: false,
                // Largest context window in the standard Groq lineup.
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
    use crate::test_helpers::test_support::{
        fixture_json, make_openai_sse, HttpMethod, MockProviderServer,
    };
    use futures::StreamExt as _;

    #[tokio::test]
    async fn groq_complete_happy_path() {
        let ctx = MockProviderServer::start("groq").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            200,
            fixture_json("groq_complete"),
        )
        .await;

        let adapter = GroqAdapter::with_base_url(ctx.base_url(), "gsk-test-key");
        let req =
            CompletionRequest::simple("llama-3.1-70b-versatile", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("complete failed");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(!resp.model.is_empty(), "model must not be empty");
        assert!(
            resp.usage.prompt_tokens > 0,
            "prompt_tokens must be positive"
        );
    }

    #[tokio::test]
    async fn groq_complete_auth_error() {
        let ctx = MockProviderServer::start("groq_auth").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            401,
            serde_json::json!({"error": {"message": "Invalid API Key"}}),
        )
        .await;

        let adapter = GroqAdapter::with_base_url(ctx.base_url(), "bad-key");
        let req = CompletionRequest::simple("llama-3.1-8b-instant", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    #[tokio::test]
    async fn groq_stream_yields_chunks() {
        let ctx = MockProviderServer::start("groq_stream").await;
        let sse_body = make_openai_sse(&["Hello", " world"]);
        ctx.mount_raw(
            HttpMethod::Post,
            "/chat/completions",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let adapter = GroqAdapter::with_base_url(ctx.base_url(), "gsk-key");
        let req = CompletionRequest::simple("llama-3.1-70b-versatile", "ping");
        let stream = adapter.complete_stream(req).await.expect("stream failed");

        let chunks: Vec<_> = stream.collect().await;
        let non_err: Vec<_> = chunks.into_iter().filter_map(Result::ok).collect();
        assert!(!non_err.is_empty(), "expected at least one chunk");
    }

    #[test]
    fn groq_provider_info() {
        let adapter = GroqAdapter::new("key");
        let info = adapter.provider_info();
        assert_eq!(info.id, "groq");
        assert!(info.capabilities.supports_streaming);
        assert_eq!(info.base_url, GROQ_BASE_URL);
        // Groq is ultra-low latency — streaming must always be enabled.
        assert!(
            info.capabilities.supports_streaming,
            "Groq must always support streaming"
        );
    }

    #[tokio::test]
    async fn groq_available_models_spec_compliant() {
        let adapter = GroqAdapter::new("key");
        let models = adapter.available_models().await.unwrap();

        // Spec requires exactly these 4 models.
        assert!(
            models.iter().any(|m| m.id == "llama-3.1-70b-versatile"),
            "missing llama-3.1-70b-versatile"
        );
        assert!(
            models.iter().any(|m| m.id == "llama-3.1-8b-instant"),
            "missing llama-3.1-8b-instant"
        );
        assert!(
            models.iter().any(|m| m.id == "mixtral-8x7b-32768"),
            "missing mixtral-8x7b-32768"
        );
        assert!(
            models.iter().any(|m| m.id == "gemma2-9b-it"),
            "missing gemma2-9b-it"
        );

        // Verify context windows match spec.
        let llama70 = models
            .iter()
            .find(|m| m.id == "llama-3.1-70b-versatile")
            .unwrap();
        assert_eq!(
            llama70.context_window, 128_000,
            "llama-70b context_window mismatch"
        );

        let llama8 = models
            .iter()
            .find(|m| m.id == "llama-3.1-8b-instant")
            .unwrap();
        assert_eq!(
            llama8.context_window, 128_000,
            "llama-8b context_window mismatch"
        );

        let mixtral = models
            .iter()
            .find(|m| m.id == "mixtral-8x7b-32768")
            .unwrap();
        assert_eq!(
            mixtral.context_window, 32_768,
            "mixtral context_window mismatch"
        );

        let gemma = models.iter().find(|m| m.id == "gemma2-9b-it").unwrap();
        assert_eq!(gemma.context_window, 8_192, "gemma context_window mismatch");
    }
}
