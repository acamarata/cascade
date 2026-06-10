/**
 * Purpose: Vitest unit tests for pure vault keyboard-navigation helpers.
 *   All tests are DOM-free — pure function assertions.
 * SPORT: MASTER-COMPONENTS.md — vaultNav helpers (T-P3-E06-02)
 */

import { describe, expect, it } from 'vitest'
import type { VaultNode } from '../../types/vault'
import {
  flattenVisible,
  handleTreeKeyDown,
  moveFocusDown,
  moveFocusUp,
  fileCount,
  totalFileCount,
  type FlatNode,
} from './vaultNav'

// ── Fixtures ─────────────────────────────────────────────────────────────────

const LEAF_A: VaultNode = { name: 'a.md', path: '/root/GCI/a.md', isDir: false, children: [] }
const LEAF_B: VaultNode = { name: 'b.md', path: '/root/GCI/b.md', isDir: false, children: [] }
const LEAF_C: VaultNode = { name: 'c.md', path: '/root/PCI/c.md', isDir: false, children: [] }

const DIR_GCI: VaultNode = {
  name: 'GCI',
  path: '/root/GCI',
  isDir: true,
  children: [LEAF_A, LEAF_B],
}
const DIR_PCI: VaultNode = {
  name: 'PCI',
  path: '/root/PCI',
  isDir: true,
  children: [LEAF_C],
}
const ROOT: VaultNode = {
  name: '.cascade',
  path: '/root',
  isDir: true,
  children: [DIR_GCI, DIR_PCI],
}

// ── flattenVisible ────────────────────────────────────────────────────────────

describe('flattenVisible', () => {
  it('returns only root when nothing is expanded', () => {
    const result = flattenVisible(ROOT, new Set())
    expect(result).toHaveLength(1)
    expect(result[0].node.path).toBe('/root')
    expect(result[0].depth).toBe(0)
  })

  it('expands root one level', () => {
    const result = flattenVisible(ROOT, new Set(['/root']))
    expect(result).toHaveLength(3) // root + GCI + PCI
    expect(result[1].node.path).toBe('/root/GCI')
    expect(result[1].depth).toBe(1)
    expect(result[2].node.path).toBe('/root/PCI')
  })

  it('expands two levels when both parent dirs are open', () => {
    const result = flattenVisible(ROOT, new Set(['/root', '/root/GCI']))
    // root + GCI + a.md + b.md + PCI = 5
    expect(result).toHaveLength(5)
    expect(result[2].node.path).toBe('/root/GCI/a.md')
    expect(result[2].depth).toBe(2)
    expect(result[3].node.path).toBe('/root/GCI/b.md')
  })

  it('does not expand collapsed dirs even when children exist', () => {
    const result = flattenVisible(ROOT, new Set(['/root', '/root/PCI']))
    // root + GCI (collapsed) + PCI + c.md = 4
    expect(result).toHaveLength(4)
    expect(result[1].node.path).toBe('/root/GCI') // GCI still present but not expanded
    expect(result[3].node.path).toBe('/root/PCI/c.md')
  })
})

// ── moveFocusUp / moveFocusDown ───────────────────────────────────────────────

describe('moveFocusUp', () => {
  it('decrements focus', () => expect(moveFocusUp(3)).toBe(2))
  it('clamps at 0', () => expect(moveFocusUp(0)).toBe(0))
})

describe('moveFocusDown', () => {
  it('increments focus', () => expect(moveFocusDown(2, 5)).toBe(3))
  it('clamps at visibleCount - 1', () => expect(moveFocusDown(4, 5)).toBe(4))
  it('handles visibleCount 0 gracefully', () => expect(moveFocusDown(0, 0)).toBe(0))
})

// ── handleTreeKeyDown ─────────────────────────────────────────────────────────

function makeVisible(expanded: Set<string>): FlatNode[] {
  return flattenVisible(ROOT, expanded)
}

describe('handleTreeKeyDown', () => {
  it('ArrowDown moves focus forward', () => {
    const visible = makeVisible(new Set(['/root']))
    const result = handleTreeKeyDown('ArrowDown', 0, visible, new Set(['/root']))
    expect(result.nextFocus).toBe(1)
  })

  it('ArrowUp moves focus back', () => {
    const visible = makeVisible(new Set(['/root']))
    const result = handleTreeKeyDown('ArrowUp', 2, visible, new Set(['/root']))
    expect(result.nextFocus).toBe(1)
  })

  it('ArrowRight expands a collapsed dir', () => {
    const expanded = new Set(['/root'])
    const visible = makeVisible(expanded)
    // index 1 = GCI dir (collapsed)
    const result = handleTreeKeyDown('ArrowRight', 1, visible, expanded)
    expect(result.expand).toBe('/root/GCI')
  })

  it('ArrowRight on already-expanded dir moves focus into first child', () => {
    const expanded = new Set(['/root', '/root/GCI'])
    const visible = makeVisible(expanded)
    // index 1 = GCI (expanded)
    const result = handleTreeKeyDown('ArrowRight', 1, visible, expanded)
    expect(result.expand).toBeNull()
    expect(result.nextFocus).toBe(2)
  })

  it('ArrowLeft collapses an expanded dir', () => {
    const expanded = new Set(['/root', '/root/GCI'])
    const visible = makeVisible(expanded)
    // index 1 = GCI (expanded)
    const result = handleTreeKeyDown('ArrowLeft', 1, visible, expanded)
    expect(result.collapse).toBe('/root/GCI')
  })

  it('ArrowLeft on a collapsed dir moves focus to parent', () => {
    const expanded = new Set(['/root'])
    const visible = makeVisible(expanded)
    // index 1 = GCI (collapsed)
    const result = handleTreeKeyDown('ArrowLeft', 1, visible, expanded)
    expect(result.nextFocus).toBe(0) // root
    expect(result.collapse).toBeNull()
  })

  it('Enter opens a file node', () => {
    const expanded = new Set(['/root', '/root/GCI'])
    const visible = makeVisible(expanded)
    // index 2 = LEAF_A
    const result = handleTreeKeyDown('Enter', 2, visible, expanded)
    expect(result.open).toBe('/root/GCI/a.md')
    expect(result.toggle).toBeNull()
  })

  it('Enter toggles a dir node', () => {
    const expanded = new Set(['/root'])
    const visible = makeVisible(expanded)
    // index 1 = GCI dir
    const result = handleTreeKeyDown('Enter', 1, visible, expanded)
    expect(result.toggle).toBe('/root/GCI')
    expect(result.open).toBeNull()
  })

  it('Space opens a file node', () => {
    const expanded = new Set(['/root', '/root/GCI'])
    const visible = makeVisible(expanded)
    // index 2 = LEAF_A
    const result = handleTreeKeyDown(' ', 2, visible, expanded)
    expect(result.open).toBe('/root/GCI/a.md')
  })

  it('Space toggles a dir node', () => {
    const expanded = new Set(['/root'])
    const visible = makeVisible(expanded)
    // index 1 = GCI dir
    const result = handleTreeKeyDown(' ', 1, visible, expanded)
    expect(result.toggle).toBe('/root/GCI')
  })

  it('unrecognised key returns unchanged state', () => {
    const expanded = new Set(['/root'])
    const visible = makeVisible(expanded)
    const result = handleTreeKeyDown('Tab', 1, visible, expanded)
    expect(result.nextFocus).toBe(1)
    expect(result.expand).toBeNull()
    expect(result.open).toBeNull()
  })

  it('handles empty visible list without throwing', () => {
    const result = handleTreeKeyDown('ArrowDown', 0, [], new Set())
    expect(result.nextFocus).toBe(0)
  })
})

// ── fileCount / totalFileCount ────────────────────────────────────────────────

describe('fileCount', () => {
  it('returns immediate file child count', () => {
    expect(fileCount(DIR_GCI)).toBe(2) // a.md, b.md
    expect(fileCount(DIR_PCI)).toBe(1) // c.md
  })

  it('returns 0 for a leaf node', () => {
    expect(fileCount(LEAF_A)).toBe(0)
  })

  it('returns 0 for empty dir', () => {
    const emptyDir: VaultNode = { name: 'APC', path: '/root/APC', isDir: true, children: [] }
    expect(fileCount(emptyDir)).toBe(0)
  })
})

describe('totalFileCount', () => {
  it('recursively counts all file descendants', () => {
    // ROOT has GCI (2 files) + PCI (1 file) = 3
    expect(totalFileCount(ROOT)).toBe(3)
  })

  it('returns 0 for leaf', () => {
    expect(totalFileCount(LEAF_A)).toBe(0)
  })

  it('returns 0 for empty dir', () => {
    const emptyDir: VaultNode = { name: 'APC', path: '/root/APC', isDir: true, children: [] }
    expect(totalFileCount(emptyDir)).toBe(0)
  })
})
