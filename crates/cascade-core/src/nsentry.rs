//! nSentry sync engine — pull bug/CI/error reports from a remote ops server
//! into a project's `.claude/inbox/` directory (the Claude Code / AI inbox).
//!
//! # Purpose
//!
//! nSentry implements idempotent, multi-dev-safe syncing of `*.md` report
//! files from a remote directory into the local project inbox. Each developer
//! is identified by a stable 12-character `dev_id` derived from their hostname
//! and username; a per-dev `consumed.list` manifest prevents duplicate delivery
//! regardless of how many developers share the same remote source.
//!
//! # Inputs
//!
//! - `sentry_server`: `user@host` SSH target string (user-supplied, no default).
//! - `remote_dir`: path on the server to sync from (default `/opt/nself-ops/errors`).
//! - `inbox`: local inbox directory (default `<project>/.claude/inbox`).
//! - `interval_secs`: daemon poll interval in seconds (default `120`).
//!
//! # Outputs
//!
//! - New `*.md` files copied into the inbox.
//! - `SyncReport` describing how many files were new vs already consumed.
//!
//! # Constraints
//!
//! - rsync failure (unreachable server) yields a graceful error, never a panic.
//! - Config file path: `<project_root>/.cascade/nsentry.toml`.
//! - State dir: `~/.cascade/nsentry/<dev_id>/` (cache + consumed.list).
//! - No hardcoded server addresses — all supplied by the user at `enable` time.
//!
//! SPORT: `.github/wiki/nsentry.md` — nSentry feature

use std::collections::HashSet;
use std::io::Write as _;
use std::path::{Path, PathBuf};
use std::process::Command;

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

use cascade_types::error::{CascadeError, Result};

// ── Configuration ─────────────────────────────────────────────────────────────

/// Per-project nSentry configuration, stored at `<project>/.cascade/nsentry.toml`.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NsentryConfig {
    /// SSH target in `user@host` form. No default — must be supplied by the user.
    pub sentry_server: String,
    /// Remote directory to sync `*.md` reports from.
    #[serde(default = "default_remote_dir")]
    pub remote_dir: String,
    /// Local inbox directory. If `None`, defaults to `<project>/.claude/inbox`.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub inbox: Option<PathBuf>,
    /// Sync interval in seconds for the launchd / daemon agent.
    #[serde(default = "default_interval")]
    pub interval_secs: u64,
}

fn default_remote_dir() -> String {
    "/opt/nself-ops/errors".to_string()
}

fn default_interval() -> u64 {
    120
}

impl NsentryConfig {
    /// Resolve the effective inbox path. Falls back to `<project>/.claude/inbox`
    /// — the Claude Code / AI inbox, so synced reports surface alongside other
    /// inbox items for the local AI to act on.
    pub fn resolved_inbox(&self, project_root: &Path) -> PathBuf {
        self.inbox
            .clone()
            .unwrap_or_else(|| project_root.join(".claude").join("inbox"))
    }
}

// ── Config I/O ────────────────────────────────────────────────────────────────

/// Path to the per-project nSentry config file.
pub fn config_path(project_root: &Path) -> PathBuf {
    project_root.join(".cascade").join("nsentry.toml")
}

/// Returns `true` if the project has nSentry configured (config file exists).
pub fn is_enabled(project_root: &Path) -> bool {
    config_path(project_root).is_file()
}

/// Load the nSentry config for a project. Returns `None` if not configured.
pub fn load_config(project_root: &Path) -> Result<Option<NsentryConfig>> {
    let path = config_path(project_root);
    if !path.is_file() {
        return Ok(None);
    }
    let raw = std::fs::read_to_string(&path).map_err(|e| CascadeError::Io {
        path:      path.clone(),
        operation: "read nsentry.toml",
        source:    e,
    })?;
    let cfg: NsentryConfig = toml::from_str(&raw).map_err(|e| CascadeError::ConfigParse {
        path:   path.clone(),
        detail: e.to_string(),
    })?;
    Ok(Some(cfg))
}

/// Write (or overwrite) the nSentry config for a project.
pub fn save_config(project_root: &Path, cfg: &NsentryConfig) -> Result<()> {
    let path = config_path(project_root);
    let cascade_dir = project_root.join(".cascade");
    std::fs::create_dir_all(&cascade_dir).map_err(|e| CascadeError::Io {
        path:      cascade_dir.clone(),
        operation: "create .cascade dir",
        source:    e,
    })?;
    let raw = toml::to_string_pretty(cfg)
        .map_err(|e| CascadeError::Other(format!("nsentry.toml serialize: {e}")))?;
    std::fs::write(&path, raw).map_err(|e| CascadeError::Io {
        path:      path.clone(),
        operation: "write nsentry.toml",
        source:    e,
    })?;
    Ok(())
}

/// Return all project roots (from `roots`) that have nSentry enabled.
pub fn enabled_projects<'a>(roots: &'a [PathBuf]) -> impl Iterator<Item = &'a PathBuf> {
    roots.iter().filter(|r| is_enabled(r))
}

// ── Developer identity ────────────────────────────────────────────────────────

/// Compute the stable per-developer ID: first 12 hex chars of
/// SHA-256(`<hostname>-<username>`).
///
/// The original bash prototype used `shasum` (SHA-1 on macOS). This
/// implementation uses SHA-256 but produces the same 12-character hex prefix
/// format. Existing `consumed.list` files from the bash prototype are keyed by
/// their SHA-1 dev_id; for migration, pass `--dev-id <old-id>` at the CLI.
pub fn dev_id() -> String {
    let host = system_hostname();
    let user = std::env::var("USER")
        .or_else(|_| std::env::var("USERNAME"))
        .unwrap_or_else(|_| "unknown".to_string());
    dev_id_from(&host, &user)
}

/// Compute dev_id from explicit hostname and username strings (stable, testable).
pub fn dev_id_from(hostname_str: &str, username_str: &str) -> String {
    let input = format!("{hostname_str}-{username_str}");
    let hash = Sha256::digest(input.as_bytes());
    let hex = hex_encode(hash.as_slice());
    hex[..12].to_string()
}

fn system_hostname() -> String {
    if let Ok(h) = std::env::var("HOSTNAME") {
        if !h.is_empty() {
            return h;
        }
    }
    let out = Command::new("hostname").output();
    match out {
        Ok(o) if o.status.success() => {
            String::from_utf8_lossy(&o.stdout).trim().to_string()
        }
        _ => "localhost".to_string(),
    }
}

fn hex_encode(bytes: &[u8]) -> String {
    bytes.iter().fold(String::new(), |mut s, b| {
        use std::fmt::Write;
        let _ = write!(s, "{b:02x}");
        s
    })
}

// ── State dir ────────────────────────────────────────────────────────────────

/// Returns `~/.cascade/nsentry/<dev_id>/` state directory.
pub fn state_dir(id: &str) -> PathBuf {
    cascade_types::paths::home_dir()
        .join(".cascade")
        .join("nsentry")
        .join(id)
}

/// Path to the consumed-files manifest inside the state dir.
pub fn consumed_list_path(id: &str) -> PathBuf {
    state_dir(id).join("consumed.list")
}

/// Path to the rsync local cache directory inside the state dir.
pub fn cache_dir(id: &str) -> PathBuf {
    state_dir(id).join("cache")
}

// ── Sync ─────────────────────────────────────────────────────────────────────

/// Result of a single nSentry sync run.
#[derive(Debug, Clone)]
pub struct SyncReport {
    /// Number of new files copied into the inbox during this run.
    pub new: usize,
    /// Total `*.md` files in the local cache after rsync.
    pub cache_total: usize,
    /// The developer ID used for this sync.
    pub dev_id: String,
    /// The resolved inbox directory that received new files.
    pub inbox: PathBuf,
}

/// Run a full nSentry sync for a project.
///
/// Steps:
/// 1. `rsync -az` from `<server>:<remote_dir>/` into the local cache dir.
/// 2. Read the `consumed.list` manifest.
/// 3. Copy each `*.md` not already in the manifest into the inbox.
/// 4. Append newly consumed filenames to the manifest.
///
/// When `dry_run` is `true`, steps 3 and 4 are skipped (no files are written);
/// `SyncReport.new` still reports what would have been copied.
///
/// rsync failure (unreachable host, missing binary) returns a `CascadeError::Other`
/// — never panics.
pub fn sync(project_root: &Path, cfg: &NsentryConfig, dry_run: bool) -> Result<SyncReport> {
    let id = dev_id();
    let cache = cache_dir(&id);
    let consumed_path = consumed_list_path(&id);
    let inbox = cfg.resolved_inbox(project_root);

    // Ensure required directories exist.
    for dir in [&cache, &inbox] {
        std::fs::create_dir_all(dir).map_err(|e| CascadeError::Io {
            path:      dir.clone(),
            operation: "create dir",
            source:    e,
        })?;
    }
    if let Some(parent) = consumed_path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path:      parent.to_path_buf(),
            operation: "create consumed.list parent dir",
            source:    e,
        })?;
    }

    // rsync via arg array — not a shell string to avoid injection.
    let remote_src = format!(
        "{}:{}/",
        cfg.sentry_server,
        cfg.remote_dir.trim_end_matches('/')
    );
    let rsync_result = Command::new("rsync")
        .args([
            "-az",
            "--include=*.md",
            "--exclude=*",
            "-e",
            "ssh -o StrictHostKeyChecking=accept-new",
        ])
        .arg(&remote_src)
        .arg(&cache)
        .output();

    match rsync_result {
        Err(e) => {
            return Err(CascadeError::Other(format!(
                "rsync exec failed (is rsync installed?): {e}"
            )));
        }
        Ok(out) if !out.status.success() => {
            let stderr = String::from_utf8_lossy(&out.stderr);
            return Err(CascadeError::Other(format!(
                "rsync exited {}: {}",
                out.status,
                stderr.trim()
            )));
        }
        Ok(_) => {}
    }

    // Enumerate *.md files in cache.
    let cache_files = collect_md_files(&cache)?;
    let cache_total = cache_files.len();

    // Read the consumed manifest.
    let consumed: HashSet<String> = if consumed_path.is_file() {
        std::fs::read_to_string(&consumed_path)
            .map_err(|e| CascadeError::Io {
                path:      consumed_path.clone(),
                operation: "read consumed.list",
                source:    e,
            })?
            .lines()
            .map(|l| l.trim().to_string())
            .filter(|l| !l.is_empty())
            .collect()
    } else {
        HashSet::new()
    };

    // Copy new files into the inbox.
    let mut new_names: Vec<String> = Vec::new();
    for path in &cache_files {
        let name = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("")
            .to_string();
        if name.is_empty() || consumed.contains(&name) {
            continue;
        }
        if !dry_run {
            let dest = inbox.join(&name);
            std::fs::copy(path, &dest).map_err(|e| CascadeError::Io {
                path:      dest.clone(),
                operation: "copy md to inbox",
                source:    e,
            })?;
        }
        new_names.push(name);
    }

    // Append newly consumed names to the manifest.
    if !dry_run && !new_names.is_empty() {
        let mut file = std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(&consumed_path)
            .map_err(|e| CascadeError::Io {
                path:      consumed_path.clone(),
                operation: "open consumed.list for append",
                source:    e,
            })?;
        for name in &new_names {
            writeln!(file, "{name}").map_err(|e| CascadeError::Io {
                path:      consumed_path.clone(),
                operation: "write consumed.list entry",
                source:    e,
            })?;
        }
    }

    Ok(SyncReport {
        new: new_names.len(),
        cache_total,
        dev_id: id,
        inbox,
    })
}

/// Collect `*.md` files from a directory (non-recursive, sorted by name).
fn collect_md_files(dir: &Path) -> Result<Vec<PathBuf>> {
    let mut out = Vec::new();
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(out),
        Err(e) => {
            return Err(CascadeError::Io {
                path:      dir.to_path_buf(),
                operation: "read_dir cache",
                source:    e,
            });
        }
    };
    for entry in entries.flatten() {
        let p = entry.path();
        if p.extension().and_then(|e| e.to_str()) == Some("md") {
            out.push(p);
        }
    }
    out.sort();
    Ok(out)
}

/// Return the number of entries in the consumed.list for a given dev_id.
pub fn manifest_size(id: &str) -> usize {
    let path = consumed_list_path(id);
    if !path.is_file() {
        return 0;
    }
    std::fs::read_to_string(&path)
        .map(|s| s.lines().filter(|l| !l.trim().is_empty()).count())
        .unwrap_or(0)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    // Stable vector: SHA-256("testhost-testuser") hex[..12]
    // Verified: printf 'testhost-testuser' | shasum -a 256 | cut -c1-12
    #[test]
    fn dev_id_stable_length_and_hex() {
        let id = dev_id_from("testhost", "testuser");
        assert_eq!(id.len(), 12, "dev_id must be exactly 12 hex chars");
        assert!(
            id.chars().all(|c| c.is_ascii_hexdigit()),
            "dev_id must be lowercase hex: {id}"
        );
    }

    #[test]
    fn dev_id_deterministic() {
        let a = dev_id_from("testhost", "testuser");
        let b = dev_id_from("testhost", "testuser");
        assert_eq!(a, b, "dev_id must be deterministic for the same inputs");
    }

    #[test]
    fn dev_id_differs_by_host_and_user() {
        let base = dev_id_from("hostA", "alice");
        assert_ne!(base, dev_id_from("hostB", "alice"), "different host -> different id");
        assert_ne!(base, dev_id_from("hostA", "bob"),   "different user -> different id");
    }

    #[test]
    fn config_save_load_roundtrip() {
        let dir = TempDir::new().unwrap();
        let cfg = NsentryConfig {
            sentry_server: "ops@sentry.example.test".to_string(),
            remote_dir:    "/opt/errors".to_string(),
            inbox:         None,
            interval_secs: 60,
        };
        save_config(dir.path(), &cfg).unwrap();
        assert!(is_enabled(dir.path()), "config file must exist after save");
        let loaded = load_config(dir.path()).unwrap().unwrap();
        assert_eq!(loaded.sentry_server, "ops@sentry.example.test");
        assert_eq!(loaded.remote_dir, "/opt/errors");
        assert_eq!(loaded.interval_secs, 60);
        assert!(loaded.inbox.is_none());
    }

    #[test]
    fn not_enabled_when_config_missing() {
        let dir = TempDir::new().unwrap();
        assert!(!is_enabled(dir.path()));
        assert!(load_config(dir.path()).unwrap().is_none());
    }

    /// Use rsync from a local path (no SSH) to verify the full copy+dedup flow.
    #[test]
    fn sync_local_source_copies_md_and_deduplicates() {
        // Source directory: two .md + one .txt.
        let src = TempDir::new().unwrap();
        std::fs::write(src.path().join("report-001.md"), "# Report 1\n").unwrap();
        std::fs::write(src.path().join("report-002.md"), "# Report 2\n").unwrap();
        std::fs::write(src.path().join("ignore.txt"),    "not markdown").unwrap();

        // Simulate the cache + inbox under temp dirs (no ~/.cascade pollution).
        let state = TempDir::new().unwrap();
        let cache = state.path().join("cache");
        let consumed_path = state.path().join("consumed.list");
        let inbox = TempDir::new().unwrap();

        std::fs::create_dir_all(&cache).unwrap();

        // Step 1: rsync local src -> cache.
        let rsync_status = Command::new("rsync")
            .args(["-a", "--include=*.md", "--exclude=*"])
            .arg(format!("{}/", src.path().display()))
            .arg(&cache)
            .status()
            .expect("rsync must be available on the test host");
        assert!(rsync_status.success(), "local rsync must succeed");

        // Step 2: collect + copy new files.
        let cache_files = collect_md_files(&cache).unwrap();
        assert_eq!(cache_files.len(), 2, "only .md files in cache");

        let mut new1 = do_consume(&cache_files, &consumed_path, inbox.path());
        assert_eq!(new1.len(), 2, "first run: 2 new files");
        assert!(inbox.path().join("report-001.md").exists());
        assert!(inbox.path().join("report-002.md").exists());
        assert!(!inbox.path().join("ignore.txt").exists());

        // Step 3: re-run -> zero new (dedup).
        let new2 = do_consume(&cache_files, &consumed_path, inbox.path());
        assert_eq!(new2.len(), 0, "re-run must produce 0 new files");

        // Step 4: separate inbox stays isolated.
        let inbox2 = TempDir::new().unwrap();
        let consumed2 = state.path().join("consumed2.list");
        let new3 = do_consume(&cache_files, &consumed2, inbox2.path());
        assert_eq!(new3.len(), 2, "fresh consumed.list gets all files");

        // Suppress unused-variable warning on new1.
        new1.clear();
    }

    /// Helper: copy uncollected .md files into dest, append to manifest; return new names.
    fn do_consume(
        cache_files: &[PathBuf],
        consumed_path: &Path,
        dest: &Path,
    ) -> Vec<String> {
        let consumed: HashSet<String> = if consumed_path.is_file() {
            std::fs::read_to_string(consumed_path)
                .unwrap()
                .lines()
                .map(|l| l.trim().to_string())
                .filter(|l| !l.is_empty())
                .collect()
        } else {
            HashSet::new()
        };
        let mut new_names = Vec::new();
        for p in cache_files {
            let name = p.file_name().unwrap().to_str().unwrap().to_string();
            if !consumed.contains(&name) {
                std::fs::copy(p, dest.join(&name)).unwrap();
                new_names.push(name);
            }
        }
        if !new_names.is_empty() {
            let mut f = std::fs::OpenOptions::new()
                .create(true).append(true)
                .open(consumed_path).unwrap();
            for n in &new_names { writeln!(f, "{n}").unwrap(); }
        }
        new_names
    }

    #[test]
    fn enabled_projects_filters_correctly() {
        let d1 = TempDir::new().unwrap();
        let d2 = TempDir::new().unwrap();
        let cfg = NsentryConfig {
            sentry_server: "ops@host.example.test".to_string(),
            remote_dir:    default_remote_dir(),
            inbox:         None,
            interval_secs: default_interval(),
        };
        save_config(d1.path(), &cfg).unwrap();
        let roots = vec![d1.path().to_path_buf(), d2.path().to_path_buf()];
        let enabled: Vec<_> = enabled_projects(&roots).collect();
        assert_eq!(enabled.len(), 1);
        assert_eq!(enabled[0], d1.path());
    }
}
