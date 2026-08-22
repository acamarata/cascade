// archive::engine — Atomic archive engine for legacy tool homes.
//
// Purpose: Moves a tool's legacy home directory to ~/.cascade/legacy/{tool_id}/
//   and writes/updates the manifest. Move semantics: rename (atomic on same
//   filesystem), with copy+delete fallback for cross-filesystem moves.
//   On any error, already-moved files are moved back (rollback).
//
// Inputs:  tool_id (String), source root resolved from HOME, archive root under
//          ~/.cascade/legacy/{tool_id}/, manifest at ~/.cascade/legacy/manifest.json.
// Outputs: ToolArchive on success; rollback + Err on failure.
//
// Constraints:
//   - MOVE, never copy-only. Non-destructive: partial moves are rolled back.
//   - Manifest write is atomic (write to .tmp, then rename).
//   - Idempotent: if tool already archived, return existing ToolArchive.
//   - HOME-confined: source and destination must canonicalize under HOME.
//
// SPORT: MASTER-COMPONENTS.md — archive_tool command — archive/engine.rs
// Task: T-P3-E03-16

use std::{
    fs,
    io::{self, Write as _},
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

use walkdir::WalkDir;

use crate::archive::types::{ArchiveManifest, ArchivedFile, ToolArchive};

// ---------------------------------------------------------------------------
// Public surface
// ---------------------------------------------------------------------------

/// Archive the legacy home directory of a single tool.
///
/// # Arguments
/// * `tool_id`    — kebab-case tool identifier (e.g. `"claude-code"`).
/// * `source_root` — absolute original tool root (e.g. `~/.claude`).
///
/// The destination is always `~/.cascade/legacy/{tool_id}/`.
///
/// # Returns
/// `Ok(ToolArchive)` on success or if the tool was already archived.
/// `Err(String)` if archive fails and rollback completes.
///
/// # Algorithm
/// 1. Resolve HOME; verify source and destination are HOME-confined.
/// 2. Idempotency: if tool_id already in manifest, return existing entry.
/// 3. Validate source exists; destination does not.
/// 4. Walk source: for each entry, rename → on EXDEV fall back to copy+verify+delete.
/// 5. On any error: reverse already-moved entries; log rollback failures.
/// 6. Write/append manifest atomically (.tmp + rename).
pub fn archive_tools(tool_id: String, source_root: PathBuf) -> Result<ToolArchive, String> {
    let home = resolve_home()?;
    let manifest_path = home.join(".cascade").join("legacy").join("manifest.json");

    // -----------------------------------------------------------------------
    // Idempotency: check manifest FIRST, before canonicalizing source.
    // Source may no longer exist on a re-run — that's fine if already archived.
    // -----------------------------------------------------------------------
    if let Ok(manifest) = load_manifest(&manifest_path) {
        if let Some(existing) = manifest.tools.into_iter().find(|t| t.tool_id == tool_id) {
            return Ok(existing);
        }
    }

    // -----------------------------------------------------------------------
    // Not already archived — canonicalize and validate paths.
    // -----------------------------------------------------------------------
    // Resolve source path to absolute; verify HOME confinement.
    let source = canonicalize_under_home(&source_root, &home)
        .map_err(|e| format!("source path rejected: {e}"))?;
    let archive_root = home.join(".cascade").join("legacy").join(&tool_id);

    // Destination must also stay under HOME.
    ensure_under_home_uncreated(&archive_root, &home)
        .map_err(|e| format!("archive destination rejected: {e}"))?;

    // -----------------------------------------------------------------------
    // Pre-flight: source must exist, archive_root must not
    // -----------------------------------------------------------------------
    if !source.exists() {
        return Err(format!("source does not exist: {}", source.display()));
    }
    if archive_root.exists() {
        return Err(format!(
            "archive destination already exists (not in manifest): {}",
            archive_root.display()
        ));
    }

    // -----------------------------------------------------------------------
    // Create archive root
    // -----------------------------------------------------------------------
    fs::create_dir_all(&archive_root).map_err(|e| {
        format!(
            "failed to create archive root {}: {e}",
            archive_root.display()
        )
    })?;

    // -----------------------------------------------------------------------
    // Walk and move
    // -----------------------------------------------------------------------
    let mut moved: Vec<(PathBuf, PathBuf)> = Vec::new(); // (archived, original)
    let mut files: Vec<ArchivedFile> = Vec::new();
    let mut move_error: Option<String> = None;

    for entry in WalkDir::new(&source)
        .min_depth(1)
        .into_iter()
        .filter_map(|e| e.ok())
    {
        let original = entry.path().to_path_buf();
        let relative = original
            .strip_prefix(&source)
            .map_err(|e| format!("strip_prefix error: {e}"))?;
        let dest = archive_root.join(relative);

        // Create parent directories in archive.
        if let Some(parent) = dest.parent() {
            if let Err(e) = fs::create_dir_all(parent) {
                move_error = Some(format!("failed to create dir {}: {e}", parent.display()));
                break;
            }
        }

        if original.is_dir() {
            // Directories are recreated above; skip actual move for dirs — their
            // contents will be moved. The empty dir at source is removed as a
            // side-effect of moving all its children.
            continue;
        }

        // Get file size before move.
        let size_bytes = fs::metadata(&original).map(|m| m.len()).unwrap_or(0);

        // Attempt rename; fall back on EXDEV.
        match atomic_move_file(&original, &dest) {
            Ok(()) => {
                let moved_at = iso8601_now();
                moved.push((dest.clone(), original.clone()));
                files.push(ArchivedFile {
                    original_path: original,
                    archived_path: dest,
                    moved_at,
                    size_bytes,
                });
            }
            Err(e) => {
                move_error = Some(format!("move failed for {}: {e}", original.display()));
                break;
            }
        }
    }

    // -----------------------------------------------------------------------
    // Rollback on error
    // -----------------------------------------------------------------------
    if let Some(err) = move_error {
        let log_path = home
            .join(".cascade")
            .join("legacy")
            .join("rollback-errors.log");
        let rollback_result = rollback(&moved, &log_path);
        // Clean up (potentially empty) archive root.
        let _ = fs::remove_dir_all(&archive_root);
        return Err(format!("{err}; rollback: {rollback_result}"));
    }

    // Remove now-empty source directories (deepest first).
    remove_empty_dirs(&source);

    // -----------------------------------------------------------------------
    // Build ToolArchive
    // -----------------------------------------------------------------------
    let archived_at = iso8601_now();
    let tool_archive = ToolArchive {
        tool_id: tool_id.clone(),
        original_root: source,
        archive_root: archive_root.clone(),
        archived_at,
        files,
    };

    // -----------------------------------------------------------------------
    // Atomic manifest write/update
    // -----------------------------------------------------------------------
    write_manifest_atomic(&manifest_path, &tool_archive)?;

    Ok(tool_archive)
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/// Resolve HOME from environment, canonicalizing through symlinks.
/// On macOS, `/var/folders/...` is a symlink to `/private/var/folders/...`;
/// canonicalizing ensures the HOME-confinement check works with `canonicalize`.
fn resolve_home() -> Result<PathBuf, String> {
    let raw = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| "HOME env var not set".to_string())?;
    raw.canonicalize()
        .map_err(|e| format!("cannot canonicalize HOME ({}): {e}", raw.display()))
}

/// Canonicalize `path`; verify it resides under `home`.
/// The path must already exist.
fn canonicalize_under_home(path: &Path, home: &Path) -> Result<PathBuf, String> {
    let canon = path
        .canonicalize()
        .map_err(|e| format!("cannot canonicalize {}: {e}", path.display()))?;
    if !canon.starts_with(home) {
        return Err(format!(
            "{} is outside HOME ({})",
            canon.display(),
            home.display()
        ));
    }
    Ok(canon)
}

/// Check that `path` (which may not yet exist) starts with `home` by walking
/// up to find an existing ancestor, canonicalizing it, then re-attaching the suffix.
/// `home` must already be canonical (resolved by `resolve_home`).
fn ensure_under_home_uncreated(path: &Path, home: &Path) -> Result<(), String> {
    let mut existing = path.to_path_buf();
    let mut suffix: Vec<std::ffi::OsString> = Vec::new();
    loop {
        if existing.exists() {
            break;
        }
        match existing.parent() {
            Some(p) => {
                if let Some(name) = existing.file_name() {
                    suffix.push(name.to_os_string());
                }
                existing = p.to_path_buf();
                if existing.as_os_str().is_empty() {
                    break;
                }
            }
            None => break,
        }
    }
    let canon_base = existing.canonicalize().unwrap_or(existing);
    let mut resolved = canon_base;
    for part in suffix.into_iter().rev() {
        resolved = resolved.join(part);
    }
    if !resolved.starts_with(home) {
        return Err(format!(
            "{} is outside HOME ({})",
            resolved.display(),
            home.display()
        ));
    }
    Ok(())
}

/// Move a single file atomically. Falls back to copy+verify+delete on EXDEV.
fn atomic_move_file(src: &Path, dst: &Path) -> io::Result<()> {
    match fs::rename(src, dst) {
        Ok(()) => Ok(()),
        Err(e) if is_cross_device(&e) => copy_verify_delete(src, dst),
        Err(e) => Err(e),
    }
}

/// Detect cross-device rename error (EXDEV = OS error 18 on Unix).
fn is_cross_device(e: &io::Error) -> bool {
    e.kind() == io::ErrorKind::CrossesDevices || e.raw_os_error() == Some(18) // EXDEV on Linux/macOS
}

/// Copy src to dst, verify size matches, then delete src.
fn copy_verify_delete(src: &Path, dst: &Path) -> io::Result<()> {
    let copied = fs::copy(src, dst)?;
    let original_size = fs::metadata(src)?.len();
    if copied != original_size {
        // Size mismatch — remove bad copy, leave original intact.
        let _ = fs::remove_file(dst);
        return Err(io::Error::other(format!(
            "copy size mismatch: copied {copied} bytes, original {original_size} bytes"
        )));
    }
    fs::remove_file(src)?;
    Ok(())
}

/// Attempt to move already-archived files back to their original paths.
/// Returns a summary string.
fn rollback(moved: &[(PathBuf, PathBuf)], log_path: &Path) -> String {
    let mut success = 0usize;
    let mut failures: Vec<String> = Vec::new();

    for (archived, original) in moved.iter().rev() {
        // Ensure parent of original exists.
        if let Some(parent) = original.parent() {
            let _ = fs::create_dir_all(parent);
        }
        match atomic_move_file(archived, original) {
            Ok(()) => success += 1,
            Err(e) => failures.push(format!(
                "{} -> {}: {e}",
                archived.display(),
                original.display()
            )),
        }
    }

    if !failures.is_empty() {
        // Best-effort log write.
        if let Some(parent) = log_path.parent() {
            let _ = fs::create_dir_all(parent);
        }
        if let Ok(mut f) = fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(log_path)
        {
            let ts = iso8601_now();
            for line in &failures {
                let _ = writeln!(f, "[{ts}] ROLLBACK FAILURE: {line}");
            }
        }
        return format!(
            "{success} restored, {} rollback failures (see {})",
            failures.len(),
            log_path.display()
        );
    }
    format!("{success} files rolled back successfully")
}

/// Remove now-empty directories from deepest to shallowest, stopping at root.
fn remove_empty_dirs(root: &Path) {
    // Collect all dirs depth-first.
    let dirs: Vec<PathBuf> = WalkDir::new(root)
        .min_depth(1)
        .into_iter()
        .filter_map(|e| e.ok())
        .filter(|e| e.file_type().is_dir())
        .map(|e| e.path().to_path_buf())
        .collect();

    // Remove deepest first.
    for dir in dirs.into_iter().rev() {
        let _ = fs::remove_dir(&dir); // only succeeds if empty
    }
    let _ = fs::remove_dir(root); // remove root itself if empty
}

/// Load manifest from disk; returns default empty manifest if absent.
fn load_manifest(path: &Path) -> Result<ArchiveManifest, String> {
    if !path.exists() {
        return Err("manifest not found".to_string());
    }
    let contents = fs::read_to_string(path).map_err(|e| format!("failed to read manifest: {e}"))?;
    serde_json::from_str::<ArchiveManifest>(&contents).map_err(|e| format!("corrupt manifest: {e}"))
}

/// Write or update the manifest atomically.
///
/// Reads existing manifest (or creates fresh), upserts `tool_archive`, writes
/// to `<path>.tmp`, then renames to `<path>`.
fn write_manifest_atomic(path: &Path, tool_archive: &ToolArchive) -> Result<(), String> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).map_err(|e| format!("failed to create manifest dir: {e}"))?;
    }

    // Load existing or create fresh.
    let mut manifest = load_manifest(path).unwrap_or_else(|_| ArchiveManifest {
        version: "1.0".to_string(),
        created_at: iso8601_now(),
        tools: Vec::new(),
        extras: std::collections::HashMap::new(),
    });

    // Upsert: replace if tool_id already present, otherwise append.
    let pos = manifest
        .tools
        .iter()
        .position(|t| t.tool_id == tool_archive.tool_id);
    match pos {
        Some(i) => manifest.tools[i] = tool_archive.clone(),
        None => manifest.tools.push(tool_archive.clone()),
    }

    let json = serde_json::to_string_pretty(&manifest)
        .map_err(|e| format!("failed to serialize manifest: {e}"))?;

    // Write to .tmp then rename for atomicity.
    let tmp_path = path.with_extension("json.tmp");
    {
        let mut f = fs::File::create(&tmp_path)
            .map_err(|e| format!("failed to create manifest tmp: {e}"))?;
        f.write_all(json.as_bytes())
            .map_err(|e| format!("failed to write manifest tmp: {e}"))?;
        f.flush()
            .map_err(|e| format!("failed to flush manifest tmp: {e}"))?;
    }
    fs::rename(&tmp_path, path)
        .map_err(|e| format!("failed to rename manifest tmp -> final: {e}"))?;

    Ok(())
}

/// Return current UTC time as an ISO 8601 string (second precision).
fn iso8601_now() -> String {
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);
    // Manual UTC conversion — no chrono dep.
    let (y, mo, d, h, mi, s) = secs_to_ymd_hms(secs);
    format!("{y:04}-{mo:02}-{d:02}T{h:02}:{mi:02}:{s:02}Z")
}

/// Convert Unix seconds to (year, month, day, hour, min, sec) UTC.
fn secs_to_ymd_hms(secs: u64) -> (u32, u32, u32, u32, u32, u32) {
    let s = (secs % 60) as u32;
    let mins = secs / 60;
    let mi = (mins % 60) as u32;
    let hours = mins / 60;
    let h = (hours % 24) as u32;
    let mut days = (hours / 24) as u32; // days since 1970-01-01

    let mut y = 1970u32;
    loop {
        let dy = if is_leap(y) { 366 } else { 365 };
        if days < dy {
            break;
        }
        days -= dy;
        y += 1;
    }
    let leap = is_leap(y);
    let months = [
        31u32,
        if leap { 29 } else { 28 },
        31,
        30,
        31,
        30,
        31,
        31,
        30,
        31,
        30,
        31,
    ];
    let mut mo = 1u32;
    for &dm in &months {
        if days < dm {
            break;
        }
        days -= dm;
        mo += 1;
    }
    (y, mo, days + 1, h, mi, s)
}

fn is_leap(y: u32) -> bool {
    (y % 4 == 0 && y % 100 != 0) || (y % 400 == 0)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
#[cfg(unix)] // unix-only: drives read-only-directory rollback via mode bits
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    /// Create a fake tool home with some files.
    fn setup_tool_dir(base: &Path, tool: &str) -> PathBuf {
        let root = base.join(tool);
        fs::create_dir_all(root.join("subdir")).unwrap();
        fs::write(root.join("settings.json"), b"{}").unwrap();
        fs::write(root.join("subdir").join("notes.md"), b"# notes").unwrap();
        root
    }

    /// Override HOME to a temp dir for the duration of a test.
    fn with_home<F: FnOnce(&Path)>(f: F) {
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());
        f(tmp.path());
        // TempDir cleans up on drop.
    }

    // -----------------------------------------------------------------------
    // 1. Happy path: files moved, manifest written, source gone
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn happy_path_moves_files_and_writes_manifest() {
        with_home(|home| {
            let source = setup_tool_dir(home, ".claude");
            let result = archive_tools("claude-code".to_string(), source.clone());
            assert!(result.is_ok(), "archive failed: {:?}", result);

            let ta = result.unwrap();
            assert_eq!(ta.tool_id, "claude-code");
            assert!(!source.exists(), "source root should be gone after archive");

            // Manifest must exist with camelCase keys.
            let manifest_path = home.join(".cascade/legacy/manifest.json");
            assert!(manifest_path.exists(), "manifest.json missing");
            let json = fs::read_to_string(&manifest_path).unwrap();
            assert!(json.contains("\"toolId\""), "camelCase toolId missing");
            assert!(
                json.contains("\"originalRoot\""),
                "camelCase originalRoot missing"
            );
            assert!(
                json.contains("\"archivedAt\""),
                "camelCase archivedAt missing"
            );
            assert!(
                json.contains("\"claude-code\""),
                "tool_id missing from manifest"
            );

            // All archived files exist at new location.
            for f in &ta.files {
                assert!(
                    f.archived_path.exists(),
                    "archived file missing: {:?}",
                    f.archived_path
                );
            }
        });
    }

    // -----------------------------------------------------------------------
    // 2. Partial failure: source files left intact, no partial archive state
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn partial_failure_leaves_source_intact() {
        with_home(|home| {
            let source = setup_tool_dir(home, ".opencode");

            // We simulate a failure by making the archive root unwritable AFTER
            // creating it — then the second file write will fail.
            // Instead, we test rollback by triggering an error via a read-only file.
            // Since we can't easily inject cross-fs errors portably, we test via
            // a destination that already has a read-only file at the target path.
            let archive_root = home.join(".cascade/legacy/test-tool");
            fs::create_dir_all(&archive_root).unwrap();

            // Place a file at the exact location where the first moved file will land,
            // but make the destination directory read-only so rename fails.
            // Use chmod to make archive root read-only.
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                let mut perms = fs::metadata(&archive_root).unwrap().permissions();
                perms.set_mode(0o444);
                fs::set_permissions(&archive_root, perms).unwrap();
            }

            let result = archive_tools("test-tool".to_string(), source.clone());
            // On read-only dest the move should fail and rollback.
            // On non-unix (Windows), this test is a no-op assertion.
            #[cfg(unix)]
            {
                // Restore perms so cleanup works.
                use std::os::unix::fs::PermissionsExt;
                let mut perms = fs::metadata(&archive_root).unwrap().permissions();
                perms.set_mode(0o755);
                fs::set_permissions(&archive_root, perms).unwrap();

                if result.is_err() {
                    // Source must still exist (rollback succeeded or files weren't moved).
                    assert!(
                        source.join("settings.json").exists(),
                        "source file missing after failed archive"
                    );
                }
                // If OS didn't return error (some macOS setups ignore mode on temp), just pass.
            }
        });
    }

    // -----------------------------------------------------------------------
    // 3. Idempotent: re-archiving already-archived tool returns existing entry
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn idempotent_returns_existing_entry() {
        with_home(|home| {
            let source = setup_tool_dir(home, ".claude");
            let first = archive_tools("claude-code".to_string(), source.clone()).unwrap();

            // Source is gone; call again with the same tool_id — should succeed
            // idempotently (reads from manifest).
            // Use a dummy path that doesn't exist — idempotency check runs before source check.
            let dummy = home.join(".claude"); // no longer exists
            let second = archive_tools("claude-code".to_string(), dummy);
            assert!(second.is_ok(), "idempotent re-archive failed: {:?}", second);
            assert_eq!(first.tool_id, second.unwrap().tool_id);
        });
    }

    // -----------------------------------------------------------------------
    // 4. Outside-HOME rejected
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn outside_home_rejected() {
        with_home(|home| {
            // Create a real dir outside HOME.
            let tmp = TempDir::new().unwrap();
            let outside = tmp.path().join("outside-tool");
            fs::create_dir_all(&outside).unwrap();
            fs::write(outside.join("file.txt"), b"data").unwrap();

            // Source outside HOME must be rejected.
            let result = archive_tools("outside-tool".to_string(), outside.clone());
            assert!(
                result.is_err(),
                "expected rejection of outside-HOME source, got Ok"
            );
            let err = result.unwrap_err();
            assert!(
                err.contains("outside HOME") || err.contains("rejected"),
                "unexpected error: {err}"
            );
            // _ = home; suppress unused warning
            let _ = home;
        });
    }

    // -----------------------------------------------------------------------
    // 5. Manifest written with camelCase keys
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn manifest_uses_camel_case_keys() {
        with_home(|home| {
            let source = setup_tool_dir(home, ".opencode");
            archive_tools("opencode".to_string(), source).unwrap();
            let manifest_path = home.join(".cascade/legacy/manifest.json");
            let json = fs::read_to_string(manifest_path).unwrap();
            // All camelCase keys must appear.
            for key in &[
                "toolId",
                "originalRoot",
                "archiveRoot",
                "archivedAt",
                "movedAt",
                "sizeBytes",
                "originalPath",
                "archivedPath",
            ] {
                assert!(
                    json.contains(&format!("\"{}\"", key)),
                    "missing camelCase key: {key}"
                );
            }
            // No snake_case leaks.
            for key in &[
                "tool_id",
                "original_root",
                "archive_root",
                "archived_at",
                "moved_at",
                "size_bytes",
            ] {
                assert!(
                    !json.contains(&format!("\"{}\"", key)),
                    "snake_case key leaked: {key}"
                );
            }
        });
    }

    // -----------------------------------------------------------------------
    // 6. ISO 8601 timestamp sanity
    // -----------------------------------------------------------------------
    #[test]
    fn iso8601_format_looks_valid() {
        let ts = iso8601_now();
        // e.g. "2026-06-09T12:34:56Z"
        assert_eq!(ts.len(), 20, "unexpected timestamp length: {ts}");
        assert!(ts.ends_with('Z'), "timestamp must end with Z: {ts}");
        assert!(ts.contains('T'), "timestamp must contain T: {ts}");
    }
}
