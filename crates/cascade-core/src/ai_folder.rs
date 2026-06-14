//! AI-folder preference resolution for the Cascade framework.
//!
//! # Purpose
//! Resolves which folder name Cascade uses for its working files at a given
//! directory. Users may prefer `.claude`, `.codex`, `.opencode`, or the
//! default `.cascade`. This module:
//!
//! 1. Reads the configured preference from config (if any).
//! 2. Falls back through a fixed priority list for auto-adoption on fresh
//!    setups: `.claude` → `.codex` → `.opencode` → `.cascade`.
//! 3. Provides helpers for the `cascade folder set` and
//!    `cascade migrate-folder` CLI commands.
//!
//! # Inputs
//! - A filesystem path to inspect.
//! - Optionally, an `AiFolder` preference already loaded from config.
//!
//! # Outputs
//! - The resolved folder name (e.g. `.cascade`).
//! - The full path to that folder under the given directory.
//!
//! # Constraints
//! - Never modifies the filesystem.
//! - Resolution is deterministic given the same directory contents.
//!
//! # SPORT
//! MASTER-CLI.md — folder: ai_folder_resolution

use cascade_types::config::AiFolder;
use std::path::{Path, PathBuf};
use tracing::debug;

/// Priority list used for auto-adoption when no configured preference exists.
///
/// The first name in this list that is found as an existing directory wins.
/// If none exist, the last entry (`.cascade`) is returned as the default.
const AUTO_ADOPT_ORDER: &[&str] = &[".claude", ".codex", ".opencode", ".cascade"];

/// Resolve the active AI folder for a given directory.
///
/// Algorithm:
/// 1. If `configured` is `Some(pref)`, use `pref.folder_name()`.
/// 2. Otherwise walk `AUTO_ADOPT_ORDER` and return the first name that
///    exists as a directory under `dir`.
/// 3. If none exist, return `.cascade` as the creation default.
///
/// This function never creates or modifies any directory. Returns only the
/// resolved folder name (e.g. `.cascade`).
pub fn resolve_folder_name(dir: &Path, configured: Option<&AiFolder>) -> String {
    if let Some(pref) = configured {
        let name = pref.folder_name().to_owned();
        debug!(dir = %dir.display(), folder = %name, "using configured ai_folder");
        return name;
    }

    for candidate in AUTO_ADOPT_ORDER {
        if dir.join(candidate).is_dir() {
            debug!(
                dir = %dir.display(),
                folder = candidate,
                "auto-adopting existing ai folder"
            );
            return (*candidate).to_owned();
        }
    }

    // Default: .cascade (last entry in AUTO_ADOPT_ORDER).
    let default = AUTO_ADOPT_ORDER[AUTO_ADOPT_ORDER.len() - 1];
    debug!(dir = %dir.display(), folder = default, "no existing ai folder; defaulting");
    default.to_owned()
}

/// Return the full path to the active AI folder under `dir`.
///
/// Convenience wrapper around [`resolve_folder_name`].
pub fn resolve_folder_path(dir: &Path, configured: Option<&AiFolder>) -> PathBuf {
    dir.join(resolve_folder_name(dir, configured))
}

/// Walk upward from `start` and find the first ancestor where an AI folder
/// exists, respecting the given preference order.
///
/// Returns `(ancestor, folder_name)` or `None` if no ancestor has any
/// known AI folder.
pub fn find_folder_upward(
    start: &Path,
    configured: Option<&AiFolder>,
) -> Option<(PathBuf, String)> {
    for ancestor in start.ancestors() {
        let name = resolve_folder_name(ancestor, configured);
        if ancestor.join(&name).is_dir() {
            return Some((ancestor.to_path_buf(), name));
        }
    }
    None
}

// ── Migration helper ──────────────────────────────────────────────────────────

/// Outcome of a folder migration attempt.
#[derive(Debug)]
pub struct MigrateResult {
    /// Absolute path of the source folder that was moved from.
    pub from: PathBuf,
    /// Absolute path of the destination folder that was created.
    pub to: PathBuf,
    /// Number of files/entries moved.
    pub entries_moved: usize,
}

/// Move the contents of `from_dir` into `to_dir`, atomically (copy → verify →
/// rename/swap → remove source). Non-destructive: aborts if `to_dir` already
/// exists and is non-empty.
///
/// When `dry_run` is `true`, prints what *would* happen without modifying
/// anything.
///
/// # Errors
///
/// Returns an error if:
/// - `from_dir` does not exist.
/// - `to_dir` already exists and is not empty.
/// - Any filesystem operation fails.
pub fn migrate_folder(
    from_dir: &Path,
    to_dir: &Path,
    dry_run: bool,
) -> cascade_types::error::Result<MigrateResult> {
    use cascade_types::error::CascadeError;

    if !from_dir.exists() {
        return Err(CascadeError::Io {
            path: from_dir.to_path_buf(),
            operation: "migrate: source folder not found",
            source: std::io::Error::new(
                std::io::ErrorKind::NotFound,
                format!("{} does not exist", from_dir.display()),
            ),
        });
    }

    // If destination already exists and is non-empty, abort.
    if to_dir.exists() {
        let is_empty = std::fs::read_dir(to_dir)
            .map(|mut d| d.next().is_none())
            .unwrap_or(false);
        if !is_empty {
            return Err(CascadeError::Io {
                path: to_dir.to_path_buf(),
                operation: "migrate: destination already exists and is non-empty",
                source: std::io::Error::new(
                    std::io::ErrorKind::AlreadyExists,
                    format!(
                        "{} already exists and is non-empty; remove it or choose a different target",
                        to_dir.display()
                    ),
                ),
            });
        }
    }

    // Collect all entries to copy.
    let entries: Vec<_> = collect_entries(from_dir).map_err(|e| CascadeError::Io {
        path: from_dir.to_path_buf(),
        operation: "migrate: collect entries",
        source: e,
    })?;

    if dry_run {
        for (src, _) in &entries {
            println!(
                "  [dry-run] move {}  →  {}",
                src.display(),
                to_dir
                    .join(src.strip_prefix(from_dir).unwrap_or(src))
                    .display()
            );
        }
        return Ok(MigrateResult {
            from: from_dir.to_path_buf(),
            to: to_dir.to_path_buf(),
            entries_moved: entries.len(),
        });
    }

    // Step 1: Copy-then-verify into a temp staging directory.
    let staging = to_dir.with_extension(".cascade-migrate-tmp");
    if staging.exists() {
        std::fs::remove_dir_all(&staging).map_err(|e| CascadeError::Io {
            path: staging.clone(),
            operation: "remove stale staging dir",
            source: e,
        })?;
    }

    copy_tree(from_dir, &staging).map_err(|e| CascadeError::Io {
        path: staging.clone(),
        operation: "copy to staging",
        source: e,
    })?;

    // Step 2: Rename staging → destination.
    std::fs::rename(&staging, to_dir).map_err(|e| CascadeError::Io {
        path: to_dir.to_path_buf(),
        operation: "rename staging to destination",
        source: e,
    })?;

    // Step 3: Remove source.
    std::fs::remove_dir_all(from_dir).map_err(|e| CascadeError::Io {
        path: from_dir.to_path_buf(),
        operation: "remove source after migration",
        source: e,
    })?;

    // Step 4: Re-create symlinks (CLAUDE.md, AGENTS.md) in the new folder if
    // the new folder contains CASCADE.md.
    let cascade_md = to_dir.join("CASCADE.md");
    if cascade_md.exists() {
        let _ = crate::symlinks::create_siblings(to_dir, true);
    }

    Ok(MigrateResult {
        from: from_dir.to_path_buf(),
        to: to_dir.to_path_buf(),
        entries_moved: entries.len(),
    })
}

// ── Private helpers ───────────────────────────────────────────────────────────

/// Recursively collect all (path, is_dir) entries under `root`.
fn collect_entries(root: &Path) -> Result<Vec<(PathBuf, bool)>, std::io::Error> {
    let mut out = Vec::new();
    for entry in std::fs::read_dir(root)? {
        let entry = entry?;
        let path = entry.path();
        let is_dir = path.is_dir();
        out.push((path.clone(), is_dir));
        if is_dir {
            out.extend(collect_entries(&path)?);
        }
    }
    Ok(out)
}

/// Recursively copy `src` directory tree into `dst`.
fn copy_tree(src: &Path, dst: &Path) -> Result<(), std::io::Error> {
    std::fs::create_dir_all(dst)?;
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let src_path = entry.path();
        let dst_path = dst.join(entry.file_name());
        if src_path.is_dir() {
            copy_tree(&src_path, &dst_path)?;
        } else if src_path.is_symlink() {
            let target = std::fs::read_link(&src_path)?;
            #[cfg(unix)]
            std::os::unix::fs::symlink(&target, &dst_path)?;
            #[cfg(not(unix))]
            std::fs::copy(&src_path, &dst_path).map(|_| ())?;
        } else {
            std::fs::copy(&src_path, &dst_path)?;
        }
    }
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    // Helper: create a directory under `root`.
    fn mkdir(root: &Path, name: &str) -> PathBuf {
        let p = root.join(name);
        fs::create_dir_all(&p).unwrap();
        p
    }

    // ── resolution precedence ─────────────────────────────────────────────────

    #[test]
    fn configured_preference_wins_over_existing() {
        let tmp = TempDir::new().unwrap();
        // Both .claude and .cascade exist.
        mkdir(tmp.path(), ".claude");
        mkdir(tmp.path(), ".cascade");

        // Configured: codex (even though .codex does not exist).
        let pref = AiFolder::Codex;
        let name = resolve_folder_name(tmp.path(), Some(&pref));
        assert_eq!(name, ".codex");
    }

    #[test]
    fn auto_adopt_claude_when_present() {
        let tmp = TempDir::new().unwrap();
        mkdir(tmp.path(), ".claude");
        // No .cascade.

        let name = resolve_folder_name(tmp.path(), None);
        assert_eq!(name, ".claude");
    }

    #[test]
    fn auto_adopt_codex_when_only_codex_present() {
        let tmp = TempDir::new().unwrap();
        mkdir(tmp.path(), ".codex");

        let name = resolve_folder_name(tmp.path(), None);
        assert_eq!(name, ".codex");
    }

    #[test]
    fn auto_adopt_opencode_when_no_claude_codex() {
        let tmp = TempDir::new().unwrap();
        mkdir(tmp.path(), ".opencode");

        let name = resolve_folder_name(tmp.path(), None);
        assert_eq!(name, ".opencode");
    }

    #[test]
    fn default_cascade_when_none_exist() {
        let tmp = TempDir::new().unwrap();
        // Empty directory — no AI folder present.

        let name = resolve_folder_name(tmp.path(), None);
        assert_eq!(name, ".cascade");
    }

    #[test]
    fn claude_beats_codex_in_auto_order() {
        let tmp = TempDir::new().unwrap();
        mkdir(tmp.path(), ".claude");
        mkdir(tmp.path(), ".codex");

        let name = resolve_folder_name(tmp.path(), None);
        assert_eq!(name, ".claude");
    }

    #[test]
    #[serial(global_env)]
    fn resolve_folder_path_returns_absolute() {
        let tmp = TempDir::new().unwrap();
        mkdir(tmp.path(), ".cascade");

        let path = resolve_folder_path(tmp.path(), None);
        assert_eq!(path, tmp.path().join(".cascade"));
    }

    // ── AiFolder helpers ──────────────────────────────────────────────────────

    #[test]
    fn ai_folder_from_str_loose_with_dot() {
        assert_eq!(AiFolder::from_str_loose(".claude"), AiFolder::Claude);
        assert_eq!(AiFolder::from_str_loose(".cascade"), AiFolder::Cascade);
        assert_eq!(AiFolder::from_str_loose(".codex"), AiFolder::Codex);
    }

    #[test]
    fn ai_folder_from_str_loose_without_dot() {
        assert_eq!(AiFolder::from_str_loose("claude"), AiFolder::Claude);
        assert_eq!(AiFolder::from_str_loose("cascade"), AiFolder::Cascade);
    }

    #[test]
    fn ai_folder_custom_preserves_dot() {
        let f = AiFolder::from_str_loose("myai");
        assert_eq!(f.folder_name(), ".myai");
    }

    // ── migrate_folder ────────────────────────────────────────────────────────

    #[test]
    fn migrate_moves_contents_and_removes_source() {
        let tmp = TempDir::new().unwrap();
        let from = tmp.path().join(".claude");
        let to = tmp.path().join(".cascade");

        fs::create_dir_all(&from).unwrap();
        fs::write(from.join("CASCADE.md"), "# hello\n").unwrap();
        fs::create_dir_all(from.join("memory")).unwrap();
        fs::write(from.join("memory").join("decisions.md"), "decided\n").unwrap();

        let result = migrate_folder(&from, &to, false).unwrap();

        // Source removed, destination populated.
        assert!(!from.exists(), "source should be gone");
        assert!(to.exists(), "destination should exist");
        assert!(
            to.join("CASCADE.md").exists(),
            "CASCADE.md should be present"
        );
        assert!(to.join("memory").join("decisions.md").exists());
        // entries_moved: CASCADE.md + memory/ + memory/decisions.md = 3
        assert!(
            result.entries_moved >= 2,
            "expected at least 2 entries moved"
        );
    }

    #[test]
    fn migrate_dry_run_leaves_source_intact() {
        let tmp = TempDir::new().unwrap();
        let from = tmp.path().join(".claude");
        let to = tmp.path().join(".cascade");

        fs::create_dir_all(&from).unwrap();
        fs::write(from.join("CASCADE.md"), "# dry\n").unwrap();

        let result = migrate_folder(&from, &to, true).unwrap();
        assert!(from.exists(), "source must survive dry-run");
        assert!(!to.exists(), "destination must not be created in dry-run");
        assert_eq!(result.entries_moved, 1);
    }

    #[test]
    fn migrate_errors_on_missing_source() {
        let tmp = TempDir::new().unwrap();
        let from = tmp.path().join(".nonexistent");
        let to = tmp.path().join(".cascade");
        let err = migrate_folder(&from, &to, false);
        assert!(err.is_err(), "should error when source is missing");
    }

    #[test]
    fn migrate_errors_on_non_empty_destination() {
        let tmp = TempDir::new().unwrap();
        let from = tmp.path().join(".claude");
        let to = tmp.path().join(".cascade");

        fs::create_dir_all(&from).unwrap();
        fs::write(from.join("a.md"), "content\n").unwrap();

        // Create a non-empty destination.
        fs::create_dir_all(&to).unwrap();
        fs::write(to.join("existing.md"), "existing\n").unwrap();

        let err = migrate_folder(&from, &to, false);
        assert!(err.is_err(), "should error when destination is non-empty");
    }

    #[test]
    fn find_folder_upward_finds_parent() {
        let tmp = TempDir::new().unwrap();
        let parent = tmp.path();
        let child = parent.join("sub").join("deep");
        fs::create_dir_all(&child).unwrap();
        fs::create_dir_all(parent.join(".claude")).unwrap();

        let result = find_folder_upward(&child, None);
        assert!(result.is_some());
        let (_anc, name) = result.unwrap();
        assert_eq!(name, ".claude");
    }
}
