/**
 * Purpose: Pure graph-data builder — converts a VaultNode tree + wikilink text
 *   map into D3-compatible node/edge arrays for the force simulation.
 *   Zero React, zero D3, zero DOM. Fully testable in Vitest.
 * Inputs:  VaultNode tree (from VaultContext.treeData) +
 *          a content map (path → markdown string) for wikilink extraction.
 *          OR a pre-built edges source (LinkIndex seam) for later integration.
 * Outputs: GraphNode[], GraphEdge[] ready for d3-force.
 * Constraints: No side effects. All helpers are pure. <=300 lines.
 * SPORT: MASTER-COMPONENTS.md — graphData (T-P3-E06-07)
 */

import { findWikilinks } from '../editor/wikilinks/wikilinkParser'
import type { VaultNode } from '../../../types/vault'

// ── Graph types ───────────────────────────────────────────────────────────────

/**
 * A node in the vault connection graph.
 * `id` is the absolute path of the .md file.
 */
export interface GraphNode {
  /** Absolute path — unique node identifier. */
  id: string
  /** Display label (filename without extension). */
  label: string
  /**
   * In-degree: number of OTHER notes that link TO this note.
   * Used to size the node circle: r = BASE_R + inDegree * DEG_SCALE.
   */
  inDegree: number
  /** Directory path — used for directory-based colouring. */
  dir: string
  // d3-force simulation fields (mutable; populated by D3 at runtime)
  x?: number
  y?: number
  vx?: number
  vy?: number
  fx?: number | null
  fy?: number | null
}

/**
 * A directed edge between two graph nodes.
 * `source` and `target` may be resolved to GraphNode references by d3-forceLink.
 */
export interface GraphEdge {
  /** Absolute path of the source (linking) note. */
  source: string | GraphNode
  /** Absolute path of the target (linked) note. */
  target: string | GraphNode
}

// ── Node radius scale ─────────────────────────────────────────────────────────

/** Minimum node radius (pixels). */
export const BASE_R = 4
/** Radius added per in-degree unit. */
export const DEG_SCALE = 1.5
/** Maximum clamped node radius (pixels). */
export const MAX_R = 16

/**
 * Compute the SVG circle radius for a node given its in-degree.
 * @param inDegree  Number of backlinks pointing at the node.
 */
export function nodeRadius(inDegree: number): number {
  return Math.min(BASE_R + inDegree * DEG_SCALE, MAX_R)
}

// ── Directory colour palette ──────────────────────────────────────────────────

/** Hue values (HSL) for the first N directory buckets. */
const DIR_HUES = [220, 160, 45, 320, 10, 270, 200, 80] as const

/**
 * Return a CSS HSL colour string for a directory path.
 * Uses a stable hash → hue assignment so the same dir always maps to the same colour.
 * @param dir  Directory path string (or empty for root-level notes).
 */
export function dirColour(dir: string): string {
  let hash = 0
  for (let i = 0; i < dir.length; i++) {
    hash = (hash * 31 + dir.charCodeAt(i)) >>> 0
  }
  const hue = DIR_HUES[hash % DIR_HUES.length]
  return `hsl(${hue}, 65%, 55%)`
}

// ── Tree flattening ───────────────────────────────────────────────────────────

/**
 * Flatten a VaultNode tree into an array of leaf file paths.
 * Only `.md` files (isDir = false) are included.
 * @param node  Root VaultNode.
 */
export function flattenLeaves(node: VaultNode): string[] {
  if (!node.isDir) return [node.path]
  const results: string[] = []
  for (const child of node.children) {
    results.push(...flattenLeaves(child))
  }
  return results
}

// ── Name-to-path resolution ───────────────────────────────────────────────────

/**
 * Build a lookup map: note short-name (stem, no path, no `.md`) → absolute path.
 * When two notes share the same stem, the LAST one wins (ambiguous — acceptable
 * for the graph; full-path resolution is a P4 enhancement).
 * @param paths  Flat list of absolute `.md` file paths.
 */
export function buildNameIndex(paths: string[]): Map<string, string> {
  const map = new Map<string, string>()
  for (const p of paths) {
    // Extract the filename stem: last segment, strip `.md`
    const seg = p.split('/').pop() ?? p
    const stem = seg.endsWith('.md') ? seg.slice(0, -3) : seg
    map.set(stem, p)
    // Also index by full path for exact matches in content
    map.set(p, p)
  }
  return map
}

// ── Graph builder ─────────────────────────────────────────────────────────────

/**
 * Injectable edge-source seam.
 * When T-P3-E06-06 (linkIndex service) is available, callers may pass a
 * `linkIndex: Map<string, string[]>` (source path → array of target paths)
 * as `edgesSource`. Falls back to parsing the provided `contentMap`.
 */
export interface GraphDataInput {
  /** Flat list of all .md file paths in the vault. */
  paths: string[]
  /**
   * Map from absolute path → full markdown text, used to extract wikilinks
   * when `edgesSource` is not provided.
   * Partial coverage is fine — missing entries are skipped.
   */
  contentMap?: Map<string, string>
  /**
   * Pre-built link index: source path → array of target paths (already resolved).
   * When supplied, `contentMap` is ignored.
   * Seam for T-P3-E06-06 linkIndex integration.
   */
  edgesSource?: Map<string, string[]>
}

/**
 * Build GraphNode[] and GraphEdge[] from vault paths + content or link index.
 *
 * Algorithm:
 *   1. Collect all leaf .md paths.
 *   2. Build name-to-path index.
 *   3. For each file, extract [[wikilinks]] from content (or use edgesSource).
 *   4. Resolve wikilink note-names to absolute paths via the name index.
 *   5. Count in-degrees; emit edges only between known nodes.
 *   6. Return nodes + edges.
 *
 * @param input  See GraphDataInput.
 * @returns      { nodes, edges } ready for D3 force simulation.
 */
export function buildGraphData(input: GraphDataInput): {
  nodes: GraphNode[]
  edges: GraphEdge[]
} {
  const { paths, contentMap, edgesSource } = input

  if (paths.length === 0) return { nodes: [], edges: [] }

  // Index: path → name for display labels
  const nameIndex = buildNameIndex(paths)
  // Set for O(1) membership checks
  const pathSet = new Set(paths)

  // Initialise nodes
  const nodeMap = new Map<string, GraphNode>()
  for (const p of paths) {
    const seg = p.split('/').pop() ?? p
    const label = seg.endsWith('.md') ? seg.slice(0, -3) : seg
    const dir = p.includes('/') ? p.slice(0, p.lastIndexOf('/')) : ''
    nodeMap.set(p, { id: p, label, inDegree: 0, dir })
  }

  // Build raw edge list (source → target, both as absolute paths)
  const rawEdges: Array<{ source: string; target: string }> = []

  if (edgesSource) {
    // Fast path: use pre-resolved link index
    for (const [src, targets] of edgesSource.entries()) {
      if (!pathSet.has(src)) continue
      for (const tgt of targets) {
        if (pathSet.has(tgt)) {
          rawEdges.push({ source: src, target: tgt })
        }
      }
    }
  } else if (contentMap) {
    // Parse path: extract [[wikilinks]] from content text
    for (const [src, text] of contentMap.entries()) {
      if (!pathSet.has(src)) continue
      const links = findWikilinks(text)
      for (const link of links) {
        // Try exact path first, then stem lookup
        const tgt = pathSet.has(link.noteName)
          ? link.noteName
          : nameIndex.get(link.noteName)
        if (tgt && pathSet.has(tgt) && tgt !== src) {
          rawEdges.push({ source: src, target: tgt })
        }
      }
    }
  }

  // Deduplicate edges (same source→target pair) and update in-degrees
  const edgeSet = new Set<string>()
  const edges: GraphEdge[] = []
  for (const { source, target } of rawEdges) {
    const key = `${source}\x00${target}`
    if (edgeSet.has(key)) continue
    edgeSet.add(key)
    edges.push({ source, target })
    const tgtNode = nodeMap.get(target)
    if (tgtNode) tgtNode.inDegree++
  }

  return { nodes: Array.from(nodeMap.values()), edges }
}

/**
 * Build GraphNode[] + GraphEdge[] from a VaultNode tree + content map.
 * Convenience wrapper over buildGraphData for callers that hold a VaultNode tree.
 * @param root        Root VaultNode from VaultContext.treeData.
 * @param contentMap  Map<absolutePath, markdownText> for wikilink parsing.
 * @param edgesSource Optional pre-built link index (seam for T-P3-E06-06).
 */
export function buildGraphDataFromTree(
  root: VaultNode,
  contentMap?: Map<string, string>,
  edgesSource?: Map<string, string[]>,
): { nodes: GraphNode[]; edges: GraphEdge[] } {
  const paths = flattenLeaves(root)
  return buildGraphData({ paths, contentMap, edgesSource })
}
