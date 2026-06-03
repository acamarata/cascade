//! MCP Sampling — server-initiated LLM completion requests.
//!
//! The MCP 2025-03 sampling feature allows the *server* to ask the *client*
//! to perform an LLM completion on its behalf. This inverts the usual
//! server/client relationship: the Cascade daemon can request a model
//! completion using the client's credentials and capabilities without
//! needing its own API keys.
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

use serde::{Deserialize, Serialize};
use serde_json::Value;
use tracing::debug;

use cascade_types::error::{CascadeError, Result};

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
/// The client instance is held by `McpServer`. When the server needs a model
/// completion (e.g. for contextual chunking), it calls `create_message`
/// which sends a `sampling/createMessage` request to the connected MCP
/// client and waits for the response notification.
///
/// # Concurrency
///
/// Multiple sampling requests may be in flight simultaneously. Each gets
/// a unique correlation ID used to match the response notification to the
/// waiting future.
pub struct SamplingClient {
    /// Timeout in seconds for `sampling/createMessage` requests.
    /// Used by the real transport implementation (not yet wired in this stub).
    _timeout_secs: u64,
    // In the real impl this holds a map of pending request IDs to
    // `oneshot::Sender<SamplingResponse>` channels. The notification
    // handler resolves them when the client's response arrives.
}

impl SamplingClient {
    pub fn new() -> Self {
        Self { _timeout_secs: 30 }
    }

    pub fn with_timeout(secs: u64) -> Self {
        Self {
            _timeout_secs: secs,
        }
    }

    /// Request a model completion from the MCP client.
    ///
    /// Sends `sampling/createMessage` and waits up to `timeout_secs` for the
    /// client to respond. Returns the assistant message text on success.
    ///
    /// # Errors
    ///
    /// - `CascadeError::Timeout` if the client does not respond in time.
    /// - `CascadeError::ConfigParse` if the response is malformed.
    pub async fn create_message(&self, params: &Value) -> Result<Value> {
        let req: SamplingRequest =
            serde_json::from_value(params.clone()).map_err(|e| CascadeError::ConfigParse {
                path: "<sampling-params>".into(),
                detail: e.to_string(),
            })?;

        debug!(max_tokens = req.max_tokens, "sampling/createMessage");

        // Stub: in the real impl, serialize `req` and write it to the
        // transport as a server-initiated request, then wait on a oneshot
        // channel keyed by request ID.
        //
        // For now return a placeholder response so the type system is happy.
        Ok(serde_json::json!({
            "role": "assistant",
            "content": { "type": "text", "text": "[sampling not yet wired to transport]" },
            "model": "unknown",
            "stopReason": "end_turn"
        }))
    }
}

impl Default for SamplingClient {
    fn default() -> Self {
        Self::new()
    }
}
