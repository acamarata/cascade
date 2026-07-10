//! `cascade harness` — detect and configure AI coding harnesses.
//!
//! ## Subcommands
//!   cascade harness status            — show running harnesses (CC, OC, Codex) in a table
//!   cascade harness detect            — raw JSON output of all detected harnesses
//!   cascade harness codex install     — detect Codex and scaffold codex/config.yaml with Cascade MCP entry
//!   cascade harness codex status      — show active Codex sessions (PID, workspace, started)
//!   cascade harness generate          — generate harness-native instruction files from resolved cascade
//!   cascade harness init-from-installed — autodetect installed harnesses + scaffold each (E-P7-03)
//!
//! ## SPORT
//! MASTER-CLI.md: cascade harness subcommands (T-P4-E06-01..05, E-P7-03)

use std::path::PathBuf;

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};
use serde::Serialize;

use super::Command;

// ── Detection helpers (CLI-side) ──────────────────────────────────────────────

/// Detection result for a single harness.
///
/// Purpose: portable representation used by both the `status` table printer
///   and the `detect` JSON output.
/// Constraints: `binary` is `None` when the harness is not installed or its
///   binary cannot be resolved on `PATH` (detection via config dir only).
#[derive(Debug, Serialize)]
pub struct HarnessInfo {
    /// Harness identifier (e.g. "claude-code", "opencode").
    pub id: String,
    /// Human-readable display name.
    pub name: String,
    /// Whether the harness is installed and detectable on this machine.
    pub installed: bool,
    /// Resolved binary path on `PATH`, if any.
    pub binary: Option<String>,
}

/// Detect all known harnesses and return their installation status.
///
/// Purpose: single source of truth for both `harness status` and
///   `harness detect`; runs entirely in the CLI process using
///   `cascade-harness` detection logic (no IPC required).
/// Inputs: `workspace` — current working directory (used for config-dir
///   detection, e.g. `.cursor/` presence).
/// Outputs: `Vec<HarnessInfo>` — one entry per known harness, in
///   `HarnessKind::ALL` order.
fn detect_all_harnesses(workspace: &std::path::Path) -> Vec<HarnessInfo> {
    use cascade_harness::generate::harness_files::HarnessKind;

    HarnessKind::ALL
        .iter()
        .map(|kind| {
            let installed = kind.is_installed(workspace);
            let binary = if installed {
                resolve_binary_path(kind.id())
            } else {
                None
            };
            HarnessInfo {
                id: kind.id().to_string(),
                name: harness_display_name(kind.id()),
                installed,
                binary,
            }
        })
        .collect()
}

/// Map a harness id to a human-readable display name.
fn harness_display_name(id: &str) -> String {
    match id {
        "claude-code" => "Claude Code".to_string(),
        "opencode" => "OpenCode".to_string(),
        "codex" => "Codex".to_string(),
        "cursor" => "Cursor".to_string(),
        "aider" => "Aider".to_string(),
        "antigravity" => "Antigravity".to_string(),
        other => other.to_string(),
    }
}

/// Resolve the first matching binary for `name` on `PATH`.
///
/// Purpose: obtain the full path to a harness binary for display in
///   `harness status --json` and the BINARY column of `harness status`.
/// Inputs: `name` — harness id (e.g. "claude-code").
/// Outputs: `Some(path_string)` when found; `None` otherwise (not an error).
/// Constraints: `claude-code` maps to the `claude` binary name.
fn resolve_binary_path(name: &str) -> Option<String> {
    // claude-code's binary is `claude`, not `claude-code`.
    let binary_name = match name {
        "claude-code" => "claude",
        other => other,
    };
    std::env::var_os("PATH").and_then(|paths| {
        std::env::split_paths(&paths).find_map(|dir| {
            let candidate = dir.join(binary_name);
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                let ok = candidate.is_file()
                    || candidate
                        .metadata()
                        .map(|m| m.permissions().mode() & 0o111 != 0)
                        .unwrap_or(false);
                if ok {
                    return Some(candidate.display().to_string());
                }
            }
            #[cfg(not(unix))]
            {
                if candidate.exists() {
                    return Some(candidate.display().to_string());
                }
            }
            None
        })
    })
}

// ── Top-level harness args ────────────────────────────────────────────────────

/// Manage AI coding harnesses.
#[derive(Debug, Args)]
pub struct HarnessArgs {
    #[command(subcommand)]
    pub subcommand: HarnessSubcommand,
}

#[derive(Debug, Subcommand)]
pub enum HarnessSubcommand {
    /// Show the status of available harnesses in a human-readable table.
    Status(HarnessStatusArgs),
    /// Print raw JSON of all detected harnesses.
    Detect(HarnessDetectArgs),
    /// Codex CLI harness operations (install, status).
    Codex(HarnessCodexArgs),
    /// Generate harness instruction files from the resolved cascade for all supported harnesses.
    Generate(HarnessGenerateArgs),
    /// Autodetect installed harnesses and scaffold each with instruction files + MCP wiring (E-P7-03).
    InitFromInstalled(HarnessInitFromInstalledArgs),
}

// ── harness status ────────────────────────────────────────────────────────────

/// Arguments for `cascade harness status`.
#[derive(Debug, Args)]
pub struct HarnessStatusArgs {
    /// Output as JSON instead of a table.
    #[arg(long)]
    pub json: bool,
}

/// Arguments for `cascade harness detect`.
#[derive(Debug, Args)]
pub struct HarnessDetectArgs {
    /// (Placeholder for future filtering options)
    #[arg(long, hide = true)]
    pub _reserved: Option<bool>,
}

#[async_trait]
impl Command for HarnessArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            HarnessSubcommand::Status(args) => args.run().await,
            HarnessSubcommand::Detect(args) => args.run().await,
            HarnessSubcommand::Codex(args) => args.run().await,
            HarnessSubcommand::Generate(args) => args.run().await,
            HarnessSubcommand::InitFromInstalled(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for HarnessStatusArgs {
    async fn run(&self) -> Result<()> {
        let workspace = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."));
        let harnesses = detect_all_harnesses(&workspace);

        if self.json {
            let json = serde_json::to_string_pretty(&harnesses)
                .map_err(|e| CascadeError::Other(format!("json serialize failed: {e}")))?;
            println!("{json}");
        } else {
            println!("{:<14} {:<10} BINARY", "HARNESS", "INSTALLED");
            println!("{}", "─".repeat(60));
            for h in &harnesses {
                let installed = if h.installed { "yes" } else { "no" };
                let binary = h.binary.as_deref().unwrap_or("-");
                println!("{:<14} {:<10} {}", h.name, installed, binary);
            }
        }
        Ok(())
    }
}

#[async_trait]
impl Command for HarnessDetectArgs {
    async fn run(&self) -> Result<()> {
        let workspace = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."));
        let harnesses = detect_all_harnesses(&workspace);
        let json = serde_json::to_string_pretty(&harnesses)
            .map_err(|e| CascadeError::Other(format!("json serialize failed: {e}")))?;
        println!("{json}");
        Ok(())
    }
}

// ── harness codex ─────────────────────────────────────────────────────────────

/// Arguments for `cascade harness codex`.
#[derive(Debug, Args)]
pub struct HarnessCodexArgs {
    #[command(subcommand)]
    pub subcommand: HarnessCodexSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum HarnessCodexSubcmd {
    /// Detect Codex CLI and scaffold codex/config.yaml with the Cascade MCP entry.
    Install(HarnessCodexInstallArgs),
    /// Show active Codex sessions (PID, workspace, started).
    Status(HarnessCodexStatusArgs),
}

/// Arguments for `cascade harness codex install`.
#[derive(Debug, Args)]
pub struct HarnessCodexInstallArgs {
    /// Workspace directory (default: current working directory).
    #[arg(long, value_name = "PATH")]
    pub workspace: Option<PathBuf>,

    /// Daemon socket path for the CASCADE_SOCKET env var.
    #[arg(
        long,
        value_name = "SOCKET",
        default_value = "unix://~/.cascade/cascade.sock"
    )]
    pub socket: String,
}

/// Arguments for `cascade harness codex status`.
#[derive(Debug, Args)]
pub struct HarnessCodexStatusArgs {
    /// Output as JSON instead of a table.
    #[arg(long)]
    pub json: bool,
}

#[async_trait]
impl Command for HarnessCodexArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            HarnessCodexSubcmd::Install(args) => args.run().await,
            HarnessCodexSubcmd::Status(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for HarnessCodexInstallArgs {
    async fn run(&self) -> Result<()> {
        use cascade_harness::codex::detect::detect_codex;
        use cascade_harness::codex::scaffold::scaffold_codex_config;

        let workspace = self
            .workspace
            .clone()
            .unwrap_or_else(|| std::env::current_dir().unwrap_or_else(|_| PathBuf::from(".")));

        // Detect Codex
        match detect_codex() {
            Some(info) => {
                println!(
                    "Codex detected: {} (v{})",
                    info.path.display(),
                    info.version
                );
            }
            None => {
                println!("Codex CLI not found. Install it from https://github.com/openai/codex");
                println!("Scaffolding config.yaml anyway so it will work once Codex is installed.");
            }
        }

        // Scaffold config
        scaffold_codex_config(&workspace, &self.socket)
            .map_err(|e| CascadeError::Other(format!("scaffold_codex_config failed: {e}")))?;

        println!(
            "Cascade MCP entry written to {}/codex/config.yaml",
            workspace.display()
        );
        println!("Restart Codex to activate.");
        Ok(())
    }
}

#[async_trait]
impl Command for HarnessCodexStatusArgs {
    async fn run(&self) -> Result<()> {
        use cascade_harness::codex::monitor::CodexMonitor;

        let mut monitor = CodexMonitor::new();
        let sessions = monitor.snapshot();

        if sessions.is_empty() {
            println!("No active Codex sessions.");
            return Ok(());
        }

        if self.json {
            println!(
                "{}",
                serde_json::to_string_pretty(&sessions).unwrap_or_default()
            );
            return Ok(());
        }

        println!("{:<8} {:<40} STARTED", "PID", "WORKSPACE");
        println!("{}", "─".repeat(70));
        for s in &sessions {
            let ws = s
                .workspace
                .as_ref()
                .map(|p| p.display().to_string())
                .unwrap_or_else(|| "(unknown)".into());
            println!("{:<8} {:<40} {}", s.pid, ws, s.started_at);
        }
        Ok(())
    }
}

// ── harness generate ──────────────────────────────────────────────────────────

/// Which harness to generate files for.
#[derive(Debug, Clone, Copy, PartialEq, Eq, clap::ValueEnum)]
pub enum HarnessTarget {
    /// Claude Code (CLAUDE.md)
    #[value(name = "claude-code")]
    ClaudeCode,
    /// OpenCode (AGENTS.md)
    #[value(name = "opencode")]
    OpenCode,
    /// Codex (AGENTS.md)
    #[value(name = "codex")]
    Codex,
    /// Cursor (.cursor/rules/cascade.mdc)
    #[value(name = "cursor")]
    Cursor,
    /// Aider (CONVENTIONS.md)
    #[value(name = "aider")]
    Aider,
    /// Antigravity (.antigravity/cascade-rules.md) — detect+config mode (E-P7-06)
    #[value(name = "antigravity")]
    Antigravity,
    /// All supported harnesses
    #[value(name = "all")]
    All,
}

/// Arguments for `cascade harness generate`.
#[derive(Debug, Args)]
pub struct HarnessGenerateArgs {
    /// Workspace directory (default: current working directory).
    #[arg(long, value_name = "PATH")]
    pub workspace: Option<PathBuf>,

    /// Which harness to generate for.
    #[arg(long, value_enum, default_value = "all")]
    pub harness: HarnessTarget,

    /// Print what would be generated without writing files.
    #[arg(long)]
    pub dry_run: bool,

    /// Write one unified merged file to this path instead of per-harness files.
    #[arg(long, value_name = "PATH")]
    pub output_single_file: Option<PathBuf>,
}

#[async_trait]
impl Command for HarnessGenerateArgs {
    async fn run(&self) -> Result<()> {
        use cascade_core::cascade_resolution::resolve_cascade_full;
        use cascade_harness::generate::harness_files::{
            generate_for_harnesses, write_single_file, HarnessKind,
        };

        let workspace = self
            .workspace
            .clone()
            .unwrap_or_else(|| std::env::current_dir().unwrap_or_else(|_| PathBuf::from(".")));

        let resolved = resolve_cascade_full(&workspace)
            .map_err(|e| CascadeError::Other(format!("cascade resolution failed: {e}")))?;

        // Translate clap enum → HarnessKind slice.
        let harnesses: &[HarnessKind] = match self.harness {
            HarnessTarget::ClaudeCode => &[HarnessKind::ClaudeCode],
            HarnessTarget::OpenCode => &[HarnessKind::OpenCode],
            HarnessTarget::Codex => &[HarnessKind::Codex],
            HarnessTarget::Cursor => &[HarnessKind::Cursor],
            HarnessTarget::Aider => &[HarnessKind::Aider],
            HarnessTarget::Antigravity => &[HarnessKind::Antigravity],
            HarnessTarget::All => HarnessKind::ALL,
        };

        if let Some(ref single_path) = self.output_single_file {
            write_single_file(&resolved, single_path, harnesses, self.dry_run)
                .map_err(|e| CascadeError::Other(format!("write_single_file failed: {e}")))?;
        } else {
            generate_for_harnesses(&resolved, &workspace, harnesses, self.dry_run)
                .map_err(|e| CascadeError::Other(format!("generate_for_harnesses failed: {e}")))?;
        }

        Ok(())
    }
}

// ── harness init-from-installed ───────────────────────────────────────────────

/// Arguments for `cascade harness init-from-installed`.
#[derive(Debug, Args)]
pub struct HarnessInitFromInstalledArgs {
    /// Workspace directory (default: current working directory).
    #[arg(long, value_name = "PATH")]
    pub workspace: Option<PathBuf>,

    /// Print what would be scaffolded without writing files.
    #[arg(long)]
    pub dry_run: bool,
}

#[async_trait]
impl Command for HarnessInitFromInstalledArgs {
    async fn run(&self) -> Result<()> {
        use cascade_core::cascade_resolution::resolve_cascade_full;
        use cascade_harness::generate::harness_files::init_from_installed;

        let workspace = self
            .workspace
            .clone()
            .unwrap_or_else(|| std::env::current_dir().unwrap_or_else(|_| PathBuf::from(".")));

        let resolved = resolve_cascade_full(&workspace)
            .map_err(|e| CascadeError::Other(format!("cascade resolution failed: {e}")))?;

        let detected = init_from_installed(&resolved, &workspace, self.dry_run)
            .map_err(|e| CascadeError::Other(format!("init_from_installed failed: {e}")))?;

        if !detected.is_empty() && !self.dry_run {
            println!(
                "Scaffolded {} harness(es). Restart each harness to activate Cascade MCP.",
                detected.len()
            );
        }

        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// `detect_all_harnesses` must return one entry per `HarnessKind::ALL`
    /// element, with valid ids and names, and serialise cleanly to JSON.
    #[test]
    fn detect_all_harnesses_returns_all_kinds() {
        use cascade_harness::generate::harness_files::HarnessKind;

        let workspace = std::env::temp_dir();
        let results = detect_all_harnesses(&workspace);

        assert_eq!(
            results.len(),
            HarnessKind::ALL.len(),
            "must return one HarnessInfo per HarnessKind"
        );

        for info in &results {
            assert!(!info.id.is_empty(), "id must not be empty");
            assert!(!info.name.is_empty(), "name must not be empty");
            // binary is None or a non-empty string (never an empty string)
            if let Some(ref bin) = info.binary {
                assert!(!bin.is_empty(), "binary path string must not be empty");
            }
        }
    }

    /// `detect_all_harnesses` output must round-trip through JSON without error.
    #[test]
    fn detect_all_harnesses_json_roundtrip() {
        let workspace = std::env::temp_dir();
        let results = detect_all_harnesses(&workspace);
        let json = serde_json::to_string_pretty(&results).expect("must serialise to JSON");
        assert!(
            json.contains("installed"),
            "JSON must contain 'installed' field"
        );
        assert!(
            json.contains("claude-code"),
            "JSON must contain 'claude-code' id"
        );
    }

    /// `harness_display_name` must map all known ids to human-readable names.
    #[test]
    fn display_names_are_human_readable() {
        assert_eq!(harness_display_name("claude-code"), "Claude Code");
        assert_eq!(harness_display_name("opencode"), "OpenCode");
        assert_eq!(harness_display_name("codex"), "Codex");
        assert_eq!(harness_display_name("cursor"), "Cursor");
        assert_eq!(harness_display_name("aider"), "Aider");
        assert_eq!(harness_display_name("antigravity"), "Antigravity");
        // Unknown ids pass through unchanged.
        assert_eq!(harness_display_name("unknown-tool"), "unknown-tool");
    }

    /// `resolve_binary_path("claude-code")` must map to the `claude` binary name
    /// (not `claude-code`), since the real binary is `claude`.
    #[test]
    fn resolve_binary_path_maps_claude_code_to_claude() {
        // We cannot assert the result (depends on PATH), but we can verify the
        // function doesn't panic and returns None or Some(non-empty).
        let result = resolve_binary_path("claude-code");
        if let Some(ref path) = result {
            assert!(
                path.ends_with("claude") || path.contains("claude"),
                "resolved path for claude-code should reference 'claude' binary, got: {path}"
            );
        }
    }
}
