/**
 * Purpose: Pure keyboard-navigation helpers for the VaultNavigator tree.
 *   Builds a flat list of visible nodes and moves focus index by key event.
 *   Keeps all DOM-free so Vitest can test it without jsdom.
 * Inputs:  VaultNode tree + expanded-dir set + current focused index.
 * Outputs: Flat visible node list; index mutation helpers.
 * Constraints: No React, no DOM references. Pure functions only.
 * SPORT: MASTER-COMPONENTS.md — vaultNav helpers (T-P3-E06-02)
 */

import type { VaultNode } from '../../types/vault'

// ── Types ────────────────────────────────────────────────────────────────────

export interface FlatNode {
  node: VaultNode
  depth: number
}

// ── Helpers ──────────────────────────────────────────────────────────────────

/**
 * Flattens the vault tree into an ordered list of visible nodes.
 * Only expands directories whose path is in `expandedPaths`.
 *
 * @param root       Root VaultNode from vault_list.
 * @param expandedPaths  Set of directory paths that are currently open.
 * @param depth      Current recursion depth (start at 0).
 */
export function flattenVisible(
  root: VaultNode,
  expandedPaths: ReadonlySet<string>,
  depth = 0
): FlatNode[] {
  const result: FlatNode[] = [{ node: root, depth }]
  if (root.isDir && expandedPaths.has(root.path)) {
    for (const child of root.children) {
      result.push(...flattenVisible(child, expandedPaths, depth + 1))
    }
  }
  return result
}

/**
 * Returns the new focus index after an ArrowUp key press.
 * Clamps at 0.
 */
export function moveFocusUp(current: number): number {
  return Math.max(0, current - 1)
}

/**
 * Returns the new focus index after an ArrowDown key press.
 * Clamps at visibleCount - 1.
 */
export function moveFocusDown(current: number, visibleCount: number): number {
  if (visibleCount <= 0) return 0
  return Math.min(visibleCount - 1, current + 1)
}

/**
 * Given a key event and the current state, returns:
 *   - nextFocus: new focused index (may be unchanged)
 *   - expand:    path to expand (ArrowRight on a collapsed dir), or null
 *   - collapse:  path to collapse (ArrowLeft on an expanded dir / any node), or null
 *   - open:      path to open (Enter / Space on a file), or null
 *   - toggle:    path to toggle expand (Space on a dir), or null
 *
 * Caller applies the returned mutations to its own state.
 */
export interface NavResult {
  nextFocus: number
  expand: string | null
  collapse: string | null
  open: string | null
  toggle: string | null
}

export function handleTreeKeyDown(
  key: string,
  focusIndex: number,
  visibleNodes: FlatNode[],
  expandedPaths: ReadonlySet<string>
): NavResult {
  const result: NavResult = {
    nextFocus: focusIndex,
    expand: null,
    collapse: null,
    open: null,
    toggle: null,
  }

  const focused = visibleNodes[focusIndex]
  if (!focused) return result

  const { node } = focused

  switch (key) {
    case 'ArrowUp':
      result.nextFocus = moveFocusUp(focusIndex)
      break
    case 'ArrowDown':
      result.nextFocus = moveFocusDown(focusIndex, visibleNodes.length)
      break
    case 'ArrowRight':
      if (node.isDir && !expandedPaths.has(node.path)) {
        result.expand = node.path
      } else if (node.isDir && expandedPaths.has(node.path)) {
        // Move into first child if already expanded
        result.nextFocus = moveFocusDown(focusIndex, visibleNodes.length)
      }
      break
    case 'ArrowLeft':
      if (node.isDir && expandedPaths.has(node.path)) {
        result.collapse = node.path
      } else {
        // Move to parent — find the nearest ancestor in visible list
        const depth = focused.depth
        for (let i = focusIndex - 1; i >= 0; i--) {
          if (visibleNodes[i].depth < depth) {
            result.nextFocus = i
            break
          }
        }
      }
      break
    case 'Enter':
      if (!node.isDir) {
        result.open = node.path
      } else {
        result.toggle = node.path
      }
      break
    case ' ':
      if (node.isDir) {
        result.toggle = node.path
      } else {
        result.open = node.path
      }
      break
  }

  return result
}

/**
 * Returns the count of immediate .md file children in a dir node.
 */
export function fileCount(node: VaultNode): number {
  if (!node.isDir) return 0
  let count = 0
  for (const child of node.children) {
    if (!child.isDir) count++
  }
  return count
}

/**
 * Returns the total recursive .md file count under a dir node.
 */
export function totalFileCount(node: VaultNode): number {
  if (!node.isDir) return 0
  let count = 0
  for (const child of node.children) {
    count += child.isDir ? totalFileCount(child) : 1
  }
  return count
}
