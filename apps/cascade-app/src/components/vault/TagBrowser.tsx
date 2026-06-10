/**
 * Purpose: TagBrowser — sidebar panel that extracts all frontmatter tags from
 *   the vault, displays them as a sortable chip list with note-count badges,
 *   and passes a set of selected tags to VaultNavigator for AND-filtering.
 * Inputs:  selectedTags / onTagsChange props (lifted state from VaultPage).
 *   Reads VaultContext for treeData, setCurrentFile, and vault://changed events.
 * Outputs: Rendered tag chip list; calls onTagsChange when selection changes.
 * Constraints:
 *   - No @radix-ui/react-toggle (not installed); uses custom accessible chips.
 *   - Rebuilds tag index on vault://changed Tauri event.
 *   - Dev fallback: iterates VaultContext.treeData with stub content when
 *     __TAURI__ is absent (no real vault_read calls — shows empty tag list).
 * SPORT: MASTER-COMPONENTS.md — TagBrowser (T-P3-E06-08)
 */

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Tag } from 'lucide-react'
import { invoke } from '@tauri-apps/api/core'
import type { UnlistenFn } from '@tauri-apps/api/event'
import { useVault } from '../../context/VaultContext'
import type { VaultNode, VaultChangedPayload } from '../../types/vault'
import {
  buildTagIndex,
  tagIndexToEntries,
  type TagEntry,
  type VaultFile,
} from '../../services/vault/tagIndex'

// ── Helpers ──────────────────────────────────────────────────────────────────

/** Returns true when running inside the Tauri webview. */
function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

/** Recursively collects all leaf .md file paths from a VaultNode tree. */
function collectLeafPaths(node: VaultNode): string[] {
  if (!node.isDir) return [node.path]
  return node.children.flatMap(collectLeafPaths)
}

/** Reads content for all leaf paths via vault_read IPC in parallel. */
async function readAllFiles(paths: string[]): Promise<VaultFile[]> {
  const results = await Promise.allSettled(
    paths.map((path) =>
      invoke<string>('vault_read', { path }).then((content) => ({ path, content }))
    )
  )
  return results
    .filter((r): r is PromiseFulfilledResult<VaultFile> => r.status === 'fulfilled')
    .map((r) => r.value)
}

// ── Props ─────────────────────────────────────────────────────────────────────

export interface TagBrowserProps {
  /** Currently selected tags (AND filter). Controlled from parent. */
  selectedTags: ReadonlySet<string>
  /** Called when the user toggles a tag chip. */
  onTagsChange: (next: Set<string>) => void
}

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * TagBrowser renders a scrollable list of tag chips, each showing the tag name
 * and the count of notes that carry it. Clicking a chip toggles it in/out of
 * the selectedTags set. The parent (VaultPage) passes selectedTags down to
 * VaultNavigator for AND-filtering the visible file list.
 */
export function TagBrowser({ selectedTags, onTagsChange }: TagBrowserProps) {
  const { treeData } = useVault()
  const [entries, setEntries] = useState<TagEntry[]>([])
  const [loading, setLoading] = useState(false)

  // ── Index builder ──────────────────────────────────────────────────────────

  const buildIndex = useCallback(async (tree: VaultNode | null) => {
    if (!tree) {
      setEntries([])
      return
    }

    const paths = collectLeafPaths(tree)
    if (paths.length === 0) {
      setEntries([])
      return
    }

    setLoading(true)
    try {
      let files: VaultFile[]
      if (isTauri()) {
        files = await readAllFiles(paths)
      } else {
        // Dev/Vitest: no real content available; return empty index.
        files = []
      }
      const index = buildTagIndex(files)
      setEntries(tagIndexToEntries(index))
    } finally {
      setLoading(false)
    }
  }, [])

  // Build on mount / treeData change
  useEffect(() => {
    void buildIndex(treeData)
  }, [treeData, buildIndex])

  // ── vault://changed listener ───────────────────────────────────────────────

  useEffect(() => {
    if (!isTauri()) return

    let unlisten: UnlistenFn | null = null

    async function subscribe() {
      const { listen } = await import('@tauri-apps/api/event')
      unlisten = await listen<VaultChangedPayload>('vault://changed', () => {
        void buildIndex(treeData)
      })
    }

    void subscribe()

    return () => {
      if (unlisten) unlisten()
    }
  }, [treeData, buildIndex])

  // ── Tag toggle ────────────────────────────────────────────────────────────

  const handleToggle = useCallback(
    (tag: string) => {
      const next = new Set(selectedTags)
      if (next.has(tag)) {
        next.delete(tag)
      } else {
        next.add(tag)
      }
      onTagsChange(next)
    },
    [selectedTags, onTagsChange]
  )

  // ── Clear all ─────────────────────────────────────────────────────────────

  const handleClear = useCallback(() => {
    onTagsChange(new Set())
  }, [onTagsChange])

  // ── Derived: sorted entries (already sorted from tagIndexToEntries) ────────
  const sortedEntries = useMemo(() => entries, [entries])

  // ── Render ────────────────────────────────────────────────────────────────

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Header */}
      <div className="flex h-10 shrink-0 items-center justify-between border-b border-border px-3">
        <div className="flex items-center gap-1.5">
          <Tag className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
          <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            Tags
          </span>
          {loading && (
            <span className="text-[10px] text-muted-foreground/60" aria-live="polite">
              …
            </span>
          )}
        </div>
        {selectedTags.size > 0 && (
          <button
            type="button"
            onClick={handleClear}
            className="rounded px-1.5 py-0.5 text-[10px] text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
            aria-label="Clear tag filter"
          >
            Clear
          </button>
        )}
      </div>

      {/* Tag list */}
      <div role="group" aria-label="Tag filters" className="flex-1 overflow-y-auto p-2">
        {sortedEntries.length === 0 ? (
          <div className="px-2 py-3 text-center text-xs text-muted-foreground">
            {loading ? 'Loading tags…' : 'No tags found'}
          </div>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {sortedEntries.map(({ tag, count }) => {
              const isActive = selectedTags.has(tag)
              return (
                <button
                  key={tag}
                  type="button"
                  role="checkbox"
                  aria-checked={isActive}
                  onClick={() => handleToggle(tag)}
                  className={[
                    'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs transition-colors',
                    isActive
                      ? 'border-primary bg-primary text-primary-foreground'
                      : 'border-border bg-card text-foreground hover:bg-accent/50',
                  ].join(' ')}
                >
                  <span>{tag}</span>
                  <span
                    className={[
                      'rounded-full px-1 py-0 text-[10px] font-medium',
                      isActive
                        ? 'bg-primary-foreground/20 text-primary-foreground'
                        : 'bg-muted text-muted-foreground',
                    ].join(' ')}
                    aria-label={`${count} note${count !== 1 ? 's' : ''}`}
                  >
                    {count}
                  </span>
                </button>
              )
            })}
          </div>
        )}
      </div>
    </div>
  )
}
