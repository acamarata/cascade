//! `cascade doctor` — diagnose cascade health and report issues.
//!
//! Runs a series of checks and prints a structured report table:
//!
//! ```text
//! CHECK                          STATUS   DETAIL
//! ─────────────────────────────────────────────────────
//! GCI tier health                PASS
//! PRC tier health                WARN     AGENTS.md is not a symlink
//! Daemon socket                  WARN     daemon not running
//! Global config                  PASS
//! Project config                 FAIL     TOML parse error on line 3
//! Stale derive-files             PASS
//! ```
//!
//! `--fix` auto-repairs safe issues (broken symlinks, stale temp files).

use std::path::PathBuf;

use async_trait::async_trait;
use clap::Args;
use cascade_types::error::Result;
use cascade_types::paths;

use super::Command;

/// Arguments for `cascade doctor`.
#[derive(Debug, Args)]
pub struct DoctorArgs {
    /// Attempt to auto-repair safe issues (rebuild symlinks, remove stale files).
    #[arg(long)]
    pub fix: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum CheckStatus {
    Pass,
    Warn,
    Fail,
}

impl std::fmt::Display for CheckStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            CheckStatus::Pass => write!(f, "PASS"),
            CheckStatus::Warn => write!(f, "WARN"),
            CheckStatus::Fail => write!(f, "FAIL"),
        }
    }
}

struct Check {
    name: &'static str,
    status: CheckStatus,
    detail: String,
}

#[async_trait]
impl Command for DoctorArgs {
    async fn run(&self) -> Result<()> {
        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
        let mut checks = Vec::<Check>::new();
        let mut any_fail = false;

        // ── Tier health ────────────────────────────────────────────────────
        let tiers = cascade_core::discovery::TierDiscovery::new().discover(&cwd)?;
        for t in &tiers {
            let name: &'static str = Box::leak(
                format!("{:?} tier health", t.tier).into_boxed_str()
            );
            let issues = cascade_core::symlinks::verify_siblings(
                &t.root.join(cascade_types::paths::CASCADE_DIR_NAME)
            );
            if !t.is_present {
                checks.push(Check { name, status: CheckStatus::Warn, detail: "CASCADE.md not found".into() });
            } else if !issues.is_empty() {
                checks.push(Check { name, status: CheckStatus::Warn, detail: issues.join("; ") });
            } else {
                checks.push(Check { name, status: CheckStatus::Pass, detail: String::new() });
            }
        }

        // ── Daemon socket ──────────────────────────────────────────────────
        {
            let sock = paths::daemon_socket();
            let (status, detail) = if sock.exists() {
                (CheckStatus::Pass, String::new())
            } else {
                (CheckStatus::Warn, format!("socket not found at {}", sock.display()))
            };
            checks.push(Check { name: "Daemon socket", status, detail });
        }

        // ── Config integrity ───────────────────────────────────────────────
        for (label, config_path) in config_paths(&cwd) {
            if !config_path.exists() {
                checks.push(Check {
                    name: Box::leak(label.into_boxed_str()),
                    status: CheckStatus::Pass,
                    detail: "(not present — using defaults)".into(),
                });
                continue;
            }
            let raw = std::fs::read_to_string(&config_path).unwrap_or_default();
            match raw.parse::<toml::Value>() {
                Ok(_) => checks.push(Check {
                    name: Box::leak(label.into_boxed_str()),
                    status: CheckStatus::Pass,
                    detail: String::new(),
                }),
                Err(e) => {
                    any_fail = true;
                    checks.push(Check {
                        name: Box::leak(label.into_boxed_str()),
                        status: CheckStatus::Fail,
                        detail: e.to_string(),
                    });
                }
            }
        }

        // ── Stale derive-files ─────────────────────────────────────────────
        {
            let mut stale: Vec<String> = Vec::new();
            for t in &tiers {
                let cascade_dir = t.root.join(cascade_types::paths::CASCADE_DIR_NAME);
                let issues = cascade_core::symlinks::verify_siblings(&cascade_dir);
                for issue in issues {
                    stale.push(format!("[{:?}] {}", t.tier, issue));
                }
            }
            let (status, detail) = if stale.is_empty() {
                (CheckStatus::Pass, String::new())
            } else {
                (CheckStatus::Warn, stale.join("; "))
            };
            checks.push(Check { name: "Stale derive-files", status, detail });
        }

        // ── Auto-fix pass ──────────────────────────────────────────────────
        if self.fix {
            for t in &tiers {
                if t.is_present {
                    let cascade_dir = t.root.join(cascade_types::paths::CASCADE_DIR_NAME);
                    if let Err(e) = cascade_core::symlinks::create_siblings(&cascade_dir, false) {
                        eprintln!("fix failed for {:?}: {}", t.tier, e);
                    }
                }
            }
        }

        // ── Print report ───────────────────────────────────────────────────
        println!("{:<40} {:<8} {}", "CHECK", "STATUS", "DETAIL");
        println!("{}", "-".repeat(80));
        for c in &checks {
            if c.status == CheckStatus::Fail { any_fail = true; }
            println!("{:<40} {:<8} {}", c.name, c.status, c.detail);
        }

        if any_fail {
            std::process::exit(1);
        }
        Ok(())
    }
}

fn config_paths(cwd: &std::path::Path) -> Vec<(String, PathBuf)> {
    let mut paths_list = Vec::new();
    // Global config.
    if let Ok(home) = std::env::var("HOME") {
        paths_list.push((
            "Global config".into(),
            PathBuf::from(home).join(".cascade").join("config.toml"),
        ));
    }
    // Project config (nearest ancestor).
    if let Some(p) = cwd.ancestors().find_map(|a| {
        let c = a.join(".cascade").join("config.toml");
        if c.exists() { Some(c) } else { None }
    }) {
        paths_list.push(("Project config".into(), p));
    }
    paths_list
}
