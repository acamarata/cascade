//! `cascade widget` — manage the cascade-fleet-widget LaunchAgent.
//!
//! Subcommands: install | uninstall | status
//!
//! Purpose: mirrors `cascade daemon install/uninstall/service-status` but targets
//!   the `cascade-fleet-widget` binary with label `dev.cascade.fleet-widget`.
//! Inputs:  none — binary path resolved from PATH or ~/.local/bin.
//! Constraints: macOS only (LaunchAgent); non-macOS subcommands print a notice
//!   and exit successfully so the command is always parseable.
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-fleet-widget install

use async_trait::async_trait;
use clap::{Args, Subcommand};

use cascade_types::error::{CascadeError, Result};

use super::Command;

/// Arguments for `cascade widget`.
#[derive(Debug, Args)]
pub struct WidgetArgs {
    #[command(subcommand)]
    pub subcommand: WidgetSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum WidgetSubcmd {
    /// Install cascade-fleet-widget as a macOS LaunchAgent (runs at login).
    Install(WidgetInstallArgs),
    /// Remove the fleet-widget LaunchAgent.
    Uninstall(WidgetUninstallArgs),
    /// Show whether the fleet-widget LaunchAgent is registered and running.
    Status(WidgetStatusArgs),
}

#[derive(Debug, Args)]
pub struct WidgetInstallArgs;

#[derive(Debug, Args)]
pub struct WidgetUninstallArgs;

#[derive(Debug, Args)]
pub struct WidgetStatusArgs;

#[async_trait]
impl Command for WidgetArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            WidgetSubcmd::Install(a) => a.run().await,
            WidgetSubcmd::Uninstall(a) => a.run().await,
            WidgetSubcmd::Status(a) => a.run().await,
        }
    }
}

#[async_trait]
impl Command for WidgetInstallArgs {
    async fn run(&self) -> Result<()> {
        widget_install()
    }
}

#[async_trait]
impl Command for WidgetUninstallArgs {
    async fn run(&self) -> Result<()> {
        widget_uninstall()
    }
}

#[async_trait]
impl Command for WidgetStatusArgs {
    async fn run(&self) -> Result<()> {
        widget_status()
    }
}

// ── Platform implementation ───────────────────────────────────────────────────

#[cfg(target_os = "macos")]
const WIDGET_LABEL: &str = "dev.cascade.fleet-widget";

/// Resolve the `cascade-fleet-widget` binary.
///
/// Search order: `~/.local/bin/cascade-fleet-widget`,
///               `~/.cargo/bin/cascade-fleet-widget`, PATH.
fn resolve_widget_binary() -> Result<std::path::PathBuf> {
    let home = cascade_types::paths::home_dir();
    let name = "cascade-fleet-widget";

    let local = home.join(".local").join("bin").join(name);
    if local.is_file() {
        return Ok(local);
    }

    let cargo_bin = std::env::var("CARGO_HOME")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(|_| home.join(".cargo"))
        .join("bin")
        .join(name);
    if cargo_bin.is_file() {
        return Ok(cargo_bin);
    }

    if let Ok(path_var) = std::env::var("PATH") {
        for dir in std::env::split_paths(&path_var) {
            let candidate = dir.join(name);
            if candidate.is_file() {
                return Ok(candidate);
            }
        }
    }

    Err(CascadeError::Other(format!(
        "cascade-fleet-widget binary not found; run \
         `cargo install --path crates/cascade-fleet-widget` or \
         place the binary at {} first",
        local.display()
    )))
}

#[cfg(target_os = "macos")]
fn widget_install() -> Result<()> {
    let binary = resolve_widget_binary()?;
    let home = cascade_types::paths::home_dir();
    let agents_dir = home.join("Library").join("LaunchAgents");
    std::fs::create_dir_all(&agents_dir).map_err(|e| CascadeError::Io {
        path: agents_dir.clone(),
        operation: "create LaunchAgents dir",
        source: e,
    })?;

    let plist_path = agents_dir.join(format!("{WIDGET_LABEL}.plist"));
    let log_out = home
        .join("Library")
        .join("Logs")
        .join("cascade-fleet-widget.log");
    let log_err = home
        .join("Library")
        .join("Logs")
        .join("cascade-fleet-widget-err.log");

    let plist = format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{label}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{binary}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{log_out}</string>
    <key>StandardErrorPath</key>
    <string>{log_err}</string>
</dict>
</plist>
"#,
        label = WIDGET_LABEL,
        binary = binary.display(),
        log_out = log_out.display(),
        log_err = log_err.display(),
    );

    atomic_write_str(&plist_path, &plist)?;

    // Idempotent: unload first (ignore error — not loaded is fine)
    let _ = std::process::Command::new("launchctl")
        .args(["unload", "-w"])
        .arg(&plist_path)
        .output();

    let out = std::process::Command::new("launchctl")
        .args(["load", "-w"])
        .arg(&plist_path)
        .output()
        .map_err(|e| CascadeError::Other(format!("launchctl load failed: {e}")))?;

    if out.status.success() {
        println!("cascade-fleet-widget installed as LaunchAgent ({WIDGET_LABEL})");
        println!("service file: {}", plist_path.display());
    } else {
        println!(
            "plist written but launchctl load returned non-zero: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        );
    }
    Ok(())
}

#[cfg(not(target_os = "macos"))]
fn widget_install() -> Result<()> {
    println!("cascade widget install: LaunchAgent is macOS-only.");
    println!("On Linux, add cascade-fleet-widget to your autostart or systemd --user manually.");
    Ok(())
}

#[cfg(target_os = "macos")]
fn widget_uninstall() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let plist_path = home
        .join("Library")
        .join("LaunchAgents")
        .join(format!("{WIDGET_LABEL}.plist"));

    if plist_path.exists() {
        let _ = std::process::Command::new("launchctl")
            .args(["unload", "-w"])
            .arg(&plist_path)
            .output();
        std::fs::remove_file(&plist_path).map_err(|e| CascadeError::Io {
            path: plist_path,
            operation: "remove widget plist",
            source: e,
        })?;
        println!("cascade-fleet-widget LaunchAgent removed");
    } else {
        println!("cascade-fleet-widget LaunchAgent not installed");
    }
    Ok(())
}

#[cfg(not(target_os = "macos"))]
fn widget_uninstall() -> Result<()> {
    println!("cascade widget uninstall: LaunchAgent is macOS-only.");
    Ok(())
}

#[cfg(target_os = "macos")]
fn widget_status() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let plist_path = home
        .join("Library")
        .join("LaunchAgents")
        .join(format!("{WIDGET_LABEL}.plist"));

    let installed = plist_path.exists();
    println!(
        "service file: {}",
        if installed { "present" } else { "not found" }
    );
    if installed {
        println!("plist: {}", plist_path.display());
    }

    let loaded = std::process::Command::new("launchctl")
        .args(["list", WIDGET_LABEL])
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false);
    println!("loaded: {}", if loaded { "yes" } else { "no" });

    if !installed && !loaded {
        std::process::exit(1);
    }
    Ok(())
}

#[cfg(not(target_os = "macos"))]
fn widget_status() -> Result<()> {
    println!("cascade widget status: LaunchAgent is macOS-only.");
    Ok(())
}

// ── Shared helper ─────────────────────────────────────────────────────────────

/// Write `content` to `path` atomically via a `.tmp` sibling.
fn atomic_write_str(path: &std::path::Path, content: &str) -> Result<()> {
    let mut tmp_name = path.as_os_str().to_owned();
    tmp_name.push(".tmp");
    let tmp = std::path::PathBuf::from(tmp_name);

    std::fs::write(&tmp, content).map_err(|e| CascadeError::Io {
        path: tmp.clone(),
        operation: "atomic write (tmp)",
        source: e,
    })?;
    std::fs::rename(&tmp, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "atomic rename",
        source: e,
    })
}
