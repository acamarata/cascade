//! `OpenAIAdapter` — `ProviderAdapter` implementation for OpenAI Chat Completions.

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;
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

use super::helpers::{CHAT_PREFIXES, DEFAULT_BASE_URL, O_SERIES_PREFIXES, model_context_window};
use super::types::{
    OaiMessage, OaiModelList, OaiRequest, OaiResponse, OaiStreamEvent,
};

async fn forward_openai_sse_line(
    line: &str,
    tx: &mpsc::Sender<Result<StreamChunk, ProviderError>>,
) -> bool {
    let Some(payload) = CascadeHttpClient::parse_sse_line(line) else {
        return true;
    };

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
                    return false;
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

    true
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
    pub(super) oauth_access_token: Option<String>,
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
    pub(super) fn effective_token(&self) -> &str {
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
    pub(super) fn build_request(req: &CompletionRequest, stream: bool) -> OaiRequest {
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
            use futures::StreamExt as _;

            let mut byte_stream = response.bytes_stream();
            let mut buf = String::new();

            while let Some(raw_chunk) = byte_stream.next().await {
                let bytes = match raw_chunk {
                    Ok(b) => b,
                    Err(e) => {
                        let _ = tx
                            .send(Err(ProviderError::NetworkError(
                                crate::http_client::redact_gemini_key(e.to_string()),
                            )))
                            .await;
                        return;
                    }
                };

                let text = match std::str::from_utf8(&bytes) {
                    Ok(t) => t,
                    Err(e) => {
                        let _ = tx
                            .send(Err(ProviderError::InvalidResponse(e.to_string())))
                            .await;
                        return;
                    }
                };

                buf.push_str(text);

                while let Some(pos) = buf.find('\n') {
                    let line = buf[..pos].trim_end_matches('\r').to_string();
                    buf = buf[pos + 1..].to_string();

                    if !forward_openai_sse_line(&line, &tx).await {
                        return;
                    }
                }
            }

            if !buf.is_empty() {
                let _ = forward_openai_sse_line(&buf, &tx).await;
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

