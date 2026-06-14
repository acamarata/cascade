//! `cascade health [path] [--json] [--max-file-lines N]`
//!
//! # Purpose
//! Runs the EIE (engineering-excellence) health check against a user project
//! and reports file-size and duplication issues with an overall score.
//!
//! # Inputs
//! - `path` (optional): project root to scan. Defaults to the current directory.
//! - `--json`: emit machine-readable JSON instead of the human table.
//! - `--max-file-lines N`: override the line cap (default 300).
//!
//! # Outputs
//! Human table (default) or JSON matching [`cascade_core::eie::HealthReport`].
//! Exits non-zero when the score is below 70.
//!
//! # Constraints
//! Analysis logic lives in `cascade_core::eie` so it can be reused by the
//! MCP server and other callers without depending on the CLI layer.

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_core::eie::{run_health_check, HealthConfig};
use cascade_types::error::Result;
use clap::Args;

use super::Command;

/// Arguments for `cascade health`.
#[derive(Debug, Args)]
pub struct HealthArgs {
    /// Project root to analyse. Defaults to the current working directory.
    #[arg(value_name = "PATH")]
    pub path: Option<PathBuf>,

    /// Emit machine-readable JSON instead of the human table.
    #[arg(long)]
    pub json: bool,

    /// Maximum source-file line count before flagging as oversized (default 300).
    #[arg(long, value_name = "N")]
    pub max_file_lines: Option<usize>,
}

#[async_trait]
impl Command for HealthArgs {
    async fn run(&self) -> Result<()> {
        let root = self
            .path
            .clone()
            .unwrap_or_else(|| std::env::current_dir().unwrap_or_else(|_| PathBuf::from(".")));

        let mut config = HealthConfig::default();
        if let Some(cap) = self.max_file_lines {
            config.max_file_lines = cap;
        }

        let report = run_health_check(&root, &config);

        if self.json {
            println!(
                "{}",
                serde_json::to_string_pretty(&report).unwrap_or_default()
            );
        } else {
            print_human(&report);
        }

        // Exit non-zero when score is below 70.
        if report.score < 70 {
            std::process::exit(1);
        }

        Ok(())
    }
}

// ── human output ─────────────────────────────────────────────────────────────

fn print_human(report: &cascade_core::eie::HealthReport) {
    println!();
    println!("  EIE Health Check");
    println!("  ─────────────────────────────────────────────────────");
    println!("  Score: {}/100", report.score);
    println!("  {}", report.summary);
    println!();

    if !report.file_size_issues.is_empty() {
        println!(
            "  Oversized Files ({} flagged):",
            report.file_size_issues.len()
        );
        println!("  {:<50} {:>8}  {:>8}", "FILE", "LINES", "CAP");
        println!("  {}", "─".repeat(70));
        for issue in &report.file_size_issues {
            println!(
                "  {:<50} {:>8}  {:>8}",
                truncate(&issue.path, 50),
                issue.lines,
                issue.cap
            );
        }
        println!();
    }

    if !report.duplication_issues.is_empty() {
        println!(
            "  Near-Duplicate Pairs ({} flagged):",
            report.duplication_issues.len()
        );
        println!("  {:<35}  {:<35}  {:>7}", "FILE A", "FILE B", "SHARED");
        println!("  {}", "─".repeat(80));
        for issue in &report.duplication_issues {
            println!(
                "  {:<35}  {:<35}  {:>7}",
                truncate(&issue.file_a, 35),
                truncate(&issue.file_b, 35),
                issue.shared_windows
            );
        }
        println!();
    }

    if report.file_size_issues.is_empty() && report.duplication_issues.is_empty() {
        println!("  No issues found.");
        println!();
    }
}

fn truncate(s: &str, max: usize) -> String {
    if s.len() <= max {
        s.to_string()
    } else {
        format!("...{}", &s[s.len().saturating_sub(max - 3)..])
    }
}

// ── tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn truncate_short_string_unchanged() {
        assert_eq!(truncate("hello", 10), "hello");
    }

    #[test]
    fn truncate_long_string_prefixed_with_ellipsis() {
        let s = "a".repeat(60);
        let t = truncate(&s, 10);
        assert!(t.starts_with("..."));
        assert!(t.len() <= 10);
    }
}
