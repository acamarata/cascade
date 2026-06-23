// Tauri IPC commands — legacy tool home scanner.
//
// Purpose: Exposes the scanner service as Tauri IPC commands callable from
//   the React frontend via `invoke("scan_global_homes")` and
//   `invoke("scan_dev_tree", { root })`.
//
// Inputs:
//   scan_global_homes — no args; scans the 7 well-known global tool dirs.
//   scan_dev_tree     — root: String — absolute path of the dev tree to walk.
//
// Outputs: JSON arrays of LegacyToolHome, serialized with camelCase keys via
//   serde(rename_all = "camelCase") on the structs in scanner::types.
//
// Constraints:
//   - Delegates entirely to scanner::global::scan_global /
//     scanner::dev_tree::scan_dev_tree — no scan logic lives here.
//   - Both commands are synchronous (no async/await).
//   - Registered in lib.rs generate_handler!.
//
// SPORT: MASTER-COMPONENTS.md — scan_global_homes / scan_dev_tree commands
// Tasks: T-P3-E03-09 (scaffold); T-P3-E03-10 / T-P3-E03-11 (implementations)

use crate::scanner;
use crate::scanner::types::{LegacyToolHome, ToolId};

/// Discover legacy tool homes at standard global paths for all 7 supported tools.
///
/// Returns a JSON array of [`LegacyToolHome`] — one entry per tool with ≥1
/// file discovered. Missing tool paths are silently skipped.
///
/// Invoked from React as `invoke("scan_global_homes")`.
///
/// # Errors
/// Returns `Err(String)` on unexpected OS-level failures.
#[tauri::command]
pub fn scan_global_homes() -> Result<Vec<LegacyToolHome>, String> {
    let all_tools: Vec<ToolId> = vec![
        ToolId::ClaudeCode,
        ToolId::Opencode,
        ToolId::Codex,
        ToolId::Cursor,
        ToolId::Aider,
        ToolId::Windsurf,
        ToolId::Antigravity,
    ];
    scanner::global::scan_global(&all_tools)
}

/// Walk a dev tree root (default ~/Sites/) and discover per-project legacy
/// tool files up to depth 3.
///
/// Returns a JSON array of [`LegacyToolHome`] — one entry per project
/// directory containing ≥1 legacy tool file. Returns an empty array (not an
/// error) if `root` does not exist.
///
/// Invoked from React as `invoke("scan_dev_tree", { root: "$HOME/Sites" })`.
///
/// # Errors
/// Returns `Err(String)` on unexpected OS-level failures.
#[tauri::command]
pub fn scan_dev_tree(root: String) -> Result<Vec<LegacyToolHome>, String> {
    scanner::dev_tree::scan_dev_tree(&root)
}
