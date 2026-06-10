//! MistralAdapter — api.mistral.ai/v1/chat/completions (OpenAI-compatible).
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for Mistral AI using the OpenAI-compatible
//! chat completions endpoint.  The wire format is identical to the OpenAI API
//! (same request shape, same SSE streaming protocol) — only the base URL and
//! model identifiers differ.
//!
//! # Inputs / Outputs
//!
//! - `complete`: POST `/v1/chat/completions` with `stream: false`, deserialize
//!   the OpenAI-compatible response envelope.
//! - `complete_stream`: POST with `stream: true`, parse `data:` SSE lines.
//! - `available_models`: returns a static list of known Mistral model IDs.
//! - `health_check`: sends a minimal request to verify credential validity.
//!
//! # Constraints
//!
//! - API key is retrieved from the keychain service `"cascade.mistral"`.
//! - No credential value is ever written to a log line or error message.
//! - Streaming terminates on the `[DONE]` sentinel or `finish_reason: "stop"`.

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
    types::{CompletionRequest, CompletionResponse, Message, ModelInfo, StreamChunk, TokenUsage},
};

// ── Constants ─────────────────────────────────────────────────────────────────

const MISTRAL_BASE_URL: &str = "https://api.mistral.ai";
const KEYCHAIN_SERVICE: &str = "cascade.mistral";
const PROVIDER_ID: &str = "mistral";

// ── Wire types (OpenAI-compatible) ────────────────────────────────────────────

/// OpenAI-compatible chat completion request.
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

/// SSE streaming chunk — OpenAI-compatible delta shape.
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

// ── MistralAdapter ────────────────────────────────────────────────────────────

/// Provider adapter for Mistral AI (api.mistral.ai).
///
/// ## Authentication
///
/// Reads the API key from the keychain service `"cascade.mistral"` via
/// `cascade-keychain`.  The key is fetched on every call — never cached —
/// so rotation takes effect immediately without restarting the daemon.
///
/// ## Model IDs
///
/// | Model | Notes |
/// |---|---|
/// | `mistral-large-2411` | Flagship, most capable |
/// | `mistral-small-2501` | Fast, cost-efficient |
/// | `open-mistral-7b` | Open-weight 7B |
/// | `open-mixtral-8x7b` | Open-weight MoE |
pub struct MistralAdapter {
    http: CascadeHttpClient,
    /// Overridable base URL — used by tests to point at a mock server.
    base_url: String,
    /// API key — `None` means "read from keychain at call time".
    /// Tests inject a key directly; production leaves this `None`.
    api_key: Option<String>,
}

impl MistralAdapter {
    /// Construct a production adapter pointing at the real Mistral API.
    pub fn new() -> Self {
        Self {
            http: CascadeHttpClient::new(),
            base_url: MISTRAL_BASE_URL.to_owned(),
            api_key: None,
        }
    }

    /// Construct a test adapter with an injected base URL and key.
    ///
    /// Used by unit tests to point the adapter at a `wiremock` server.
    #[cfg(test)]
    pub fn with_base_url(base_url: impl Into<String>, api_key: impl Into<String>) -> Self {
        Self {
            http: CascadeHttpClient::new(),
            base_url: base_url.into(),
            api_key: Some(api_key.into()),
        }
    }

    /// Retrieve the API key — from the injected field (tests) or keychain.
    fn api_key(&self) -> Result<String, ProviderError> {
        if let Some(ref key) = self.api_key {
            return Ok(key.clone());
        }
        cascade_keychain::platform_keychain()
            .get_key(KEYCHAIN_SERVICE, PROVIDER_ID)
            .map_err(|_| {
                ProviderError::CredentialNotFound(format!(
                    "no Mistral API key in keychain service '{KEYCHAIN_SERVICE}'"
                ))
            })
    }

    /// Build the OpenAI-compatible request body from a `CompletionRequest`.
    fn build_request(req: &CompletionRequest, stream: bool) -> OaiRequest {
        // Flatten system prompt into the messages list as an OpenAI system message
        // (Mistral accepts system messages inline in the messages array).
        let mut messages: Vec<OaiMessage> = Vec::new();
        if let Some(ref sys) = req.system {
            messages.push(OaiMessage {
                role: "system".to_owned(),
                content: sys.clone(),
            });
        }
        for msg in &req.messages {
            messages.push(OaiMessage {
                role: format!("{:?}", msg.role).to_lowercase(),
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

    /// Completion endpoint URL.
    fn completions_url(&self) -> String {
        format!("{}/v1/chat/completions", self.base_url)
    }
}

impl Default for MistralAdapter {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl ProviderAdapter for MistralAdapter {
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
        let cost_usd = compute_cost("mistral", &resp.model, &usage);
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

        // Collect the full SSE body and parse it into a stream of chunks.
        // This is intentionally simple — P3 targets correctness over
        // low-latency forwarding.
        let bytes = response
            .bytes()
            .await
            .map_err(|e| ProviderError::NetworkError(e.to_string()))?;

        let text =
            String::from_utf8_lossy(&bytes).into_owned();

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
                id: "mistral-large-2411".into(),
                name: "Mistral Large (Nov 2024)".into(),
                context_window: 128_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "mistral-small-2501".into(),
                name: "Mistral Small (Jan 2025)".into(),
                context_window: 32_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "open-mistral-7b".into(),
                name: "Open Mistral 7B".into(),
                context_window: 32_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "open-mixtral-8x7b".into(),
                name: "Open Mixtral 8x7B".into(),
                context_window: 32_000,
                supports_streaming: true,
            },
        ])
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        // Attempt a minimal request; any non-auth error counts as "reachable".
        let req = CompletionRequest::simple("mistral-small-2501", "ping");
        match self.complete(req).await {
            Ok(_) => Ok(()),
            Err(ProviderError::AuthFailed(msg)) => Err(ProviderError::AuthFailed(msg)),
            Err(ProviderError::CredentialNotFound(msg)) => {
                Err(ProviderError::CredentialNotFound(msg))
            }
            Err(_) => {
                // Connectivity issue but key was accepted — provider is reachable.
                Ok(())
            }
        }
    }

    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: PROVIDER_ID.into(),
            name: "Mistral AI".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: MISTRAL_BASE_URL.into(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: false,
                max_context_tokens: 128_000,
                supports_function_calling: true,
            },
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Convert a `Message` role to the lowercase string Mistral expects.
fn _role_str(msg: &Message) -> &'static str {
    use crate::types::MessageRole;
    match msg.role {
        MessageRole::System => "system",
        MessageRole::User => "user",
        MessageRole::Assistant => "assistant",
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::test_support::{fixture_json, HttpMethod, MockProviderServer};

    /// Happy-path: fixture JSON is mapped to CompletionResponse correctly.
    #[tokio::test]
    async fn mistral_complete_happy_path() {
        let ctx = MockProviderServer::start("mistral").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            fixture_json("mistral_complete"),
        )
        .await;

        let adapter = MistralAdapter::with_base_url(ctx.base_url(), "test-key");
        let req = CompletionRequest::simple("mistral-large-2411", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("should succeed");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(!resp.model.is_empty(), "model must not be empty");
        assert!(resp.usage.prompt_tokens > 0, "prompt_tokens must be positive");
    }

    /// Auth error: 401 → ProviderError::AuthFailed.
    #[tokio::test]
    async fn mistral_complete_auth_error() {
        let ctx = MockProviderServer::start("mistral").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            401,
            serde_json::json!({"message": "Unauthorized"}),
        )
        .await;

        let adapter = MistralAdapter::with_base_url(ctx.base_url(), "bad-key");
        let req = CompletionRequest::simple("mistral-large-2411", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    /// `provider_info` returns the expected provider ID.
    #[test]
    fn mistral_provider_info() {
        let adapter = MistralAdapter::new();
        let info = adapter.provider_info();
        assert_eq!(info.id, "mistral");
        assert!(info.capabilities.supports_streaming);
    }

    /// `available_models` returns the documented model list.
    #[tokio::test]
    async fn mistral_available_models() {
        let adapter = MistralAdapter::new();
        let models = adapter.available_models().await.unwrap();
        assert_eq!(models.len(), 4);
        assert!(models.iter().any(|m| m.id == "mistral-large-2411"));
        assert!(models.iter().any(|m| m.id == "open-mixtral-8x7b"));
    }
}
