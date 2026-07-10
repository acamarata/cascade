//! `cascade sentry` — manage nSentry bug/CI/error-report sync.
//!
//! # Purpose
//!
//! nSentry pulls `*.md` report files from a remote ops server into the local
//! project inbox exactly once per developer, using an SSH + rsync pipeline
//! and a per-dev consumed.list manifest for dedup.
//!
//! # Subcommands
//!
//! - `enable`  — write `.cascade/nsentry.toml`; install launchd agent on macOS.
//! - `sync`    — run one sync immediately; `--dry-run` prints without copying.
//! - `status`  — list enabled projects, last-sync stats, launchd loaded state.
//! - `disable` — remove launchd agent and delete `.cascade/nsentry.toml`.
//! - `update`  — regenerate launchd plist from current config (idempotent).
//!
//! # Inputs
//!
//! - `--server user@host` (required for `enable`).
//! - `--remote-dir`, `--inbox`, `--interval` (optional overrides).
//! - `--project <dir>` (optional; defaults to cwd).
//!
//! # Constraints
//!
//! - No hardcoded server addresses; all supplied by the user.
//! - launchd agent: macOS only. Linux/other prints a notice and exits 0.
//!
//! SPORT: `.github/wiki/nsentry.md` — nSentry CLI surface

use async_trait::async_trait;
use clap::{Args, Subcommand};

use cascade_core::nsentry::{self, dev_id, load_config, manifest_size, save_config, NsentryConfig};
use cascade_types::error::{CascadeError, Result};

use super::Command;

// ── Top-level args ────────────────────────────────────────────────────────────

/// Arguments for `cascade sentry`.
#[derive(Debug, Args)]
pub struct SentryArgs {
    #[command(subcommand)]
    pub subcommand: SentrySubcmd,
}

#[derive(Debug, Subcommand)]
pub enum SentrySubcmd {
    /// Enable nSentry for a project and install the sync agent.
    Enable(SentryEnableArgs),
    /// Run a single nSentry sync immediately.
    Sync(SentrySyncArgs),
    /// Show nSentry status for known projects.
    Status(SentryStatusArgs),
    /// Remove the sync agent and nSentry config for a project.
    Disable(SentryDisableArgs),
    /// Regenerate the launchd agent from the current config (idempotent).
    Update(SentryUpdateArgs),
}

#[async_trait]
impl Command for SentryArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            SentrySubcmd::Enable(a) => a.run().await,
            SentrySubcmd::Sync(a) => a.run().await,
            SentrySubcmd::Status(a) => a.run().await,
            SentrySubcmd::Disable(a) => a.run().await,
            SentrySubcmd::Update(a) => a.run().await,
        }
    }
}

// ── enable ────────────────────────────────────────────────────────────────────

/// Arguments for `cascade sentry enable`.
#[derive(Debug, Args)]
pub struct SentryEnableArgs {
    /// SSH target in `user@host` form (required).
    #[arg(long)]
    pub server: String,
    /// Remote directory to sync from (default `/opt/nself-ops/errors`).
    #[arg(long)]
    pub remote_dir: Option<String>,
    /// Local inbox directory (default `<project>/.claude/inbox`).
    #[arg(long)]
    pub inbox: Option<std::path::PathBuf>,
    /// Sync interval in seconds for the launchd agent (default 120).
    #[arg(long)]
    pub interval: Option<u64>,
    /// Project root to configure (defaults to cwd).
    #[arg(long)]
    pub project: Option<std::path::PathBuf>,
}

#[async_trait]
impl Command for SentryEnableArgs {
    async fn run(&self) -> Result<()> {
        let project_root = resolve_project(self.project.as_deref())?;
        let cfg = NsentryConfig {
            sentry_server: self.server.clone(),
            remote_dir: self
                .remote_dir
                .clone()
                .unwrap_or_else(|| "/opt/nself-ops/errors".to_string()),
            inbox: self.inbox.clone(),
            interval_secs: self.interval.unwrap_or(120),
        };
        save_config(&project_root, &cfg)?;
        println!("nSentry enabled for {}", project_root.display());
        println!("  server:     {}", cfg.sentry_server);
        println!("  remote_dir: {}", cfg.remote_dir);
        println!("  interval:   {}s", cfg.interval_secs);
        println!(
            "  inbox:      {}",
            cfg.resolved_inbox(&project_root).display()
        );
        install_launchd_agent(&project_root, &cfg)?;
        Ok(())
    }
}

// ── sync ──────────────────────────────────────────────────────────────────────

/// Arguments for `cascade sentry sync`.
#[derive(Debug, Args)]
pub struct SentrySyncArgs {
    /// Project root to sync (defaults to cwd).
    #[arg(long)]
    pub project: Option<std::path::PathBuf>,
    /// Print what would be synced without copying any files.
    #[arg(long)]
    pub dry_run: bool,
}

#[async_trait]
impl Command for SentrySyncArgs {
    async fn run(&self) -> Result<()> {
        let project_root = resolve_project(self.project.as_deref())?;
        let cfg = load_config(&project_root)?.ok_or_else(|| {
            CascadeError::Other(format!(
                "nSentry not configured for {}. Run `cascade sentry enable --server user@host` first.",
                project_root.display()
            ))
        })?;

        if self.dry_run {
            println!(
                "[dry-run] would sync from {}:{}",
                cfg.sentry_server, cfg.remote_dir
            );
        }

        let report = nsentry::sync(&project_root, &cfg, self.dry_run, None)?;
        if self.dry_run {
            println!(
                "[dry-run] would copy {} new file(s) into {}",
                report.new,
                report.inbox.display()
            );
        } else {
            println!(
                "nSentry sync complete: {} new, {} cached — dev_id={} inbox={}",
                report.new,
                report.cache_total,
                report.dev_id,
                report.inbox.display()
            );
        }
        Ok(())
    }
}

// ── status ────────────────────────────────────────────────────────────────────

/// Arguments for `cascade sentry status`.
#[derive(Debug, Args)]
pub struct SentryStatusArgs {
    /// Additional project roots to check (current directory always included).
    #[arg(long = "project", num_args = 0..)]
    pub projects: Vec<std::path::PathBuf>,
}

#[async_trait]
impl Command for SentryStatusArgs {
    async fn run(&self) -> Result<()> {
        let mut roots = vec![resolve_project(None)?];
        roots.extend(self.projects.clone());
        roots.dedup();

        let id = dev_id();
        let enabled: Vec<_> = roots.iter().filter(|r| nsentry::is_enabled(r)).collect();

        if enabled.is_empty() {
            println!("No nSentry-enabled projects found in checked directories.");
            println!("Run `cascade sentry enable --server user@host` to configure one.");
            return Ok(());
        }

        println!("dev_id:  {id}");
        println!();

        for root in &enabled {
            if let Ok(Some(cfg)) = load_config(root) {
                let slug = project_slug(root);
                let loaded = launchd_loaded(&slug);
                println!("  project:    {}", root.display());
                println!("  server:     {}", cfg.sentry_server);
                println!("  remote_dir: {}", cfg.remote_dir);
                println!("  inbox:      {}", cfg.resolved_inbox(root).display());
                println!("  interval:   {}s", cfg.interval_secs);
                println!(
                    "  consumed:   {} file(s) in manifest",
                    manifest_size(&id, &slug)
                );
                println!(
                    "  launchd:    {}",
                    if loaded { "loaded" } else { "not loaded" }
                );
                println!();
            }
        }
        Ok(())
    }
}

// ── disable ───────────────────────────────────────────────────────────────────

/// Arguments for `cascade sentry disable`.
#[derive(Debug, Args)]
pub struct SentryDisableArgs {
    /// Project root to disable (defaults to cwd).
    #[arg(long)]
    pub project: Option<std::path::PathBuf>,
}

#[async_trait]
impl Command for SentryDisableArgs {
    async fn run(&self) -> Result<()> {
        let project_root = resolve_project(self.project.as_deref())?;
        remove_launchd_agent(&project_root)?;
        let config = nsentry::config_path(&project_root);
        if config.is_file() {
            std::fs::remove_file(&config).map_err(|e| CascadeError::Io {
                path: config.clone(),
                operation: "remove nsentry.toml",
                source: e,
            })?;
        }
        println!("nSentry disabled for {}", project_root.display());
        Ok(())
    }
}

// ── update ────────────────────────────────────────────────────────────────────

/// Arguments for `cascade sentry update`.
#[derive(Debug, Args)]
pub struct SentryUpdateArgs {
    /// Project root to update (defaults to cwd).
    #[arg(long)]
    pub project: Option<std::path::PathBuf>,
}

#[async_trait]
impl Command for SentryUpdateArgs {
    async fn run(&self) -> Result<()> {
        let project_root = resolve_project(self.project.as_deref())?;
        let cfg = load_config(&project_root)?.ok_or_else(|| {
            CascadeError::Other(format!(
                "nSentry not configured for {}. Run `cascade sentry enable` first.",
                project_root.display()
            ))
        })?;
        install_launchd_agent(&project_root, &cfg)?;
        println!("nSentry agent updated for {}", project_root.display());
        Ok(())
    }
}

// ── Shared helpers ────────────────────────────────────────────────────────────

/// Resolve the project root: use the provided path or fall back to cwd.
fn resolve_project(path: Option<&std::path::Path>) -> Result<std::path::PathBuf> {
    match path {
        Some(p) => Ok(p.to_path_buf()),
        None => std::env::current_dir()
            .map_err(|e| CascadeError::Other(format!("cannot determine current directory: {e}"))),
    }
}

/// Launch-agent label slug for a project — delegates to the engine so the
/// launchd label and the per-project state directory always use the same slug.
fn project_slug(project_root: &std::path::Path) -> String {
    nsentry::project_slug(project_root)
}

/// launchd label for a given project slug.
fn launchd_label(slug: &str) -> String {
    format!("dev.cascade.nsentry-{slug}")
}

/// Check whether a launchd service is currently loaded.
#[cfg(target_os = "macos")]
fn launchd_loaded(slug: &str) -> bool {
    let label = launchd_label(slug);
    std::process::Command::new("launchctl")
        .args(["list", &label])
        .output()
        .map(|o| o.status.success())
        .unwrap_or(false)
}

#[cfg(not(target_os = "macos"))]
fn launchd_loaded(_slug: &str) -> bool {
    false
}

/// Resolve the `cascade` binary path (used in the plist ProgramArguments).
fn resolve_cascade_binary() -> std::path::PathBuf {
    // Check standard install locations.
    let home = cascade_types::paths::home_dir();
    for candidate in [
        home.join(".local").join("bin").join("cascade"),
        home.join(".cargo").join("bin").join("cascade"),
    ] {
        if candidate.is_file() {
            return candidate;
        }
    }
    // Fall back to PATH lookup.
    if let Ok(path_var) = std::env::var("PATH") {
        for dir in std::env::split_paths(&path_var) {
            let candidate = dir.join("cascade");
            if candidate.is_file() {
                return candidate;
            }
        }
    }
    // Last resort: assume it's on PATH.
    std::path::PathBuf::from("cascade")
}

/// Write and load a launchd plist for the nSentry sync agent (macOS).
#[cfg(target_os = "macos")]
fn install_launchd_agent(project_root: &std::path::Path, cfg: &NsentryConfig) -> Result<()> {
    let slug = project_slug(project_root);
    let label = launchd_label(&slug);
    let binary = resolve_cascade_binary();
    let home = cascade_types::paths::home_dir();
    let agents_dir = home.join("Library").join("LaunchAgents");
    std::fs::create_dir_all(&agents_dir).map_err(|e| CascadeError::Io {
        path: agents_dir.clone(),
        operation: "create LaunchAgents dir",
        source: e,
    })?;

    let plist_path = agents_dir.join(format!("{label}.plist"));
    let log_out = home
        .join("Library")
        .join("Logs")
        .join(format!("cascade-nsentry-{slug}.log"));
    let log_err = home
        .join("Library")
        .join("Logs")
        .join(format!("cascade-nsentry-{slug}-err.log"));

    // PATH includes Homebrew so rsync and ssh are reachable from launchd.
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
        <string>sentry</string>
        <string>sync</string>
        <string>--project</string>
        <string>{project}</string>
    </array>
    <key>StartInterval</key>
    <integer>{interval}</integer>
    <key>RunAtLoad</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{log_out}</string>
    <key>StandardErrorPath</key>
    <string>{log_err}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    </dict>
</dict>
</plist>
"#,
        label = label,
        binary = binary.display(),
        project = project_root.display(),
        interval = cfg.interval_secs,
        log_out = log_out.display(),
        log_err = log_err.display(),
    );

    // Atomic write via .tmp sibling.
    let tmp = plist_path.with_extension("plist.tmp");
    std::fs::write(&tmp, &plist).map_err(|e| CascadeError::Io {
        path: tmp.clone(),
        operation: "write plist (tmp)",
        source: e,
    })?;
    std::fs::rename(&tmp, &plist_path).map_err(|e| CascadeError::Io {
        path: plist_path.clone(),
        operation: "rename plist into place",
        source: e,
    })?;

    // Idempotent: unload first (ignore error — may not be loaded yet).
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
        println!("launchd agent installed: {label}");
        println!("  plist: {}", plist_path.display());
    } else {
        println!(
            "plist written but launchctl load returned non-zero: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        );
    }
    Ok(())
}

/// Non-macOS: print a notice, do not install a launchd agent.
#[cfg(not(target_os = "macos"))]
fn install_launchd_agent(_project_root: &std::path::Path, _cfg: &NsentryConfig) -> Result<()> {
    println!("launchd agent: macOS only.");
    println!(
        "On Linux, add `cascade sentry sync --project <dir>` to a systemd --user timer or cron."
    );
    Ok(())
}

/// Remove the launchd plist and unload it (macOS).
#[cfg(target_os = "macos")]
fn remove_launchd_agent(project_root: &std::path::Path) -> Result<()> {
    let slug = project_slug(project_root);
    let label = launchd_label(&slug);
    let home = cascade_types::paths::home_dir();
    let plist_path = home
        .join("Library")
        .join("LaunchAgents")
        .join(format!("{label}.plist"));
    if plist_path.exists() {
        let _ = std::process::Command::new("launchctl")
            .args(["unload", "-w"])
            .arg(&plist_path)
            .output();
        std::fs::remove_file(&plist_path).map_err(|e| CascadeError::Io {
            path: plist_path.clone(),
            operation: "remove nsentry plist",
            source: e,
        })?;
        println!("launchd agent removed: {label}");
    } else {
        println!("launchd agent not found (already removed): {label}");
    }
    Ok(())
}

#[cfg(not(target_os = "macos"))]
fn remove_launchd_agent(_project_root: &std::path::Path) -> Result<()> {
    Ok(())
}
