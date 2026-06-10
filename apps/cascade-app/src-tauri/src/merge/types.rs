// merge/types.rs — Rust counterparts to apps/cascade-app/src/features/onboarding/merge/types.ts.
//
// Purpose: Defines the data contract for the AI-driven legacy content merge engine.
//   These types cross the Tauri IPC boundary; all JSON keys are camelCase to match
//   the TypeScript types exactly.
//
// Inputs:  None — type declarations only.
// Outputs: Types consumed by merge::reader, merge::ai_service, merge::writer,
//          merge::conflict, and commands::merge.
//
// Constraints:
//   - `serde(rename_all = "camelCase")` on every struct — mandatory for JSON ↔ TypeScript parity.
//   - MergeSection.id is a String UUID; generated on the TypeScript side via crypto.randomUUID().
//   - SectionStatus serializes as a lowercase string matching the TypeScript union.
//   - FileKind and ToolId are re-exported from the scanner module (no duplication).
//
// SPORT: MASTER-COMPONENTS.md — MergeSection / MergeResult / MergeConflict types — merge/types.rs
// Task: T-P3-E03-23

use serde::{Deserialize, Serialize};

use crate::scanner::types::{FileKind, ToolId};

// ---------------------------------------------------------------------------
// SectionStatus — lifecycle state of one merged section
// ---------------------------------------------------------------------------

/// The review state of a MergeSection.
///
/// Serializes as a lowercase string to match the TypeScript `SectionStatus` union.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SectionStatus {
    Pending,
    Approved,
    Rejected,
    Edited,
}

// ---------------------------------------------------------------------------
// MergeSourceFile — one legacy file contributing to a merge
// ---------------------------------------------------------------------------

/// A single legacy file that feeds into the AI merge for one tier/section.
///
/// `content` is `None` if the file was skipped (binary or >1 MB).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MergeSourceFile {
    /// Which AI tool this file came from.
    pub tool_id: ToolId,
    /// Absolute path to the original file.
    pub path: String,
    /// UTF-8 text content of the file; `None` if skipped.
    pub content: Option<String>,
    /// Semantic classification matching LegacyFile.kind.
    pub kind: FileKind,
}

// ---------------------------------------------------------------------------
// MergeSection — one logical output section from the AI merge
// ---------------------------------------------------------------------------

/// A single section produced by the AI merge pass for a given cascade tier.
///
/// The user reviews each section in the DiffPanel and sets its status.
/// `edited_content` is present only when `status == SectionStatus::Edited`.
/// `conflict_with` references another section's UUID id when overlap is detected.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MergeSection {
    /// UUID string generated at merge time.
    pub id: String,
    /// Human-readable section title.
    pub title: String,
    /// Legacy files this section was built from.
    pub source_files: Vec<MergeSourceFile>,
    /// AI-generated markdown content for this section.
    pub proposed_content: String,
    /// Current review status.
    pub status: SectionStatus,
    /// User-edited content; only set when `status == Edited`.
    pub edited_content: Option<String>,
    /// UUID of a conflicting sibling section, if any.
    pub conflict_with: Option<String>,
}

// ---------------------------------------------------------------------------
// MergeResult — full AI merge output for one wizard tier pass
// ---------------------------------------------------------------------------

/// The complete result of one AI merge pass covering a single cascade tier.
///
/// `tier` is an integer matching the `WizardStep` const enum on the TypeScript side.
/// `prompt_hash` is a SHA-256 hex string of the user prompt used in the AI call.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MergeResult {
    /// WizardStep integer value (e.g. 4 = MergeContent).
    pub tier: u32,
    /// All sections produced by the AI pass, pending user review.
    pub sections: Vec<MergeSection>,
    /// ISO-8601 timestamp when the AI call completed.
    pub generated_at: String,
    /// Provider/model identifier (e.g. "gemini-2.0-flash").
    pub model_used: String,
    /// SHA-256 hex of the user prompt for re-run identity detection.
    pub prompt_hash: String,
}

// ---------------------------------------------------------------------------
// MergeConflict — advisory conflict between two sections
// ---------------------------------------------------------------------------

/// Flags two MergeSection instances that may overlap or contradict.
/// Produced by `merge::conflict`; advisory only — the user decides.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MergeConflict {
    /// First conflicting section.
    pub section_a: MergeSection,
    /// Second conflicting section.
    pub section_b: MergeSection,
    /// Human-readable description of why these sections conflict.
    pub reason: String,
}

// ---------------------------------------------------------------------------
// MergeSourceFileRaw — reader output, includes size metadata
// ---------------------------------------------------------------------------

/// Output of `read_legacy_content` — like MergeSourceFile but with byte count
/// and a flag for files that were skipped due to size or binary content.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct MergeSourceFileRaw {
    /// Which AI tool this file came from.
    pub tool_id: String,
    /// Absolute path to the original file.
    pub path: String,
    /// UTF-8 text content; `None` if skipped (binary, >1 MB, or truncated).
    pub content: Option<String>,
    /// Semantic classification.
    pub kind: FileKind,
    /// File size in bytes (from filesystem; accurate even when content is None).
    pub size_bytes: u64,
}

// ---------------------------------------------------------------------------
// ApprovedSection — input to write_cascade_content
// ---------------------------------------------------------------------------

/// A user-approved section ready for writing to ~/.cascade/CASCADE.md.
/// `content` is either `proposedContent` or `editedContent` depending on
/// whether the user edited the AI output.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ApprovedSection {
    /// UUID matching the source MergeSection.id.
    pub id: String,
    /// Section heading text (written as `## {title}`).
    pub title: String,
    /// Final content to write (proposed or edited).
    pub content: String,
}

// ---------------------------------------------------------------------------
// WriteResult — outcome of write_cascade_content
// ---------------------------------------------------------------------------

/// Summary result returned by `write_cascade_content`.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct WriteResult {
    /// Absolute path of the file that was written.
    pub path: String,
    /// Number of bytes in the file after writing.
    pub bytes_written: u64,
    /// Number of sections written in this call.
    pub section_count: usize,
}

// ---------------------------------------------------------------------------
// Tests — JSON round-trip asserting camelCase keys (serde parity gate)
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    /// Serializes a MergeSection and verifies all JSON keys are camelCase.
    /// Guards against snake_case regression that would break the Tauri IPC contract.
    #[test]
    fn merge_section_roundtrip_camel_case() {
        let section = MergeSection {
            id: "550e8400-e29b-41d4-a716-446655440000".to_string(),
            title: "Code Quality Rules".to_string(),
            source_files: vec![MergeSourceFile {
                tool_id: ToolId::ClaudeCode,
                path: "/Users/admin/.claude/CLAUDE.md".to_string(),
                content: Some("no any types allowed".to_string()),
                kind: FileKind::Instructions,
            }],
            proposed_content: "## Code Quality\nNo any types.".to_string(),
            status: SectionStatus::Pending,
            edited_content: None,
            conflict_with: None,
        };

        let json = serde_json::to_string(&section).expect("serialize failed");

        // Assert camelCase keys.
        assert!(json.contains("\"id\""), "missing id key");
        assert!(json.contains("\"title\""), "missing title key");
        assert!(
            json.contains("\"sourceFiles\""),
            "missing sourceFiles — snake_case leak"
        );
        assert!(
            json.contains("\"proposedContent\""),
            "missing proposedContent — snake_case leak"
        );
        assert!(json.contains("\"status\""), "missing status key");
        assert!(
            json.contains("\"editedContent\""),
            "missing editedContent — snake_case leak"
        );
        assert!(
            json.contains("\"conflictWith\""),
            "missing conflictWith — snake_case leak"
        );
        assert!(
            json.contains("\"toolId\""),
            "missing toolId in sourceFiles — snake_case leak"
        );
        assert!(
            json.contains("\"sizeBytes\"") || !json.contains("sizeBytes"),
            "sizeBytes check"
        );

        // Assert NO snake_case keys.
        assert!(
            !json.contains("\"source_files\""),
            "snake_case source_files leaked"
        );
        assert!(
            !json.contains("\"proposed_content\""),
            "snake_case proposed_content leaked"
        );
        assert!(
            !json.contains("\"edited_content\""),
            "snake_case edited_content leaked"
        );
        assert!(
            !json.contains("\"conflict_with\""),
            "snake_case conflict_with leaked"
        );
        assert!(!json.contains("\"tool_id\""), "snake_case tool_id leaked");

        // SectionStatus serializes as lowercase string.
        assert!(
            json.contains("\"pending\""),
            "SectionStatus::Pending should serialize as 'pending'"
        );

        // Round-trip.
        let roundtrip: MergeSection = serde_json::from_str(&json).expect("deserialize failed");
        assert_eq!(roundtrip.id, section.id);
        assert_eq!(roundtrip.title, section.title);
        assert_eq!(roundtrip.status, SectionStatus::Pending);
        assert_eq!(roundtrip.source_files.len(), 1);
        assert_eq!(
            roundtrip.source_files[0].path,
            "/Users/admin/.claude/CLAUDE.md"
        );
    }

    #[test]
    fn merge_result_camel_case() {
        let result = MergeResult {
            tier: 4,
            sections: vec![],
            generated_at: "2026-06-09T00:00:00Z".to_string(),
            model_used: "gemini-2.0-flash".to_string(),
            prompt_hash: "abc123".to_string(),
        };
        let json = serde_json::to_string(&result).expect("serialize");
        assert!(json.contains("\"generatedAt\""), "missing generatedAt");
        assert!(json.contains("\"modelUsed\""), "missing modelUsed");
        assert!(json.contains("\"promptHash\""), "missing promptHash");
        assert!(
            !json.contains("\"generated_at\""),
            "snake_case generated_at leaked"
        );
        assert!(
            !json.contains("\"model_used\""),
            "snake_case model_used leaked"
        );
        assert!(
            !json.contains("\"prompt_hash\""),
            "snake_case prompt_hash leaked"
        );
    }

    #[test]
    fn section_status_variants_serialize_lowercase() {
        let cases = [
            (SectionStatus::Pending, "\"pending\""),
            (SectionStatus::Approved, "\"approved\""),
            (SectionStatus::Rejected, "\"rejected\""),
            (SectionStatus::Edited, "\"edited\""),
        ];
        for (variant, expected) in cases {
            let json = serde_json::to_string(&variant).expect("serialize");
            assert_eq!(
                json, expected,
                "SectionStatus::{variant:?} did not serialize to {expected}"
            );
        }
    }

    #[test]
    fn write_result_camel_case() {
        let wr = WriteResult {
            path: "/Users/admin/.cascade/CASCADE.md".to_string(),
            bytes_written: 1024,
            section_count: 3,
        };
        let json = serde_json::to_string(&wr).expect("serialize");
        assert!(json.contains("\"bytesWritten\""), "missing bytesWritten");
        assert!(json.contains("\"sectionCount\""), "missing sectionCount");
        assert!(
            !json.contains("\"bytes_written\""),
            "snake_case bytes_written leaked"
        );
        assert!(
            !json.contains("\"section_count\""),
            "snake_case section_count leaked"
        );
    }
}
