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
    let graph = cascade_core::maps::project::get_project_graph(sites_root.as_deref());
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

/// Map a `TierEntry.tier` slug to the `&'static str` label `scaffold_ai_folder`
/// expects (its own `starter_template`/result fields require `'static`, so we
/// can't just borrow the incoming `String`).
fn static_tier_label(tier: &str) -> Result<&'static str, CascadeError> {
    match tier {
        "GCI" => Ok("GCI"),
        "PCI" => Ok("PCI"),
        "APC" => Ok("APC"),
        "PPC" => Ok("PPC"),
        "PRC" => Ok("PRC"),
        "PAC" => Ok("PAC"),
        other => Err(CascadeError::InvalidParams(format!(
            "unknown tier '{other}' — expected one of GCI, PCI, APC, PPC, PRC, PAC"
        ))),
    }
}

/// Scaffold a missing cascade tier's `.cascade/` directory in place.
///
/// # Purpose
/// Backs the "Create" button in `CascadeTierTree` (T-P7-E10-03). Previously
/// that button only opened the parent directory in Finder — this actually
/// creates the tier's directory structure, skill suite, agents, `CASCADE.md`,
/// tool symlinks, and `.gitignore`, reusing the exact same scaffolding logic
/// `cascade init` uses (`scaffold_ai_folder`), so a tier created from the
/// desktop app is byte-identical to one created from the CLI.
///
/// # Inputs
/// - `tier`: One of `GCI`, `PCI`, `APC`, `PPC`, `PRC`, `PAC` (matches
///   `TierEntry.tier` from `get_cascade_tier_tree`).
/// - `path`: The tier's expected `CASCADE.md` path, as returned by
///   `get_cascade_tier_tree` (`TierEntry.path`). The `.cascade/` directory to
///   scaffold is this path's parent.
///
/// # Outputs
/// `{ dirs_created: string[], files_written: string[] }` — the relative
/// paths of everything actually created (empty arrays if the tier already
/// had everything, since scaffolding is idempotent).
///
/// # Constraints
/// - Never overwrites an existing `CASCADE.md`, symlinks, or `.gitignore`
///   (idempotent — only heals missing pieces).
/// - `system` is always auto-detected (git repo → PEWS skill suite; else →
///   Personal), matching `cascade init`'s own default when `--system` is
///   omitted.
/// # SPORT
/// MASTER-COMMANDS.md — create_cascade_tier (T-P7-E10-03)
#[tauri::command]
pub fn create_cascade_tier(tier: String, path: String) -> Result<serde_json::Value, CascadeError> {
    let tier_label = static_tier_label(&tier)?;

    let cascade_md_path = std::path::PathBuf::from(&path);
    let ai_dir = cascade_md_path.parent().ok_or_else(|| {
        CascadeError::InvalidParams(format!("path '{path}' has no parent directory"))
    })?;
    let folder_name = ai_dir
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_else(|| ".cascade".to_string());

    let scaffold =
        cascade_cli::cmd::init::scaffold_ai_folder(ai_dir, &folder_name, tier_label, false, None)
            .map_err(|e| CascadeError::Custom(e.to_string()))?;

    Ok(serde_json::json!({
        "dirsCreated": scaffold.dirs_created,
        "filesWritten": scaffold.files_written,
    }))
}
