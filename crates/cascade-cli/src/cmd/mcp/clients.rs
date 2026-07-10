//! Per-client MCP setup functions and client detection.
//!
//! Covers: Claude Code, Claude Desktop, VS Code, OpenCode.

use std::path::PathBuf;

use cascade_types::{
    error::{CascadeError, Result},
    paths::home_dir,
};
use serde_json::json;

use super::helpers::{read_json_or_empty, remove_cascade, upsert_key, write_json_atomic};
use super::token::McpTokenArgs;

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
        "windows" => std::env::var("APPDATA").ok().map(|appdata| {
            PathBuf::from(appdata)
                .join("Claude")
                .join("claude_desktop_config.json")
        }),
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
        println!("Claude Desktop config not found at {}.", path.display());
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
                println!(
                    "cascade MCP server removed from VS Code ({}).",
                    path.display()
                );
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
        println!("cascade MCP server added to VS Code ({}).", path.display());
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
        home_dir()
            .join(".config")
            .join("opencode")
            .join("opencode.json")
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
                println!(
                    "cascade MCP server removed from OpenCode ({}).",
                    path.display()
                );
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
        println!("cascade MCP server added to OpenCode ({}).", path.display());
        println!("Restart OpenCode to activate.");
    }
    Ok(())
}

// ── Detection ─────────────────────────────────────────────────────────────────

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
            reason: if cc_path.exists() {
                "~/.claude/settings.json found"
            } else {
                "~/.claude/settings.json not found"
            },
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
            reason: if cd_detected {
                "config file found"
            } else {
                "config file not found"
            },
        },
    ));

    // VS Code: code binary in PATH OR ~/.vscode/ exists
    let vscode_bin = std::env::var_os("PATH")
        .map(|p| std::env::split_paths(&p).any(|d| d.join("code").exists()))
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
pub fn setup_list() -> Result<()> {
    let clients = detect_clients();
    println!("{:<16} {:<10} Reason", "Client", "Detected");
    println!("{}", "─".repeat(60));
    for (name, det) in &clients {
        let mark = if det.detected { "✓ yes" } else { "✗ no" };
        println!("{:<16} {:<10} {}", name, mark, det.reason);
    }
    Ok(())
}

/// Configure all detected clients.
pub fn setup_all(remove: bool, dry_run: bool) -> Result<()> {
    let clients = detect_clients();
    let mut configured = 0usize;

    println!("{:<16} Status", "Client");
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
