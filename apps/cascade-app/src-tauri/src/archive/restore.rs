// archive::restore — Restore engine: moves archived files back to original paths.
//
// Purpose: Reads manifest.json, finds the ToolArchive for the requested tool,
//   and moves each ArchivedFile back to its originalPath. Atomic manifest update
//   removes the tool entry after successful restore.
//
// Inputs:  tool_id (String), overwrite_existing (bool), manifest path.
// Outputs: RestoreResult { restored_files, skipped_conflicts, errors }.
//
// Constraints:
//   - Cross-filesystem fallback (copy + delete) same as archive engine.
//   - Conflict (originalPath already exists): skip if overwrite_existing=false;
//     backup to {path}.cascade-backup if overwrite_existing=true.
//   - Manifest update is atomic (.tmp + rename).
//   - Archive root directory removed if empty after restore.
//   - CLI crate calls restore_core() directly (no Tauri dep).
//
// SPORT: MASTER-COMPONENTS.md — restore_tool command — archive/restore.rs
// Task: T-P3-E03-19

use std::path::{Path, PathBuf};

use crate::archive::types::{ArchiveManifest, RestoreResult};

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/// Core restore logic shared between the Tauri command and CLI crate.
///
/// # Arguments
/// * `tool_id` — kebab-case tool identifier (e.g. `"claude-code"`).
/// * `overwrite_existing` — if `true`, back up conflicting files to
///   `{original_path}.cascade-backup` before overwriting.
///
/// # Returns
/// `Ok(RestoreResult)` summarizing restored, skipped, and errored files.
/// `Err(String)` if the manifest cannot be read or the tool entry is not found.
pub fn restore_core(tool_id: String, overwrite_existing: bool) -> Result<RestoreResult, String> {
    let manifest_path = resolve_manifest_path()?;
    restore_from_manifest(&tool_id, overwrite_existing, &manifest_path)
}

// ---------------------------------------------------------------------------
// Internal implementation (also used by CLI's duplicate copy)
// ---------------------------------------------------------------------------

/// Resolve `~/.cascade/legacy/manifest.json`.
pub(crate) fn resolve_manifest_path() -> Result<PathBuf, String> {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| "HOME env var not set".to_string())?;
    Ok(home.join(".cascade").join("legacy").join("manifest.json"))
}

/// Execute the restore from a caller-supplied manifest path.
///
/// Factored out for testability — tests can supply a temp-dir manifest path
/// without touching HOME.
pub fn restore_from_manifest(
    tool_id: &str,
    overwrite_existing: bool,
    manifest_path: &Path,
) -> Result<RestoreResult, String> {
    // 1. Read and parse the manifest.
    let raw = std::fs::read_to_string(manifest_path).map_err(|e| {
        format!(
            "failed to read manifest at {}: {e}",
            manifest_path.display()
        )
    })?;
    let mut manifest: ArchiveManifest =
        serde_json::from_str(&raw).map_err(|e| format!("corrupt manifest — invalid JSON: {e}"))?;

    // 2. Find the ToolArchive entry for tool_id.
    let tool_idx = manifest
        .tools
        .iter()
        .position(|t| t.tool_id == tool_id)
        .ok_or_else(|| format!("tool '{tool_id}' not found in manifest"))?;
    let tool_archive = manifest.tools[tool_idx].clone();

    // 3. Restore each archived file.
    let mut restored_files: usize = 0;
    let mut skipped_conflicts: usize = 0;
    let mut errors: Vec<String> = Vec::new();

    for entry in &tool_archive.files {
        let original = &entry.original_path;
        let archived = &entry.archived_path;

        // Safety: canonicalize-then-check HOME confinement on the original path.
        if let Err(e) = assert_home_confined(original) {
            errors.push(format!(
                "SECURITY: original path '{}' not HOME-confined — skipped: {e}",
                original.display()
            ));
            continue;
        }

        // Check whether the archived file still exists.
        if !archived.exists() {
            errors.push(format!(
                "archived file not found at '{}' — skipped",
                archived.display()
            ));
            continue;
        }

        // Conflict check: original path already exists.
        if original.exists() {
            if !overwrite_existing {
                skipped_conflicts += 1;
                continue;
            }
            // overwrite_existing=true: backup the existing file first.
            let backup_path = backup_path_for(original);
            if let Err(e) = move_file(original, &backup_path) {
                errors.push(format!(
                    "failed to backup '{}' to '{}': {e}",
                    original.display(),
                    backup_path.display()
                ));
                continue;
            }
        }

        // Create parent directories if needed.
        if let Some(parent) = original.parent() {
            if let Err(e) = std::fs::create_dir_all(parent) {
                errors.push(format!(
                    "failed to create parent dir '{}': {e}",
                    parent.display()
                ));
                continue;
            }
        }

        // Move the archived file back to the original path.
        match move_file(archived, original) {
            Ok(()) => restored_files += 1,
            Err(e) => errors.push(format!(
                "failed to restore '{}' -> '{}': {e}",
                archived.display(),
                original.display()
            )),
        }
    }

    // 4. Remove archive root directory if now empty.
    let archive_root = &tool_archive.archive_root;
    if archive_root.exists() {
        let _ = remove_dir_if_empty(archive_root);
    }

    // 5. Update manifest atomically: remove the restored tool entry.
    manifest.tools.remove(tool_idx);
    write_manifest_atomic(&manifest, manifest_path)?;

    Ok(RestoreResult {
        restored_files,
        skipped_conflicts,
        errors,
    })
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Assert the path is within $HOME (HOME-confinement safety check).
fn assert_home_confined(path: &Path) -> Result<(), String> {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| "HOME env var not set".to_string())?;
    if !path.starts_with(&home) {
        return Err(format!(
            "path '{}' is outside HOME '{}'",
            path.display(),
            home.display()
        ));
    }
    Ok(())
}

/// Return the backup path: `{path}.cascade-backup`.
fn backup_path_for(path: &Path) -> PathBuf {
    let mut s = path.as_os_str().to_owned();
    s.push(".cascade-backup");
    PathBuf::from(s)
}

/// Move `src` to `dst`. Uses `rename` first; falls back to copy+delete on
/// cross-filesystem errors (same pattern as archive::engine).
fn move_file(src: &Path, dst: &Path) -> Result<(), String> {
    match std::fs::rename(src, dst) {
        Ok(()) => Ok(()),
        Err(e) if is_cross_device_error(&e) => {
            std::fs::copy(src, dst).map_err(|ce| format!("cross-fs copy failed: {ce}"))?;
            std::fs::remove_file(src).map_err(|re| format!("cross-fs remove failed: {re}"))?;
            Ok(())
        }
        Err(e) => Err(format!("rename failed: {e}")),
    }
}

/// Detect EXDEV (cross-device rename) errors portably.
fn is_cross_device_error(e: &std::io::Error) -> bool {
    e.raw_os_error() == Some(libc::EXDEV)
}

/// Remove a directory only if it is empty.
fn remove_dir_if_empty(dir: &Path) -> Result<(), String> {
    let is_empty = std::fs::read_dir(dir)
        .map_err(|e| format!("read_dir failed: {e}"))?
        .next()
        .is_none();
    if is_empty {
        std::fs::remove_dir(dir).map_err(|e| format!("remove_dir failed: {e}"))?;
    }
    Ok(())
}

/// Write manifest atomically: serialize to `.tmp`, then rename.
fn write_manifest_atomic(manifest: &ArchiveManifest, path: &Path) -> Result<(), String> {
    let tmp_path = path.with_extension("json.tmp");
    let json =
        serde_json::to_string_pretty(manifest).map_err(|e| format!("serialize failed: {e}"))?;
    std::fs::write(&tmp_path, &json).map_err(|e| format!("write tmp manifest failed: {e}"))?;
    std::fs::rename(&tmp_path, path).map_err(|e| format!("atomic rename manifest failed: {e}"))?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use crate::archive::types::{ArchiveManifest, ArchivedFile, ToolArchive};
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    /// Build a test manifest with one tool entry containing one file.
    fn make_manifest(
        tool_id: &str,
        original_path: PathBuf,
        archived_path: PathBuf,
    ) -> ArchiveManifest {
        ArchiveManifest {
            version: "1.0".to_string(),
            created_at: "2026-06-09T00:00:00Z".to_string(),
            tools: vec![ToolArchive {
                tool_id: tool_id.to_string(),
                original_root: original_path.parent().unwrap().to_path_buf(),
                archive_root: archived_path.parent().unwrap().to_path_buf(),
                archived_at: "2026-06-09T00:01:00Z".to_string(),
                files: vec![ArchivedFile {
                    original_path,
                    archived_path,
                    moved_at: "2026-06-09T00:01:00Z".to_string(),
                    size_bytes: 42,
                }],
            }],
        }
    }

    /// Write a manifest to `dir/manifest.json`.
    fn write_manifest(dir: &Path, manifest: &ArchiveManifest) -> PathBuf {
        let path = dir.join("manifest.json");
        let json = serde_json::to_string_pretty(manifest).unwrap();
        fs::write(&path, json).unwrap();
        path
    }

    // -----------------------------------------------------------------------
    // Happy path: file exists at archived path, original path is free
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn restore_happy_path() {
        let tmp = TempDir::new().unwrap();
        // Set HOME to the temp dir so HOME-confinement checks pass.
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("my-tool");
        let original_dir = tmp.path().join("original");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("settings.json");
        let original_file = original_dir.join("settings.json");
        fs::write(&archived_file, b"hello world").unwrap();

        let manifest = make_manifest("my-tool", original_file.clone(), archived_file.clone());
        let manifest_path = write_manifest(tmp.path(), &manifest);

        let result = restore_from_manifest("my-tool", false, &manifest_path).unwrap();

        assert_eq!(result.restored_files, 1);
        assert_eq!(result.skipped_conflicts, 0);
        assert!(result.errors.is_empty());

        // File restored to original path.
        assert!(original_file.exists());
        assert_eq!(fs::read(&original_file).unwrap(), b"hello world");
        // Archived file is gone.
        assert!(!archived_file.exists());

        // Manifest updated: tool entry removed.
        let updated_raw = fs::read_to_string(&manifest_path).unwrap();
        let updated: ArchiveManifest = serde_json::from_str(&updated_raw).unwrap();
        assert!(updated.tools.is_empty(), "tool entry should be removed");
    }

    // -----------------------------------------------------------------------
    // Conflict: original already exists, overwrite_existing=false → skip
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn restore_conflict_skip() {
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("my-tool");
        let original_dir = tmp.path().join("original");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("settings.json");
        let original_file = original_dir.join("settings.json");
        fs::write(&archived_file, b"new content").unwrap();
        fs::write(&original_file, b"existing content").unwrap();

        let manifest = make_manifest("my-tool", original_file.clone(), archived_file.clone());
        let manifest_path = write_manifest(tmp.path(), &manifest);

        let result = restore_from_manifest("my-tool", false, &manifest_path).unwrap();

        assert_eq!(result.restored_files, 0);
        assert_eq!(result.skipped_conflicts, 1);
        assert!(result.errors.is_empty());

        // Original file unchanged.
        assert_eq!(fs::read(&original_file).unwrap(), b"existing content");
    }

    // -----------------------------------------------------------------------
    // Conflict + overwrite_existing=true → backup created, file restored
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn restore_overwrite_existing() {
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("my-tool");
        let original_dir = tmp.path().join("original");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("settings.json");
        let original_file = original_dir.join("settings.json");
        fs::write(&archived_file, b"archived content").unwrap();
        fs::write(&original_file, b"existing content").unwrap();

        let manifest = make_manifest("my-tool", original_file.clone(), archived_file.clone());
        let manifest_path = write_manifest(tmp.path(), &manifest);

        let result = restore_from_manifest("my-tool", true, &manifest_path).unwrap();

        assert_eq!(result.restored_files, 1);
        assert_eq!(result.skipped_conflicts, 0);
        assert!(result.errors.is_empty());

        // Original path now has the archived content.
        assert_eq!(fs::read(&original_file).unwrap(), b"archived content");

        // Backup was created with the prior existing content.
        let backup_path = backup_path_for(&original_file);
        assert!(backup_path.exists(), "backup file should exist");
        assert_eq!(fs::read(&backup_path).unwrap(), b"existing content");
    }

    // -----------------------------------------------------------------------
    // Manifest updated: tool entry removed after restore
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn restore_manifest_updated() {
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("tool-a");
        let original_dir = tmp.path().join("home");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("data.txt");
        let original_file = original_dir.join("data.txt");
        fs::write(&archived_file, b"data").unwrap();

        let manifest = make_manifest("tool-a", original_file.clone(), archived_file.clone());
        let manifest_path = write_manifest(tmp.path(), &manifest);

        restore_from_manifest("tool-a", false, &manifest_path).unwrap();

        let updated_raw = fs::read_to_string(&manifest_path).unwrap();
        let updated: ArchiveManifest = serde_json::from_str(&updated_raw).unwrap();
        assert!(
            updated.tools.iter().all(|t| t.tool_id != "tool-a"),
            "tool-a should not appear in manifest after restore"
        );
    }

    // -----------------------------------------------------------------------
    // Tool not found in manifest → Err
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn restore_tool_not_found() {
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("other-tool");
        let original_dir = tmp.path().join("original");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("x.txt");
        let original_file = original_dir.join("x.txt");
        fs::write(&archived_file, b"x").unwrap();

        let manifest = make_manifest("other-tool", original_file, archived_file);
        let manifest_path = write_manifest(tmp.path(), &manifest);

        let err = restore_from_manifest("nonexistent-tool", false, &manifest_path).unwrap_err();
        assert!(
            err.contains("not found in manifest"),
            "unexpected error: {err}"
        );
    }
}
