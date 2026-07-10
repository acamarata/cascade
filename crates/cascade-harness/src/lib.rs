//! # cascade-harness
//!
//! AI coding harness detection, configuration scaffolding, instruction-file
//! generation, MCP stdio bridge, and session monitoring for Cascade.
//!
//! ## Modules
//!
//! | Module | Purpose |
//! |--------|---------|
//! | `codex::detect`   | Detect the Codex CLI binary and version (T-P4-E06-01) |
//! | `codex::scaffold` | Write/merge `codex/config.yaml` with Cascade MCP entry (T-P4-E06-02) |
//! | `codex::monitor`  | Watch running Codex processes + quota-store integration (T-P4-E06-05) |
//! | `generate`        | Generate `AGENTS.md` and `.cursorrules` from resolved cascade (T-P4-E06-03) |
//! | `settings`        | Idempotent harness settings writer (`cascade configure`, P13) |
//!
//! ## SPORT
//! MASTER-CRATES.md: cascade-harness (T-P4-E06-01)

pub mod agents;
pub mod codex;
pub mod generate;
pub mod policy;
pub mod settings;
pub mod skills;

#[cfg(test)]
pub(crate) mod test_support;
