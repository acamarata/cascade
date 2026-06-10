//! Codex CLI harness integration.
//!
//! Sub-modules:
//! - [`detect`]   — binary detection and `CodexInfo` population (T-P4-E06-01)
//! - [`scaffold`] — `codex/config.yaml` non-destructive merge (T-P4-E06-02)
//! - [`monitor`]  — process watch + quota-store integration (T-P4-E06-05)

pub mod detect;
pub mod monitor;
pub mod scaffold;
