//! # cascade-agents
//!
//! Contract foundation and execution engine for the Cascade agent-orchestration runtime.
//!
//! ## Modules
//!
//! | Module           | Epic       | Purpose                                                 |
//! |------------------|------------|---------------------------------------------------------|
//! | [`spec`]         | E-P6-01    | `AgentSpec`, `AgentRole`, `Capability`, `Runtime`       |
//! | [`registry`]     | E-P6-01    | `AgentRegistry` — thread-safe store + routing           |
//! | [`task`]         | E-P6-01    | `AgentTask` — unit of agent work                        |
//! | [`grants`]       | E-P6-06    | `ToolGrant`, `AccessLevel`, `GrantDecision`             |
//! | [`tool_registry`]| E-P6-06    | `ToolRegistry` — per-agent tool provisioning            |
//! | [`library`]      | E-P6-09    | On-disk agent-library schema + `validate_agent_library` |
//! | [`context`]      | E-P6-03    | `AgentRunContext` builder + `StepOutcome` + `ContextProvider` |
//! | [`executor`]     | E-P6-02    | `AgentExecutor` — multi-step ReAct dispatch loop        |
//! | [`inbox`]        | E-P6-05    | `AgentInbox` — per-agent message queue + handoff + approval channel |
//! | [`chain`]        | E-P6-08    | `ChainFlow` DSL — declarative multi-step agent/LLM/tool pipelines |
//!
//! ## Design notes
//!
//! - `AgentTask` defines the seam between orchestrator and execution engine.
//! - `Outbound` access level always requires approval by default (safe default).
//! - `AgentExecutor` uses injectable `ProviderRouter` + `ToolInvoker` traits; tests
//!   inject deterministic mocks with no live LLM or network calls.
//! - All serde uses `camelCase` rename and STRUCT enum variants only (repo rule).

pub mod automation;
pub mod ceo;
pub mod chain;
pub mod context;
pub mod executor;
pub mod grants;
pub mod inbox;
pub mod library;
pub mod registry;
pub mod spec;
pub mod task;
pub mod tool_registry;

// ── Top-level re-exports ──────────────────────────────────────────────────────
pub use ceo::{
    CeoError, CeoOrchestrator, FounderDirective, FounderReport, MockPlanner, Plan, Planner,
    PlannerError, SessionState, SubGoal, SubTaskOutcome,
};

pub use context::{
    AgentRunContext, AgentRunContextBuilder, ContextMessage, ContextProvider, ContextRole,
    NoopContextProvider, StepOutcome, TokenUsage, ToolCall, ToolResult,
};
pub use executor::{
    AgentExecutor, AgentExecutorBuilder, ApprovalRequest, ChildTaskRequest, ExecutorError,
    ProviderRouter, TaskStore, ToolInvoker,
};
pub use grants::{AccessLevel, GrantDecision, ToolGrant};
pub use inbox::{
    AgentInbox, AgentMessage, InboxError, InboxStore, MemoryInboxStore, MessageKind, MessageTarget,
    RoleResolver, StaticRoleResolver, DEFAULT_INBOX_CAP,
};
pub use library::{validate_agent_library, LibraryIssue, LibraryIssueKind};
pub use registry::AgentRegistry;
pub use spec::{AgentRole, AgentSpec, Capability, Runtime};
pub use task::{AgentTask, TaskStatus};
pub use tool_registry::{ToolDescriptor, ToolRegistry};

// automation module re-exports (E-P6-07)
pub use automation::{
    Automation, AutomationError, AutomationOutcome, AutomationRunner, AutomationRunnerBuilder,
    AutomationTarget, AutomationTrigger, DiskSink, DraftArtifact, DraftKind, NoopSink,
    OutboundSink,
};

// chain module re-exports
pub use chain::{
    builtin_research_summarize_draft, builtin_triage_branch_respond, ChainContext, ChainError,
    ChainExecutor, ChainExecutorBuilder, ChainFlow, ChainResult, ChainStep, ChainStepTrace,
    ChainValidationError, CHAIN_DEFAULT_MAX_DEPTH, CHAIN_DEFAULT_MAX_STEPS,
};
