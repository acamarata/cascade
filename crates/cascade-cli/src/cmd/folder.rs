//! `cascade folder` — manage the AI folder preference.
//!
//! # Purpose
//! Lets users choose which folder name Cascade uses for its working files:
//! `.cascade` (default), `.claude`, `.codex`, `.opencode`, or any custom name.
//!
//! # Subcommands
//! - `cascade folder get`         — print the current AI folder name
//! - `cascade folder set <name>`  — persist a new preference to config.toml
//! - `cascade folder migrate-folder <to>` — move current AI folder to a new name
//!
//! # Inputs
//! - The AI folder name (with or without a leading dot).
//!
//! # Outputs
//! - Printed status lines and the written config path.
//!
//! # Constraints
//! - `set` writes to the nearest project config (or `--global` for the global
//!   config) without touching any other key.
//! - `migrate-folder` is non-destructive: copy-verify-swap, never deletes
//!   source until destination is verified.
//!
//! # SPORT
//! MASTER-CLI.md — folder: cascade_folder_cmd

use async_trait::async_trait;
use cascade_core::ai_folder::{migrate_folder, resolve_folder_name};
use cascade_types::config::AiFolder;
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};
use std::path::PathBuf;

use super::Command;

/// Arguments for `cascade folder`.
#[derive(Debug, Args)]
pub struct FolderArgs {
    #[command(subcommand)]
    pub subcommand: FolderSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum FolderSubcmd {
    /// Print the currently active AI folder name.
    Get(FolderGetArgs),
    /// Persist a new AI folder preference to config.toml.
    Set(FolderSetArgs),
    /// Move the current AI folder's contents to a new name (non-destructive).
    #[command(name = "migrate-folder")]
    MigrateFolder(FolderMigrateArgs),
}

#[derive(Debug, Args)]
pub struct FolderGetArgs {
    /// Directory to resolve the AI folder for (default: CWD).
    #[arg(long)]
    pub dir: Option<PathBuf>,
}

#[derive(Debug, Args)]
pub struct FolderSetArgs {
    /// New AI folder name. Examples: `.cascade`, `.claude`, `.codex`.
    pub name: String,

    /// Write to the global config (`~/.cascade/config.toml`) instead of the
    /// nearest project config.
    #[arg(long)]
    pub global: bool,
}

#[derive(Debug, Args)]
pub struct FolderMigrateArgs {
    /// Target AI folder name to migrate to (e.g. `.cascade`).
    pub to: String,

    /// Directory that contains the current AI folder (default: CWD).
    #[arg(long)]
    pub dir: Option<PathBuf>,

    /// Print what would happen without modifying anything.
    #[arg(long)]
    pub dry_run: bool,
}

// ── Command impl ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for FolderArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            FolderSubcmd::Get(a) => a.run().await,
            FolderSubcmd::Set(a) => a.run().await,
            FolderSubcmd::MigrateFolder(a) => a.run().await,
        }
    }
}

#[async_trait]
impl Command for FolderGetArgs {
    async fn run(&self) -> Result<()> {
        let dir = resolve_dir(self.dir.as_deref())?;
        // Load configured preference from the nearest config.toml.
        let configured = load_ai_folder_pref(&dir).await;
        let name = resolve_folder_name(&dir, configured.as_ref());
        println!("{}", name);
        Ok(())
    }
}

#[async_trait]
impl Command for FolderSetArgs {
    async fn run(&self) -> Result<()> {
        let folder = AiFolder::from_str_loose(&self.name);
        let folder_name = folder.folder_name().to_owned();

        // Determine config path.
        let config_path = if self.global {
            let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
            PathBuf::from(home).join(".cascade").join("config.toml")
        } else {
            let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
            find_project_config(&cwd)
                .unwrap_or_else(|| cwd.join(".cascade").join("config.toml"))
        };

        // Read → edit (just ai_folder) → write via toml_edit for round-trip safety.
        let raw = std::fs::read_to_string(&config_path).unwrap_or_default();
        let mut doc: toml_edit::DocumentMut =
            raw.parse()
                .map_err(|e: toml_edit::TomlError| CascadeError::ConfigParse {
                    path: config_path.clone(),
                    detail: e.to_string(),
                })?;

        // Serialize AiFolder to its serde representation.
        let toml_val = match &folder {
            AiFolder::Custom(s) => {
                // Custom serializes as { custom = "<name>" }.
                let mut t = toml_edit::InlineTable::new();
                t.insert("custom", toml_edit::Value::from(s.as_str()));
                toml_edit::Value::InlineTable(t)
            }
            _ => {
                // Named variants serialize as their lowercase name string.
                let s = match &folder {
                    AiFolder::Cascade => "cascade",
                    AiFolder::Claude => "claude",
                    AiFolder::Codex => "codex",
                    AiFolder::Opencode => "opencode",
                    AiFolder::Custom(_) => unreachable!(),
                };
                toml_edit::Value::from(s)
            }
        };

        doc["ai_folder"] = toml_edit::Item::Value(toml_val);

        std::fs::create_dir_all(config_path.parent().unwrap()).ok();
        std::fs::write(&config_path, doc.to_string()).map_err(|e| CascadeError::Io {
            path: config_path.clone(),
            operation: "write config",
            source: e,
        })?;

        println!(
            "ai_folder set to {folder_name} in {path}",
            folder_name = folder_name,
            path = config_path.display()
        );
        Ok(())
    }
}

#[async_trait]
impl Command for FolderMigrateArgs {
    async fn run(&self) -> Result<()> {
        let dir = resolve_dir(self.dir.as_deref())?;

        // Resolve the current active AI folder.
        let configured = load_ai_folder_pref(&dir).await;
        let current_name = resolve_folder_name(&dir, configured.as_ref());
        let from = dir.join(&current_name);

        let to_folder = AiFolder::from_str_loose(&self.to);
        let to_name = to_folder.folder_name().to_owned();
        let to = dir.join(&to_name);

        if current_name == to_name {
            println!("Already using {current_name}; nothing to do.");
            return Ok(());
        }

        if self.dry_run {
            println!(
                "[dry-run] Would migrate {} → {}",
                from.display(),
                to.display()
            );
        } else {
            println!("Migrating {} → {}", from.display(), to.display());
        }

        let result = migrate_folder(&from, &to, self.dry_run)?;

        if self.dry_run {
            println!(
                "[dry-run] {} entries would be moved.",
                result.entries_moved
            );
        } else {
            println!("Moved {} entries.", result.entries_moved);
            println!("Run `cascade folder set {}` to update your config.", to_name);
        }

        Ok(())
    }
}

// ── helpers ───────────────────────────────────────────────────────────────────

fn resolve_dir(explicit: Option<&std::path::Path>) -> Result<PathBuf> {
    if let Some(d) = explicit {
        return Ok(d.to_path_buf());
    }
    std::env::current_dir().map_err(|e| CascadeError::Io {
        path: PathBuf::from("."),
        operation: "get cwd",
        source: e,
    })
}

/// Try to load the `ai_folder` setting from the nearest `config.toml`.
/// Returns `None` if no config is found or the key is absent.
async fn load_ai_folder_pref(dir: &std::path::Path) -> Option<AiFolder> {
    let config_path = find_project_config(dir)
        .or_else(|| {
            let home = std::env::var("HOME").ok()?;
            let p = PathBuf::from(home).join(".cascade").join("config.toml");
            p.exists().then_some(p)
        })?;

    let raw = tokio::fs::read_to_string(&config_path).await.ok()?;
    let table: toml::Value = raw.parse().ok()?;
    let val = table.get("ai_folder")?;
    // Deserialize via serde round-trip.
    let folder: AiFolder = val.clone().try_into().ok()?;
    Some(folder)
}

fn find_project_config(cwd: &std::path::Path) -> Option<PathBuf> {
    cwd.ancestors().find_map(|p| {
        // Check both .cascade/config.toml and the configured folder name.
        for candidate in [".cascade", ".claude", ".codex", ".opencode"] {
            let c = p.join(candidate).join("config.toml");
            if c.exists() {
                return Some(c);
            }
        }
        None
    })
}
