/**
 * Purpose: Unit tests for mapTransforms — pure data-transform helpers for the
 *   three project-map views. No React/DOM, no Tauri IPC — pure data transforms.
 * Inputs:  Inline fixture GraphData and TierEntry arrays.
 * Outputs: Verified ReactFlow-compatible nodes/edges and tier label/exists helpers.
 * SPORT: MASTER-COMPONENTS.md — mapTransforms tests (T-P3-E07-13)
 */

import { describe, it, expect } from 'vitest'
import {
  projectGraphToFlow,
  pewsDagToFlow,
  tierExists,
  tierLabel,
  PROJECT_NODE_COLORS,
  STATUS_COLORS,
} from './mapTransforms'
import type { GraphData } from '../../types/maps'
import type { TierEntry } from '../../types/maps'

// ── Fixtures ──────────────────────────────────────────────────────────────────

const SIMPLE_PROJECT_GRAPH: GraphData = {
  version: 1,
  nodes: [
    {
      id: 'acamarata',
      label: 'acamarata',
      nodeType: 'project',
      path: '/Users/admin/Sites/acamarata',
    },
    {
      id: 'cascade',
      label: 'cascade',
      nodeType: 'repo',
      path: '/Users/admin/Sites/acamarata/cascade',
    },
    {
      id: 'cascade-app',
      label: 'cascade-app',
      nodeType: 'app',
      path: '/Users/admin/Sites/acamarata/cascade/apps/cascade-app',
    },
  ],
  edges: [
    { from: 'acamarata', to: 'cascade' },
    { from: 'cascade', to: 'cascade-app' },
  ],
}

const PEWS_GRAPH: GraphData = {
  version: 1,
  nodes: [
    {
      id: 'T-01',
      label: 'Foundation setup',
      nodeType: 'ticket',
      path: '',
      meta: { status: 'done' },
    },
    {
      id: 'T-02',
      label: 'Core engine build',
      nodeType: 'ticket',
      path: '',
      meta: { status: 'in-progress' },
    },
    {
      id: 'T-03',
      label: 'Integration layer',
      nodeType: 'ticket',
      path: '',
      meta: { status: 'pending' },
    },
    {
      id: 'T-04',
      label: 'Blocked task',
      nodeType: 'ticket',
      path: '',
      meta: { status: 'blocked' },
    },
  ],
  edges: [
    { from: 'T-01', to: 'T-02' },
    { from: 'T-02', to: 'T-03' },
  ],
}

const SIX_TIERS: TierEntry[] = [
  {
    tier: 'GCI',
    name: 'Global Cascade Instructions',
    path: '/Users/admin/.cascade/CASCADE.md',
    exists: true,
  },
  {
    tier: 'PCI',
    name: 'Platform Cascade Instructions',
    path: '/Users/admin/Sites/.cascade/CASCADE.md',
    exists: false,
  },
  {
    tier: 'APC',
    name: 'Account Cascade Instructions',
    path: '/Users/admin/Sites/acamarata/.cascade/CASCADE.md',
    exists: false,
  },
  {
    tier: 'PPC',
    name: 'Project-parent Cascade Instructions',
    path: '/Users/admin/Sites/acamarata/cascade/.cascade/CASCADE.md',
    exists: true,
  },
  {
    tier: 'PRC',
    name: 'Project Cascade Instructions',
    path: '/Users/admin/Sites/acamarata/cascade/apps/.cascade/CASCADE.md',
    exists: false,
  },
  {
    tier: 'PAC',
    name: 'Per-app Cascade Instructions',
    path: '/Users/admin/Sites/acamarata/cascade/apps/cascade-app/.cascade/CASCADE.md',
    exists: false,
  },
]

// ── projectGraphToFlow ────────────────────────────────────────────────────────

describe('projectGraphToFlow', () => {
  it('produces one ReactFlow node per GraphNode', () => {
    const { nodes } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    expect(nodes).toHaveLength(3)
  })

  it('produces one ReactFlow edge per GraphEdge', () => {
    const { edges } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    expect(edges).toHaveLength(2)
  })

  it('assigns blue colour to project nodes', () => {
    const { nodes } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    const project = nodes.find((n) => n.id === 'acamarata')
    expect(project?.data.color).toBe(PROJECT_NODE_COLORS.project)
    expect(project?.data.color).toBe('#3b82f6')
  })

  it('assigns purple colour to repo nodes', () => {
    const { nodes } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    const repo = nodes.find((n) => n.id === 'cascade')
    expect(repo?.data.color).toBe(PROJECT_NODE_COLORS.repo)
    expect(repo?.data.color).toBe('#a855f7')
  })

  it('assigns green colour to app nodes', () => {
    const { nodes } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    const app = nodes.find((n) => n.id === 'cascade-app')
    expect(app?.data.color).toBe(PROJECT_NODE_COLORS.app)
    expect(app?.data.color).toBe('#22c55e')
  })

  it('places project nodes in column 0 (leftmost)', () => {
    const { nodes } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    const project = nodes.find((n) => n.id === 'acamarata')
    expect(project?.position.x).toBe(0)
  })

  it('places repo nodes in column 1 (middle)', () => {
    const { nodes } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    const repo = nodes.find((n) => n.id === 'cascade')
    expect(repo?.position.x).toBeGreaterThan(0)
  })

  it('preserves the path in node data', () => {
    const { nodes } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    const cascade = nodes.find((n) => n.id === 'cascade')
    expect(cascade?.data.path).toBe('/Users/admin/Sites/acamarata/cascade')
  })

  it('assigns unique edge ids', () => {
    const { edges } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    const ids = edges.map((e) => e.id)
    expect(new Set(ids).size).toBe(ids.length)
  })

  it('returns empty arrays for an empty graph', () => {
    const { nodes, edges } = projectGraphToFlow({ version: 1, nodes: [], edges: [] })
    expect(nodes).toHaveLength(0)
    expect(edges).toHaveLength(0)
  })

  it('uses projectNode type for all nodes', () => {
    const { nodes } = projectGraphToFlow(SIMPLE_PROJECT_GRAPH)
    expect(nodes.every((n) => n.type === 'projectNode')).toBe(true)
  })
})

// ── pewsDagToFlow ─────────────────────────────────────────────────────────────

describe('pewsDagToFlow', () => {
  it('produces one ReactFlow node per ticket', () => {
    const { nodes } = pewsDagToFlow(PEWS_GRAPH)
    expect(nodes).toHaveLength(4)
  })

  it('produces one ReactFlow edge per dependency', () => {
    const { edges } = pewsDagToFlow(PEWS_GRAPH)
    expect(edges).toHaveLength(2)
  })

  it('colours done tickets green', () => {
    const { nodes } = pewsDagToFlow(PEWS_GRAPH)
    const done = nodes.find((n) => n.id === 'T-01')
    expect(done?.data.color).toBe(STATUS_COLORS.done)
    expect(done?.data.color).toBe('#22c55e')
  })

  it('colours in-progress tickets blue', () => {
    const { nodes } = pewsDagToFlow(PEWS_GRAPH)
    const inProgress = nodes.find((n) => n.id === 'T-02')
    expect(inProgress?.data.color).toBe(STATUS_COLORS['in-progress'])
    expect(inProgress?.data.color).toBe('#3b82f6')
  })

  it('colours pending tickets gray', () => {
    const { nodes } = pewsDagToFlow(PEWS_GRAPH)
    const pending = nodes.find((n) => n.id === 'T-03')
    expect(pending?.data.color).toBe(STATUS_COLORS.pending)
    expect(pending?.data.color).toBe('#6b7280')
  })

  it('colours blocked tickets red', () => {
    const { nodes } = pewsDagToFlow(PEWS_GRAPH)
    const blocked = nodes.find((n) => n.id === 'T-04')
    expect(blocked?.data.color).toBe(STATUS_COLORS.blocked)
    expect(blocked?.data.color).toBe('#ef4444')
  })

  it('places root node (no in-edges) in column 0', () => {
    const { nodes } = pewsDagToFlow(PEWS_GRAPH)
    const root = nodes.find((n) => n.id === 'T-01')
    expect(root?.position.x).toBe(0)
  })

  it('places dependent nodes in later columns (LR direction)', () => {
    const { nodes } = pewsDagToFlow(PEWS_GRAPH)
    const t1 = nodes.find((n) => n.id === 'T-01')!
    const t2 = nodes.find((n) => n.id === 'T-02')!
    const t3 = nodes.find((n) => n.id === 'T-03')!
    expect(t2.position.x).toBeGreaterThan(t1.position.x)
    expect(t3.position.x).toBeGreaterThan(t2.position.x)
  })

  it('truncates long labels to ≤24 characters', () => {
    const longLabel = 'A very long ticket title that exceeds 24 chars'
    const graph: GraphData = {
      version: 1,
      nodes: [
        { id: 'T-X', label: longLabel, nodeType: 'ticket', path: '', meta: { status: 'pending' } },
      ],
      edges: [],
    }
    const { nodes } = pewsDagToFlow(graph)
    expect((nodes[0]!.data.label as string).length).toBeLessThanOrEqual(24)
  })

  it('defaults to pending color when status is missing', () => {
    const graph: GraphData = {
      version: 1,
      nodes: [{ id: 'T-X', label: 'No status', nodeType: 'ticket', path: '' }],
      edges: [],
    }
    const { nodes } = pewsDagToFlow(graph)
    expect(nodes[0]!.data.color).toBe(STATUS_COLORS.pending)
  })

  it('uses ticketNode type for all nodes', () => {
    const { nodes } = pewsDagToFlow(PEWS_GRAPH)
    expect(nodes.every((n) => n.type === 'ticketNode')).toBe(true)
  })
})

// ── Tier helpers ──────────────────────────────────────────────────────────────

describe('tierExists', () => {
  it('returns true when exists is true', () => {
    expect(tierExists(SIX_TIERS[0]!)).toBe(true)
  })

  it('returns false when exists is false', () => {
    expect(tierExists(SIX_TIERS[1]!)).toBe(false)
  })

  it('counts the correct number of existing tiers in the fixture', () => {
    const count = SIX_TIERS.filter(tierExists).length
    expect(count).toBe(2) // GCI + PPC exist
  })
})

describe('tierLabel', () => {
  it('formats as TIER — name', () => {
    expect(tierLabel(SIX_TIERS[0]!)).toBe('GCI — Global Cascade Instructions')
  })

  it('works for PAC tier', () => {
    expect(tierLabel(SIX_TIERS[5]!)).toBe('PAC — Per-app Cascade Instructions')
  })
})

// ── Colour constant sanity ────────────────────────────────────────────────────

describe('colour constants', () => {
  it('PROJECT_NODE_COLORS has project / repo / app keys', () => {
    expect(PROJECT_NODE_COLORS).toHaveProperty('project')
    expect(PROJECT_NODE_COLORS).toHaveProperty('repo')
    expect(PROJECT_NODE_COLORS).toHaveProperty('app')
  })

  it('STATUS_COLORS has all four statuses', () => {
    expect(STATUS_COLORS).toHaveProperty('pending')
    expect(STATUS_COLORS).toHaveProperty('in-progress')
    expect(STATUS_COLORS).toHaveProperty('done')
    expect(STATUS_COLORS).toHaveProperty('blocked')
  })
})
