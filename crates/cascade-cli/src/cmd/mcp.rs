//! `cascade mcp` — MCP server token management and client setup.
//!
//! ## Purpose
//! Provides the `cascade mcp` subcommand surface for interacting with the
//! cascade MCP server: printing auth tokens, checking server status, and
//! configuring supported AI client tools.
//!
//! ## Subcommands
//! - `cascade mcp token` — print the current HMAC bearer token
//! - `cascade mcp status` — show MCP server transport status
//! - `cascade mcp setup --tool <tool>` — configure an MCP client
//! - `cascade mcp setup --all` — auto-detect and configure all clients
//! - `cascade mcp setup --list` — detect clients without configuring
//!
//! ## Inputs
//! - `~/.cascade/runtime/mcp-secret.key` — 32-byte secret key file
//! - Platform-specific client config files (merged non-destructively)
//!
//! ## Outputs
//! - Token printed to stdout (no trailing newline decorations)
//! - Config files written atomically (write temp → rename)
//!
//! ## Constraints
//! - Atomic writes: temp file → rename, never direct overwrite
//! - Non-destructive: existing config entries preserved; cascade entry upserted
//! - HOME-confined: all paths derived from home_dir(), never hard-coded
//! - Backup existing config before first write (`.bak` suffix)
//!
//! ## SPORT
//! MASTER-CLI.md: cascade mcp subcommands

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use cascade_mcp::{McpAuth, McpSecretManager};
use cascade_types::{
    error::{CascadeError, Result},
    paths::home_dir,
};
use clap::{Args, Subcommand, ValueEnum};
use serde_json::{json, Value};

use super::Command;

// ── Top-level args ────────────────────────────────────────────────────────────

/// Arguments for `cascade mcp`.
#[derive(Debug, Args)]
pub struct McpArgs {
    #[command(subcommand)]
    pub subcommand: McpSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum McpSubcmd {
    /// Print the current MCP auth token for manual client configuration.
    Token(McpTokenArgs),
    /// Show MCP server transport status and active connections.
    Status(McpStatusArgs),
    /// Configure an AI client tool to connect to the cascade MCP server.
    Setup(McpSetupArgs),
}

// ── Token ─────────────────────────────────────────────────────────────────────

/// Arguments for `cascade mcp token`.
#[derive(Debug, Args)]
pub struct McpTokenArgs;

impl McpTokenArgs {
    fn secret_path() -> PathBuf {
        home_dir().join(".cascade").join("runtime").join("mcp-secret.key")
    }

    pub fn load_auth() -> Result<McpAuth> {
        let path = Self::secret_path();
        if !path.exists() {
            return Err(CascadeError::Other(
                "Cascade MCP server not running or not yet initialized. Run: cascade daemon start"
                    .into(),
            ));
        }
        McpSecretManager::load_or_create(&path).map_err(|e| CascadeError::Other(e.to_string()))
    }

    pub fn mint_token() -> Result<String> {
        let auth = Self::load_auth()?;
        Ok(auth.generate_token())
    }
}

#[async_trait]
impl Command for McpTokenArgs {
    async fn run(&self) -> Result<()> {
        let token = McpTokenArgs::mint_token()?;
        print!("{token}");
        Ok(())
    }
}

// ── Status ────────────────────────────────────────────────────────────────────

/// Arguments for `cascade mcp status`.
#[derive(Debug, Args)]
pub struct McpStatusArgs;

#[async_trait]
impl Command for McpStatusArgs {
    async fn run(&self) -> Result<()> {
        let runtime_dir = home_dir().join(".cascade").join("runtime");
        let socket_path = runtime_dir.join("mcp.sock");

        let socket_status = if socket_path.exists() { "active" } else { "inactive" };

        println!("MCP Server Status");
        println!("─────────────────────────────────────");
        println!("Unix socket:  {socket_status}  ({})", socket_path.display());
        println!("HTTP:         http://127.0.0.1:7722/mcp");
        println!("stdio:        available (subprocess mode)");

        // Token (best-effort — may not exist if server not started)
        match McpTokenArgs::mint_token() {
            Ok(token) => println!("\nAuth token:   {token}"),
            Err(e) => println!("\nAuth token:   unavailable ({e})"),
        }

        Ok(())
    }
}

// ── Setup ─────────────────────────────────────────────────────────────────────

/// Which AI client tool to configure.
#[derive(Debug, Clone, ValueEnum)]
pub enum ToolName {
    /// Claude Code CLI
    #[value(name = "claude-code")]
    ClaudeCode,
    /// Claude Desktop app
    #[value(name = "claude-desktop")]
    ClaudeDesktop,
    /// VS Code (+ Continue.dev)
    #[value(name = "vscode")]
    Vscode,
    /// OpenCode
    #[value(name = "opencode")]
    Opencode,
}

/// Arguments for `cascade mcp setup`.
#[derive(Debug, Args)]
pub struct McpSetupArgs {
    /// The AI client tool to configure.
    #[arg(long, value_enum)]
    pub tool: Option<ToolName>,

    /// Configure all detected clients automatically.
    #[arg(long, conflicts_with = "tool")]
    pub all: bool,

    /// List detected clients without configuring anything.
    #[arg(long, conflicts_with = "tool")]
    pub list: bool,

    /// Remove the cascade entry from the target config instead of adding it.
    #[arg(long)]
    pub remove: bool,

    /// Use HTTP transport instead of stdio (for tools that support it).
    #[arg(long)]
    pub http: bool,

    /// Write to project-local config instead of global (opencode only).
    #[arg(long)]
    pub local: bool,

    /// Write to global user config (vscode: ~/.vscode/mcp.json).
    #[arg(long)]
    pub global: bool,

    /// Print what would be written without modifying any files.
    #[arg(long)]
    pub dry_run: bool,
}

#[async_trait]
impl Command for McpSetupArgs {
    async fn run(&self) -> Result<()> {
        if self.list {
            return setup_list();
        }
        if self.all {
            return setup_all(self.remove, self.dry_run);
        }
        match &self.tool {
            Some(ToolName::ClaudeCode) => {
                setup_claude_code(self.remove, self.dry_run)
            }
            Some(ToolName::ClaudeDesktop) => {
                setup_claude_desktop(self.remove, self.dry_run)
            }
            Some(ToolName::Vscode) => {
                setup_vscode(self.remove, self.global, self.dry_run)
            }
            Some(ToolName::Opencode) => {
                setup_opencode(self.remove, self.local, self.http, self.dry_run)
            }
            None => {
                eprintln!("Error: specify --tool <tool>, --all, or --list");
                eprintln!("  Tools: claude-code, claude-desktop, vscode, opencode");
                Err(CascadeError::Other("no tool specified".into()))
            }
        }
    }
}

// ── Setup helpers — shared ────────────────────────────────────────────────────

/// Read a JSON file or return `{}` if absent.
fn read_json_or_empty(path: &Path) -> Result<Value> {
    if !path.exists() {
        return Ok(json!({}));
    }
    let raw = std::fs::read_to_string(path)
        .map_err(|e| CascadeError::Other(format!("failed to read {}: {e}", path.display())))?;
    serde_json::from_str(&raw)
        .map_err(|e| CascadeError::Other(format!("failed to parse JSON {}: {e}", path.display())))
}

/// Atomically write JSON to `path` (write to `.tmp`, then rename).
/// Creates parent directories if needed. Backs up existing file to `.bak` on
/// first write.
fn write_json_atomic(path: &Path, value: &Value, dry_run: bool) -> Result<()> {
    let pretty = serde_json::to_string_pretty(value)
        .map_err(|e| CascadeError::Other(format!("JSON serialise error: {e}")))?;

    if dry_run {
        println!("--- (dry-run) would write: {} ---", path.display());
        println!("{pretty}");
        println!("--- end ---");
        return Ok(());
    }

    // Ensure parent exists
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| {
            CascadeError::Other(format!("cannot create dir {}: {e}", parent.display()))
        })?;
    }

    // Backup existing file (best-effort, once)
    if path.exists() {
        let bak = path.with_extension("json.bak");
        if !bak.exists() {
            let _ = std::fs::copy(path, &bak);
        }
    }

    // Write to temp then rename (atomic)
    let tmp = path.with_extension("json.tmp");
    std::fs::write(&tmp, pretty.as_bytes())
        .map_err(|e| CascadeError::Other(format!("write tmp {}: {e}", tmp.display())))?;
    std::fs::rename(&tmp, path)
        .map_err(|e| CascadeError::Other(format!("rename to {}: {e}", path.display())))?;
    Ok(())
}

/// Ensure `obj["mcp_key"]` is a JSON object, upsert `entry_key → entry_value`.
/// Returns `true` if a change was made.
fn upsert_key(obj: &mut Value, mcp_key: &str, entry_key: &str, entry_value: Value) -> bool {
    let top = obj
        .as_object_mut()
        .expect("caller must pass a JSON object");
    let servers = top
        .entry(mcp_key)
        .or_insert_with(|| json!({}));
    let servers_obj = servers
        .as_object_mut()
        .expect("mcpServers/servers must be an object");
    let old = servers_obj.get(entry_key).cloned();
    servers_obj.insert(entry_key.to_string(), entry_value.clone());
    old.as_ref() != Some(&entry_value)
}

/// Remove `obj["mcp_key"]["cascade"]`. Returns `true` if it was present.
fn remove_cascade(obj: &mut Value, mcp_key: &str) -> bool {
    obj.get_mut(mcp_key)
        .and_then(|v| v.as_object_mut())
        .and_then(|m| m.remove("cascade"))
        .is_some()
}

// ── Claude Code ───────────────────────────────────────────────────────────────

/// `~/.claude/settings.json`
pub fn claude_code_settings_path() -> PathBuf {
    home_dir().join(".claude").join("settings.json")
}

/// Merge or remove the cascade entry in `~/.claude/settings.json`.
pub fn setup_claude_code(remove: bool, dry_run: bool) -> Result<()> {
    let path = claude_code_settings_path();
    let mut config = read_json_or_empty(&path)?;

    if remove {
        let changed = remove_cascade(&mut config, "mcpServers");
        if changed {
            write_json_atomic(&path, &config, dry_run)?;
            if !dry_run {
                println!("cascade MCP server removed from Claude Code settings.");
            }
        } else {
            println!("cascade entry not found in Claude Code settings.");
        }
        return Ok(());
    }

    let cascade_entry = json!({
        "command": "cascade",
        "args": ["mcp", "--stdio"],
        "env": {}
    });
    upsert_key(&mut config, "mcpServers", "cascade", cascade_entry);
    write_json_atomic(&path, &config, dry_run)?;
    if !dry_run {
        println!("cascade MCP server added to Claude Code. Restart Claude Code to activate.");
    }
    Ok(())
}

// ── Claude Desktop ────────────────────────────────────────────────────────────

/// Returns the platform-specific Claude Desktop config path, or `None` if the
/// platform is unsupported.
pub fn claude_desktop_config_path() -> Option<PathBuf> {
    match std::env::consts::OS {
        "macos" => Some(
            home_dir()
                .join("Library")
                .join("Application Support")
                .join("Claude")
                .join("claude_desktop_config.json"),
        ),
        "linux" => Some(
            home_dir()
                .join(".config")
                .join("Claude")
                .join("claude_desktop_config.json"),
        ),
        "windows" => {
            std::env::var("APPDATA").ok().map(|appdata| {
                PathBuf::from(appdata)
                    .join("Claude")
                    .join("claude_desktop_config.json")
            })
        }
        _ => None,
    }
}

/// Merge or remove the cascade entry in the Claude Desktop config.
pub fn setup_claude_desktop(remove: bool, dry_run: bool) -> Result<()> {
    let path = match claude_desktop_config_path() {
        Some(p) => p,
        None => {
            println!("Claude Desktop: unsupported platform.");
            return Ok(());
        }
    };

    // If config file doesn't exist, Claude Desktop is likely not installed
    if !path.exists() {
        println!(
            "Claude Desktop config not found at {}.",
            path.display()
        );
        println!("Install Claude Desktop from https://claude.ai/download then re-run.");
        return Ok(());
    }

    let mut config = read_json_or_empty(&path)?;

    if remove {
        let changed = remove_cascade(&mut config, "mcpServers");
        if changed {
            write_json_atomic(&path, &config, dry_run)?;
            if !dry_run {
                println!("cascade MCP server removed from Claude Desktop.");
            }
        } else {
            println!("cascade entry not found in Claude Desktop config.");
        }
        return Ok(());
    }

    // Mint a token for HTTP transport
    let token = McpTokenArgs::mint_token().unwrap_or_else(|_| "<start daemon first>".into());

    let cascade_entry = json!({
        "url": "http://127.0.0.1:7722/mcp",
        "headers": {
            "Authorization": format!("Bearer {token}")
        }
    });
    upsert_key(&mut config, "mcpServers", "cascade", cascade_entry);
    write_json_atomic(&path, &config, dry_run)?;
    if !dry_run {
        println!("cascade MCP server added to Claude Desktop.");
        println!(
            "NOTE: Token embedded in config is valid for 5 minutes.\n\
             Re-run `cascade mcp setup --tool claude-desktop` if Claude Desktop loses connection.\n\
             Restart Claude Desktop to activate."
        );
    }
    Ok(())
}

// ── VS Code ───────────────────────────────────────────────────────────────────

/// Return the VS Code mcp.json path: project-local or global.
pub fn vscode_mcp_path(global: bool) -> PathBuf {
    if global {
        home_dir().join(".vscode").join("mcp.json")
    } else {
        std::env::current_dir()
            .unwrap_or_else(|_| PathBuf::from("."))
            .join(".vscode")
            .join("mcp.json")
    }
}

/// Merge or remove the cascade entry in VS Code mcp.json.
pub fn setup_vscode(remove: bool, global: bool, dry_run: bool) -> Result<()> {
    let path = vscode_mcp_path(global);
    let mut config = read_json_or_empty(&path)?;

    if remove {
        let changed = remove_cascade(&mut config, "servers");
        if changed {
            write_json_atomic(&path, &config, dry_run)?;
            if !dry_run {
                println!("cascade MCP server removed from VS Code ({}).", path.display());
            }
        } else {
            println!("cascade entry not found in VS Code config.");
        }
        return Ok(());
    }

    let cascade_entry = json!({
        "type": "stdio",
        "command": "cascade",
        "args": ["mcp", "--stdio"]
    });
    upsert_key(&mut config, "servers", "cascade", cascade_entry);
    write_json_atomic(&path, &config, dry_run)?;
    if !dry_run {
        println!(
            "cascade MCP server added to VS Code ({}).",
            path.display()
        );
        // Advisory: check .gitignore
        let gitignore = std::env::current_dir()
            .unwrap_or_else(|_| PathBuf::from("."))
            .join(".gitignore");
        if !global {
            let ignore_ok = std::fs::read_to_string(&gitignore)
                .map(|s| s.contains(".vscode/mcp.json"))
                .unwrap_or(false);
            if !ignore_ok {
                println!(
                    "TIP: Add `.vscode/mcp.json` to .gitignore to avoid committing personal tokens."
                );
            }
        }
        println!("Restart VS Code to activate.");
    }
    Ok(())
}

// ── OpenCode ──────────────────────────────────────────────────────────────────

/// Return the opencode.json path: global or project-local.
pub fn opencode_config_path(local: bool) -> PathBuf {
    if local {
        std::env::current_dir()
            .unwrap_or_else(|_| PathBuf::from("."))
            .join("opencode.json")
    } else {
        home_dir().join(".config").join("opencode").join("opencode.json")
    }
}

/// Check if the opencode binary is on PATH.
pub fn opencode_installed() -> bool {
    std::env::var_os("PATH")
        .map(|path_env| {
            std::env::split_paths(&path_env).any(|dir| {
                let candidate = dir.join("opencode");
                candidate.exists()
            })
        })
        .unwrap_or(false)
}

/// Merge or remove the cascade entry in opencode.json.
pub fn setup_opencode(remove: bool, local: bool, http: bool, dry_run: bool) -> Result<()> {
    let path = opencode_config_path(local);

    // If global config doesn't exist and opencode isn't installed, exit gracefully
    if !local && !path.exists() && !opencode_installed() {
        println!("OpenCode not detected (opencode not in PATH and config not found).");
        println!("Install OpenCode from https://opencode.ai then re-run.");
        return Ok(());
    }

    let mut config = read_json_or_empty(&path)?;

    if remove {
        let changed = remove_cascade(&mut config, "mcpServers");
        if changed {
            write_json_atomic(&path, &config, dry_run)?;
            if !dry_run {
                println!("cascade MCP server removed from OpenCode ({}).", path.display());
            }
        } else {
            println!("cascade entry not found in OpenCode config.");
        }
        return Ok(());
    }

    let cascade_entry = if http {
        let token = McpTokenArgs::mint_token().unwrap_or_else(|_| "<start daemon first>".into());
        json!({
            "type": "http",
            "url": "http://127.0.0.1:7722/mcp",
            "headers": { "Authorization": format!("Bearer {token}") }
        })
    } else {
        json!({
            "type": "stdio",
            "command": "cascade",
            "args": ["mcp", "--stdio"]
        })
    };

    upsert_key(&mut config, "mcpServers", "cascade", cascade_entry);
    write_json_atomic(&path, &config, dry_run)?;
    if !dry_run {
        println!(
            "cascade MCP server added to OpenCode ({}).",
            path.display()
        );
        println!("Restart OpenCode to activate.");
    }
    Ok(())
}

// ── --list detection ──────────────────────────────────────────────────────────

/// Detection result for a single client.
#[derive(Debug)]
pub struct DetectionResult {
    pub detected: bool,
    pub reason: &'static str,
}

/// Detect all supported clients.
pub fn detect_clients() -> Vec<(&'static str, DetectionResult)> {
    let mut results = Vec::new();

    // Claude Code: settings.json exists
    let cc_path = claude_code_settings_path();
    results.push((
        "Claude Code",
        DetectionResult {
            detected: cc_path.exists(),
            reason: if cc_path.exists() { "~/.claude/settings.json found" } else { "~/.claude/settings.json not found" },
        },
    ));

    // Claude Desktop: platform config path exists
    let cd_detected = claude_desktop_config_path()
        .map(|p| p.exists())
        .unwrap_or(false);
    results.push((
        "Claude Desktop",
        DetectionResult {
            detected: cd_detected,
            reason: if cd_detected { "config file found" } else { "config file not found" },
        },
    ));

    // VS Code: code binary in PATH OR ~/.vscode/ exists
    let vscode_bin = std::env::var_os("PATH")
        .map(|p| {
            std::env::split_paths(&p).any(|d| d.join("code").exists())
        })
        .unwrap_or(false);
    let vscode_dir = home_dir().join(".vscode").exists();
    let vscode_detected = vscode_bin || vscode_dir;
    results.push((
        "VS Code",
        DetectionResult {
            detected: vscode_detected,
            reason: if vscode_bin {
                "`code` binary found in PATH"
            } else if vscode_dir {
                "~/.vscode/ directory found"
            } else {
                "`code` not in PATH and ~/.vscode/ not found"
            },
        },
    ));

    // OpenCode: binary in PATH OR ~/.config/opencode/ exists
    let oc_dir = home_dir().join(".config").join("opencode").exists();
    let oc_detected = opencode_installed() || oc_dir;
    results.push((
        "OpenCode",
        DetectionResult {
            detected: oc_detected,
            reason: if opencode_installed() {
                "`opencode` binary found in PATH"
            } else if oc_dir {
                "~/.config/opencode/ directory found"
            } else {
                "`opencode` not in PATH and ~/.config/opencode/ not found"
            },
        },
    ));

    results
}

/// Print detection table without configuring anything.
fn setup_list() -> Result<()> {
    let clients = detect_clients();
    println!("{:<16} {:<10} {}", "Client", "Detected", "Reason");
    println!("{}", "─".repeat(60));
    for (name, det) in &clients {
        let mark = if det.detected { "✓ yes" } else { "✗ no" };
        println!("{:<16} {:<10} {}", name, mark, det.reason);
    }
    Ok(())
}

// ── --all orchestration ───────────────────────────────────────────────────────

fn setup_all(remove: bool, dry_run: bool) -> Result<()> {
    let clients = detect_clients();
    let mut configured = 0usize;

    println!("{:<16} {}", "Client", "Status");
    println!("{}", "─".repeat(50));

    for (name, det) in &clients {
        if !det.detected {
            println!("{:<16} ✗ Not detected ({})", name, det.reason);
            continue;
        }

        let result = match *name {
            "Claude Code" => setup_claude_code(remove, dry_run),
            "Claude Desktop" => setup_claude_desktop(remove, dry_run),
            "VS Code" => setup_vscode(remove, false, dry_run),
            "OpenCode" => setup_opencode(remove, false, false, dry_run),
            _ => Ok(()),
        };

        match result {
            Ok(()) => {
                let verb = if remove { "Removed" } else { "Configured" };
                println!("{:<16} ✓ {verb}", name);
                configured += 1;
            }
            Err(e) => println!("{:<16} ✗ Error: {e}", name),
        }
    }

    if configured == 0 {
        eprintln!("No clients detected. Install a supported client and re-run.");
        return Err(CascadeError::Other("no clients detected".into()));
    }

    Ok(())
}

// ── Command dispatch ──────────────────────────────────────────────────────────

#[async_trait]
impl Command for McpArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            McpSubcmd::Token(args) => args.run().await,
            McpSubcmd::Status(args) => args.run().await,
            McpSubcmd::Setup(args) => args.run().await,
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use tempfile::TempDir;

    // Helper: set HOME to a tempdir and return the guard.
    fn temp_home() -> TempDir {
        TempDir::new().expect("tempdir")
    }

    // ── Token ─────────────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn token_missing_key_file_returns_error() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let result = McpTokenArgs::mint_token();
        assert!(result.is_err(), "missing key file should error");
        let msg = result.unwrap_err().to_string();
        assert!(msg.contains("not running") || msg.contains("not yet initialized"),
            "unexpected error message: {msg}");
    }

    #[test]
    #[serial(global_env)]
    fn token_existing_key_file_returns_valid_token() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        // Create the secret key file
        let runtime_dir = tmp.path().join(".cascade").join("runtime");
        std::fs::create_dir_all(&runtime_dir).unwrap();
        let key_path = runtime_dir.join("mcp-secret.key");
        McpSecretManager::load_or_create(&key_path).expect("create key");

        let token = McpTokenArgs::mint_token().expect("token from valid key");
        assert!(
            token.starts_with("cascade-mcp-"),
            "token must start with 'cascade-mcp-': {token}"
        );
    }

    // ── Claude Code setup ─────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn setup_claude_code_creates_settings_with_cascade_entry() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let settings_path = tmp.path().join(".claude").join("settings.json");
        std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

        setup_claude_code(false, false).expect("setup should succeed");

        let raw = std::fs::read_to_string(&settings_path).expect("settings.json written");
        let parsed: Value = serde_json::from_str(&raw).expect("valid JSON");
        assert_eq!(
            parsed["mcpServers"]["cascade"]["command"],
            json!("cascade"),
            "command must be 'cascade'"
        );
        assert_eq!(
            parsed["mcpServers"]["cascade"]["args"],
            json!(["mcp", "--stdio"]),
            "args must be ['mcp', '--stdio']"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_claude_code_idempotent() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let settings_path = tmp.path().join(".claude").join("settings.json");
        std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

        setup_claude_code(false, false).expect("first setup");
        setup_claude_code(false, false).expect("second setup");

        let raw = std::fs::read_to_string(&settings_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        let servers = parsed["mcpServers"].as_object().unwrap();
        let count = servers.keys().filter(|k| *k == "cascade").count();
        assert_eq!(count, 1, "exactly one cascade entry after two setup runs");
    }

    #[test]
    #[serial(global_env)]
    fn setup_claude_code_remove_clears_entry_preserves_others() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let settings_path = tmp.path().join(".claude").join("settings.json");
        std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

        // Pre-populate with another server + cascade
        let initial = json!({
            "mcpServers": {
                "other-tool": { "command": "other", "args": [] },
                "cascade": { "command": "cascade", "args": ["mcp", "--stdio"], "env": {} }
            }
        });
        std::fs::write(&settings_path, serde_json::to_string_pretty(&initial).unwrap()).unwrap();

        setup_claude_code(true, false).expect("remove should succeed");

        let raw = std::fs::read_to_string(&settings_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        assert!(
            parsed["mcpServers"].get("cascade").is_none()
                || parsed["mcpServers"]["cascade"].is_null(),
            "cascade entry should be removed"
        );
        assert_eq!(
            parsed["mcpServers"]["other-tool"]["command"],
            json!("other"),
            "other entries must be preserved"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_claude_code_dry_run_does_not_write() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let settings_path = tmp.path().join(".claude").join("settings.json");
        std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

        setup_claude_code(false, true).expect("dry-run should not error");
        assert!(!settings_path.exists(), "dry-run must not write file");
    }

    #[test]
    #[serial(global_env)]
    fn setup_claude_code_preserves_existing_settings() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let settings_path = tmp.path().join(".claude").join("settings.json");
        std::fs::create_dir_all(settings_path.parent().unwrap()).unwrap();

        // Write existing settings with unrelated keys
        let initial = json!({
            "theme": "dark",
            "verbose": true,
            "mcpServers": { "existing-tool": { "command": "existing" } }
        });
        std::fs::write(&settings_path, serde_json::to_string_pretty(&initial).unwrap()).unwrap();

        setup_claude_code(false, false).unwrap();

        let raw = std::fs::read_to_string(&settings_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        assert_eq!(parsed["theme"], json!("dark"), "theme must be preserved");
        assert_eq!(parsed["verbose"], json!(true), "verbose must be preserved");
        assert_eq!(
            parsed["mcpServers"]["existing-tool"]["command"],
            json!("existing"),
            "existing server must be preserved"
        );
        assert_eq!(
            parsed["mcpServers"]["cascade"]["command"],
            json!("cascade"),
            "cascade must be added"
        );
    }

    // ── Claude Desktop setup ──────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn setup_claude_desktop_path_resolves_correctly_on_macos() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        if std::env::consts::OS != "macos" {
            // Only run on macOS
            return;
        }

        let path = claude_desktop_config_path().expect("macOS should return a path");
        let s = path.to_str().unwrap();
        assert!(
            s.contains("Application Support/Claude/claude_desktop_config.json"),
            "unexpected path: {s}"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_claude_desktop_missing_config_exits_gracefully() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        // Don't create the config file — Claude Desktop "not installed"
        let result = setup_claude_desktop(false, false);
        assert!(result.is_ok(), "missing config should exit 0 gracefully");
    }

    #[test]
    #[serial(global_env)]
    fn setup_claude_desktop_creates_entry_with_url_and_auth() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        // Create a MCP key so token mint works
        let runtime_dir = tmp.path().join(".cascade").join("runtime");
        std::fs::create_dir_all(&runtime_dir).unwrap();
        McpSecretManager::load_or_create(&runtime_dir.join("mcp-secret.key")).unwrap();

        // Create the Claude Desktop config directory + empty file
        let Some(config_path) = claude_desktop_config_path() else { return };
        std::fs::create_dir_all(config_path.parent().unwrap()).unwrap();
        std::fs::write(&config_path, "{}").unwrap();

        setup_claude_desktop(false, false).expect("setup should succeed");

        let raw = std::fs::read_to_string(&config_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        let entry = &parsed["mcpServers"]["cascade"];
        assert_eq!(
            entry["url"],
            json!("http://127.0.0.1:7722/mcp"),
            "url must be correct"
        );
        assert!(
            entry["headers"]["Authorization"]
                .as_str()
                .unwrap_or("")
                .starts_with("Bearer cascade-mcp-")
                || entry["headers"]["Authorization"]
                    .as_str()
                    .unwrap_or("")
                    .starts_with("Bearer <"),
            "Authorization header must be a Bearer token"
        );
    }

    // ── VS Code setup ─────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn setup_vscode_creates_mcp_json_with_stdio_entry() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        // Use a temp cwd
        let project_dir = tmp.path().join("project");
        std::fs::create_dir_all(&project_dir).unwrap();
        std::env::set_current_dir(&project_dir).unwrap();

        setup_vscode(false, false, false).expect("setup should succeed");

        let mcp_path = project_dir.join(".vscode").join("mcp.json");
        assert!(mcp_path.exists(), ".vscode/mcp.json must be created");

        let raw = std::fs::read_to_string(&mcp_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).expect("must be valid JSON");
        assert_eq!(
            parsed["servers"]["cascade"]["type"],
            json!("stdio"),
            "type must be stdio"
        );
        assert_eq!(
            parsed["servers"]["cascade"]["command"],
            json!("cascade"),
            "command must be cascade"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_vscode_idempotent() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let project_dir = tmp.path().join("project");
        std::fs::create_dir_all(&project_dir).unwrap();
        std::env::set_current_dir(&project_dir).unwrap();

        setup_vscode(false, false, false).unwrap();
        setup_vscode(false, false, false).unwrap();

        let mcp_path = project_dir.join(".vscode").join("mcp.json");
        let raw = std::fs::read_to_string(&mcp_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        let count = parsed["servers"].as_object().unwrap()
            .keys().filter(|k| *k == "cascade").count();
        assert_eq!(count, 1, "exactly one cascade entry after two runs");
    }

    #[test]
    #[serial(global_env)]
    fn setup_vscode_remove_preserves_other_servers() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let project_dir = tmp.path().join("project");
        std::fs::create_dir_all(project_dir.join(".vscode")).unwrap();
        std::env::set_current_dir(&project_dir).unwrap();

        let mcp_path = project_dir.join(".vscode").join("mcp.json");
        let initial = json!({
            "servers": {
                "other": { "type": "stdio", "command": "other" },
                "cascade": { "type": "stdio", "command": "cascade", "args": ["mcp", "--stdio"] }
            }
        });
        std::fs::write(&mcp_path, serde_json::to_string_pretty(&initial).unwrap()).unwrap();

        setup_vscode(true, false, false).unwrap();

        let raw = std::fs::read_to_string(&mcp_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        assert!(parsed["servers"].get("cascade").is_none()
            || parsed["servers"]["cascade"].is_null(),
            "cascade must be removed");
        assert_eq!(
            parsed["servers"]["other"]["command"],
            json!("other"),
            "other server must survive"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_vscode_dry_run_does_not_write() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let project_dir = tmp.path().join("project");
        std::fs::create_dir_all(&project_dir).unwrap();
        std::env::set_current_dir(&project_dir).unwrap();

        setup_vscode(false, false, true).expect("dry-run should not error");
        let mcp_path = project_dir.join(".vscode").join("mcp.json");
        assert!(!mcp_path.exists(), "dry-run must not write file");
    }

    // ── OpenCode setup ────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn setup_opencode_global_not_installed_exits_gracefully() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());
        // Isolate PATH to avoid finding a real opencode binary
        std::env::set_var("PATH", tmp.path().to_str().unwrap());

        let result = setup_opencode(false, false, false, false);
        assert!(result.is_ok(), "missing opencode should exit 0 gracefully");
    }

    #[test]
    #[serial(global_env)]
    fn setup_opencode_local_creates_opencode_json() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let project_dir = tmp.path().join("project");
        std::fs::create_dir_all(&project_dir).unwrap();
        std::env::set_current_dir(&project_dir).unwrap();

        setup_opencode(false, true, false, false).expect("local setup should succeed");

        let oc_path = project_dir.join("opencode.json");
        assert!(oc_path.exists(), "opencode.json must be created");

        let raw = std::fs::read_to_string(&oc_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).expect("valid JSON");
        assert_eq!(
            parsed["mcpServers"]["cascade"]["type"],
            json!("stdio"),
            "type must be stdio"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_opencode_http_flag_writes_http_entry() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        // Create a MCP key for token mint
        let runtime_dir = tmp.path().join(".cascade").join("runtime");
        std::fs::create_dir_all(&runtime_dir).unwrap();
        McpSecretManager::load_or_create(&runtime_dir.join("mcp-secret.key")).unwrap();

        let project_dir = tmp.path().join("project");
        std::fs::create_dir_all(&project_dir).unwrap();
        std::env::set_current_dir(&project_dir).unwrap();

        setup_opencode(false, true, true, false).expect("http setup should succeed");

        let oc_path = project_dir.join("opencode.json");
        let raw = std::fs::read_to_string(&oc_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        let entry = &parsed["mcpServers"]["cascade"];
        assert_eq!(entry["type"], json!("http"), "type must be http");
        assert_eq!(
            entry["url"],
            json!("http://127.0.0.1:7722/mcp"),
            "url must be correct"
        );
    }

    #[test]
    #[serial(global_env)]
    fn setup_opencode_idempotent() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        let project_dir = tmp.path().join("project");
        std::fs::create_dir_all(&project_dir).unwrap();
        std::env::set_current_dir(&project_dir).unwrap();

        setup_opencode(false, true, false, false).unwrap();
        setup_opencode(false, true, false, false).unwrap();

        let oc_path = project_dir.join("opencode.json");
        let raw = std::fs::read_to_string(&oc_path).unwrap();
        let parsed: Value = serde_json::from_str(&raw).unwrap();
        let count = parsed["mcpServers"].as_object().unwrap()
            .keys().filter(|k| *k == "cascade").count();
        assert_eq!(count, 1, "exactly one cascade entry");
    }

    // ── Setup --all ───────────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn setup_all_detects_at_least_claude_code_when_settings_present() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());

        // Create ~/.claude/settings.json so Claude Code is detected
        let claude_dir = tmp.path().join(".claude");
        std::fs::create_dir_all(&claude_dir).unwrap();
        std::fs::write(claude_dir.join("settings.json"), "{}").unwrap();

        let result = setup_all(false, false);
        assert!(result.is_ok(), "setup_all should succeed when at least one client detected");
    }

    #[test]
    #[serial(global_env)]
    fn setup_all_no_clients_returns_error() {
        let tmp = temp_home();
        std::env::set_var("HOME", tmp.path());
        std::env::set_var("PATH", tmp.path().to_str().unwrap());

        // No clients present
        let result = setup_all(false, false);
        assert!(result.is_err(), "should error when no clients detected");
    }
}
