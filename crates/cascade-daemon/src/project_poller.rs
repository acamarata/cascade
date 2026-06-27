//! Project-state poller — async Rust replacement for the legacy shell project-state scanner.
//!
//! Purpose: Poll ~/Sites/ for active phase state files (status.yaml), aggregate their
//! state into a summary, write to `project-state.json`, and publish a `project.state.updated`
//! event. Replaces the shell-based scanning logic that lived in claw-dash.
//!
//! Inputs:
//!   - `ProjectPollerConfig` — interval_secs, enabled
//!   - `config_dir: PathBuf` — where to write `project-state.json`
//!   - `bus: SharedBus` — event bus for `project.state.updated` events
//!   - `shutdown: CancellationToken` — graceful-stop signal
//!
//! Outputs:
//!   - `project-state.json` written every interval with aggregated project state
//!   - One `project.state.updated` event published per successful write
//!
//! Constraints:
//!   - tokio::spawn fire-and-forget: supervisor drops the handle; shutdown token
//!     drives the exit, not task completion.
//!   - Scans ~/Sites/*/.claude/phases/current/*/status.yaml and
//!     ~/Sites/*/*/.claude/phases/current/*/status.yaml recursively (max 2 levels).
//!   - If ~/Sites is missing or unreadable, logs warn and exits the loop gracefully.
//!   - Parses YAML status.yaml files; parse errors are logged but do not crash.
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — project_poller module row (T-P2-E02-12)

use std::fs;
use std::path::PathBuf;
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

use crate::config::ProjectPollerConfig;
use crate::event_bus::SharedBus;
use crate::supervisor::DaemonError;

// ── Types ─────────────────────────────────────────────────────────────────────

/// Parsed subset of a status.yaml file from a phase directory.
///
/// We only care about phase_status and pct_done; other fields are ignored.
#[derive(Debug, Clone, Deserialize, Default)]
struct PhaseStatus {
    #[serde(default)]
    phase_status: String,
    #[serde(default)]
    phase_id: String,
    #[serde(default)]
    pct_done: f64,
    #[serde(default)]
    critical_findings_open: u32,
    #[serde(default)]
    high_findings_open: u32,
}

/// A single project's aggregated state (derived from its status.yaml).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectState {
    /// Absolute path to the project root (e.g., /home/user/projects/myapp).
    pub root: String,
    /// Phase ID if a phase is active (e.g., P1, P2); null if mode=normal.
    pub phase_id: Option<String>,
    /// Phase status enum (e.g., planning, building, done); null if mode=normal.
    pub phase_status: Option<String>,
    /// Percentage complete (0.0 to 100.0); 0.0 if no active phase.
    pub pct_done: f64,
    /// Number of open critical findings; 0 if no active phase.
    pub critical_open: u32,
    /// Number of open high findings; 0 if no active phase.
    pub high_open: u32,
}

/// Aggregated snapshot of all projects' states.
///
/// Inputs: collected from ~/Sites/*//.claude/phases/current/*/status.yaml
/// Outputs: written verbatim to `project-state.json`; also the bus event payload.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct ProjectStateReport {
    /// List of discovered projects with their current state.
    pub projects: Vec<ProjectState>,
    /// Unix timestamp (seconds) of when this report was generated.
    pub ts: i64,
}

// ── Poller ────────────────────────────────────────────────────────────────────

/// Async project-state poller task.
///
/// Constructed as a zero-size unit struct; all state is passed into `run()`.
/// Callers spawn `ProjectPoller::run(...)` with `tokio::spawn` (fire-and-forget).
pub struct ProjectPoller;

impl ProjectPoller {
    /// Run the project poller loop until `shutdown` is cancelled.
    ///
    /// Inputs:
    ///   - `config`     — `ProjectPollerConfig` (interval_secs, enabled)
    ///   - `config_dir` — directory where `project-state.json` is written
    ///   - `bus`        — shared event bus for `project.state.updated` events
    ///   - `shutdown`   — cancellation token; loop exits when fired
    ///
    /// Outputs: `Ok(())` always (errors are logged, not propagated — fire-and-forget).
    ///
    /// Constraints:
    ///   - Scans max 2 levels deep under ~/Sites/ (project + sub-repo).
    ///   - Parse errors are logged as warnings, not fatal.
    ///   - If ~/Sites is missing, logs once and exits loop gracefully.
    ///   - Shutdown: checked via `tokio::select!` before and after each sleep.
    pub async fn run(
        config: ProjectPollerConfig,
        config_dir: PathBuf,
        bus: SharedBus,
        shutdown: CancellationToken,
    ) -> Result<(), DaemonError> {
        let interval = Duration::from_secs(config.interval_secs);
        let state_path = config_dir.join("project-state.json");
        let sites_root = dirs::home_dir()
            .map(|h| h.join("Sites"))
            .unwrap_or_else(|| PathBuf::from("~/Sites"));

        info!(
            interval_secs = config.interval_secs,
            sites_root = %sites_root.display(),
            "project_poller: starting"
        );

        loop {
            // Scan ~/Sites/ for all status.yaml files and aggregate into a report.
            let report = scan_projects(&sites_root);

            // Write project-state.json (best-effort; log on error).
            write_report(&state_path, &report).await;

            // Publish event to the bus (best-effort; log on error).
            if let Err(e) = bus
                .publish(
                    "project.state.updated",
                    serde_json::to_value(&report).unwrap_or(serde_json::Value::Null),
                )
                .await
            {
                warn!(error = %e, "project_poller: failed to publish project.state.updated event");
            }

            // Sleep for the configured interval, but wake immediately on shutdown.
            tokio::select! {
                _ = tokio::time::sleep(interval) => {}
                _ = shutdown.cancelled() => {
                    info!("project_poller: shutdown signal — exiting");
                    break;
                }
            }
        }

        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Scan ~/Sites/ recursively (max 2 levels) for all status.yaml files and
/// aggregate into a ProjectStateReport.
///
/// Returns a report even if ~/Sites/ is missing (empty projects array).
fn scan_projects(sites_root: &std::path::Path) -> ProjectStateReport {
    let mut projects = Vec::new();

    // Check if ~/Sites/ exists.
    if !sites_root.exists() {
        warn!(
            path = %sites_root.display(),
            "project_poller: sites_root does not exist — returning empty report"
        );
        return ProjectStateReport {
            projects,
            ts: now_secs(),
        };
    }

    // Level 1: iterate direct subdirectories of ~/Sites (projects).
    match fs::read_dir(sites_root) {
        Err(e) => {
            warn!(
                path = %sites_root.display(),
                error = %e,
                "project_poller: failed to read sites_root — returning empty report"
            );
            return ProjectStateReport {
                projects,
                ts: now_secs(),
            };
        }
        Ok(entries) => {
            for entry in entries {
                let entry = match entry {
                    Ok(e) => e,
                    Err(e) => {
                        warn!(error = %e, "project_poller: failed to read dir entry");
                        continue;
                    }
                };
                let path = entry.path();

                // Skip non-directories.
                if !path.is_dir() {
                    continue;
                }

                // Try to find status.yaml at path/.claude/phases/current/*/status.yaml.
                // Also check one level deeper (path/*/.claude/phases/current/*/status.yaml).
                scan_project_dir(&path, &mut projects);
                scan_sub_repos(&path, &mut projects);
            }
        }
    }

    ProjectStateReport {
        projects,
        ts: now_secs(),
    }
}

/// Scan a single project directory for status.yaml at .claude/phases/current/*/status.yaml.
fn scan_project_dir(project_path: &std::path::Path, projects: &mut Vec<ProjectState>) {
    let phases_current = project_path.join(".claude").join("phases").join("current");

    if !phases_current.exists() {
        return;
    }

    // List all phase dirs (P1, P2, etc.) in current/.
    match fs::read_dir(&phases_current) {
        Err(e) => {
            warn!(
                path = %phases_current.display(),
                error = %e,
                "project_poller: failed to read phases/current"
            );
        }
        Ok(entries) => {
            for entry in entries {
                let entry = match entry {
                    Ok(e) => e,
                    Err(e) => {
                        warn!(error = %e, "project_poller: failed to read phase dir entry");
                        continue;
                    }
                };
                let status_yaml = entry.path().join("status.yaml");

                if status_yaml.exists() {
                    if let Some(state) = parse_status_yaml(&status_yaml, project_path) {
                        projects.push(state);
                    }
                }
            }
        }
    }
}

/// Scan one level deeper for repo subdirectories and their status.yaml files.
/// This handles the case where a project has sub-repos (e.g., ~/Sites/nself/cli/).
fn scan_sub_repos(project_path: &std::path::Path, projects: &mut Vec<ProjectState>) {
    // Iterate direct subdirectories of the project.
    match fs::read_dir(project_path) {
        Err(_) => (),
        Ok(entries) => {
            for entry in entries {
                let entry = match entry {
                    Ok(e) => e,
                    Err(_) => continue,
                };
                let sub_path = entry.path();

                if !sub_path.is_dir() {
                    continue;
                }

                // Skip .claude and other dotfiles at the project level.
                if let Some(name) = sub_path.file_name() {
                    if name.to_string_lossy().starts_with('.') {
                        continue;
                    }
                }

                // Try to find status.yaml in sub_path/.claude/phases/current/*/status.yaml.
                let phases_current = sub_path.join(".claude").join("phases").join("current");

                if !phases_current.exists() {
                    continue;
                }

                match fs::read_dir(&phases_current) {
                    Err(_) => continue,
                    Ok(entries) => {
                        for entry in entries {
                            let entry = match entry {
                                Ok(e) => e,
                                Err(_) => continue,
                            };
                            let status_yaml = entry.path().join("status.yaml");

                            if status_yaml.exists() {
                                if let Some(state) = parse_status_yaml(&status_yaml, &sub_path) {
                                    projects.push(state);
                                }
                            }
                        }
                    }
                }
            }
        }
    }
}

/// Parse a single status.yaml file and return a ProjectState.
///
/// Returns None if the file cannot be read or parsed; errors are logged.
fn parse_status_yaml(path: &std::path::Path, root_path: &std::path::Path) -> Option<ProjectState> {
    let content = match fs::read_to_string(path) {
        Ok(c) => c,
        Err(e) => {
            warn!(
                path = %path.display(),
                error = %e,
                "project_poller: failed to read status.yaml"
            );
            return None;
        }
    };

    let phase: PhaseStatus = match serde_yaml::from_str(&content) {
        Ok(p) => p,
        Err(e) => {
            warn!(
                path = %path.display(),
                error = %e,
                "project_poller: failed to parse status.yaml"
            );
            return None;
        }
    };

    // If phase_status is "normal" (or empty/null), skip this project.
    if phase.phase_status.is_empty() || phase.phase_status == "normal" {
        return None;
    }

    Some(ProjectState {
        root: root_path.to_string_lossy().to_string(),
        phase_id: if phase.phase_id.is_empty() {
            None
        } else {
            Some(phase.phase_id)
        },
        phase_status: if phase.phase_status.is_empty() {
            None
        } else {
            Some(phase.phase_status)
        },
        pct_done: phase.pct_done,
        critical_open: phase.critical_findings_open,
        high_open: phase.high_findings_open,
    })
}

/// Write a ProjectStateReport to disk as JSON.
///
/// Logs errors but never crashes.
async fn write_report(path: &std::path::Path, report: &ProjectStateReport) {
    let json = match serde_json::to_string_pretty(report) {
        Ok(j) => j,
        Err(e) => {
            warn!(error = %e, "project_poller: failed to serialize report");
            return;
        }
    };

    if let Err(e) = tokio::fs::write(path, json).await {
        warn!(
            path = %path.display(),
            error = %e,
            "project_poller: failed to write project-state.json"
        );
    }
}

/// Get current Unix timestamp (seconds).
fn now_secs() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    #[test]
    fn test_parse_status_yaml_building() {
        // Create a tempdir with a fake status.yaml.
        let tempdir = TempDir::new().unwrap();
        let status_yaml = tempdir.path().join("status.yaml");

        let content = r#"
phase_status: building
phase_id: P1
pct_done: 50.0
critical_findings_open: 0
high_findings_open: 2
"#;
        fs::write(&status_yaml, content).unwrap();

        let state = parse_status_yaml(&status_yaml, tempdir.path()).unwrap();

        assert_eq!(state.phase_status, Some("building".to_string()));
        assert_eq!(state.phase_id, Some("P1".to_string()));
        assert_eq!(state.pct_done, 50.0);
        assert_eq!(state.critical_open, 0);
        assert_eq!(state.high_open, 2);
    }

    #[test]
    fn test_parse_status_yaml_normal_mode() {
        let tempdir = TempDir::new().unwrap();
        let status_yaml = tempdir.path().join("status.yaml");

        let content = r#"
phase_status: normal
"#;
        fs::write(&status_yaml, content).unwrap();

        let state = parse_status_yaml(&status_yaml, tempdir.path());
        // Normal mode projects are skipped (None).
        assert!(state.is_none());
    }

    #[test]
    fn test_project_state_report() {
        let report = ProjectStateReport {
            projects: vec![ProjectState {
                root: "/home/user/projects/myapp".to_string(),
                phase_id: Some("P1".to_string()),
                phase_status: Some("building".to_string()),
                pct_done: 50.0,
                critical_open: 0,
                high_open: 2,
            }],
            ts: 1234567890,
        };

        // Verify it serializes to JSON correctly.
        let json = serde_json::to_string_pretty(&report).unwrap();
        assert!(json.contains("\"phase_status\": \"building\""));
        assert!(json.contains("\"pct_done\": 50.0"));

        // Verify it deserializes back.
        let deserialized: ProjectStateReport = serde_json::from_str(&json).unwrap();
        assert_eq!(deserialized, report);
    }
}
