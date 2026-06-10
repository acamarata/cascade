//! `cascade update check | apply | auto` — delta update commands.
//!
//! Purpose: User-facing interface for the update pipeline (T-P4-E04-14/16).
//!
//! Subcommands:
//!   `cascade update check`              — query GitHub for a newer version; print result
//!   `cascade update apply [--yes]`      — trigger full download+verify+apply via IPC
//!   `cascade update auto [--enable|--disable]` — toggle auto_update in config.toml
//!
//! Exit code `0` on success; `1` on daemon error.
//!
//! SPORT: MASTER-CLI.md — cascade update check/apply/auto (T-P4-E04-16)

use async_trait::async_trait;
use cascade_types::error::Result;
use cascade_types::ipc::{
    UpdateApplyParams, UpdateApplyResult, UpdateAutoParams, UpdateAutoResult, UpdateCheckParams,
    UpdateCheckResult,
};
use clap::{Args, Subcommand};

use super::Command;
use crate::ipc_client::IpcClient;

// ── Arg types ─────────────────────────────────────────────────────────────────

/// Arguments for `cascade update`.
#[derive(Debug, Args)]
pub struct UpdateArgs {
    #[command(subcommand)]
    pub subcommand: Option<UpdateSubcommand>,
}

/// Subcommands under `cascade update`.
#[derive(Debug, Subcommand)]
pub enum UpdateSubcommand {
    /// Check for an available update without installing.
    Check,
    /// Download, verify, and apply the latest update.
    Apply {
        /// Skip the confirmation prompt.
        #[arg(long, short = 'y')]
        yes: bool,
    },
    /// Toggle auto-update in config.toml.
    Auto {
        /// Enable auto-update.
        #[arg(long, conflicts_with = "disable")]
        enable: bool,
        /// Disable auto-update.
        #[arg(long, conflicts_with = "enable")]
        disable: bool,
    },
}

#[async_trait]
impl Command for UpdateArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            None | Some(UpdateSubcommand::Check) => run_check().await,
            Some(UpdateSubcommand::Apply { yes }) => run_apply(*yes).await,
            Some(UpdateSubcommand::Auto { enable, disable }) => {
                if *enable {
                    run_auto(true).await
                } else if *disable {
                    run_auto(false).await
                } else {
                    eprintln!("cascade update auto requires --enable or --disable");
                    std::process::exit(1);
                }
            }
        }
    }
}

// ── check ─────────────────────────────────────────────────────────────────────

async fn run_check() -> Result<()> {
    let client = ipc_client()?;
    let result = client
        .send::<UpdateCheckParams, UpdateCheckResult>("update_check", UpdateCheckParams {})
        .await;

    match result {
        Ok(res) => {
            if res.update_available {
                let latest = res.latest_version.as_deref().unwrap_or("unknown");
                println!(
                    "Update available: {} — run `cascade update apply` to install.",
                    latest
                );
            } else {
                println!("Up to date ({})", res.current_version);
            }
            Ok(())
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => daemon_not_running(),
        Err(e) => ipc_error(e),
    }
}

// ── apply ─────────────────────────────────────────────────────────────────────

async fn run_apply(yes: bool) -> Result<()> {
    if !yes {
        eprint!("Download and apply the latest update? [y/N] ");
        let mut input = String::new();
        std::io::stdin()
            .read_line(&mut input)
            .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;
        if !input.trim().eq_ignore_ascii_case("y") {
            println!("Aborted.");
            return Ok(());
        }
    }

    println!("Checking for update…");
    let client = ipc_client()?;

    let result = client
        .send::<UpdateApplyParams, UpdateApplyResult>("update_apply", UpdateApplyParams {})
        .await;

    match result {
        Ok(res) if res.ok => {
            let version = res.installed_version.as_deref().unwrap_or("unknown");
            let snapshot_msg = res
                .snapshot_id
                .as_deref()
                .map(|id| format!(" (snapshot: {id})"))
                .unwrap_or_default();
            println!("Updated to {version}{snapshot_msg}. Daemon reloading.");
            Ok(())
        }
        Ok(res) => {
            let err = res.error.as_deref().unwrap_or("unknown error");
            eprintln!("Update failed: {err}");
            std::process::exit(1);
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => daemon_not_running(),
        Err(e) => ipc_error(e),
    }
}

// ── auto ──────────────────────────────────────────────────────────────────────

async fn run_auto(enable: bool) -> Result<()> {
    let client = ipc_client()?;
    let params = UpdateAutoParams { enable };

    let result = client
        .send::<UpdateAutoParams, UpdateAutoResult>("update_auto", params)
        .await;

    match result {
        Ok(res) => {
            let state = if res.auto_update { "enabled" } else { "disabled" };
            println!("Auto-update {state}.");
            Ok(())
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => daemon_not_running(),
        Err(e) => ipc_error(e),
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

fn ipc_client() -> Result<IpcClient> {
    IpcClient::new().map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))
}

fn daemon_not_running() -> Result<()> {
    eprintln!("cascade daemon is not running. Start it with: cascade daemon start");
    std::process::exit(1);
}

fn ipc_error(e: crate::ipc_client::IpcClientError) -> Result<()> {
    eprintln!("Error: {e}");
    std::process::exit(1);
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
    fn update_check_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "check"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Check)
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_apply_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "apply"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Apply { yes: false })
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_apply_yes_flag_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "apply", "--yes"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Apply { yes: true })
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_auto_enable_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "auto", "--enable"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => match args.subcommand {
                Some(super::UpdateSubcommand::Auto { enable, disable }) => {
                    assert!(enable);
                    assert!(!disable);
                }
                _ => panic!("expected Auto"),
            },
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_auto_disable_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "auto", "--disable"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => match args.subcommand {
                Some(super::UpdateSubcommand::Auto { enable, disable }) => {
                    assert!(!enable);
                    assert!(disable);
                }
                _ => panic!("expected Auto"),
            },
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_auto_enable_disable_conflict_fails() {
        let result =
            Cli::try_parse_from(["cascade", "update", "auto", "--enable", "--disable"]);
        assert!(result.is_err(), "enable and disable must conflict");
    }

    #[test]
    fn update_no_subcommand_defaults_to_check() {
        let cli = Cli::try_parse_from(["cascade", "update"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(args.subcommand.is_none());
            }
            _ => panic!("expected Update"),
        }
    }
}
