//! `cascade check` — local content checks.
//!
//! Currently exposes `cascade check injection`, invoked by the injection-guard
//! hook (`scripts/hooks/injection-guard.sh`). It reads a prompt (from `--text`
//! or stdin), scans it for prompt-injection / jailbreak patterns, prints a
//! concise risk summary (never echoing the prompt), and exits:
//! - `0` — clean (risk None/Low)
//! - `1` — warn (risk Medium/High)
//! - `2` — halt (risk Critical)
//!
//! so the hook can gate tool dispatch on the exit code.

use std::io::Read;

use async_trait::async_trait;
use cascade_core::injection_scan::{scan_for_injection, Risk, Sensitivity};
use cascade_types::error::Result;
use clap::{Args, Subcommand, ValueEnum};

use super::Command;

/// `cascade check` argument root.
#[derive(Debug, Args)]
pub struct CheckArgs {
    #[command(subcommand)]
    command: CheckCommand,
}

#[derive(Debug, Subcommand)]
enum CheckCommand {
    /// Scan a prompt for injection / jailbreak patterns (used by the injection-guard hook).
    Injection(InjectionArgs),
}

/// `cascade check injection` arguments.
#[derive(Debug, Args)]
pub struct InjectionArgs {
    /// Text to scan. If omitted, the prompt is read from stdin.
    #[arg(long)]
    text: Option<String>,

    /// Detection sensitivity.
    #[arg(long, value_enum, default_value_t = SensitivityArg::Moderate)]
    sensitivity: SensitivityArg,
}

/// CLI mirror of [`Sensitivity`] so clap can parse it.
#[derive(Debug, Clone, ValueEnum)]
enum SensitivityArg {
    Strict,
    Moderate,
    LogOnly,
}

impl From<SensitivityArg> for Sensitivity {
    fn from(value: SensitivityArg) -> Self {
        match value {
            SensitivityArg::Strict => Sensitivity::Strict,
            SensitivityArg::Moderate => Sensitivity::Moderate,
            SensitivityArg::LogOnly => Sensitivity::LogOnly,
        }
    }
}

#[async_trait]
impl Command for CheckArgs {
    async fn run(&self) -> Result<()> {
        match &self.command {
            CheckCommand::Injection(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for InjectionArgs {
    async fn run(&self) -> Result<()> {
        let text = match &self.text {
            Some(t) => t.clone(),
            None => {
                let mut buf = String::new();
                // Best-effort stdin read; empty input scans as clean.
                let _ = std::io::stdin().read_to_string(&mut buf);
                buf
            }
        };

        let report = scan_for_injection(&text, self.sensitivity.clone().into());
        // Summary only — never echo the scanned prompt or matched spans.
        println!(
            "injection risk: {:?} ({} pattern match(es))",
            report.risk,
            report.matches.len()
        );

        let code = match report.risk {
            Risk::None | Risk::Low => 0,
            Risk::Medium | Risk::High => 1,
            Risk::Critical => 2,
        };
        std::process::exit(code);
    }
}
