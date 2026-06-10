//! # cascade_core::maps
//!
//! Data providers for the three project-map views rendered by the
//! T-P3-E07-xx Maps panel:
//!
//! 1. [`project`] — scans `~/Sites/` for cascade-managed projects and returns
//!    a [`GraphData`] of project → repo → app nodes with parent edges.
//! 2. [`cascade_tiers`] — resolves the six-tier cascade chain
//!    (GCI → PCI → APC → PPC → PRC → PAC) for a given project root.
//! 3. [`pews_dag`] — reads PEWS YAML under `<root>/.claude/phases/current/`
//!    and returns a dependency-graph of tickets with status/weight augmentation.
//!
//! ## Design
//!
//! All three providers are **synchronous** and **pure** (no daemon IPC, no
//! network calls). They operate only on `$HOME`-confined paths to satisfy the
//! security/permission constraints from the spec.
//!
//! The [`GraphData`] type is shared by providers 1 and 3; it is intentionally
//! extensible — new fields can be added to [`GraphNode`] / [`GraphEdge`] without
//! breaking existing deserialisers because `serde_json` ignores unknown fields
//! by default.
//!
//! ## SPORT
//! MASTER-COMPONENTS.md — maps module (cascade-core::maps)

pub mod cascade_tiers;
pub mod pews_dag;
pub mod project;

// ---------------------------------------------------------------------------
// Shared graph data model (used by project.rs and pews_dag.rs)
// ---------------------------------------------------------------------------

use serde::{Deserialize, Serialize};

/// A single node in a project-map or PEWS dependency graph.
///
/// # Purpose
/// Represents any entity in a graph view (project, repo, app, ticket).
/// The `nodeType` field disambiguates meaning; `meta` carries optional
/// provider-specific data without breaking the schema.
///
/// # Inputs
/// Constructed by the three data-provider functions; never built by the frontend.
///
/// # Outputs
/// Serialised to JSON by Tauri IPC; consumed by the React graph renderer (T-P3-E07-13).
///
/// # Constraints
/// Fields use `camelCase` so the TypeScript frontend needs no mapping layer.
/// The `meta` field is optional and must never be relied on for core display logic.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct GraphNode {
    /// Stable unique id within this graph (e.g. slug or ticket id).
    pub id: String,
    /// Human-readable display label.
    pub label: String,
    /// Semantic type: "project" | "repo" | "app" | "ticket".
    pub node_type: String,
    /// Absolute filesystem path (project/repo/app) or empty string (ticket).
    pub path: String,
    /// Optional extra data (status, weight, etc.) passed through to the frontend.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub meta: Option<serde_json::Value>,
}

/// A directed edge between two nodes.
///
/// # Purpose
/// Encodes a parent-child or depends-on relationship between two [`GraphNode`]s.
///
/// # Constraints
/// `from` and `to` must reference `id` values that exist in the same [`GraphData`]
/// nodes list. The frontend renderer will silently drop dangling edges.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct GraphEdge {
    /// Source node id.
    pub from: String,
    /// Target node id.
    pub to: String,
}

/// A complete graph: a list of nodes and the directed edges between them.
///
/// # Purpose
/// Top-level return value for `get_project_graph` and `get_pews_dag` IPC commands.
/// Designed to be extensible — the `version` field lets the frontend detect schema
/// changes without breaking on new optional node/edge fields.
///
/// # Constraints
/// `version` is always `1` in this implementation. Increment only on breaking changes.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct GraphData {
    /// Schema version (currently `1`).
    pub version: u32,
    /// All nodes in the graph.
    pub nodes: Vec<GraphNode>,
    /// All edges in the graph.
    pub edges: Vec<GraphEdge>,
}

impl GraphData {
    /// Construct an empty graph at the current schema version.
    pub fn empty() -> Self {
        GraphData {
            version: 1,
            nodes: Vec::new(),
            edges: Vec::new(),
        }
    }
}
