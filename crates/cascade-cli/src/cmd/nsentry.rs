//! `cascade nsentry` — manage the daemon-owned multi-stream nSentry sync.
//!
//! # Purpose
//!
//! The new `nsentry` subcommand replaces the old launchd-based `sentry`
//! workflow with a daemon-owned, config-driven subsystem. Three report streams
//! per project are configured in `~/.cascade/nsentry-sync.yaml`.
//!
//! # Subcommands
//!
//! - `status`  — per-project table: stream · enabled · last-run (relative) ·
//!               delivered (last / total) · error.
//! - `run`     — trigger immediately for all/one project, all/one stream.
//! - `pause`   — flip a project's `enabled = false` in the YAML.
//! - `resume`  — flip a project's `enabled = true`.
//! - `list`    — list configured projects + schedules.
//!
//! # Inputs
//!
//! - `~/.cascade/nsentry-sync.yaml` (config).
//! - `~/.cascade/nsentry-sync-status.json` (live status).
//!
//! # Constraints
//!
//! - `run` reuses the same stream functions as the daemon (via `cascade_daemon`
//!   lib) so CLI and daemon are always in sync.
//! - No maintainer hostnames/paths/emails in shipped code.
//!
//! SPORT: `.github/wiki/nsentry.md` — nSentry CLI surface

use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use clap::{Args, Subcommand};

use cascade_core::nsentry_config::{
    load as load_cfg, run_ci_stream, run_dependabot_stream, run_rsync_stream,
    toggle_project_enabled, NsentrySyncStatus,
};
use cascade_types::error::{CascadeError, Result};

use super::Command;

// ── Top-level args ─────────────────────────────────────────────────────────────

/// Arguments for `cascade nsentry`.
#[derive(Debug, Args)]
pub struct NsentryArgs {
    #[command(subcommand)]
    pub subcommand: NsentrySubcmd,
}

#[derive(Debug, Subcommand)]
pub enum NsentrySubcmd {
    /// Show per-project/stream status table.
    Status(NsentryStatusArgs),
    /// Trigger one or more streams immediately.
    Run(NsentryRunArgs),
    /// Pause a project (set enabled = false in the YAML).
    Pause(NsentryPauseResumeArgs),
    /// Resume a project (set enabled = true in the YAML).
    Resume(NsentryPauseResumeArgs),
    /// List configured projects and schedules.
    List(NsentryListArgs),
}

#[async_trait]
impl Command for NsentryArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            NsentrySubcmd::Status(a)  => a.run().await,
            NsentrySubcmd::Run(a)     => a.run().await,
            NsentrySubcmd::Pause(a)   => a.run_toggle(false).await,
            NsentrySubcmd::Resume(a)  => a.run_toggle(true).await,
            NsentrySubcmd::List(a)    => a.run().await,
        }
    }
}

// ── Shared path helpers ────────────────────────────────────────────────────────

fn cascade_dir() -> PathBuf {
    cascade_types::paths::home_dir().join(".cascade")
}

fn config_yaml_path() -> PathBuf {
    cascade_dir().join("nsentry-sync.yaml")
}

fn status_json_path() -> PathBuf {
    cascade_dir().join("nsentry-sync-status.json")
}

fn scripts_dir() -> PathBuf {
    cascade_dir().join("nsentry").join("scripts")
}

// ── status ─────────────────────────────────────────────────────────────────────

/// Arguments for `cascade nsentry status`.
#[derive(Debug, Args)]
pub struct NsentryStatusArgs {}

#[async_trait]
impl Command for NsentryStatusArgs {
    async fn run(&self) -> Result<()> {
        let cfg = load_cfg(&config_yaml_path())?;
        let status = NsentrySyncStatus::load(&status_json_path());

        if cfg.projects.is_empty() {
            println!("No projects configured. Add projects to ~/.cascade/nsentry-sync.yaml.");
            return Ok(());
        }

        // Header
        println!(
            "{:<16} {:<12} {:<8} {:<12} {:<12} {}",
            "PROJECT", "STREAM", "ENABLED", "LAST RUN", "DELIVERED", "ERROR"
        );
        println!("{}", "-".repeat(80));

        for proj in &cfg.projects {
            for stream_name in ["rsync", "ci", "dependabot"] {
                let stream_enabled = match stream_name {
                    "rsync"      => proj.streams.rsync.enabled,
                    "ci"         => proj.streams.ci.enabled,
                    "dependabot" => proj.streams.dependabot.enabled,
                    _ => false,
                } && proj.enabled;

                let ss = status
                    .streams
                    .get(&proj.name)
                    .and_then(|m| m.get(stream_name));

                let last_run = ss
                    .and_then(|s| s.last_run_at)
                    .map(relative_time)
                    .unwrap_or_else(|| "never".to_string());

                let delivered = ss
                    .map(|s| format!("{}/{}", s.last_delivered, s.total_delivered))
                    .unwrap_or_else(|| "0/0".to_string());

                let error = ss
                    .and_then(|s| s.last_error.as_deref())
                    .unwrap_or("");

                // Flag stalled streams: last run > 2× interval with no success.
                let stalled = if let Some(s) = ss {
                    let interval_secs = match stream_name {
                        "rsync"      => proj.streams.rsync.interval_secs,
                        "ci"         => proj.streams.ci.interval_secs,
                        "dependabot" => proj.streams.dependabot.interval_secs,
                        _ => 3600,
                    };
                    let now = unix_now();
                    let stale_threshold = interval_secs * 2;
                    let since_last = s.last_run_at.map(|t| now.saturating_sub(t)).unwrap_or(u64::MAX);
                    (s.last_error.is_some() || since_last > stale_threshold) && stream_enabled
                } else {
                    false
                };

                let stall_marker = if stalled { " [STALLED]" } else { "" };

                println!(
                    "{:<16} {:<12} {:<8} {:<12} {:<12} {}{}",
                    proj.name,
                    stream_name,
                    if stream_enabled { "yes" } else { "no" },
                    last_run,
                    delivered,
                    error,
                    stall_marker,
                );
            }
        }
        Ok(())
    }
}

/// Format a Unix timestamp as a human-readable relative time string.
fn relative_time(ts: u64) -> String {
    let now = unix_now();
    let secs = now.saturating_sub(ts);
    if secs < 60 {
        format!("{secs}s ago")
    } else if secs < 3600 {
        format!("{}m ago", secs / 60)
    } else if secs < 86400 {
        format!("{}h ago", secs / 3600)
    } else {
        format!("{}d ago", secs / 86400)
    }
}

fn unix_now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

// ── run ────────────────────────────────────────────────────────────────────────

/// Arguments for `cascade nsentry run`.
#[derive(Debug, Args)]
pub struct NsentryRunArgs {
    /// Limit to a specific project by name.
    #[arg(long)]
    pub project: Option<String>,
    /// Limit to a specific stream (rsync, ci, or dependabot).
    #[arg(long)]
    pub stream: Option<String>,
}

#[async_trait]
impl Command for NsentryRunArgs {
    async fn run(&self) -> Result<()> {
        let cfg = load_cfg(&config_yaml_path())?;

        if cfg.projects.is_empty() {
            println!("No projects configured in ~/.cascade/nsentry-sync.yaml.");
            return Ok(());
        }

        let mut status = NsentrySyncStatus::load(&status_json_path());
        let sdir = scripts_dir();
        let mut total_delivered = 0usize;

        for proj in cfg.projects.iter().filter(|p| p.enabled) {
            // Project filter.
            if let Some(ref name) = self.project {
                if &proj.name != name {
                    continue;
                }
            }

            for stream_name in ["rsync", "ci", "dependabot"] {
                // Stream filter.
                if let Some(ref s) = self.stream {
                    if s != stream_name {
                        continue;
                    }
                }

                let enabled = match stream_name {
                    "rsync"      => proj.streams.rsync.enabled,
                    "ci"         => proj.streams.ci.enabled,
                    "dependabot" => proj.streams.dependabot.enabled,
                    _ => false,
                };
                if !enabled {
                    continue;
                }

                print!("  {} / {} ... ", proj.name, stream_name);

                let result = match stream_name {
                    "rsync"      => run_rsync_stream(proj),
                    "ci"         => run_ci_stream(proj, &sdir),
                    "dependabot" => run_dependabot_stream(proj, &sdir),
                    _ => continue,
                };

                match result {
                    Ok(r) => {
                        println!("delivered={} dropped={}", r.delivered, r.dropped);
                        status.record_success(&proj.name, stream_name, r.delivered, true);
                        total_delivered += r.delivered;
                    }
                    Err(e) => {
                        println!("ERROR: {e}");
                        status.record_failure(&proj.name, stream_name, &e, true);
                    }
                }
            }
        }

        if let Err(e) = status.save(&status_json_path()) {
            eprintln!("warning: failed to persist status.json: {e}");
        }

        println!("nsentry run complete. total_delivered={total_delivered}");
        Ok(())
    }
}

// ── pause / resume ─────────────────────────────────────────────────────────────

/// Arguments for `cascade nsentry pause` / `cascade nsentry resume`.
#[derive(Debug, Args)]
pub struct NsentryPauseResumeArgs {
    /// Project name to toggle.
    #[arg(long)]
    pub project: String,
}

impl NsentryPauseResumeArgs {
    async fn run_toggle(&self, enabled: bool) -> Result<()> {
        let path = config_yaml_path();
        let found = toggle_project_enabled(&path, &self.project, enabled)?;
        if found {
            println!(
                "nsentry: project '{}' {}",
                self.project,
                if enabled { "resumed" } else { "paused" }
            );
        } else {
            return Err(CascadeError::Other(format!(
                "nsentry: project '{}' not found in ~/.cascade/nsentry-sync.yaml",
                self.project
            )));
        }
        Ok(())
    }
}

// ── list ───────────────────────────────────────────────────────────────────────

/// Arguments for `cascade nsentry list`.
#[derive(Debug, Args)]
pub struct NsentryListArgs {}

#[async_trait]
impl Command for NsentryListArgs {
    async fn run(&self) -> Result<()> {
        let cfg = load_cfg(&config_yaml_path())?;

        if cfg.projects.is_empty() {
            println!("No projects configured in ~/.cascade/nsentry-sync.yaml.");
            println!("Add a projects: entry to get started.");
            return Ok(());
        }

        println!("nSentry configured projects ({}):", cfg.projects.len());
        for proj in &cfg.projects {
            println!();
            println!("  name:        {}", proj.name);
            println!("  path:        {}", proj.path.display());
            println!("  org:         {}", proj.org);
            println!("  enabled:     {}", proj.enabled);
            println!("  per_run_cap: {}", proj.per_run_cap);
            println!("  inbox:       {}", proj.resolved_inbox().display());
            if let Some(ref s) = proj.sentry_server {
                println!("  rsync:       {} → {} ({}s)", s, proj.remote_dir, proj.streams.rsync.interval_secs);
            }
            println!(
                "  ci:          org={} per_repo={} ({}s)",
                proj.org, proj.streams.ci.per_repo, proj.streams.ci.interval_secs
            );
            println!(
                "  dependabot:  org={} ({}s)",
                proj.org, proj.streams.dependabot.interval_secs
            );
        }
        Ok(())
    }
}
