//! `cascade telemetry` — manage telemetry opt-in/out.
//!
//! Purpose: Let the user enable, disable, or inspect the OTLP telemetry gate
//! in `~/.cascade/config.toml` without hand-editing TOML.
//!
//! Subcommands:
//!   enable   — set telemetry.enabled = true in ~/.cascade/config.toml
//!   disable  — set telemetry.enabled = false
//!   status   — print current state (enabled/disabled, endpoint)
//!
//! Inputs:  subcommand action (enable / disable / status).
//! Outputs: stdout confirmation message; config.toml updated on enable/disable.
//! Constraints: default is disabled; never prompts; never auto-enables.
//! SPORT: MASTER-CLI.md → cascade telemetry (priv-01)

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};
use std::path::PathBuf;
use toml_edit::DocumentMut;

use super::Command;

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade telemetry`.
#[derive(Debug, Args)]
pub struct TelemetryArgs {
    #[command(subcommand)]
    pub action: TelemetryAction,
}

/// Subcommands for `cascade telemetry`.
#[derive(Debug, Subcommand)]
pub enum TelemetryAction {
    /// Opt in to telemetry (sets telemetry.enabled = true).
    Enable,
    /// Opt out of telemetry (sets telemetry.enabled = false).
    Disable,
    /// Print current telemetry opt-in state.
    Status,
}

// ── Implementation ────────────────────────────────────────────────────────────

#[async_trait]
impl Command for TelemetryArgs {
    async fn run(&self) -> Result<()> {
        let home = std::env::var("HOME").unwrap_or_else(|_| String::from("."));
        let config_path = PathBuf::from(&home).join(".cascade").join("config.toml");

        match &self.action {
            TelemetryAction::Enable => {
                set_telemetry_enabled(&config_path, true)?;
                println!("Telemetry enabled. Spans will be exported to the configured endpoint.");
                println!("Run `cascade telemetry status` to review the current configuration.");
            }
            TelemetryAction::Disable => {
                set_telemetry_enabled(&config_path, false)?;
                println!("Telemetry disabled. No data leaves this machine.");
            }
            TelemetryAction::Status => {
                let raw = std::fs::read_to_string(&config_path).unwrap_or_default();
                let table: DocumentMut = raw.parse().unwrap_or_default();
                let enabled = table
                    .get("telemetry")
                    .and_then(|t| t.get("enabled"))
                    .and_then(|v| v.as_bool())
                    .unwrap_or(false);
                let endpoint = table
                    .get("telemetry")
                    .and_then(|t| t.get("endpoint"))
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_owned();
                println!(
                    "Telemetry: {}",
                    if enabled {
                        "enabled"
                    } else {
                        "disabled (default)"
                    }
                );
                if enabled && !endpoint.is_empty() {
                    println!("Endpoint:  {endpoint}");
                } else if enabled {
                    println!("Endpoint:  (none -- spans stored locally only)");
                }
                println!("Config:    {}", config_path.display());
                println!("\nTo change: cascade telemetry enable | disable");
            }
        }
        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn set_telemetry_enabled(config_path: &PathBuf, enabled: bool) -> Result<()> {
    std::fs::create_dir_all(config_path.parent().expect("config path has parent")).ok();
    let raw = std::fs::read_to_string(config_path).unwrap_or_default();
    let mut table: DocumentMut = raw.parse().unwrap_or_default();

    if table.get("telemetry").is_none() {
        table["telemetry"] = toml_edit::table();
    }
    table["telemetry"]["enabled"] = toml_edit::value(enabled);

    std::fs::write(config_path, table.to_string()).map_err(|e| {
        cascade_types::error::CascadeError::Io {
            path: config_path.clone(),
            operation: "telemetry write config",
            source: e,
        }
    })?;
    Ok(())
}
