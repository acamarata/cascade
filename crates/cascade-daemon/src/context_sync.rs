//! # context_sync — E2-S3 POST-processing background sync
//!
//! ## Purpose
//!
//! After a chat response has been fully delivered, extract a structured
//! [`ResponseDigest`] (decisions / files touched / completed tasks / taxonomy
//! tags) from the assistant text and append it to
//! `~/.cascade/context-sync/<yyyy-mm-dd>.jsonl`. The RAG watcher
//! ([`crate::rag_watcher`]) watches that directory, so the JSONL write ITSELF
//! is the index-refresh nudge — no extra IPC is needed or invented.
//!
//! ## Hot-path contract (hard)
//!
//! - [`spawn_context_sync`] is called AFTER the final SSE sentinel is sent.
//! - Flag off ([`should_sync`] == false) → literally no spawn, no allocation,
//!   zero overhead. The caller also skips response-text accumulation.
//! - Everything blocking (taxonomy GFP curl, file I/O) runs inside
//!   `spawn_blocking` on a detached task; failures are logged at debug/warn
//!   and never surface to the chat client.
//!
//! ## Permissions
//!
//! Same pattern as the rest of `~/.cascade`: directory `0o700`, files
//! `0o600` (owner-only) — set at create time via `OpenOptionsExt::mode` and
//! re-tightened best-effort if a file pre-existed with looser permissions
//! (mirrors `key_loader::tighten_providers_json_perms`).
//!
//! ## SPORT
//!
//! MASTER-DAEMON.md — cascade-daemon: context_sync (E2-S3)

use std::io::Write;
use std::path::{Path, PathBuf};

use cascade_core::middleware::{extract_digest_taxonomy, to_jsonl_line, ResponseDigest};
use chrono::{DateTime, Utc};
use tracing::{debug, warn};

use crate::config::MiddlewareConfig;

/// Directory name under `~/.cascade` holding the daily digest JSONL files.
pub const CONTEXT_SYNC_DIR_NAME: &str = "context-sync";

// ── Guard (unit-testable — the flags-off no-spawn proof) ──────────────────────

/// The spawn guard: `true` only when `middleware.context_sync` is on.
///
/// Callers MUST check this before accumulating response text or spawning —
/// flag off means zero overhead on the chat path.
// Called from http::chat_handlers — a lib-target-only module (the bin target
// has no `http` mod), so the bin tree sees this as dead code.
#[allow(dead_code)]
pub fn should_sync(flags: &MiddlewareConfig) -> bool {
    flags.context_sync
}

// ── Paths + permissions ────────────────────────────────────────────────────────

/// `<config_dir>/context-sync` — the digest directory.
pub fn context_sync_dir(config_dir: &Path) -> PathBuf {
    config_dir.join(CONTEXT_SYNC_DIR_NAME)
}

/// Create the context-sync directory (idempotent) and tighten it to `0o700`.
///
/// Also called from the supervisor at boot so the directory exists when the
/// RAG watcher registers its watch set (the watcher only registers dirs that
/// exist at start). An empty dir is the entire boot cost of this feature.
pub fn ensure_context_sync_dir(config_dir: &Path) -> std::io::Result<PathBuf> {
    let dir = context_sync_dir(config_dir);
    std::fs::create_dir_all(&dir)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let _ = std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o700));
    }
    Ok(dir)
}

// ── JSONL append ───────────────────────────────────────────────────────────────

/// Append one digest entry to `<dir>/<yyyy-mm-dd>.jsonl` and return the path.
///
/// Atomic append: the file is opened with `O_APPEND` and the whole line
/// (JSON + trailing newline) goes through ONE `write_all`, so concurrent
/// appenders never interleave partial lines. Created files get mode `0o600`;
/// pre-existing files are re-tightened best-effort (key_loader pattern).
// Reached via the lib-target chat path (http::chat_handlers → spawn) only.
#[allow(dead_code)]
pub fn append_digest(
    dir: &Path,
    digest: &ResponseDigest,
    now: DateTime<Utc>,
) -> std::io::Result<PathBuf> {
    let path = dir.join(format!("{}.jsonl", now.format("%Y-%m-%d")));

    let mut opts = std::fs::OpenOptions::new();
    opts.create(true).append(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        opts.mode(0o600);
    }
    let mut file = opts.open(&path)?;

    // Best-effort tighten for files created before this policy (mode() only
    // applies at create time). Mirrors key_loader::tighten_providers_json_perms.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        if let Ok(meta) = file.metadata() {
            if meta.permissions().mode() & 0o777 != 0o600 {
                let _ = std::fs::set_permissions(
                    &path,
                    std::fs::Permissions::from_mode(0o600),
                );
            }
        }
    }

    let mut line = to_jsonl_line(digest, &now.to_rfc3339());
    line.push('\n');
    file.write_all(line.as_bytes())?;
    Ok(path)
}

// ── Path-existence gate for digest extraction ──────────────────────────────────

/// Production existence check for path-like tokens in a response.
///
/// The chat surface carries no per-request project root, so the daemon checks
/// absolute tokens as-is and relative tokens against the process CWD and the
/// user's home directory. Tokens that resolve nowhere (URL fragments, prose
/// like "and/or") are rejected — the closure is the only existence authority.
// Reached via the lib-target chat path (http::chat_handlers → spawn) only.
#[allow(dead_code)]
fn production_path_exists(token: &str) -> bool {
    let p = Path::new(token);
    if p.is_absolute() {
        return p.exists();
    }
    if let Ok(cwd) = std::env::current_dir() {
        if cwd.join(p).exists() {
            return true;
        }
    }
    if let Some(home) = dirs::home_dir() {
        if home.join(p).exists() {
            return true;
        }
    }
    false
}

// ── Background task ────────────────────────────────────────────────────────────

/// Spawn the E2-S3 background context-sync task for one delivered response.
///
/// MUST be called only AFTER the response has been fully sent to the client.
/// Flag off or blank response → returns immediately WITHOUT spawning (the
/// zero-overhead guarantee). The spawned work is detached: taxonomy + file
/// I/O run in `spawn_blocking`; every failure is swallowed into logs.
// Called from http::chat_handlers — lib target only (bin has no `http` mod).
#[allow(dead_code)]
pub fn spawn_context_sync(flags: MiddlewareConfig, response_text: String) {
    if !should_sync(&flags) || response_text.trim().is_empty() {
        return;
    }
    tokio::spawn(async move {
        // extract_digest_taxonomy may block on the bounded GFP curl call and
        // the append does file I/O — both belong off the async runtime.
        let joined = tokio::task::spawn_blocking(move || run_sync(&response_text)).await;
        if let Err(e) = joined {
            warn!(error = %e, "context_sync: background task panicked/cancelled");
        }
    });
}

/// Blocking body of the sync task: digest → ensure dir → append.
///
/// The JSONL append into `~/.cascade/context-sync/` IS the rag_watcher nudge:
/// the watcher watches that directory (see `rag_watcher::WatcherConfig`), so
/// the debounced FS event triggers ingest/index refresh with no extra IPC.
// Reached via the lib-target chat path (http::chat_handlers → spawn) only.
#[allow(dead_code)]
fn run_sync(response_text: &str) {
    let Some(home) = dirs::home_dir() else {
        debug!("context_sync: no home dir — skipping");
        return;
    };
    let config_dir = home.join(".cascade");

    // Taxonomy hook degrades to rule-based inside cascade-core when GFP is
    // unavailable — GP is never a hard dependency for post-processing.
    let digest = extract_digest_taxonomy(response_text, production_path_exists);
    if digest.is_empty() {
        debug!("context_sync: empty digest — nothing to append");
        return;
    }

    let dir = match ensure_context_sync_dir(&config_dir) {
        Ok(d) => d,
        Err(e) => {
            warn!(error = %e, "context_sync: cannot create context-sync dir");
            return;
        }
    };
    match append_digest(&dir, &digest, Utc::now()) {
        Ok(path) => debug!(
            path = %path.display(),
            decisions = digest.decisions.len(),
            files = digest.files_touched.len(),
            tasks = digest.completed_tasks.len(),
            "context_sync: digest appended (rag_watcher will pick it up)"
        ),
        Err(e) => warn!(error = %e, "context_sync: append failed"),
    }
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    fn digest_fixture() -> ResponseDigest {
        ResponseDigest {
            decisions: vec!["decided to use sqlite".into()],
            files_touched: vec!["src/lib.rs".into()],
            completed_tasks: vec!["wire the store".into()],
            tags: vec!["decision".into()],
        }
    }

    // ── Guard: flags-off = no spawn ───────────────────────────────────────────

    /// The flags-off no-spawn proof: default flags fail the guard, so
    /// `spawn_context_sync` returns before any tokio::spawn.
    #[test]
    fn guard_default_flags_off() {
        assert!(!should_sync(&MiddlewareConfig::default()));
    }

    #[test]
    fn guard_context_sync_flag_on() {
        let flags = MiddlewareConfig {
            context_sync: true,
            ..Default::default()
        };
        assert!(should_sync(&flags));
        // Other flags never enable the post-sync guard.
        let pre_only = MiddlewareConfig {
            compress_context: true,
            inject_context: true,
            classify_requests: true,
            ..Default::default()
        };
        assert!(!should_sync(&pre_only));
    }

    /// spawn_context_sync with the flag off must not require a tokio runtime:
    /// proof that it returns before ever reaching tokio::spawn (which would
    /// panic outside a runtime).
    #[test]
    fn spawn_flag_off_never_spawns() {
        spawn_context_sync(MiddlewareConfig::default(), "decided to ship".into());
        // Blank text with the flag ON also never spawns.
        let flags = MiddlewareConfig {
            context_sync: true,
            ..Default::default()
        };
        spawn_context_sync(flags, "   \n".into());
    }

    // ── Dir + file creation with tight permissions ────────────────────────────

    #[test]
    fn ensure_dir_creates_0700() {
        let tmp = TempDir::new().unwrap();
        let dir = ensure_context_sync_dir(tmp.path()).unwrap();
        assert!(dir.is_dir());
        assert!(dir.ends_with(CONTEXT_SYNC_DIR_NAME));
        // Idempotent.
        let again = ensure_context_sync_dir(tmp.path()).unwrap();
        assert_eq!(dir, again);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&dir).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o700, "dir mode must be 0700, got 0o{mode:o}");
        }
    }

    #[test]
    fn append_writes_dated_jsonl_0600() {
        let tmp = TempDir::new().unwrap();
        let dir = ensure_context_sync_dir(tmp.path()).unwrap();
        let now = Utc::now();

        let path = append_digest(&dir, &digest_fixture(), now).unwrap();
        assert_eq!(
            path.file_name().and_then(|n| n.to_str()),
            Some(format!("{}.jsonl", now.format("%Y-%m-%d")).as_str())
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(&path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o600, "file mode must be 0600, got 0o{mode:o}");
        }

        // Two appends → two parseable lines with the expected fields.
        append_digest(&dir, &digest_fixture(), now).unwrap();
        let content = std::fs::read_to_string(&path).unwrap();
        let lines: Vec<&str> = content.lines().collect();
        assert_eq!(lines.len(), 2);
        for line in lines {
            let v: serde_json::Value = serde_json::from_str(line).expect("valid JSONL");
            assert_eq!(v["decisions"][0], "decided to use sqlite");
            assert_eq!(v["files_touched"][0], "src/lib.rs");
            assert!(v["ts"].as_str().unwrap().starts_with("20"));
        }
    }

    /// A pre-existing loose-permission file is re-tightened on append.
    #[cfg(unix)]
    #[test]
    fn append_retightens_loose_existing_file() {
        use std::os::unix::fs::PermissionsExt;
        let tmp = TempDir::new().unwrap();
        let dir = ensure_context_sync_dir(tmp.path()).unwrap();
        let now = Utc::now();
        let path = dir.join(format!("{}.jsonl", now.format("%Y-%m-%d")));
        std::fs::write(&path, "").unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();

        append_digest(&dir, &digest_fixture(), now).unwrap();
        let mode = std::fs::metadata(&path).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode, 0o600, "loose file must be re-tightened to 0600");
    }
}
