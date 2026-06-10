/**
 * Purpose: VaultNavigator — collapsible tree panel for the .cascade vault.
 *   Renders dirs and .md leaves from VaultContext.treeData.
 *   Keyboard navigation: ArrowUp/Down/Left/Right, Enter, Space.
 *   Integrates with VaultContext: clicking a file calls setCurrentFile(path).
 *   Sidebar slot integration: mounts in AppLayout sidebar via /vault route.
 * Inputs:  None — reads VaultContext.
 * Outputs: Scrollable tree panel; fires VaultContext.setCurrentFile on leaf click.
 * Constraints: Roving tabindex pattern for a11y.  No drag-drop (T-P3-E06-scope).
 *   Recent files list shows last 5 opened paths (local state).
 * SPORT: MASTER-COMPONENTS.md — VaultNavigator (T-P3-E06-02)
 */

import React, { useCallback, useEffect, useRef, useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { useVault } from '../../context/VaultContext'
import type { VaultNode } from '../../types/vault'
import { VaultNodeRow } from './VaultNodeRow'
import {
  flattenVisible,
  handleTreeKeyDown,
  totalFileCount,
  type FlatNode,
} from './vaultNav'
// ── Constants ────────────────────────────────────────────────────────────────

const MAX_RECENT = 5

// ── Props ─────────────────────────────────────────────────────────────────────

export interface VaultNavigatorProps {
  /**
   * When non-empty, only leaf nodes whose path appears in this set are shown.
   * Directories that contain at least one matching descendant remain visible.
   * Empty set (or undefined) = show all. (T-P3-E06-08)
   */
  tagFilter?: ReadonlySet<string>
}

// ── Tag-filter helpers ────────────────────────────────────────────────────────

/**
 * Recursively prunes a VaultNode tree to include only nodes that pass the
 * allowedPaths filter (and their ancestor dirs).
 */
function pruneTree(node: VaultNode, allowedPaths: ReadonlySet<string>): VaultNode | null {
  if (!node.isDir) {
    return allowedPaths.has(node.path) ? node : null
  }
  const filteredChildren = node.children
    .map((child) => pruneTree(child, allowedPaths))
    .filter((child): child is VaultNode => child !== null)
  if (filteredChildren.length === 0) return null
  return { ...node, children: filteredChildren }
}

// ── Component ────────────────────────────────────────────────────────────────

export function VaultNavigator({ tagFilter }: VaultNavigatorProps = {}) {
  const { treeData, currentFile, loading, error, refreshTree, setCurrentFile } = useVault()

  // Which dir paths are currently expanded
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(() => new Set())
  // Index of the keyboard-focused node in the flat visible list
  const [focusIndex, setFocusIndex] = useState(0)
  // Recent files (most recent first)
  const [recentFiles, setRecentFiles] = useState<string[]>([])

  // Row refs for focus management
  const rowRefs = useRef<Array<React.RefObject<HTMLDivElement>>>([])

  // ── Auto-expand root on tree load ────────────────────────────────────────

  useEffect(() => {
    if (treeData) {
      setExpandedPaths((prev) => {
        if (prev.has(treeData.path)) return prev
        return new Set([...prev, treeData.path])
      })
    }
  }, [treeData])

  // ── Helpers ──────────────────────────────────────────────────────────────

  const getVisible = useCallback((): FlatNode[] => {
    if (!treeData) return []
    // Apply tag filter: prune tree to only nodes matching allowedPaths.
    const effectiveTree: VaultNode =
      tagFilter && tagFilter.size > 0
        ? (pruneTree(treeData, tagFilter) ?? { ...treeData, children: [] })
        : treeData
    return flattenVisible(effectiveTree, expandedPaths)
  }, [treeData, expandedPaths, tagFilter])

  const toggleExpand = useCallback((path: string) => {
    setExpandedPaths((prev) => {
      const next = new Set(prev)
      if (next.has(path)) {
        next.delete(path)
      } else {
        next.add(path)
      }
      return next
    })
  }, [])

  const openFile = useCallback(
    async (path: string) => {
      await setCurrentFile(path)
      setRecentFiles((prev) => {
        const filtered = prev.filter((p) => p !== path)
        return [path, ...filtered].slice(0, MAX_RECENT)
      })
    },
    [setCurrentFile],
  )

  // ── Keyboard handler ────────────────────────────────────────────────────

  function handleKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    const handled = [
      'ArrowUp',
      'ArrowDown',
      'ArrowLeft',
      'ArrowRight',
      'Enter',
      ' ',
    ].includes(e.key)
    if (!handled) return
    e.preventDefault()

    const visible = getVisible()
    const { nextFocus, expand, collapse, open, toggle } = handleTreeKeyDown(
      e.key,
      focusIndex,
      visible,
      expandedPaths,
    )

    if (expand) {
      setExpandedPaths((prev) => new Set([...prev, expand]))
    }
    if (collapse) {
      setExpandedPaths((prev) => {
        const next = new Set(prev)
        next.delete(collapse)
        return next
      })
    }
    if (toggle) toggleExpand(toggle)
    if (open) void openFile(open)

    setFocusIndex(nextFocus)
  }

  // Focus the DOM element when focusIndex changes
  useEffect(() => {
    const ref = rowRefs.current[focusIndex]
    if (ref?.current) {
      ref.current.focus({ preventScroll: false })
    }
  }, [focusIndex])

  // ── States ────────────────────────────────────────────────────────────────

  if (loading && !treeData) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-2 p-4 text-sm text-muted-foreground">
        <RefreshCw className="h-4 w-4 animate-spin" aria-hidden="true" />
        <span>Loading vault…</span>
      </div>
    )
  }

  if (error) {
    return (
      <div role="alert" className="flex flex-1 flex-col gap-2 p-4 text-sm">
        <p className="font-medium text-destructive">Vault error</p>
        <p className="text-muted-foreground">{error}</p>
        <button
          type="button"
          onClick={() => void refreshTree()}
          className="mt-2 rounded-md border border-border px-3 py-1.5 text-xs hover:bg-accent"
        >
          Retry
        </button>
      </div>
    )
  }

  if (!treeData) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center p-4 text-sm text-muted-foreground">
        No vault found
      </div>
    )
  }

  const visible = getVisible()

  // Build row refs array to match visible length
  while (rowRefs.current.length < visible.length) {
    rowRefs.current.push(React.createRef<HTMLDivElement>())
  }
  rowRefs.current.length = visible.length

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* ── Header ── */}
      <div className="flex h-10 shrink-0 items-center justify-between border-b border-border px-3">
        <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Vault
        </span>
        <button
          type="button"
          onClick={() => void refreshTree()}
          aria-label="Refresh vault tree"
          className="rounded p-1 text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
          disabled={loading}
        >
          <RefreshCw
            className={['h-3.5 w-3.5', loading ? 'animate-spin' : ''].filter(Boolean).join(' ')}
            aria-hidden="true"
          />
        </button>
      </div>

      {/* ── Tree ── */}
      <div
        role="tree"
        aria-label={`Vault: ${treeData.name}`}
        className="flex-1 overflow-y-auto p-1"
        onKeyDown={handleKeyDown}
      >
        {visible.length === 0 ? (
          <div className="p-4 text-center text-sm text-muted-foreground">Empty vault</div>
        ) : (
          visible.map((flat, idx) => (
            <VaultNodeRow
              key={flat.node.path}
              node={flat.node}
              depth={flat.depth}
              isExpanded={flat.node.isDir && expandedPaths.has(flat.node.path)}
              isActive={flat.node.path === currentFile}
              isFocused={idx === focusIndex}
              fileCountBadge={totalFileCount(flat.node)}
              onToggle={toggleExpand}
              onOpen={(path) => void openFile(path)}
              rowRef={rowRefs.current[idx] as React.RefObject<HTMLDivElement>}
            />
          ))
        )}
      </div>

      {/* ── Recent files ── */}
      {recentFiles.length > 0 && (
        <div className="shrink-0 border-t border-border">
          <div className="px-3 py-1.5">
            <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
              Recent
            </span>
          </div>
          <ul className="pb-1">
            {recentFiles.map((path) => {
              const name = path.split('/').pop() ?? path
              return (
                <li key={path}>
                  <button
                    type="button"
                    onClick={() => void openFile(path)}
                    title={path}
                    className={[
                      'w-full truncate px-3 py-1 text-left text-xs transition-colors',
                      path === currentFile
                        ? 'bg-accent text-accent-foreground'
                        : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
                    ].join(' ')}
                  >
                    {name}
                  </button>
                </li>
              )
            })}
          </ul>
        </div>
      )}
    </div>
  )
}

