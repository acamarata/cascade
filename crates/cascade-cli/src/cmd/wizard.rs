//! `cascade wizard` — interactive first-run setup.
//!
//! Prompts the user for the three values needed to make cascade operational:
//!   1. `personal_dir`   — personal workspace root (default: ~/Downloads)
//!   2. `projects_dir`   — projects root (default: ~/Sites)
//!   3. provider API key — written to the global config
//!
//! Writes accepted values into `~/.cascade/config.toml` using the same
//! toml_edit path as `cascade config set`.
//!
//! ## Usage
//!
//! ```text
//! cascade wizard
//! ```
//!
//! All prompts show the current default in brackets. Press Enter to accept.
//!
//! SPORT: MASTER-CLI.md → cascade wizard (rag-11)

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::Args;
use std::io::{self, Write};
use std::path::PathBuf;

use super::Command;

// ── Args ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade wizard`.
#[derive(Debug, Args)]
pub struct WizardArgs {
    // No flags — the wizard is fully interactive.
}

// ── Implementation ────────────────────────────────────────────────────────────

#[async_trait]
impl Command for WizardArgs {
    async fn run(&self) -> Result<()> {
        let home = std::env::var("HOME").unwrap_or_else(|_| String::from("."));
        let home_path = PathBuf::from(&home);

        println!("Cascade setup wizard");
        println!("====================");
        println!("Press Enter to accept the default shown in [brackets].\n");

        // ── personal_dir ──────────────────────────────────────────────────────
        let default_personal = home_path.join("Downloads").display().to_string();
        let personal_dir = prompt(
            &format!("Personal workspace directory [{}]: ", default_personal),
            &default_personal,
        )?;

        // ── projects_dir ──────────────────────────────────────────────────────
        let default_projects = home_path.join("Sites").display().to_string();
        let projects_dir = prompt(
            &format!("Projects root directory [{}]: ", default_projects),
            &default_projects,
        )?;

        // ── provider key ──────────────────────────────────────────────────────
        let provider_key = prompt(
            "Provider API key (leave blank to skip): ",
            "",
        )?;

        // ── telemetry consent ─────────────────────────────────────────────────
        println!("\nCascade can optionally export tracing spans to a local OTLP collector.");
        println!("This is local-only by default; nothing is transmitted externally without");
        println!("additional configuration. See PRIVACY.md for details.");
        let telemetry_raw = prompt(
            "Enable telemetry? [y/N]: ",
            "n",
        )?;
        let telemetry_enabled = matches!(telemetry_raw.to_lowercase().as_str(), "y" | "yes");

        // ── Write to config ───────────────────────────────────────────────────
        let config_dir = home_path.join(".cascade");
        std::fs::create_dir_all(&config_dir).ok();
        let config_path = config_dir.join("config.toml");

        let raw = std::fs::read_to_string(&config_path).unwrap_or_default();
        let mut table: toml_edit::DocumentMut = raw.parse().unwrap_or_default();

        // personal_dir
        if personal_dir != default_personal {
            set_toml_key(&mut table, "personal_dir", &personal_dir);
        }

        // projects_dirs (array)
        if projects_dir != default_projects {
            let mut arr = toml_edit::Array::new();
            arr.push(projects_dir.as_str());
            table["projects_dirs"] = toml_edit::value(arr);
        }

        // provider key
        if !provider_key.is_empty() {
            set_toml_key(&mut table, "provider.key_id", &provider_key);
        }

        // telemetry consent
        {
            if table.get("telemetry").is_none() {
                table["telemetry"] = toml_edit::table();
            }
            table["telemetry"]["enabled"] = toml_edit::value(telemetry_enabled);
        }

        std::fs::write(&config_path, table.to_string()).map_err(|e| {
            cascade_types::error::CascadeError::Io {
                path: config_path.clone(),
                operation: "wizard write config",
                source: e,
            }
        })?;

        println!("\nConfiguration written to {}", config_path.display());
        println!("Run `cascade status` to verify the daemon is operational.");
        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Print `prompt` and read a line from stdin. Returns `default` if the user
/// presses Enter without input.
fn prompt(text: &str, default: &str) -> Result<String> {
    print!("{text}");
    io::stdout().flush().ok();

    let mut line = String::new();
    io::stdin().read_line(&mut line).map_err(|e| {
        cascade_types::error::CascadeError::Io {
            path: PathBuf::from("<stdin>"),
            operation: "wizard prompt",
            source: e,
        }
    })?;

    let trimmed = line.trim().to_owned();
    if trimmed.is_empty() {
        Ok(default.to_owned())
    } else {
        Ok(trimmed)
    }
}

/// Set a dot-separated key in a toml_edit document.
fn set_toml_key(table: &mut toml_edit::DocumentMut, key: &str, value: &str) {
    let parts: Vec<&str> = key.splitn(2, '.').collect();
    match parts.as_slice() {
        [k] => {
            table[*k] = toml_edit::value(value);
        }
        [section, field] => {
            if table[*section].is_none() {
                table[*section] = toml_edit::table();
            }
            table[*section][*field] = toml_edit::value(value);
        }
        _ => {}
    }
}
