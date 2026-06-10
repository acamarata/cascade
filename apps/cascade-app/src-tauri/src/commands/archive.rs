// commands::archive — Tauri command surface for the archive/restore subsystem.
//
// Purpose: Exposes archive, manifest-reader, validation, and restore operations
//   as Tauri IPC commands callable from the React frontend.
//
// Commands registered in lib.rs generate_handler!:
//   archive_legacy_tools   — T-P3-E03-16 (delegates to archive::engine)
//   read_archive_manifest  — T-P3-E03-17 (reads manifest.json; None if absent)
//   list_archived_tools    — T-P3-E03-17 (convenience: manifest.tools or [])
//   archive_preflight      — T-P3-E03-18 (delegates to archive::validation)
//   restore_tool           — T-P3-E03-19 (delegates to archive::restore)
//
// Constraints:
//   - read_archive_manifest returns None when no manifest exists (not Err).
//   - All paths resolved to absolute strings before serialization.
//   - No serde `any` — all return types are fully typed.
//
// SPORT: MASTER-COMPONENTS.md — archive commands — commands/archive.rs
// Tasks: T-P3-E03-16 through T-P3-E03-19

use crate::archive::{
    engine::archive_tools,
    restore::restore_core,
    types::{ArchiveManifest, ArchiveValidation, RestoreResult, ToolArchive},
    validation::preflight_validation,
};
use std::path::PathBuf;

/// Resolve source root for a tool from HOME.
/// Falls back to `~/.{tool_id}` if no explicit path provided.
fn default_source_for(tool_id: &str) -> Result<PathBuf, String> {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| "HOME env var not set".to_string())?;
    // Conventional tool home: ~/.{tool_id} (e.g. ~/.claude, ~/.opencode)
    Ok(home.join(format!(".{tool_id}")))
}
#[cfg(test)]
use serial_test::serial;

/// Resolve the manifest path: `~/.cascade/legacy/manifest.json`.
fn manifest_path() -> Result<PathBuf, String> {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| "HOME env var not set".to_string())?;
    Ok(home.join(".cascade").join("legacy").join("manifest.json"))
}

// ---------------------------------------------------------------------------
// T-P3-E03-16: archive_legacy_tools
// ---------------------------------------------------------------------------

/// Archive a tool's legacy home directory.
///
/// Moves the tool root to `~/.cascade/legacy/{tool_id}/` and writes the
/// manifest. Idempotent: returns the existing `ToolArchive` if already archived.
///
/// # Errors
/// Returns `Err(String)` if the move fails and rollback completes. Rollback
/// failures are logged to `~/.cascade/legacy/rollback-errors.log`.
#[tauri::command]
pub async fn archive_legacy_tools(
    tool_id: String,
    source_root: Option<String>,
) -> Result<ToolArchive, String> {
    let src = match source_root {
        Some(p) => PathBuf::from(p),
        None => default_source_for(&tool_id)?,
    };
    archive_tools(tool_id, src)
}

// ---------------------------------------------------------------------------
// T-P3-E03-17: read_archive_manifest / list_archived_tools
// ---------------------------------------------------------------------------

/// Read the archive manifest from `~/.cascade/legacy/manifest.json`.
///
/// Returns `None` when no manifest exists (no tools archived yet).
/// Returns `Some(manifest)` when the manifest is valid.
/// Returns `Err` when the file exists but contains corrupt/invalid JSON.
#[tauri::command]
pub async fn read_archive_manifest() -> Result<Option<ArchiveManifest>, String> {
    let path = manifest_path()?;
    if !path.exists() {
        return Ok(None);
    }
    let contents =
        std::fs::read_to_string(&path).map_err(|e| format!("failed to read manifest: {e}"))?;
    let manifest: ArchiveManifest = serde_json::from_str(&contents)
        .map_err(|e| format!("corrupt manifest — invalid JSON: {e}"))?;
    Ok(Some(manifest))
}

/// Convenience wrapper: returns `manifest.tools` or an empty Vec if no manifest exists.
///
/// Never returns `Err` for a missing manifest — only for corrupt JSON.
#[tauri::command]
pub async fn list_archived_tools() -> Result<Vec<ToolArchive>, String> {
    match read_archive_manifest().await? {
        Some(manifest) => Ok(manifest.tools),
        None => Ok(vec![]),
    }
}

// ---------------------------------------------------------------------------
// T-P3-E03-18: archive_preflight
// ---------------------------------------------------------------------------

/// Run pre-flight validation before archiving a tool.
///
/// Checks disk space, write permissions, destination conflicts, source existence,
/// and active processes. The frontend calls this before showing the confirm dialog.
///
/// Fatal issues: `InsufficientDisk`, `PermissionDenied` — must be resolved.
/// Non-fatal issues: others — user may proceed with warnings.
#[tauri::command]
pub async fn archive_preflight(tool_id: String) -> Result<ArchiveValidation, String> {
    preflight_validation(tool_id)
}

// ---------------------------------------------------------------------------
// T-P3-E03-19: restore_tool
// ---------------------------------------------------------------------------

/// Restore an archived tool's files to their original paths.
///
/// Reads the manifest, finds the `ToolArchive` for `tool_id`, and moves each
/// file back. Atomic manifest update removes the tool entry on completion.
///
/// # Arguments
/// * `tool_id` — kebab-case tool identifier.
/// * `overwrite_existing` — if `true`, backup conflicting files before overwrite.
///
/// # Returns
/// `RestoreResult` with counts of restored, skipped, and errored files.
#[tauri::command]
pub async fn restore_tool(
    tool_id: String,
    overwrite_existing: bool,
) -> Result<RestoreResult, String> {
    restore_core(tool_id, overwrite_existing)
}

// ---------------------------------------------------------------------------
// T-P3-E03-17 tests: manifest reader — missing / valid / corrupt
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    /// Override HOME to a temp dir and return the TempDir guard so it lives
    /// for the duration of the test.
    fn set_home(tmp: &TempDir) {
        std::env::set_var("HOME", tmp.path());
    }

    /// Build the expected manifest.json path inside the temp HOME.
    fn manifest_file(tmp: &TempDir) -> PathBuf {
        tmp.path()
            .join(".cascade")
            .join("legacy")
            .join("manifest.json")
    }

    /// Write a minimal valid manifest JSON to disk.
    fn write_valid_manifest(path: &PathBuf) {
        let dir = path.parent().unwrap();
        fs::create_dir_all(dir).unwrap();
        let json = r#"{"version":"1.0","createdAt":"2026-06-09T00:00:00Z","tools":[]}"#;
        fs::write(path, json).unwrap();
    }

    /// `read_archive_manifest` returns `Ok(None)` when manifest.json is absent.
    #[tokio::test]
    #[serial(global_env)]
    async fn manifest_missing_returns_none() {
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);

        let result = read_archive_manifest().await;
        assert!(result.is_ok(), "expected Ok, got: {result:?}");
        assert!(
            result.unwrap().is_none(),
            "expected None for missing manifest"
        );
    }

    /// `read_archive_manifest` returns `Ok(Some(manifest))` for valid JSON.
    #[tokio::test]
    #[serial(global_env)]
    async fn manifest_valid_returns_some() {
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);
        write_valid_manifest(&manifest_file(&tmp));

        let result = read_archive_manifest().await;
        assert!(result.is_ok(), "expected Ok, got: {result:?}");
        let manifest = result.unwrap().expect("expected Some(manifest)");
        assert_eq!(manifest.version, "1.0");
        assert!(manifest.tools.is_empty());
    }

    /// `read_archive_manifest` returns `Err` for corrupt JSON.
    #[tokio::test]
    #[serial(global_env)]
    async fn manifest_corrupt_returns_err() {
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);
        let path = manifest_file(&tmp);
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(&path, b"{ not valid json !!!").unwrap();

        let result = read_archive_manifest().await;
        assert!(result.is_err(), "expected Err for corrupt manifest");
        let err = result.unwrap_err();
        assert!(
            err.contains("corrupt manifest"),
            "error should mention 'corrupt manifest', got: {err}"
        );
    }

    /// `list_archived_tools` returns empty Vec when no manifest exists.
    #[tokio::test]
    #[serial(global_env)]
    async fn list_tools_no_manifest_returns_empty() {
        let tmp = TempDir::new().unwrap();
        set_home(&tmp);

        let result = list_archived_tools().await;
        assert!(result.is_ok(), "expected Ok, got: {result:?}");
        assert!(result.unwrap().is_empty());
    }
}
