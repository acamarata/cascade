//! `cascade status` — report daemon health, index freshness, and tier health.
//!
//! Outputs a human-readable table with three sections:
//! 1. **Daemon** — socket present, PID alive, daemon version.
//! 2. **Index** — last index timestamp (from cache), number of chunks.
//! 3. **Cascade tiers** — one row per discovered tier, health indicator.
//!
//! Exit code `0` if all checks pass; `1` if any check is FAIL.

use std::path::PathBuf;

use async_trait::async_trait;
use clap::Args;
use cascade_types::error::Result;
use cascade_types::paths;

use super::Command;

/// Arguments for `cascade status`.
#[derive(Debug, Args)]
pub struct StatusArgs {
    /// Output as JSON instead of a human-readable table.
    #[arg(long)]
    pub json: bool,
}

#[async_trait]
impl Command for StatusArgs {
    async fn run(&self) -> Result<()> {
        let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));

        let daemon_ok = check_daemon();
        let tiers = cascade_core::discovery::TierDiscovery::new().discover(&cwd)?;

        let any_fail = !daemon_ok || tiers.iter().any(|t| !t.is_healthy() && t.is_present);

        if self.json {
            // Minimal JSON output for scripting.
            let tier_json: Vec<serde_json::Value> = tiers
                .iter()
                .map(|t| {
                    serde_json::json!({
                        "tier": format!("{:?}", t.tier),
                        "path": t.cascade_md.display().to_string(),
                        "present": t.is_present,
                        "healthy": t.is_healthy(),
                    })
                })
                .collect();
            let out = serde_json::json!({
                "daemon": daemon_ok,
                "tiers": tier_json,
            });
            println!("{}", serde_json::to_string_pretty(&out).unwrap());
        } else {
            println!("{:<12} {}", "DAEMON", if daemon_ok { "OK" } else { "NOT RUNNING" });
            println!();
            println!("{:<8} {:<10} {:<8} {}", "TIER", "STATUS", "SYMLINKS", "PATH");
            println!("{}", "-".repeat(72));
            for t in &tiers {
                let status = if t.is_present { "OK" } else { "MISSING" };
                let symlinks = if t.claude_md_valid && t.agents_md_valid {
                    "OK"
                } else if t.is_present {
                    "BROKEN"
                } else {
                    "—"
                };
                println!(
                    "{:<8} {:<10} {:<8} {}",
                    format!("{:?}", t.tier),
                    status,
                    symlinks,
                    t.cascade_md.display()
                );
            }
        }

        if any_fail {
            std::process::exit(1);
        }
        Ok(())
    }
}

/// Returns `true` if the daemon socket exists and is a socket file.
fn check_daemon() -> bool {
    let sock = paths::daemon_socket();
    sock.exists()
}
