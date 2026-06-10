//! `cascade resolve` — print the merged cascade for the current directory.
//!
//! Walks the filesystem upward from CWD, locates `.cascade/CASCADE.md` at
//! each tier (GCI → PCI → APC → PPC → PRC → PAC), and prints the merged
//! content to stdout.
//!
//! `--json` emits a `ResolvedCascade` JSON object with tier provenance.
//! `--dedup` skips duplicate lines across tiers.

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::Args;

use super::Command;

/// Arguments for `cascade resolve`.
#[derive(Debug, Args)]
pub struct ResolveArgs {
    /// Output as JSON with tier provenance instead of plain merged text.
    #[arg(long)]
    pub json: bool,

    /// Deduplicate identical lines across tiers (opt-in; off by default).
    #[arg(long)]
    pub dedup: bool,

    /// Directory to resolve from. Defaults to the current working directory.
    #[arg(long)]
    pub dir: Option<PathBuf>,
}

#[async_trait]
impl Command for ResolveArgs {
    async fn run(&self) -> Result<()> {
        let cwd = self
            .dir
            .clone()
            .or_else(|| std::env::current_dir().ok())
            .unwrap_or_else(|| PathBuf::from("."));

        let mut resolver = cascade_core::resolution::Resolver::new();
        if self.dedup {
            resolver = resolver.with_dedup();
        }

        let resolved = resolver.resolve(&cwd).await?;

        if resolved.tier_sources.is_empty() {
            eprintln!("warning: no cascade tiers found for {}", cwd.display());
            eprintln!("Run `cascade init` to scaffold a .cascade/ directory.");
            return Ok(());
        }

        if self.json {
            println!("{}", serde_json::to_string_pretty(&resolved).unwrap());
        } else {
            print!("{}", resolved.merged_text);
        }

        Ok(())
    }
}
