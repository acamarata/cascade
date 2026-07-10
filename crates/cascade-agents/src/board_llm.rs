//! BoardLlm — injectable LLM abstraction for board debate opinion gathering.
//!
//! Purpose: decouple `board_debate` from any concrete provider so it can be
//!   tested without a live LLM and wired to a real provider by the daemon.
//!
//! Inputs:
//!   - `AgentRole` — the board role asking the question.
//!   - `topic` — free-text motion or question being debated.
//!   - `system` — the role's soul/system prompt injected as context.
//!
//! Outputs:
//!   - `Ok(String)` — the LLM's raw prose response.
//!   - `Err(String)` — any failure (no provider, network, etc.).
//!
//! Constraints:
//!   - Object-safe: no associated types, no generic params.
//!   - `async_trait` for async object safety.
//!   - The crate uses `thiserror` not `anyhow`; errors are `String` here to
//!     avoid adding an `anyhow` dep to the trait surface.
//!
//! SPORT: cascade-agents / board_llm — agents-02

use async_trait::async_trait;

use crate::spec::AgentRole;

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Injectable LLM back-end for board debate opinion collection.
///
/// Implementors:
///  - `NoopBoardLlm` — always errors; used when no provider is configured.
///  - `cascade_daemon::board_llm_impl::ProviderBoardLlm` — routes to
///    the daemon's `ProviderRegistry`.
///  - Test `MockBoardLlm` (see `roster` tests) — returns scripted stances.
#[async_trait]
pub trait BoardLlm: Send + Sync {
    /// Ask `role` to opine on `topic`, using `system` as the persona context.
    ///
    /// Returns the LLM's raw prose response, which the caller interprets for
    /// stance classification.
    async fn opine(&self, role: &AgentRole, topic: &str, system: &str) -> Result<String, String>;
}

// ── NoopBoardLlm ──────────────────────────────────────────────────────────────

/// No-op implementation — returns an explicit error.
///
/// Used as the default when no provider is configured so callers see
/// `stance: "error"` with a clear message rather than silent "pending" stubs.
pub struct NoopBoardLlm;

#[async_trait]
impl BoardLlm for NoopBoardLlm {
    async fn opine(
        &self,
        _role: &AgentRole,
        _topic: &str,
        _system: &str,
    ) -> Result<String, String> {
        Err("no LLM provider configured for board debate".to_owned())
    }
}
