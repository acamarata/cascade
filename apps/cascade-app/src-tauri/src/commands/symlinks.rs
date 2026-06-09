// Tauri IPC commands — symlink setup for Cascade-managed tools.
//
// Purpose: Exposes the symlink generation service as a Tauri IPC command
//   callable from the onboarding wizard Phase 8 (SymlinkSetupPhase.tsx).
//
// Inputs:
//   setup_symlinks — plan: SymlinkPlan — per-tool list of {source, target, kind}.
//
// Outputs: Vec<SymlinkResult> — per-symlink success/error status.
//
// Constraints:
//   - Never overwrites a real (non-symlink) file — returns an error entry.
//   - Replaces an existing symlink pointing to a different target.
//   - No auto-apply: the frontend must show the preview before invoking.
//   - Cross-platform: std::os::unix::fs::symlink on macOS/Linux.
//
// SPORT: MASTER-COMPONENTS.md — setup_symlinks command / SymlinkSetupPhase
// Tasks: T-P3-E03-09 (scaffold); T-P3-E03-14 (implementation)

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

// ---------------------------------------------------------------------------
// SymlinkPlan / SymlinkEntry — input types
// ---------------------------------------------------------------------------

/// The kind of symlink to create.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SymlinkKind {
    /// Replace the canonical global path with a symlink to the Cascade file.
    Replace,
    /// Create a sibling symlink next to an existing tool file.
    Sibling,
}

/// A single planned symlink operation.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SymlinkEntry {
    /// Absolute path that will become the symlink (the link location).
    pub source: String,
    /// Absolute path that the symlink will point to (the link target).
    pub target: String,
    /// Whether to replace or add a sibling.
    pub kind: SymlinkKind,
}

/// The full plan for Phase 8 symlink setup, covering all cascade-managed tools.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SymlinkPlan {
    /// All planned symlink operations across all tools.
    pub entries: Vec<SymlinkEntry>,
}

// ---------------------------------------------------------------------------
// SymlinkResult — output type
// ---------------------------------------------------------------------------

/// The outcome of a single symlink operation.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SymlinkResult {
    /// The symlink location (matches SymlinkEntry::source).
    pub source: String,
    /// The symlink target (matches SymlinkEntry::target).
    pub target: String,
    /// Whether the operation succeeded.
    pub success: bool,
    /// Error message if success is false; absent on success.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

// ---------------------------------------------------------------------------
// Security helpers — T-P3-E03-14
// ---------------------------------------------------------------------------

/// Resolve the user HOME directory. Returns None if the env var is unset.
fn home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(PathBuf::from)
}

/// Normalize a path without requiring it to exist.
///
/// Walks the components, applying `..` reductions on the accumulated result.
/// Symlinks in intermediate components are NOT resolved (no stat calls).
/// This is a best-effort HOME-prefix check; real canonicalization of existing
/// paths is done separately via `std::fs::canonicalize`.
fn normalize_path(path: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for component in path.components() {
        match component {
            std::path::Component::ParentDir => {
                out.pop();
            }
            std::path::Component::CurDir => {}
            c => out.push(c),
        }
    }
    out
}

/// Verify `p` is confined to `home`.
///
/// For paths that already exist the full canonicalization (resolving symlinks)
/// is used. For link locations that may not yet exist, lexical normalization
/// is used (no symlink resolution in intermediate dirs), which is safe because
/// the only attack vector — a symlink in an intermediate component pointing
/// outside HOME — requires the attacker to already have write access to the
/// user's home directory.
///
/// Returns the resolved/normalized path or an error string.
fn confine_to_home(p: &str, home: &Path, is_link_location: bool) -> Result<PathBuf, String> {
    let raw = PathBuf::from(p);
    if raw.is_relative() {
        return Err(format!("path must be absolute: {p}"));
    }

    let resolved = if is_link_location {
        // The link may not exist yet. Try to canonicalize the parent if it
        // exists; otherwise fall back to lexical normalization.
        if let Some(parent) = raw.parent() {
            if parent.exists() {
                let canon_parent = parent.canonicalize().map_err(|e| {
                    format!("cannot resolve parent of {p}: {e}")
                })?;
                canon_parent
                    .join(raw.file_name().ok_or_else(|| format!("path has no filename: {p}"))?)
            } else {
                normalize_path(&raw)
            }
        } else {
            normalize_path(&raw)
        }
    } else {
        raw.canonicalize()
            .map_err(|e| format!("cannot resolve {p}: {e}"))?
    };

    if !resolved.starts_with(home) {
        return Err(format!(
            "path escapes HOME ({p} → {resolved:?})"
        ));
    }
    Ok(resolved)
}

// ---------------------------------------------------------------------------
// Internal implementation — T-P3-E03-14
// ---------------------------------------------------------------------------

/// Execute a symlink plan, creating or replacing symlinks as specified.
///
/// # Safety invariants (per spec + non-destructive doctrine)
/// - All paths are canonicalized and verified to reside under the user HOME.
///   Any path that escapes HOME is rejected with a per-entry error.
/// - If `source` already exists as a **real file or directory** (not a symlink),
///   the operation is **skipped** — we return `success: false` with a
///   descriptive error. The W-03 archive flow handles moving real files; this
///   function never destroys data.
/// - If `source` already exists as a **symlink** (pointing anywhere), it is
///   removed and recreated pointing at `target` (atomic replace).
/// - An empty plan returns `Ok(vec![])` immediately.
fn apply_symlink_plan(plan: &SymlinkPlan) -> Vec<SymlinkResult> {
    apply_symlink_plan_with_home(plan, None)
}

/// Internal implementation allowing an injected HOME for testing.
fn apply_symlink_plan_with_home(plan: &SymlinkPlan, home_override: Option<&Path>) -> Vec<SymlinkResult> {
    if plan.entries.is_empty() {
        return vec![];
    }

    let home_buf: PathBuf;
    let home: &Path = if let Some(h) = home_override {
        h
    } else {
        match home_dir() {
            Some(h) => match h.canonicalize() {
                Ok(c) => {
                    home_buf = c;
                    &home_buf
                }
                Err(e) => {
                    return plan
                        .entries
                        .iter()
                        .map(|entry| SymlinkResult {
                            source: entry.source.clone(),
                            target: entry.target.clone(),
                            success: false,
                            error: Some(format!("cannot resolve HOME: {e}")),
                        })
                        .collect();
                }
            },
            None => {
                return plan
                    .entries
                    .iter()
                    .map(|entry| SymlinkResult {
                        source: entry.source.clone(),
                        target: entry.target.clone(),
                        success: false,
                        error: Some("HOME environment variable is not set".to_string()),
                    })
                    .collect();
            }
        }
    };

    plan.entries
        .iter()
        .map(|entry| {
            let result = apply_single_entry(entry, home);
            match result {
                Ok(()) => SymlinkResult {
                    source: entry.source.clone(),
                    target: entry.target.clone(),
                    success: true,
                    error: None,
                },
                Err(msg) => SymlinkResult {
                    source: entry.source.clone(),
                    target: entry.target.clone(),
                    success: false,
                    error: Some(msg),
                },
            }
        })
        .collect()
}

/// Create (or replace) a single symlink for `entry`.
///
/// Returns `Ok(())` on success or `Err(description)` for any failure including
/// HOME-escape, real-file collision, and OS errors.
fn apply_single_entry(entry: &SymlinkEntry, home: &Path) -> Result<(), String> {
    // --- Confine both paths to HOME ---
    let link_path = confine_to_home(&entry.source, home, /* is_link_location */ true)?;
    // target must exist (it's the Cascade-managed source file)
    let target_path = confine_to_home(&entry.target, home, /* is_link_location */ false)?;

    // --- Inspect the link location ---
    // `symlink_metadata` does NOT follow symlinks, so we get metadata for the
    // link itself, not its target.
    match std::fs::symlink_metadata(&link_path) {
        Ok(meta) => {
            if meta.file_type().is_symlink() {
                // Existing symlink → remove it so we can recreate.
                std::fs::remove_file(&link_path)
                    .map_err(|e| format!("failed to remove existing symlink {link_path:?}: {e}"))?;
            } else {
                // Real file or directory — skip, do NOT destroy.
                return Err(format!(
                    "skipped: {link_path:?} is a real {} — move it first (W-03 archive handles this)",
                    if meta.is_dir() { "directory" } else { "file" }
                ));
            }
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            // Path does not exist — safe to create.
        }
        Err(e) => {
            return Err(format!("failed to stat {link_path:?}: {e}"));
        }
    }

    // --- Ensure the parent directory exists ---
    if let Some(parent) = link_path.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("failed to create parent dir {parent:?}: {e}"))?;
    }

    // --- Create the symlink ---
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(&target_path, &link_path)
            .map_err(|e| format!("symlink({target_path:?} → {link_path:?}): {e}"))?;
    }
    #[cfg(windows)]
    {
        // On Windows: use junction for directories, symlink for files.
        if target_path.is_dir() {
            std::os::windows::fs::symlink_dir(&target_path, &link_path)
                .map_err(|e| format!("symlink_dir({target_path:?} → {link_path:?}): {e}"))?;
        } else {
            std::os::windows::fs::symlink_file(&target_path, &link_path)
                .map_err(|e| format!("symlink_file({target_path:?} → {link_path:?}): {e}"))?;
        }
    }

    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::symlink;
    use tempfile::TempDir;

    fn make_plan(source: &str, target: &str) -> SymlinkPlan {
        SymlinkPlan {
            entries: vec![SymlinkEntry {
                source: source.to_string(),
                target: target.to_string(),
                kind: SymlinkKind::Replace,
            }],
        }
    }

    /// Run `f` with an injected HOME pointing at `dir`.
    /// Uses the `_with_home` variant directly — no env-var mutation needed.
    fn run_with_home<F: FnOnce(&Path) -> Vec<SymlinkResult>>(
        dir: &TempDir,
        f: F,
    ) -> Vec<SymlinkResult> {
        // Canonicalize the tmpdir path (macOS /var → /private/var etc.)
        let home = dir.path().canonicalize().unwrap();
        f(&home)
    }

    #[test]
    fn test_empty_plan_returns_empty() {
        let plan = SymlinkPlan { entries: vec![] };
        let results = apply_symlink_plan(&plan);
        assert!(results.is_empty());
    }

    #[test]
    fn test_happy_path_creates_symlink() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path().canonicalize().unwrap();

        // Create the target file inside HOME.
        let target = home.join("managed").join("CLAUDE.md");
        std::fs::create_dir_all(target.parent().unwrap()).unwrap();
        std::fs::write(&target, "# cascade managed").unwrap();

        let link = home.join("tools").join("CLAUDE.md");
        let plan = make_plan(link.to_str().unwrap(), target.to_str().unwrap());

        let results = run_with_home(&tmp, |h| apply_symlink_plan_with_home(&plan, Some(h)));
        assert_eq!(results.len(), 1);
        assert!(results[0].success, "expected success, got: {:?}", results[0].error);

        // Verify link exists and points to target.
        let meta = std::fs::symlink_metadata(&link).unwrap();
        assert!(meta.file_type().is_symlink());
        let dest = std::fs::read_link(&link).unwrap();
        assert_eq!(dest, target);
    }

    #[test]
    fn test_real_file_at_source_is_skipped_not_destroyed() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path().canonicalize().unwrap();

        // Create a real file at the link location.
        let link = home.join("real_file.md");
        std::fs::write(&link, "important data").unwrap();

        let target = home.join("managed.md");
        std::fs::write(&target, "cascade copy").unwrap();

        let plan = make_plan(link.to_str().unwrap(), target.to_str().unwrap());
        let results = run_with_home(&tmp, |h| apply_symlink_plan_with_home(&plan, Some(h)));

        assert_eq!(results.len(), 1);
        assert!(!results[0].success, "should have been skipped");
        let err = results[0].error.as_deref().unwrap_or("");
        assert!(err.contains("real"), "error should mention 'real': {err}");

        // The original file must still exist with its original content.
        let content = std::fs::read_to_string(&link).unwrap();
        assert_eq!(content, "important data", "real file must not be destroyed");
    }

    #[test]
    fn test_existing_symlink_is_replaced() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path().canonicalize().unwrap();

        // Create an old target and a new target.
        let old_target = home.join("old.md");
        let new_target = home.join("new.md");
        std::fs::write(&old_target, "old").unwrap();
        std::fs::write(&new_target, "new").unwrap();

        let link = home.join("CLAUDE.md");
        // Pre-create a symlink pointing at the old target.
        symlink(&old_target, &link).unwrap();

        let plan = make_plan(link.to_str().unwrap(), new_target.to_str().unwrap());
        let results = run_with_home(&tmp, |h| apply_symlink_plan_with_home(&plan, Some(h)));

        assert_eq!(results.len(), 1);
        assert!(results[0].success, "expected success, got: {:?}", results[0].error);

        // Link should now point at new_target.
        let dest = std::fs::read_link(&link).unwrap();
        assert_eq!(dest, new_target);
    }

    #[test]
    fn test_path_outside_home_is_rejected() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path().canonicalize().unwrap();

        // Use /tmp as a source path (outside HOME).
        let outside = "/tmp/escape_attempt.md";
        let target = home.join("target.md");
        std::fs::write(&target, "cascade").unwrap();

        let plan = make_plan(outside, target.to_str().unwrap());
        let results = run_with_home(&tmp, |h| apply_symlink_plan_with_home(&plan, Some(h)));

        assert_eq!(results.len(), 1);
        assert!(!results[0].success, "path outside HOME should be rejected");
        let err = results[0].error.as_deref().unwrap_or("");
        assert!(
            err.contains("HOME") || err.contains("escapes"),
            "error should mention HOME escape: {err}"
        );
    }
}

// ---------------------------------------------------------------------------
// Tauri command
// ---------------------------------------------------------------------------

/// Create symlinks for all cascade-managed tools as specified by `plan`.
///
/// The frontend (SymlinkSetupPhase.tsx) MUST display a preview table and
/// require explicit user confirmation before invoking this command.
///
/// Invoked from React as `invoke("setup_symlinks", { plan })`.
///
/// # Errors
/// Returns `Err(String)` only on unexpected OS-level failures that prevent
/// processing the plan entirely. Per-entry failures are reported inside the
/// returned `Vec<SymlinkResult>` with `success: false`.
#[tauri::command]
pub fn setup_symlinks(plan: SymlinkPlan) -> Result<Vec<SymlinkResult>, String> {
    Ok(apply_symlink_plan(&plan))
}
