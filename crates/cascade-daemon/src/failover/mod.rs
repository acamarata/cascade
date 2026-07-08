//! Failover — Phase C (session-failover) subsystem primitives.
//!
//! Purpose: houses the pure, always-compiled filesystem primitive
//! ([`session_copy`], ticket C2) shared by the continuity-driven
//! interactive-session handoff (`crate::continuity::handoff`, ticket C3).
//!
//! The request-level proxy (ticket C4, `AnthropicFailoverServer` on
//! `:3763`) is a separate, feature-gated, currently-DEFERRED skeleton at
//! `crate::proxy::anthropic_failover` — see that module's doc comment for
//! why it is not implemented in this pass.
//!
//! Design spec: `.claude/planning/10-session-failover-spec.md`.
//! Master plan: `.claude/planning/11-vNEXT-master-plan.md` Phase C.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — failover module (Phase C)

pub mod session_copy;

#[allow(unused_imports)] // public API — consumed when session-failover is fully wired
pub use session_copy::{copy_session, CopyOutcome};
