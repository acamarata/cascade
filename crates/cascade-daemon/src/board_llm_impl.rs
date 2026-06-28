//! ProviderBoardLlm — `BoardLlm` implementation backed by `ProviderRegistry`.
//!
//! Purpose: wire the daemon's live LLM provider pool into `board_debate` so
//!   opinions come from a real language model instead of the old "pending" stub.
//!
//! Inputs:
//!   - `Arc<ProviderRegistry>` — the shared provider registry (same instance
//!     used by the HTTP chat handler and `RegistryRouter`).
//!   - `role`, `topic`, `system` — forwarded from `BoardLlm::opine`.
//!
//! Outputs:
//!   - `Ok(String)` — the LLM's raw completion text.
//!   - `Err(String)` — any provider failure (no provider, network error, etc.).
//!
//! Constraints:
//!   - Zero-copy Arc share — no new work at construction, lazy on first call.
//!   - Uses `pick_for_chat(None)` (routing-table default) — no role-specific
//!     model routing for now; can be refined with `spec.model_pref` later.
//!   - Graceful degradation: if the registry is empty or all providers are
//!     unhealthy, `opine` returns `Err("no provider available")` so
//!     `board_debate` records `stance: "error"` rather than silently stalling.
//!
//! SPORT: cascade-daemon / board_llm_impl — agents-02

use std::sync::Arc;

use async_trait::async_trait;

use cascade_agents::board_llm::BoardLlm;
use cascade_agents::spec::AgentRole;
use cascade_providers::{CompletionRequest, Message, MessageRole, ProviderRegistry};

// ── ProviderBoardLlm ──────────────────────────────────────────────────────────

/// `BoardLlm` that dispatches through the daemon's `ProviderRegistry`.
///
/// Constructed once at daemon startup (or when `board_debate` is first needed)
/// and shared via `Arc`. Each `opine` call performs one non-streaming
/// `CompletionRequest` to the best available provider.
pub struct ProviderBoardLlm {
    registry: Arc<ProviderRegistry>,
}

impl ProviderBoardLlm {
    /// Construct a `ProviderBoardLlm` around the shared provider registry.
    pub fn new(registry: Arc<ProviderRegistry>) -> Self {
        Self { registry }
    }
}

#[async_trait]
impl BoardLlm for ProviderBoardLlm {
    /// Ask `role` to opine on `topic` using `system` as its persona prompt.
    ///
    /// Assembles a two-message conversation (system prompt + user ask) and
    /// sends it to the registry's best available provider.
    async fn opine(&self, role: &AgentRole, topic: &str, system: &str) -> Result<String, String> {
        let (adapter, provider_id) = self
            .registry
            .pick_for_chat(None)
            .await
            .ok_or_else(|| {
                format!(
                    "board_debate({:?}): no provider available — registry empty or all unhealthy",
                    role
                )
            })?;

        let messages = vec![
            Message {
                role: MessageRole::System,
                content: system.to_owned(),
            },
            Message {
                role: MessageRole::User,
                content: format!("Topic for board debate: {topic}"),
            },
        ];

        let req = CompletionRequest {
            // Use the provider_id as the model string; the adapter maps it to
            // its own default model (same pattern as RegistryRouter::step).
            model: provider_id,
            messages,
            max_tokens: Some(512),
            temperature: Some(0.3),
            stream: false,
            system: None,
        };

        let resp = adapter
            .complete(req)
            .await
            .map_err(|e| format!("board_debate({role:?}): provider error: {e}"))?;

        Ok(resp.content)
    }
}
