//! `cascade backup` subcommand — manage backup snapshots.
//!
//! Purpose: List, verify, and restore backup snapshots created by the daemon's
//! BackupSync task.
//!
//! Subcommands:
//!   - `cascade backup list` — list snapshots for a given tier
//!   - (future) `cascade backup restore` — restore from a snapshot (P3)
//!   - (future) `cascade backup verify` — check integrity (P3)
//!
//! SPORT: .claude/docs/MASTER-CLI.md — backup command entry (T-P2-E02-21)

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};
use std::path::PathBuf;

use super::Command;

/// Backup arguments for `cascade backup`.
#[derive(Debug, Args)]
pub struct BackupArgs {
    /// Backup subcommand variant.
    #[command(subcommand)]
    pub cmd: BackupSubcommand,
}

/// Backup subcommand variants.
#[derive(Debug, Subcommand)]
pub enum BackupSubcommand {
    /// List snapshots for a given tier.
    List {
        /// Tier name (e.g., "GCI", "PPI").
        #[arg(value_name = "TIER")]
        tier: String,

        /// Backup root directory (default: ~/.cascade/backups).
        #[arg(long, value_name = "PATH")]
        backup_root: Option<PathBuf>,
    },
}

#[async_trait]
impl Command for BackupArgs {
    async fn run(&self) -> Result<()> {
        match &self.cmd {
            BackupSubcommand::List { tier, backup_root } => {
                list_snapshots(tier, backup_root.clone()).await
            }
        }
    }
}

/// List snapshots for a given tier.
///
/// Reads all `snapshot-*` directories under `{backup_root}/{tier}/`,
/// sorts lexicographically (oldest first), and displays count and names.
async fn list_snapshots(tier: &str, backup_root: Option<PathBuf>) -> Result<()> {
    let backup_root = backup_root
        .or_else(|| {
            std::env::var("HOME")
                .ok()
                .map(|h| PathBuf::from(h).join(".cascade").join("backups"))
        })
        .ok_or_else(|| {
            CascadeError::Other("cannot determine backup root: provide --backup-root".into())
        })?;

    let tier_root = backup_root.join(tier);

    if !tier_root.exists() {
        println!("no backups found for tier '{}'", tier);
        return Ok(());
    }

    // Collect and sort snapshot directories.
    let mut snapshots: Vec<PathBuf> = std::fs::read_dir(&tier_root)
        .map_err(|e| CascadeError::Other(e.to_string()))?
        .filter_map(|entry| {
            let entry = entry.ok()?;
            let path = entry.path();
            let name = entry.file_name();
            if path.is_dir() && name.to_string_lossy().starts_with("snapshot-") {
                Some(path)
            } else {
                None
            }
        })
        .collect();

    snapshots.sort();

    if snapshots.is_empty() {
        println!("no snapshots found for tier '{}'", tier);
    } else {
        println!(
            "Found {} snapshot{} for tier '{}' (newest last):",
            snapshots.len(),
            if snapshots.len() == 1 { "" } else { "s" },
            tier
        );
        for snapshot in snapshots {
            if let Some(name) = snapshot.file_name() {
                println!("  {}", name.to_string_lossy());
            }
        }
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backup_list_command_parses() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: BackupSubcommand,
        }

        let args = vec!["backup", "list", "GCI"];
        let cli = Cli::try_parse_from(args).unwrap();
        match cli.cmd {
            BackupSubcommand::List { tier, .. } => {
                assert_eq!(tier, "GCI");
            }
        }
    }
}
