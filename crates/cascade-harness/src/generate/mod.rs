//! Harness instruction-file generators.
//!
//! Sub-modules:
//! - [`agents_md`] — generate `AGENTS.md` and `.cursorrules` from the resolved cascade (T-P4-E06-03)
//! - [`harness_files`] — all-harness generator: claude-code, opencode, codex, cursor, aider (E-P7-03)

pub mod agents_md;
pub mod harness_files;
