/**
 * Purpose: Pure data-transform helpers converting IPC graph types to ReactFlow
 *   nodes/edges. Extracted for testability — no React or ReactFlow import.
 * Inputs:  GraphData (from get_project_graph or get_pews_dag), TierEntry[].
 * Outputs: ReactFlow-compatible node/edge arrays and layout positions.
 * Constraints:
 *   - No side-effects; deterministic given the same input.
 *   - Status colour mapping for PEWS ticket nodes.
 *   - Hierarchical X/Y position from depth (project→repo→app = depth 0/1/2).
 * SPORT: MASTER-COMPONENTS.md — mapTransforms (T-P3-E07-13)
 */

import type { GraphData, GraphNode, TierEntry } from '../../types/maps'
import type { Node, Edge } from '@xyflow/react'

// ── Colour constants ──────────────────────────────────────────────────────────

/** Node background colours per node type (project graph). */
export const PROJECT_NODE_COLORS: Record<string, string> = {
  project: '#3b82f6', // blue-500
  repo: '#a855f7', // purple-500
  app: '#22c55e', // green-500
}

/** Node background colours per PEWS ticket status. */
export const STATUS_COLORS: Record<string, string> = {
  pending: '#6b7280', // gray-500
  'in-progress': '#3b82f6', // blue-500
  done: '#22c55e', // green-500
  blocked: '#ef4444', // red-500
}

// ── Project graph transform ───────────────────────────────────────────────────

/**
 * Convert a project-graph GraphData to ReactFlow nodes with hierarchical layout.
 * Depth is inferred from node type: project=0, repo=1, app=2.
 *
 * @param data GraphData from get_project_graph
 * @returns { nodes, edges } for ReactFlow
 */
export function projectGraphToFlow(data: GraphData): {
  nodes: Node[]
  edges: Edge[]
} {
  // Group nodes by type to assign Y positions
  const byType: Record<string, GraphNode[]> = {}
  for (const n of data.nodes) {
    const list = byType[n.nodeType] ?? []
    list.push(n)
    byType[n.nodeType] = list
  }

  const typeOrder = ['project', 'repo', 'app']
  const NODE_WIDTH = 160
  const NODE_HEIGHT = 40
  const COL_GAP = 220
  const ROW_GAP = 60

  // Assign positions: column = depth, row = index in type group
  const positionMap = new Map<string, { x: number; y: number }>()
  for (const type of typeOrder) {
    const nodes = byType[type] ?? []
    const col = typeOrder.indexOf(type)
    nodes.forEach((n, i) => {
      positionMap.set(n.id, {
        x: col * COL_GAP,
        y: i * ROW_GAP,
      })
    })
  }

  const nodes: Node[] = data.nodes.map((n) => ({
    id: n.id,
    type: 'projectNode',
    position: positionMap.get(n.id) ?? { x: 0, y: 0 },
    data: {
      label: n.label,
      nodeType: n.nodeType,
      path: n.path,
      color: PROJECT_NODE_COLORS[n.nodeType] ?? '#6b7280',
    },
    style: {
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
    },
  }))

  const edges: Edge[] = data.edges.map((e) => ({
    id: `${e.from}->${e.to}`,
    source: e.from,
    target: e.to,
    type: 'smoothstep',
    style: { stroke: '#64748b', strokeWidth: 1.5 },
  }))

  return { nodes, edges }
}

// ── PEWS DAG transform ────────────────────────────────────────────────────────

/**
 * Convert a PEWS DAG GraphData to ReactFlow nodes with a left-to-right layout.
 * Uses a simple topological-sort-based column assignment (dagre not required).
 * Status is read from node.meta.status.
 *
 * @param data GraphData from get_pews_dag
 * @returns { nodes, edges } for ReactFlow
 */
export function pewsDagToFlow(data: GraphData): {
  nodes: Node[]
  edges: Edge[]
} {
  const NODE_WIDTH = 180
  const NODE_HEIGHT = 50
  const COL_GAP = 220
  const ROW_GAP = 70

  // Build adjacency and in-degree for topological column assignment.
  const inDegree = new Map<string, number>()
  const adj = new Map<string, string[]>()
  for (const n of data.nodes) {
    inDegree.set(n.id, 0)
    adj.set(n.id, [])
  }
  for (const e of data.edges) {
    adj.get(e.from)?.push(e.to)
    inDegree.set(e.to, (inDegree.get(e.to) ?? 0) + 1)
  }

  // BFS for column assignment (longest path from root).
  const col = new Map<string, number>()
  const queue: string[] = []
  for (const [id, deg] of inDegree) {
    if (deg === 0) {
      queue.push(id)
      col.set(id, 0)
    }
  }

  const visited = new Set<string>()
  while (queue.length > 0) {
    const id = queue.shift()!
    if (visited.has(id)) continue
    visited.add(id)
    const c = col.get(id) ?? 0
    for (const next of adj.get(id) ?? []) {
      const nextCol = Math.max(col.get(next) ?? 0, c + 1)
      col.set(next, nextCol)
      if (!visited.has(next)) queue.push(next)
    }
  }

  // Group by column for row assignment.
  const byCol = new Map<number, GraphNode[]>()
  for (const n of data.nodes) {
    const c = col.get(n.id) ?? 0
    const list = byCol.get(c) ?? []
    list.push(n)
    byCol.set(c, list)
  }

  const positionMap = new Map<string, { x: number; y: number }>()
  for (const [c, nodes] of byCol) {
    nodes.forEach((n, i) => {
      positionMap.set(n.id, { x: c * COL_GAP, y: i * ROW_GAP })
    })
  }

  const nodes: Node[] = data.nodes.map((n) => {
    const status = (n.meta?.status as string | undefined) ?? 'pending'
    const truncatedLabel = n.label.length > 24 ? n.label.slice(0, 22) + '…' : n.label
    return {
      id: n.id,
      type: 'ticketNode',
      position: positionMap.get(n.id) ?? { x: 0, y: 0 },
      data: {
        label: truncatedLabel,
        ticketId: n.id,
        status,
        color: STATUS_COLORS[status] ?? STATUS_COLORS.pending,
      },
      style: {
        width: NODE_WIDTH,
        height: NODE_HEIGHT,
      },
    }
  })

  const edges: Edge[] = data.edges.map((e) => ({
    id: `${e.from}->${e.to}`,
    source: e.from,
    target: e.to,
    type: 'smoothstep',
    markerEnd: { type: 'arrowclosed' as const },
    style: { stroke: '#64748b', strokeWidth: 1.5 },
  }))

  return { nodes, edges }
}

// ── Tier tree helpers ─────────────────────────────────────────────────────────

/**
 * Returns true when this tier entry represents an existing cascade file.
 * Extracted for simple unit-testability.
 */
export function tierExists(entry: TierEntry): boolean {
  return entry.exists
}

/**
 * Returns the display label for a tier entry.
 * Format: "GCI — Global Cascade Instructions"
 */
export function tierLabel(entry: TierEntry): string {
  return `${entry.tier} — ${entry.name}`
}
