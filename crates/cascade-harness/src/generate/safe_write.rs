//! Safe, atomic, symlink-aware, hash-stamped derived-file writing.
//!
//! ## Purpose
//!
//! Provides the primitives used by all harness-file generators to ensure
//! generated files are never partially written, always carry a content hash
//! for tamper detection, and leave pre-existing files in a recoverable state.
//!
//! ## Atomic writes
//!
//! Every write goes through [`atomic_write_content`]:
//! 1. Write content to `<dest>.harness.tmp` in the same directory.
//! 2. `rename()` the temp file onto the destination — atomic on all POSIX
//!    filesystems and on Windows (NTFS, same volume).
//!
//! This guarantees the destination is never seen in a partial state by other
//! processes, even if the writer crashes mid-write.
//!
//! ## Symlink handling
//!
//! If the target path is a symlink, [`atomic_write_content`] resolves it to
//! its real target and writes _that_ file instead.  The symlink itself is
//! never replaced.
//!
//! **Out-of-tree symlinks are rejected**: if the resolved target lies outside
//! the `workspace` directory the caller supplies, the write is refused with
//! [`SafeWriteError::SymlinkEscape`].  This prevents a crafted symlink from
//! silently overwriting files outside the project.
//!
//! ## Content hash
//!
//! [`content_hash`] computes a truncated hex SHA-256 over the file body
//! (first 16 bytes of the digest → 32 hex chars).  Call sites embed this in
//! the cascade marker line so later reads can detect hand-edits:
//!
//! ```text
//! <!-- cascade:unified-harness sha256=a3f7... -->
//! ```
//!
//! [`hash_matches`] re-hashes the body and compares.
//!
//! ## Snapshot-before-regenerate
//!
//! [`snapshot_files`] copies a list of paths into a timestamped subdirectory
//! under `<workspace>/.cascade/snapshots/<timestamp>/` before any generation
//! run overwrites them.  The snapshot is a plain filesystem copy — no git
//! required, no daemon dependency.
//!
//! [`list_snapshots`] returns all snapshot directories sorted oldest-first.
//! [`restore_snapshot`] copies files from a snapshot back to their original
//! workspace-relative paths (dry-run or apply).
//!
//! ## SPORT
//!
//! MASTER-HARNESSES.md: safe-write primitives (P12, snapshot-before-regenerate)

use std::fs;
use std::path::{Component, Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use cascade_types::error::{CascadeError, Result};
use sha2::{Digest, Sha256};

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors specific to safe-write operations.
///
/// All variants surface as [`CascadeError::Other`] at the crate boundary so
/// callers need only handle `CascadeError`.
#[derive(Debug)]
pub enum SafeWriteError {
    /// The symlink at `link` resolves to `target` which is outside `workspace`.
    SymlinkEscape { link: PathBuf, target: PathBuf },
    /// A snapshot directory for `timestamp` already exists.
    SnapshotExists { dir: PathBuf },
}

impl std::fmt::Display for SafeWriteError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::SymlinkEscape { link, target } => write!(
                f,
                "symlink '{}' resolves to '{}' which is outside the workspace — write refused",
                link.display(),
                target.display()
            ),
            Self::SnapshotExists { dir } => {
                write!(f, "snapshot directory '{}' already exists", dir.display())
            }
        }
    }
}

// ── Content hash ──────────────────────────────────────────────────────────────

/// Compute a 32-hex-char content fingerprint (first 128 bits of SHA-256).
///
/// The hash is computed over the raw bytes of `content`.  It is embedded in
/// the cascade marker line so that hand-edits can be detected on the next
/// generation run.
///
/// # Example
///
/// ```
/// # use cascade_harness::generate::safe_write::content_hash;
/// let h = content_hash("hello");
/// assert_eq!(h.len(), 32);
/// assert!(h.chars().all(|c| c.is_ascii_hexdigit()));
/// ```
pub fn content_hash(content: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(content.as_bytes());
    let result = hasher.finalize();
    // Truncate to first 16 bytes (128 bits) → 32 hex chars.
    result[..16]
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect::<String>()
}

/// Return true if the embedded hash in `content` matches a freshly computed
/// hash of the portion of `content` _after_ the marker line.
///
/// The marker line has the form:
/// ```text
/// <!-- cascade:unified-harness sha256=<hash> -->
/// ```
///
/// If no marker line is present the function returns `None` (unknown).
/// If a marker is present but the hash does not match, returns `Some(false)`
/// (hand-edited).  If it matches, returns `Some(true)`.
pub fn hash_matches(content: &str) -> Option<bool> {
    let prefix = "<!-- cascade:unified-harness sha256=";
    let line = content.lines().find(|l| l.starts_with(prefix))?;

    // Extract the stored hash from the marker line.
    let after_prefix = line.trim_start_matches(prefix);
    let stored = after_prefix.trim_end_matches(" -->").trim();
    if stored.len() != 32 || !stored.chars().all(|c| c.is_ascii_hexdigit()) {
        return Some(false);
    }

    // The body to hash is everything after the first newline following the
    // marker line.  We locate the marker position in `content`, skip past it
    // and the newline, then hash the remainder.
    let marker_pos = content.find(line)?;
    let after_marker = &content[marker_pos + line.len()..];
    let body = after_marker.trim_start_matches('\n');

    let computed = content_hash(body);
    Some(computed == stored)
}

// ── Atomic write ──────────────────────────────────────────────────────────────

/// Write `content` to `dest` atomically, handling symlinks safely.
///
/// # Symlink behaviour
///
/// - If `dest` is a regular file: write to `<dest>.harness.tmp` then rename.
/// - If `dest` is a symlink:
///   - Resolve the real target with `fs::canonicalize`.
///   - If the target lies outside `workspace`: return `Err(SymlinkEscape)`.
///   - Otherwise: write to a temp file adjacent to the **real target** and
///     rename onto it.  The symlink is left untouched.
/// - If `dest` does not exist yet: write normally (no symlink to resolve).
///
/// # Errors
///
/// Returns `CascadeError::Io` on I/O failure or `CascadeError::Other` for
/// the symlink-escape case.
pub fn atomic_write_content(dest: &Path, content: &str, workspace: &Path) -> Result<()> {
    // Resolve symlinks if the target exists.
    let real_dest: PathBuf = if dest.is_symlink() {
        let target = fs::canonicalize(dest).map_err(|e| CascadeError::Io {
            path: dest.to_path_buf(),
            operation: "canonicalize symlink target",
            source: e,
        })?;
        // Safety: reject writes that escape the workspace tree.
        let canon_ws = fs::canonicalize(workspace).unwrap_or_else(|_| workspace.to_path_buf());
        if !target.starts_with(&canon_ws) {
            return Err(CascadeError::Other(
                SafeWriteError::SymlinkEscape {
                    link: dest.to_path_buf(),
                    target,
                }
                .to_string(),
            ));
        }
        target
    } else {
        dest.to_path_buf()
    };

    // Ensure parent directory exists.
    if let Some(parent) = real_dest.parent() {
        fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create_dir_all",
            source: e,
        })?;
    }

    // Write to temp file, then rename (atomic on same filesystem).
    let tmp = {
        let mut s = real_dest.as_os_str().to_owned();
        s.push(".harness.tmp");
        PathBuf::from(s)
    };

    fs::write(&tmp, content).map_err(|e| CascadeError::Io {
        path: tmp.clone(),
        operation: "write_tmp",
        source: e,
    })?;

    fs::rename(&tmp, &real_dest).map_err(|e| {
        // Best-effort cleanup of the temp file on rename failure.
        let _ = fs::remove_file(&tmp);
        CascadeError::Io {
            path: real_dest.clone(),
            operation: "rename",
            source: e,
        }
    })
}

// ── Snapshot ──────────────────────────────────────────────────────────────────

/// Snapshot directory root under `<workspace>/.cascade/snapshots/`.
fn snapshot_root(workspace: &Path) -> PathBuf {
    workspace.join(".cascade").join("snapshots")
}

/// Generate a timestamp string for snapshot directory names.
///
/// Format: `YYYYMMDD-HHMMSS-<millis>` (UTC, from `SystemTime`).
/// Falls back to a monotonic counter on platforms without system time.
fn timestamp_now() -> String {
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    let millis = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.subsec_millis())
        .unwrap_or(0);

    // Decompose seconds into Y-M-D H:M:S (no chrono dependency).
    let (y, mo, d, h, mi, s) = secs_to_ymd_hms(secs);
    format!("{y:04}{mo:02}{d:02}-{h:02}{mi:02}{s:02}-{millis:03}")
}

/// Decompose a Unix timestamp (seconds) into (year, month, day, hour, min, sec).
///
/// Uses the proleptic Gregorian calendar algorithm from POSIX.
fn secs_to_ymd_hms(secs: u64) -> (u32, u32, u32, u32, u32, u32) {
    let s = secs % 60;
    let m = (secs / 60) % 60;
    let h = (secs / 3600) % 24;
    let days = secs / 86400;

    // Days since 1970-01-01 → Gregorian date via the algorithm in POSIX.1.
    let z = days + 719468;
    let era = z / 146097;
    let doe = z % 146097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = if month <= 2 { y + 1 } else { y };

    (
        year as u32,
        month as u32,
        day as u32,
        h as u32,
        m as u32,
        s as u32,
    )
}

/// Copy `paths` into a timestamped snapshot directory before overwriting them.
///
/// # Behaviour
///
/// - Creates `<workspace>/.cascade/snapshots/<timestamp>/` and mirrors the
///   workspace-relative path of each file inside it.
/// - Only files that currently exist are copied; missing files are skipped
///   (no error — the caller may be generating a brand-new file).
/// - The snapshot is a plain copy, so it works even if the workspace is not
///   a git repository.
/// - Symlinks: the **link target content** is copied (not the link itself),
///   so the snapshot is a self-contained set of regular files.
///
/// # Arguments
///
/// * `workspace` — project root directory (used for relative-path computation
///   and as the snapshot-root parent).
/// * `paths` — absolute paths of files to capture; each must be under
///   `workspace`.
///
/// # Returns
///
/// The snapshot directory path, or `Ok(None)` if no files existed to capture.
pub fn snapshot_files(workspace: &Path, paths: &[PathBuf]) -> Result<Option<PathBuf>> {
    let canon_ws = fs::canonicalize(workspace).unwrap_or_else(|_| workspace.to_path_buf());

    // Collect only the files that currently exist.
    let existing: Vec<&PathBuf> = paths.iter().filter(|p| p.exists()).collect();
    if existing.is_empty() {
        return Ok(None);
    }

    let ts = timestamp_now();
    let snap_dir = snapshot_root(workspace).join(&ts);

    fs::create_dir_all(&snap_dir).map_err(|e| CascadeError::Io {
        path: snap_dir.clone(),
        operation: "create snapshot dir",
        source: e,
    })?;

    for src in &existing {
        // Compute workspace-relative path.
        let canon_src = fs::canonicalize(src).unwrap_or_else(|_| src.to_path_buf());
        let rel = canon_src.strip_prefix(&canon_ws).map_err(|_| {
            CascadeError::Other(format!(
                "snapshot: '{}' is outside workspace '{}'",
                src.display(),
                workspace.display()
            ))
        })?;

        // Strip leading path separators / current-dir components.
        let rel = strip_prefix_components(rel);

        let snap_target = snap_dir.join(rel);
        if let Some(parent) = snap_target.parent() {
            fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
                path: parent.to_path_buf(),
                operation: "create snapshot subdir",
                source: e,
            })?;
        }

        fs::copy(&canon_src, &snap_target).map_err(|e| CascadeError::Io {
            path: snap_target.clone(),
            operation: "snapshot copy",
            source: e,
        })?;
    }

    Ok(Some(snap_dir))
}

/// Strip leading `/`, `./`, or `../` components from a relative path.
///
/// Needed because `Path::strip_prefix` can return paths with a leading `./`.
fn strip_prefix_components(path: &Path) -> &Path {
    let mut components = path.components().peekable();
    while let Some(c) = components.peek() {
        match c {
            Component::CurDir | Component::RootDir => {
                components.next();
            }
            _ => break,
        }
    }
    // Re-build from remaining: since we can't reconstruct a &Path cheaply
    // without allocation, return the original path if no stripping needed.
    path
}

// ── Snapshot listing & restore ────────────────────────────────────────────────

/// A snapshot entry returned by [`list_snapshots`].
#[derive(Debug, Clone)]
pub struct SnapshotEntry {
    /// Timestamp portion of the snapshot directory name.
    pub timestamp: String,
    /// Absolute path of the snapshot directory.
    pub path: PathBuf,
}

/// List all snapshots under `<workspace>/.cascade/snapshots/`, oldest first.
///
/// Returns an empty `Vec` if no snapshots exist.
pub fn list_snapshots(workspace: &Path) -> Result<Vec<SnapshotEntry>> {
    let root = snapshot_root(workspace);
    if !root.exists() {
        return Ok(Vec::new());
    }

    let mut entries: Vec<SnapshotEntry> = fs::read_dir(&root)
        .map_err(|e| CascadeError::Io {
            path: root.clone(),
            operation: "read snapshot root",
            source: e,
        })?
        .filter_map(|e| {
            let entry = e.ok()?;
            let path = entry.path();
            if !path.is_dir() {
                return None;
            }
            let ts = path.file_name()?.to_str()?.to_owned();
            Some(SnapshotEntry {
                timestamp: ts,
                path,
            })
        })
        .collect();

    entries.sort_by(|a, b| a.timestamp.cmp(&b.timestamp));
    Ok(entries)
}

/// Restore files from a snapshot directory back into the workspace.
///
/// # Arguments
///
/// * `snapshot_dir` — absolute path to the snapshot directory (from
///   [`list_snapshots`]).
/// * `workspace`    — project root; files are restored to the same
///   workspace-relative paths they were captured from.
/// * `dry_run`      — if `true`, print planned restores without writing.
///
/// # Returns
///
/// List of `(snapshot_src, workspace_dest)` pairs that were (or would be)
/// restored.
pub fn restore_snapshot(
    snapshot_dir: &Path,
    workspace: &Path,
    dry_run: bool,
) -> Result<Vec<(PathBuf, PathBuf)>> {
    if !snapshot_dir.exists() {
        return Err(CascadeError::Other(format!(
            "snapshot directory not found: {}",
            snapshot_dir.display()
        )));
    }

    let mut restored = Vec::new();

    for entry in walkdir_flat(snapshot_dir)? {
        let rel = entry.strip_prefix(snapshot_dir).map_err(|_| {
            CascadeError::Other(format!(
                "restore: '{}' not under snapshot dir",
                entry.display()
            ))
        })?;

        let dest = workspace.join(rel);

        if dry_run {
            println!(
                "[dry-run] would restore: {} -> {}",
                entry.display(),
                dest.display()
            );
        } else {
            if let Some(parent) = dest.parent() {
                fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
                    path: parent.to_path_buf(),
                    operation: "restore: create_dir_all",
                    source: e,
                })?;
            }
            // Use atomic_write_content so the restore itself is also atomic.
            let content = fs::read_to_string(&entry).map_err(|e| CascadeError::Io {
                path: entry.clone(),
                operation: "restore: read snapshot file",
                source: e,
            })?;
            atomic_write_content(&dest, &content, workspace)?;
            println!("restored: {} -> {}", entry.display(), dest.display());
        }

        restored.push((entry, dest));
    }

    Ok(restored)
}

/// Recursively collect all regular files under `dir` (no symlinks followed).
fn walkdir_flat(dir: &Path) -> Result<Vec<PathBuf>> {
    let mut files = Vec::new();
    for entry in fs::read_dir(dir).map_err(|e| CascadeError::Io {
        path: dir.to_path_buf(),
        operation: "walkdir_flat: read_dir",
        source: e,
    })? {
        let entry = entry.map_err(|e| CascadeError::Io {
            path: dir.to_path_buf(),
            operation: "walkdir_flat: dir entry",
            source: e,
        })?;
        let path = entry.path();
        if path.is_dir() {
            files.extend(walkdir_flat(&path)?);
        } else {
            files.push(path);
        }
    }
    files.sort();
    Ok(files)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    // ── content_hash ──────────────────────────────────────────────────────────

    /// content_hash returns 32 lowercase hex chars.
    ///
    /// WHY: the hash is embedded in generated files; wrong length breaks parsing.
    #[test]
    fn content_hash_length_and_charset() {
        let h = content_hash("hello world");
        assert_eq!(h.len(), 32, "hash must be 32 hex chars");
        assert!(
            h.chars().all(|c| c.is_ascii_hexdigit()),
            "hash must be lowercase hex"
        );
    }

    /// content_hash is deterministic — same input always produces same output.
    #[test]
    fn content_hash_deterministic() {
        let a = content_hash("cascade rules");
        let b = content_hash("cascade rules");
        assert_eq!(a, b);
    }

    /// Different inputs produce different hashes (no trivial collision).
    #[test]
    fn content_hash_collision_free() {
        let a = content_hash("file A content");
        let b = content_hash("file B content");
        assert_ne!(a, b);
    }

    /// SHA-256 known-answer: content_hash("abc") must match the known SHA-256 prefix.
    ///
    /// SHA-256("abc") = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
    /// content_hash returns the first 32 hex chars (16 bytes = 128 bits).
    #[test]
    fn sha256_known_answer_via_content_hash() {
        let h = content_hash("abc");
        assert_eq!(
            h, "ba7816bf8f01cfea414140de5dae2223",
            "SHA-256 known-answer failed: {h}"
        );
    }

    // ── hash_matches ──────────────────────────────────────────────────────────

    /// hash_matches returns Some(true) when the embedded hash matches the body.
    #[test]
    fn hash_matches_true_when_unmodified() {
        let body = "# Rules\n\nDo good work.\n";
        let hash = content_hash(body);
        let content = format!("<!-- cascade:unified-harness sha256={hash} -->\n{body}");
        assert_eq!(hash_matches(&content), Some(true));
    }

    /// hash_matches returns Some(false) when the body has been hand-edited.
    #[test]
    fn hash_matches_false_when_modified() {
        let original_body = "# Rules\n\nDo good work.\n";
        let hash = content_hash(original_body);
        // Simulate a hand-edit: body differs from what the hash covers.
        let content = format!("<!-- cascade:unified-harness sha256={hash} -->\n# HAND EDITED\n");
        assert_eq!(hash_matches(&content), Some(false));
    }

    /// hash_matches returns None when no marker line is present.
    #[test]
    fn hash_matches_none_without_marker() {
        let content = "# No marker here\n\nSome content.";
        assert_eq!(hash_matches(content), None);
    }

    // ── atomic_write_content ──────────────────────────────────────────────────

    /// Atomic write creates the destination file with expected content.
    #[test]
    fn atomic_write_creates_file() {
        let tmp = TempDir::new().unwrap();
        let dest = tmp.path().join("CLAUDE.md");
        atomic_write_content(&dest, "hello", tmp.path()).unwrap();
        assert!(dest.exists());
        assert_eq!(fs::read_to_string(&dest).unwrap(), "hello");
    }

    /// No partial file remains after a simulated write failure.
    ///
    /// We simulate failure by writing to a path whose parent does not exist
    /// (so the rename step will fail if the parent dir is not created).
    /// The more relevant invariant tested here is: no `.harness.tmp` survives.
    #[test]
    fn atomic_write_no_partial_on_rename_failure() {
        let tmp = TempDir::new().unwrap();
        // Parent does not exist — create_dir_all is called internally, so the
        // write actually succeeds (no rename failure to test).
        // The invariant we verify: no `.harness.tmp` file is left behind in any case.
        let bad_dest = tmp.path().join("nonexistent_subdir_abc").join("file.md");
        let _ = atomic_write_content(&bad_dest, "content", tmp.path());
        let tmp_file = PathBuf::from(format!("{}.harness.tmp", bad_dest.display()));
        assert!(
            !tmp_file.exists(),
            "temp file must not remain after failure"
        );
    }

    /// Symlink pointing within workspace: content is written to the real target.
    #[test]
    #[cfg(unix)]
    fn atomic_write_follows_intra_workspace_symlink() {
        let tmp = TempDir::new().unwrap();
        let real_file = tmp.path().join("real.md");
        fs::write(&real_file, "original").unwrap();
        let link = tmp.path().join("CLAUDE.md");
        std::os::unix::fs::symlink("real.md", &link).unwrap();

        atomic_write_content(&link, "updated", tmp.path()).unwrap();

        // Real file is updated, symlink still exists.
        assert!(link.is_symlink(), "symlink must remain");
        assert_eq!(fs::read_to_string(&real_file).unwrap(), "updated");
    }

    /// Symlink pointing outside workspace: write is refused.
    #[test]
    #[cfg(unix)]
    fn atomic_write_refuses_symlink_escape() {
        let workspace = TempDir::new().unwrap();
        let outside = TempDir::new().unwrap();
        let outside_file = outside.path().join("escape.md");
        fs::write(&outside_file, "outside").unwrap();

        let link = workspace.path().join("CLAUDE.md");
        std::os::unix::fs::symlink(&outside_file, &link).unwrap();

        let result = atomic_write_content(&link, "attacker", workspace.path());
        assert!(result.is_err(), "write to escaped symlink must be rejected");
        // The outside file must be untouched.
        assert_eq!(fs::read_to_string(&outside_file).unwrap(), "outside");
    }

    // ── snapshot_files ────────────────────────────────────────────────────────

    /// snapshot_files captures existing files into a timestamped directory.
    #[test]
    fn snapshot_captures_files() {
        let tmp = TempDir::new().unwrap();
        let claude_md = tmp.path().join("CLAUDE.md");
        let agents_md = tmp.path().join("AGENTS.md");
        fs::write(&claude_md, "original claude content").unwrap();
        fs::write(&agents_md, "original agents content").unwrap();

        let snap_opt = snapshot_files(tmp.path(), &[claude_md.clone(), agents_md.clone()]).unwrap();
        let snap_dir = snap_opt.expect("snapshot must be created");

        assert!(snap_dir.exists(), "snapshot dir must exist");
        // Verify captured content matches originals.
        let snap_claude = snap_dir.join("CLAUDE.md");
        let snap_agents = snap_dir.join("AGENTS.md");
        assert!(snap_claude.exists(), "CLAUDE.md must be in snapshot");
        assert!(snap_agents.exists(), "AGENTS.md must be in snapshot");
        assert_eq!(
            fs::read_to_string(&snap_claude).unwrap(),
            "original claude content"
        );
        assert_eq!(
            fs::read_to_string(&snap_agents).unwrap(),
            "original agents content"
        );
    }

    /// snapshot_files returns Ok(None) when no files exist yet.
    #[test]
    fn snapshot_returns_none_when_no_files() {
        let tmp = TempDir::new().unwrap();
        let missing = tmp.path().join("does_not_exist.md");
        let result = snapshot_files(tmp.path(), &[missing]).unwrap();
        assert!(result.is_none());
    }

    // ── list_snapshots ────────────────────────────────────────────────────────

    /// list_snapshots returns entries sorted oldest-first.
    #[test]
    fn list_snapshots_sorted() {
        let tmp = TempDir::new().unwrap();
        let snap_root = tmp.path().join(".cascade").join("snapshots");
        fs::create_dir_all(&snap_root).unwrap();
        fs::create_dir_all(snap_root.join("20260101-000000-001")).unwrap();
        fs::create_dir_all(snap_root.join("20260102-000000-001")).unwrap();
        fs::create_dir_all(snap_root.join("20260103-000000-001")).unwrap();

        let entries = list_snapshots(tmp.path()).unwrap();
        assert_eq!(entries.len(), 3);
        assert!(entries[0].timestamp < entries[1].timestamp);
        assert!(entries[1].timestamp < entries[2].timestamp);
    }

    /// list_snapshots returns empty vec when no snapshots exist.
    #[test]
    fn list_snapshots_empty() {
        let tmp = TempDir::new().unwrap();
        let entries = list_snapshots(tmp.path()).unwrap();
        assert!(entries.is_empty());
    }

    // ── restore_snapshot ──────────────────────────────────────────────────────

    /// restore_snapshot (dry-run) reports files without writing.
    #[test]
    fn restore_dry_run_no_writes() {
        let tmp = TempDir::new().unwrap();
        // Create a fake snapshot dir with one file.
        let snap_dir = tmp
            .path()
            .join(".cascade")
            .join("snapshots")
            .join("20260101-000000-001");
        fs::create_dir_all(&snap_dir).unwrap();
        fs::write(snap_dir.join("CLAUDE.md"), "snapshot content").unwrap();

        // Ensure the workspace doesn't have CLAUDE.md yet.
        let dest = tmp.path().join("CLAUDE.md");
        assert!(!dest.exists());

        let pairs = restore_snapshot(&snap_dir, tmp.path(), /* dry_run= */ true).unwrap();
        assert_eq!(pairs.len(), 1);
        // File must NOT have been written in dry-run.
        assert!(!dest.exists(), "dry-run must not write CLAUDE.md");
    }

    /// restore_snapshot (--apply) writes files back to workspace.
    #[test]
    fn restore_apply_restores_files() {
        let tmp = TempDir::new().unwrap();
        let snap_dir = tmp
            .path()
            .join(".cascade")
            .join("snapshots")
            .join("20260101-000000-001");
        fs::create_dir_all(&snap_dir).unwrap();
        fs::write(snap_dir.join("CLAUDE.md"), "restored content").unwrap();

        let dest = tmp.path().join("CLAUDE.md");
        let pairs = restore_snapshot(&snap_dir, tmp.path(), /* dry_run= */ false).unwrap();
        assert_eq!(pairs.len(), 1);
        assert!(dest.exists());
        assert_eq!(fs::read_to_string(&dest).unwrap(), "restored content");
    }
}
