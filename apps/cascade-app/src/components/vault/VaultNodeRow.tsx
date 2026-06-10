/**
 * Purpose: Renders a single row in the VaultNavigator tree.
 *   Directories render with a collapse toggle, tier badge, and file-count badge.
 *   Files render as a clickable leaf row.
 * Inputs:  node, depth, isExpanded, isActive, isFocused, onToggle, onOpen.
 * Outputs: Single <div> row with keyboard-accessible role and tabIndex.
 * Constraints: tabIndex managed externally (roving tabindex pattern).
 *   Tier badge colours: GCI=purple, PCI=blue, APC=green, PPC=yellow, PRC=orange, PAC=red.
 * SPORT: MASTER-COMPONENTS.md — VaultNodeRow (T-P3-E06-02)
 */

import React from 'react'
import { ChevronDown, ChevronRight, FileText, Folder, FolderOpen } from 'lucide-react'
import type { VaultNode } from '../../types/vault'

// ── Tier badge colours ────────────────────────────────────────────────────────

const TIER_BADGE: Record<string, string> = {
  GCI: 'bg-purple-500/20 text-purple-300 border border-purple-500/40',
  PCI: 'bg-blue-500/20 text-blue-300 border border-blue-500/40',
  APC: 'bg-green-500/20 text-green-300 border border-green-500/40',
  PPC: 'bg-yellow-500/20 text-yellow-300 border border-yellow-500/40',
  PRC: 'bg-orange-500/20 text-orange-300 border border-orange-500/40',
  PAC: 'bg-red-500/20 text-red-300 border border-red-500/40',
}

/**
 * Returns the tier key if the node name exactly matches a .cascade tier dir,
 * otherwise null.
 */
function tierKey(name: string): string | null {
  return Object.prototype.hasOwnProperty.call(TIER_BADGE, name) ? name : null
}

// ── Component ────────────────────────────────────────────────────────────────

interface VaultNodeRowProps {
  node: VaultNode
  depth: number
  isExpanded: boolean
  isActive: boolean
  isFocused: boolean
  fileCountBadge: number
  onToggle: (path: string) => void
  onOpen: (path: string) => void
  rowRef?: React.Ref<HTMLDivElement>
}

export function VaultNodeRow({
  node,
  depth,
  isExpanded,
  isActive,
  isFocused,
  fileCountBadge,
  onToggle,
  onOpen,
  rowRef,
}: VaultNodeRowProps) {
  const tier = tierKey(node.name)
  const indent = depth * 12

  function handleClick() {
    if (node.isDir) {
      onToggle(node.path)
    } else {
      onOpen(node.path)
    }
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    // Individual row key events are handled by the parent tree keyboard handler.
    // We only capture Enter/Space here to support mouse-less activation when
    // the tree's own keydown handler is not present (e.g. isolated renders).
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      handleClick()
    }
  }

  const isDir = node.isDir

  return (
    <div
      ref={rowRef}
      role={isDir ? 'treeitem' : 'treeitem'}
      aria-expanded={isDir ? isExpanded : undefined}
      aria-selected={isActive}
      tabIndex={isFocused ? 0 : -1}
      onClick={handleClick}
      onKeyDown={handleKeyDown}
      className={[
        'flex items-center gap-1.5 rounded-md px-2 py-1 text-sm cursor-pointer select-none transition-colors',
        'outline-none focus-visible:ring-1 focus-visible:ring-ring',
        isActive
          ? 'bg-accent text-accent-foreground'
          : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
        isFocused && !isActive ? 'ring-1 ring-ring/40' : '',
      ]
        .filter(Boolean)
        .join(' ')}
      style={{ paddingLeft: `${indent + 8}px` }}
    >
      {/* Expand / collapse chevron for dirs */}
      {isDir && (
        <span className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true">
          {isExpanded ? (
            <ChevronDown className="h-3.5 w-3.5" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5" />
          )}
        </span>
      )}

      {/* File/folder icon */}
      <span className="h-4 w-4 shrink-0" aria-hidden="true">
        {isDir ? (
          isExpanded ? (
            <FolderOpen className="h-4 w-4" />
          ) : (
            <Folder className="h-4 w-4" />
          )
        ) : (
          <FileText className="h-4 w-4" />
        )}
      </span>

      {/* Node name */}
      <span className="flex-1 truncate">{node.name}</span>

      {/* Tier badge (dirs that match .cascade tier names) */}
      {tier && (
        <span
          className={`shrink-0 rounded px-1 py-0.5 text-[10px] font-semibold leading-none ${TIER_BADGE[tier]}`}
          aria-label={`Tier: ${tier}`}
        >
          {tier}
        </span>
      )}

      {/* File count badge for dirs with children */}
      {isDir && fileCountBadge > 0 && (
        <span
          className="shrink-0 rounded bg-muted px-1 py-0.5 text-[10px] font-mono leading-none text-muted-foreground"
          aria-label={`${fileCountBadge} files`}
        >
          {fileCountBadge}
        </span>
      )}
    </div>
  )
}
