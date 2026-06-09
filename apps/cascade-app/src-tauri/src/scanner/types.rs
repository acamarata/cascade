// Scanner types — Rust counterparts to apps/cascade-app/src/lib/scanner/types.ts.
//
// Purpose: Defines the core data structures for legacy tool home discovery.
//   These types are serialized to JSON and sent to the React frontend via
//   Tauri IPC; field names are therefore camelCase in JSON (serde rename).
//
// Inputs:  None — type declarations only.
// Outputs: Exported types consumed by scanner::global, scanner::dev_tree, and
//          the commands/scanner.rs Tauri command surface.
//
// Constraints:
//   - `serde(rename_all = "camelCase")` on every struct: JSON ↔ TypeScript
//     field parity is mandatory. W-01 QA failed on snake/camel mismatch.
//   - `serde(rename_all = "kebab-case")` on ToolId: matches the TypeScript
//     ToolId string literal union ('claude-code', 'opencode', …).
//   - No scanning logic — types only.
//
// SPORT: MASTER-COMPONENTS.md — LegacyToolHome / ScanResult types — scanner/types.rs
// Task: T-P3-E03-09

use serde::{Deserialize, Serialize};

// ---------------------------------------------------------------------------
// ToolId — the 7 supported AI coding tools
// ---------------------------------------------------------------------------

/// Identifies one of the 7 AI coding tools Cascade can discover and manage.
///
/// Serializes to kebab-case strings ('claude-code', 'opencode', …) so JSON
/// round-trips transparently with the TypeScript `ToolId` type.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum ToolId {
    ClaudeCode,
    Opencode,
    Codex,
    Cursor,
    Aider,
    Windsurf,
    Antigravity,
}

// ---------------------------------------------------------------------------
// FileKind — semantic classification of a discovered file
// ---------------------------------------------------------------------------

/// The semantic role of a file found inside a legacy tool home.
///
/// Serializes to lowercase strings matching the TypeScript `FileKind` union.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum FileKind {
    Instructions,
    Memory,
    Docs,
    Tasks,
    Ideas,
    Other,
}

// ---------------------------------------------------------------------------
// LegacyFile — a single file discovered inside a tool home
// ---------------------------------------------------------------------------

/// A single file found inside a legacy tool home directory.
///
/// All fields serialize to camelCase to match the TypeScript `LegacyFile`
/// interface field names.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LegacyFile {
    /// Absolute filesystem path to the file.
    pub path: String,
    /// File size in bytes.
    pub size_bytes: u64,
    /// Semantic classification of the file's role.
    pub kind: FileKind,
}

// ---------------------------------------------------------------------------
// LegacyToolHome — aggregated info for one tool
// ---------------------------------------------------------------------------

/// All legacy files found for a single AI tool across global and per-project paths.
///
/// Fields serialize to camelCase to match `LegacyToolHome` in TypeScript.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LegacyToolHome {
    /// Which tool this record belongs to.
    pub tool_id: ToolId,
    /// Global config directories that were scanned (may be empty).
    pub global_paths: Vec<String>,
    /// Per-project directories containing tool files (may be empty).
    pub per_project_paths: Vec<String>,
    /// Total number of files discovered across all paths.
    pub total_files: u32,
    /// Aggregate size of all discovered files in bytes.
    pub total_size_bytes: u64,
}

// ---------------------------------------------------------------------------
// ScanResult — top-level result returned by scan commands
// ---------------------------------------------------------------------------

/// The result of a full scanner pass (global or dev-tree).
///
/// Fields serialize to camelCase to match `ScanResult` in TypeScript.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ScanResult {
    /// One entry per tool that had at least one file discovered.
    pub homes: Vec<LegacyToolHome>,
    /// ISO-8601 timestamp when the scan completed.
    pub scanned_at: String,
    /// The dev-tree root that was scanned, or None for global scans.
    pub dev_tree_root: Option<String>,
}

// ---------------------------------------------------------------------------
// Unit tests — camelCase JSON serialization parity with TypeScript
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    /// Serialize a LegacyToolHome and assert all JSON keys are camelCase.
    /// This guards against the snake_case regression that failed W-01 QA.
    #[test]
    fn legacy_tool_home_serializes_camel_case() {
        let home = LegacyToolHome {
            tool_id: ToolId::ClaudeCode,
            global_paths: vec!["/home/user/.claude".to_string()],
            per_project_paths: vec![],
            total_files: 3,
            total_size_bytes: 1024,
        };

        let json = serde_json::to_string(&home).expect("serialize");
        let value: serde_json::Value = serde_json::from_str(&json).expect("deserialize");

        // All struct field names must appear as camelCase keys.
        assert!(value.get("toolId").is_some(),       "expected 'toolId', got: {json}");
        assert!(value.get("globalPaths").is_some(),  "expected 'globalPaths', got: {json}");
        assert!(value.get("perProjectPaths").is_some(), "expected 'perProjectPaths', got: {json}");
        assert!(value.get("totalFiles").is_some(),   "expected 'totalFiles', got: {json}");
        assert!(value.get("totalSizeBytes").is_some(), "expected 'totalSizeBytes', got: {json}");

        // Snake_case keys must NOT appear.
        assert!(value.get("tool_id").is_none(),        "snake_case 'tool_id' leaked into JSON");
        assert!(value.get("global_paths").is_none(),   "snake_case 'global_paths' leaked into JSON");
        assert!(value.get("total_size_bytes").is_none(),"snake_case 'total_size_bytes' leaked into JSON");

        // ToolId serializes to kebab-case string.
        assert_eq!(value["toolId"], "claude-code", "ToolId should be 'claude-code'");
    }

    /// ScanResult fields also round-trip as camelCase.
    #[test]
    fn scan_result_serializes_camel_case() {
        let result = ScanResult {
            homes: vec![],
            scanned_at: "2026-06-09T00:00:00Z".to_string(),
            dev_tree_root: Some("/home/user/Sites".to_string()),
        };

        let json = serde_json::to_string(&result).expect("serialize");
        let value: serde_json::Value = serde_json::from_str(&json).expect("deserialize");

        assert!(value.get("scannedAt").is_some(),   "expected 'scannedAt'");
        assert!(value.get("devTreeRoot").is_some(), "expected 'devTreeRoot'");
        assert!(value.get("scanned_at").is_none(),  "snake_case 'scanned_at' leaked");
    }

    /// All 7 ToolId variants serialize to the expected kebab-case strings.
    #[test]
    fn tool_id_variants_serialize_kebab_case() {
        let cases = [
            (ToolId::ClaudeCode,  "\"claude-code\""),
            (ToolId::Opencode,    "\"opencode\""),
            (ToolId::Codex,       "\"codex\""),
            (ToolId::Cursor,      "\"cursor\""),
            (ToolId::Aider,       "\"aider\""),
            (ToolId::Windsurf,    "\"windsurf\""),
            (ToolId::Antigravity, "\"antigravity\""),
        ];
        for (variant, expected) in cases {
            let json = serde_json::to_string(&variant).expect("serialize");
            assert_eq!(json, expected, "ToolId::{variant:?} did not serialize to {expected}");
        }
    }
}
