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

// Used by set_schedule to parse/update config.toml.
// toml_edit is already a dependency of cascade-cli (Cargo.toml).

/// Backup arguments for `cascade backup`.
#[derive(Debug, Args)]
pub struct BackupArgs {
    /// Backup subcommand variant.
    #[command(subcommand)]
    pub cmd: BackupSubcommand,
}

/// Supported schedule intervals for `cascade backup schedule`.
#[derive(Debug, Clone, clap::ValueEnum)]
pub enum ScheduleInterval {
    /// Once per day.
    Daily,
    /// Once per week.
    Weekly,
    /// Once per hour.
    Hourly,
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

    /// Configure automatic backup schedule (writes to ~/.cascade/config.toml).
    ///
    /// Sets `backup.schedule` so the cascade daemon triggers periodic exports.
    /// TODO: wire daemon scheduled-task trigger once that feature lands.
    Schedule {
        /// How often to run an automatic backup.
        #[arg(value_enum, default_value = "daily")]
        interval: ScheduleInterval,
    },
}

#[async_trait]
impl Command for BackupArgs {
    async fn run(&self) -> Result<()> {
        match &self.cmd {
            BackupSubcommand::List { tier, backup_root } => {
                list_snapshots(tier, backup_root.clone()).await
            }
            BackupSubcommand::Schedule { interval } => set_schedule(interval).await,
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

/// Write the backup schedule to `~/.cascade/config.toml`.
///
/// Sets `backup.schedule = "daily"|"weekly"|"hourly"`. The cascade daemon
/// reads this value and triggers timed exports accordingly.
///
/// TODO: once the daemon scheduled-task subsystem lands, wire the actual
/// trigger here (e.g., create a `.cascade/schedule/backup.toml` job file).
async fn set_schedule(interval: &ScheduleInterval) -> Result<()> {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| CascadeError::Other("cannot determine home directory".into()))?;

    let config_path = home.join(".cascade").join("config.toml");
    let interval_str = match interval {
        ScheduleInterval::Daily => "daily",
        ScheduleInterval::Weekly => "weekly",
        ScheduleInterval::Hourly => "hourly",
    };

    // Read existing config or start fresh.
    let raw = if config_path.exists() {
        std::fs::read_to_string(&config_path).map_err(|e| CascadeError::Io {
            path: config_path.clone(),
            operation: "read config.toml",
            source: e,
        })?
    } else {
        String::new()
    };

    // Parse as TOML and set backup.schedule.
    let mut doc: toml_edit::DocumentMut = raw
        .parse()
        .map_err(|e| CascadeError::Other(format!("parse config.toml: {e}")))?;

    doc["backup"]["schedule"] = toml_edit::value(interval_str);

    // Ensure parent dir exists.
    if let Some(parent) = config_path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create .cascade dir",
            source: e,
        })?;
    }

    std::fs::write(&config_path, doc.to_string()).map_err(|e| CascadeError::Io {
        path: config_path.clone(),
        operation: "write config.toml",
        source: e,
    })?;

    println!(
        "Backup schedule set to '{}' in {}",
        interval_str,
        config_path.display()
    );
    println!("Note: daemon scheduling wiring is pending (TODO).");

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
            _ => panic!("expected List variant"),
        }
    }

    #[test]
    fn backup_schedule_command_parses() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: BackupSubcommand,
        }

        let args = vec!["backup", "schedule", "daily"];
        let cli = Cli::try_parse_from(args).unwrap();
        match cli.cmd {
            BackupSubcommand::Schedule { interval } => {
                assert!(matches!(interval, ScheduleInterval::Daily));
            }
            _ => panic!("expected Schedule variant"),
        }
    }
}
