//! `cascade import` — lossless migration engine for legacy harness setups.
//!
//! ## Purpose
//! Migrate a user's hand-built `~/.claude/` (or `.opencode/`, `.codex/`)
//! instruction setup into Cascade's `.cascade/` tiers with a PROVEN lossless
//! guarantee. Produces a dry-run plan with coverage ledger and round-trip
//! verification before writing anything.
//!
//! ## Usage
//! ```text
//! cascade import --from <path> [--harness claude-code|opencode|codex]
//!                [--dest <path>] [--apply] [--report-json]
//! ```
//!
//! ## Behaviour
//! - Default = DRY RUN: print the ImportPlan + coverage ledger + round-trip verdict.
//! - `--apply` only proceeds if `ledger.is_lossless()` AND round-trip passes.
//!   On failure: refuses with the gap list and exits non-zero.
//! - `--report-json`: emit the full plan (or report) as JSON to stdout.
//!
//! ## Notes
//! This is separate from `cascade migrate` (which does a basic file move).
//! `cascade import` is the lossless, verifiable superset with coverage
//! accounting and deterministic round-trip proofs.

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_core::import_engine::ImportEngine;
use cascade_types::error::{CascadeError, Result};
use clap::Args;

use super::export::{ImportFromExportArgs, run_import_from_export};
use super::Command;

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade import`.
#[derive(Debug, Args)]
pub struct ImportArgs {
    /// Source directory to import from (e.g. `~/.claude`, `~/.opencode`).
    ///
    /// If not specified, the engine auto-detects: checks `~/.claude`, then
    /// `~/.opencode`, then `~/.codex`.
    #[arg(long, short = 'f')]
    pub from: Option<PathBuf>,

    /// Source harness tool.
    ///
    /// Valid values: `claude-code` (default), `opencode`, `codex`.
    #[arg(long, default_value = "claude-code")]
    pub harness: String,

    /// Destination `.cascade/` base directory.
    ///
    /// Defaults to `<cwd>/.cascade/`.
    #[arg(long, short = 'd')]
    pub dest: Option<PathBuf>,

    /// Apply the migration (write files).
    ///
    /// Without this flag, only a dry-run plan is printed.
    /// Apply is refused if the coverage ledger or round-trip gate fails.
    #[arg(long, short = 'a')]
    pub apply: bool,

    /// Emit the full plan (or report) as JSON to stdout.
    ///
    /// Useful for CI pipelines that need structured output.
    #[arg(long)]
    pub report_json: bool,

    /// Restore from a `.cascade-archive.tar.gz` produced by `cascade export` (data-01).
    ///
    /// When set, all other import flags except `--dest` and `--force` are ignored.
    /// The archive is verified by BLAKE3 hash before extraction.
    #[arg(long, value_name = "ARCHIVE", conflicts_with = "from")]
    pub from_export: Option<PathBuf>,

    /// Overwrite existing files when using `--from-export`.
    #[arg(long)]
    pub force: bool,
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for ImportArgs {
    async fn run(&self) -> Result<()> {
        // data-01: if --from-export is set, delegate to the archive restore path.
        if let Some(archive) = &self.from_export {
            let archive_args = ImportFromExportArgs {
                from_export: archive.clone(),
                dest: self.dest.clone(),
                force: self.force,
            };
            return run_import_from_export(&archive_args).await;
        }

        let home = PathBuf::from(std::env::var("HOME").unwrap_or_else(|_| ".".into()));
        let cwd = std::env::current_dir().map_err(|e| CascadeError::Io {
            path: PathBuf::from("."),
            operation: "get current directory",
            source: e,
        })?;

        // Resolve source root.
        let source_root = match &self.from {
            Some(p) => p.clone(),
            None => auto_detect_source(&home, &self.harness),
        };

        if !source_root.exists() {
            eprintln!(
                "error: source directory not found: {}",
                source_root.display()
            );
            std::process::exit(1);
        }

        // Resolve destination.
        let dest_root = match &self.dest {
            Some(d) => d.clone(),
            None => cwd.join(".cascade"),
        };

        // Build the plan.
        let engine = ImportEngine::new();
        let plan = engine.plan(&source_root, &self.harness, &dest_root)?;

        if self.report_json && !self.apply {
            // JSON dry-run output.
            match serde_json::to_string_pretty(&plan) {
                Ok(json) => println!("{}", json),
                Err(e) => eprintln!("warn: JSON serialization failed: {}", e),
            }
            if !plan.is_ready() {
                std::process::exit(2);
            }
            return Ok(());
        }

        // Human-readable dry-run output.
        if !self.apply || !plan.is_ready() {
            print_dry_run_report(&plan);
        }

        if !self.apply {
            // Dry run complete.
            if !plan.is_ready() {
                std::process::exit(2);
            }
            return Ok(());
        }

        // Apply mode: gate must pass.
        if !plan.is_ready() {
            eprintln!("\nerror: import plan failed lossless gate — refusing to apply.");
            eprintln!("       Re-run without --apply to see the full gap list.");
            std::process::exit(2);
        }

        println!("\nApplying import plan…");
        let report = engine.apply(&plan)?;

        if self.report_json {
            match serde_json::to_string_pretty(&report) {
                Ok(json) => println!("{}", json),
                Err(e) => eprintln!("warn: JSON serialization failed: {}", e),
            }
        } else {
            print_apply_report(&report);
        }

        if report.failed > 0 {
            std::process::exit(1);
        }

        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Auto-detect the source directory based on harness name and well-known paths.
fn auto_detect_source(home: &Path, harness: &str) -> PathBuf {
    match harness {
        "opencode" => {
            let p = home.join(".opencode");
            if p.exists() { p } else { home.join(".claude") }
        }
        "codex" => home.join(".codex"),
        _ => home.join(".claude"),
    }
}

/// Print a human-readable dry-run report.
fn print_dry_run_report(plan: &cascade_core::import_engine::ImportPlan) {
    println!("cascade import — DRY RUN\n");
    println!("Source:      {}", plan.source_root.display());
    println!("Destination: {}", plan.dest_root.display());
    println!("Harness:     {}\n", plan.harness);

    // File mapping table.
    println!("{:<55} {:<15} STATUS", "SOURCE", "TIER");
    println!("{}", "-".repeat(85));
    for entry in &plan.entries {
        let rel = entry.source_relative.display().to_string();
        let tier = format!("{:?}", entry.mapping.tier);
        let status = if entry.conflict { "SKIP (conflict)" } else { "COPY" };
        println!("{:<55} {:<15} {}", rel, tier, status);
    }

    // Coverage summary.
    println!("\n── Coverage Ledger ──");
    let cov = &plan.coverage;
    println!(
        "  Total atomics: {} | Covered: {} | Dropped: {} | Unmapped: {}",
        cov.total, cov.covered, cov.dropped, cov.unmapped
    );
    println!(
        "  Coverage: {:.1}% [{}]",
        cov.coverage_pct,
        if cov.is_lossless { "LOSSLESS ✓" } else { "GAPS FOUND ✗" }
    );
    if !cov.gaps.is_empty() {
        println!("\n  Gaps (unmapped atomics):");
        for gap in &cov.gaps {
            println!("    {} :: {}", gap.source_path.display(), gap.atomic_id);
        }
    }

    // Round-trip verdict.
    println!("\n── Round-trip Verification ──");
    println!("  {}", plan.round_trip.verdict);
    if !plan.round_trip.missing.is_empty() {
        println!("  Missing facts:");
        for fact in &plan.round_trip.missing {
            println!("    {}::{}", fact.source_file, fact.id);
        }
    }

    // Isolation report.
    println!("\n── Scope Isolation ──");
    if plan.isolation.clean {
        println!("  CLEAN — no scope violations");
    } else {
        println!("  VIOLATIONS:");
        for v in &plan.isolation.violations {
            println!("    {}", v.reason);
        }
    }

    // Overall verdict.
    println!("\n── Verdict ──");
    if plan.is_ready() {
        println!("  READY — run with --apply to migrate files");
    } else {
        println!("  NOT READY — fix gaps above before applying");
    }
}

/// Print a human-readable apply report.
fn print_apply_report(report: &cascade_core::import_engine::ImportReport) {
    println!("\n── Import Complete ──");
    println!(
        "  Copied: {} | Skipped: {} | Failed: {}",
        report.copied, report.skipped, report.failed
    );

    if report.failed > 0 {
        use cascade_core::import_engine::FileStatus;
        println!("\n  Failures:");
        for outcome in &report.outcomes {
            if let FileStatus::Failed(reason) = &outcome.status {
                println!("    {} → {}", outcome.source.display(), reason);
            }
        }
    }

    println!(
        "\n  Coverage: {:.1}% | Round-trip: {}",
        report.coverage.coverage_pct,
        if report.round_trip.passed { "PASS" } else { "FAIL" }
    );
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use clap::Parser;

    use super::*;
    use crate::cmd::{Cli, Commands};

    #[test]
    fn parse_import_dry_run() {
        let cli = Cli::try_parse_from(["cascade", "import", "--from", "/tmp/test-src"]).unwrap();
        let Commands::Import(args) = cli.command else {
            panic!("expected Import command");
        };
        assert_eq!(args.from, Some(PathBuf::from("/tmp/test-src")));
        assert!(!args.apply, "apply must default to false");
        assert!(!args.report_json);
    }

    #[test]
    fn parse_import_apply() {
        let cli = Cli::try_parse_from([
            "cascade",
            "import",
            "--from",
            "/tmp/src",
            "--harness",
            "opencode",
            "--apply",
        ])
        .unwrap();
        let Commands::Import(args) = cli.command else {
            panic!("expected Import command");
        };
        assert!(args.apply);
        assert_eq!(args.harness, "opencode");
    }

    #[test]
    fn parse_import_report_json() {
        let cli = Cli::try_parse_from([
            "cascade",
            "import",
            "--from",
            "/tmp/src",
            "--report-json",
        ])
        .unwrap();
        let Commands::Import(args) = cli.command else {
            panic!("expected Import command");
        };
        assert!(args.report_json);
    }

    #[test]
    fn parse_import_custom_dest() {
        let cli = Cli::try_parse_from([
            "cascade",
            "import",
            "--from",
            "/tmp/src",
            "--dest",
            "/tmp/dest/.cascade",
        ])
        .unwrap();
        let Commands::Import(args) = cli.command else {
            panic!("expected Import command");
        };
        assert_eq!(args.dest, Some(PathBuf::from("/tmp/dest/.cascade")));
    }
}
