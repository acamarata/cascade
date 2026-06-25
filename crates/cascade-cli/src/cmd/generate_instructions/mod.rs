//! `cascade generate-instructions` — harness-native instruction file generator.
//!
//! Consumes a [`ResolvedCascade`] (from T-P4-E02-26) and writes harness-native
//! instruction files so that Claude Code (CC) and OpenCode (OC) automatically
//! use Cascade MCP + RAG on next startup.
//!
//! ## CC output (per found tier)
//!
//! - `{tier_path}/.claude/CLAUDE.md` — header block + tier instruction text.
//!   Idempotent: skips if the cascade header is already present.
//! - `{tier_path}/.claude/AGENTS.md` — symlink to CLAUDE.md (OC/CC compat).
//! - `{tier_path}/.claude/settings.json` — MCP server entry added (additive).
//!
//! ## OC output (per found tier)
//!
//! - `~/.config/opencode/opencode.json` — MCP server entry added (additive).
//! - `{tier_path}/.cascade/opencode-instructions.md` — OC-specific preamble.
//!
//! ## Flags
//!
//! - `--harness [cc|oc|both]` — default: both.
//! - `--dry-run` — print unified diff without writing.
//! - `--tier [gci|pci|apc|ppc|prc|pac|all]` — default: all found tiers.
//!
//! ## Hard rule compliance
//!
//! Generated CLAUDE.md content follows GCI writing rules (no AI attribution,
//! no AI tells). The cascade header marker ensures idempotency.
//!
//! ## SPORT
//!
//! MASTER-CLI.md — cascade generate-instructions (T-P4-E02-27)

mod cc;
mod oc;
mod utils;

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_core::cascade_resolution::resolve_cascade_full;
use cascade_types::cascade_tier::CascadeTier;
use cascade_types::error::{CascadeError, Result};
use clap::Args;

use super::Command;
use cc::generate_cc;
use oc::generate_oc;

// ── Args ──────────────────────────────────────────────────────────────────────

/// Which harness to generate files for.
#[derive(Debug, Clone, Copy, PartialEq, Eq, clap::ValueEnum)]
pub enum Harness {
    /// Claude Code (CLAUDE.md + settings.json).
    Cc,
    /// OpenCode (opencode.json + opencode-instructions.md).
    Oc,
    /// Both CC and OC.
    Both,
}

/// Arguments for `cascade generate-instructions`.
#[derive(Debug, Args)]
pub struct GenerateInstructionsArgs {
    /// Harness to generate files for.
    #[arg(long, value_enum, default_value = "both")]
    pub harness: Harness,

    /// Only generate for this tier. Defaults to all found tiers.
    #[arg(long)]
    pub tier: Option<String>,

    /// Project path. Defaults to the current working directory.
    #[arg(long)]
    pub project: Option<PathBuf>,

    /// Print a diff of what would be written without modifying any files.
    #[arg(long)]
    pub dry_run: bool,
}

#[async_trait]
impl Command for GenerateInstructionsArgs {
    async fn run(&self) -> Result<()> {
        let cwd = self
            .project
            .clone()
            .or_else(|| std::env::current_dir().ok())
            .unwrap_or_else(|| PathBuf::from("."));

        let resolved = resolve_cascade_full(&cwd).map_err(|e| CascadeError::Io {
            path: cwd.clone(),
            operation: "resolve cascade for generate-instructions",
            source: std::io::Error::other(e.to_string()),
        })?;

        // Filter tiers by --tier flag
        let tiers_to_process = if let Some(tier_str) = &self.tier {
            if tier_str == "all" {
                resolved.tiers_found.iter().filter(|t| t.found).collect::<Vec<_>>()
            } else {
                let target: CascadeTier = tier_str.parse().map_err(|_| CascadeError::Io {
                    path: cwd.clone(),
                    operation: "parse --tier argument",
                    source: std::io::Error::new(
                        std::io::ErrorKind::InvalidInput,
                        format!("unknown tier: {tier_str}"),
                    ),
                })?;
                resolved
                    .tiers_found
                    .iter()
                    .filter(|t| t.found && t.tier == target)
                    .collect::<Vec<_>>()
            }
        } else {
            resolved.tiers_found.iter().filter(|t| t.found).collect::<Vec<_>>()
        };

        if tiers_to_process.is_empty() {
            eprintln!(
                "No cascade tiers found. Run `cascade init` to scaffold a .cascade/ directory."
            );
            return Ok(());
        }

        let mut any_written = false;

        for tier_result in &tiers_to_process {
            // Compute the tier root: parent of the path_searched (.cascade/ dir)
            let cascade_dir = &tier_result.path_searched;
            let tier_root = if cascade_dir.ends_with(".cascade") {
                cascade_dir.parent().map(|p| p.to_path_buf())
            } else if cascade_dir.is_file() {
                // path is a file (CASCADE.md); go up to .cascade parent then tier root
                cascade_dir
                    .parent()
                    .and_then(|p| p.parent())
                    .map(|p| p.to_path_buf())
            } else {
                cascade_dir.parent().map(|p| p.to_path_buf())
            };

            let Some(tier_root) = tier_root else {
                eprintln!(
                    "warning: cannot determine tier root for {:?}, skipping",
                    cascade_dir
                );
                continue;
            };

            match self.harness {
                Harness::Cc => {
                    any_written |= generate_cc(
                        tier_result,
                        &tier_root,
                        &resolved.mcp_server_url,
                        self.dry_run,
                    )?;
                }
                Harness::Oc => {
                    any_written |= generate_oc(tier_result, &tier_root, self.dry_run)?;
                }
                Harness::Both => {
                    any_written |= generate_cc(
                        tier_result,
                        &tier_root,
                        &resolved.mcp_server_url,
                        self.dry_run,
                    )?;
                    any_written |= generate_oc(tier_result, &tier_root, self.dry_run)?;
                }
            }
        }

        if !any_written && !self.dry_run {
            println!("All targets already up to date.");
        }

        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests;
