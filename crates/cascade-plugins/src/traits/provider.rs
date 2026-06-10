//! Provider plugin trait — host-side plugin-facing API.
//!
//! Purpose: define the `ProviderPlugin` trait that a WASM plugin must satisfy to
//!   be registered as an LLM/AI provider in the cascade-plugins host. Covers both
//!   synchronous completion and streaming delta responses.
//! Inputs: `ProviderRequest` — messages, model hint, temperature, max_tokens.
//! Outputs: `ProviderResponse` (complete) or stream of `ProviderDelta` (streaming).
//! Constraints: all public types are `Serialize + Deserialize`; trait is object-safe;
//!   `Send + Sync + 'static` bounds required; stream() returns a boxed futures Stream.
//! SPORT: cascade-plugins / provider plugin trait (T-P4-E03-02)

use async_trait::async_trait;
use futures::stream::BoxStream;
use serde::{Deserialize, Serialize};
use thiserror::Error;

// ── I/O shapes ────────────────────────────────────────────────────────────────

/// Role of a message in the conversation.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum MessageRole {
    /// System-level instruction.
    System,
    /// Human/user turn.
    User,
    /// Assistant/model turn.
    Assistant,
}

/// A single message in a conversation thread.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Message {
    /// The role of the participant who sent this message.
    pub role: MessageRole,
    /// The text content of the message.
    pub content: String,
}

/// Request sent to a provider plugin's `complete()` or `stream()` method.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderRequest {
    /// Ordered conversation history including the latest user turn.
    pub messages: Vec<Message>,

    /// Provider-scoped model hint (e.g. `"qwen2.5-coder:7b"`, `"gpt-4o-mini"`).
    /// The plugin interprets this; the host does not validate it.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,

    /// Sampling temperature in `[0.0, 2.0]`. `None` means provider default.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature: Option<f32>,

    /// Maximum output tokens. `None` means provider default.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_tokens: Option<u32>,
}

/// Complete response from a `ProviderPlugin::complete()` call.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderResponse {
    /// Full text content of the model response.
    pub content: String,

    /// Model identifier the provider actually used (may differ from the hint).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model_used: Option<String>,

    /// Input token count (if the provider reported it).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub input_tokens: Option<u32>,

    /// Output token count (if the provider reported it).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub output_tokens: Option<u32>,
}

/// A single incremental text delta from a `ProviderPlugin::stream()` call.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderDelta {
    /// Incremental text fragment to append to the running response.
    pub text: String,

    /// If `true`, this is the final delta; streaming is complete.
    #[serde(default)]
    pub is_final: bool,
}

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors that a `ProviderPlugin` may return.
#[derive(Debug, Error, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ProviderPluginError {
    /// Network or API call failed.
    #[error("IO error in provider plugin: {message}")]
    Io { message: String },

    /// The request payload was malformed or rejected by the provider.
    #[error("parse/request error in provider plugin: {message}")]
    Parse { message: String },

    /// The provider is temporarily unavailable (rate limit, cold start, quota).
    #[error("provider plugin unavailable: {message}")]
    Unavailable { message: String },
}

// ── Capability declarations ───────────────────────────────────────────────────

/// Capability declaration for a `ProviderPlugin`.
///
/// Providers almost always require outbound network access. Memory limit is
/// set to 256 MiB (same as Retriever/Agent) because providers may cache
/// tokenizer tables or local model weights.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderPluginCapabilities {
    /// Whether this plugin requires outbound network access (almost always `true`).
    #[serde(default = "always_true")]
    pub needs_net_outbound: bool,

    /// Requested WASM memory in MiB. Host may cap to `MEM_LARGE_MB` (256).
    #[serde(default = "default_provider_memory_mb")]
    pub max_memory_mb: u32,

    /// Whether this plugin supports the streaming `stream()` method.
    #[serde(default)]
    pub supports_streaming: bool,
}

fn always_true() -> bool {
    true
}
fn default_provider_memory_mb() -> u32 {
    256
}

impl Default for ProviderPluginCapabilities {
    fn default() -> Self {
        Self {
            needs_net_outbound: true,
            max_memory_mb: default_provider_memory_mb(),
            supports_streaming: false,
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Host-side trait a WASM LLM/AI provider plugin must satisfy.
///
/// Providers cover both synchronous complete and streaming delta responses.
/// Streaming is optional: if `capabilities().supports_streaming == false`, the
/// host will not call `stream()`.
///
/// # Object-safety
///
/// This trait is object-safe. Async methods use `async_trait`. The `stream()`
/// method returns `BoxStream` (a type-erased stream) to preserve object-safety.
///
/// # Example (mock implementation for tests)
///
/// ```rust
/// # use cascade_plugins::traits::provider::{ProviderPlugin, ProviderPluginCapabilities, ProviderPluginError, ProviderRequest, ProviderResponse, ProviderDelta, Message, MessageRole};
/// # use async_trait::async_trait;
/// # use futures::stream::{self, BoxStream};
/// struct EchoProvider;
///
/// #[async_trait]
/// impl ProviderPlugin for EchoProvider {
///     fn name(&self) -> &str { "echo-provider" }
///     fn capabilities(&self) -> ProviderPluginCapabilities { Default::default() }
///
///     async fn complete(&self, req: ProviderRequest) -> Result<ProviderResponse, ProviderPluginError> {
///         let last = req.messages.last().map(|m| m.content.clone()).unwrap_or_default();
///         Ok(ProviderResponse {
///             content: format!("echo: {last}"),
///             model_used: None,
///             input_tokens: None,
///             output_tokens: None,
///         })
///     }
///
///     fn stream<'a>(&'a self, _req: ProviderRequest) -> BoxStream<'a, Result<ProviderDelta, ProviderPluginError>> {
///         Box::pin(stream::empty())
///     }
/// }
/// ```
#[async_trait]
pub trait ProviderPlugin: Send + Sync + 'static {
    /// A stable, human-readable name for this plugin.
    fn name(&self) -> &str;

    /// Capability declaration — inspected at plugin registration time.
    fn capabilities(&self) -> ProviderPluginCapabilities;

    /// Perform a synchronous (non-streaming) completion.
    async fn complete(&self, req: ProviderRequest)
        -> Result<ProviderResponse, ProviderPluginError>;

    /// Perform a streaming completion, yielding incremental deltas.
    ///
    /// The host only calls this if `capabilities().supports_streaming == true`.
    /// Implementations that do not support streaming should return an empty stream.
    fn stream<'a>(
        &'a self,
        req: ProviderRequest,
    ) -> BoxStream<'a, Result<ProviderDelta, ProviderPluginError>>;
}

// ── Object-safety static assertion ───────────────────────────────────────────

fn _assert_object_safe(_: &dyn ProviderPlugin) {}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use futures::stream::{self, StreamExt};

    struct MockProvider;

    #[async_trait]
    impl ProviderPlugin for MockProvider {
        fn name(&self) -> &str {
            "mock-provider"
        }

        fn capabilities(&self) -> ProviderPluginCapabilities {
            ProviderPluginCapabilities {
                needs_net_outbound: false,
                max_memory_mb: 64,
                supports_streaming: true,
            }
        }

        async fn complete(
            &self,
            req: ProviderRequest,
        ) -> Result<ProviderResponse, ProviderPluginError> {
            if req.messages.is_empty() {
                return Err(ProviderPluginError::Parse {
                    message: "no messages".into(),
                });
            }
            let last = req.messages.last().unwrap().content.clone();
            Ok(ProviderResponse {
                content: format!("echo: {last}"),
                model_used: Some("mock-v1".into()),
                input_tokens: Some(5),
                output_tokens: Some(8),
            })
        }

        fn stream<'a>(
            &'a self,
            req: ProviderRequest,
        ) -> BoxStream<'a, Result<ProviderDelta, ProviderPluginError>> {
            let text = req
                .messages
                .last()
                .map(|m| m.content.clone())
                .unwrap_or_default();
            let deltas = vec![
                Ok(ProviderDelta {
                    text: text.clone(),
                    is_final: false,
                }),
                Ok(ProviderDelta {
                    text: String::new(),
                    is_final: true,
                }),
            ];
            Box::pin(stream::iter(deltas))
        }
    }

    #[tokio::test]
    async fn provider_plugin_complete_happy_path() {
        let plugin = MockProvider;
        let req = ProviderRequest {
            messages: vec![Message {
                role: MessageRole::User,
                content: "hello".into(),
            }],
            model: None,
            temperature: Some(0.7),
            max_tokens: Some(100),
        };
        let resp = plugin.complete(req).await.unwrap();
        assert!(resp.content.contains("echo: hello"));
        assert_eq!(resp.model_used, Some("mock-v1".into()));
        assert_eq!(resp.input_tokens, Some(5));
    }

    #[tokio::test]
    async fn provider_plugin_complete_error_path() {
        let plugin = MockProvider;
        let req = ProviderRequest {
            messages: vec![],
            model: None,
            temperature: None,
            max_tokens: None,
        };
        let err = plugin.complete(req).await.unwrap_err();
        assert!(matches!(err, ProviderPluginError::Parse { .. }));
    }

    #[tokio::test]
    async fn provider_plugin_stream() {
        let plugin = MockProvider;
        let req = ProviderRequest {
            messages: vec![Message {
                role: MessageRole::User,
                content: "stream test".into(),
            }],
            model: None,
            temperature: None,
            max_tokens: None,
        };
        let deltas: Vec<_> = plugin.stream(req).collect().await;
        assert_eq!(deltas.len(), 2);
        let first = deltas[0].as_ref().unwrap();
        assert_eq!(first.text, "stream test");
        assert!(!first.is_final);
        let last = deltas[1].as_ref().unwrap();
        assert!(last.is_final);
    }

    #[test]
    fn provider_request_serde_round_trip() {
        let req = ProviderRequest {
            messages: vec![
                Message {
                    role: MessageRole::System,
                    content: "You are helpful.".into(),
                },
                Message {
                    role: MessageRole::User,
                    content: "What is 2+2?".into(),
                },
            ],
            model: Some("qwen2.5-coder:7b".into()),
            temperature: Some(0.5),
            max_tokens: Some(200),
        };
        let json = serde_json::to_string(&req).unwrap();
        let decoded: ProviderRequest = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.messages.len(), 2);
        assert_eq!(decoded.messages[0].role, MessageRole::System);
        assert_eq!(decoded.model, Some("qwen2.5-coder:7b".into()));
    }

    #[test]
    fn provider_response_serde_round_trip() {
        let resp = ProviderResponse {
            content: "4".into(),
            model_used: Some("mock".into()),
            input_tokens: Some(10),
            output_tokens: Some(1),
        };
        let json = serde_json::to_string(&resp).unwrap();
        let decoded: ProviderResponse = serde_json::from_str(&json).unwrap();
        assert_eq!(decoded.content, "4");
        assert_eq!(decoded.input_tokens, Some(10));
    }

    #[test]
    fn provider_error_serde_round_trip() {
        let err = ProviderPluginError::Unavailable {
            message: "rate limited".into(),
        };
        let json = serde_json::to_string(&err).unwrap();
        let decoded: ProviderPluginError = serde_json::from_str(&json).unwrap();
        let msg = decoded.to_string();
        assert!(msg.contains("rate limited"));
    }

    #[test]
    fn trait_object_usable_as_box() {
        let _plugin: Box<dyn ProviderPlugin> = Box::new(MockProvider);
    }
}
