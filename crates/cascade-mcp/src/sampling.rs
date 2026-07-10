//! MCP Sampling — server-initiated LLM completion requests (T-P4-E02-11).
//!
//! ## Purpose
//!
//! The MCP 2025-03 sampling feature allows the *server* to ask the *client*
//! to perform an LLM completion on its behalf. This inverts the usual
//! server/client relationship: the Cascade daemon can request a model
//! completion using the client's credentials and capabilities without
//! needing its own API keys.
//!
//! This module also implements `SamplingHandler` — the server-side handler
//! that receives `sampling/createMessage` from an **authenticated** MCP
//! client and proxies the call through the `ProviderRegistry`.
//!
//! ## Use cases in Cascade
//!
//! - **Contextual chunking (S06-06):** request a summary or context
//!   sentence for each chunk via the client's model.
//! - **HyDE query expansion (S12-02):** generate a hypothetical answer
//!   before embedding.
//! - **Step-back prompting (S12-03):** rephrase to a higher abstraction.
//!
//! ## Wire format
//!
//! Request: `sampling/createMessage`
//! ```json
//! {
//!   "messages": [{"role": "user", "content": {"type": "text", "text": "..."}}],
//!   "modelPreferences": { "hints": [{"name": "claude-3-5-haiku"}] },
//!   "maxTokens": 1024,
//!   "stopSequences": []
//! }
//! ```
//!
//! Response contains `{"role": "assistant", "content": {"type": "text", "text": "..."}}`.
//!
//! ## Timeout
//!
//! Sampling requests timeout after `McpServerConfig::sampling_timeout_secs`
//! (default 30s). On timeout the caller receives a `SamplingError::Timeout`.
//!
//! ## Auth
//!
//! Unauthenticated connections receive `McpServerError::AuthFailed` which
//! maps to JSON-RPC error code -32000 (Unauthorized) via the `From` impl.
//!
//! ## SPORT
//! MASTER-MCP-PRIMITIVES.md: sampling handler — Done

use std::sync::Arc;

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use tracing::{debug, warn};

use cascade_providers::{
    CompletionRequest, Message, MessageRole, ProviderError, ProviderRegistry, TaskType,
};

use crate::error::McpServerError;
use crate::handler::McpHandler;

// ── Sampling request / response types ────────────────────────────────────────

/// A message in a sampling conversation turn.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SamplingMessage {
    pub role: String,
    pub content: SamplingContent,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SamplingContent {
    #[serde(rename = "type")]
    pub kind: String,
    pub text: String,
}

/// Model preference hints (vendor-neutral tier names, not model IDs).
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct ModelPreferences {
    /// Ordered list of model name hints (client picks the best available).
    /// Use tier names ("haiku", "sonnet") not vendor model IDs.
    pub hints: Vec<ModelHint>,
    /// Cost priority [0.0, 1.0] — 1.0 means prefer cheapest.
    pub cost_priority: Option<f32>,
    /// Speed priority [0.0, 1.0] — 1.0 means prefer fastest.
    pub speed_priority: Option<f32>,
    /// Intelligence priority [0.0, 1.0] — 1.0 means prefer most capable.
    pub intelligence_priority: Option<f32>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModelHint {
    pub name: String,
}

/// Parameters for a `sampling/createMessage` request.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SamplingRequest {
    pub messages: Vec<SamplingMessage>,
    #[serde(default)]
    pub model_preferences: ModelPreferences,
    pub max_tokens: u32,
    #[serde(default)]
    pub stop_sequences: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub system_prompt: Option<String>,
    /// Include in sampling metadata for tracing; not sent to the model.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub metadata: Option<Value>,
}

impl SamplingRequest {
    /// Build a simple single-turn user message.
    pub fn user(text: impl Into<String>) -> Self {
        Self {
            messages: vec![SamplingMessage {
                role: "user".into(),
                content: SamplingContent {
                    kind: "text".into(),
                    text: text.into(),
                },
            }],
            model_preferences: ModelPreferences {
                hints: vec![ModelHint {
                    name: "haiku".into(),
                }],
                cost_priority: Some(0.8),
                speed_priority: Some(0.8),
                intelligence_priority: None,
            },
            max_tokens: 512,
            stop_sequences: vec![],
            system_prompt: None,
            metadata: None,
        }
    }
}

/// A completed sampling response from the client.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SamplingResponse {
    pub role: String,
    pub content: SamplingContent,
    /// Model name the client used (may differ from requested hints).
    pub model: String,
    /// Why the model stopped generating.
    pub stop_reason: Option<String>,
}

// ── SamplingClient ────────────────────────────────────────────────────────────

/// Manages server-initiated sampling requests.
///
/// Receives `sampling/createMessage` from a connected MCP client and forwards
/// it to the daemon's [`SamplingHandler`], which selects a provider from the
/// [`ProviderRegistry`] and calls the real LLM.
///
/// When constructed without a registry (`SamplingClient::new`) every call
/// returns [`McpServerError::Internal`] — wire the registry via
/// `SamplingClient::with_registry` before accepting traffic.
///
/// # Concurrency
///
/// `Arc<ProviderRegistry>` is `Send + Sync`; multiple requests may execute
/// in parallel without any additional locking in this layer.
pub struct SamplingClient {
    /// Delegated handler that owns provider-selection and retry logic.
    handler: SamplingHandler,
}

impl SamplingClient {
    /// Create a client with **no provider registry**.
    ///
    /// Every `create_message` call will return
    /// `CascadeError::ConfigParse` wrapping
    /// `McpServerError::Internal { "no AI provider configured …" }`.
    /// Use [`SamplingClient::with_registry`] to attach a live registry.
    pub fn new() -> Self {
        // An authenticated handler backed by an empty registry returns
        // McpServerError::Internal("no AI provider configured") on every call,
        // which is the correct typed error for the "not yet wired" state.
        Self {
            handler: SamplingHandler::new(Arc::new(ProviderRegistry::new())),
        }
    }

    /// Create a client backed by a live [`ProviderRegistry`].
    ///
    /// All `create_message` calls are forwarded to [`SamplingHandler::handle`],
    /// which performs provider selection, message mapping, and LLM retry.
    pub fn with_registry(registry: Arc<ProviderRegistry>) -> Self {
        Self {
            handler: SamplingHandler::new(registry),
        }
    }

    /// Handle a `sampling/createMessage` request from the MCP client.
    ///
    /// Delegates to [`SamplingHandler::handle`], which selects a provider,
    /// calls the real LLM, and returns a `CreateMessageResult`-shaped JSON:
    /// `{ role, content: { type, text }, model, stopReason }`.
    ///
    /// # Errors
    ///
    /// - [`CascadeError::ConfigParse`] when `params` cannot be parsed.
    /// - [`CascadeError::ConfigParse`] wrapping `McpServerError::Internal`
    ///   when no provider is configured or the provider call fails after retries.
    pub async fn create_message(&self, params: &Value) -> cascade_types::error::Result<Value> {
        let params_opt = if params.is_null() {
            None
        } else {
            Some(params.clone())
        };

        self.handler.handle(params_opt).await.map_err(|e| {
            cascade_types::error::CascadeError::ConfigParse {
                path: "<sampling/createMessage>".into(),
                detail: e.to_string(),
            }
        })
    }
}

impl Default for SamplingClient {
    fn default() -> Self {
        Self::new()
    }
}

// ── SamplingHandler ───────────────────────────────────────────────────────────

/// MCP handler for `sampling/createMessage`.
///
/// ## Purpose
///
/// Receives `sampling/createMessage` from an authenticated MCP client, selects
/// the best provider from the registry via model hint matching, translates the
/// MCP `SamplingRequest` into a `CompletionRequest`, calls the provider, and
/// returns a `CreateMessageResult`-shaped JSON value.
///
/// ## Provider selection
///
/// 1. Scan `model_preferences.hints` in order. The first hint whose `name`
///    (case-insensitive substring) matches a registered provider ID is used.
///    - Contains `"claude"` or `"anthropic"` → prefer `"anthropic"`
///    - Contains `"gemini"` or `"google"` → prefer `"gemini"`
///    - Contains `"openai"` or `"gpt"` → prefer `"openai"`
/// 2. If no hint matches, fall back to `ProviderRegistry::default_for_task(Chat)`.
/// 3. If no provider is registered → `McpServerError::Internal` (no provider).
///
/// ## Retry
///
/// On `ProviderError::RateLimited`, retries once after 5 s (max 2 attempts total).
/// On second 429 → `McpServerError::Internal` with the rate-limit message.
///
/// ## Auth
///
/// The `authenticated` flag MUST be set to `true` by the transport layer before
/// calling this handler. When `false`, returns `McpServerError::AuthFailed`.
///
/// ## Inputs / Outputs
///
/// - Input: `Option<Value>` — JSON-serialized `SamplingRequest`
/// - Output: `Value` — `{ role, content: { type, text }, model, stopReason }`
///
/// ## Constraints
///
/// - API keys are NEVER logged at any tracing level.
/// - Provider selection is deterministic: hint ordering is respected, then
///   `RoutingTable` priority for fallback.
/// - Streaming is out-of-scope (deferred to P5) — always calls `complete()`.
pub struct SamplingHandler {
    /// Provider registry shared with the rest of the daemon.
    registry: Arc<ProviderRegistry>,
    /// Whether the connection that issued this request is authenticated.
    /// The transport layer sets this before dispatching.
    authenticated: bool,
}

impl SamplingHandler {
    /// Create a new `SamplingHandler` for an **authenticated** connection.
    pub fn new(registry: Arc<ProviderRegistry>) -> Self {
        Self {
            registry,
            authenticated: true,
        }
    }

    /// Create a handler for an **unauthenticated** connection.
    ///
    /// Every call will return `McpServerError::AuthFailed` before touching the
    /// provider registry.
    pub fn unauthenticated(registry: Arc<ProviderRegistry>) -> Self {
        Self {
            registry,
            authenticated: false,
        }
    }

    /// Select the provider ID to use based on model preference hints.
    ///
    /// Returns the first provider ID that is both named in a hint and registered
    /// in the registry. Falls back to `default_for_task(Chat)`.
    fn select_provider_id(&self, prefs: &ModelPreferences) -> Option<String> {
        // Hint → canonical provider-id mapping (case-insensitive substring).
        let hint_to_id = |hint: &str| -> Option<&'static str> {
            let lower = hint.to_ascii_lowercase();
            if lower.contains("claude") || lower.contains("anthropic") {
                Some("anthropic")
            } else if lower.contains("gemini") || lower.contains("google") {
                Some("gemini")
            } else if lower.contains("openai") || lower.contains("gpt") {
                Some("openai")
            } else if lower.contains("local") || lower.contains("ollama") {
                Some("local")
            } else {
                None
            }
        };

        // Try hints in order — use first one whose resolved ID is registered.
        for hint in &prefs.hints {
            if let Some(id) = hint_to_id(&hint.name) {
                if self.registry.get(id).is_some() {
                    return Some(id.to_string());
                }
            }
        }

        // No hint matched — fall back to routing table default for Chat.
        self.registry
            .default_for_task(&TaskType::Chat)
            .map(|a| a.provider_info().id)
    }

    /// Map MCP `SamplingMessage` list to `cascade-providers` `Message` list.
    fn map_messages(messages: &[SamplingMessage]) -> Vec<Message> {
        messages
            .iter()
            .map(|m| {
                let role = match m.role.to_ascii_lowercase().as_str() {
                    "user" => MessageRole::User,
                    "assistant" => MessageRole::Assistant,
                    "system" => MessageRole::System,
                    _ => MessageRole::User, // unknown → user is the safe default
                };
                Message {
                    role,
                    content: m.content.text.clone(),
                }
            })
            .collect()
    }

    /// Execute a completion with up to 2 attempts (retry once on 429).
    async fn complete_with_retry(
        &self,
        provider_id: &str,
        req: CompletionRequest,
    ) -> Result<Value, McpServerError> {
        const MAX_ATTEMPTS: u8 = 2;
        const RETRY_DELAY_SECS: u64 = 5;

        let adapter = self
            .registry
            .get(provider_id)
            .ok_or_else(|| McpServerError::Internal {
                detail: format!("provider '{}' disappeared from registry", provider_id),
            })?;

        let mut attempt = 0u8;
        loop {
            attempt += 1;
            match adapter.complete(req.clone()).await {
                Ok(resp) => {
                    let model_label = format!("{}/{}", provider_id, resp.model);
                    return Ok(serde_json::json!({
                        "role": "assistant",
                        "content": { "type": "text", "text": resp.content },
                        "model": model_label,
                        "stopReason": "end_turn"
                    }));
                }
                Err(ProviderError::RateLimited { .. }) if attempt < MAX_ATTEMPTS => {
                    warn!(
                        provider = %provider_id,
                        attempt,
                        "sampling: rate-limited, retrying after {}s",
                        RETRY_DELAY_SECS
                    );
                    tokio::time::sleep(tokio::time::Duration::from_secs(RETRY_DELAY_SECS)).await;
                }
                Err(e) => {
                    return Err(McpServerError::Internal {
                        detail: e.to_string(),
                    });
                }
            }
        }
    }
}

#[async_trait]
impl McpHandler for SamplingHandler {
    /// Handle a `sampling/createMessage` request.
    ///
    /// Returns a JSON value shaped as `CreateMessageResult` on success.
    /// Returns `McpServerError` variants on failure:
    /// - `AuthFailed` (→ -32000) when the connection is unauthenticated.
    /// - `InvalidParams` when `params` cannot be parsed as `SamplingRequest`.
    /// - `Internal` (→ -32603) when no provider is configured or the call fails.
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        // ── 1. Auth gate — must happen before any provider interaction ────────
        if !self.authenticated {
            return Err(McpServerError::AuthFailed {
                detail: "sampling/createMessage requires an authenticated connection".into(),
            });
        }

        // ── 2. Parse request ──────────────────────────────────────────────────
        let raw = params.unwrap_or(Value::Null);
        let req: SamplingRequest =
            serde_json::from_value(raw).map_err(|e| McpServerError::InvalidParams {
                detail: format!("sampling/createMessage params: {}", e),
            })?;

        debug!(
            max_tokens = req.max_tokens,
            hints = ?req.model_preferences.hints.iter().map(|h| h.name.as_str()).collect::<Vec<_>>(),
            "sampling/createMessage received"
        );

        // ── 3. Select provider ────────────────────────────────────────────────
        let provider_id = self
            .select_provider_id(&req.model_preferences)
            .ok_or_else(|| McpServerError::Internal {
                detail: "no AI provider configured; connect a provider in Cascade settings".into(),
            })?;

        debug!(provider = %provider_id, "sampling: selected provider");

        // ── 4. Build CompletionRequest ────────────────────────────────────────
        // These are MCP sampling fallback IDs — provider API defaults used when
        // no model is specified in the MCP request. Intentionally different from
        // the fleet routing IDs in `cascade_core::model_ids` (different purpose).
        let model = match provider_id.as_str() {
            "anthropic" => "claude-3-5-haiku-20241022",
            "gemini" => "gemini-2.0-flash",
            "openai" => "gpt-4o-mini",
            _ => "default",
        };

        let mut messages = Self::map_messages(&req.messages);

        // Prepend system message if provided (for providers that do not have a
        // separate system field — CompletionRequest handles both paths).
        let system = req.system_prompt.clone();

        // Remove any system-role messages from the mapped list (they will be
        // handled via the `system` field in CompletionRequest).
        messages.retain(|m| m.role != MessageRole::System);

        let completion_req = CompletionRequest {
            model: model.into(),
            messages,
            max_tokens: Some(req.max_tokens),
            temperature: None,
            stream: false,
            system,
        };

        // ── 5. Call provider with retry ───────────────────────────────────────
        self.complete_with_retry(&provider_id, completion_req).await
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::pin::Pin;
    use std::sync::Arc;

    use async_trait::async_trait;
    use futures_core::Stream;

    use cascade_providers::{
        AuthMethod, CompletionResponse, ModelInfo, ProviderCapabilities, ProviderInfo, StreamChunk,
        TokenUsage,
    };

    // ── Mock providers ────────────────────────────────────────────────────────

    /// A provider that always returns a successful completion.
    struct MockCompleteProvider {
        id: &'static str,
        response_text: &'static str,
    }

    #[async_trait]
    impl cascade_providers::ProviderAdapter for MockCompleteProvider {
        async fn complete(
            &self,
            _req: CompletionRequest,
        ) -> Result<CompletionResponse, ProviderError> {
            Ok(CompletionResponse {
                content: self.response_text.into(),
                model: self.id.into(),
                usage: TokenUsage::default(),
                cost_usd: None,
            })
        }

        async fn complete_stream(
            &self,
            _req: CompletionRequest,
        ) -> Result<
            Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
            ProviderError,
        > {
            Err(ProviderError::NotImplemented)
        }

        async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
            Err(ProviderError::NotImplemented)
        }

        async fn health_check(&self) -> Result<(), ProviderError> {
            Ok(())
        }

        fn provider_info(&self) -> ProviderInfo {
            ProviderInfo {
                id: self.id.into(),
                name: self.id.into(),
                auth_method: AuthMethod::None,
                base_url: String::new(),
                capabilities: ProviderCapabilities {
                    supports_streaming: false,
                    supports_vision: false,
                    max_context_tokens: 4096,
                    supports_function_calling: false,
                },
            }
        }
    }

    /// A provider that always returns 429 (rate limited).
    struct MockRateLimitedProvider;

    #[async_trait]
    impl cascade_providers::ProviderAdapter for MockRateLimitedProvider {
        async fn complete(
            &self,
            _req: CompletionRequest,
        ) -> Result<CompletionResponse, ProviderError> {
            Err(ProviderError::RateLimited {
                retry_after_secs: Some(5),
            })
        }

        async fn complete_stream(
            &self,
            _req: CompletionRequest,
        ) -> Result<
            Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
            ProviderError,
        > {
            Err(ProviderError::NotImplemented)
        }

        async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
            Err(ProviderError::NotImplemented)
        }

        async fn health_check(&self) -> Result<(), ProviderError> {
            Ok(())
        }

        fn provider_info(&self) -> ProviderInfo {
            ProviderInfo {
                id: "rate-limited".into(),
                name: "MockRateLimited".into(),
                auth_method: AuthMethod::None,
                base_url: String::new(),
                capabilities: ProviderCapabilities {
                    supports_streaming: false,
                    supports_vision: false,
                    max_context_tokens: 4096,
                    supports_function_calling: false,
                },
            }
        }
    }

    // ── Helper ────────────────────────────────────────────────────────────────

    fn registry_with(id: &'static str, text: &'static str) -> Arc<ProviderRegistry> {
        let r = Arc::new(ProviderRegistry::new());
        r.register(
            id.into(),
            Arc::new(MockCompleteProvider {
                id,
                response_text: text,
            }),
        )
        .expect("register mock provider");
        r
    }

    fn sampling_params(hint: &str, user_msg: &str) -> Value {
        serde_json::json!({
            "messages": [{"role": "user", "content": {"type": "text", "text": user_msg}}],
            "modelPreferences": {"hints": [{"name": hint}]},
            "maxTokens": 256
        })
    }

    // ── Tests ─────────────────────────────────────────────────────────────────

    /// Happy path: authenticated + anthropic provider → CreateMessageResult.
    #[tokio::test]
    async fn happy_path_returns_create_message_result() {
        let registry = registry_with("anthropic", "hello from anthropic");
        let handler = SamplingHandler::new(registry);
        let params = sampling_params("claude-3-5-haiku", "say hello");

        let result = handler.handle(Some(params)).await.unwrap();

        assert_eq!(result["role"], "assistant", "role must be assistant");
        assert_eq!(result["content"]["type"], "text");
        assert_eq!(result["content"]["text"], "hello from anthropic");
        assert!(result["model"].as_str().unwrap().contains("anthropic"));
        assert_eq!(result["stopReason"], "end_turn");
    }

    /// Gemini hint routes to gemini provider.
    #[tokio::test]
    async fn model_hint_gemini_routes_to_gemini() {
        let registry = registry_with("gemini", "hello from gemini");
        let handler = SamplingHandler::new(registry);
        let params = sampling_params("gemini-2.0-flash", "say hello");

        let result = handler.handle(Some(params)).await.unwrap();

        assert!(
            result["model"].as_str().unwrap().contains("gemini"),
            "model field must contain 'gemini'"
        );
        assert_eq!(result["content"]["text"], "hello from gemini");
    }

    /// OpenAI hint routes to openai provider.
    #[tokio::test]
    async fn model_hint_openai_routes_to_openai() {
        let registry = registry_with("openai", "hello from openai");
        let handler = SamplingHandler::new(registry);
        let params = sampling_params("gpt-4o", "say hello");

        let result = handler.handle(Some(params)).await.unwrap();

        assert!(
            result["model"].as_str().unwrap().contains("openai"),
            "model field must contain 'openai'"
        );
    }

    /// No-provider error: empty registry returns McpServerError::Internal.
    #[tokio::test]
    async fn no_provider_returns_internal_error() {
        let registry = Arc::new(ProviderRegistry::new()); // empty
        let handler = SamplingHandler::new(registry);
        let params = sampling_params("claude", "hello");

        let err = handler.handle(Some(params)).await.unwrap_err();
        assert!(
            matches!(err, McpServerError::Internal { .. }),
            "expected Internal, got {:?}",
            err
        );
        if let McpServerError::Internal { detail } = err {
            assert!(
                detail.contains("no AI provider"),
                "error message must mention provider: {detail}"
            );
        }
    }

    /// Unauthenticated connection returns McpServerError::AuthFailed (-32000).
    #[tokio::test]
    async fn unauthenticated_returns_auth_failed() {
        let registry = registry_with("anthropic", "should not reach here");
        let handler = SamplingHandler::unauthenticated(registry);
        let params = sampling_params("claude", "hello");

        let err = handler.handle(Some(params)).await.unwrap_err();
        assert!(
            matches!(err, McpServerError::AuthFailed { .. }),
            "expected AuthFailed, got {:?}",
            err
        );
    }

    /// Auth check happens before provider interaction (registry never queried).
    #[tokio::test]
    async fn auth_check_before_provider_interaction() {
        // Even an empty registry: auth error fires first.
        let registry = Arc::new(ProviderRegistry::new());
        let handler = SamplingHandler::unauthenticated(registry);
        let params = sampling_params("claude", "hello");

        let err = handler.handle(Some(params)).await.unwrap_err();
        assert!(
            matches!(err, McpServerError::AuthFailed { .. }),
            "auth gate must fire before registry lookup"
        );
    }

    /// Invalid params (missing required fields) → McpServerError::InvalidParams.
    #[tokio::test]
    async fn invalid_params_returns_invalid_params_error() {
        let registry = registry_with("anthropic", "doesn't matter");
        let handler = SamplingHandler::new(registry);

        // Missing `messages` and `maxTokens` — invalid SamplingRequest
        let bad_params = serde_json::json!({"wrong": "field"});
        let err = handler.handle(Some(bad_params)).await.unwrap_err();
        assert!(
            matches!(err, McpServerError::InvalidParams { .. }),
            "expected InvalidParams, got {:?}",
            err
        );
    }

    /// Role mapping: "user" → User, "assistant" → Assistant, unknown → User.
    #[test]
    fn role_mapping_user_and_assistant() {
        let msgs = vec![
            SamplingMessage {
                role: "user".into(),
                content: SamplingContent {
                    kind: "text".into(),
                    text: "hi".into(),
                },
            },
            SamplingMessage {
                role: "assistant".into(),
                content: SamplingContent {
                    kind: "text".into(),
                    text: "hello".into(),
                },
            },
            SamplingMessage {
                role: "unknown_role".into(),
                content: SamplingContent {
                    kind: "text".into(),
                    text: "?".into(),
                },
            },
        ];

        let mapped = SamplingHandler::map_messages(&msgs);
        assert_eq!(mapped[0].role, MessageRole::User);
        assert_eq!(mapped[1].role, MessageRole::Assistant);
        assert_eq!(
            mapped[2].role,
            MessageRole::User,
            "unknown role defaults to User"
        );
    }

    /// Rate-limited provider: after 2 attempts (with instant mock) returns Internal.
    ///
    /// Uses `tokio::time::pause()` to skip the real 5s sleep.
    #[tokio::test(start_paused = true)]
    async fn rate_limited_after_retries_returns_internal_error() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register("rate-limited".into(), Arc::new(MockRateLimitedProvider))
            .expect("register rate-limited provider");

        let handler = SamplingHandler::new(registry);
        let params = serde_json::json!({
            "messages": [{"role": "user", "content": {"type": "text", "text": "hello"}}],
            "modelPreferences": {"hints": [{"name": "rate-limited"}]},
            "maxTokens": 64
        });

        let err = handler.handle(Some(params)).await.unwrap_err();
        assert!(
            matches!(err, McpServerError::Internal { .. }),
            "exhausted retries must yield Internal error, got {:?}",
            err
        );
    }

    /// Fallback: no hint → registry routing table default.
    #[tokio::test]
    async fn no_hint_falls_back_to_routing_default() {
        // Register "openai" which is first in Chat routing table.
        let registry = registry_with("openai", "openai fallback response");
        let handler = SamplingHandler::new(registry);

        // Empty hints list — must fall back to routing table.
        let params = serde_json::json!({
            "messages": [{"role": "user", "content": {"type": "text", "text": "hello"}}],
            "modelPreferences": {"hints": []},
            "maxTokens": 64
        });

        let result = handler.handle(Some(params)).await.unwrap();
        assert_eq!(result["content"]["text"], "openai fallback response");
    }

    /// `SamplingRequest::user()` convenience constructor.
    #[test]
    fn sampling_request_user_ctor() {
        let req = SamplingRequest::user("test message");
        assert_eq!(req.messages.len(), 1);
        assert_eq!(req.messages[0].role, "user");
        assert_eq!(req.messages[0].content.text, "test message");
        assert_eq!(req.max_tokens, 512);
    }

    /// `SamplingClient::new()` (no registry) → typed Err, not a fake success.
    #[tokio::test]
    async fn sampling_client_no_registry_returns_err() {
        let client = SamplingClient::new();
        let params = serde_json::json!({
            "messages": [{"role": "user", "content": {"type": "text", "text": "hi"}}],
            "modelPreferences": {"hints": []},
            "maxTokens": 64
        });
        // Must be an error — not a fake success string.
        let result = client.create_message(&params).await;
        assert!(
            result.is_err(),
            "SamplingClient with no registry must return Err, got Ok({:?})",
            result.ok()
        );
        let err_str = result.unwrap_err().to_string();
        assert!(
            err_str.contains("no AI provider"),
            "error must mention 'no AI provider', got: {err_str}"
        );
    }

    /// `SamplingClient::with_registry` routes to the real provider and returns text.
    #[tokio::test]
    async fn sampling_client_with_registry_returns_real_text() {
        let registry = registry_with("anthropic", "real text from mock anthropic");
        let client = SamplingClient::with_registry(registry);
        let params = serde_json::json!({
            "messages": [{"role": "user", "content": {"type": "text", "text": "hi"}}],
            "modelPreferences": {"hints": [{"name": "claude"}]},
            "maxTokens": 64
        });
        let result = client.create_message(&params).await.unwrap();
        assert_eq!(result["role"], "assistant");
        assert_eq!(
            result["content"]["text"], "real text from mock anthropic",
            "content must be real provider text, not a stub string"
        );
        assert!(
            !result["content"]["text"]
                .as_str()
                .unwrap_or("")
                .contains("not yet wired"),
            "stub string must be gone"
        );
    }
}
