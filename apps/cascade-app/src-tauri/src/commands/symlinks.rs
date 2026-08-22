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

use notify::Watcher;
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
                let canon_parent = parent
                    .canonicalize()
                    .map_err(|e| format!("cannot resolve parent of {p}: {e}"))?;
                canon_parent.join(
                    raw.file_name()
                        .ok_or_else(|| format!("path has no filename: {p}"))?,
                )
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
        return Err(format!("path escapes HOME ({p} → {resolved:?})"));
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
fn apply_symlink_plan_with_home(
    plan: &SymlinkPlan,
    home_override: Option<&Path>,
) -> Vec<SymlinkResult> {
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
#[cfg(unix)] // unix-only: builds real symlinks via std::os::unix::fs::symlink
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
        assert!(
            results[0].success,
            "expected success, got: {:?}",
            results[0].error
        );

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
        assert!(
            results[0].success,
            "expected success, got: {:?}",
            results[0].error
        );

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
// Tauri command — setup_symlinks
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

// ---------------------------------------------------------------------------
// cascade_unlink_tool — T-P3-E03-37
// ---------------------------------------------------------------------------

/// Sibling filenames that each tool uses for symlink-based cascade integration.
///
/// Must stay in sync with `TOOL_SIBLING_FILES` in `wizard/lib/symlinks.ts`.
fn tool_sibling_files(tool_id: &str) -> &'static [&'static str] {
    match tool_id {
        "claude-code" => &["CLAUDE.md", "AGENTS.md"],
        "opencode" => &["AGENTS.md"],
        "cursor" => &[".cursorrules"],
        "aider" => &[".aider.conf.yml"],
        "windsurf" => &[".windsurf"],
        "antigravity" => &[".antigravity"],
        "codex" => &["CODEX.md"],
        _ => &[],
    }
}

/// Result of a cascade_unlink_tool command.
///
/// `removed` — absolute paths of symlinks that were removed.
/// `skipped` — paths that existed but were real files (not symlinks); left untouched.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UnlinkResult {
    pub removed: Vec<String>,
    pub skipped: Vec<String>,
}

/// Remove all sibling symlinks pointing to CASCADE.md for a given tool.
///
/// # Purpose
/// Switches a tool from Cascade-managed to Independent by removing the symlinks
/// that were created by setup_symlinks / cascade_link_tool. Never removes a real
/// (non-symlink) file — those are placed in `skipped`.
///
/// # Inputs
/// - `tool_id`: kebab-case tool identifier, e.g. `"claude-code"`.
///
/// # Outputs
/// `UnlinkResult { removed, skipped }` — lists every path examined.
///
/// # Constraints
/// - All paths are confined to HOME (same policy as setup_symlinks).
/// - Non-symlink files are never removed; they appear in `skipped`.
/// - Only symlinks whose name matches a known sibling filename for the tool
///   are considered, regardless of what they point to.
/// - Returns `Err(String)` only when HOME cannot be resolved.
///
/// # SPORT
/// MASTER-COMMANDS.md — cascade_unlink_tool — T-P3-E03-37
#[tauri::command]
pub fn cascade_unlink_tool(tool_id: String) -> Result<UnlinkResult, String> {
    cascade_unlink_tool_with_home(&tool_id, None)
}

/// Internal implementation accepting an optional HOME override for testing.
pub(crate) fn cascade_unlink_tool_with_home(
    tool_id: &str,
    home_override: Option<&std::path::Path>,
) -> Result<UnlinkResult, String> {
    use std::path::Path;

    let home_buf: std::path::PathBuf;
    let home: &Path = if let Some(h) = home_override {
        h
    } else {
        match home_dir() {
            Some(h) => match h.canonicalize() {
                Ok(c) => {
                    home_buf = c;
                    &home_buf
                }
                Err(e) => return Err(format!("cannot resolve HOME: {e}")),
            },
            None => return Err("HOME environment variable is not set".to_string()),
        }
    };

    let siblings = tool_sibling_files(tool_id);
    let mut removed: Vec<String> = Vec::new();
    let mut skipped: Vec<String> = Vec::new();

    // Walk the standard Cascade tier directories under HOME.
    // Each tier is a directory that may contain sibling symlinks.
    let cascade_root = home.join(".cascade");
    let tier_dirs = [
        cascade_root.join("gci"),
        cascade_root.join("pci"),
        cascade_root.join("apc"),
        cascade_root.join("ppc"),
        cascade_root.join("prc"),
        cascade_root.join("pac"),
    ];

    for tier_dir in &tier_dirs {
        if !tier_dir.exists() {
            continue;
        }
        for filename in siblings {
            let candidate = tier_dir.join(filename);
            match std::fs::symlink_metadata(&candidate) {
                Ok(meta) => {
                    if meta.file_type().is_symlink() {
                        match std::fs::remove_file(&candidate) {
                            Ok(()) => removed.push(candidate.to_string_lossy().into_owned()),
                            Err(e) => skipped.push(format!(
                                "{} (remove failed: {e})",
                                candidate.to_string_lossy()
                            )),
                        }
                    } else {
                        // Real file — skip; never destroy user data.
                        skipped.push(candidate.to_string_lossy().into_owned());
                    }
                }
                // File does not exist — nothing to remove.
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                Err(e) => skipped.push(format!(
                    "{} (stat failed: {e})",
                    candidate.to_string_lossy()
                )),
            }
        }
    }

    Ok(UnlinkResult { removed, skipped })
}

// ---------------------------------------------------------------------------
// Filesystem primitives — symlink_create / file_write / file_copy_create
// ---------------------------------------------------------------------------

/// Create a symlink at `target` pointing to `source`.
///
/// # Purpose
/// Low-level single-symlink command used by `symlinks.ts` `executeSymlinkPlan`
/// for the non-dry-run execution path.  Differs from `setup_symlinks` in that
/// it operates on exactly one path pair with no HOME-confinement check — the
/// caller (TS layer) is responsible for supplying safe, already-resolved paths.
///
/// # Behaviour
/// - If `target` already exists as a **symlink** (any destination), it is
///   removed first and recreated pointing at `source`.
/// - If `target` already exists as a **real file or directory**, returns an
///   error — never overwrites real data.
/// - Creates parent directories of `target` if they do not exist.
///
/// # Inputs
/// - `source`: absolute path the new symlink will point to (link target).
/// - `target`: absolute path where the symlink will be created (link location).
///
/// # JS
/// `invoke("symlink_create", { source, target })`
#[tauri::command]
pub async fn symlink_create(source: String, target: String) -> Result<(), String> {
    let src = std::path::Path::new(&source);
    let tgt = std::path::Path::new(&target);

    // Inspect whatever is at the target location (without following symlinks).
    match std::fs::symlink_metadata(tgt) {
        Ok(meta) => {
            if meta.file_type().is_symlink() {
                // Existing symlink — remove it so we can recreate.
                std::fs::remove_file(tgt)
                    .map_err(|e| format!("failed to remove existing symlink {target:?}: {e}"))?;
            } else {
                // Real file or directory — refuse to overwrite.
                return Err(format!(
                    "skipped: {target:?} is a real {} — move it first",
                    if meta.is_dir() { "directory" } else { "file" }
                ));
            }
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            // Nothing there yet — proceed.
        }
        Err(e) => return Err(format!("failed to stat {target:?}: {e}")),
    }

    // Ensure parent directory exists.
    if let Some(parent) = tgt.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("failed to create parent dir {parent:?}: {e}"))?;
    }

    // Create the platform symlink.
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(src, tgt)
            .map_err(|e| format!("symlink({source:?} → {target:?}): {e}"))?;
    }
    #[cfg(windows)]
    {
        if src.is_dir() {
            std::os::windows::fs::symlink_dir(src, tgt)
                .map_err(|e| format!("symlink_dir({source:?} → {target:?}): {e}"))?;
        } else {
            std::os::windows::fs::symlink_file(src, tgt)
                .map_err(|e| format!("symlink_file({source:?} → {target:?}): {e}"))?;
        }
    }

    Ok(())
}

/// Write UTF-8 `content` to `path`, creating parent directories as needed.
///
/// # Purpose
/// Used by `symlinks.ts` `generateDerivedFile` when producing cursor rules
/// (`.cursorrules`), opencode config, and similar derived files that must be
/// real files rather than symlinks.
///
/// # Behaviour
/// - Creates all missing parent directories before writing.
/// - Uses an atomic tmp-then-rename write to avoid partial reads on concurrent
///   access: writes to `<path>.tmp` then renames over `<path>`.
///
/// # Inputs
/// - `path`: absolute destination path.
/// - `content`: UTF-8 text to write.
///
/// # JS
/// `invoke("file_write", { path, content })`
#[tauri::command]
pub async fn file_write(path: String, content: String) -> Result<(), String> {
    let dest = std::path::Path::new(&path);

    // Create parent directories if they do not exist.
    if let Some(parent) = dest.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("failed to create parent dir {parent:?}: {e}"))?;
    }

    // Atomic write: write to a sibling .tmp file then rename into place.
    let mut tmp_name = dest.as_os_str().to_owned();
    tmp_name.push(".tmp");
    let tmp_path = std::path::PathBuf::from(tmp_name);

    std::fs::write(&tmp_path, content.as_bytes())
        .map_err(|e| format!("failed to write to {tmp_path:?}: {e}"))?;

    std::fs::rename(&tmp_path, dest)
        .map_err(|e| format!("failed to rename {tmp_path:?} → {path:?}: {e}"))?;

    Ok(())
}

/// Copy a file from `source` to `target`.
///
/// # Purpose
/// Windows fallback used by `symlinks.ts` `executeFileCopy` when creating
/// symlinks is not available or requires elevated privileges.  On macOS/Linux
/// this command is still available but symlink creation is preferred.
///
/// # Behaviour
/// - Creates parent directories of `target` if they do not exist.
/// - Uses `std::fs::copy` which overwrites an existing `target` file.
///
/// # Inputs
/// - `source`: absolute path of the file to copy.
/// - `target`: absolute destination path.
///
/// # JS
/// `invoke("file_copy_create", { source, target })`
#[tauri::command]
pub async fn file_copy_create(source: String, target: String) -> Result<(), String> {
    let tgt = std::path::Path::new(&target);

    if let Some(parent) = tgt.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| format!("failed to create parent dir {parent:?}: {e}"))?;
    }

    std::fs::copy(&source, &target)
        .map(|_| ())
        .map_err(|e| format!("copy({source:?} → {target:?}): {e}"))
}

/// Checks that a symlink path exists and points to a valid target.
#[tauri::command]
pub async fn symlink_integrity_check(path: String) -> Result<bool, String> {
    let p = std::path::Path::new(&path);
    if !p.exists() && p.symlink_metadata().is_err() {
        return Ok(false);
    }
    if let Ok(meta) = p.symlink_metadata() {
        if meta.file_type().is_symlink() {
            return Ok(std::fs::read_link(p)
                .ok()
                .map(|t| t.exists())
                .unwrap_or(false));
        }
    }
    Ok(p.exists())
}

/// Registers a source→target file watch so the target is re-copied whenever
/// the source changes.
///
/// Uses `notify::RecommendedWatcher` — the same underlying watcher library
/// as the daemon's `rag_watcher` (crates/cascade-daemon/src/rag_watcher.rs).
/// The watcher is leaked (Box::leak) so it lives for the process lifetime,
/// matching the pattern established by `vault_watch` in commands/vault.rs.
///
/// JS: `invoke("daemon_watch_register", { source, target })`
///
/// # Errors
///
/// Returns a string error if the source path does not exist or the watcher
/// cannot be initialised.
#[tauri::command]
pub async fn daemon_watch_register(source: String, target: String) -> Result<(), String> {
    let src_path = PathBuf::from(&source);
    let tgt_path = PathBuf::from(&target);

    // Validate source exists.
    if !src_path.exists() {
        return Err(format!("source path does not exist: {source}"));
    }

    // Perform an initial copy so the target is in sync immediately.
    copy_file_to_target(&src_path, &tgt_path).map_err(|e| format!("initial copy failed: {e}"))?;

    // Set up the watcher — on source change, re-copy to target.
    //
    // WHY canonicalize watch_src here: on macOS, FSEvents reports
    // canonicalized paths (e.g. /var/folders/... -> /private/var/folders/...
    // since /var is itself a symlink to /private/var). Comparing the raw,
    // non-canonicalized src_path against event.paths would silently never
    // match on macOS, making the watcher a permanent no-op. Canonicalize
    // once at setup time (stable for the watch's lifetime) rather than
    // per-event, and fall back to the raw path if canonicalization fails
    // (e.g. the file is deleted between the initial copy and watch setup).
    let watch_src = src_path.canonicalize().unwrap_or_else(|_| src_path.clone());
    let watch_tgt = tgt_path.clone();
    let mut watcher = notify::RecommendedWatcher::new(
        move |res: notify::Result<notify::Event>| {
            if let Ok(event) = res {
                let is_content_event = matches!(
                    event.kind,
                    notify::EventKind::Create(_) | notify::EventKind::Modify(_)
                );
                if !is_content_event {
                    return;
                }
                // Check if any changed path is the source file. Canonicalize
                // each event path before comparing, since the OS may report
                // either the canonical or non-canonical form depending on
                // platform and backend.
                for path in &event.paths {
                    let event_path = path.canonicalize().unwrap_or_else(|_| path.clone());
                    if event_path == watch_src {
                        if let Err(e) = copy_file_to_target(&watch_src, &watch_tgt) {
                            tracing::warn!("daemon_watch_register: copy failed: {e}");
                        }
                        break;
                    }
                }
            }
        },
        notify::Config::default(),
    )
    .map_err(|e| format!("failed to create watcher: {e}"))?;

    // Watch the source file (non-recursive — it's a single file).
    // If the source is a directory, watch it recursively.
    let mode = if src_path.is_dir() {
        notify::RecursiveMode::Recursive
    } else {
        notify::RecursiveMode::NonRecursive
    };
    watcher
        .watch(&src_path, mode)
        .map_err(|e| format!("failed to watch source: {e}"))?;

    // Leak the watcher so it lives for the process lifetime.
    // SAFETY: the watcher holds only OS file handles and the closure captures
    // (PathBuf values) — no raw pointer unsafety.
    Box::leak(Box::new(watcher));

    tracing::info!("daemon_watch_register: watching {source} → {target}");
    Ok(())
}

/// Copy the source file to the target path, creating parent directories if
/// needed. This is the core copy logic shared between the initial copy and
/// the watcher callback.
fn copy_file_to_target(source: &Path, target: &Path) -> std::io::Result<()> {
    // Create parent directories for the target if needed.
    if let Some(parent) = target.parent() {
        std::fs::create_dir_all(parent)?;
    }
    std::fs::copy(source, target)?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests — cascade_unlink_tool (T-P3-E03-37)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod unlink_tests {
    use super::*;
    use std::os::unix::fs::symlink;
    use tempfile::TempDir;

    fn setup_cascade_tier(home: &std::path::Path, tier: &str) -> std::path::PathBuf {
        let dir = home.join(".cascade").join(tier);
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    #[test]
    fn test_unlink_removes_symlinks() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path().canonicalize().unwrap();

        // Create a fake CASCADE.md target.
        let cascade_md = home.join("CASCADE.md");
        std::fs::write(&cascade_md, "# cascade").unwrap();

        // Create a GCI tier dir with a CLAUDE.md symlink.
        let gci = setup_cascade_tier(&home, "gci");
        symlink(&cascade_md, gci.join("CLAUDE.md")).unwrap();
        symlink(&cascade_md, gci.join("AGENTS.md")).unwrap();

        let result = cascade_unlink_tool_with_home("claude-code", Some(&home)).unwrap();
        assert_eq!(result.removed.len(), 2, "should remove 2 symlinks");
        assert!(result.skipped.is_empty(), "should skip nothing");

        // Verify symlinks are gone.
        assert!(!gci.join("CLAUDE.md").exists());
        assert!(!gci.join("AGENTS.md").exists());
    }

    #[test]
    fn test_unlink_skips_real_files() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path().canonicalize().unwrap();

        // Create a GCI tier dir with a real CLAUDE.md (not a symlink).
        let gci = setup_cascade_tier(&home, "gci");
        std::fs::write(gci.join("CLAUDE.md"), "real file — do not delete").unwrap();

        let result = cascade_unlink_tool_with_home("claude-code", Some(&home)).unwrap();
        assert!(result.removed.is_empty(), "real file must not be removed");
        assert_eq!(
            result.skipped.len(),
            1,
            "real file should appear in skipped"
        );

        // File must still exist with its original content.
        let content = std::fs::read_to_string(gci.join("CLAUDE.md")).unwrap();
        assert_eq!(content, "real file — do not delete");
    }

    #[test]
    fn test_unlink_unknown_tool_is_noop() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path().canonicalize().unwrap();

        let result = cascade_unlink_tool_with_home("not-a-real-tool", Some(&home)).unwrap();
        assert!(result.removed.is_empty());
        assert!(result.skipped.is_empty());
    }
}

// ---------------------------------------------------------------------------
// Tests — daemon_watch_register (T-P7-E08-05)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod watch_tests {
    use super::*;
    use std::time::{Duration, Instant};
    use tempfile::TempDir;

    #[test]
    fn copy_file_to_target_creates_parent_dirs() {
        let tmp = TempDir::new().unwrap();
        let src = tmp.path().join("source.md");
        let tgt = tmp.path().join("subdir").join("target.md");
        std::fs::write(&src, "# content").unwrap();

        copy_file_to_target(&src, &tgt).unwrap();
        assert!(tgt.exists());
        assert_eq!(std::fs::read_to_string(&tgt).unwrap(), "# content");
    }

    #[test]
    fn copy_file_to_target_overwrites_existing() {
        let tmp = TempDir::new().unwrap();
        let src = tmp.path().join("source.md");
        let tgt = tmp.path().join("target.md");
        std::fs::write(&src, "new content").unwrap();
        std::fs::write(&tgt, "old content").unwrap();

        copy_file_to_target(&src, &tgt).unwrap();
        assert_eq!(std::fs::read_to_string(&tgt).unwrap(), "new content");
    }

    #[test]
    fn daemon_watch_register_rejects_missing_source() {
        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        rt.block_on(async {
            let result = daemon_watch_register(
                "/nonexistent/path/source.md".to_string(),
                "/tmp/target.md".to_string(),
            )
            .await;
            assert!(result.is_err(), "missing source should fail");
        });
    }

    #[test]
    fn daemon_watch_register_initial_copy_and_watch_fires() {
        // Integration test: set up a watcher, modify the source, verify the
        // target is updated. Uses polling with a timeout to handle async
        // watcher timing.
        let tmp = TempDir::new().unwrap();
        let src = tmp.path().join("source.md");
        let tgt = tmp.path().join("target.md");
        std::fs::write(&src, "initial content").unwrap();

        let src_str = src.to_str().unwrap().to_string();
        let tgt_str = tgt.to_str().unwrap().to_string();

        let rt = tokio::runtime::Builder::new_current_thread()
            .enable_all()
            .build()
            .unwrap();
        rt.block_on(async {
            // Register the watch — this also does an initial copy.
            daemon_watch_register(src_str, tgt_str)
                .await
                .expect("watch register should succeed");
        });

        // Verify the initial copy happened.
        assert!(tgt.exists(), "target should exist after initial copy");
        assert_eq!(std::fs::read_to_string(&tgt).unwrap(), "initial content");

        // Modify the source file.
        std::fs::write(&src, "updated content").unwrap();

        // Poll for the target to be updated (watcher is async).
        let deadline = Instant::now() + Duration::from_secs(5);
        let mut updated = false;
        while Instant::now() < deadline {
            if tgt.exists()
                && std::fs::read_to_string(&tgt).unwrap_or_default() == "updated content"
            {
                updated = true;
                break;
            }
            std::thread::sleep(Duration::from_millis(100));
        }
        assert!(
            updated,
            "target should be updated after source change within 5s"
        );
    }
}
