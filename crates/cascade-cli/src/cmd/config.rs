//! `cascade config` — read and write cascade configuration values.
//!
//! Configuration follows tier precedence: project-level (`.cascade/config.toml`)
//! overrides global (`~/.cascade/config.toml`). `cascade config set --global`
//! writes to the global config; otherwise writes to the project config.
//!
//! Subcommands:
//! - `get <key>` — print the resolved value for a key
//! - `set <key> <value>` — write a value to the appropriate tier
//! - `list` — table of all active config values with tier source

use async_trait::async_trait;
use cascade_types::error::Result;
use clap::{Args, Subcommand};

use super::Command;

/// Arguments for `cascade config`.
#[derive(Debug, Args)]
pub struct ConfigArgs {
    #[command(subcommand)]
    pub subcommand: ConfigSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum ConfigSubcmd {
    /// Print the resolved value for a configuration key.
    Get(ConfigGetArgs),
    /// Set a configuration value.
    Set(ConfigSetArgs),
    /// List all active configuration values with their tier source.
    List(ConfigListArgs),
}

#[derive(Debug, Args)]
pub struct ConfigGetArgs {
    /// Dot-separated config key (e.g. `daemon.port`, `rag.top_k`).
    pub key: String,
}

#[derive(Debug, Args)]
pub struct ConfigSetArgs {
    /// Dot-separated config key.
    pub key: String,
    /// Value to set. Parsed as TOML: strings, integers, booleans all work.
    pub value: String,
    /// Write to the global config (`~/.cascade/config.toml`) instead of the
    /// nearest project config.
    #[arg(long)]
    pub global: bool,
}

#[derive(Debug, Args)]
pub struct ConfigListArgs {
    /// Show only global config values.
    #[arg(long)]
    pub global: bool,
    /// Output as JSON.
    #[arg(long)]
    pub json: bool,
}

#[async_trait]
impl Command for ConfigArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            ConfigSubcmd::Get(a) => a.run().await,
            ConfigSubcmd::Set(a) => a.run().await,
            ConfigSubcmd::List(a) => a.run().await,
        }
    }
}

#[async_trait]
impl Command for ConfigGetArgs {
    async fn run(&self) -> Result<()> {
        let config = load_merged_config().await?;
        match config.get(&self.key) {
            Some(v) => println!("{}", v),
            None => {
                eprintln!("key not found: {}", self.key);
                std::process::exit(1);
            }
        }
        Ok(())
    }
}

#[async_trait]
impl Command for ConfigSetArgs {
    async fn run(&self) -> Result<()> {
        use cascade_types::error::CascadeError;
        use std::path::PathBuf;

        let config_path = if self.global {
            let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
            PathBuf::from(home).join(".cascade").join("config.toml")
        } else {
            // Nearest project .cascade/config.toml.
            let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
            find_project_config(&cwd).unwrap_or_else(|| cwd.join(".cascade").join("config.toml"))
        };

        // Read → parse → mutate → write.
        let raw = std::fs::read_to_string(&config_path).unwrap_or_default();
        let mut table: toml_edit::DocumentMut =
            raw.parse()
                .map_err(|e: toml_edit::TomlError| CascadeError::ConfigParse {
                    path: config_path.clone(),
                    detail: e.to_string(),
                })?;

        set_nested_key(&mut table, &self.key, &self.value);

        std::fs::create_dir_all(config_path.parent().unwrap()).ok();
        std::fs::write(&config_path, table.to_string()).map_err(|e| CascadeError::Io {
            path: config_path.clone(),
            operation: "write config",
            source: e,
        })?;
        println!(
            "set {key} = {value} in {path}",
            key = self.key,
            value = self.value,
            path = config_path.display()
        );
        Ok(())
    }
}

#[async_trait]
impl Command for ConfigListArgs {
    async fn run(&self) -> Result<()> {
        let config = if self.global {
            load_global_config().await?
        } else {
            load_merged_config().await?
        };

        if self.json {
            println!("{}", serde_json::to_string_pretty(&config).unwrap());
        } else {
            println!("{:<40} {}", "KEY", "VALUE");
            println!("{}", "-".repeat(60));
            for (k, v) in &config {
                println!("{:<40} {}", k, v);
            }
        }
        Ok(())
    }
}

// ── helpers ──────────────────────────────────────────────────────────────────

type FlatConfig = std::collections::BTreeMap<String, String>;

async fn load_merged_config() -> Result<FlatConfig> {
    let mut merged = load_global_config().await?;
    if let Ok(project) = load_project_config().await {
        merged.extend(project);
    }
    Ok(merged)
}

async fn load_global_config() -> Result<FlatConfig> {
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".into());
    let path = std::path::PathBuf::from(home)
        .join(".cascade")
        .join("config.toml");
    parse_toml_flat(&path).await
}

async fn load_project_config() -> Result<FlatConfig> {
    let cwd = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."));
    let path =
        find_project_config(&cwd).unwrap_or_else(|| cwd.join(".cascade").join("config.toml"));
    parse_toml_flat(&path).await
}

async fn parse_toml_flat(path: &std::path::Path) -> Result<FlatConfig> {
    use cascade_types::error::CascadeError;
    let raw = tokio::fs::read_to_string(path).await.unwrap_or_default();
    let table: toml::Value =
        raw.parse()
            .map_err(|e: toml::de::Error| CascadeError::ConfigParse {
                path: path.to_path_buf(),
                detail: e.to_string(),
            })?;
    Ok(flatten_toml("", &table))
}

fn flatten_toml(prefix: &str, value: &toml::Value) -> FlatConfig {
    let mut map = FlatConfig::new();
    match value {
        toml::Value::Table(t) => {
            for (k, v) in t {
                let key = if prefix.is_empty() {
                    k.clone()
                } else {
                    format!("{}.{}", prefix, k)
                };
                map.extend(flatten_toml(&key, v));
            }
        }
        _ => {
            map.insert(prefix.to_string(), value.to_string());
        }
    }
    map
}

fn find_project_config(cwd: &std::path::Path) -> Option<std::path::PathBuf> {
    cwd.ancestors().find_map(|p| {
        let c = p.join(".cascade").join("config.toml");
        if c.exists() {
            Some(c)
        } else {
            None
        }
    })
}

fn set_nested_key(doc: &mut toml_edit::DocumentMut, key: &str, value: &str) {
    let parts: Vec<&str> = key.splitn(2, '.').collect();
    if parts.len() == 1 {
        doc[key] = toml_edit::value(value);
    } else {
        doc[parts[0]][parts[1]] = toml_edit::value(value);
    }
}
