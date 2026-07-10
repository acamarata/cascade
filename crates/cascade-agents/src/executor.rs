//! AgentExecutor — multi-step ReAct dispatch loop.
//!
//! Purpose: run `AgentTask` values to completion via a bounded async queue +
//!   worker pool. Each task executes a ReAct-style loop: step → check grants →
//!   execute tools (or park for approval / error on denied) → feed results back
//!   → repeat until done or loop-guard trips.
//!
//! Inputs: `AgentTask` enqueued via `AgentExecutor::submit`.
//!
//! Outputs: task transitions (Running → Done/Failed); `ApprovalRequest` events
//!   for outbound tool calls; child tasks spawned and awaited.
//!
//! Constraints:
//!   - `ToolInvoker` and `ProviderRouter` are injectable traits — tests use mocks.
//!   - `AccessLevel::Outbound` → `NeedsApproval` → park in `PendingApproval`, never execute.
//!   - `Denied` → hard error; task transitions to `Failed`.
//!   - Loop guards: `max_steps`, total token budget.
//!   - Child tasks: an agent may request spawning a child via `StepOutcome` by
//!     returning a `ToolCall` with `tool_id == "cascade.spawn_child"`.
//!
//! SPORT: cascade-agents / executor — E-P6-02

mod engine;
mod store;
mod types;

#[cfg(test)]
mod tests;

// ── Public re-exports ─────────────────────────────────────────────────────────

pub use engine::{AgentExecutor, AgentExecutorBuilder, ChildTaskRequest};
pub use store::TaskStore;
pub use types::{
    ApprovalRequest, ExecutorError, ProviderRouter, ToolInvoker, DEFAULT_MAX_STEPS,
    DEFAULT_QUEUE_CAPACITY, DEFAULT_TOKEN_BUDGET, SUBAGENT_CONTEXT_PREFIX,
};
