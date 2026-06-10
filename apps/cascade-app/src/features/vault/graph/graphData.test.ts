/**
 * Purpose: Unit tests for graphData pure helpers.
 *   Covers buildGraphData, nodeRadius, dirColour, flattenLeaves, buildNameIndex.
 *   Pure logic only — no DOM, no React, no D3.
 * SPORT: MASTER-COMPONENTS.md — graphData tests (T-P3-E06-07)
 */

import { describe, expect, it } from 'vitest'
import {
  BASE_R,
  DEG_SCALE,
  MAX_R,
  buildGraphData,
  buildGraphDataFromTree,
  buildNameIndex,
  dirColour,
  flattenLeaves,
  nodeRadius,
} from './graphData'
import type { VaultNode } from '../../../types/vault'

// ── nodeRadius ────────────────────────────────────────────────────────────────

describe('nodeRadius', () => {
  it('returns BASE_R for inDegree=0', () => {
    expect(nodeRadius(0)).toBe(BASE_R)
  })

  it('scales linearly with inDegree', () => {
    expect(nodeRadius(1)).toBe(BASE_R + 1 * DEG_SCALE)
    expect(nodeRadius(3)).toBe(BASE_R + 3 * DEG_SCALE)
  })

  it('clamps at MAX_R', () => {
    expect(nodeRadius(1000)).toBe(MAX_R)
  })

  it('clamps at MAX_R for the exact boundary', () => {
    const boundary = Math.ceil((MAX_R - BASE_R) / DEG_SCALE)
    expect(nodeRadius(boundary)).toBeLessThanOrEqual(MAX_R)
  })
})

// ── dirColour ─────────────────────────────────────────────────────────────────

describe('dirColour', () => {
  it('returns a CSS hsl() string', () => {
    const c = dirColour('/vault/GCI')
    expect(c).toMatch(/^hsl\(\d+, \d+%, \d+%\)$/)
  })

  it('is stable — same dir always returns same colour', () => {
    const dir = '/vault/PCI/sub'
    expect(dirColour(dir)).toBe(dirColour(dir))
  })

  it('returns different colours for different dirs (statistical)', () => {
    const dirs = ['/a', '/b', '/c', '/d', '/e', '/f', '/g', '/h', '/i', '/j']
    const colours = dirs.map(dirColour)
    // At least 2 distinct colours among 10 inputs
    const unique = new Set(colours)
    expect(unique.size).toBeGreaterThan(1)
  })

  it('handles empty dir (root-level notes)', () => {
    expect(() => dirColour('')).not.toThrow()
  })
})

// ── flattenLeaves ─────────────────────────────────────────────────────────────

describe('flattenLeaves', () => {
  const tree: VaultNode = {
    name: 'root',
    path: '/vault',
    isDir: true,
    children: [
      {
        name: 'GCI',
        path: '/vault/GCI',
        isDir: true,
        children: [
          { name: 'CLAUDE.md', path: '/vault/GCI/CLAUDE.md', isDir: false, children: [] },
          { name: 'rules.md', path: '/vault/GCI/rules.md', isDir: false, children: [] },
        ],
      },
      { name: 'note.md', path: '/vault/note.md', isDir: false, children: [] },
      {
        name: 'empty-dir',
        path: '/vault/empty-dir',
        isDir: true,
        children: [],
      },
    ],
  }

  it('returns all leaf .md paths', () => {
    const leaves = flattenLeaves(tree)
    expect(leaves).toHaveLength(3)
    expect(leaves).toContain('/vault/GCI/CLAUDE.md')
    expect(leaves).toContain('/vault/GCI/rules.md')
    expect(leaves).toContain('/vault/note.md')
  })

  it('excludes directory nodes', () => {
    const leaves = flattenLeaves(tree)
    expect(leaves).not.toContain('/vault/GCI')
    expect(leaves).not.toContain('/vault/empty-dir')
  })

  it('returns single leaf for a file node', () => {
    const file: VaultNode = { name: 'a.md', path: '/x/a.md', isDir: false, children: [] }
    expect(flattenLeaves(file)).toEqual(['/x/a.md'])
  })

  it('returns empty array for empty directory', () => {
    const empty: VaultNode = { name: 'd', path: '/d', isDir: true, children: [] }
    expect(flattenLeaves(empty)).toEqual([])
  })
})

// ── buildNameIndex ────────────────────────────────────────────────────────────

describe('buildNameIndex', () => {
  it('maps stem to absolute path', () => {
    const idx = buildNameIndex(['/vault/GCI/CLAUDE.md', '/vault/note.md'])
    expect(idx.get('CLAUDE')).toBe('/vault/GCI/CLAUDE.md')
    expect(idx.get('note')).toBe('/vault/note.md')
  })

  it('also maps exact path to itself', () => {
    const idx = buildNameIndex(['/vault/GCI/CLAUDE.md'])
    expect(idx.get('/vault/GCI/CLAUDE.md')).toBe('/vault/GCI/CLAUDE.md')
  })
})

// ── buildGraphData ────────────────────────────────────────────────────────────

describe('buildGraphData', () => {
  const paths = ['/vault/A.md', '/vault/B.md', '/vault/C.md']

  it('returns empty arrays for empty paths', () => {
    const { nodes, edges } = buildGraphData({ paths: [] })
    expect(nodes).toHaveLength(0)
    expect(edges).toHaveLength(0)
  })

  it('creates one node per path', () => {
    const { nodes } = buildGraphData({ paths })
    expect(nodes).toHaveLength(3)
    const ids = nodes.map((n) => n.id)
    expect(ids).toContain('/vault/A.md')
    expect(ids).toContain('/vault/B.md')
    expect(ids).toContain('/vault/C.md')
  })

  it('all nodes start with inDegree=0 when no content/edges', () => {
    const { nodes } = buildGraphData({ paths })
    expect(nodes.every((n) => n.inDegree === 0)).toBe(true)
  })

  it('uses note label = filename stem', () => {
    const { nodes } = buildGraphData({ paths })
    const a = nodes.find((n) => n.id === '/vault/A.md')
    expect(a?.label).toBe('A')
  })

  it('extracts edges from contentMap [[wikilinks]]', () => {
    const contentMap = new Map([
      ['/vault/A.md', 'Hello [[B]] world'],
      ['/vault/B.md', ''],
      ['/vault/C.md', '[[A]] and [[B]]'],
    ])
    const { nodes, edges } = buildGraphData({ paths, contentMap })
    expect(edges).toHaveLength(3) // A→B, C→A, C→B
    const bNode = nodes.find((n) => n.id === '/vault/B.md')
    expect(bNode?.inDegree).toBe(2) // linked by A and C
    const aNode = nodes.find((n) => n.id === '/vault/A.md')
    expect(aNode?.inDegree).toBe(1) // linked by C
  })

  it('deduplicates repeated wikilinks in same file', () => {
    const contentMap = new Map([
      ['/vault/A.md', '[[B]] and [[B]] again'],
      ['/vault/B.md', ''],
      ['/vault/C.md', ''],
    ])
    const { edges } = buildGraphData({ paths, contentMap })
    // A→B should appear only once
    const aToBEdges = edges.filter((e) => {
      const src = typeof e.source === 'string' ? e.source : (e.source as { id: string }).id
      const tgt = typeof e.target === 'string' ? e.target : (e.target as { id: string }).id
      return src === '/vault/A.md' && tgt === '/vault/B.md'
    })
    expect(aToBEdges).toHaveLength(1)
  })

  it('ignores wikilinks to unknown notes', () => {
    const contentMap = new Map([
      ['/vault/A.md', '[[NonExistent]] [[B]]'],
      ['/vault/B.md', ''],
      ['/vault/C.md', ''],
    ])
    const { edges } = buildGraphData({ paths, contentMap })
    expect(edges).toHaveLength(1) // only A→B
  })

  it('uses edgesSource when provided (linkIndex seam)', () => {
    const edgesSource = new Map([['/vault/A.md', ['/vault/B.md', '/vault/C.md']]])
    const { edges, nodes } = buildGraphData({ paths, edgesSource })
    expect(edges).toHaveLength(2)
    const bNode = nodes.find((n) => n.id === '/vault/B.md')
    expect(bNode?.inDegree).toBe(1)
    const cNode = nodes.find((n) => n.id === '/vault/C.md')
    expect(cNode?.inDegree).toBe(1)
  })

  it('handles 500 nodes without error', () => {
    const largePaths = Array.from({ length: 500 }, (_, i) => `/vault/note${i}.md`)
    const contentMap = new Map(
      largePaths.map((p, i) => [
        p,
        // Each note links to the next 3 notes (circular within bounds)
        Array.from({ length: 3 }, (_, j) => `[[note${(i + j + 1) % 500}]]`).join(' '),
      ])
    )
    const start = Date.now()
    const { nodes, edges } = buildGraphData({ paths: largePaths, contentMap })
    const elapsed = Date.now() - start
    expect(nodes).toHaveLength(500)
    expect(edges.length).toBeGreaterThan(0)
    // Pure computation should complete in <500ms for 500 nodes
    expect(elapsed).toBeLessThan(500)
  })
})

// ── buildGraphDataFromTree ────────────────────────────────────────────────────

describe('buildGraphDataFromTree', () => {
  const tree: VaultNode = {
    name: 'root',
    path: '/v',
    isDir: true,
    children: [
      { name: 'A.md', path: '/v/A.md', isDir: false, children: [] },
      { name: 'B.md', path: '/v/B.md', isDir: false, children: [] },
    ],
  }

  it('produces nodes from tree leaves', () => {
    const { nodes } = buildGraphDataFromTree(tree)
    expect(nodes).toHaveLength(2)
  })

  it('produces edges from contentMap', () => {
    const contentMap = new Map([
      ['/v/A.md', '[[B]]'],
      ['/v/B.md', ''],
    ])
    const { edges } = buildGraphDataFromTree(tree, contentMap)
    expect(edges).toHaveLength(1)
  })
})
