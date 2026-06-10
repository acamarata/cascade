//! OpenAI Chat Completions adapter.
//!
//! # Purpose
//!
//! Implements [`ProviderAdapter`] for the OpenAI Chat Completions API
//! (`POST /v1/chat/completions`).  Supports GPT-4o, GPT-4o-mini, gpt-4-turbo,
//! gpt-3.5-turbo, o1, o1-mini, o3, o4-mini, and text-davinci-003 (Codex-compatible).
//!
//! # Inputs / Outputs
//!
//! - `complete()` — blocking (non-streaming) JSON response.
//! - `complete_stream()` — SSE stream; each `data:` line is a JSON chunk with
//!   `choices[0].delta.content`; stream ends at `data: [DONE]`.
//! - `available_models()` — `GET /v1/models`; result filtered to known
//!   chat-capable prefixes (gpt-, o1, o3, o4, text-davinci, chatgpt-).
//! - `health_check()` — `GET /v1/models`; returns `Ok(())` when reachable.
//!
//! # o-series quirks (o1, o3, o4)
//!
//! These models do not accept `temperature`, use `max_completion_tokens` instead
//! of `max_tokens`, and treat `system` messages as `developer` role.
//! (OpenAI API reference, 2024-12.)
//!
//! # Constraints
//!
//! - The API key is NEVER written to any log line, `Debug` output, or error string.
//! - `base_url` is configurable so the same adapter targets Azure OpenAI or any
//!   OpenAI-compatible endpoint (Together AI, Fireworks, etc.).

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;
use serde::{Deserialize, Serialize};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tracing::warn;

use crate::{
    adapter::ProviderAdapter,
    cost::compute_cost,
    error::ProviderError,
    http_client::CascadeHttpClient,
    oauth::client::{OAuthClient, OAuthProviderConfig},
    provider_info::{AuthMethod, ProviderCapabilities, ProviderInfo},
    types::{
        CompletionRequest, CompletionResponse, MessageRole, ModelInfo, StreamChunk, TokenUsage,
    },
};

// ── Constants ──────────────────────────────────────────────────────────────────

/// Default OpenAI REST base URL (no trailing slash).
const DEFAULT_BASE_URL: &str = "https://api.openai.com";

/// Model ID prefixes this adapter considers chat-capable.
/// Embedding, DALL-E, Whisper, and TTS models are excluded.
const CHAT_PREFIXES: &[&str] = &["gpt-", "chatgpt-", "o1", "o3", "o4", "text-davinci"];

/// Prefixes for o-series reasoning models with API quirks.
const O_SERIES_PREFIXES: &[&str] = &["o1", "o3", "o4"];

// ── Wire types — request ───────────────────────────────────────────────────────

/// A single message entry in the OpenAI `messages` array.
#[derive(Debug, Serialize)]
struct OaiMessage {
    role: String,
    content: String,
}

/// JSON body for `POST /v1/chat/completions`.
#[derive(Debug, Serialize)]
struct OaiRequest {
    model: String,
    messages: Vec<OaiMessage>,
    /// Standard models: `max_tokens`; o-series: use `max_completion_tokens` instead.
    #[serde(skip_serializing_if = "Option::is_none")]
    max_tokens: Option<u32>,
    /// o-series only — replaces `max_tokens`.
    #[serde(skip_serializing_if = "Option::is_none")]
    max_completion_tokens: Option<u32>,
    /// Not sent for o-series models.
    #[serde(skip_serializing_if = "Option::is_none")]
    temperature: Option<f32>,
    stream: bool,
}

// ── Wire types — response ──────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
struct OaiMessageOut {
    content: Option<String>,
}

#[derive(Debug, Deserialize)]
struct OaiChoice {
    message: OaiMessageOut,
}

#[derive(Debug, Deserialize)]
struct OaiUsage {
    prompt_tokens: u32,
    completion_tokens: u32,
    total_tokens: u32,
}

#[derive(Debug, Deserialize)]
struct OaiResponse {
    model: String,
    choices: Vec<OaiChoice>,
    usage: Option<OaiUsage>,
}

// ── Wire types — streaming ─────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
struct OaiStreamDelta {
    content: Option<String>,
}

#[derive(Debug, Deserialize)]
struct OaiStreamChoice {
    delta: OaiStreamDelta,
    finish_reason: Option<String>,
}

#[derive(Debug, Deserialize)]
struct OaiStreamEvent {
    choices: Vec<OaiStreamChoice>,
}

// ── Wire types — model list ────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
struct OaiModel {
    id: String,
}

#[derive(Debug, Deserialize)]
struct OaiModelList {
    data: Vec<OaiModel>,
}

// ── OpenAIAdapter ─────────────────────────────────────────────────────────────

/// `ProviderAdapter` implementation for the OpenAI Chat Completions API.
///
/// ## Construction
///
/// ```rust,ignore
/// // Default endpoint (api.openai.com) with API key
/// let adapter = OpenAIAdapter::new("sk-...", None::<String>);
///
/// // OAuth bearer token (loaded from keychain "cascade.oauth.openai")
/// let oauth = OpenAIAdapter::with_oauth_token("access-token", "refresh-token", oauth_cfg, None::<String>);
///
/// // Azure OpenAI or compatible endpoint
/// let azure = OpenAIAdapter::new("sk-...", Some("https://my-tenant.openai.azure.com"));
/// ```
///
/// ## Auth precedence
///
/// OAuth token wins over API key when both are present.
///
/// ## Thread safety
///
/// `OpenAIAdapter` is `Send + Sync` and safe to share across tasks via `Arc`.
pub struct OpenAIAdapter {
    /// OpenAI API key — never logged.
    api_key: String,
    /// Base URL without trailing slash.
    base_url: String,
    /// Shared HTTP client (`Arc`-backed, clone-cheap).
    http: CascadeHttpClient,
    /// OAuth access token — wins over `api_key` when present.  Never logged.
    oauth_access_token: Option<String>,
    /// OAuth provider config for token refresh on 401.
    oauth_config: Option<OAuthProviderConfig>,
    /// OAuth refresh token for 401 retry.
    oauth_refresh_token: Option<String>,
}

impl OpenAIAdapter {
    /// Create a new adapter targeting `base_url` (defaults to `api.openai.com`).
    pub fn new(api_key: impl Into<String>, base_url: Option<impl Into<String>>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: base_url
                .map(Into::into)
                .unwrap_or_else(|| DEFAULT_BASE_URL.to_owned()),
            http: CascadeHttpClient::new(),
            oauth_access_token: None,
            oauth_config: None,
            oauth_refresh_token: None,
        }
    }

    /// Create an OAuth-authenticated adapter.  OAuth token takes precedence over
    /// any API key.  On a 401 response the adapter attempts one token refresh via
    /// `OAuthClient`, then retries.  A second 401 returns `OAuthExpired`.
    ///
    /// ## Inputs
    /// - `access_token`: current OAuth access token (loaded from keychain).
    /// - `refresh_token`: OAuth refresh token.
    /// - `oauth_config`: provider config for `OAuthClient::refresh_token`.
    /// - `base_url`: optional endpoint override.
    pub fn with_oauth_token(
        access_token: impl Into<String>,
        refresh_token: impl Into<String>,
        oauth_config: OAuthProviderConfig,
        base_url: Option<impl Into<String>>,
    ) -> Self {
        Self {
            api_key: String::new(), // not used in OAuth mode
            base_url: base_url
                .map(Into::into)
                .unwrap_or_else(|| DEFAULT_BASE_URL.to_owned()),
            http: CascadeHttpClient::new(),
            oauth_access_token: Some(access_token.into()),
            oauth_config: Some(oauth_config),
            oauth_refresh_token: Some(refresh_token.into()),
        }
    }

    /// Attempt one OAuth token refresh, returning the new access token.
    async fn refresh_once(&self) -> Result<String, ProviderError> {
        let cfg = self
            .oauth_config
            .as_ref()
            .ok_or_else(|| ProviderError::OAuthExpired {
                provider: "openai".to_owned(),
            })?;
        let rt =
            self.oauth_refresh_token
                .as_deref()
                .ok_or_else(|| ProviderError::OAuthExpired {
                    provider: "openai".to_owned(),
                })?;
        OAuthClient::new(cfg.clone())
            .refresh_token(rt)
            .await
            .map(|t| t.access_token)
            .map_err(|_| ProviderError::OAuthExpired {
                provider: "openai".to_owned(),
            })
    }

    /// Return the effective bearer token: OAuth access token wins over API key.
    fn effective_token(&self) -> &str {
        self.oauth_access_token
            .as_deref()
            .unwrap_or(self.api_key.as_str())
    }

    /// Returns `true` when the model id belongs to the o-series quirk family.
    fn is_o_series(model: &str) -> bool {
        O_SERIES_PREFIXES.iter().any(|p| model.starts_with(p))
    }

    /// Translate a [`CompletionRequest`] into the OpenAI wire format.
    ///
    /// Applies o-series quirks when the model id starts with `o1`, `o3`, or `o4`:
    /// - `max_completion_tokens` instead of `max_tokens`
    /// - `temperature` omitted
    /// - `system` messages mapped to `developer` role
    fn build_request(req: &CompletionRequest, stream: bool) -> OaiRequest {
        let o_series = Self::is_o_series(&req.model);

        // Map message roles; system → developer for o-series.
        let mut messages: Vec<OaiMessage> = req
            .messages
            .iter()
            .map(|m| OaiMessage {
                role: match &m.role {
                    MessageRole::System if o_series => "developer".to_owned(),
                    MessageRole::System => "system".to_owned(),
                    MessageRole::User => "user".to_owned(),
                    MessageRole::Assistant => "assistant".to_owned(),
                },
                content: m.content.clone(),
            })
            .collect();

        // Prepend a system/developer message when `req.system` is supplied
        // and not already the first message.
        if let Some(sys) = &req.system {
            let role = if o_series { "developer" } else { "system" };
            messages.insert(
                0,
                OaiMessage {
                    role: role.to_owned(),
                    content: sys.clone(),
                },
            );
        }

        let (max_tokens, max_completion_tokens) = if o_series {
            (None, req.max_tokens)
        } else {
            (req.max_tokens, None)
        };

        OaiRequest {
            model: req.model.clone(),
            messages,
            max_tokens,
            max_completion_tokens,
            temperature: if o_series { None } else { req.temperature },
            stream,
        }
    }
}

// ── ProviderAdapter ────────────────────────────────────────────────────────────

#[async_trait]
impl ProviderAdapter for OpenAIAdapter {
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        let url = format!("{}/v1/chat/completions", self.base_url);
        let body = Self::build_request(&req, false);
        let token = self.effective_token().to_owned();

        let first: Result<OaiResponse, ProviderError> = self
            .http
            .post_json(&url, &body, move |b| {
                CascadeHttpClient::apply_bearer(b, &token)
            })
            .await;

        let resp: OaiResponse = match first {
            Ok(r) => r,
            Err(ProviderError::AuthFailed(_)) if self.oauth_access_token.is_some() => {
                // OAuth mode: attempt one refresh then retry.
                let new_token = self.refresh_once().await?;
                self.http
                    .post_json(&url, &body, move |b| {
                        CascadeHttpClient::apply_bearer(b, &new_token)
                    })
                    .await
                    .map_err(|e| match e {
                        ProviderError::AuthFailed(_) => ProviderError::OAuthExpired {
                            provider: "openai".to_owned(),
                        },
                        other => other,
                    })?
            }
            Err(e) => return Err(e),
        };

        let content = resp
            .choices
            .into_iter()
            .next()
            .and_then(|c| c.message.content)
            .unwrap_or_default();

        let usage = resp
            .usage
            .map(|u| TokenUsage {
                prompt_tokens: u.prompt_tokens,
                completion_tokens: u.completion_tokens,
                total_tokens: u.total_tokens,
            })
            .unwrap_or_default();

        let cost_usd = compute_cost("openai", &resp.model, &usage);
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
    ) -> Result<Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>, ProviderError>
    {
        let url = format!("{}/v1/chat/completions", self.base_url);
        let body = Self::build_request(&req, true);
        let token = self.effective_token().to_owned();

        let response = self
            .http
            .post_sse(&url, &body, move |b| {
                CascadeHttpClient::apply_bearer(b, &token)
            })
            .await?;

        let (tx, rx) = mpsc::channel::<Result<StreamChunk, ProviderError>>(64);

        tokio::spawn(async move {
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

            // Parse SSE lines.  [DONE] is handled by parse_sse_line → None.
            for line in text.lines() {
                if let Some(payload) = CascadeHttpClient::parse_sse_line(line) {
                    match serde_json::from_str::<OaiStreamEvent>(payload) {
                        Ok(event) => {
                            if let Some(choice) = event.choices.into_iter().next() {
                                let delta = choice.delta.content.unwrap_or_default();
                                let finish_reason = choice.finish_reason;
                                // Only emit a chunk when there is content or a stop reason.
                                if (!delta.is_empty() || finish_reason.is_some())
                                    && tx
                                        .send(Ok(StreamChunk {
                                            delta,
                                            finish_reason,
                                        }))
                                        .await
                                        .is_err()
                                {
                                    // Receiver dropped — consumer cancelled.
                                    return;
                                }
                            }
                        }
                        Err(e) => {
                            warn!(
                                error = %e,
                                payload = %payload,
                                "openai SSE: failed to parse event"
                            );
                        }
                    }
                }
            }
        });

        Ok(Box::pin(ReceiverStream::new(rx)))
    }

    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        let url = format!("{}/v1/models", self.base_url);
        let token = self.effective_token().to_owned();

        let list: OaiModelList = self
            .http
            .get_json(&url, move |b| CascadeHttpClient::apply_bearer(b, &token))
            .await?;

        let models = list
            .data
            .into_iter()
            .filter(|m| CHAT_PREFIXES.iter().any(|p| m.id.starts_with(p)))
            .map(|m| ModelInfo {
                context_window: model_context_window(&m.id),
                name: m.id.clone(),
                id: m.id,
                supports_streaming: true,
            })
            .collect();

        Ok(models)
    }

    async fn health_check(&self) -> Result<(), ProviderError> {
        let url = format!("{}/v1/models", self.base_url);
        let token = self.effective_token().to_owned();

        let _: OaiModelList = self
            .http
            .get_json(&url, move |b| CascadeHttpClient::apply_bearer(b, &token))
            .await?;

        Ok(())
    }

    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: "openai".into(),
            name: "OpenAI".into(),
            auth_method: AuthMethod::ApiKey,
            base_url: self.base_url.clone(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: true,
                // Capped at gpt-4o/o-series window; individual models vary.
                max_context_tokens: 128_000,
                // Tool-calling out of scope per spec (T-P3-E04-08).
                supports_function_calling: false,
            },
        }
    }
}

// ── Private helpers ────────────────────────────────────────────────────────────

/// Best-effort context window for well-known OpenAI model families.
/// Falls back to 4 096 for unrecognised model ids.
fn model_context_window(id: &str) -> u32 {
    if id.starts_with("gpt-4o")
        || id.starts_with("chatgpt-4o")
        || id.starts_with("gpt-4-turbo")
        || id.starts_with("o1")
        || id.starts_with("o3")
        || id.starts_with("o4")
    {
        128_000
    } else if id.starts_with("gpt-4") {
        8_192
    } else if id.starts_with("gpt-3.5") {
        16_385
    } else {
        // text-davinci and unrecognised models both default to 4 096.
        4_096
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use futures::StreamExt as _;
    use serde_json::json;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    use super::*;
    use crate::{
        test_helpers::test_support::{fixture_json, fixture_text, HttpMethod, MockProviderServer},
        types::Message,
    };

    // ── helpers ───────────────────────────────────────────────────────────────

    fn adapter_for(server: &MockProviderServer) -> OpenAIAdapter {
        OpenAIAdapter::new("sk-test-key", Some(server.base_url()))
    }

    // ── complete() happy path ─────────────────────────────────────────────────

    /// T-P3-E04-08 acceptance criterion: content non-empty, usage > 0.
    #[tokio::test]
    async fn openai_complete_happy_path_fixture() {
        let ctx = MockProviderServer::start("openai").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            fixture_json("openai_complete"),
        )
        .await;

        let adapter = adapter_for(&ctx);
        let req = CompletionRequest::simple("gpt-4o", "What is the capital of France?");
        let resp = adapter.complete(req).await.expect("complete ok");

        crate::test_helpers::test_support::assert_completion_contract(
            &resp.content,
            &resp.model,
            resp.usage.prompt_tokens,
        );
        assert!(resp.content.contains("Paris"), "content: {}", resp.content);
        assert_eq!(resp.usage.prompt_tokens, 14);
        assert_eq!(resp.usage.completion_tokens, 9);
        assert_eq!(resp.usage.total_tokens, 23);
    }

    // ── complete() auth failure ───────────────────────────────────────────────

    #[tokio::test]
    async fn openai_complete_401_maps_to_auth_failed() {
        let ctx = MockProviderServer::start("openai").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            401,
            json!({"error": {"message": "Invalid API key", "type": "invalid_request_error"}}),
        )
        .await;

        let adapter = adapter_for(&ctx);
        let err = adapter
            .complete(CompletionRequest::simple("gpt-4o", "hi"))
            .await
            .expect_err("should fail");

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    // ── complete() rate limit ─────────────────────────────────────────────────

    #[tokio::test]
    async fn openai_complete_persistent_429_maps_to_rate_limited() {
        let server = MockServer::start().await;
        Mock::given(method("POST"))
            .and(path("/v1/chat/completions"))
            .respond_with(
                ResponseTemplate::new(429)
                    .append_header("retry-after", "0")
                    .append_header("content-length", "0"),
            )
            .mount(&server)
            .await;

        let adapter = OpenAIAdapter::new("sk-test-key", Some(server.uri()));
        let err = adapter
            .complete(CompletionRequest::simple("gpt-4o", "hi"))
            .await
            .expect_err("should fail");

        assert!(
            matches!(err, ProviderError::RateLimited { .. }),
            "expected RateLimited, got {err:?}"
        );
    }

    // ── complete_stream() ─────────────────────────────────────────────────────

    /// T-P3-E04-08 acceptance: stream yields ≥1 StreamChunk from fixture.
    #[tokio::test]
    async fn openai_stream_parse_fixture() {
        let ctx = MockProviderServer::start("openai").await;
        let sse_body = fixture_text("openai_stream");
        ctx.mount_raw(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let adapter = adapter_for(&ctx);
        let req = CompletionRequest::simple("gpt-4o", "What is the capital of France?");
        let mut stream = adapter.complete_stream(req).await.expect("stream open");

        let mut chunks: Vec<StreamChunk> = Vec::new();
        while let Some(result) = stream.next().await {
            chunks.push(result.expect("chunk ok"));
        }

        assert!(!chunks.is_empty(), "expected ≥1 StreamChunk, got 0");

        // At least one chunk should carry a stop finish_reason.
        let has_stop = chunks
            .iter()
            .any(|c| c.finish_reason.as_deref() == Some("stop"));
        assert!(has_stop, "expected a chunk with finish_reason=stop");
    }

    /// [DONE]-only SSE body closes the stream with zero chunks, no error.
    #[tokio::test]
    async fn openai_stream_done_sentinel_yields_zero_chunks() {
        let ctx = MockProviderServer::start("openai").await;
        ctx.mount_raw(
            HttpMethod::Post,
            "/v1/chat/completions",
            200,
            "text/event-stream",
            "data: [DONE]\n\n".to_owned(),
        )
        .await;

        let adapter = adapter_for(&ctx);
        let mut stream = adapter
            .complete_stream(CompletionRequest::simple("gpt-4o", "hi"))
            .await
            .expect("stream open");

        let mut count = 0usize;
        while let Some(r) = stream.next().await {
            r.expect("unexpected error in DONE-only stream");
            count += 1;
        }
        assert_eq!(count, 0);
    }

    // ── available_models() ────────────────────────────────────────────────────

    /// T-P3-E04-08 acceptance: ≥1 ModelInfo; filter excludes embeddings/dall-e/whisper.
    #[tokio::test]
    async fn openai_available_models_filtered() {
        let ctx = MockProviderServer::start("openai").await;
        ctx.mount_json(
            HttpMethod::Get,
            "/v1/models",
            200,
            json!({
                "object": "list",
                "data": [
                    {"id": "gpt-4o"},
                    {"id": "gpt-4-turbo"},
                    {"id": "gpt-3.5-turbo"},
                    {"id": "o1-mini"},
                    {"id": "o4-mini"},
                    {"id": "text-davinci-003"},
                    // These must be filtered out:
                    {"id": "text-embedding-3-small"},
                    {"id": "dall-e-3"},
                    {"id": "whisper-1"}
                ]
            }),
        )
        .await;

        let adapter = adapter_for(&ctx);
        let models = adapter.available_models().await.expect("models ok");

        assert!(!models.is_empty(), "expected ≥1 model");
        let ids: Vec<&str> = models.iter().map(|m| m.id.as_str()).collect();
        assert!(ids.contains(&"gpt-4o"));
        assert!(ids.contains(&"o1-mini"));
        assert!(ids.contains(&"text-davinci-003"));
        assert!(!ids.contains(&"dall-e-3"), "dall-e-3 must be filtered");
        assert!(!ids.contains(&"whisper-1"), "whisper-1 must be filtered");
        assert!(
            !ids.contains(&"text-embedding-3-small"),
            "embedding must be filtered"
        );
    }

    // ── health_check() ────────────────────────────────────────────────────────

    #[tokio::test]
    async fn openai_health_check_success() {
        let ctx = MockProviderServer::start("openai").await;
        ctx.mount_json(
            HttpMethod::Get,
            "/v1/models",
            200,
            json!({"object": "list", "data": [{"id": "gpt-4o"}]}),
        )
        .await;
        adapter_for(&ctx).health_check().await.expect("health ok");
    }

    // ── contract assertions ───────────────────────────────────────────────────

    #[test]
    fn openai_provider_info_contract() {
        let adapter = OpenAIAdapter::new("sk-x", None::<String>);
        let info = adapter.provider_info();
        assert_eq!(info.id, "openai");
        assert_eq!(info.name, "OpenAI");
        assert!(info.capabilities.supports_streaming);
        assert_eq!(info.base_url, DEFAULT_BASE_URL);
    }

    // ── API key safety ────────────────────────────────────────────────────────

    #[tokio::test]
    async fn openai_api_key_not_in_error_messages() {
        let ctx = MockProviderServer::start("openai").await;
        ctx.mount_json(
            HttpMethod::Post,
            "/v1/chat/completions",
            401,
            json!({"error": {"message": "Incorrect API key"}}),
        )
        .await;

        let adapter = OpenAIAdapter::new("sk-canary-secret-1234", Some(ctx.base_url()));
        let err = adapter
            .complete(CompletionRequest::simple("gpt-4o", "hi"))
            .await
            .unwrap_err();
        let err_str = err.to_string();
        assert!(
            !err_str.contains("sk-canary-secret-1234"),
            "API key leaked: {err_str}"
        );
    }

    // ── o-series quirks ───────────────────────────────────────────────────────

    #[test]
    fn o_series_uses_max_completion_tokens_and_no_temperature() {
        let req = CompletionRequest {
            model: "o1".to_owned(),
            messages: vec![
                Message::system("You are helpful."),
                Message::user("Explain recursion."),
            ],
            max_tokens: Some(500),
            temperature: Some(0.7),
            stream: false,
            system: None,
        };

        let oai = OpenAIAdapter::build_request(&req, false);

        assert_eq!(
            oai.max_completion_tokens,
            Some(500),
            "o-series needs max_completion_tokens"
        );
        assert_eq!(oai.max_tokens, None, "o-series must not send max_tokens");
        assert_eq!(oai.temperature, None, "o-series must not send temperature");
        assert_eq!(
            oai.messages[0].role, "developer",
            "system → developer for o-series"
        );
        assert_eq!(oai.messages[1].role, "user");
    }

    #[test]
    fn standard_gpt4o_uses_max_tokens_and_temperature() {
        let req = CompletionRequest {
            model: "gpt-4o".to_owned(),
            messages: vec![Message::user("hello")],
            max_tokens: Some(100),
            temperature: Some(0.5),
            stream: false,
            system: None,
        };

        let oai = OpenAIAdapter::build_request(&req, false);

        assert_eq!(oai.max_tokens, Some(100));
        assert_eq!(oai.max_completion_tokens, None);
        assert_eq!(oai.temperature, Some(0.5));
    }

    // ── context window table ──────────────────────────────────────────────────

    #[test]
    fn context_window_lookup_coverage() {
        assert_eq!(model_context_window("gpt-4o"), 128_000);
        assert_eq!(model_context_window("gpt-4o-mini"), 128_000);
        assert_eq!(model_context_window("gpt-4-turbo"), 128_000);
        assert_eq!(model_context_window("gpt-4"), 8_192);
        assert_eq!(model_context_window("gpt-3.5-turbo"), 16_385);
        assert_eq!(model_context_window("o1"), 128_000);
        assert_eq!(model_context_window("o3-mini"), 128_000);
        assert_eq!(model_context_window("o4-mini"), 128_000);
        assert_eq!(model_context_window("text-davinci-003"), 4_096);
        assert_eq!(model_context_window("unknown-xyz"), 4_096);
    }

    // ── base_url configurability ──────────────────────────────────────────────

    #[test]
    fn custom_base_url_stored_in_provider_info() {
        let adapter = OpenAIAdapter::new("sk-k", Some("https://my-tenant.openai.azure.com"));
        assert_eq!(
            adapter.provider_info().base_url,
            "https://my-tenant.openai.azure.com"
        );
    }

    #[test]
    fn default_base_url_is_api_openai_com() {
        let adapter = OpenAIAdapter::new("sk-k", None::<String>);
        assert_eq!(adapter.provider_info().base_url, DEFAULT_BASE_URL);
    }

    // ── OAuth effective_token precedence ──────────────────────────────────────

    #[test]
    fn oauth_token_wins_over_api_key() {
        use crate::oauth::client::OAuthProviderConfig;
        let cfg = OAuthProviderConfig {
            client_id: "cid".to_owned(),
            client_secret: String::new(),
            auth_url: "https://chat.openai.com/oauth/authorize".to_owned(),
            token_url: "https://auth.openai.com/oauth/token".to_owned(),
            scopes: vec!["openid".to_owned()],
        };
        let adapter = OpenAIAdapter::with_oauth_token("oauth-token", "rt", cfg, None::<String>);
        assert_eq!(adapter.effective_token(), "oauth-token");
        assert!(adapter.oauth_access_token.is_some());
    }

    #[test]
    fn api_key_used_when_no_oauth_token() {
        let adapter = OpenAIAdapter::new("sk-test", None::<String>);
        assert_eq!(adapter.effective_token(), "sk-test");
        assert!(adapter.oauth_access_token.is_none());
    }

    // ── OAuth 401 + refresh + retry ───────────────────────────────────────────

    #[tokio::test]
    async fn openai_oauth_401_triggers_refresh_and_retry() {
        use crate::oauth::client::OAuthProviderConfig;
        use wiremock::matchers::{body_string_contains, method as wm_method, path as wm_path};
        use wiremock::{Mock, MockServer, ResponseTemplate};

        let api_server = MockServer::start().await;
        let token_server = MockServer::start().await;

        // First API call: 401.
        Mock::given(wm_method("POST"))
            .and(wm_path("/v1/chat/completions"))
            .respond_with(ResponseTemplate::new(401).set_body_string("Unauthorized"))
            .up_to_n_times(1)
            .mount(&api_server)
            .await;

        // Second API call: 200.
        Mock::given(wm_method("POST"))
            .and(wm_path("/v1/chat/completions"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "model": "gpt-4o",
                "choices": [{"message": {"content": "hello"}}],
                "usage": {"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8}
            })))
            .mount(&api_server)
            .await;

        // Token refresh: succeeds.
        Mock::given(wm_method("POST"))
            .and(wm_path("/token"))
            .and(body_string_contains("refresh_token"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "access_token": "refreshed-openai-token",
                "expires_in": 3600,
                "token_type": "Bearer"
            })))
            .mount(&token_server)
            .await;

        let oauth_cfg = OAuthProviderConfig {
            client_id: "test-client".to_owned(),
            client_secret: String::new(),
            auth_url: "https://chat.openai.com/oauth/authorize".to_owned(),
            token_url: format!("{}/token", token_server.uri()),
            scopes: vec!["openid".to_owned()],
        };

        let adapter = OpenAIAdapter::with_oauth_token(
            "initial-token",
            "refresh-token-xyz",
            oauth_cfg,
            Some(api_server.uri()),
        );

        let req = CompletionRequest::simple("gpt-4o", "hello");
        let result = adapter.complete(req).await;
        assert!(
            result.is_ok(),
            "expected Ok after refresh+retry: {:?}",
            result
        );
    }

    #[tokio::test]
    async fn openai_oauth_double_401_returns_oauth_expired() {
        use crate::oauth::client::OAuthProviderConfig;
        use wiremock::matchers::{method as wm_method, path as wm_path};
        use wiremock::{Mock, MockServer, ResponseTemplate};

        let api_server = MockServer::start().await;
        let token_server = MockServer::start().await;

        Mock::given(wm_method("POST"))
            .and(wm_path("/v1/chat/completions"))
            .respond_with(ResponseTemplate::new(401).set_body_string("Unauthorized"))
            .mount(&api_server)
            .await;

        Mock::given(wm_method("POST"))
            .and(wm_path("/token"))
            .respond_with(ResponseTemplate::new(400).set_body_string("invalid_grant"))
            .mount(&token_server)
            .await;

        let oauth_cfg = OAuthProviderConfig {
            client_id: "tc".to_owned(),
            client_secret: String::new(),
            auth_url: "https://chat.openai.com/oauth/authorize".to_owned(),
            token_url: format!("{}/token", token_server.uri()),
            scopes: vec!["openid".to_owned()],
        };

        let adapter = OpenAIAdapter::with_oauth_token(
            "bad-token",
            "bad-rt",
            oauth_cfg,
            Some(api_server.uri()),
        );

        let err = adapter
            .complete(CompletionRequest::simple("gpt-4o", "hi"))
            .await
            .unwrap_err();
        assert!(
            matches!(err, ProviderError::OAuthExpired { ref provider } if provider == "openai"),
            "expected OAuthExpired(openai), got: {err:?}"
        );
    }
}
