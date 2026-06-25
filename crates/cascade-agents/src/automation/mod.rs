//! Non-coding automation — OpenClaw/Hermes parity pillar.
//!
//! Purpose: define the types and runner for prompt-driven, non-coding workflows
//!   (email drafting, ticket triage, scheduled summaries). Every outbound action
//!   (send email, file ticket, publish) produces a DRAFT artifact + an
//!   `ApprovalRequest` and NEVER auto-executes. On approval the draft is forwarded
//!   to an `OutboundSink` impl; on denial it is discarded.
//!
//! Inputs:
//!   - `Automation` — loaded from `<ai-folder>/library/automations/*.yaml`
//!   - `AutomationTrigger` — how the workflow starts (manual, hook, scheduled, inbox)
//!   - `OutboundSink` — injectable for email / ticket delivery (Noop + Disk impls)
//!
//! Outputs:
//!   - `DraftArtifact` — the proposed content, saved to
//!     `<ai-folder>/agents/drafts/<id>.json`
//!   - `ApprovalRequest` from the executor (surfaces in the CEO channel)
//!   - `AutomationOutcome` per run
//!
//! Constraints:
//!   - SAFE DEFAULT: outbound steps always produce a draft + `ApprovalRequest`.
//!     The `OutboundSink::send` method is ONLY called after explicit approval.
//!   - `OutboundSink` is injectable; only Noop and Disk impls ship here.
//!     Real Gmail / issue-tracker integrations are provider-plugin work.
//!   - `AutomationRunner::run` is the single entry point; it delegates execution
//!     to either `ChainExecutor` (if `chain_ref` is set) or `AgentExecutor`
//!     (if `agent_role` + `goal` are set).
//!   - YAML files under `<ai-folder>/library/automations/*.yaml` are loadable via
//!     `Automation::load_library`.
//!   - serde: `camelCase` field names; struct enum variants only (repo rule).
//!
//! SPORT: cascade-agents / automation — E-P6-07

pub mod runner;
pub mod sink;
pub mod types;

mod tests;

// Re-export all public surface at the automation module level so existing
// callers using `crate::automation::Foo` continue to work unchanged.
pub use runner::{AutomationRunner, AutomationRunnerBuilder};
pub use sink::{DiskSink, NoopSink, OutboundSink};
pub use types::{
    Automation, AutomationError, AutomationOutcome, AutomationTarget, AutomationTrigger,
    DraftArtifact, DraftKind,
};
