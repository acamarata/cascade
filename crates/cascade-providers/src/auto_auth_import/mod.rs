//! # auto_auth_import
//!
//! Detect and import AI harness accounts from well-known disk locations.
//!
//! ## Purpose
//! Scans installed AI coding harnesses (Claude Code, OpenCode, Codex, Cursor)
//! and env vars for authentication state, producing a list of `DiscoveredAccount`
//! entries that the onboarding wizard can present for import.
//!
//! ## Design invariants
//! - **Read-only.** No harness config file is ever written or modified.
//! - **No secret logging.** API key and token values are never written to trace spans.
//! - **AI-optional.** This step runs entirely before any AI provider is available.
//! - macOS Keychain reads use `security find-generic-password` subprocess.
//! - JWT decode extracts the `sub`/`email` field from the payload (no signature verification needed).
//!
//! ## Inputs
//! None — reads from the filesystem and environment.
//!
//! ## Outputs
//! `Vec<DiscoveredAccount>` — one entry per detected harness account or env API key.
//!
//! ## Constraints
//! - `HOME`-confined reads — never reads outside `~` (home directory).
//! - `permission-denied` on any file → silently skip (not an error).
//! - JWT payload decode is best-effort; malformed tokens are skipped.

pub(crate) mod helpers;
pub(crate) mod import;
pub(crate) mod scanners;

#[cfg(test)]
mod tests;

// Re-export public API at the same path as before
pub use import::import_accounts;
pub use scanners::{
    scan_all, scan_antigravity, scan_claude_code, scan_codex, scan_cursor, scan_env_vars,
    scan_opencode,
};
