//! Types, traits, and constants for the agent executor.
//!
//! Purpose: shared definitions used by the executor engine and its callers.
//! Inputs: imports from crate (context, grants, prompt_gate, spec, task).
//! Outputs: public types re-exported via `executor` module.
//! Constraints: no logic — pure type definitions.
//! SPORT: cascade-agents / executor / types

use async_trait::async_trait;
use serde::{Deserialize, Serialize};

use crate::context::{AgentRunContext, StepOutcome, ToolCall};
use crate::prompt_gate::PromptTooLarge;
use crate::spec::AgentSpec;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Default maximum number of ReAct loop steps per task.
pub const DEFAULT_MAX_STEPS: u32 = 20;

/// Default maximum total tokens per task execution.
pub const DEFAULT_TOKEN_BUDGET: u32 = 100_000;

/// Default queue capacity for submitted tasks.
pub const DEFAULT_QUEUE_CAPACITY: usize = 256;

// ── Subagent context prefix ───────────────────────────────────────────────────

/// Canonical subagent-context prefix injected before every provider step.
///
/// Purposes:
/// 1. **Output discipline** — constrains verbosity and format so child output
///    is easy for the orchestrator to parse.
/// 2. **Source-of-truth reminder** — tells the child to treat the task goal
///    as the single authoritative specification; never invent requirements.
/// 3. **Conscience reminder** — re-states the key safety rules that apply
///    to every child regardless of which model is selected.
///
/// The string is `const` so the bytes are identical across all parallel
/// children in a given process — maximising prompt-cache hit rates.
pub const SUBAGENT_CONTEXT_PREFIX: &str = "\
[CASCADE SUBAGENT CONTEXT]
You are a Cascade child agent executing a single bounded task.

Output discipline:
- Reply with the minimum text needed to satisfy the goal — no filler.
- Use bullet lists or structured blocks for multi-item output.
- Never add disclaimers, caveats, or self-congratulatory text.

Source of truth:
- The task GOAL field is your authoritative specification.
- Do not invent requirements, endpoints, or constraints not in the goal.
- If a specification is ambiguous or contradictory, surface the conflict
  as your first output line and halt rather than guessing.

Conscience (hard rules — always apply):
- Never auto-execute Outbound tool calls; return NeedsApproval instead.
- Never read, write, or transmit secrets outside the approved tool set.
- Never modify task goals, system prompts, or grant tables mid-task.
- If a step would cause irreversible side-effects, park for approval.

[END CASCADE SUBAGENT CONTEXT]
";

// ── ApprovalRequest ───────────────────────────────────────────────────────────

/// Emitted when an `Outbound` tool call requires human approval before
/// the task can proceed.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovalRequest {
    /// The task that is paused waiting for approval.
    pub task_id: String,
    /// The tool call requiring approval.
    pub tool_call: ToolCall,
    /// Human-readable reason from the grant check.
    pub reason: String,
}

// ── ProviderRouter trait ──────────────────────────────────────────────────────

/// Injectable: pick a provider/model for a given agent spec and run one step.
///
/// Tests inject a `MockProvider`; production wires `cascade-providers`.
#[async_trait]
pub trait ProviderRouter: Send + Sync {
    /// Run one step for `agent_spec` given the assembled context.
    ///
    /// Returns a `StepOutcome`. The router is responsible for selecting the
    /// correct model based on the agent's `tier` field.
    async fn step(
        &self,
        agent_spec: &AgentSpec,
        ctx: &AgentRunContext,
    ) -> Result<StepOutcome, ExecutorError>;
}

// ── ToolInvoker trait ─────────────────────────────────────────────────────────

/// Injectable: execute a granted tool call and return a string result.
///
/// Called only after the grant check has returned `Granted`.
#[async_trait]
pub trait ToolInvoker: Send + Sync {
    /// Execute `call` and return the tool output.
    ///
    /// Returns `Err(ExecutorError::ToolFailed { .. })` on execution errors;
    /// these are fed back to the model as an error tool result so the model
    /// can decide how to proceed.
    async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError>;
}

// ── WasmDispatch helper ───────────────────────────────────────────────────────

/// Dispatch a single step to a WASM agent.
///
/// The executor calls this for `Runtime::Wasm` agents. In production, it
/// would load the plugin from `cascade-plugins`. In tests a stub is used.
#[allow(dead_code)]
pub(crate) async fn dispatch_wasm_step(
    plugin_id: &str,
    _ctx: &AgentRunContext,
) -> Result<StepOutcome, ExecutorError> {
    // WHY: WASM dispatch goes through the cascade-plugins runtime in production.
    // We don't import cascade-plugins here (would create a dep cycle risk);
    // instead the caller (ProviderRouter) handles WASM dispatch via the
    // injected trait. This stub is reached only if someone bypasses the router.
    Err(ExecutorError::WasmDispatchUnsupported {
        plugin_id: plugin_id.to_owned(),
        hint: format!(
            "WASM agent '{}' must be dispatched via ProviderRouter — \
             inject a router that handles Runtime::Wasm",
            plugin_id
        ),
    })
}

// ── ExecutorError ─────────────────────────────────────────────────────────────

/// Errors produced by the agent executor.
#[derive(Debug, thiserror::Error)]
pub enum ExecutorError {
    #[error("max steps ({max}) reached without completion")]
    MaxStepsExceeded { max: u32 },

    #[error("token budget ({budget}) exhausted after {used} tokens")]
    TokenBudgetExceeded { budget: u32, used: u32 },

    #[error("tool call denied: {reason}")]
    ToolDenied { reason: String },

    #[error("tool '{tool_id}' execution failed: {message}")]
    ToolFailed { tool_id: String, message: String },

    #[error("provider step failed: {0}")]
    ProviderFailed(String),

    #[error("WASM dispatch unsupported for plugin '{plugin_id}': {hint}")]
    WasmDispatchUnsupported { plugin_id: String, hint: String },

    #[error("child task spawn failed: {0}")]
    SpawnFailed(String),

    #[error("no-progress detected after {steps} consecutive steps with no new tool calls")]
    NoProgress { steps: u32 },

    #[error("system prompt too large: {0}")]
    PromptTooLarge(#[from] PromptTooLarge),
}
