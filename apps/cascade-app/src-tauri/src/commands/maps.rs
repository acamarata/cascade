// maps.rs — Tauri IPC commands for the three project-map data providers.
//
// Why: the Maps panel (T-P3-E07-13) needs three Tauri commands that call the
// cascade-core maps module and return structured JSON for React rendering.
// These are pure data-query commands — no daemon IPC, no network calls.
//
// IPC contract: all commands return serde_json::Value so new fields can be
// added to the Rust structs without breaking existing TypeScript consumers.
// The TypeScript bindings (T-P3-E07-12) should use the types in
// `src/types/maps.ts` which mirror the GraphData / TierEntry shapes.
//
// SPORT: MASTER-COMMANDS.md — get_project_graph, get_cascade_tier_tree, get_pews_dag

use crate::error::CascadeError;

/// Return the project graph: nodes (project/repo/app) with parent-child edges.
///
/// # Purpose
/// Powers the "Project Graph" map view. Scans `~/Sites/` (or `sites_root` when
/// provided) for cascade-managed directories at up to three nesting levels.
///
/// # Inputs
/// - `sites_root`: Optional override for the sites root path (default: `~/Sites`).
///
/// # Outputs
/// `GraphData` as JSON `{ version, nodes, edges }`.
///
/// # Constraints
/// - Scan depth is capped at 3 (project/repo/app) — never recurses further.
/// - Only directories containing `.cascade/` are included.
/// - All included paths must be HOME-confined.
/// # SPORT
/// MASTER-COMMANDS.md — get_project_graph
#[tauri::command]
pub fn get_project_graph(sites_root: Option<String>) -> Result<serde_json::Value, CascadeError> {
    let graph = cascade_core::maps::project::get_project_graph(
        sites_root.as_deref(),
    );
    serde_json::to_value(&graph).map_err(|e| CascadeError::Custom(e.to_string()))
}

/// Return the cascade tier tree for a given project root.
///
/// # Purpose
/// Powers the "Cascade Tier Tree" map view. For each of the six tiers
/// (GCI→PCI→APC→PPC→PRC→PAC), reports the canonical path and whether
/// the `CASCADE.md` file exists there.
///
/// # Inputs
/// - `root`: Absolute path to the project/repo directory to inspect.
///
/// # Outputs
/// Array of `TierEntry` as JSON `[{ tier, name, path, exists }, ...]`.
/// Always six entries in GCI→PAC resolution order.
///
/// # Constraints
/// - Tier paths are computed relative to `$HOME`; HOME-confined by construction.
/// - `exists` reflects the real filesystem at call time (not cached).
/// # SPORT
/// MASTER-COMMANDS.md — get_cascade_tier_tree
#[tauri::command]
pub fn get_cascade_tier_tree(root: String) -> Result<serde_json::Value, CascadeError> {
    let entries = cascade_core::maps::cascade_tiers::get_cascade_tier_tree(&root);
    serde_json::to_value(&entries).map_err(|e| CascadeError::Custom(e.to_string()))
}

/// Return the PEWS dependency DAG for the active phase under `phase_root`.
///
/// # Purpose
/// Powers the "PEWS DAG" map view. Reads all `T-*.yaml` ticket files under
/// `<phase_root>/.claude/phases/current/p*/` and returns a dependency graph.
///
/// # Inputs
/// - `phase_root`: Absolute path to the project root whose PEWS files to read.
///   Typically the cascade repo root or any project using PBD / PEWS.
///
/// # Outputs
/// `GraphData` as JSON `{ version, nodes, edges }` where:
/// - `node.nodeType = "ticket"`, `node.meta = { status, weight }`.
/// - Each edge encodes a `depends_on` relationship.
///
/// # Constraints
/// - Files with `.status.yaml` suffix are skipped (sidecars, not spec files).
/// - Missing `depends_on` field is treated as an empty list.
/// - Unparseable files are silently skipped.
/// # SPORT
/// MASTER-COMMANDS.md — get_pews_dag
#[tauri::command]
pub fn get_pews_dag(phase_root: String) -> Result<serde_json::Value, CascadeError> {
    let graph = cascade_core::maps::pews_dag::get_pews_dag(&phase_root);
    serde_json::to_value(&graph).map_err(|e| CascadeError::Custom(e.to_string()))
}
