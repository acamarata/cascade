//! ChainFlow — a composable, declarative multi-step workflow DSL.
//!
//! Purpose: provide a Langchain-equivalent "chain" primitive so multi-step
//!   agent/LLM/tool pipelines can be expressed declaratively, loaded from YAML,
//!   and executed with the same approval-gate and budget guards as the ReAct
//!   executor.
//!
//! Inputs:
//!   - `ChainFlow` (struct): ordered list of `ChainStep` variants + named I/O map.
//!   - `ChainExecutor` (struct): built with injectable `ProviderRouter` +
//!     `ToolInvoker` + `AgentExecutor`; runs `ChainFlow` values.
//!   - YAML files under `<ai-folder>/library/chains/*.yaml`.
//!
//! Outputs:
//!   - `ChainResult` (struct): final context map + per-step traces.
//!   - `ChainError` (enum): budget exceeded, cycle, bad ref, tool approval park.
//!
//! Constraints:
//!   - `ChainStep` uses struct enum variants only; serde `camelCase` (repo rule).
//!   - `Outbound` tool steps park and return `ChainError::NeedsApproval` — never
//!     auto-execute (same gate as `AgentExecutor`).
//!   - Loop/depth guards: `max_steps` (default 50) and `max_depth` (default 8).
//!   - DAG validation: `ChainFlow::validate()` detects missing refs + cycles before run.
//!   - `ProviderRouter` and `ToolInvoker` are the same injectable traits from
//!     `executor.rs`; no extra wiring required.
//!
//! SPORT: cascade-agents / chain — E-P6-08

pub mod builtins;
pub mod executor;
pub mod helpers;
mod tests;
pub mod types;

// ── Public re-exports (preserve original API surface) ─────────────────────────

pub use types::{
    ChainContext, ChainError, ChainFlow, ChainResult, ChainStep, ChainStepTrace,
    ChainValidationError, CHAIN_DEFAULT_MAX_DEPTH, CHAIN_DEFAULT_MAX_STEPS, PARALLEL_CAP_MAX,
};

pub use executor::{parallel_concurrency_cap, ChainExecutor, ChainExecutorBuilder};

pub use builtins::{builtin_research_summarize_draft, builtin_triage_branch_respond};
