//! `cascade restore` subcommand — restore an archived tool's files.
//!
//! Purpose: Reads `~/.cascade/legacy/manifest.json`, finds the ToolArchive for
//! the given tool, and moves each file back to its original path.
//!
//! Usage:
//! ```text
//! cascade restore --tool <name>
//! cascade restore --tool claude-code --overwrite
//! ```
//!
//! Architecture note: cascade-cli cannot depend on the cascade-app Tauri crate
//! (which carries Tauri, reqwest, and OS-plugin dependencies). The restore
//! algorithm is therefore implemented directly in this module — identical
//! semantics to `archive::restore::restore_from_manifest` in the Tauri app.
//! If a shared `cascade-archive` crate is extracted in a future epic, both
//! callers should migrate to it (tracked in T-P3-E03-19 gaps).
//!
//! SPORT: MASTER-COMPONENTS.md — `cascade restore` CLI command — cmd/restore.rs
//! Task: T-P3-E03-19

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::Args;
use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};

use super::Command;

// ---------------------------------------------------------------------------
// Clap Args
// ---------------------------------------------------------------------------

/// Arguments for `cascade restore`.
#[derive(Debug, Args)]
pub struct RestoreArgs {
    /// Kebab-case tool identifier to restore (e.g. `claude-code`, `opencode`).
    #[arg(long, short = 't', value_name = "NAME")]
    pub tool: String,

    /// When the original path already exists, back it up to
    /// `{path}.cascade-backup` before overwriting. Default: skip conflicts.
    #[arg(long)]
    pub overwrite: bool,
}

#[async_trait]
impl Command for RestoreArgs {
    async fn run(&self) -> Result<()> {
        let manifest_path = manifest_path()?;

        if !manifest_path.exists() {
            return Err(CascadeError::Other(format!(
                "no archive manifest found at '{}' — nothing to restore",
                manifest_path.display()
            )));
        }

        let result = restore_from_manifest(&self.tool, self.overwrite, &manifest_path)
            .map_err(CascadeError::Other)?;

        println!(
            "restore '{}': {} restored, {} conflicts skipped, {} errors",
            self.tool,
            result.restored_files,
            result.skipped_conflicts,
            result.errors.len()
        );

        for err in &result.errors {
            eprintln!("  error: {err}");
        }

        if result.errors.is_empty() {
            Ok(())
        } else {
            Err(CascadeError::Other(format!(
                "{} file(s) could not be restored — see errors above",
                result.errors.len()
            )))
        }
    }
}

// ---------------------------------------------------------------------------
// Manifest types (minimal — mirrors archive::types without Tauri dep)
// ---------------------------------------------------------------------------

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct ArchivedFile {
    original_path: PathBuf,
    archived_path: PathBuf,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct ToolArchive {
    tool_id: String,
    archive_root: PathBuf,
    files: Vec<ArchivedFile>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(rename_all = "camelCase")]
struct ArchiveManifest {
    version: String,
    created_at: String,
    tools: Vec<ToolArchive>,
}

/// Summary of a restore operation.
pub struct RestoreResult {
    pub restored_files: usize,
    pub skipped_conflicts: usize,
    pub errors: Vec<String>,
}

/// Public wrapper around [`restore_from_manifest`] for use by `uninstall.rs`.
///
/// Called by `cascade uninstall --full` to restore each archived tool before
/// removing `~/.cascade/`. Returns `Err(String)` on hard failure.
pub fn restore_from_manifest_pub(
    tool_id: &str,
    overwrite_existing: bool,
    manifest_path: &Path,
) -> std::result::Result<RestoreResult, String> {
    restore_from_manifest(tool_id, overwrite_existing, manifest_path)
}

// ---------------------------------------------------------------------------
// Core restore logic
// ---------------------------------------------------------------------------

/// Resolve `~/.cascade/legacy/manifest.json`.
fn manifest_path() -> Result<PathBuf> {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| CascadeError::Other("HOME env var not set".into()))?;
    Ok(home.join(".cascade").join("legacy").join("manifest.json"))
}

/// Execute the restore from a caller-supplied manifest path.
///
/// Identical semantics to `cascade_app::archive::restore::restore_from_manifest`.
/// Both implementations should be kept in sync until a shared crate is extracted.
fn restore_from_manifest(
    tool_id: &str,
    overwrite_existing: bool,
    manifest_path: &Path,
) -> std::result::Result<RestoreResult, String> {
    // 1. Read and parse the manifest.
    let raw = std::fs::read_to_string(manifest_path).map_err(|e| {
        format!(
            "failed to read manifest at {}: {e}",
            manifest_path.display()
        )
    })?;
    let mut manifest: ArchiveManifest =
        serde_json::from_str(&raw).map_err(|e| format!("corrupt manifest — invalid JSON: {e}"))?;

    // 2. Find the ToolArchive entry.
    let tool_idx = manifest
        .tools
        .iter()
        .position(|t| t.tool_id == tool_id)
        .ok_or_else(|| format!("tool '{tool_id}' not found in manifest"))?;
    let tool_archive_root = manifest.tools[tool_idx].archive_root.clone();
    let files: Vec<_> = manifest.tools[tool_idx]
        .files
        .iter()
        .map(|f| (f.original_path.clone(), f.archived_path.clone()))
        .collect();

    // 3. Restore each file.
    let mut restored_files: usize = 0;
    let mut skipped_conflicts: usize = 0;
    let mut errors: Vec<String> = Vec::new();

    for (original, archived) in &files {
        // HOME-confinement check.
        if let Err(e) = assert_home_confined(original) {
            errors.push(format!(
                "SECURITY: original path '{}' not HOME-confined — skipped: {e}",
                original.display()
            ));
            continue;
        }

        if !archived.exists() {
            errors.push(format!(
                "archived file not found at '{}' — skipped",
                archived.display()
            ));
            continue;
        }

        if original.exists() {
            if !overwrite_existing {
                skipped_conflicts += 1;
                continue;
            }
            // Backup the existing file.
            let backup = backup_path_for(original);
            if let Err(e) = move_file(original, &backup) {
                errors.push(format!(
                    "failed to backup '{}' to '{}': {e}",
                    original.display(),
                    backup.display()
                ));
                continue;
            }
        }

        // Create parent dirs.
        if let Some(parent) = original.parent() {
            if let Err(e) = std::fs::create_dir_all(parent) {
                errors.push(format!(
                    "failed to create parent dir '{}': {e}",
                    parent.display()
                ));
                continue;
            }
        }

        match move_file(archived, original) {
            Ok(()) => restored_files += 1,
            Err(e) => errors.push(format!(
                "failed to restore '{}' -> '{}': {e}",
                archived.display(),
                original.display()
            )),
        }
    }

    // 4. Remove archive root if empty.
    if tool_archive_root.exists() {
        let _ = remove_dir_if_empty(&tool_archive_root);
    }

    // 5. Atomic manifest update.
    manifest.tools.remove(tool_idx);
    let json = serde_json::to_string_pretty(&manifest)
        .map_err(|e| format!("manifest serialize failed: {e}"))?;
    let tmp = manifest_path.with_extension("json.tmp");
    std::fs::write(&tmp, &json).map_err(|e| format!("write tmp manifest failed: {e}"))?;
    std::fs::rename(&tmp, manifest_path)
        .map_err(|e| format!("atomic rename manifest failed: {e}"))?;

    Ok(RestoreResult {
        restored_files,
        skipped_conflicts,
        errors,
    })
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

fn assert_home_confined(path: &Path) -> std::result::Result<(), String> {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| "HOME env var not set".to_string())?;
    if !path.starts_with(&home) {
        return Err(format!(
            "path '{}' outside HOME '{}'",
            path.display(),
            home.display()
        ));
    }
    Ok(())
}

fn backup_path_for(path: &Path) -> PathBuf {
    let mut s = path.as_os_str().to_owned();
    s.push(".cascade-backup");
    PathBuf::from(s)
}

fn move_file(src: &Path, dst: &Path) -> std::result::Result<(), String> {
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

fn is_cross_device_error(e: &std::io::Error) -> bool {
    #[cfg(unix)]
    {
        e.raw_os_error() == Some(libc::EXDEV)
    }
    #[cfg(not(unix))]
    {
        let _ = e;
        false
    }
}

fn remove_dir_if_empty(dir: &Path) -> std::result::Result<(), String> {
    let is_empty = std::fs::read_dir(dir)
        .map_err(|e| format!("read_dir failed: {e}"))?
        .next()
        .is_none();
    if is_empty {
        std::fs::remove_dir(dir).map_err(|e| format!("remove_dir failed: {e}"))?;
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    fn make_manifest_json(tool_id: &str, original_file: &Path, archived_file: &Path) -> String {
        let archive_root = archived_file.parent().unwrap().to_str().unwrap();
        let original_root = original_file.parent().unwrap().to_str().unwrap();
        let orig = original_file.to_str().unwrap();
        let arch = archived_file.to_str().unwrap();
        format!(
            r#"{{
  "version": "1.0",
  "createdAt": "2026-06-09T00:00:00Z",
  "tools": [{{
    "toolId": "{tool_id}",
    "originalRoot": "{original_root}",
    "archiveRoot": "{archive_root}",
    "archivedAt": "2026-06-09T00:01:00Z",
    "files": [{{
      "originalPath": "{orig}",
      "archivedPath": "{arch}",
      "movedAt": "2026-06-09T00:01:00Z",
      "sizeBytes": 42
    }}]
  }}]
}}"#
        )
    }

    #[test]
    #[serial(global_env)]
    fn cli_restore_happy_path() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("test-tool");
        let original_dir = tmp.path().join("original");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("config.json");
        let original_file = original_dir.join("config.json");
        fs::write(&archived_file, b"config data").unwrap();

        let manifest_path = tmp.path().join("manifest.json");
        fs::write(
            &manifest_path,
            make_manifest_json("test-tool", &original_file, &archived_file),
        )
        .unwrap();

        let result = restore_from_manifest("test-tool", false, &manifest_path).unwrap();

        assert_eq!(result.restored_files, 1);
        assert_eq!(result.skipped_conflicts, 0);
        assert!(result.errors.is_empty());
        assert!(original_file.exists());
        assert_eq!(fs::read(&original_file).unwrap(), b"config data");
    }

    #[test]
    #[serial(global_env)]
    fn cli_restore_conflict_skip() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("test-tool");
        let original_dir = tmp.path().join("original");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("config.json");
        let original_file = original_dir.join("config.json");
        fs::write(&archived_file, b"new").unwrap();
        fs::write(&original_file, b"existing").unwrap();

        let manifest_path = tmp.path().join("manifest.json");
        fs::write(
            &manifest_path,
            make_manifest_json("test-tool", &original_file, &archived_file),
        )
        .unwrap();

        let result = restore_from_manifest("test-tool", false, &manifest_path).unwrap();
        assert_eq!(result.skipped_conflicts, 1);
        assert_eq!(result.restored_files, 0);
        assert_eq!(fs::read(&original_file).unwrap(), b"existing");
    }

    #[test]
    #[serial(global_env)]
    fn cli_restore_overwrite() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("test-tool");
        let original_dir = tmp.path().join("original");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("config.json");
        let original_file = original_dir.join("config.json");
        fs::write(&archived_file, b"archived").unwrap();
        fs::write(&original_file, b"existing").unwrap();

        let manifest_path = tmp.path().join("manifest.json");
        fs::write(
            &manifest_path,
            make_manifest_json("test-tool", &original_file, &archived_file),
        )
        .unwrap();

        let result = restore_from_manifest("test-tool", true, &manifest_path).unwrap();
        assert_eq!(result.restored_files, 1);
        assert_eq!(fs::read(&original_file).unwrap(), b"archived");
        let backup = backup_path_for(&original_file);
        assert!(backup.exists());
        assert_eq!(fs::read(&backup).unwrap(), b"existing");
    }

    #[test]
    #[serial(global_env)]
    fn cli_restore_manifest_updated() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", tmp.path()) };
        let archived_dir = tmp.path().join("archive").join("tool-b");
        let original_dir = tmp.path().join("home");
        fs::create_dir_all(&archived_dir).unwrap();
        fs::create_dir_all(&original_dir).unwrap();

        let archived_file = archived_dir.join("x.txt");
        let original_file = original_dir.join("x.txt");
        fs::write(&archived_file, b"x").unwrap();

        let manifest_path = tmp.path().join("manifest.json");
        fs::write(
            &manifest_path,
            make_manifest_json("tool-b", &original_file, &archived_file),
        )
        .unwrap();

        restore_from_manifest("tool-b", false, &manifest_path).unwrap();

        let raw = fs::read_to_string(&manifest_path).unwrap();
        let updated: ArchiveManifest = serde_json::from_str(&raw).unwrap();
        assert!(updated.tools.is_empty());
    }

    #[test]
    fn cli_restore_parses() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(flatten)]
            args: RestoreArgs,
        }

        let cli = Cli::try_parse_from(["restore", "--tool", "claude-code"]).unwrap();
        assert_eq!(cli.args.tool, "claude-code");
        assert!(!cli.args.overwrite);

        let cli2 = Cli::try_parse_from(["restore", "--tool", "opencode", "--overwrite"]).unwrap();
        assert!(cli2.args.overwrite);
    }
}
