/**
 * maps.ts — TypeScript types for the three project-map data providers.
 *
 * These types mirror the Rust structs in cascade-core::maps:
 *   - GraphNode / GraphEdge / GraphData  →  get_project_graph + get_pews_dag
 *   - TierEntry[]                        →  get_cascade_tier_tree
 *
 * All camelCase field names match the `#[serde(rename_all = "camelCase")]`
 * annotations in the Rust structs — no mapping layer needed.
 *
 * T-P3-E07-12 | SPORT: MASTER-COMPONENTS.md — maps TS types
 */

// ---------------------------------------------------------------------------
// Shared graph types (project graph + PEWS DAG)
// ---------------------------------------------------------------------------

/**
 * A single node in a project-map or PEWS dependency graph.
 *
 * `nodeType` is one of:
 *   - "project"  — top-level project directory (depth 1 under ~/Sites)
 *   - "repo"     — repository directory (depth 2)
 *   - "app"      — application subdirectory (depth 3)
 *   - "ticket"   — PEWS ticket (from T-*.yaml)
 *
 * `meta` carries optional provider-specific data:
 *   - For tickets: `{ status: string, weight: string }`
 *   - For project/repo/app nodes: currently absent (`undefined`)
 */
export interface GraphNode {
  /** Stable unique id within this graph (slug or ticket id). */
  id: string
  /** Human-readable display label. */
  label: string
  /** Semantic type: "project" | "repo" | "app" | "ticket". */
  nodeType: string
  /** Absolute filesystem path (project/repo/app) or empty string (ticket). */
  path: string
  /** Optional extra data — provider-specific. */
  meta?: Record<string, unknown>
}

/**
 * A directed edge between two nodes.
 *
 * For the project graph: `from` is the parent, `to` is the child.
 * For the PEWS DAG: `from` is the dependency, `to` is the dependent ticket.
 */
export interface GraphEdge {
  /** Source node id. */
  from: string
  /** Target node id. */
  to: string
}

/**
 * Complete graph returned by `get_project_graph` and `get_pews_dag`.
 *
 * `version` is always `1` until a breaking schema change; use it for
 * forward-compatibility checks in the renderer.
 */
export interface GraphData {
  /** Schema version — always 1 currently. */
  version: number
  /** All nodes in the graph. */
  nodes: GraphNode[]
  /** All edges in the graph. */
  edges: GraphEdge[]
}

// ---------------------------------------------------------------------------
// Cascade tier tree types
// ---------------------------------------------------------------------------

/**
 * One tier entry in the resolved cascade chain.
 *
 * Returned as an array of exactly six entries (GCI → PCI → APC → PPC → PRC → PAC)
 * by `get_cascade_tier_tree`.
 */
export interface TierEntry {
  /** Tier abbreviation: "GCI" | "PCI" | "APC" | "PPC" | "PRC" | "PAC". */
  tier: 'GCI' | 'PCI' | 'APC' | 'PPC' | 'PRC' | 'PAC'
  /** Human-readable tier name. */
  name: string
  /** Absolute path to the expected `.cascade/CASCADE.md` file. */
  path: string
  /** Whether the file exists on disk at call time. */
  exists: boolean
}

// ---------------------------------------------------------------------------
// Typed Tauri invoke wrappers
// ---------------------------------------------------------------------------

import { invoke } from '@tauri-apps/api/core'

/**
 * Scan ~/Sites (or a custom root) for cascade-managed projects and return
 * a project → repo → app graph.
 *
 * @param sitesRoot Optional override for the ~/Sites scan root.
 */
export function getProjectGraph(sitesRoot?: string): Promise<GraphData> {
  return invoke<GraphData>('get_project_graph', {
    sitesRoot: sitesRoot ?? null,
  })
}

/**
 * Resolve the six-tier cascade chain for a given project root.
 *
 * @param root Absolute path to the project/repo directory to inspect.
 */
export function getCascadeTierTree(root: string): Promise<TierEntry[]> {
  return invoke<TierEntry[]>('get_cascade_tier_tree', { root })
}

/**
 * Build the PEWS dependency DAG for the active phase under `phaseRoot`.
 *
 * @param phaseRoot Absolute path to the project root whose PEWS files to read.
 */
export function getPewsDag(phaseRoot: string): Promise<GraphData> {
  return invoke<GraphData>('get_pews_dag', { phaseRoot })
}
