//! DeepSeekAdapter — api.deepseek.com/v1 (OpenAI-compatible).
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for DeepSeek using the OpenAI-compatible
//! chat completions endpoint at `api.deepseek.com/v1/chat/completions`.
//! The wire format is identical to the OpenAI API; only the base URL and
//! model identifiers differ.
//!
//! # Inputs / Outputs
//!
//! - `complete`: POST `/v1/chat/completions` with `stream: false`.
//! - `complete_stream`: POST with `stream: true`, parse OpenAI-style SSE.
//! - `available_models`: returns the two current DeepSeek model IDs.
//! - `health_check`: checks credential validity with a minimal request.
//!
//! # Constraints
//!
//! - API key is retrieved from the keychain service `"cascade.deepseek"`.
//! - Model alias mapping:
//!   - `"deepseek-chat"` → deepseek-v3 (general purpose)
//!   - `"deepseek-reasoner"` → deepseek-r1 (chain-of-thought)
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
    types::{CompletionRequest, CompletionResponse, ModelInfo, StreamChunk, TokenUsage},
};

// ── Constants ─────────────────────────────────────────────────────────────────

const DEEPSEEK_BASE_URL: &str = "https://api.deepseek.com";
const KEYCHAIN_SERVICE: &str = "cascade.deepseek";
const PROVIDER_ID: &str = "deepseek";

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
    usage: OaiUsage,
}

#[derive(Debug, Deserialize)]
struct OaiChoice {
    message: OaiMessage,
    finish_reason: Option<String>,
}

#[derive(Debug, Deserialize)]
struct OaiUsage {
    prompt_tokens: u32,
    completion_tokens: u32,
    total_tokens: u32,
}

#[derive(Debug, Deserialize)]
struct OaiStreamChunk {
    choices: Vec<OaiStreamChoice>,
}

#[derive(Debug, Deserialize)]
struct OaiStreamChoice {
    delta: OaiDelta,
    finish_reason: Option<String>,
}

#[derive(Debug, Deserialize)]
struct OaiDelta {
    content: Option<String>,
}

// ── DeepSeekAdapter ───────────────────────────────────────────────────────────

/// Provider adapter for DeepSeek (api.deepseek.com).
///
/// ## Authentication
///
/// Reads the API key from the keychain service `"cascade.deepseek"` via
/// `cascade-keychain`.  The key is fetched on every call — rotation takes
/// effect immediately.
///
/// ## Model IDs
///
/// | Model | Alias | Notes |
/// |---|---|---|
/// | `deepseek-chat` | deepseek-v3 | General-purpose chat |
/// | `deepseek-reasoner` | deepseek-r1 | Chain-of-thought reasoning |
pub struct DeepSeekAdapter {
    http: CascadeHttpClient,
    base_url: String,
    api_key: Option<String>,
}

impl DeepSeekAdapter {
    /// Construct a production adapter pointing at the real DeepSeek API.
    pub fn new() -> Self {
        Self {
            http: CascadeHttpClient::new(),
            base_url: DEEPSEEK_BASE_URL.to_owned(),
            api_key: None,
        }
    }

    /// Construct a test adapter with an injected base URL and key.
    #[cfg(test)]
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
                    "no DeepSeek API key in keychain service '{KEYCHAIN_SERVICE}'"
                ))
            })
    }

    fn build_request(req: &CompletionRequest, stream: bool) -> OaiRequest {
        let mut messages: Vec<OaiMessage> = Vec::new();
        if let Some(ref sys) = req.system {
            messages.push(OaiMessage {
                role: "system".to_owned(),
                content: sys.clone(),
            });
        }
        for msg in &req.messages {
            use crate::types::MessageRole;
            let role = match msg.role {
                MessageRole::System => "system",
                MessageRole::User => "user",
                MessageRole::Assistant => "assistant",
            };
            messages.push(OaiMessage {
                role: role.to_owned(),
                content: msg.content.clone(),
            });
        }
        OaiRequest {
            model: req.model.clone(),
            messages,
            max_tokens: req.max_tokens,
            temperature: req.temperature,
            stream,
        }
    }

    fn completions_url(&self) -> String {
        format!("{}/v1/chat/completions", self.base_url)
    }
}

impl Default for DeepSeekAdapter {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl ProviderAdapter for DeepSeekAdapter {
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        let key = self.api_key()?;
        let body = Self::build_request(&req, false);
        let url = self.completions_url();

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

        let usage = TokenUsage {
            prompt_tokens: resp.usage.prompt_tokens,
            completion_tokens: resp.usage.completion_tokens,
            total_tokens: resp.usage.total_tokens,
        };
        let cost_usd = compute_cost("deepseek", &resp.model, &usage);
        Ok(CompletionResponse {
            content,
            model: resp.model,
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
        let url = self.completions_url();

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
                id: "deepseek-chat".into(),
                name: "DeepSeek Chat (deepseek-v3)".into(),
                context_window: 64_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "deepseek-reasoner".into(),
                name: "DeepSeek Reasoner (deepseek-r1)".into(),
                context_window: 64_000,
                supports_streaming: true,
            },
        ])
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        let req = CompletionRequest::simple("deepseek-chat", "ping");
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
            name: "DeepSeek".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: DEEPSEEK_BASE_URL.into(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: false,
                max_context_tokens: 64_000,
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

    #[tokio::test]
    async fn deepseek_complete_happy_path() {
        let ctx = MockProviderServer::start("deepseek").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            fixture_json("deepseek_complete"),
        )
        .await;

        let adapter = DeepSeekAdapter::with_base_url(ctx.base_url(), "test-key");
        let req = CompletionRequest::simple("deepseek-chat", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("should succeed");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(!resp.model.is_empty(), "model must not be empty");
        assert!(resp.usage.prompt_tokens > 0, "prompt_tokens must be positive");
    }

    #[tokio::test]
    async fn deepseek_complete_auth_error() {
        let ctx = MockProviderServer::start("deepseek").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            401,
            serde_json::json!({"error": {"message": "Invalid API key", "type": "authentication_error"}}),
        )
        .await;

        let adapter = DeepSeekAdapter::with_base_url(ctx.base_url(), "bad-key");
        let req = CompletionRequest::simple("deepseek-chat", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    #[test]
    fn deepseek_provider_info() {
        let adapter = DeepSeekAdapter::new();
        let info = adapter.provider_info();
        assert_eq!(info.id, "deepseek");
        assert!(info.capabilities.supports_streaming);
    }

    #[tokio::test]
    async fn deepseek_available_models() {
        let adapter = DeepSeekAdapter::new();
        let models = adapter.available_models().await.unwrap();
        assert_eq!(models.len(), 2);
        assert!(models.iter().any(|m| m.id == "deepseek-chat"));
        assert!(models.iter().any(|m| m.id == "deepseek-reasoner"));
    }
}
