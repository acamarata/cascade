//! nSentry multi-stream sync subsystem — daemon-owned, config-driven.
//!
//! # Purpose
//!
//! Runs 3 independent report streams per project on fixed schedules derived
//! from `~/.cascade/nsentry-sync.yaml`:
//!
//! | Stream     | Mechanism                                   | Default interval |
//! |------------|---------------------------------------------|-----------------|
//! | rsync      | `cascade_core::nsentry::sync()`             | 300 s (5 min)   |
//! | ci         | `gh-ci-failures-to-reports.sh`              | 900 s (15 min)  |
//! | dependabot | `gh-dependabot-to-reports.sh`               | 21600 s (6 h)   |
//!
//! # Inputs
//!
//! - `~/.cascade/nsentry-sync.yaml` (absent = no-op).
//! - Bundled scripts at `~/.cascade/nsentry/scripts/` (written once from
//!   `include_str!` at startup).
//!
//! # Outputs
//!
//! - New `*.md` reports in each project's configured inbox.
//! - `~/.cascade/nsentry-sync-status.json` — per (project, stream) status.
//!
//! # Constraints
//!
//! - Stream failures are caught, logged at WARN, recorded in status — never
//!   crash the loop or affect other projects/streams.
//! - Never write outside the configured inbox.
//! - Scheduler keys jobs by (project_name, stream_name): a config reload never
//!   double-schedules.
//! - `gh`, `rsync`, `bash` resolved via probe list in cascade_core::nsentry_config.
//!
//! SPORT: `.github/wiki/nsentry.md` — nSentry multi-stream daemon subsystem

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use cascade_core::nsentry_config::{
    load as load_cfg, run_ci_stream, run_dependabot_stream, run_rsync_stream, NsentrySyncConfig,
    NsentrySyncStatus,
};

// ── Script assets (embedded at compile time) ──────────────────────────────────

const CI_SCRIPT: &str = include_str!("../assets/nsentry/gh-ci-failures-to-reports.sh");
const DEPENDABOT_SCRIPT: &str = include_str!("../assets/nsentry/gh-dependabot-to-reports.sh");

// ── Script setup ─────────────────────────────────────────────────────────────

/// Write the bundled scripts to `scripts_dir` (mode 0755).
/// Uses a tmp-then-rename pattern so partial writes are never visible.
fn install_scripts(scripts_dir: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(scripts_dir)?;

    for (name, content) in [
        ("gh-ci-failures-to-reports.sh", CI_SCRIPT),
        ("gh-dependabot-to-reports.sh", DEPENDABOT_SCRIPT),
    ] {
        let dest = scripts_dir.join(name);
        let tmp = scripts_dir.join(format!("{name}.tmp"));
        std::fs::write(&tmp, content)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(0o755))?;
        }
        std::fs::rename(&tmp, &dest)?;
    }
    Ok(())
}

// ── Scheduler state ───────────────────────────────────────────────────────────

/// In-memory next-run tracker keyed by (project_name, stream_name).
type NextRunMap = HashMap<(String, String), Instant>;

// ── Main spawn entry point ────────────────────────────────────────────────────

/// Spawn the nSentry sync subsystem as a background Tokio task.
///
/// - Installs bundled scripts to `scripts_dir` (typically
///   `~/.cascade/nsentry/scripts/`).
/// - Loads config from `config_yaml_path`.
/// - If config is absent or has no projects, returns immediately (no-op).
/// - Spawns a single async task that drives all project/stream intervals.
/// - Respects `shutdown` for clean exit.
pub fn spawn(
    config_yaml_path: PathBuf,
    status_json_path: PathBuf,
    scripts_dir: PathBuf,
    shutdown: CancellationToken,
) {
    tokio::spawn(async move {
        if let Err(e) = install_scripts(&scripts_dir) {
            warn!(%e, "nsentry: failed to install bundled scripts (streams needing bash will fail)");
        }

        let cfg = match load_cfg(&config_yaml_path) {
            Ok(c) => c,
            Err(e) => {
                warn!(%e, "nsentry: failed to load config (subsystem disabled)");
                return;
            }
        };

        if cfg.projects.is_empty() {
            info!("nsentry: no projects configured — sync subsystem idle");
            return;
        }

        info!(
            projects = cfg.projects.len(),
            "nsentry: sync subsystem starting"
        );

        run_sync_loop(
            cfg,
            config_yaml_path,
            status_json_path,
            scripts_dir,
            shutdown,
        )
        .await;
    });
}

/// Inner async loop — poll all (project, stream) pairs on their configured intervals.
async fn run_sync_loop(
    mut cfg: NsentrySyncConfig,
    config_yaml_path: PathBuf,
    status_json_path: PathBuf,
    scripts_dir: PathBuf,
    shutdown: CancellationToken,
) {
    // Initialise next-run map: each enabled (project, stream) fires shortly after start.
    let mut next_runs: NextRunMap = HashMap::new();
    seed_next_runs(&cfg, &mut next_runs, /* initial_delay_secs= */ 5);

    // Load (or create empty) status.
    let mut status = NsentrySyncStatus::load(&status_json_path);

    // Polling cadence: wake up every 10 s to check if any stream is due.
    let poll = Duration::from_secs(10);

    loop {
        tokio::select! {
            _ = tokio::time::sleep(poll) => {}
            _ = shutdown.cancelled() => {
                info!("nsentry: sync subsystem shutting down");
                break;
            }
        }

        // Reload config every tick (cheap: file stat only).
        if let Ok(new_cfg) = load_cfg(&config_yaml_path) {
            // Add any new (project, stream) pairs; do not reset existing ones.
            seed_next_runs(&new_cfg, &mut next_runs, 0);
            cfg = new_cfg;
        }

        let now = Instant::now();
        let mut any_ran = false;

        for proj in cfg.projects.iter().filter(|p| p.enabled) {
            // ── rsync ──────────────────────────────────────────────────────
            if proj.streams.rsync.enabled {
                let key = (proj.name.clone(), "rsync".to_string());
                if next_runs.get(&key).map(|t| now >= *t).unwrap_or(true) {
                    let result = run_rsync_stream(proj);
                    match &result {
                        Ok(r) => {
                            info!(project = %proj.name, stream = "rsync", delivered = r.delivered, "nsentry stream ok");
                            status.record_success(&proj.name, "rsync", r.delivered, true);
                        }
                        Err(e) => {
                            warn!(project = %proj.name, stream = "rsync", error = %e, "nsentry stream failed");
                            status.record_failure(&proj.name, "rsync", e, true);
                        }
                    }
                    next_runs.insert(
                        key,
                        Instant::now() + Duration::from_secs(proj.streams.rsync.interval_secs),
                    );
                    any_ran = true;
                }
            }

            // ── ci ─────────────────────────────────────────────────────────
            if proj.streams.ci.enabled {
                let key = (proj.name.clone(), "ci".to_string());
                if next_runs.get(&key).map(|t| now >= *t).unwrap_or(true) {
                    let result = run_ci_stream(proj, &scripts_dir);
                    match &result {
                        Ok(r) => {
                            info!(project = %proj.name, stream = "ci", delivered = r.delivered, "nsentry stream ok");
                            status.record_success(&proj.name, "ci", r.delivered, true);
                        }
                        Err(e) => {
                            warn!(project = %proj.name, stream = "ci", error = %e, "nsentry stream failed");
                            status.record_failure(&proj.name, "ci", e, true);
                        }
                    }
                    next_runs.insert(
                        key,
                        Instant::now() + Duration::from_secs(proj.streams.ci.interval_secs),
                    );
                    any_ran = true;
                }
            }

            // ── dependabot ─────────────────────────────────────────────────
            if proj.streams.dependabot.enabled {
                let key = (proj.name.clone(), "dependabot".to_string());
                if next_runs.get(&key).map(|t| now >= *t).unwrap_or(true) {
                    let result = run_dependabot_stream(proj, &scripts_dir);
                    match &result {
                        Ok(r) => {
                            info!(project = %proj.name, stream = "dependabot", delivered = r.delivered, "nsentry stream ok");
                            status.record_success(&proj.name, "dependabot", r.delivered, true);
                        }
                        Err(e) => {
                            warn!(project = %proj.name, stream = "dependabot", error = %e, "nsentry stream failed");
                            status.record_failure(&proj.name, "dependabot", e, true);
                        }
                    }
                    next_runs.insert(
                        key,
                        Instant::now() + Duration::from_secs(proj.streams.dependabot.interval_secs),
                    );
                    any_ran = true;
                }
            }
        }

        if any_ran {
            if let Err(e) = status.save(&status_json_path) {
                warn!(%e, "nsentry: failed to persist status.json");
            }
        }
    }
}

/// Seed `next_runs` for every enabled (project, stream) that does not yet have
/// an entry. `initial_delay_secs` staggers the first run after startup.
fn seed_next_runs(cfg: &NsentrySyncConfig, next_runs: &mut NextRunMap, initial_delay_secs: u64) {
    let soon = Instant::now() + Duration::from_secs(initial_delay_secs);
    for proj in cfg.projects.iter().filter(|p| p.enabled) {
        for stream in ["rsync", "ci", "dependabot"] {
            let key = (proj.name.clone(), stream.to_string());
            next_runs.entry(key).or_insert(soon);
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::nsentry_config::{
        CiStreamConfig, DependabotStreamConfig, NsentryProjectConfig, NsentrySyncConfig,
        RsyncStreamConfig, StreamsConfig,
    };
    use tempfile::TempDir;

    fn make_project(name: &str, tmp: &Path) -> NsentryProjectConfig {
        NsentryProjectConfig {
            name: name.to_string(),
            path: tmp.join(name),
            org: "test-org".to_string(),
            sentry_server: Some("root@sentry.test".to_string()),
            remote_dir: "/opt/nself-ops/errors".to_string(),
            inbox: Some(tmp.join(name).join(".claude").join("inbox")),
            enabled: true,
            per_run_cap: 50,
            streams: StreamsConfig {
                rsync: RsyncStreamConfig {
                    interval_secs: 300,
                    enabled: true,
                },
                ci: CiStreamConfig {
                    interval_secs: 900,
                    enabled: true,
                    per_repo: 10,
                },
                dependabot: DependabotStreamConfig {
                    interval_secs: 21600,
                    enabled: true,
                },
            },
        }
    }

    #[test]
    fn status_json_round_trip() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("nsentry-sync-status.json");

        let mut s = NsentrySyncStatus::default();
        s.record_success("nself", "rsync", 3, true);
        s.record_failure("nself", "ci", "gh error", true);
        s.save(&path).unwrap();

        let loaded = NsentrySyncStatus::load(&path);
        let rsync = &loaded.streams["nself"]["rsync"];
        assert_eq!(rsync.last_delivered, 3);
        assert_eq!(rsync.total_delivered, 3);
        assert!(rsync.last_error.is_none());

        let ci = &loaded.streams["nself"]["ci"];
        assert!(ci.last_error.is_some());
        assert_eq!(ci.last_delivered, 0);
    }

    #[test]
    fn status_load_absent_returns_empty() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("noexist.json");
        let s = NsentrySyncStatus::load(&path);
        assert!(s.streams.is_empty());
    }

    #[test]
    fn status_total_accumulates_across_runs() {
        let mut s = NsentrySyncStatus::default();
        s.record_success("proj", "rsync", 4, true);
        s.record_success("proj", "rsync", 6, true);
        assert_eq!(s.streams["proj"]["rsync"].total_delivered, 10);
        assert_eq!(s.streams["proj"]["rsync"].last_delivered, 6);
    }

    #[test]
    fn scheduler_no_dup_on_reload() {
        let dir = TempDir::new().unwrap();
        let cfg = NsentrySyncConfig {
            version: 1,
            projects: vec![make_project("nself", dir.path())],
        };
        let mut next_runs: NextRunMap = HashMap::new();

        seed_next_runs(&cfg, &mut next_runs, 0);
        assert_eq!(next_runs.len(), 3);

        let t_before = next_runs[&("nself".to_string(), "rsync".to_string())];
        // Reload with large delay — must NOT overwrite existing entry.
        seed_next_runs(&cfg, &mut next_runs, 999);
        assert_eq!(next_runs.len(), 3);
        let t_after = next_runs[&("nself".to_string(), "rsync".to_string())];
        assert_eq!(
            t_before, t_after,
            "existing next-run must not be reset on reload"
        );
    }

    #[test]
    fn scheduler_adds_new_project_on_reload() {
        let dir = TempDir::new().unwrap();
        let cfg1 = NsentrySyncConfig {
            version: 1,
            projects: vec![make_project("alpha", dir.path())],
        };
        let cfg2 = NsentrySyncConfig {
            version: 1,
            projects: vec![
                make_project("alpha", dir.path()),
                make_project("beta", dir.path()),
            ],
        };
        let mut next_runs: NextRunMap = HashMap::new();
        seed_next_runs(&cfg1, &mut next_runs, 0);
        assert_eq!(next_runs.len(), 3); // alpha×3
        seed_next_runs(&cfg2, &mut next_runs, 0);
        assert_eq!(next_runs.len(), 6); // alpha×3 + beta×3
    }

    #[test]
    fn rsync_stream_missing_server_returns_err() {
        let dir = TempDir::new().unwrap();
        let mut proj = make_project("test", dir.path());
        proj.sentry_server = None;
        let r = run_rsync_stream(&proj);
        assert!(r.is_err());
        assert!(r.unwrap_err().contains("sentry_server"));
    }
}
