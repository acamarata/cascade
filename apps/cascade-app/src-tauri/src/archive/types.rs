// Archive types — Rust counterparts to apps/cascade-app/src/lib/archive/types.ts.
//
// Purpose: Defines the manifest schema for ~/.cascade/legacy/manifest.json.
//   Records every file moved during archive so the operation is fully reversible.
//
// Inputs:  None — type declarations only.
// Outputs: Types consumed by archive::engine, archive::validation,
//          archive::restore, and commands::archive.
//
// Constraints:
//   - `serde(rename_all = "camelCase")` on EVERY struct — mandatory for
//     JSON ↔ TypeScript field parity (matches src/lib/archive/types.ts).
//   - All path fields use PathBuf internally; serialized as absolute strings.
//   - ArchiveManifest.version is a semver string for future migration.
//     Use serde_json::Value for forward-compatible deserialization on mismatch.
//
// SPORT: MASTER-COMPONENTS.md — ArchiveManifest / ToolArchive types — archive/types.rs
// Task: T-P3-E03-15

use std::collections::HashMap;
use std::path::PathBuf;

use serde::{Deserialize, Serialize};
use serde_json::Value;

// ---------------------------------------------------------------------------
// ArchivedFile — single file/dir entry moved during archive
// ---------------------------------------------------------------------------

/// Records one file or directory moved during an archive operation.
///
/// Both paths are absolute, fully resolved (no `~/` shorthand).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ArchivedFile {
    /// Absolute path where the file lived before archiving.
    pub original_path: PathBuf,
    /// Absolute path inside `~/.cascade/legacy/{tool_id}/` where the file now lives.
    pub archived_path: PathBuf,
    /// ISO 8601 timestamp of when the file was moved.
    pub moved_at: String,
    /// File size in bytes at the time of archiving.
    pub size_bytes: u64,
}

// ---------------------------------------------------------------------------
// ToolArchive — all files archived for one tool
// ---------------------------------------------------------------------------

/// Records the complete archive of one AI coding tool's legacy home directory.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolArchive {
    /// Kebab-case tool identifier, e.g. `claude-code`, `opencode`.
    pub tool_id: String,
    /// Absolute original root that was archived (e.g. `~/.claude`).
    pub original_root: PathBuf,
    /// Absolute destination root inside `~/.cascade/legacy/`.
    pub archive_root: PathBuf,
    /// ISO 8601 timestamp of when the archive operation completed.
    pub archived_at: String,
    /// Every file and directory moved during this archive operation.
    pub files: Vec<ArchivedFile>,
}

// ---------------------------------------------------------------------------
// ArchiveManifest — root manifest.json document
// ---------------------------------------------------------------------------

/// Root document written to `~/.cascade/legacy/manifest.json`.
///
/// `version` is a semver string (`"1.0"`) enabling forward-compatible schema
/// migration. Deserializers should check this field before parsing `tools`.
///
/// `extras` uses `#[serde(flatten, default)]` to absorb unknown fields written
/// by future versions of Cascade. This ensures older app versions can still
/// parse a manifest that was created by a newer version — unknown fields are
/// preserved in `extras` and survive a round-trip without data loss.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ArchiveManifest {
    /// Manifest schema version — currently `"1.0"`.
    pub version: String,
    /// ISO 8601 timestamp of when this manifest was first created.
    pub created_at: String,
    /// All tool archives. One entry per archived tool.
    pub tools: Vec<ToolArchive>,
    /// Forward-compat catch-all: unknown fields from future schema versions.
    /// Absorbed on read; re-emitted on write so data is never lost.
    #[serde(flatten, default)]
    pub extras: HashMap<String, Value>,
}

// ---------------------------------------------------------------------------
// ArchiveValidation — pre-flight check result (T-P3-E03-18)
// ---------------------------------------------------------------------------

/// A single issue detected during pre-flight validation.
///
/// `InsufficientDisk` and `PermissionDenied` are fatal; others are warnings
/// that allow the user to proceed.
///
/// `tag = "type"` produces `{"type":"InsufficientDisk","requiredBytes":...}` —
/// matching the TypeScript discriminated union shape.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type")]
pub enum ArchiveIssue {
    InsufficientDisk {
        #[serde(rename = "requiredBytes")]
        required_bytes: u64,
        #[serde(rename = "availableBytes")]
        available_bytes: u64,
    },
    DestinationExists {
        path: PathBuf,
    },
    PermissionDenied {
        path: PathBuf,
    },
    SourceNotFound {
        path: PathBuf,
    },
    ActiveProcessUsing {
        #[serde(rename = "toolName")]
        tool_name: String,
    },
}

/// Result of the `validate_archive` pre-flight check.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ArchiveValidation {
    /// `true` only when `issues` is empty.
    pub ok: bool,
    /// Detected issues; may be empty.
    pub issues: Vec<ArchiveIssue>,
}

// ---------------------------------------------------------------------------
// RestoreResult — outcome of restore_tool (T-P3-E03-19)
// ---------------------------------------------------------------------------

/// Summary result returned by `restore_tool`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct RestoreResult {
    /// Number of files successfully moved back to their original paths.
    pub restored_files: usize,
    /// Number of files skipped due to conflicts at the original path.
    pub skipped_conflicts: usize,
    /// Error messages for files that could not be moved or cleanly skipped.
    pub errors: Vec<String>,
}

// ---------------------------------------------------------------------------
// Tests — JSON round-trip asserting camelCase keys
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    /// Verifies camelCase serialization parity with the TypeScript types.
    /// A regression here breaks the Tauri IPC contract with the frontend.
    #[test]
    fn archive_manifest_roundtrip_camel_case() {
        let manifest = ArchiveManifest {
            version: "1.0".to_string(),
            created_at: "2026-06-09T00:00:00Z".to_string(),
            tools: vec![ToolArchive {
                tool_id: "claude-code".to_string(),
                original_root: PathBuf::from("/Users/admin/.claude"),
                archive_root: PathBuf::from("/Users/admin/.cascade/legacy/claude-code"),
                archived_at: "2026-06-09T00:01:00Z".to_string(),
                files: vec![ArchivedFile {
                    original_path: PathBuf::from("/Users/admin/.claude/settings.json"),
                    archived_path: PathBuf::from(
                        "/Users/admin/.cascade/legacy/claude-code/settings.json",
                    ),
                    moved_at: "2026-06-09T00:01:00Z".to_string(),
                    size_bytes: 1024,
                }],
            }],
            extras: HashMap::new(),
        };

        let json = serde_json::to_string(&manifest).expect("serialize failed");

        // Assert camelCase keys are present in the serialized output.
        assert!(json.contains("\"version\""), "missing version key");
        assert!(
            json.contains("\"createdAt\""),
            "missing createdAt key — snake_case leak"
        );
        assert!(json.contains("\"tools\""), "missing tools key");
        assert!(
            json.contains("\"toolId\""),
            "missing toolId key — snake_case leak"
        );
        assert!(
            json.contains("\"originalRoot\""),
            "missing originalRoot key"
        );
        assert!(json.contains("\"archiveRoot\""), "missing archiveRoot key");
        assert!(json.contains("\"archivedAt\""), "missing archivedAt key");
        assert!(
            json.contains("\"originalPath\""),
            "missing originalPath key"
        );
        assert!(
            json.contains("\"archivedPath\""),
            "missing archivedPath key"
        );
        assert!(json.contains("\"movedAt\""), "missing movedAt key");
        assert!(json.contains("\"sizeBytes\""), "missing sizeBytes key");

        // Assert NO snake_case keys leak through.
        assert!(
            !json.contains("\"created_at\""),
            "snake_case created_at leaked"
        );
        assert!(!json.contains("\"tool_id\""), "snake_case tool_id leaked");
        assert!(
            !json.contains("\"original_root\""),
            "snake_case original_root leaked"
        );

        // Round-trip: deserialize back and compare.
        let roundtrip: ArchiveManifest = serde_json::from_str(&json).expect("deserialize failed");
        assert_eq!(manifest, roundtrip);
        assert_eq!(roundtrip.version, "1.0");
        assert_eq!(roundtrip.tools.len(), 1);
        assert_eq!(roundtrip.tools[0].tool_id, "claude-code");
        assert_eq!(roundtrip.tools[0].files[0].size_bytes, 1024);
    }

    #[test]
    fn archive_validation_serializes_camel_case() {
        let v = ArchiveValidation {
            ok: false,
            issues: vec![
                ArchiveIssue::InsufficientDisk {
                    required_bytes: 1_000_000,
                    available_bytes: 500_000,
                },
                ArchiveIssue::PermissionDenied {
                    path: PathBuf::from("/Users/admin/.cascade/legacy"),
                },
            ],
        };
        let json = serde_json::to_string(&v).unwrap();
        assert!(json.contains("\"ok\""));
        assert!(json.contains("\"issues\""));
        assert!(json.contains("\"requiredBytes\""), "missing requiredBytes");
        assert!(
            json.contains("\"availableBytes\""),
            "missing availableBytes"
        );
        assert!(!json.contains("\"required_bytes\""), "snake_case leak");
    }

    #[test]
    fn restore_result_serializes_camel_case() {
        let r = RestoreResult {
            restored_files: 5,
            skipped_conflicts: 1,
            errors: vec!["could not move foo".to_string()],
        };
        let json = serde_json::to_string(&r).unwrap();
        assert!(json.contains("\"restoredFiles\""), "missing restoredFiles");
        assert!(
            json.contains("\"skippedConflicts\""),
            "missing skippedConflicts"
        );
        assert!(!json.contains("\"restored_files\""), "snake_case leak");
    }

    /// Verifies that ArchiveManifest absorbs unknown fields from a future schema
    /// version without failing (forward-compat via `#[serde(flatten, default)]`).
    /// Unknown fields survive a round-trip: they are preserved in `extras`.
    #[test]
    fn archive_manifest_forward_compat_unknown_field() {
        // Raw JSON that a future version might write — includes known fields
        // PLUS an unknown "author" field that this version does not declare.
        let raw = r#"{
            "version": "2.0",
            "createdAt": "2026-06-19T00:00:00Z",
            "tools": [],
            "author": "cascade-next"
        }"#;

        // Deserialization must succeed even though "author" is unknown.
        let roundtrip: ArchiveManifest =
            serde_json::from_str(raw).expect("forward-compat deserialization failed");

        assert_eq!(roundtrip.version, "2.0");
        assert_eq!(roundtrip.created_at, "2026-06-19T00:00:00Z");
        assert!(roundtrip.tools.is_empty());

        // The unknown field must be captured in extras.
        assert!(
            roundtrip.extras.contains_key("author"),
            "extras did not absorb unknown 'author' field"
        );
        assert_eq!(
            roundtrip.extras["author"],
            serde_json::Value::String("cascade-next".to_string())
        );

        // Serialize back: known camelCase fields must still be present.
        let back = serde_json::to_string(&roundtrip).expect("re-serialize failed");
        assert!(back.contains("\"version\""), "version missing after round-trip");
        assert!(
            back.contains("\"createdAt\""),
            "createdAt missing after round-trip"
        );
        assert!(back.contains("\"tools\""), "tools missing after round-trip");
        // The unknown field is re-emitted (no data loss).
        assert!(
            back.contains("\"author\""),
            "unknown field 'author' was lost on re-serialize"
        );
    }
}
