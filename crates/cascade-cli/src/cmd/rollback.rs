//! `cascade rollback list | apply <id>` — snapshot rollback commands.
//!
//! Purpose: Display available pre-update snapshots and restore a chosen snapshot
//! by sending an IPC `rollback_apply` request to the daemon.
//!
//! Subcommands:
//!   `cascade rollback list`         — print table of all snapshots
//!   `cascade rollback apply <id>`   — restore a snapshot; asks for confirmation
//!
//! Exit code `0` on success; `1` on daemon error or missing snapshot.
//!
//! SPORT: MASTER-CLI.md — cascade rollback list/apply (T-P4-E04-15)

use async_trait::async_trait;
use cascade_types::error::Result;
use cascade_types::ipc::{
    RollbackApplyParams, RollbackApplyResult, RollbackListParams, RollbackListResult,
};
use clap::{Args, Subcommand};

use super::Command;
use crate::ipc_client::IpcClient;

// ── Arg types ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade rollback`.
#[derive(Debug, Args)]
pub struct RollbackArgs {
    #[command(subcommand)]
    pub subcommand: Option<RollbackSubcommand>,
}

/// Subcommands under `cascade rollback`.
#[derive(Debug, Subcommand)]
pub enum RollbackSubcommand {
    /// List all available snapshots.
    List,
    /// Restore a snapshot by its id.
    Apply {
        /// Snapshot id to restore (e.g., `snapshot-1717200000`).
        snapshot_id: String,
        /// Skip the confirmation prompt.
        #[arg(long, short = 'y')]
        yes: bool,
    },
}

#[async_trait]
impl Command for RollbackArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            None | Some(RollbackSubcommand::List) => run_list().await,
            Some(RollbackSubcommand::Apply { snapshot_id, yes }) => {
                run_apply(snapshot_id, *yes).await
            }
        }
    }
}

// ── list ──────────────────────────────────────────────────────────────────────

async fn run_list() -> Result<()> {
    let client =
        IpcClient::new().map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;

    let result = client
        .send::<RollbackListParams, RollbackListResult>("rollback_list", RollbackListParams {})
        .await;

    match result {
        Ok(res) => {
            if res.snapshots.is_empty() {
                println!("No snapshots found.");
                return Ok(());
            }
            // Print table header.
            println!(
                "{:<28}  {:<20}  {:<16}  {:>6}  {:>8}",
                "id", "created", "cascade_version", "files", "size"
            );
            println!("{}", "-".repeat(86));
            for snap in &res.snapshots {
                let created = snap.created.get(..16).unwrap_or(&snap.created);
                let size_mb = snap.total_bytes as f64 / 1_048_576.0;
                println!(
                    "{:<28}  {:<20}  {:<16}  {:>6}  {:>7.1} MB",
                    snap.id, created, snap.cascade_version, snap.file_count, size_mb
                );
            }
            Ok(())
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => {
            eprintln!("cascade daemon is not running. Start it with: cascade daemon start");
            std::process::exit(1);
        }
        Err(e) => {
            eprintln!("Error: {e}");
            std::process::exit(1);
        }
    }
}

// ── apply ─────────────────────────────────────────────────────────────────────

async fn run_apply(snapshot_id: &str, yes: bool) -> Result<()> {
    if !yes {
        eprint!(
            "Restore snapshot '{snapshot_id}'? This will overwrite current protocol files. [y/N] "
        );
        let mut input = String::new();
        std::io::stdin()
            .read_line(&mut input)
            .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;
        if !input.trim().eq_ignore_ascii_case("y") {
            println!("Aborted.");
            return Ok(());
        }
    }

    let client =
        IpcClient::new().map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;

    let params = RollbackApplyParams {
        snapshot_id: snapshot_id.to_string(),
    };

    let result = client
        .send::<RollbackApplyParams, RollbackApplyResult>("rollback_apply", params)
        .await;

    match result {
        Ok(res) if res.ok => {
            let version = res.restored_version.as_deref().unwrap_or("unknown");
            println!("Rolled back to {snapshot_id} (cascade {version}). Daemon reloading.");
            Ok(())
        }
        Ok(res) => {
            let err = res.error.as_deref().unwrap_or("unknown error");
            eprintln!("Rollback failed: {err}");
            std::process::exit(1);
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => {
            eprintln!("cascade daemon is not running. Start it with: cascade daemon start");
            std::process::exit(1);
        }
        Err(e) => {
            eprintln!("Error: {e}");
            std::process::exit(1);
        }
    }
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use clap::Parser;

    #[derive(Parser)]
    struct Cli {
        #[command(subcommand)]
        cmd: crate::cmd::Commands,
    }

    #[test]
    fn rollback_list_parses() {
        let cli = Cli::try_parse_from(["cascade", "rollback", "list"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Rollback(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::RollbackSubcommand::List)
                ));
            }
            _ => panic!("expected Rollback"),
        }
    }

    #[test]
    fn rollback_apply_parses() {
        let cli =
            Cli::try_parse_from(["cascade", "rollback", "apply", "snapshot-1717200000"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Rollback(args) => match args.subcommand {
                Some(super::RollbackSubcommand::Apply { snapshot_id, yes }) => {
                    assert_eq!(snapshot_id, "snapshot-1717200000");
                    assert!(!yes);
                }
                _ => panic!("expected Apply"),
            },
            _ => panic!("expected Rollback"),
        }
    }

    #[test]
    fn rollback_apply_yes_flag_parses() {
        let cli = Cli::try_parse_from([
            "cascade",
            "rollback",
            "apply",
            "snapshot-1717200000",
            "--yes",
        ])
        .unwrap();
        match cli.cmd {
            crate::cmd::Commands::Rollback(args) => match args.subcommand {
                Some(super::RollbackSubcommand::Apply { yes, .. }) => {
                    assert!(yes);
                }
                _ => panic!("expected Apply"),
            },
            _ => panic!("expected Rollback"),
        }
    }

    #[test]
    fn rollback_no_subcommand_defaults_to_list_behavior() {
        // `cascade rollback` with no subcommand is valid (prints help or list).
        let cli = Cli::try_parse_from(["cascade", "rollback"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Rollback(args) => {
                assert!(args.subcommand.is_none());
            }
            _ => panic!("expected Rollback"),
        }
    }
}
