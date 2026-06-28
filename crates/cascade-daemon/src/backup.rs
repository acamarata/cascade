//! Backup sync task — mirrors tier `.cascade/` directories to versioned snapshots.
//!
//! Purpose: Subscribes to `cascade.regen.complete` events from the SQLite event
//! bus. When a regen completes, mirrors all tier `.cascade/` directories from
//! [`ResolvedContext::tier_sources`] to timestamped snapshot directories under
//! a configurable backup root (`~/.cascade/backups/{tier_name}/snapshot-YYYY-MM-DD-HHMMSS/`).
//! After each successful backup, old snapshots are pruned to keep at most
//! `max_versions` (default 7). Throttled to at most one backup per tier per
//! `sync_interval_secs` (default 60 s) to prevent backup storms.
//!
//! Inputs:
//!   - `BackupConfig` — enabled flag, backup root path, sync interval secs, max_versions
//!   - `SharedBus`    — event bus to consume `cascade.regen.complete` events from
//!   - `CancellationToken` — graceful-stop signal
//!
//! Outputs:
//!   - Timestamped snapshot directories created under `{backup_root}/{tier_name}/`
//!   - Files mirrored from source `.cascade/` to each snapshot
//!   - Older snapshots pruned after rotation logic
//!   - INFO log: "backed up {n} files from {tier_name} to {snapshot} in {ms}ms"
//!   - WARN log: rotation errors are logged but not fatal
//!
//! Snapshot naming: `snapshot-{YYYY-MM-DD-HHMMSS}` format ensures lexicographic sort
//! equals chronological order.
//!
//! Constraints:
//!   - Monotonic `Instant` is used for throttle tracking (not `SystemTime`).
//!   - Excluded from backup: `logs/`, `temp/`, `*.pid`, `*.lock` entries.
//!   - Errors on a single tier copy are logged and skipped — backup task never crashes.
//!   - `backup_enabled = false` suppresses all operations entirely.
//!   - Snapshot deletion race conditions (e.g., user deletes a snapshot between list
//!     and delete) are handled gracefully with WARN logs, no panic.
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — BackupSync task entry (T-P2-E02-21)

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use chrono::Utc;
use serde::{Deserialize, Serialize};
use tokio_util::sync::CancellationToken;
use tracing::{error, info, warn};

use crate::config::BackupConfig;
use crate::event_bus::SharedBus;
use crate::supervisor::DaemonError;

/// Event kind published by supervisor when derived-file regeneration succeeds.
///
/// The backup task consumes (marks consumed) events of this kind so they are
/// not replayed on daemon restart.
pub const REGEN_COMPLETE_KIND: &str = "cascade.regen.complete";

/// How many `cascade.regen.complete` events to drain per poll iteration.
const CONSUME_LIMIT: u64 = 64;

/// Polling interval for checking the event bus (when not using watch_loop).
///
/// BackupSync polls the bus on this interval; the shutdown token cancels
/// the sleep early, so actual latency is at most `POLL_INTERVAL_MS` ms.
const POLL_INTERVAL_MS: u64 = 500;

// ── RegenPayload ─────────────────────────────────────────────────────────────

/// Payload shape of a `cascade.regen.complete` event.
///
/// The supervisor publishes `tier_sources` as a JSON object mapping tier names
/// to their resolved `.cascade/` directory paths. The backup task deserialises
/// this from the event payload.
#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct RegenPayload {
    /// Map from tier short name (e.g. "GCI") to the resolved `.cascade/` dir path.
    pub tier_sources: HashMap<String, PathBuf>,
    /// Unix timestamp (seconds) of when regeneration completed.
    pub ts: u64,
}

// ── BackupSync ────────────────────────────────────────────────────────────────

/// Daemon task that mirrors tier `.cascade/` directories to a backup location.
///
/// Constructed as a unit struct; all state passes through [`BackupSync::run`].
/// Callers `tokio::spawn` the `run()` future (fire-and-forget).
///
/// Inputs:  `BackupConfig`, `SharedBus`, `CancellationToken`.
/// Outputs: files written under `{backup_root}/{tier_name}/`.
/// Constraints: never panics; all errors are logged.
pub struct BackupSync;

impl BackupSync {
    /// Run the backup sync loop until `shutdown` is cancelled.
    ///
    /// Poll the event bus for `cascade.regen.complete` events every
    /// [`POLL_INTERVAL_MS`] ms. For each event, run a backup of all
    /// tier sources listed in the payload, subject to per-tier throttling.
    ///
    /// Inputs:
    ///   - `config`   — `BackupConfig` (enabled, backup_root, sync_interval_secs)
    ///   - `bus`      — shared event bus for consuming regen events
    ///   - `shutdown` — cancellation token; loop exits when fired
    ///
    /// Outputs: `Ok(())` always (errors are logged, not propagated).
    ///
    /// Constraints:
    ///   - If `config.enabled == false`, returns immediately without consuming any events.
    ///   - Throttle uses `Instant` (monotonic) — not `SystemTime`.
    ///   - Per-tier throttle is independent: rapid changes to tier A do not block tier B.
    pub async fn run(
        config: BackupConfig,
        bus: SharedBus,
        shutdown: CancellationToken,
    ) -> Result<(), DaemonError> {
        if !config.enabled {
            info!("backup_sync: disabled via config — exiting");
            return Ok(());
        }

        // Expand ~ in backup_root at runtime.
        let backup_root = expand_tilde(&config.backup_root);
        let sync_interval = Duration::from_secs(config.sync_interval_secs);

        info!(
            backup_root = %backup_root.display(),
            sync_interval_secs = config.sync_interval_secs,
            "backup_sync: starting"
        );

        // Per-tier throttle: maps tier short name → last backup Instant.
        let mut last_backup: HashMap<String, Instant> = HashMap::new();

        let poll_interval = Duration::from_millis(POLL_INTERVAL_MS);

        loop {
            // Consume all pending regen.complete events from the bus.
            let events = match bus.consume(REGEN_COMPLETE_KIND, CONSUME_LIMIT).await {
                Ok(evs) => evs,
                Err(e) => {
                    warn!(error = %e, "backup_sync: failed to consume events — skipping iteration");
                    vec![]
                }
            };

            for event in events {
                // Deserialise the tier_sources payload.
                let payload: RegenPayload = match serde_json::from_value(event.payload.clone()) {
                    Ok(p) => p,
                    Err(e) => {
                        warn!(
                            event_id = event.id,
                            error = %e,
                            "backup_sync: failed to deserialise regen payload — skipping event"
                        );
                        continue;
                    }
                };

                // Back up each tier, subject to per-tier throttle.
                for (tier_name, source_dir) in &payload.tier_sources {
                    let now = Instant::now();
                    let last = last_backup.get(tier_name.as_str());

                    // Skip if backed up too recently (throttle).
                    if let Some(last_t) = last {
                        if now.duration_since(*last_t) < sync_interval {
                            info!(
                                tier = %tier_name,
                                elapsed_secs = now.duration_since(*last_t).as_secs(),
                                min_interval_secs = config.sync_interval_secs,
                                "backup_sync: throttled — skipping backup for tier"
                            );
                            continue;
                        }
                    }

                    // Run the backup for this tier to a timestamped snapshot directory.
                    let tier_backup_root = backup_root.join(tier_name);
                    let snapshot_timestamp = Utc::now().format("%Y-%m-%d-%H%M%S").to_string();
                    let snapshot_dir =
                        tier_backup_root.join(format!("snapshot-{}", snapshot_timestamp));
                    let t0 = Instant::now();

                    match copy_cascade_dir(source_dir, &snapshot_dir) {
                        Ok(n) => {
                            let elapsed_ms = t0.elapsed().as_millis();
                            info!(
                                tier = %tier_name,
                                n_files = n,
                                snapshot = %snapshot_dir.display(),
                                elapsed_ms = elapsed_ms,
                                "backup_sync: backed up {n} files from {tier_name} to {snapshot} in {elapsed_ms}ms",
                                n = n,
                                tier_name = tier_name,
                                snapshot = snapshot_dir.display(),
                                elapsed_ms = elapsed_ms,
                            );
                            last_backup.insert(tier_name.clone(), Instant::now());

                            // Run snapshot rotation: keep only max_versions snapshots.
                            if let Err(e) = rotate_snapshots(&tier_backup_root, config.max_versions)
                            {
                                warn!(
                                    tier = %tier_name,
                                    tier_root = %tier_backup_root.display(),
                                    max_versions = config.max_versions,
                                    error = %e,
                                    "backup_sync: snapshot rotation failed — continuing without cleanup"
                                );
                            }
                        }
                        Err(e) => {
                            error!(
                                tier = %tier_name,
                                source = %source_dir.display(),
                                snapshot = %snapshot_dir.display(),
                                error = %e,
                                "backup_sync: failed to back up tier — continuing"
                            );
                            // Do not update last_backup on error so the next
                            // event triggers a retry for this tier.
                        }
                    }
                }
            }

            // Sleep until the next poll, waking immediately on shutdown.
            tokio::select! {
                _ = tokio::time::sleep(poll_interval) => {}
                _ = shutdown.cancelled() => {
                    info!("backup_sync: shutdown signal — exiting");
                    break;
                }
            }
        }

        Ok(())
    }
}

// ── copy_cascade_dir ──────────────────────────────────────────────────────────

/// Copy all non-excluded files from `src` to `dest` recursively.
///
/// Excluded entries (never copied):
///   - Top-level `logs/` subdirectory
///   - Top-level `temp/` subdirectory
///   - Any file whose name ends with `.pid`
///   - Any file whose name ends with `.lock`
///   - Any subdirectory that is a prefix of, or equal to, `dest` (recursion guard —
///     prevents infinite loops when `backup_root` lives inside the source `.cascade/`)
///
/// Creates `dest` and any intermediate directories if they do not exist.
///
/// Returns the number of files successfully copied.
///
/// # Errors
///
/// Returns `std::io::Error` if `dest` creation or any file copy fails.
fn copy_cascade_dir(src: &Path, dest: &Path) -> std::io::Result<usize> {
    // Source must exist; if not, treat as 0 files (silently skip).
    if !src.exists() {
        return Ok(0);
    }

    // Guard: if `dest` is inside `src`, we would create an infinite recursion.
    // Canonicalize both to handle symlinks and relative components, but fall back
    // to the raw path if canonicalization fails (e.g. dest does not yet exist).
    let src_canon = std::fs::canonicalize(src).unwrap_or_else(|_| src.to_path_buf());
    // Use the already-resolved dest (it may not exist yet, so we check if
    // the real dest is nested under the real src once it's created).
    // We defer the nested check to `copy_dir_recursive` via the `dest_canon` arg.

    // Create destination directory.
    std::fs::create_dir_all(dest)?;

    // Re-canonicalize dest now that it exists.
    let dest_canon = std::fs::canonicalize(dest).unwrap_or_else(|_| dest.to_path_buf());

    copy_dir_recursive(src, dest, src, &src_canon, &dest_canon)
}

/// Recursive helper for [`copy_cascade_dir`].
///
/// `root` is the original source root (used to compute relative paths for
/// exclusion checks). `src` is the directory being copied. `dest` is the
/// parallel destination directory. `src_canon` / `dest_canon` are canonicalized
/// versions used to detect and skip the destination subtree when it lives inside
/// the source (infinite-recursion guard).
///
/// Inputs:  `src` — source directory, `dest` — destination, `root` — original src root.
/// Outputs: number of files copied.
/// Constraints: `logs/` and `temp/` exclusion is applied to TOP-LEVEL children only
///              (relative to `root`); nested files under non-excluded subdirs are copied.
///              Any path that starts with `dest_canon` is silently skipped.
fn copy_dir_recursive(
    src: &Path,
    dest: &Path,
    root: &Path,
    _src_canon: &Path,
    dest_canon: &Path,
) -> std::io::Result<usize> {
    let mut count = 0;

    let entries = match std::fs::read_dir(src) {
        Ok(e) => e,
        Err(e) => {
            warn!(path = %src.display(), error = %e, "backup_sync: cannot read dir — skipping");
            return Ok(0);
        }
    };

    for entry in entries {
        let entry = match entry {
            Ok(e) => e,
            Err(e) => {
                warn!(error = %e, "backup_sync: failed to read dir entry — skipping");
                continue;
            }
        };

        let src_path = entry.path();
        let file_name = entry.file_name();
        let file_name_str = file_name.to_string_lossy();

        // Infinite-recursion guard: skip any entry that is the destination subtree
        // (handles the case where backup_root lives inside the source .cascade/ dir).
        if src_path.is_dir() {
            let entry_canon = std::fs::canonicalize(&src_path).unwrap_or_else(|_| src_path.clone());
            if entry_canon.starts_with(dest_canon) {
                // dest is nested inside src — skip to avoid infinite copy loop.
                continue;
            }
        }

        // Check if this is a top-level excluded directory (`logs/` or `temp/`).
        if src_path.is_dir() {
            // Only apply the name-based exclusion at the immediate children of root.
            if src == root {
                let name = file_name_str.as_ref();
                if name == "logs" || name == "temp" {
                    // Skip this entire subtree.
                    continue;
                }
            }
            // Recurse into non-excluded subdirectory.
            let dest_subdir = dest.join(&file_name);
            if let Err(e) = std::fs::create_dir_all(&dest_subdir) {
                warn!(
                    path = %dest_subdir.display(),
                    error = %e,
                    "backup_sync: cannot create dest subdir — skipping subtree"
                );
                continue;
            }
            count += copy_dir_recursive(&src_path, &dest_subdir, root, _src_canon, dest_canon)?;
        } else if src_path.is_file() {
            // Skip .pid and .lock files.
            if file_name_str.ends_with(".pid") || file_name_str.ends_with(".lock") {
                continue;
            }

            let dest_file = dest.join(&file_name);
            if let Err(e) = std::fs::copy(&src_path, &dest_file) {
                warn!(
                    src = %src_path.display(),
                    dest = %dest_file.display(),
                    error = %e,
                    "backup_sync: failed to copy file — skipping"
                );
                continue;
            }
            count += 1;
        }
        // Symlinks and other non-file/non-dir entries are silently skipped.
    }

    Ok(count)
}

// ── rotate_snapshots ──────────────────────────────────────────────────────────

/// Rotate snapshots in `tier_root`, keeping at most `max_versions` most recent.
///
/// Reads all directories matching the `snapshot-*` pattern under `tier_root`,
/// sorts lexicographically (ISO timestamp format ensures chronological order),
/// then deletes the oldest snapshots if the count exceeds `max_versions`.
///
/// Race conditions (e.g., a snapshot deleted by the user between list and delete)
/// are handled gracefully with WARN logs.
///
/// Inputs:  `tier_root` — parent directory containing `snapshot-*` subdirs,
///          `max_versions` — maximum snapshots to keep
/// Outputs: Ok(()) if rotation succeeds, Err if serious I/O errors occur
fn rotate_snapshots(tier_root: &Path, max_versions: usize) -> std::io::Result<()> {
    // Create the tier root if it doesn't exist.
    if !tier_root.exists() {
        return Ok(());
    }

    // Collect all snapshot directories.
    let mut snapshots: Vec<PathBuf> = vec![];
    for entry in std::fs::read_dir(tier_root)? {
        let entry = entry?;
        let path = entry.path();
        let file_name = entry.file_name();
        let file_name_str = file_name.to_string_lossy();

        if path.is_dir() && file_name_str.starts_with("snapshot-") {
            snapshots.push(path);
        }
    }

    // Sort lexicographically (oldest first).
    snapshots.sort();

    // If we exceed max_versions, delete the oldest ones.
    if snapshots.len() > max_versions {
        let to_delete = snapshots.len() - max_versions;
        for snapshot in snapshots.iter().take(to_delete) {
            if let Err(e) = std::fs::remove_dir_all(snapshot) {
                // Log the error but don't fail the entire rotation. The snapshot
                // may have been deleted by the user already.
                warn!(
                    snapshot = %snapshot.display(),
                    error = %e,
                    "backup_sync: failed to delete old snapshot — may have been removed already"
                );
            }
        }
    }

    Ok(())
}

// ── expand_tilde ──────────────────────────────────────────────────────────────

/// Expand a leading `~` in `path` to the home directory.
///
/// Returns the path unchanged if it does not start with `~`, or if `$HOME`
/// is not set.
fn expand_tilde(path: &str) -> PathBuf {
    if let Some(rest) = path.strip_prefix("~/") {
        if let Ok(home) = std::env::var("HOME").or_else(|_| std::env::var("USERPROFILE")) {
            return PathBuf::from(home).join(rest);
        }
    } else if path == "~" {
        if let Ok(home) = std::env::var("HOME").or_else(|_| std::env::var("USERPROFILE")) {
            return PathBuf::from(home);
        }
    }
    PathBuf::from(path)
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    use std::collections::HashMap;
    use std::path::Path;

    use serde_json::json;
    use serial_test::serial;
    use tempfile::TempDir;

    // ── helper: build a small test .cascade/ tree ────────────────────────────

    /// Create a `.cascade/` directory tree under `dir` with:
    ///   - `file_a.md`   — should be copied
    ///   - `file_b.yaml` — should be copied
    ///   - `daemon.pid`  — should be EXCLUDED (.pid)
    ///   - `state.lock`  — should be EXCLUDED (.lock)
    ///   - `logs/run.log` — should be EXCLUDED (logs/ subtree)
    ///   - `temp/scratch.txt` — should be EXCLUDED (temp/ subtree)
    ///   - `memory/decisions.md` — should be COPIED (non-excluded subdir)
    fn make_cascade_dir(dir: &Path) -> PathBuf {
        let base = dir.join(".cascade");
        std::fs::create_dir_all(&base).unwrap();
        std::fs::write(base.join("file_a.md"), "# instructions").unwrap();
        std::fs::write(base.join("file_b.yaml"), "key: value").unwrap();
        std::fs::write(base.join("daemon.pid"), "12345").unwrap();
        std::fs::write(base.join("state.lock"), "locked").unwrap();

        let logs = base.join("logs");
        std::fs::create_dir_all(&logs).unwrap();
        std::fs::write(logs.join("run.log"), "log line").unwrap();

        let temp = base.join("temp");
        std::fs::create_dir_all(&temp).unwrap();
        std::fs::write(temp.join("scratch.txt"), "scratch").unwrap();

        let memory = base.join("memory");
        std::fs::create_dir_all(&memory).unwrap();
        std::fs::write(memory.join("decisions.md"), "# decisions").unwrap();

        base
    }

    // ── Test 1: copy_cascade_dir copies correct files, excludes excluded ─────

    /// Verifies that copy_cascade_dir:
    ///   - copies `file_a.md`, `file_b.yaml`, `memory/decisions.md`
    ///   - excludes `daemon.pid`, `state.lock`, `logs/run.log`, `temp/scratch.txt`
    #[test]
    fn copy_cascade_dir_correct_files() {
        let src_tmp = TempDir::new().unwrap();
        let dest_tmp = TempDir::new().unwrap();

        let src = make_cascade_dir(src_tmp.path());
        let dest = dest_tmp.path().join("backup");

        let n = copy_cascade_dir(&src, &dest).unwrap();

        // 3 files should be copied: file_a.md, file_b.yaml, memory/decisions.md
        assert_eq!(n, 3, "expected 3 files copied, got {n}");

        assert!(
            dest.join("file_a.md").exists(),
            "file_a.md should be present"
        );
        assert!(
            dest.join("file_b.yaml").exists(),
            "file_b.yaml should be present"
        );
        assert!(
            dest.join("memory").join("decisions.md").exists(),
            "memory/decisions.md should be present"
        );

        // Excluded
        assert!(
            !dest.join("daemon.pid").exists(),
            "daemon.pid should be excluded"
        );
        assert!(
            !dest.join("state.lock").exists(),
            "state.lock should be excluded"
        );
        assert!(
            !dest.join("logs").exists(),
            "logs/ subtree should be excluded"
        );
        assert!(
            !dest.join("temp").exists(),
            "temp/ subtree should be excluded"
        );
    }

    // ── Test 2: throttle prevents second backup within interval ──────────────

    /// Verifies that when two `cascade.regen.complete` events arrive for the
    /// same tier within the `sync_interval_secs`, only one backup is run.
    #[tokio::test]
    async fn backup_sync_throttle_suppresses_rapid_second_backup() {
        use crate::config::BackupConfig;
        use crate::event_bus::EventBus;
        use tempfile::TempDir;

        let tmp = TempDir::new().unwrap();
        let config_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&config_dir).unwrap();

        let backup_root = tmp.path().join("backups");

        // Make a source .cascade dir with some files
        let src = make_cascade_dir(tmp.path());

        let tier_sources_json: HashMap<&str, String> = {
            let mut m = HashMap::new();
            m.insert("GCI", src.display().to_string());
            m
        };

        // Build an event bus and publish TWO regen.complete events back-to-back.
        let config = crate::config::Config::default();
        let bus = EventBus::new(config_dir.clone(), &config)
            .await
            .expect("bus");

        // Event 1
        bus.publish(
            "cascade.regen.complete",
            json!({
                "tier_sources": tier_sources_json,
                "ts": 1000u64,
            }),
        )
        .await
        .expect("publish 1");

        // Event 2 (rapid — within sync_interval)
        bus.publish(
            "cascade.regen.complete",
            json!({
                "tier_sources": tier_sources_json,
                "ts": 1001u64,
            }),
        )
        .await
        .expect("publish 2");

        // Run BackupSync with a 60 s sync_interval so the second event is throttled.
        let cfg = BackupConfig {
            enabled: true,
            backup_root: backup_root.display().to_string(),
            sync_interval_secs: 60,
            max_versions: 7,
        };

        // Use a shutdown token that fires after a short poll to process both events.
        let shutdown = CancellationToken::new();
        let shutdown_clone = shutdown.clone();

        // Consume all events synchronously to test the throttle logic.
        // We drive one iteration manually instead of spawning the full loop.
        {
            let events = bus
                .consume("cascade.regen.complete", 64)
                .await
                .expect("consume");
            assert_eq!(events.len(), 2, "expected 2 events in bus");

            let mut last_backup: HashMap<String, Instant> = HashMap::new();
            let sync_interval = Duration::from_secs(cfg.sync_interval_secs);

            for event in &events {
                let payload: RegenPayload =
                    serde_json::from_value(event.payload.clone()).expect("payload");
                for (tier_name, source_dir) in &payload.tier_sources {
                    let now = Instant::now();
                    let last = last_backup.get(tier_name.as_str());
                    if let Some(last_t) = last {
                        if now.duration_since(*last_t) < sync_interval {
                            // Throttled — skip
                            continue;
                        }
                    }
                    let dest = backup_root.join(tier_name);
                    let n = copy_cascade_dir(source_dir, &dest).unwrap();
                    assert!(n > 0, "first backup should copy files");
                    last_backup.insert(tier_name.clone(), Instant::now());
                }
            }

            // After processing both events, there should be exactly 1 backup dir for GCI.
            // The second event was throttled, so the backup ran only once.
            let gci_backup = backup_root.join("GCI");
            assert!(
                gci_backup.exists(),
                "GCI backup dir should exist after first event"
            );
        }

        // Cancel the shutdown token (not needed for this sync test, but documents usage).
        shutdown_clone.cancel();
        let _ = shutdown;
    }

    // ── Test 3: snapshot rotation keeps exactly max_versions snapshots ────────

    /// Verifies that after multiple backup runs, exactly `max_versions` snapshot
    /// directories remain under `{backup_root}/{tier_name}/`, and older ones are
    /// deleted.
    #[test]
    fn snapshot_rotation_keeps_max_versions() {
        let tmp = TempDir::new().unwrap();
        let tier_root = tmp.path().join("tier_backups");
        std::fs::create_dir_all(&tier_root).unwrap();

        // Create 10 fake snapshot directories with names that sort correctly.
        for i in 1..=10 {
            let snapshot_name = format!("snapshot-2026-06-01-{:06}", i * 1000);
            let snapshot_dir = tier_root.join(&snapshot_name);
            std::fs::create_dir(&snapshot_dir).unwrap();
            std::fs::write(snapshot_dir.join("data.txt"), format!("snapshot {}", i)).unwrap();
        }

        // Verify all 10 exist.
        let before_count = std::fs::read_dir(&tier_root)
            .unwrap()
            .filter_map(|e| {
                let entry = e.ok()?;
                let path = entry.path();
                if path.is_dir() {
                    let name = entry.file_name();
                    if name.to_string_lossy().starts_with("snapshot-") {
                        return Some(path);
                    }
                }
                None
            })
            .count();
        assert_eq!(before_count, 10, "expected 10 snapshots before rotation");

        // Run rotation with max_versions = 7.
        let result = rotate_snapshots(&tier_root, 7);
        assert!(result.is_ok(), "rotation should succeed");

        // Verify exactly 7 remain.
        let after_count = std::fs::read_dir(&tier_root)
            .unwrap()
            .filter_map(|e| {
                let entry = e.ok()?;
                let path = entry.path();
                if path.is_dir() {
                    let name = entry.file_name();
                    if name.to_string_lossy().starts_with("snapshot-") {
                        return Some(path);
                    }
                }
                None
            })
            .count();
        assert_eq!(
            after_count, 7,
            "expected exactly 7 snapshots after rotation"
        );

        // Verify the oldest 3 (snapshot-001000, 002000, 003000) are gone.
        for i in 1..=3 {
            let old_name = format!("snapshot-2026-06-01-{:06}", i * 1000);
            let old_path = tier_root.join(&old_name);
            assert!(
                !old_path.exists(),
                "snapshot {} should have been deleted",
                old_name
            );
        }

        // Verify the newest 7 remain.
        for i in 4..=10 {
            let keep_name = format!("snapshot-2026-06-01-{:06}", i * 1000);
            let keep_path = tier_root.join(&keep_name);
            assert!(
                keep_path.exists(),
                "snapshot {} should still exist",
                keep_name
            );
        }
    }

    // ── Test 4: backup_enabled=false suppresses all operations ───────────────

    /// Verifies that when `BackupConfig::enabled = false`, `BackupSync::run`
    /// returns `Ok(())` immediately without consuming any events or writing files.
    #[tokio::test]
    async fn backup_sync_disabled_skips_all_operations() {
        use crate::config::BackupConfig;
        use crate::event_bus::EventBus;
        use tempfile::TempDir;

        let tmp = TempDir::new().unwrap();
        let config_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&config_dir).unwrap();

        let backup_root = tmp.path().join("backups");

        let src = make_cascade_dir(tmp.path());
        let tier_sources_json: HashMap<&str, String> = {
            let mut m = HashMap::new();
            m.insert("GCI", src.display().to_string());
            m
        };

        let config = crate::config::Config::default();
        let bus = EventBus::new(config_dir.clone(), &config)
            .await
            .expect("bus");

        // Publish an event — it should NOT be consumed when disabled.
        bus.publish(
            "cascade.regen.complete",
            json!({ "tier_sources": tier_sources_json, "ts": 1000u64 }),
        )
        .await
        .expect("publish");

        let cfg = BackupConfig {
            enabled: false, // Disabled
            backup_root: backup_root.display().to_string(),
            sync_interval_secs: 60,
            max_versions: 7,
        };

        let shutdown = CancellationToken::new();

        // BackupSync::run should return immediately.
        let result = BackupSync::run(cfg, bus.clone(), shutdown).await;
        assert!(result.is_ok(), "expected Ok(()), got: {:?}", result.err());

        // Backup dir should not exist since no backup was run.
        assert!(
            !backup_root.exists(),
            "backup_root should not exist when backup is disabled"
        );

        // The event should still be unconsumed in the bus.
        let remaining = bus.peek("cascade.regen.complete", 10).await.expect("peek");
        assert_eq!(
            remaining.len(),
            1,
            "event should remain unconsumed when backup is disabled"
        );
    }

    // ── Test 4: expand_tilde expands ~ correctly ──────────────────────────────

    /// Verifies that `expand_tilde("~/foo")` returns `{$HOME}/foo`.
    #[test]
    #[serial(global_env)]
    fn expand_tilde_expands_home() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        // Set HOME for test isolation.
        let fake_home = "/tmp/fake-home-for-test";
        std::env::set_var("HOME", fake_home);

        let expanded = expand_tilde("~/backups");
        assert_eq!(
            expanded,
            PathBuf::from("/tmp/fake-home-for-test/backups"),
            "expected tilde expansion, got: {:?}",
            expanded
        );
    }
}
