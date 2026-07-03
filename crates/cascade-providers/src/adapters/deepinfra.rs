//! DeepInfraAdapter — api.deepinfra.com/v1/openai (OpenAI-compatible).
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for DeepInfra using its OpenAI-compatible
//! chat completions endpoint at `api.deepinfra.com/v1/openai/chat/completions`.
//! DeepInfra hosts a large catalogue of open-source models (Llama, Qwen,
//! Mistral, etc.) behind a single OpenAI-shaped wire format.
//!
//! # Inputs / Outputs
//!
//! - `complete`: POST `/v1/openai/chat/completions` with `stream: false`.
//! - `complete_stream`: POST with `stream: true`, parse OpenAI-style SSE.
//! - `available_models`: GET `/v1/openai/models`, parsed from a live catalogue.
//! - `health_check`: checks credential validity with a minimal request.
//!
//! # Constraints
//!
//! - API key resolution order: `DEEPINFRA_API_KEY` env var first, then the
//!   keychain service `"cascade.deepinfra"` (env var wins — lets local dev
//!   override a stored key without re-connecting the provider).
//! - Default model: `"meta-llama/Meta-Llama-3.1-70B-Instruct"`.
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

const DEEPINFRA_BASE_URL: &str = "https://api.deepinfra.com/v1/openai";
const KEYCHAIN_SERVICE: &str = "cascade.deepinfra";
const PROVIDER_ID: &str = "deepinfra";
const ENV_KEY_VAR: &str = "DEEPINFRA_API_KEY";

/// Default model used by `health_check()` and offered as the first entry of
/// the static fallback list (when the live `/models` call fails).
const DEFAULT_MODEL: &str = "meta-llama/Meta-Llama-3.1-70B-Instruct";

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

/// A single entry in the `GET /v1/openai/models` catalogue response.
#[derive(Debug, Deserialize)]
struct DeepInfraModelEntry {
    id: String,
    /// Context window, when the catalogue reports one. Not every DeepInfra
    /// model entry includes this field.
    #[serde(default)]
    context_length: Option<u32>,
}

/// Top-level response from `GET /v1/openai/models` (OpenAI-shaped list envelope).
#[derive(Debug, Deserialize)]
struct DeepInfraModelsResponse {
    data: Vec<DeepInfraModelEntry>,
}

// ── DeepInfraAdapter ──────────────────────────────────────────────────────────

/// `ProviderAdapter` for DeepInfra.
///
/// Targets `api.deepinfra.com/v1/openai` with `Authorization: Bearer` auth.
pub struct DeepInfraAdapter {
    http: CascadeHttpClient,
    base_url: String,
    api_key: Option<String>,
}

impl DeepInfraAdapter {
    /// Construct a production adapter pointing at the real DeepInfra API.
    ///
    /// The API key is resolved lazily on each call: `DEEPINFRA_API_KEY` env
    /// var first, then the OS keychain.
    pub fn new() -> Self {
        Self {
            http: CascadeHttpClient::new(),
            base_url: DEEPINFRA_BASE_URL.to_owned(),
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

    /// Resolve the API key: env var `DEEPINFRA_API_KEY` first, then keychain.
    fn api_key(&self) -> Result<String, ProviderError> {
        if let Some(ref key) = self.api_key {
            return Ok(key.clone());
        }
        if let Ok(key) = std::env::var(ENV_KEY_VAR) {
            if !key.is_empty() {
                return Ok(key);
            }
        }
        cascade_keychain::platform_keychain()
            .get_key(KEYCHAIN_SERVICE, PROVIDER_ID)
            .map_err(|_| {
                ProviderError::CredentialNotFound(format!(
                    "no DeepInfra API key in ${ENV_KEY_VAR} or keychain service '{KEYCHAIN_SERVICE}'"
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
        format!("{}/chat/completions", self.base_url.trim_end_matches('/'))
    }

    fn models_url(&self) -> String {
        format!("{}/models", self.base_url.trim_end_matches('/'))
    }
}

impl Default for DeepInfraAdapter {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl ProviderAdapter for DeepInfraAdapter {
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
        let cost_usd = compute_cost(PROVIDER_ID, &resp.model, &token_usage);
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
        let key = self.api_key()?;
        let url = self.models_url();

        let resp: DeepInfraModelsResponse = self
            .http
            .get_json(&url, move |b| CascadeHttpClient::apply_bearer(b, &key))
            .await?;

        Ok(resp
            .data
            .into_iter()
            .map(|m| ModelInfo {
                name: m.id.clone(),
                id: m.id,
                context_window: m.context_length.unwrap_or(32_768),
                supports_streaming: true,
            })
            .collect())
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        let req = CompletionRequest::simple(DEFAULT_MODEL, "ping");
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
            name: "DeepInfra".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: DEEPINFRA_BASE_URL.into(),
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

    #[tokio::test]
    async fn deepinfra_complete_happy_path() {
        let ctx = MockProviderServer::start("deepinfra").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            200,
            fixture_json("deepinfra_complete"),
        )
        .await;

        let adapter = DeepInfraAdapter::with_base_url(ctx.base_url(), "test-key");
        let req = CompletionRequest::simple(DEFAULT_MODEL, "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("should succeed");

        assert!(!resp.content.is_empty(), "content must not be empty");
        assert!(!resp.model.is_empty(), "model must not be empty");
        assert!(
            resp.usage.prompt_tokens > 0,
            "prompt_tokens must be positive"
        );
    }

    #[test]
    fn deepinfra_build_request_serializes_expected_shape() {
        let req = CompletionRequest::simple(DEFAULT_MODEL, "hello");
        let body = DeepInfraAdapter::build_request(&req, false);

        assert_eq!(body.model, DEFAULT_MODEL);
        assert!(!body.stream);
        assert_eq!(body.messages.len(), 1);
        assert_eq!(body.messages[0].role, "user");
        assert_eq!(body.messages[0].content, "hello");

        let json = serde_json::to_value(&body).expect("serializes");
        assert_eq!(json["model"], DEFAULT_MODEL);
        assert_eq!(json["stream"], false);
        assert_eq!(json["messages"][0]["role"], "user");
        assert!(json.get("max_tokens").is_none(), "omitted when None");
    }

    #[tokio::test]
    async fn deepinfra_complete_matches_request_body_via_mock() {
        use wiremock::matchers::{body_json, method, path};
        use wiremock::{Mock, ResponseTemplate};

        let ctx = MockProviderServer::start("deepinfra_body").await;
        let expected_body = serde_json::json!({
            "model": DEFAULT_MODEL,
            "messages": [{"role": "user", "content": "hello"}],
            "stream": false,
        });
        Mock::given(method("POST"))
            .and(path("/chat/completions"))
            .and(body_json(&expected_body))
            .respond_with(
                ResponseTemplate::new(200).set_body_json(fixture_json("deepinfra_complete")),
            )
            .mount(&ctx.server)
            .await;

        let adapter = DeepInfraAdapter::with_base_url(ctx.base_url(), "test-key");
        let req = CompletionRequest::simple(DEFAULT_MODEL, "hello");
        let resp = adapter.complete(req).await;

        assert!(
            resp.is_ok(),
            "expected the mock to match the exact serialized body, got {resp:?}"
        );
    }

    #[tokio::test]
    async fn deepinfra_complete_auth_error() {
        let ctx = MockProviderServer::start("deepinfra_auth").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            401,
            serde_json::json!({"error": {"message": "Invalid API key", "type": "authentication_error"}}),
        )
        .await;

        let adapter = DeepInfraAdapter::with_base_url(ctx.base_url(), "bad-key");
        let req = CompletionRequest::simple(DEFAULT_MODEL, "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    #[test]
    fn deepinfra_provider_info() {
        let adapter = DeepInfraAdapter::new();
        let info = adapter.provider_info();
        assert_eq!(info.id, "deepinfra");
        assert_eq!(info.name, "DeepInfra");
        assert_eq!(info.base_url, DEEPINFRA_BASE_URL);
        assert!(info.capabilities.supports_streaming);
        assert!(matches!(info.auth_method, AuthMethod::ApiKey));
    }

    #[tokio::test]
    async fn deepinfra_available_models_parses_catalogue_fixture() {
        let ctx = MockProviderServer::start("deepinfra_models").await;
        ctx.mount_json(
            HttpMethod::Get,
            "/models",
            200,
            fixture_json("deepinfra_models"),
        )
        .await;

        let adapter = DeepInfraAdapter::with_base_url(ctx.base_url(), "test-key");
        let models = adapter.available_models().await.expect("should succeed");

        assert_eq!(models.len(), 2, "expected 2 models from fixture");
        assert!(models
            .iter()
            .any(|m| m.id == "meta-llama/Meta-Llama-3.1-70B-Instruct"));
        let llama = models
            .iter()
            .find(|m| m.id == "meta-llama/Meta-Llama-3.1-70B-Instruct")
            .unwrap();
        assert_eq!(llama.context_window, 131_072);
        assert!(llama.supports_streaming);
    }

    #[tokio::test]
    async fn deepinfra_health_check_ok_on_success() {
        let ctx = MockProviderServer::start("deepinfra_health").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            200,
            fixture_json("deepinfra_complete"),
        )
        .await;

        let adapter = DeepInfraAdapter::with_base_url(ctx.base_url(), "test-key");
        assert!(adapter.health_check().await.is_ok());
    }

    #[tokio::test]
    async fn deepinfra_health_check_propagates_auth_failure() {
        let ctx = MockProviderServer::start("deepinfra_health_auth").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/chat/completions",
            401,
            serde_json::json!({"error": {"message": "Invalid API key"}}),
        )
        .await;

        let adapter = DeepInfraAdapter::with_base_url(ctx.base_url(), "bad-key");
        let err = adapter.health_check().await.unwrap_err();
        assert!(matches!(err, ProviderError::AuthFailed(_)));
    }

    #[test]
    fn deepinfra_api_key_env_var_overrides_keychain() {
        let _guard = crate::test_helpers::ENV_TEST_LOCK.lock().unwrap();
        std::env::set_var(ENV_KEY_VAR, "env-key-value");

        let adapter = DeepInfraAdapter::new();
        let resolved = adapter.api_key().expect("env var should resolve");
        assert_eq!(resolved, "env-key-value");

        std::env::remove_var(ENV_KEY_VAR);
    }

    #[test]
    fn deepinfra_api_key_missing_returns_credential_not_found() {
        let _guard = crate::test_helpers::ENV_TEST_LOCK.lock().unwrap();
        std::env::remove_var(ENV_KEY_VAR);

        let adapter = DeepInfraAdapter::new();
        let err = adapter.api_key().unwrap_err();
        assert!(matches!(err, ProviderError::CredentialNotFound(_)));
    }
}
