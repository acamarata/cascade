//! Anthropic Messages API adapter.
//!
//! # Purpose
//!
//! Implements `ProviderAdapter` for the Anthropic Messages API
//! (`https://api.anthropic.com/v1/messages`).  Supports blocking completions,
//! SSE streaming, model enumeration, and connectivity health-check.
//!
//! # Inputs / Outputs
//!
//! - `complete`: maps `CompletionRequest` → Anthropic messages body →
//!   `CompletionResponse` (extracts `content[0].text`, usage, model).
//! - `complete_stream`: same endpoint with `stream: true`; parses
//!   `content_block_delta` SSE events into `StreamChunk` values.
//! - `available_models`: returns a hardcoded list of current Claude models.
//! - `health_check`: GET `/v1/models`; returns `Ok(())` on HTTP 200.
//!
//! # Constraints
//!
//! - The API key is NEVER written to a log line, `Debug` output, or error
//!   message — use the `api_key` field only at the call site.
//! - `max_tokens` defaults to 1024 when the caller omits it (Anthropic
//!   requires the field).
//! - SSE parsing yields chunks until `event: message_stop` or `[DONE]`; any
//!   parse error yields `StreamInterrupted`.
//!
//! # Prompt caching (this adapter only)
//!
//! `CacheStrategy::Auto` inserts an ephemeral `cache_control` breakpoint at
//! the end of the system block (and after the last tool, if tools are ever
//! added to this adapter) — but ONLY when both gates hold:
//!
//! 1. The estimated prefix token count clears [`MIN_CACHEABLE_PREFIX_CHARS`]
//!    (a conservative chars/4 estimate of the ~1024–4096 token minimum
//!    Anthropic requires before it will cache a prefix at all — see
//!    constant doc comment for the exact reasoning).
//! 2. The caller sets `reuse_expected: true` on [`AnthropicRequestOptions`],
//!    signalling that this prefix is expected to be replayed within the
//!    cache TTL. This encodes the break-even guardrail: a cache write costs
//!    1.25x (5-min TTL) base input price, so caching a prefix that is never
//!    reused is a pure loss.
//!
//! `CacheStrategy::Off` (the default) never emits `cache_control`.

use std::pin::Pin;

use async_trait::async_trait;
use futures_core::Stream;
use serde::{Deserialize, Serialize};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;

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

const BASE_URL: &str = "https://api.anthropic.com";
const MESSAGES_PATH: &str = "/v1/messages";
const MODELS_PATH: &str = "/v1/models";
const ANTHROPIC_VERSION: &str = "2023-06-01";
const DEFAULT_MAX_TOKENS: u32 = 1024;

/// Conservative minimum prefix length (in characters) required before
/// `CacheStrategy::Auto` will emit a `cache_control` breakpoint.
///
/// Anthropic's documented minimum cacheable prefix ranges from 1024 tokens
/// (e.g. Sonnet-tier models) up to 4096 tokens (e.g. Opus-tier models) —
/// below that, a `cache_control` marker is silently ignored (no error, just
/// `cache_creation_input_tokens: 0`). This adapter has no tokenizer
/// available, so it uses the standard chars/4 estimate and picks the
/// *stricter* end of the range (4096 tokens) so the gate never fires a
/// cache write on a prefix that would silently no-op on an Opus-tier model.
/// `4096 tokens * 4 chars/token = 16384 chars`.
const MIN_CACHEABLE_PREFIX_CHARS: usize = 16_384;

// ── Cache strategy (this adapter only) ────────────────────────────────────────

/// Controls whether [`AnthropicAdapter`] inserts `cache_control` ephemeral
/// breakpoints on the request's stable prefix (tools → system → early
/// messages).
///
/// See the module-level "Prompt caching" doc section for the full gating
/// rules. Default is [`CacheStrategy::Off`] — caching is opt-in because a
/// cache write is only economical when the prefix is actually reused within
/// the cache TTL, and this adapter cannot know that on its own.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum CacheStrategy {
    /// Never emit `cache_control` — every request is billed as a plain,
    /// uncached prompt. This is the safe, zero-surprise default.
    #[default]
    Off,
    /// Emit a `cache_control` ephemeral breakpoint at the end of the system
    /// block when both the prefix-size gate and the `reuse_expected` gate
    /// (see [`AnthropicRequestOptions`]) hold.
    Auto,
}

/// Anthropic-adapter-specific request options that sit alongside the
/// provider-agnostic [`CompletionRequest`].
///
/// These are intentionally NOT part of `CompletionRequest` (in `types.rs`)
/// because prompt-caching strategy is an Anthropic-wire-format concern, not
/// a cross-provider one. A future router/middleware layer is expected to
/// construct this and pass it to [`AnthropicAdapter::complete_with_options`]
/// / [`AnthropicAdapter::complete_stream_with_options`] once caching is
/// wired into routing decisions.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct AnthropicRequestOptions {
    /// Whether to attempt prompt-cache breakpoint insertion. Defaults to
    /// [`CacheStrategy::Off`].
    pub cache_strategy: CacheStrategy,
    /// Caller signal that this exact prefix is expected to be reused by a
    /// subsequent request within the cache TTL (5 minutes for the default
    /// ephemeral breakpoint). A cache write costs ~1.25x base input price;
    /// break-even requires at least 2 requests sharing the prefix within
    /// the TTL. Defaults to `false` — no reuse assumed, so `Auto` never
    /// writes a cache entry unless the caller explicitly opts in.
    pub reuse_expected: bool,
}

/// Best-effort lint: reject prefix content that is obviously non-stable
/// (i.e. would change on every request and therefore invalidate the cache
/// on every call, defeating the point of caching it).
///
/// This is NOT a guarantee of stability — it is a cheap heuristic check
/// intended for `debug_assert!`-style use during development, catching the
/// most common mistake (interpolating `chrono::Utc::now()` /
/// `Utc::today()` / similar into a system prompt that is otherwise meant to
/// be a stable, cacheable prefix). It specifically looks for an ISO-8601
/// calendar date matching *today* (`YYYY-MM-DD`, UTC) anywhere in the text.
/// It does not attempt to detect UUIDs, unsorted maps, or other
/// non-determinism — those are caller responsibilities documented in
/// `shared/prompt-caching.md`.
///
/// Returns `true` if the prefix looks stable (no obvious today's-date
/// stamp found), `false` if it looks unstable.
fn prefix_is_stable(prefix: &str) -> bool {
    let today = current_iso_date();
    !prefix.contains(&today)
}

/// Today's date in `YYYY-MM-DD` (UTC), computed without pulling in a
/// datetime dependency — good enough for the `prefix_is_stable` heuristic,
/// which only needs to catch an accidentally-interpolated "today" stamp.
fn current_iso_date() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};

    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let days = secs / 86_400;

    // Civil-from-days algorithm (Howard Hinnant's `civil_from_days`),
    // proleptic Gregorian, days since 1970-01-01 -> (y, m, d).
    let z = days as i64 + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = (z - era * 146_097) as u64; // [0, 146096]
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365; // [0, 399]
    let y = yoe as i64 + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100); // [0, 365]
    let mp = (5 * doy + 2) / 153; // [0, 11]
    let d = doy - (153 * mp + 2) / 5 + 1; // [1, 31]
    let m = if mp < 10 { mp + 3 } else { mp - 9 }; // [1, 12]
    let y = if m <= 2 { y + 1 } else { y };

    format!("{y:04}-{m:02}-{d:02}")
}

// ── Wire types (Anthropic-specific request/response shapes) ──────────────────

/// A `cache_control` marker attached to a content block to request an
/// ephemeral prompt-cache breakpoint at that position.
#[derive(Debug, Serialize)]
struct CacheControl {
    #[serde(rename = "type")]
    control_type: &'static str,
}

impl CacheControl {
    const fn ephemeral() -> Self {
        Self {
            control_type: "ephemeral",
        }
    }
}

#[derive(Debug, Serialize)]
struct AnthropicMessage {
    role: &'static str,
    content: String,
}

/// System prompt content block. Anthropic's `system` field accepts either a
/// bare string or an array of content blocks; this adapter always sends the
/// array form so a `cache_control` breakpoint can be attached when caching
/// is enabled.
#[derive(Debug, Serialize)]
struct AnthropicSystemBlock {
    #[serde(rename = "type")]
    block_type: &'static str,
    text: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    cache_control: Option<CacheControl>,
}

#[derive(Debug, Serialize)]
struct AnthropicRequest {
    model: String,
    max_tokens: u32,
    messages: Vec<AnthropicMessage>,
    #[serde(skip_serializing_if = "Option::is_none")]
    system: Option<Vec<AnthropicSystemBlock>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    temperature: Option<f32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    stream: Option<bool>,
}

#[derive(Debug, Deserialize)]
struct AnthropicContent {
    #[serde(rename = "type")]
    content_type: String,
    #[serde(default)]
    text: String,
}

#[derive(Debug, Deserialize)]
struct AnthropicUsage {
    input_tokens: u32,
    output_tokens: u32,
    /// Tokens written to the prompt cache on this request (billed at the
    /// cache-write premium, ~1.25x base input price for the 5-min TTL).
    /// Absent from the response entirely when caching is not in play, so
    /// this defaults to 0 rather than failing deserialization.
    #[serde(default)]
    cache_creation_input_tokens: u32,
    /// Tokens served from the prompt cache on this request (billed at
    /// ~0.1x base input price). Same absence behavior as
    /// `cache_creation_input_tokens`.
    #[serde(default)]
    cache_read_input_tokens: u32,
}

#[derive(Debug, Deserialize)]
struct AnthropicResponse {
    content: Vec<AnthropicContent>,
    model: String,
    usage: AnthropicUsage,
}

/// Extended usage/cost view surfaced by this adapter, layering Anthropic's
/// prompt-cache economics on top of the provider-agnostic
/// [`CompletionResponse`]/[`TokenUsage`].
///
/// This is intentionally adapter-local rather than added to the shared
/// `TokenUsage` struct in `types.rs` — cache-creation/cache-read token
/// counts are an Anthropic-specific wire concept (other providers have
/// different or no equivalent), so folding them into the cross-provider
/// type would leak provider-specific fields into every adapter.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct AnthropicCacheUsage {
    /// Tokens written to the prompt cache this request.
    pub cache_creation_input_tokens: u32,
    /// Tokens read from the prompt cache this request.
    pub cache_read_input_tokens: u32,
}

impl From<&AnthropicUsage> for AnthropicCacheUsage {
    fn from(u: &AnthropicUsage) -> Self {
        Self {
            cache_creation_input_tokens: u.cache_creation_input_tokens,
            cache_read_input_tokens: u.cache_read_input_tokens,
        }
    }
}

// SSE event shapes
#[derive(Debug, Deserialize)]
struct AnthropicSseDelta {
    #[serde(rename = "type")]
    delta_type: String,
    #[serde(default)]
    text: String,
}

#[derive(Debug, Deserialize)]
struct AnthropicSseEvent {
    #[serde(rename = "type")]
    event_type: String,
    #[serde(default)]
    delta: Option<AnthropicSseDelta>,
}

// Models endpoint response — fields exist for JSON deserialization; not all are read at runtime.
#[derive(Debug, Deserialize)]
struct AnthropicModelsResponse {
    #[allow(dead_code)]
    data: Vec<AnthropicModelEntry>,
}

#[derive(Debug, Deserialize)]
struct AnthropicModelEntry {
    #[allow(dead_code)]
    id: String,
}

// ── AnthropicAdapter ──────────────────────────────────────────────────────────

/// `ProviderAdapter` implementation for the Anthropic Messages API.
///
/// Construct with `AnthropicAdapter::new(api_key)`.  The API key must be
/// obtained from the OS keychain via `cascade-keychain` before construction;
/// this adapter does not perform keychain access itself.
pub struct AnthropicAdapter {
    /// Anthropic API key — never logged or included in error messages.
    api_key: String,
    /// Base URL override (used in tests to point at a mock server).
    base_url: String,
    http: CascadeHttpClient,
}

impl AnthropicAdapter {
    /// Construct a new adapter.
    ///
    /// `api_key` is the Anthropic API key loaded by the caller from the OS
    /// keychain (service name `"cascade.anthropic"`).  It is stored in memory
    /// only for the lifetime of this adapter instance.
    pub fn new(api_key: impl Into<String>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: BASE_URL.to_owned(),
            http: CascadeHttpClient::new(),
        }
    }

    /// Construct with a custom base URL — used in unit tests and integration tests.
    ///
    /// Gated behind `test` or `integration_tests` — not compiled in production.
    #[cfg(any(test, feature = "integration_tests"))]
    pub fn new_with_base_url(api_key: impl Into<String>, base_url: impl Into<String>) -> Self {
        Self {
            api_key: api_key.into(),
            base_url: base_url.into(),
            http: CascadeHttpClient::new(),
        }
    }

    /// Build the Anthropic request body from a `CompletionRequest`.
    ///
    /// System messages are extracted from `messages` (or from `request.system`)
    /// and placed in the Anthropic top-level `system` field.  Only `user` and
    /// `assistant` turns are sent in `messages`.
    ///
    /// `options` controls prompt-cache breakpoint insertion — see
    /// [`CacheStrategy`] and [`AnthropicRequestOptions`] for the gating
    /// rules. Defaulted `AnthropicRequestOptions` (`CacheStrategy::Off`)
    /// reproduces the pre-caching request shape exactly.
    fn build_request(
        &self,
        req: &CompletionRequest,
        stream: bool,
        options: AnthropicRequestOptions,
    ) -> AnthropicRequest {
        // Extract system prompt: prefer explicit `req.system`, else pull system
        // messages out of the messages array.
        let system_text = if let Some(s) = &req.system {
            Some(s.clone())
        } else {
            let sys: String = req
                .messages
                .iter()
                .filter(|m| m.role == MessageRole::System)
                .map(|m| m.content.as_str())
                .collect::<Vec<_>>()
                .join("\n");
            if sys.is_empty() {
                None
            } else {
                Some(sys)
            }
        };

        // Only user/assistant messages go into the Anthropic messages array.
        let messages: Vec<AnthropicMessage> = req
            .messages
            .iter()
            .filter(|m| m.role != MessageRole::System)
            .map(|m| AnthropicMessage {
                role: match m.role {
                    MessageRole::User => "user",
                    MessageRole::Assistant => "assistant",
                    MessageRole::System => unreachable!("system filtered above"),
                },
                content: m.content.clone(),
            })
            .collect();

        let should_cache = self.should_emit_cache_breakpoint(system_text.as_deref(), options);

        let system = system_text.map(|text| {
            let cache_control = should_cache.then(CacheControl::ephemeral);
            vec![AnthropicSystemBlock {
                block_type: "text",
                text,
                cache_control,
            }]
        });

        AnthropicRequest {
            model: req.model.clone(),
            max_tokens: req.max_tokens.unwrap_or(DEFAULT_MAX_TOKENS),
            messages,
            system,
            temperature: req.temperature,
            stream: if stream { Some(true) } else { None },
        }
    }

    /// Decide whether `build_request` should attach a `cache_control`
    /// breakpoint to the end of the system block, per [`CacheStrategy`].
    ///
    /// Both gates must hold for `Auto`:
    /// 1. `options.reuse_expected` — caller signals this prefix will be
    ///    replayed within the cache TTL (break-even guardrail).
    /// 2. The system-prompt length clears [`MIN_CACHEABLE_PREFIX_CHARS`] —
    ///    otherwise Anthropic would silently no-op the cache write anyway.
    ///
    /// `debug_assert!`s [`prefix_is_stable`] as a best-effort development-time
    /// lint; this never affects release-build behavior.
    fn should_emit_cache_breakpoint(
        &self,
        system_text: Option<&str>,
        options: AnthropicRequestOptions,
    ) -> bool {
        if options.cache_strategy != CacheStrategy::Auto {
            return false;
        }
        if !options.reuse_expected {
            return false;
        }
        let Some(text) = system_text else {
            return false;
        };
        if text.len() < MIN_CACHEABLE_PREFIX_CHARS {
            return false;
        }

        debug_assert!(
            prefix_is_stable(text),
            "anthropic cache prefix looks unstable (contains today's date) — \
             this will invalidate the cache on every request"
        );

        true
    }
}

impl AnthropicAdapter {
    /// Send a blocking completion request, with explicit control over
    /// prompt-cache behavior via [`AnthropicRequestOptions`], returning both
    /// the provider-agnostic [`CompletionResponse`] and the
    /// Anthropic-specific [`AnthropicCacheUsage`] reported for this call.
    ///
    /// [`ProviderAdapter::complete`] delegates here with
    /// `AnthropicRequestOptions::default()` (i.e. `CacheStrategy::Off`) and
    /// discards the cache-usage half of the tuple, so existing callers going
    /// through the trait see no behavior change.
    pub async fn complete_with_options(
        &self,
        req: CompletionRequest,
        options: AnthropicRequestOptions,
    ) -> Result<(CompletionResponse, AnthropicCacheUsage), ProviderError> {
        let url = format!("{}{}", self.base_url, MESSAGES_PATH);
        let body = self.build_request(&req, false, options);
        let api_key = self.api_key.clone();

        let raw: AnthropicResponse = self
            .http
            .post_json(&url, &body, move |builder| {
                CascadeHttpClient::apply_api_key(builder, &api_key)
                    .header("anthropic-version", ANTHROPIC_VERSION)
            })
            .await?;

        // Extract text from the first text content block.
        let content = raw
            .content
            .into_iter()
            .find(|c| c.content_type == "text")
            .map(|c| c.text)
            .unwrap_or_default();

        let cache_usage = AnthropicCacheUsage::from(&raw.usage);

        let prompt_tokens = raw.usage.input_tokens;
        let completion_tokens = raw.usage.output_tokens;
        let usage = TokenUsage {
            prompt_tokens,
            completion_tokens,
            total_tokens: prompt_tokens + completion_tokens,
        };
        let cost_usd = compute_cost("anthropic", &raw.model, &usage);

        if cache_usage.cache_creation_input_tokens > 0 || cache_usage.cache_read_input_tokens > 0
        {
            tracing::debug!(
                cache_creation_input_tokens = cache_usage.cache_creation_input_tokens,
                cache_read_input_tokens = cache_usage.cache_read_input_tokens,
                "anthropic: prompt cache activity"
            );
        }

        Ok((
            CompletionResponse {
                content,
                model: raw.model,
                usage,
                cost_usd,
            },
            cache_usage,
        ))
    }
}

#[async_trait]
impl ProviderAdapter for AnthropicAdapter {
    /// Send a blocking completion request to `POST /v1/messages`.
    ///
    /// Uses `AnthropicRequestOptions::default()` (`CacheStrategy::Off`) — no
    /// `cache_control` breakpoints are emitted. Use
    /// [`AnthropicAdapter::complete_with_options`] directly for prompt
    /// caching or to read [`AnthropicCacheUsage`].
    async fn complete(&self, req: CompletionRequest) -> Result<CompletionResponse, ProviderError> {
        let (response, _cache_usage) = self
            .complete_with_options(req, AnthropicRequestOptions::default())
            .await?;
        Ok(response)
    }

    /// Stream a completion from `POST /v1/messages` using SSE.
    ///
    /// Uses `AnthropicRequestOptions::default()` (`CacheStrategy::Off`).
    async fn complete_stream(
        &self,
        req: CompletionRequest,
    ) -> Result<Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>, ProviderError>
    {
        let url = format!("{}{}", self.base_url, MESSAGES_PATH);
        let body = self.build_request(&req, true, AnthropicRequestOptions::default());
        let api_key = self.api_key.clone();

        let response = self
            .http
            .post_sse(&url, &body, move |builder| {
                CascadeHttpClient::apply_api_key(builder, &api_key)
                    .header("anthropic-version", ANTHROPIC_VERSION)
            })
            .await?;

        let (tx, rx) = mpsc::channel::<Result<StreamChunk, ProviderError>>(64);

        tokio::spawn(async move {
            // Collect the full body as bytes then parse SSE lines.
            // (wiremock delivers the full body; production sends chunked stream.)
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

            let mut stopped = false;
            for line in text.lines() {
                if let Some(payload) = CascadeHttpClient::parse_sse_line(line) {
                    match serde_json::from_str::<AnthropicSseEvent>(payload) {
                        Ok(event) => match event.event_type.as_str() {
                            "content_block_delta" => {
                                if let Some(delta) = event.delta {
                                    if delta.delta_type == "text_delta"
                                        && !delta.text.is_empty()
                                        && tx
                                            .send(Ok(StreamChunk {
                                                delta: delta.text,
                                                finish_reason: None,
                                            }))
                                            .await
                                            .is_err()
                                    {
                                        return;
                                    }
                                }
                            }
                            "message_stop" => {
                                stopped = true;
                                let _ = tx
                                    .send(Ok(StreamChunk {
                                        delta: String::new(),
                                        finish_reason: Some("end_turn".to_owned()),
                                    }))
                                    .await;
                                break;
                            }
                            // message_start, content_block_start, content_block_stop,
                            // message_delta — no chunk to yield.
                            _ => {}
                        },
                        Err(e) => {
                            tracing::warn!(
                                error = %e,
                                payload = %payload,
                                "anthropic SSE: failed to parse event"
                            );
                        }
                    }
                }
            }

            if !stopped {
                let _ = tx
                    .send(Err(ProviderError::StreamInterrupted(
                        "stream ended without message_stop".to_owned(),
                    )))
                    .await;
            }
        });

        Ok(Box::pin(ReceiverStream::new(rx)))
    }

    /// Return a hardcoded list of current Anthropic model identifiers.
    async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
        Ok(vec![
            ModelInfo {
                id: "claude-opus-4-5".to_owned(),
                name: "Claude Opus 4.5".to_owned(),
                context_window: 200_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "claude-sonnet-4-6".to_owned(),
                name: "Claude Sonnet 4.6".to_owned(),
                context_window: 200_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "claude-haiku-4-5".to_owned(),
                name: "Claude Haiku 4.5".to_owned(),
                context_window: 200_000,
                supports_streaming: true,
            },
            ModelInfo {
                id: "claude-3-opus".to_owned(),
                name: "Claude 3 Opus".to_owned(),
                context_window: 200_000,
                supports_streaming: true,
            },
        ])
    }

    /// Verify connectivity by GETting `/v1/models`.
    ///
    /// Returns `Ok(())` when the server responds with HTTP 200.
    async fn health_check(&self) -> Result<(), ProviderError> {
        let url = format!("{}{}", self.base_url, MODELS_PATH);
        let api_key = self.api_key.clone();
        self.http
            .get_json::<AnthropicModelsResponse, _>(&url, move |builder| {
                CascadeHttpClient::apply_api_key(builder, &api_key)
                    .header("anthropic-version", ANTHROPIC_VERSION)
            })
            .await?;
        Ok(())
    }

    /// Return static metadata about the Anthropic provider.
    fn provider_info(&self) -> ProviderInfo {
        ProviderInfo {
            id: "anthropic".to_owned(),
            name: "Anthropic Claude".to_owned(),
            auth_method: AuthMethod::ApiKey,
            base_url: BASE_URL.to_owned(),
            capabilities: ProviderCapabilities {
                supports_streaming: true,
                supports_vision: true,
                max_context_tokens: 200_000,
                supports_function_calling: false,
            },
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_helpers::test_support::{
        assert_completion_contract, fixture_json, make_anthropic_sse, HttpMethod,
        MockProviderServer,
    };
    use futures::StreamExt;
    use serde_json::json;
    use wiremock::matchers::{method, path};
    use wiremock::{Mock, ResponseTemplate};

    // ── happy-path complete() ─────────────────────────────────────────────────

    #[tokio::test]
    async fn complete_happy_path_from_fixture() {
        let ctx = MockProviderServer::start("anthropic").await;
        ctx.mount_json(
            HttpMethod::Post,
            MESSAGES_PATH,
            200,
            fixture_json("anthropic_complete"),
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let req = CompletionRequest::simple(
            "claude-3-5-sonnet-20241022",
            "What is the capital of France?",
        );
        let resp = adapter
            .complete(req)
            .await
            .expect("complete should succeed");

        assert_completion_contract(&resp.content, &resp.model, resp.usage.prompt_tokens);
        assert!(
            resp.usage.completion_tokens > 0,
            "completion_tokens must be positive"
        );
        assert_eq!(
            resp.usage.total_tokens,
            resp.usage.prompt_tokens + resp.usage.completion_tokens
        );
    }

    // ── system prompt extracted from messages array ───────────────────────────

    #[tokio::test]
    async fn complete_with_system_message_in_array() {
        let ctx = MockProviderServer::start("anthropic").await;
        ctx.mount_json(
            HttpMethod::Post,
            MESSAGES_PATH,
            200,
            fixture_json("anthropic_complete"),
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let mut req = CompletionRequest::simple("claude-3-5-sonnet-20241022", "Hello");
        req.messages
            .insert(0, crate::types::Message::system("Be concise."));
        let resp = adapter
            .complete(req)
            .await
            .expect("complete should succeed");
        assert!(!resp.content.is_empty());
    }

    // ── auth error (401) ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn complete_returns_auth_failed_on_401() {
        let ctx = MockProviderServer::start("anthropic").await;
        Mock::given(method("POST"))
            .and(path(MESSAGES_PATH))
            .respond_with(ResponseTemplate::new(401))
            .mount(&ctx.server)
            .await;

        let adapter = AnthropicAdapter::new_with_base_url("bad-key", ctx.base_url());
        let req = CompletionRequest::simple("claude-3-5-sonnet-20241022", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::AuthFailed(_)),
            "expected AuthFailed, got {err:?}"
        );
    }

    // ── rate-limit: retry-after header parsed ─────────────────────────────────

    #[tokio::test]
    async fn complete_returns_rate_limited_with_retry_after() {
        let ctx = MockProviderServer::start("anthropic").await;
        // Exhaust all retries with 429; retry-after: 0 so backoff is instant.
        Mock::given(method("POST"))
            .and(path(MESSAGES_PATH))
            .respond_with(
                ResponseTemplate::new(429)
                    .append_header("retry-after", "42")
                    .append_header("content-length", "0"),
            )
            .mount(&ctx.server)
            .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let req = CompletionRequest::simple("claude-3-5-sonnet-20241022", "hello");
        let err = adapter.complete(req).await.unwrap_err();

        assert!(
            matches!(err, ProviderError::RateLimited { .. }),
            "expected RateLimited, got {err:?}"
        );
    }

    // ── SSE stream parse ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn complete_stream_yields_chunks_from_sse() {
        let ctx = MockProviderServer::start("anthropic").await;
        let sse_body = make_anthropic_sse(&["The ", "capital ", "of ", "France is Paris."]);
        ctx.mount_raw(
            HttpMethod::Post,
            MESSAGES_PATH,
            200,
            "text/event-stream",
            sse_body,
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let req = CompletionRequest::simple("claude-3-5-sonnet-20241022", "What is the capital?");

        let stream = adapter
            .complete_stream(req)
            .await
            .expect("stream should open");
        let chunks: Vec<_> = stream.collect().await;

        let text_chunks: Vec<_> = chunks
            .iter()
            .filter_map(|r| r.as_ref().ok())
            .filter(|c| !c.delta.is_empty())
            .collect();

        assert!(
            text_chunks.len() >= 3,
            "expected at least 3 text chunks, got {}",
            text_chunks.len()
        );

        let stop_chunk = chunks
            .iter()
            .filter_map(|r| r.as_ref().ok())
            .find(|c| c.finish_reason.is_some());
        assert!(
            stop_chunk.is_some(),
            "expected a stop chunk with finish_reason"
        );
    }

    // ── available_models ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn available_models_returns_four_models() {
        let adapter = AnthropicAdapter::new("test-key");
        let models = adapter
            .available_models()
            .await
            .expect("models should load");
        assert_eq!(models.len(), 4, "expected exactly 4 models");
        assert!(models.iter().all(|m| m.context_window == 200_000));
        assert!(models.iter().all(|m| m.supports_streaming));
        let ids: Vec<&str> = models.iter().map(|m| m.id.as_str()).collect();
        assert!(ids.contains(&"claude-opus-4-5"));
        assert!(ids.contains(&"claude-sonnet-4-6"));
        assert!(ids.contains(&"claude-haiku-4-5"));
        assert!(ids.contains(&"claude-3-opus"));
    }

    // ── health_check ──────────────────────────────────────────────────────────

    #[tokio::test]
    async fn health_check_ok_on_200() {
        let ctx = MockProviderServer::start("anthropic").await;
        ctx.mount_json(
            HttpMethod::Get,
            MODELS_PATH,
            200,
            json!({"data": [{"id": "claude-3-5-sonnet-20241022"}]}),
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        adapter
            .health_check()
            .await
            .expect("health_check should succeed");
    }

    #[tokio::test]
    async fn health_check_returns_auth_failed_on_401() {
        let ctx = MockProviderServer::start("anthropic").await;
        Mock::given(method("GET"))
            .and(path(MODELS_PATH))
            .respond_with(ResponseTemplate::new(401))
            .mount(&ctx.server)
            .await;

        let adapter = AnthropicAdapter::new_with_base_url("bad-key", ctx.base_url());
        let err = adapter.health_check().await.unwrap_err();
        assert!(matches!(err, ProviderError::AuthFailed(_)));
    }

    // ── provider_info ─────────────────────────────────────────────────────────

    #[test]
    fn provider_info_fields() {
        let adapter = AnthropicAdapter::new("test-key");
        let info = adapter.provider_info();
        assert_eq!(info.id, "anthropic");
        assert_eq!(info.auth_method, AuthMethod::ApiKey);
        assert_eq!(info.base_url, BASE_URL);
        assert!(info.capabilities.supports_streaming);
        assert_eq!(info.capabilities.max_context_tokens, 200_000);
    }

    // ── API key never exposed ─────────────────────────────────────────────────

    #[test]
    fn api_key_not_in_provider_info_debug() {
        let adapter = AnthropicAdapter::new("sk-secret-1234567890");
        let info = adapter.provider_info();
        let s = format!("{info:?}");
        assert!(
            !s.contains("sk-secret"),
            "API key must not appear in provider_info debug output"
        );
    }

    // ── prompt caching: CacheStrategy gating ──────────────────────────────────

    fn req_with_system(system_len: usize) -> CompletionRequest {
        let mut req = CompletionRequest::simple("claude-opus-4-8", "Hello");
        req.system = Some("S".repeat(system_len));
        req
    }

    #[test]
    fn cache_auto_small_prefix_emits_no_cache_control() {
        let adapter = AnthropicAdapter::new("test-key");
        // Below MIN_CACHEABLE_PREFIX_CHARS even with reuse_expected: true.
        let req = req_with_system(100);
        let options = AnthropicRequestOptions {
            cache_strategy: CacheStrategy::Auto,
            reuse_expected: true,
        };
        let body = adapter.build_request(&req, false, options);

        let system = body.system.expect("system block should be present");
        assert_eq!(system.len(), 1);
        assert!(
            system[0].cache_control.is_none(),
            "small prefix must not get a cache_control breakpoint even under Auto+reuse_expected"
        );
    }

    #[test]
    fn cache_auto_large_prefix_without_reuse_expected_emits_no_cache_control() {
        let adapter = AnthropicAdapter::new("test-key");
        // Large enough, but reuse_expected is false — second gate fails.
        let req = req_with_system(MIN_CACHEABLE_PREFIX_CHARS + 1000);
        let options = AnthropicRequestOptions {
            cache_strategy: CacheStrategy::Auto,
            reuse_expected: false,
        };
        let body = adapter.build_request(&req, false, options);

        let system = body.system.expect("system block should be present");
        assert!(
            system[0].cache_control.is_none(),
            "large prefix without reuse_expected must not get a cache_control breakpoint"
        );
    }

    #[test]
    fn cache_auto_large_prefix_with_reuse_expected_emits_breakpoint_at_end_of_system() {
        let adapter = AnthropicAdapter::new("test-key");
        let req = req_with_system(MIN_CACHEABLE_PREFIX_CHARS + 1000);
        let options = AnthropicRequestOptions {
            cache_strategy: CacheStrategy::Auto,
            reuse_expected: true,
        };
        let body = adapter.build_request(&req, false, options);

        let system = body.system.expect("system block should be present");
        assert_eq!(
            system.len(),
            1,
            "breakpoint goes on the (only / last) system block"
        );
        assert!(
            system[0].cache_control.is_some(),
            "large prefix + reuse_expected under Auto must get a cache_control breakpoint"
        );
    }

    #[test]
    fn cache_off_never_emits_cache_control_regardless_of_size_or_reuse() {
        let adapter = AnthropicAdapter::new("test-key");
        let req = req_with_system(MIN_CACHEABLE_PREFIX_CHARS + 1000);
        // Off is the default; reuse_expected: true should not matter.
        let options = AnthropicRequestOptions {
            cache_strategy: CacheStrategy::Off,
            reuse_expected: true,
        };
        let body = adapter.build_request(&req, false, options);

        let system = body.system.expect("system block should be present");
        assert!(
            system[0].cache_control.is_none(),
            "CacheStrategy::Off must never emit cache_control"
        );
    }

    #[test]
    fn cache_options_default_is_off_and_reuse_not_expected() {
        let options = AnthropicRequestOptions::default();
        assert_eq!(options.cache_strategy, CacheStrategy::Off);
        assert!(!options.reuse_expected);
    }

    // ── prompt caching: determinism guard (prefix_is_stable) ─────────────────

    #[test]
    fn prefix_is_stable_rejects_todays_iso_date() {
        let today = current_iso_date();
        let unstable = format!("You are a helpful assistant. Today's date is {today}.");
        assert!(
            !prefix_is_stable(&unstable),
            "prefix containing today's ISO date must be flagged unstable"
        );
    }

    #[test]
    fn prefix_is_stable_accepts_static_prompt() {
        let stable = "You are a helpful assistant. Always answer concisely.";
        assert!(
            prefix_is_stable(stable),
            "a prompt with no date stamp must be flagged stable"
        );
    }

    // ── prompt caching: usage parsing ─────────────────────────────────────────

    #[test]
    fn anthropic_cache_usage_maps_both_fields_from_response_usage() {
        let usage = AnthropicUsage {
            input_tokens: 100,
            output_tokens: 50,
            cache_creation_input_tokens: 4096,
            cache_read_input_tokens: 12_288,
        };
        let cache_usage = AnthropicCacheUsage::from(&usage);
        assert_eq!(cache_usage.cache_creation_input_tokens, 4096);
        assert_eq!(cache_usage.cache_read_input_tokens, 12_288);
    }

    #[test]
    fn anthropic_cache_usage_defaults_to_zero_when_fields_absent() {
        // cache_creation_input_tokens / cache_read_input_tokens are #[serde(default)]
        // — a response with no cache activity must deserialize cleanly to 0, not error.
        let raw: AnthropicUsage =
            serde_json::from_value(json!({"input_tokens": 14, "output_tokens": 9}))
                .expect("usage without cache fields must still deserialize");
        let cache_usage = AnthropicCacheUsage::from(&raw);
        assert_eq!(cache_usage, AnthropicCacheUsage::default());
    }

    #[tokio::test]
    async fn complete_with_options_surfaces_cache_usage_from_response() {
        let ctx = MockProviderServer::start("anthropic").await;
        ctx.mount_json(
            HttpMethod::Post,
            MESSAGES_PATH,
            200,
            json!({
                "id": "msg_cache_01",
                "type": "message",
                "role": "assistant",
                "content": [{"type": "text", "text": "Cached answer."}],
                "model": "claude-opus-4-8",
                "stop_reason": "end_turn",
                "stop_sequence": null,
                "usage": {
                    "input_tokens": 12,
                    "output_tokens": 5,
                    "cache_creation_input_tokens": 0,
                    "cache_read_input_tokens": 16384
                }
            }),
        )
        .await;

        let adapter = AnthropicAdapter::new_with_base_url("test-key", ctx.base_url());
        let req = CompletionRequest::simple("claude-opus-4-8", "Hello again");
        let (resp, cache_usage) = adapter
            .complete_with_options(req, AnthropicRequestOptions::default())
            .await
            .expect("complete_with_options should succeed");

        assert_eq!(resp.content, "Cached answer.");
        assert_eq!(cache_usage.cache_creation_input_tokens, 0);
        assert_eq!(cache_usage.cache_read_input_tokens, 16_384);
    }

    // ── prompt caching: request-body serialization snapshot ──────────────────

    #[test]
    fn serialized_request_with_breakpoint_has_exact_cache_control_shape() {
        let adapter = AnthropicAdapter::new("test-key");
        let req = req_with_system(MIN_CACHEABLE_PREFIX_CHARS + 1000);
        let options = AnthropicRequestOptions {
            cache_strategy: CacheStrategy::Auto,
            reuse_expected: true,
        };
        let body = adapter.build_request(&req, false, options);

        let value = serde_json::to_value(&body).expect("request body must serialize");
        let cache_control = &value["system"][0]["cache_control"];
        assert_eq!(
            *cache_control,
            json!({"type": "ephemeral"}),
            "cache_control must serialize to the exact Anthropic wire shape"
        );
        assert_eq!(value["system"][0]["type"], json!("text"));
    }

    #[test]
    fn serialized_request_without_cache_control_omits_the_field() {
        let adapter = AnthropicAdapter::new("test-key");
        let req = req_with_system(100);
        let body = adapter.build_request(&req, false, AnthropicRequestOptions::default());

        let value = serde_json::to_value(&body).expect("request body must serialize");
        let system_block = value["system"][0]
            .as_object()
            .expect("system block must be an object");
        assert!(
            !system_block.contains_key("cache_control"),
            "cache_control key must be entirely absent (not null) when caching is off"
        );
    }
}
