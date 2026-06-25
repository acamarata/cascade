// Wizard state types and checkpoint persistence commands.
//
// Why: groups all onboarding-wizard IPC commands (T-P3-E03-07 first-run detection,
// T-P3-E03-03 checkpoint persistence, audit-log format check / rotation) so the
// wizard surface can grow without bloating mod.rs.

use serde::{Deserialize, Serialize};
use std::env;
use std::fs;
use std::path::PathBuf;

use crate::error::CascadeError;

// ---------------------------------------------------------------------------
// Wizard types
// ---------------------------------------------------------------------------

/// Wizard status enum for first-run detection.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "status", content = "data")]
pub enum WizardStatus {
    /// Wizard has never been run
    NeverRun,
    /// Wizard is in progress
    InProgress(WizardCheckpoint),
    /// Wizard is complete
    Complete,
}

/// Wizard checkpoint type.
///
/// Serialized as camelCase JSON so the TypeScript frontend can consume it
/// without a mapping layer (e.g. `completedSteps`, `providerConnected`).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(crate = "serde", rename_all = "camelCase")]
pub struct WizardCheckpoint {
    pub step: u32,
    pub completed_steps: Vec<u32>,
    pub provider_connected: bool,
    pub scan_result: Option<ScanResult>,
    pub merge_result: Option<MergeResult>,
    pub archive_manifest_path: Option<String>,
    pub daemon_installed: bool,
    pub started_at: String,
    pub updated_at: String,
    /// Forward-compatible passthrough: preserves wizard-state fields added on the
    /// TS side (mergeResults, toolModes, archivedTools, detectedToolIds, ...) through
    /// the save/load round-trip without requiring a Rust struct change per field.
    #[serde(flatten)]
    pub extra: serde_json::Map<String, serde_json::Value>,
}

/// Result of the legacy config scan (wizard phase 3).
/// Fields serialized as camelCase for TypeScript consumers.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(crate = "serde", rename_all = "camelCase")]
pub struct ScanResult {
    pub legacy_files_found: u32,
    pub total_size: u64,
    pub project_roots: Vec<String>,
}

/// Result of the config merge operation (wizard phase 4).
/// Fields serialized as camelCase for TypeScript consumers.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(crate = "serde", rename_all = "camelCase")]
pub struct MergeResult {
    pub merged_file_count: u32,
    pub conflicts_resolved: u32,
    pub timestamp: String,
}

/// Audit check result from wizard_check_audit_format.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AuditCheckResult {
    pub audit_log_exists: bool,
    pub format_mismatch: bool,
    pub is_tampered: bool,
    pub violation_count: u32,
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Get home directory for current platform.
pub fn get_home_dir() -> Option<PathBuf> {
    #[cfg(target_os = "windows")]
    {
        env::var("USERPROFILE").ok().map(PathBuf::from)
    }
    #[cfg(not(target_os = "windows"))]
    {
        env::var("HOME").ok().map(PathBuf::from)
    }
}

/// Get the checkpoint file path: ~/.cascade/wizard-state.json
fn checkpoint_path() -> Result<PathBuf, CascadeError> {
    get_home_dir()
        .ok_or_else(|| CascadeError::Custom("could not resolve home directory".into()))
        .map(|h| h.join(".cascade").join("wizard-state.json"))
}

/// Get the temporary checkpoint file path: ~/.cascade/wizard-state.json.tmp
fn checkpoint_tmp_path() -> Result<PathBuf, CascadeError> {
    checkpoint_path().map(|p| {
        let mut tmp = p.as_os_str().to_owned();
        tmp.push(".tmp");
        PathBuf::from(tmp)
    })
}

// ---------------------------------------------------------------------------
// Wizard commands
// ---------------------------------------------------------------------------

/// Check wizard status to determine if it should launch, resume, or be skipped.
#[tauri::command]
pub fn check_wizard_status() -> Result<WizardStatus, CascadeError> {
    let home = get_home_dir()
        .ok_or_else(|| CascadeError::Custom("Could not determine home directory".to_string()))?;

    let cascade_dir = home.join(".cascade");
    let wizard_complete_marker = cascade_dir.join(".wizard-complete");
    let wizard_state_file = cascade_dir.join("wizard-state.json");

    // Check if ~/.cascade/ exists
    if !cascade_dir.exists() {
        return Ok(WizardStatus::NeverRun);
    }

    // Check if wizard-complete marker exists
    if wizard_complete_marker.exists() {
        return Ok(WizardStatus::Complete);
    }

    // Check if wizard-state.json exists
    if wizard_state_file.exists() {
        match fs::read_to_string(&wizard_state_file) {
            Ok(content) => match serde_json::from_str::<WizardCheckpoint>(&content) {
                Ok(checkpoint) => {
                    if checkpoint.completed_steps.len() < 10 {
                        return Ok(WizardStatus::InProgress(checkpoint));
                    }
                    return Ok(WizardStatus::Complete);
                }
                Err(_) => {
                    return Ok(WizardStatus::NeverRun);
                }
            },
            Err(_) => {
                return Ok(WizardStatus::NeverRun);
            }
        }
    }

    Ok(WizardStatus::NeverRun)
}

/// Save checkpoint atomically to ~/.cascade/wizard-state.json.
#[tauri::command]
pub async fn wizard_save_checkpoint(checkpoint: WizardCheckpoint) -> Result<(), CascadeError> {
    let target_path = checkpoint_path()?;
    let tmp_path = checkpoint_tmp_path()?;

    if let Some(parent) = target_path.parent() {
        fs::create_dir_all(parent)
            .map_err(|e| CascadeError::Custom(format!("failed to create directory: {}", e)))?;
    }

    let json = serde_json::to_string_pretty(&checkpoint)
        .map_err(|e| CascadeError::Custom(format!("failed to serialize checkpoint: {}", e)))?;

    fs::write(&tmp_path, json)
        .map_err(|e| CascadeError::Custom(format!("failed to write checkpoint: {}", e)))?;

    fs::rename(&tmp_path, &target_path)
        .map_err(|e| CascadeError::Custom(format!("failed to finalize checkpoint: {}", e)))?;

    Ok(())
}

/// Load checkpoint from ~/.cascade/wizard-state.json.
#[tauri::command]
pub async fn wizard_load_checkpoint() -> Result<Option<WizardCheckpoint>, CascadeError> {
    let path = checkpoint_path()?;

    if !path.exists() {
        return Ok(None);
    }

    let json = fs::read_to_string(&path)
        .map_err(|e| CascadeError::Custom(format!("failed to read checkpoint: {}", e)))?;

    let checkpoint = serde_json::from_str(&json)
        .map_err(|e| CascadeError::Custom(format!("failed to parse checkpoint: {}", e)))?;

    Ok(Some(checkpoint))
}

/// Clear (delete) the checkpoint file.
#[tauri::command]
pub async fn wizard_clear_checkpoint() -> Result<(), CascadeError> {
    let path = checkpoint_path()?;

    if !path.exists() {
        return Ok(());
    }

    fs::remove_file(&path)
        .map_err(|e| CascadeError::Custom(format!("failed to delete checkpoint: {}", e)))?;

    Ok(())
}

/// Check the audit log format and classify violations as format-mismatch or tampering.
/// JS: `invoke("wizard_check_audit_format")`
///
/// Returns AuditCheckResult with:
/// - `audit_log_exists`: true if ~/.cascade/audit.log exists
/// - `format_mismatch`: true if ALL records fail self-hash (pre-gate format)
/// - `is_tampered`: true if only some records fail (genuine tampering)
/// - `violation_count`: number of violations detected by verify_chain
#[tauri::command]
pub async fn wizard_check_audit_format() -> Result<AuditCheckResult, CascadeError> {
    use cascade_audit::AuditLog;
    use cascade_types::paths::home_dir;

    let audit_log_path = home_dir().join(".cascade").join("audit.log");

    // Check if log exists
    if !audit_log_path.exists() {
        return Ok(AuditCheckResult {
            audit_log_exists: false,
            format_mismatch: false,
            is_tampered: false,
            violation_count: 0,
        });
    }

    // Log exists — verify chain
    let log = AuditLog::open(&audit_log_path)
        .map_err(|e| CascadeError::Custom(format!("failed to open audit log: {}", e)))?;

    let violations = log
        .verify_chain()
        .map_err(|e| CascadeError::Custom(format!("failed to verify audit chain: {}", e)))?;

    if violations.is_empty() {
        // No violations — log is valid
        return Ok(AuditCheckResult {
            audit_log_exists: true,
            format_mismatch: false,
            is_tampered: false,
            violation_count: 0,
        });
    }

    // Violations detected — classify them
    let hash_mismatch_count = violations
        .iter()
        .filter(|v| v.reason.contains("hash does not match recomputed"))
        .count();

    // Format-mismatch only if EVERY violation is a hash mismatch (pre-gate format)
    let format_mismatch = hash_mismatch_count == violations.len();

    Ok(AuditCheckResult {
        audit_log_exists: true,
        format_mismatch,
        is_tampered: !format_mismatch,
        violation_count: violations.len() as u32,
    })
}

/// Rotate the audit log: archive old log to audit.log.pre-p3.<timestamp>
/// and remove the .count sidecar. Next daemon append starts a fresh chain from GENESIS.
/// JS: `invoke("wizard_rotate_audit_log")`
///
/// Returns the absolute path to the archived log file.
#[tauri::command]
pub async fn wizard_rotate_audit_log() -> Result<String, CascadeError> {
    use cascade_types::paths::home_dir;
    use std::time::{SystemTime, UNIX_EPOCH};

    let audit_log_path = home_dir().join(".cascade").join("audit.log");
    let count_sidecar_path = home_dir().join(".cascade").join("audit.log.count");

    // Check if log exists
    if !audit_log_path.exists() {
        return Err(CascadeError::Custom(
            "audit.log does not exist; nothing to rotate".into(),
        ));
    }

    // Generate archive name: audit.log.pre-p3.<unix-timestamp>
    let unix_ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(|e| CascadeError::Custom(format!("failed to get current time: {}", e)))?
        .as_secs();

    let archive_path = home_dir()
        .join(".cascade")
        .join(format!("audit.log.pre-p3.{}", unix_ts));

    // Rename (move) the log to archive
    fs::rename(&audit_log_path, &archive_path)
        .map_err(|e| CascadeError::Custom(format!("failed to archive audit log: {}", e)))?;

    // Remove the count sidecar (safe to ignore if it doesn't exist)
    let _ = fs::remove_file(&count_sidecar_path);

    Ok(archive_path.to_string_lossy().to_string())
}

// ---------------------------------------------------------------------------
// T-P3-E03-03: serde round-trip unit tests for WizardCheckpoint
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    /// Verify WizardCheckpoint serializes to camelCase JSON and round-trips intact.
    /// Acceptance: T-P3-E03-03 — "Rust unit test covers save→load round-trip with all fields"
    #[test]
    fn wizard_checkpoint_serde_camel_case_round_trip() {
        let original = WizardCheckpoint {
            step: 5,
            completed_steps: vec![1, 2, 3, 4],
            provider_connected: true,
            scan_result: Some(ScanResult {
                legacy_files_found: 42,
                total_size: 1_048_576,
                project_roots: vec!["/home/user/project".to_string()],
            }),
            merge_result: Some(MergeResult {
                merged_file_count: 40,
                conflicts_resolved: 2,
                timestamp: "2026-06-09T00:00:00Z".to_string(),
            }),
            archive_manifest_path: Some("/home/user/.cascade/manifest.json".to_string()),
            daemon_installed: false,
            started_at: "2026-06-09T00:00:00Z".to_string(),
            updated_at: "2026-06-09T00:01:00Z".to_string(),
            extra: serde_json::Map::new(),
        };

        let json = serde_json::to_string(&original).expect("serialization failed");

        // Verify camelCase keys are present in the JSON output.
        assert!(
            json.contains("\"completedSteps\""),
            "expected camelCase 'completedSteps', got: {}",
            json
        );
        assert!(
            json.contains("\"providerConnected\""),
            "expected camelCase 'providerConnected', got: {}",
            json
        );
        assert!(
            json.contains("\"scanResult\""),
            "expected camelCase 'scanResult', got: {}",
            json
        );
        assert!(
            json.contains("\"mergeResult\""),
            "expected camelCase 'mergeResult', got: {}",
            json
        );
        assert!(
            json.contains("\"archiveManifestPath\""),
            "expected camelCase 'archiveManifestPath', got: {}",
            json
        );
        assert!(
            json.contains("\"daemonInstalled\""),
            "expected camelCase 'daemonInstalled', got: {}",
            json
        );
        assert!(
            json.contains("\"startedAt\""),
            "expected camelCase 'startedAt', got: {}",
            json
        );
        assert!(
            json.contains("\"updatedAt\""),
            "expected camelCase 'updatedAt', got: {}",
            json
        );

        // Verify no snake_case keys leaked through.
        assert!(
            !json.contains("\"completed_steps\""),
            "snake_case key leaked: {}",
            json
        );
        assert!(
            !json.contains("\"provider_connected\""),
            "snake_case key leaked: {}",
            json
        );

        // ScanResult camelCase keys.
        assert!(
            json.contains("\"legacyFilesFound\""),
            "expected 'legacyFilesFound', got: {}",
            json
        );
        assert!(
            json.contains("\"totalSize\""),
            "expected 'totalSize', got: {}",
            json
        );
        assert!(
            json.contains("\"projectRoots\""),
            "expected 'projectRoots', got: {}",
            json
        );

        // MergeResult camelCase keys.
        assert!(
            json.contains("\"mergedFileCount\""),
            "expected 'mergedFileCount', got: {}",
            json
        );
        assert!(
            json.contains("\"conflictsResolved\""),
            "expected 'conflictsResolved', got: {}",
            json
        );

        // Round-trip: deserialize back and compare.
        let deserialized: WizardCheckpoint =
            serde_json::from_str(&json).expect("deserialization failed");

        assert_eq!(deserialized.step, original.step);
        assert_eq!(deserialized.completed_steps, original.completed_steps);
        assert_eq!(deserialized.provider_connected, original.provider_connected);
        assert_eq!(deserialized.daemon_installed, original.daemon_installed);
        assert_eq!(deserialized.started_at, original.started_at);
        assert_eq!(deserialized.updated_at, original.updated_at);
        assert_eq!(
            deserialized.archive_manifest_path,
            original.archive_manifest_path
        );

        let sr = deserialized
            .scan_result
            .expect("scan_result missing after round-trip");
        assert_eq!(sr.legacy_files_found, 42);
        assert_eq!(sr.total_size, 1_048_576);
        assert_eq!(sr.project_roots, vec!["/home/user/project"]);

        let mr = deserialized
            .merge_result
            .expect("merge_result missing after round-trip");
        assert_eq!(mr.merged_file_count, 40);
        assert_eq!(mr.conflicts_resolved, 2);
    }
}
