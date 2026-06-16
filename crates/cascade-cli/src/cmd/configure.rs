//! `cascade configure` — write Cascade-managed values into a harness runtime
//! config file, idempotently and non-destructively.
//!
//! ## Usage
//!
//! ```text
//! cascade configure --harness claude-code [--apply] [--path <override>]
//! ```
//!
//! Default: **dry-run**.  Pass `--apply` to write the file.
//!
//! ## What it writes
//!
//! All managed content lives under `"_cascade_managed"` in the target JSON
//! file.  The writer owns that key entirely; all other top-level keys are
//! preserved verbatim.  Re-running is idempotent: the managed block is
//! replaced in-place, never duplicated.
//!
//! ## Harness targets
//!
//! | Target | Config file |
//! |--------|-------------|
//! | `claude-code` | `~/.claude/settings.json` |
//!
//! ## SPORT
//! MASTER-CLI.md: cascade configure (P13)

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_harness::settings::{configure_harness, HarnessTarget};
use cascade_types::error::{CascadeError, Result};
use clap::{Args, ValueEnum};

use super::Command;

// ── Clap value enum ───────────────────────────────────────────────────────────

/// Harness targets supported by `cascade configure`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, ValueEnum)]
pub enum HarnessChoice {
    /// Claude Code (`~/.claude/settings.json`)
    #[value(name = "claude-code")]
    ClaudeCode,
}

impl HarnessChoice {
    fn to_target(self) -> HarnessTarget {
        match self {
            HarnessChoice::ClaudeCode => HarnessTarget::ClaudeCode,
        }
    }
}

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade configure`.
#[derive(Debug, Args)]
pub struct ConfigureArgs {
    /// Which harness to configure.
    #[arg(long, value_enum)]
    pub harness: HarnessChoice,

    /// Write the merged settings file to disk.
    ///
    /// Without this flag the command runs in dry-run mode and only prints the
    /// diff of what would change.
    #[arg(long)]
    pub apply: bool,

    /// Override the default settings file path.
    ///
    /// Useful for testing or when the harness config lives in a non-default
    /// location.
    #[arg(long, value_name = "PATH")]
    pub path: Option<PathBuf>,
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for ConfigureArgs {
    async fn run(&self) -> Result<()> {
        let target = self.harness.to_target();
        let dry_run = !self.apply;

        if dry_run {
            println!("Dry-run mode (pass --apply to write the file)");
        }

        let result = configure_harness(target, self.path.as_deref(), dry_run).map_err(|e| {
            CascadeError::Other(format!(
                "configure {} failed: {e}",
                target.display_name()
            ))
        })?;

        println!("Target:  {}", target.display_name());
        println!("File:    {}", result.path.display());

        if result.wrote {
            println!("Status:  written (atomic)");
        } else if dry_run {
            println!("Status:  dry-run (not written)");
        } else {
            println!("Status:  unchanged (skipped)");
        }

        println!("\nChanges:");
        for line in result.diff.lines() {
            println!("  {line}");
        }

        Ok(())
    }
}
