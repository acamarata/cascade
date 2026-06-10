/**
 * Purpose: Card for a single library item — name, description, tag chips, kind badge,
 *   delete button, edit seam, and an Inject dropdown (CC / OC / Codex targets).
 * Inputs:  LibraryItem, onEdit callback (seam), onDelete callback, optional isSelected.
 * Outputs: Keyboard-navigable card with role=article.
 * Constraints:
 *   - Description truncated to 2 lines (line-clamp-2).
 *   - Favourite toggle is a seam; state lives in settings IPC (extended separately).
 *   - onEdit/onCreate props are optional seams — T-P3-E07-06 will connect them.
 *   - No inline Card import (component uses custom styling matching TemplateCard pattern).
 *   - Inject dropdown calls inject_library_item IPC (T-P3-E07-07).
 * SPORT: MASTER-COMPONENTS.md — ItemCard (T-P3-E07-04/07)
 */

import React, { useState } from 'react'
import { Trash2, Pencil, Download } from 'lucide-react'
import { invoke } from '@tauri-apps/api/core'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '../../components/ui/dropdown-menu'
import type { LibraryItem, LibraryItemKind } from '../../types/library'

// ── Kind badge styling ─────────────────────────────────────────────────────────

const KIND_COLORS: Record<LibraryItemKind, string> = {
  prompt: 'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300',
  agent: 'bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300',
  persona: 'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300',
  'slash-command': 'bg-orange-100 text-orange-800 dark:bg-orange-900/40 dark:text-orange-300',
}

const KIND_LABELS: Record<LibraryItemKind, string> = {
  prompt: 'Prompt',
  agent: 'Agent',
  persona: 'Persona',
  'slash-command': 'Command',
}

// ── Harness targets ────────────────────────────────────────────────────────────

const HARNESS_TARGETS = [
  { value: 'cc', label: 'Claude Code' },
  { value: 'oc', label: 'OpenCode' },
  { value: 'codex', label: 'Codex' },
] as const

type HarnessTarget = (typeof HARNESS_TARGETS)[number]['value']

// ── InjectResult mirrors the Rust InjectResult struct ─────────────────────────

interface InjectResult {
  injected: boolean
  alreadyPresent: boolean
  wouldInject: boolean
}

// ── Props ─────────────────────────────────────────────────────────────────────

interface ItemCardProps {
  item: LibraryItem
  /** Called when the card is clicked (opens detail view). */
  onClick: () => void
  /** Seam: called by T-P3-E07-06 author form trigger. */
  onEdit?: (item: LibraryItem) => void
  /** Called after user confirms delete. */
  onDelete: (item: LibraryItem) => void
  isSelected?: boolean
}

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * Library item card.
 */
export function ItemCard({
  item,
  onClick,
  onEdit,
  onDelete,
  isSelected = false,
}: ItemCardProps): React.ReactElement {
  const kindColor = KIND_COLORS[item.kind]
  const kindLabel = KIND_LABELS[item.kind]

  /** Brief status message shown after an inject attempt; clears after 3 s. */
  const [injectStatus, setInjectStatus] = useState<string | null>(null)

  function showStatus(msg: string) {
    setInjectStatus(msg)
    setTimeout(() => setInjectStatus(null), 3000)
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      onClick()
    }
  }

  function handleDeleteClick(e: React.MouseEvent) {
    e.stopPropagation()
    onDelete(item)
  }

  function handleEditClick(e: React.MouseEvent) {
    e.stopPropagation()
    onEdit?.(item)
  }

  async function handleInject(harness: HarnessTarget) {
    try {
      const result = await invoke<InjectResult>('inject_library_item', {
        id: item.id,
        itemType: item.kind,
        harness,
        dryRun: false,
      })
      const harnessLabel = HARNESS_TARGETS.find((h) => h.value === harness)?.label ?? harness
      if (result.alreadyPresent) {
        showStatus(`Already injected into ${harnessLabel}`)
      } else {
        showStatus(`${item.title} injected into ${harnessLabel}`)
      }
    } catch (err) {
      showStatus(`Inject failed: ${String(err)}`)
    }
  }

  return (
    // role="option" supports aria-selected and interactive usage (tabIndex, click, keyDown)
    <div
      role="option"
      tabIndex={0}
      aria-selected={isSelected}
      aria-label={`Library item: ${item.title}`}
      onClick={onClick}
      onKeyDown={handleKeyDown}
      className={[
        'group cursor-pointer rounded-lg border p-3 transition-colors focus:outline-none focus:ring-2 focus:ring-ring',
        isSelected
          ? 'border-primary bg-primary/5'
          : 'border-border bg-card hover:border-primary/40 hover:bg-accent/20',
      ].join(' ')}
    >
      {/* Header row: name + kind badge + actions */}
      <div className="mb-1.5 flex items-start justify-between gap-2">
        <h3 className="truncate text-sm font-medium leading-tight text-foreground">{item.title}</h3>

        <div className="flex shrink-0 items-center gap-1">
          {/* Kind badge */}
          <span
            className={[
              'inline-flex items-center rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide',
              kindColor,
            ].join(' ')}
            aria-label={`Kind: ${kindLabel}`}
          >
            {kindLabel}
          </span>

          {/* Inject dropdown */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label={`Inject ${item.title} into harness`}
                onClick={(e) => e.stopPropagation()}
                className="rounded p-0.5 text-muted-foreground opacity-0 transition-opacity hover:text-foreground group-hover:opacity-100 focus:opacity-100 focus:outline-none focus:ring-1 focus:ring-ring"
              >
                <Download className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" onClick={(e) => e.stopPropagation()}>
              {HARNESS_TARGETS.map((h) => (
                <DropdownMenuItem key={h.value} onSelect={() => handleInject(h.value)}>
                  Inject into {h.label}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>

          {/* Edit seam — rendered only when onEdit is connected */}
          {onEdit && (
            <button
              type="button"
              aria-label={`Edit ${item.title}`}
              onClick={handleEditClick}
              className="rounded p-0.5 text-muted-foreground opacity-0 transition-opacity hover:text-foreground group-hover:opacity-100 focus:opacity-100 focus:outline-none focus:ring-1 focus:ring-ring"
            >
              <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
            </button>
          )}

          {/* Delete */}
          <button
            type="button"
            aria-label={`Delete ${item.title}`}
            onClick={handleDeleteClick}
            className="rounded p-0.5 text-muted-foreground opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100 focus:opacity-100 focus:outline-none focus:ring-1 focus:ring-ring"
          >
            <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </div>
      </div>

      {/* Description (2-line clamp) */}
      {item.description && (
        <p className="line-clamp-2 text-xs text-muted-foreground">{item.description}</p>
      )}

      {/* Tag chips */}
      {item.tags.length > 0 && (
        <div className="mt-2 flex flex-wrap gap-1" role="list" aria-label="Tags">
          {item.tags.map((tag) => (
            <span
              key={tag}
              role="listitem"
              className="inline-flex items-center rounded bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground"
            >
              {tag}
            </span>
          ))}
        </div>
      )}

      {/* Inject status notification (brief inline feedback) */}
      {injectStatus !== null && (
        <p role="status" aria-live="polite" className="mt-1.5 text-[10px] text-muted-foreground">
          {injectStatus}
        </p>
      )}
    </div>
  )
}
