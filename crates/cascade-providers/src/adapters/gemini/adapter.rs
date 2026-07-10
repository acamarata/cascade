//! `GeminiAdapter` — `ProviderAdapter` implementation for Google Gemini.

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;
use tracing::debug;

use cascade_types::model_ids::{MODEL_GEMINI_FLASH, MODEL_GEMINI_PRO};

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

use super::{
    config::GeminiConfig,
    stream::parse_gemini_stream_event,
    wire_types::{
        GenerationConfig, GeminiContent, GeminiPart, GeminiRequest, GeminiSystemInstruction,
    },
};

// ── GeminiAdapter ─────────────────────────────────────────────────────────────

/// `ProviderAdapter` for Google Gemini.
///
/// Create via [`GeminiAdapter::new`].  One instance per adapter seat in the
/// `ProviderRegistry`.
///
/// ## Authentication modes
///
/// - **API key** (default): key passed as `?key=` query parameter.
/// - **OAuth bearer**: `access_token` set via `with_oauth_token` or keychain.
///   When an OAuth token is present it takes precedence over the API key and is
///   sent as `Authorization: Bearer <token>`.  On a 401 response the adapter
///   attempts one token refresh via `OAuthClient`, updates the stored token, and
///   retries the request.  A second 401 returns `ProviderError::OAuthExpired`.
///
/// ## Thread safety
///
/// `GeminiAdapter` is `Send + Sync`; safe to wrap in `Arc`.
pub struct GeminiAdapter {
    pub(super) config: GeminiConfig,
    pub(super) http: CascadeHttpClient,
    /// OAuth access token — takes precedence over `config.api_key` when present.
    /// Never logged.
    pub(super) access_token: Option<String>,
    /// OAuth provider config used for token refresh on 401.  `None` in API-key mode.
    pub(super) oauth_config: Option<OAuthProviderConfig>,
    /// Current refresh token — used by `refresh_once` to obtain a new access token.
    /// `None` in API-key mode or when no refresh token is available.
    pub(super) refresh_token: Option<String>,
}

impl GeminiAdapter {
    /// Construct a new adapter from a [`GeminiConfig`].
    pub fn new(config: GeminiConfig) -> Self {
        Self {
            config,
            http: CascadeHttpClient::new(),
            access_token: None,
            oauth_config: None,
            refresh_token: None,
        }
    }

    /// Construct an OAuth-authenticated adapter.
    ///
    /// When `access_token` is present the adapter uses `Authorization: Bearer`
    /// instead of `?key=`.  On a 401 response it calls `OAuthClient::refresh_token`
    /// with `oauth_config` and retries once before returning `OAuthExpired`.
    ///
    /// ## Inputs
    /// - `config`: base Gemini config (base_url, model).
    /// - `access_token`: current OAuth access token from keychain.
    /// - `refresh_token`: OAuth refresh token for 401 retry.
    /// - `oauth_config`: provider config used to build the `OAuthClient` for refresh.
    pub fn with_oauth_token(
        config: GeminiConfig,
        access_token: impl Into<String>,
        refresh_token: impl Into<String>,
        oauth_config: OAuthProviderConfig,
    ) -> Self {
        Self {
            config,
            http: CascadeHttpClient::new(),
            access_token: Some(access_token.into()),
            oauth_config: Some(oauth_config),
            refresh_token: Some(refresh_token.into()),
        }
    }

    /// Attempt one OAuth token refresh.  Returns the new access token, or
    /// `Err(OAuthExpired)` if the refresh endpoint also fails.
    async fn refresh_once(&self) -> Result<String, ProviderError> {
        let oauth_cfg = self
            .oauth_config
            .as_ref()
            .ok_or_else(|| ProviderError::OAuthExpired {
                provider: "gemini".to_owned(),
            })?;
        let rt = self
            .refresh_token
            .as_deref()
            .ok_or_else(|| ProviderError::OAuthExpired {
                provider: "gemini".to_owned(),
            })?;
        let client = OAuthClient::new(oauth_cfg.clone());
        let tokens = client
            .refresh_token(rt)
            .await
            .map_err(|_| ProviderError::OAuthExpired {
                provider: "gemini".to_owned(),
            })?;
        Ok(tokens.access_token)
    }

    /// Return an auth closure appropriate for the current auth mode.
    ///
    /// - OAuth mode: adds `Authorization: Bearer <token>`.
    /// - API-key / proxy mode: no-op (key is in the URL query string).
    fn auth_for_token<'a>(
        token: &'a str,
    ) -> impl Fn(reqwest::RequestBuilder) -> reqwest::RequestBuilder + 'a {
        move |b| CascadeHttpClient::apply_bearer(b, token)
    }

    // ── URL builders ──────────────────────────────────────────────────────────

    /// Build the `generateContent` URL.
    ///
    /// - Proxy mode: no key.
    /// - OAuth mode: no key (auth is `Authorization: Bearer`).
    /// - API-key mode: appends `?key=`.
    pub(super) fn complete_url(&self, model: &str) -> String {
        let base = &self.config.base_url;
        if self.config.use_gfp_proxy || self.access_token.is_some() {
            format!("{base}/v1beta/models/{model}:generateContent")
        } else {
            let key = self.config.api_key.as_deref().unwrap_or("");
            format!("{base}/v1beta/models/{model}:generateContent?key={key}")
        }
    }

    /// Build the `streamGenerateContent` URL with `alt=sse`.
    pub(super) fn stream_url(&self, model: &str) -> String {
        let base = &self.config.base_url;
        if self.config.use_gfp_proxy || self.access_token.is_some() {
            format!("{base}/v1beta/models/{model}:streamGenerateContent?alt=sse")
        } else {
            let key = self.config.api_key.as_deref().unwrap_or("");
            format!("{base}/v1beta/models/{model}:streamGenerateContent?key={key}&alt=sse")
        }
    }

    /// Build the `GET /v1beta/models` health-check URL.
    pub(super) fn models_url(&self) -> String {
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
    pub(super) fn map_request(&self, req: &CompletionRequest) -> GeminiRequest {
        // Separate system prompt from user/assistant messages. Gemini has no
        // system role in `contents`, so BOTH sources of system text must be
        // hoisted into `systemInstruction`: the explicit `req.system` field
        // AND any system-role turns in the messages array (e.g. the
        // middleware compression summary). Filtering the latter without
        // hoisting silently deleted that context from the wire request.
        let mut system_texts: Vec<&str> = Vec::new();
        if let Some(s) = req.system.as_deref() {
            system_texts.push(s);
        }
        system_texts.extend(
            req.messages
                .iter()
                .filter(|m| m.role == MessageRole::System)
                .map(|m| m.content.as_str()),
        );
        let system_instruction = if system_texts.is_empty() {
            None
        } else {
            Some(GeminiSystemInstruction {
                parts: vec![GeminiPart {
                    text: system_texts.join("\n"),
                }],
            })
        };

        // System-role messages were hoisted into systemInstruction above.
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
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        use super::wire_types::{GeminiResponse, GeminiUsageMetadata};

        let model = if req.model.is_empty() {
            &self.config.model
        } else {
            &req.model
        };

        let url = self.complete_url(model);
        debug!(
            model,
            url_path = "/v1beta/models/…:generateContent",
            "gemini complete"
        );

        let wire_req = self.map_request(&req);

        // ── OAuth bearer mode: attempt refresh once on AuthFailed (401) ──────
        if let Some(ref token) = self.access_token {
            let first: Result<GeminiResponse, ProviderError> = self
                .http
                .post_json(&url, &wire_req, Self::auth_for_token(token))
                .await;

            let raw: GeminiResponse = match first {
                Ok(r) => r,
                Err(ProviderError::AuthFailed(_)) => {
                    // Attempt one token refresh.
                    let new_token = self.refresh_once().await?;
                    self.http
                        .post_json(&url, &wire_req, Self::auth_for_token(&new_token))
                        .await
                        .map_err(|e| match e {
                            ProviderError::AuthFailed(_) => ProviderError::OAuthExpired {
                                provider: "gemini".to_owned(),
                            },
                            other => other,
                        })?
                }
                Err(e) => return Err(e),
            };

            let content = raw
                .candidates
                .first()
                .and_then(|c| c.content.parts.first())
                .map(|p| p.text.clone())
                .unwrap_or_default();

            let usage = TokenUsage {
                prompt_tokens: raw
                    .usage_metadata
                    .as_ref()
                    .map(|u| u.prompt_token_count)
                    .unwrap_or(0),
                completion_tokens: raw
                    .usage_metadata
                    .as_ref()
                    .map(|u| u.candidates_token_count)
                    .unwrap_or(0),
                total_tokens: raw
                    .usage_metadata
                    .as_ref()
                    .map(|u| u.total_token_count)
                    .unwrap_or(0),
            };
            let response_model = raw.model_version.unwrap_or_else(|| model.to_string());
            let cost_usd = compute_cost("google-gemini", &response_model, &usage);
            return Ok(CompletionResponse {
                content,
                model: response_model,
                usage,
                cost_usd,
            });
        }

        // ── API-key / proxy mode (existing path) ──────────────────────────────
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
            prompt_tokens: raw
                .usage_metadata
                .as_ref()
                .map(|u: &GeminiUsageMetadata| u.prompt_token_count)
                .unwrap_or(0),
            completion_tokens: raw
                .usage_metadata
                .as_ref()
                .map(|u| u.candidates_token_count)
                .unwrap_or(0),
            total_tokens: raw
                .usage_metadata
                .as_ref()
                .map(|u| u.total_token_count)
                .unwrap_or(0),
        };

        let response_model = raw.model_version.unwrap_or_else(|| model.to_string());

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
    ) -> Result<Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>, ProviderError>
    {
        let model = if req.model.is_empty() {
            self.config.model.clone()
        } else {
            req.model.clone()
        };

        let url = self.stream_url(&model);
        debug!(model, "gemini complete_stream");

        let wire_req = self.map_request(&req);
        let response = match self.http.post_sse(&url, &wire_req, Self::auth_noop()).await {
            Ok(r) => r,
            Err(e) => {
                // Google's `streamGenerateContent` 503s under high demand
                // ("This model is currently experiencing high demand") while
                // non-streaming `generateContent` still serves. Fall back to
                // non-streaming so chat never fails just because the streaming
                // endpoint is congested — emit the whole reply as one chunk.
                debug!(%e, "gemini stream open failed — falling back to non-streaming complete()");
                let full = self.complete(req).await?;
                let (tx, rx) =
                    tokio::sync::mpsc::channel::<Result<StreamChunk, ProviderError>>(1);
                let _ = tx
                    .send(Ok(StreamChunk {
                        delta: full.content,
                        finish_reason: Some("stop".to_string()),
                    }))
                    .await;
                return Ok(Box::pin(tokio_stream::wrappers::ReceiverStream::new(rx)));
            }
        };

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
                        let _ = tx
                            .send(Err(ProviderError::StreamInterrupted(e.to_string())))
                            .await;
                        return;
                    }
                };

                let text = match std::str::from_utf8(&bytes) {
                    Ok(s) => s.to_string(),
                    Err(_) => {
                        let _ = tx
                            .send(Err(ProviderError::InvalidResponse(
                                "non-UTF-8 SSE chunk".to_string(),
                            )))
                            .await;
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
        // Roster per models/models.yaml (2026-07 audit). Doctrine model IDs
        // (MODEL_GEMINI_PRO, MODEL_GEMINI_FLASH) are imported from
        // cascade_types::model_ids; `gemini-3-flash` has no doctrine constant
        // (it is a preview-tier alt, not one of the 8 canonical fleet IDs).
        Ok(vec![
            ModelInfo {
                id: MODEL_GEMINI_PRO.to_string(),
                name: "Gemini 3.1 Pro".to_string(),
                context_window: 1_000_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: MODEL_GEMINI_FLASH.to_string(),
                name: "Gemini 3.5 Flash".to_string(),
                context_window: 1_000_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "gemini-3-flash".to_string(),
                name: "Gemini 3 Flash".to_string(),
                context_window: 1_000_000,
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
                // Vision is supported by the Gemini 3 family via multimodal
                // parts; text-only for now (P3 scope).
                supports_vision: false,
                // Gemini 3 family context window (1M tokens across the roster).
                max_context_tokens: 1_000_000,
                supports_function_calling: true,
            },
        }
    }
}
