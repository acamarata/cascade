//! `cascade update check | apply | auto | models` — update commands.
//!
//! Purpose: User-facing interface for the update pipeline (T-P4-E04-14/16).
//!
//! Subcommands:
//!   `cascade update check`              — query GitHub for a newer version; print result
//!   `cascade update apply [--yes]`      — trigger full download+verify+apply via IPC
//!   `cascade update auto [--enable|--disable]` — toggle auto_update in config.toml
//!   `cascade update models`             — refresh ~/.cascade/models.yaml from GitHub
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
use std::io::Write;
use std::path::{Path, PathBuf};

use super::Command;
use crate::ipc_client::IpcClient;

const MODELS_YAML_URL: &str =
    "https://raw.githubusercontent.com/acamarata/cascade/main/models/models.yaml";
const COMPILED_MODELS_YAML: &str = include_str!("../../../../models/models.yaml");

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
    /// Refresh the cached fleet model roster from GitHub.
    Models,
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
            Some(UpdateSubcommand::Models) => run_models().await,
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
            match res.snapshot_id.as_deref() {
                // A real install always snapshots the old binaries first; no
                // snapshot means nothing was swapped — we were already current.
                None => println!("Already up to date ({version})."),
                Some(id) => {
                    println!("Updated to {version} (snapshot: {id}). Daemon reloading.")
                }
            }
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
            let state = if res.auto_update {
                "enabled"
            } else {
                "disabled"
            };
            println!("Auto-update {state}.");
            Ok(())
        }
        Err(crate::ipc_client::IpcClientError::DaemonNotRunning) => daemon_not_running(),
        Err(e) => ipc_error(e),
    }
}

// ── models ────────────────────────────────────────────────────────────────────

#[derive(Debug, serde::Deserialize)]
struct ModelsYaml {
    providers: Vec<ProviderBlock>,
}

#[derive(Debug, serde::Deserialize)]
struct ProviderBlock {
    name: String,
}

#[derive(Debug)]
struct ValidatedModelsYaml {
    provider_count: usize,
}

#[derive(Debug)]
struct ModelsUpdateReport {
    cache_path: PathBuf,
    changed: bool,
    compared_to: &'static str,
    provider_count: usize,
}

async fn run_models() -> Result<()> {
    match update_models_cache().await {
        Ok(report) => {
            let change = if report.changed {
                "changed"
            } else {
                "unchanged"
            };
            println!(
                "Models roster refreshed ({change} vs {}; {} providers).",
                report.compared_to, report.provider_count
            );
            println!("Cache: {}", report.cache_path.display());
            Ok(())
        }
        Err(e) => {
            eprintln!("Failed to update models.yaml: {e}");
            std::process::exit(1);
        }
    }
}

async fn update_models_cache() -> Result<ModelsUpdateReport> {
    let fetched = fetch_models_yaml().await?;
    let validated = validate_models_yaml(&fetched)?;
    let cache_path = models_cache_path();

    let (baseline, compared_to) = match std::fs::read_to_string(&cache_path) {
        Ok(cached) => (cached, "cached models.yaml"),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            (COMPILED_MODELS_YAML.to_string(), "compiled-in default")
        }
        Err(_) => (
            COMPILED_MODELS_YAML.to_string(),
            "compiled-in default (cache unreadable)",
        ),
    };
    let changed = fetched != baseline;

    write_models_cache_atomically(&cache_path, &fetched)?;

    Ok(ModelsUpdateReport {
        cache_path,
        changed,
        compared_to,
        provider_count: validated.provider_count,
    })
}

async fn fetch_models_yaml() -> Result<String> {
    let client = reqwest::Client::builder()
        .use_rustls_tls()
        .build()
        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;
    let response = client
        .get(MODELS_YAML_URL)
        .header("User-Agent", "cascade-updater/1")
        .send()
        .await
        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?
        .error_for_status()
        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))?;

    response
        .text()
        .await
        .map_err(|e| cascade_types::error::CascadeError::Other(e.to_string()))
}

fn validate_models_yaml(contents: &str) -> Result<ValidatedModelsYaml> {
    let parsed: ModelsYaml = serde_yaml::from_str(contents).map_err(|e| {
        cascade_types::error::CascadeError::Other(format!(
            "fetched models.yaml is not valid YAML with providers[].name: {e}"
        ))
    })?;

    if parsed.providers.is_empty() {
        return Err(cascade_types::error::CascadeError::Other(
            "fetched models.yaml has no providers".to_string(),
        ));
    }

    for provider in &parsed.providers {
        if provider.name.trim().is_empty() {
            return Err(cascade_types::error::CascadeError::Other(
                "fetched models.yaml contains a provider with an empty name".to_string(),
            ));
        }
    }

    Ok(ValidatedModelsYaml {
        provider_count: parsed.providers.len(),
    })
}

fn models_cache_path() -> PathBuf {
    cascade_types::paths::global_cascade_dir().join("models.yaml")
}

fn write_models_cache_atomically(cache_path: &Path, contents: &str) -> Result<()> {
    let parent = cache_path
        .parent()
        .expect("models.yaml cache path has parent");
    std::fs::create_dir_all(parent).map_err(|e| cascade_types::error::CascadeError::Io {
        path: parent.to_path_buf(),
        operation: "create models cache directory",
        source: e,
    })?;

    let tmp_path = cache_path.with_file_name("models.yaml.tmp");
    let mut tmp =
        std::fs::File::create(&tmp_path).map_err(|e| cascade_types::error::CascadeError::Io {
            path: tmp_path.clone(),
            operation: "create temporary models cache",
            source: e,
        })?;
    tmp.write_all(contents.as_bytes())
        .map_err(|e| cascade_types::error::CascadeError::Io {
            path: tmp_path.clone(),
            operation: "write temporary models cache",
            source: e,
        })?;
    tmp.sync_all()
        .map_err(|e| cascade_types::error::CascadeError::Io {
            path: tmp_path.clone(),
            operation: "sync temporary models cache",
            source: e,
        })?;
    std::fs::rename(&tmp_path, cache_path).map_err(|e| cascade_types::error::CascadeError::Io {
        path: cache_path.to_path_buf(),
        operation: "replace models cache",
        source: e,
    })?;
    Ok(())
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
    fn update_models_parses() {
        let cli = Cli::try_parse_from(["cascade", "update", "models"]).unwrap();
        match cli.cmd {
            crate::cmd::Commands::Update(args) => {
                assert!(matches!(
                    args.subcommand,
                    Some(super::UpdateSubcommand::Models)
                ));
            }
            _ => panic!("expected Update"),
        }
    }

    #[test]
    fn update_auto_enable_disable_conflict_fails() {
        let result = Cli::try_parse_from(["cascade", "update", "auto", "--enable", "--disable"]);
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
