//! Per-harness instruction-file generation from a unified merged cascade.
//!
//! ## Purpose
//!
//! Every harness (Claude Code, OpenCode, Codex, Cursor, Aider) consumes
//! identical merged instruction text, but expects it in a different file and
//! format.  This module writes the harness-native file for each and optionally
//! writes a single merged file via `--output-single-file`.
//!
//! ## Supported harnesses
//!
//! | Harness | Output file | Format |
//! |---------|-------------|--------|
//! | `claude-code` | `CLAUDE.md` | Markdown |
//! | `opencode` | `AGENTS.md` | YAML-frontmatter Markdown |
//! | `codex` | `AGENTS.md` | same as opencode |
//! | `cursor` | `.cursor/rules/{slug}.mdc` | MDC (Cursor rules) |
//! | `aider` | `CONVENTIONS.md` | plain Markdown |
//!
//! All output files embed the same `merged_instructions` body so content is
//! byte-for-byte identical across harnesses (only the envelope differs).
//!
//! ## Single-file output (`--output-single-file`)
//!
//! Writes one file containing the merged body prefixed with a harness-index
//! header so a user can manually distribute the content to their harness.
//!
//! ## `init-from-installed` (autodetect + scaffold)
//!
//! `init_from_installed` detects which harnesses are installed on the machine
//! and scaffolds the correct output file for each.  It is idempotent (skips
//! when the file already contains the cascade marker) and supports dry-run.
//!
//! ## Idempotency marker
//!
//! The string `<!-- cascade:unified-harness sha256=<hash> -->` is written near
//! the top of every generated file.  The hash covers the body that follows so
//! hand-edits are detectable.  Subsequent runs detect the base marker and skip
//! the file (idempotent).
//!
//! ## Snapshot-before-regenerate
//!
//! Before any files are written, all target paths that currently exist are
//! captured into `<workspace>/.cascade/snapshots/<timestamp>/`.  This allows
//! recovery via `cascade snapshot restore` without any git dependency.
//!
//! ## SPORT
//!
//! MASTER-HARNESSES.md: all-harness generator (E-P7-03)

mod api;
mod constants;
mod detect;
mod kind;
mod render;
mod tests;

// ── Public re-exports ─────────────────────────────────────────────────────────

pub use api::{
    generate_for_harnesses, init_from_installed, inject_active_work_section, write_single_file,
};
pub use constants::{
    ACTIVE_WORK_BEGIN, ACTIVE_WORK_END, UNIFIED_HARNESS_MARKER, UNIFIED_HARNESS_MARKER_BASE,
};
pub use kind::HarnessKind;
