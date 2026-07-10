//! nSentry multi-stream sync configuration — `~/.cascade/nsentry-sync.yaml`.
//!
//! # Purpose
//!
//! Declarative YAML config that drives the daemon-owned nSentry sync subsystem.
//! Each project entry configures up to three independent streams:
//! - **rsync** — pull `*.md` reports from a remote sentry box (reuses
//!   [`cascade_core::nsentry::sync`]).
//! - **ci** — run the bundled `gh-ci-failures-to-reports.sh` script against the
//!   project's GitHub org and write reports into the inbox.
//! - **dependabot** — run the bundled `gh-dependabot-to-reports.sh` script.
//!
//! # Inputs
//!
//! - Config file at `~/.cascade/nsentry-sync.yaml` (absent = no-op).
//! - `load(path)` returns `Ok(NsentrySyncConfig { projects: [] })` when absent.
//!
//! # Outputs
//!
//! - `NsentrySyncConfig` — loaded/saved via `load` / `save`.
//! - `toggle_project_enabled` — flip a project's `enabled` flag (idempotent).
//!
//! # Constraints
//!
//! - No maintainer hostnames/paths/emails in shipped code (all from config at
//!   runtime).
//! - Re-loading is idempotent: callers key scheduler jobs by (project, stream).
//! - `serde_yaml` for YAML I/O; all fields have serde defaults so absent keys
//!   parse cleanly.
//!
//! SPORT: `.github/wiki/nsentry.md` — nSentry multi-stream config

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

use cascade_types::error::{CascadeError, Result};

// ── Stream configs ─────────────────────────────────────────────────────────────

/// Per-stream interval + enable toggle for the rsync stream.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RsyncStreamConfig {
    /// Poll interval in seconds.
    #[serde(default = "default_rsync_interval")]
    pub interval_secs: u64,
    /// Whether this stream is active.
    #[serde(default = "default_true")]
    pub enabled: bool,
}

impl Default for RsyncStreamConfig {
    fn default() -> Self {
        Self {
            interval_secs: default_rsync_interval(),
            enabled: true,
        }
    }
}

/// Per-stream interval + enable toggle for the CI script stream.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CiStreamConfig {
    /// Poll interval in seconds.
    #[serde(default = "default_ci_interval")]
    pub interval_secs: u64,
    /// Whether this stream is active.
    #[serde(default = "default_true")]
    pub enabled: bool,
    /// Max failed runs fetched per repo per script invocation.
    #[serde(default = "default_per_repo")]
    pub per_repo: u32,
}

impl Default for CiStreamConfig {
    fn default() -> Self {
        Self {
            interval_secs: default_ci_interval(),
            enabled: true,
            per_repo: default_per_repo(),
        }
    }
}

/// Per-stream interval + enable toggle for the Dependabot script stream.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct DependabotStreamConfig {
    /// Poll interval in seconds.
    #[serde(default = "default_dependabot_interval")]
    pub interval_secs: u64,
    /// Whether this stream is active.
    #[serde(default = "default_true")]
    pub enabled: bool,
}

impl Default for DependabotStreamConfig {
    fn default() -> Self {
        Self {
            interval_secs: default_dependabot_interval(),
            enabled: true,
        }
    }
}

/// All three stream configs grouped together.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct StreamsConfig {
    #[serde(default)]
    pub rsync: RsyncStreamConfig,
    #[serde(default)]
    pub ci: CiStreamConfig,
    #[serde(default)]
    pub dependabot: DependabotStreamConfig,
}

// ── Project entry ─────────────────────────────────────────────────────────────

/// A single project's nSentry sync configuration.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NsentryProjectConfig {
    /// Human-readable project name (used as the scheduler key).
    pub name: String,
    /// Local filesystem path to the project root.
    pub path: PathBuf,
    /// GitHub organisation slug (used by the ci and dependabot script streams).
    pub org: String,
    /// SSH target for the rsync stream (`user@host`). Required only when
    /// `streams.rsync.enabled` is `true`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub sentry_server: Option<String>,
    /// Remote directory to rsync from. Defaults to `/opt/nself-ops/errors`.
    #[serde(
        default = "default_remote_dir",
        skip_serializing_if = "is_default_remote_dir"
    )]
    pub remote_dir: String,
    /// Local inbox directory. Defaults to `<path>/.claude/inbox`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub inbox: Option<PathBuf>,
    /// Whether the whole project is syncing. Individual streams can also be
    /// toggled; both must be `true` for a stream to run.
    #[serde(default = "default_true")]
    pub enabled: bool,
    /// Maximum reports written to the inbox per stream run.
    #[serde(default = "default_per_run_cap")]
    pub per_run_cap: usize,
    /// Per-stream configuration.
    #[serde(default)]
    pub streams: StreamsConfig,
}

impl NsentryProjectConfig {
    /// Resolve the effective inbox path.
    pub fn resolved_inbox(&self) -> PathBuf {
        self.inbox
            .clone()
            .unwrap_or_else(|| self.path.join(".claude").join("inbox"))
    }
}

// ── Top-level config ──────────────────────────────────────────────────────────

/// Top-level structure for `~/.cascade/nsentry-sync.yaml`.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct NsentrySyncConfig {
    /// Schema version (currently 1).
    #[serde(default = "schema_v1")]
    pub version: u32,
    /// Configured projects.
    #[serde(default)]
    pub projects: Vec<NsentryProjectConfig>,
}

// ── I/O helpers ───────────────────────────────────────────────────────────────

/// Load the multi-stream config from `path`.
///
/// Returns an empty config (not an error) when the file is absent — the caller
/// can treat that as "nothing configured yet" and skip spawning any jobs.
pub fn load(path: &Path) -> Result<NsentrySyncConfig> {
    if !path.is_file() {
        return Ok(NsentrySyncConfig::default());
    }
    let raw = std::fs::read_to_string(path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "read nsentry-sync.yaml",
        source: e,
    })?;
    let cfg: NsentrySyncConfig =
        serde_yaml::from_str(&raw).map_err(|e| CascadeError::ConfigParse {
            path: path.to_path_buf(),
            detail: e.to_string(),
        })?;
    Ok(cfg)
}

/// Persist the config to `path` atomically (write tmp then rename).
pub fn save(path: &Path, cfg: &NsentrySyncConfig) -> Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create nsentry-sync.yaml parent dir",
            source: e,
        })?;
    }
    let raw = serde_yaml::to_string(cfg)
        .map_err(|e| CascadeError::Other(format!("nsentry-sync.yaml serialize: {e}")))?;
    let tmp = path.with_extension("yaml.tmp");
    std::fs::write(&tmp, &raw).map_err(|e| CascadeError::Io {
        path: tmp.clone(),
        operation: "write nsentry-sync.yaml (tmp)",
        source: e,
    })?;
    std::fs::rename(&tmp, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "rename nsentry-sync.yaml into place",
        source: e,
    })?;
    Ok(())
}

/// Toggle the `enabled` flag for a project by name. Writes the updated config
/// back to `path`. Idempotent: if the project is not found, a warning is
/// returned via `Ok(false)`.
pub fn toggle_project_enabled(path: &Path, project_name: &str, enabled: bool) -> Result<bool> {
    let mut cfg = load(path)?;
    let Some(proj) = cfg.projects.iter_mut().find(|p| p.name == project_name) else {
        return Ok(false);
    };
    proj.enabled = enabled;
    save(path, &cfg)?;
    Ok(true)
}

// ── Default helpers ───────────────────────────────────────────────────────────

fn default_true() -> bool {
    true
}
fn schema_v1() -> u32 {
    1
}
fn default_rsync_interval() -> u64 {
    300
}
fn default_ci_interval() -> u64 {
    900
}
fn default_dependabot_interval() -> u64 {
    21600
}
fn default_per_repo() -> u32 {
    10
}
fn default_per_run_cap() -> usize {
    50
}
fn default_remote_dir() -> String {
    "/opt/nself-ops/errors".to_string()
}
fn is_default_remote_dir(s: &str) -> bool {
    s == "/opt/nself-ops/errors"
}

// ── Sync status ───────────────────────────────────────────────────────────────

use std::collections::HashMap;

/// Per (project, stream) run status, persisted to
/// `~/.cascade/nsentry-sync-status.json` after every run.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct StreamStatus {
    /// Whether this stream is enabled in the config.
    pub enabled: bool,
    /// Unix timestamp (seconds) of the most recent attempt.
    pub last_run_at: Option<u64>,
    /// Unix timestamp of the most recent successful attempt.
    pub last_success_at: Option<u64>,
    /// Count of reports delivered in the last run.
    pub last_delivered: usize,
    /// Cumulative reports delivered across all runs.
    pub total_delivered: usize,
    /// Human-readable error from the last failed run, if any.
    pub last_error: Option<String>,
}

/// Top-level status map: project_name → stream_name → StreamStatus.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct NsentrySyncStatus {
    /// Per-project, per-stream status.
    #[serde(default)]
    pub streams: HashMap<String, HashMap<String, StreamStatus>>,
}

impl NsentrySyncStatus {
    /// Load from `path`. Returns an empty status when absent.
    pub fn load(path: &Path) -> Self {
        if !path.is_file() {
            return Self::default();
        }
        std::fs::read_to_string(path)
            .ok()
            .and_then(|raw| serde_json::from_str(&raw).ok())
            .unwrap_or_default()
    }

    /// Persist atomically to `path`.
    pub fn save(&self, path: &Path) -> std::io::Result<()> {
        let raw = serde_json::to_string_pretty(self)?;
        let tmp = path.with_extension("json.tmp");
        std::fs::write(&tmp, &raw)?;
        std::fs::rename(&tmp, path)?;
        Ok(())
    }

    /// Record a successful run result.
    pub fn record_success(&mut self, project: &str, stream: &str, delivered: usize, enabled: bool) {
        let now = unix_now();
        let s = self
            .streams
            .entry(project.to_string())
            .or_default()
            .entry(stream.to_string())
            .or_default();
        s.enabled = enabled;
        s.last_run_at = Some(now);
        s.last_success_at = Some(now);
        s.last_delivered = delivered;
        s.total_delivered += delivered;
        s.last_error = None;
    }

    /// Record a failed run.
    pub fn record_failure(&mut self, project: &str, stream: &str, error: &str, enabled: bool) {
        let now = unix_now();
        let s = self
            .streams
            .entry(project.to_string())
            .or_default()
            .entry(stream.to_string())
            .or_default();
        s.enabled = enabled;
        s.last_run_at = Some(now);
        s.last_error = Some(error.to_string());
    }
}

fn unix_now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

// ── Binary resolver ───────────────────────────────────────────────────────────

/// Resolve the absolute path to `name` by probing well-known dirs, then PATH.
///
/// launchd and daemon contexts start with a minimal PATH (often just `/usr/bin`).
/// Probing explicit directories ensures Homebrew (`/opt/homebrew/bin`) and
/// `/usr/local/bin` binaries are reachable without relying on the inherited PATH.
pub fn resolve_binary(name: &str) -> Option<std::path::PathBuf> {
    let probe_dirs = [
        std::path::PathBuf::from("/opt/homebrew/bin"),
        std::path::PathBuf::from("/usr/local/bin"),
        std::path::PathBuf::from("/usr/bin"),
        std::path::PathBuf::from("/bin"),
    ];
    for dir in &probe_dirs {
        let p = dir.join(name);
        if p.is_file() {
            return Some(p);
        }
    }
    if let Ok(path_var) = std::env::var("PATH") {
        for dir in std::env::split_paths(&path_var) {
            let p = dir.join(name);
            if p.is_file() {
                return Some(p);
            }
        }
    }
    None
}

// ── Stream result ─────────────────────────────────────────────────────────────

/// Result of a single stream run.
#[derive(Debug, Clone)]
pub struct RunResult {
    /// Number of new reports delivered to the inbox.
    pub delivered: usize,
    /// Number of reports dropped due to `per_run_cap`.
    pub dropped: usize,
}

// ── Stream run functions (shared between daemon and CLI) ──────────────────────

/// Run the rsync stream for one project.
pub fn run_rsync_stream(proj: &NsentryProjectConfig) -> std::result::Result<RunResult, String> {
    use crate::nsentry::{self, NsentryConfig};

    let sentry_server = proj
        .sentry_server
        .as_deref()
        .ok_or_else(|| "rsync stream: sentry_server not configured".to_string())?;

    let cfg = NsentryConfig {
        sentry_server: sentry_server.to_string(),
        remote_dir: proj.remote_dir.clone(),
        inbox: proj.inbox.clone(),
        interval_secs: proj.streams.rsync.interval_secs,
    };

    let report = nsentry::sync(&proj.path, &cfg, false, Some(proj.per_run_cap))
        .map_err(|e| format!("rsync: {e}"))?;

    Ok(RunResult {
        delivered: report.new,
        dropped: report.dropped,
    })
}

/// Build a PATH that includes the well-known bin dirs, so a spawned bridge
/// script's bare-name tools (`gh`, `md5sum`, `awk`, `date`) resolve even under a
/// daemon/launchd context whose inherited PATH is minimal (often just `/usr/bin`).
fn augmented_path() -> String {
    let mut dirs = vec![
        "/opt/homebrew/bin".to_string(),
        "/usr/local/bin".to_string(),
        "/usr/bin".to_string(),
        "/bin".to_string(),
        "/usr/sbin".to_string(),
        "/sbin".to_string(),
    ];
    if let Ok(p) = std::env::var("PATH") {
        for d in p.split(':') {
            if !d.is_empty() && !dirs.iter().any(|x| x == d) {
                dirs.push(d.to_string());
            }
        }
    }
    dirs.join(":")
}

/// Enforce `per_run_cap`: if `wrote_count > cap`, return cap as delivered and
/// (wrote_count - cap) as dropped.
pub fn enforce_cap(proj: &NsentryProjectConfig, _stream: &str, wrote_count: usize) -> RunResult {
    let cap = proj.per_run_cap;
    if wrote_count > cap {
        let dropped = wrote_count - cap;
        RunResult {
            delivered: cap,
            dropped,
        }
    } else {
        RunResult {
            delivered: wrote_count,
            dropped: 0,
        }
    }
}

/// Run the CI script stream for one project.
pub fn run_ci_stream(
    proj: &NsentryProjectConfig,
    scripts_dir: &Path,
) -> std::result::Result<RunResult, String> {
    let bash = resolve_binary("bash")
        .ok_or_else(|| "ci stream: bash not found on PATH or probe dirs".to_string())?;
    let script = scripts_dir.join("gh-ci-failures-to-reports.sh");
    let inbox = proj.resolved_inbox();
    std::fs::create_dir_all(&inbox).map_err(|e| format!("ci stream: create inbox dir: {e}"))?;

    let out = std::process::Command::new(&bash)
        .arg(&script)
        .arg("--org")
        .arg(&proj.org)
        .arg("--out")
        .arg(&inbox)
        .arg("--per-repo")
        .arg(proj.streams.ci.per_repo.to_string())
        .env("GH_ORG", &proj.org)
        .env("PATH", augmented_path())
        .output()
        .map_err(|e| format!("ci stream: exec bash: {e}"))?;

    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        return Err(format!(
            "ci stream: script exited {}: {}",
            out.status,
            stderr.trim()
        ));
    }

    let stdout = String::from_utf8_lossy(&out.stdout);
    let wrote_count = stdout.lines().filter(|l| l.starts_with("WROTE ")).count();
    Ok(enforce_cap(proj, "ci", wrote_count))
}

/// Run the Dependabot script stream for one project.
pub fn run_dependabot_stream(
    proj: &NsentryProjectConfig,
    scripts_dir: &Path,
) -> std::result::Result<RunResult, String> {
    let bash =
        resolve_binary("bash").ok_or_else(|| "dependabot stream: bash not found".to_string())?;
    let script = scripts_dir.join("gh-dependabot-to-reports.sh");
    let inbox = proj.resolved_inbox();
    std::fs::create_dir_all(&inbox)
        .map_err(|e| format!("dependabot stream: create inbox dir: {e}"))?;

    let out = std::process::Command::new(&bash)
        .arg(&script)
        .arg("--org")
        .arg(&proj.org)
        .arg("--out")
        .arg(&inbox)
        .env("GH_ORG", &proj.org)
        .env("PATH", augmented_path())
        .output()
        .map_err(|e| format!("dependabot stream: exec bash: {e}"))?;

    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        return Err(format!(
            "dependabot stream: script exited {}: {}",
            out.status,
            stderr.trim()
        ));
    }

    let stdout = String::from_utf8_lossy(&out.stdout);
    let wrote_count = stdout.lines().filter(|l| l.starts_with("WROTE ")).count();
    Ok(enforce_cap(proj, "dependabot", wrote_count))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn sample_config() -> NsentrySyncConfig {
        NsentrySyncConfig {
            version: 1,
            projects: vec![NsentryProjectConfig {
                name: "nself".to_string(),
                path: PathBuf::from("/some/path/nself"),
                org: "nself-org".to_string(),
                sentry_server: Some("root@sentry.example.test".to_string()),
                remote_dir: default_remote_dir(),
                inbox: None,
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
            }],
        }
    }

    #[test]
    fn yaml_round_trip() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("nsentry-sync.yaml");
        let cfg = sample_config();

        save(&path, &cfg).unwrap();
        assert!(path.is_file(), "config file must exist after save");

        let loaded = load(&path).unwrap();
        assert_eq!(loaded.version, 1);
        assert_eq!(loaded.projects.len(), 1);
        let p = &loaded.projects[0];
        assert_eq!(p.name, "nself");
        assert_eq!(p.org, "nself-org");
        assert_eq!(p.streams.ci.per_repo, 10);
        assert_eq!(p.streams.dependabot.interval_secs, 21600);
        assert!(p.enabled);
    }

    #[test]
    fn load_absent_returns_empty() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("nsentry-sync.yaml");
        let cfg = load(&path).unwrap();
        assert!(
            cfg.projects.is_empty(),
            "absent config must yield empty projects"
        );
    }

    #[test]
    fn toggle_project_enabled_idempotent() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("nsentry-sync.yaml");
        let cfg = sample_config();
        save(&path, &cfg).unwrap();

        // Disable
        let found = toggle_project_enabled(&path, "nself", false).unwrap();
        assert!(found);
        let loaded = load(&path).unwrap();
        assert!(!loaded.projects[0].enabled);

        // Re-enable (idempotent)
        toggle_project_enabled(&path, "nself", true).unwrap();
        let loaded = load(&path).unwrap();
        assert!(loaded.projects[0].enabled);

        // Non-existent project
        let found = toggle_project_enabled(&path, "noexist", false).unwrap();
        assert!(!found);
    }

    #[test]
    fn defaults_are_sane() {
        let dir = TempDir::new().unwrap();
        let path = dir.path().join("nsentry-sync.yaml");
        // Minimal YAML with only required fields — defaults must fill in the rest.
        let minimal =
            "version: 1\nprojects:\n  - name: test\n    path: /tmp/test\n    org: test-org\n";
        std::fs::write(&path, minimal).unwrap();
        let cfg = load(&path).unwrap();
        let p = &cfg.projects[0];
        assert_eq!(p.per_run_cap, 50);
        assert!(p.enabled);
        assert_eq!(p.streams.rsync.interval_secs, 300);
        assert_eq!(p.streams.ci.interval_secs, 900);
        assert_eq!(p.streams.ci.per_repo, 10);
        assert_eq!(p.streams.dependabot.interval_secs, 21600);
    }
}
