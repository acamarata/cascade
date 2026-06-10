/**
 * Purpose: Floating full-text search panel for the vault.
 *   Opens on Cmd+Shift+F (macOS) / Ctrl+Shift+F (Windows/Linux), accepts a query,
 *   debounces 300 ms, calls vault_search via useVaultSearch, and renders matches
 *   grouped by file. Clicking a result calls onOpenNote to open the note and
 *   emits a "vault://scrollToLine" Tauri event so the editor can scroll to the match.
 *   Escape or a second Cmd+Shift+F closes the panel.
 * Inputs:
 *   - root: absolute vault root path (passed from the vault page / context)
 *   - onOpenNote: callback invoked with (absolutePath: string, lineNo: number)
 * Outputs:
 *   - Renders a fixed floating panel (position: fixed, top 20%, centered)
 *   - Keyboard nav: ArrowUp/Down moves through results; Enter opens selected; Escape closes
 * Constraints:
 *   - Panel is mounted/unmounted on open — no hidden-but-rendered state.
 *   - Accessibility: role="dialog", aria-modal, aria-label.
 *   - No global state — keybind wired in the parent (App.tsx) via useSearchPanel.
 *   - Snippet is capped to 80 chars in the UI; full lineText kept in SearchMatch.
 *   - The "vault://scrollToLine" Tauri event is best-effort — MarkdownEditor listens
 *     for it; if the editor is not open it silently drops the event.
 * SPORT: MASTER-COMPONENTS.md — SearchPanel (T-P3-E06-09)
 */

import * as Dialog from '@radix-ui/react-dialog'
import { FileText, Loader2, Search, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { emit } from '@tauri-apps/api/event'
import { useVaultSearch } from '@/hooks/useVaultSearch'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Input } from '@/components/ui/input'
import type { SearchMatch } from '@/types/vault'

// ── Constants ─────────────────────────────────────────────────────────────────

/** Maximum characters shown in the match snippet row. */
const SNIPPET_MAX_LEN = 80

/** The Tauri event name listened by MarkdownEditor to scroll to a match. */
const SCROLL_TO_LINE_EVENT = 'vault://scrollToLine'

// ── Helper ────────────────────────────────────────────────────────────────────

/** Truncate text to maxLen, appending "…" when truncated. */
function truncate(text: string, maxLen: number): string {
  const t = text.trim()
  return t.length <= maxLen ? t : t.slice(0, maxLen - 1) + '…'
}

/** Extract the file name (last path component) from an absolute path. */
function fileName(path: string): string {
  return path.split('/').pop() ?? path
}

/** Group SearchMatch[] by file path, preserving insertion order. */
function groupByFile(matches: SearchMatch[]): Map<string, SearchMatch[]> {
  const map = new Map<string, SearchMatch[]>()
  for (const m of matches) {
    const list = map.get(m.file)
    if (list) {
      list.push(m)
    } else {
      map.set(m.file, [m])
    }
  }
  return map
}

// ── Payload type for the scrollToLine Tauri event ────────────────────────────

interface ScrollToLinePayload {
  path: string
  lineNo: number
}

// ── Props ─────────────────────────────────────────────────────────────────────

export interface SearchPanelProps {
  /** Absolute vault root — passed straight to vault_search IPC. */
  root: string | undefined
  /**
   * Called when the user selects a result.
   * Opens the note in the editor and scrolls to `lineNo`.
   */
  onOpenNote: (path: string, lineNo: number) => void
  /** Whether the panel is currently open. */
  open: boolean
  /** Close the panel. */
  onClose: () => void
}

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * Floating vault search panel.
 * Toggled open/closed by the parent via `open` / `onClose`.
 * Internal state: query, result selection index.
 */
export function SearchPanel({ root, onOpenNote, open, onClose }: SearchPanelProps) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [selectedIdx, setSelectedIdx] = useState(0)

  const { query, setQuery, results, isLoading, error } = useVaultSearch({ root })

  // Flatten grouped results for keyboard navigation.
  const flatResults = results

  // Reset selection when results change.
  useEffect(() => {
    setSelectedIdx(0)
  }, [results])

  // Auto-focus input when panel opens; clear query on close.
  useEffect(() => {
    if (open) {
      setTimeout(() => inputRef.current?.focus(), 50)
    } else {
      setQuery('')
    }
  }, [open, setQuery])

  const openResult = useCallback(
    async (match: SearchMatch) => {
      onOpenNote(match.file, match.lineNo)
      // Emit the scrollToLine event — best-effort (MarkdownEditor listens).
      const payload: ScrollToLinePayload = { path: match.file, lineNo: match.lineNo }
      try {
        await emit(SCROLL_TO_LINE_EVENT, payload)
      } catch {
        // Silently drop — editor may not be open yet.
      }
      onClose()
    },
    [onClose, onOpenNote],
  )

  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
        return
      }
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setSelectedIdx((i) => Math.min(i + 1, flatResults.length - 1))
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setSelectedIdx((i) => Math.max(i - 1, 0))
        return
      }
      if (e.key === 'Enter' && flatResults.length > 0) {
        e.preventDefault()
        const match = flatResults[selectedIdx]
        if (match) void openResult(match)
        return
      }
    },
    [flatResults, onClose, openResult, selectedIdx],
  )

  const grouped = groupByFile(results)
  // Build a flat index → grouped-file key and item for keyboard selection.
  let flatIdx = -1

  return (
    <Dialog.Root open={open} onOpenChange={(v) => !v && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/50 backdrop-blur-sm" />
        <Dialog.Content
          className="fixed left-1/2 top-[20%] z-50 w-full max-w-2xl -translate-x-1/2 rounded-xl border border-border bg-popover shadow-2xl outline-none"
          aria-label="Vault search"
          aria-modal
          onKeyDown={handleKeyDown}
          data-testid="search-panel"
        >
          <Dialog.Title className="sr-only">Vault search</Dialog.Title>

          {/* ── Search input ──────────────────────────────────────────────── */}
          <div className="flex items-center gap-2 border-b border-border px-4 py-3">
            {isLoading ? (
              <Loader2 className="h-4 w-4 shrink-0 animate-spin text-muted-foreground" />
            ) : (
              <Search className="h-4 w-4 shrink-0 text-muted-foreground" />
            )}
            <Input
              ref={inputRef}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search vault notes…"
              className="flex-1 border-0 bg-transparent p-0 text-sm shadow-none focus-visible:ring-0"
              aria-label="Search query"
              data-testid="search-input"
            />
            <button
              onClick={onClose}
              className="ml-auto rounded p-1 text-muted-foreground hover:text-foreground"
              aria-label="Close search"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          {/* ── Results ───────────────────────────────────────────────────── */}
          <ScrollArea className="max-h-[400px]">
            <div className="p-2" data-testid="search-results">

              {/* Error state */}
              {error && (
                <p className="px-3 py-4 text-center text-sm text-destructive" role="alert">
                  {error.includes('not found')
                    ? 'ripgrep (rg) not available — install ripgrep to enable full-text search.'
                    : `Search error: ${error}`}
                </p>
              )}

              {/* Empty state — only shown after a non-empty query completes */}
              {!error && !isLoading && query.trim() !== '' && results.length === 0 && (
                <p className="px-3 py-6 text-center text-sm text-muted-foreground">
                  No results for <strong>{query}</strong>
                </p>
              )}

              {/* Initial state */}
              {!error && query.trim() === '' && (
                <p className="px-3 py-6 text-center text-sm text-muted-foreground">
                  Type to search all vault notes
                </p>
              )}

              {/* Results grouped by file */}
              {!error && results.length > 0 &&
                Array.from(grouped.entries()).map(([filePath, fileMatches]) => (
                  <div key={filePath} className="mb-2">
                    {/* File heading */}
                    <div className="flex items-center gap-1.5 px-2 py-1">
                      <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                      <span
                        className="truncate text-xs font-medium text-muted-foreground"
                        title={filePath}
                      >
                        {fileName(filePath)}
                      </span>
                      <span className="ml-auto text-xs text-muted-foreground opacity-60">
                        {fileMatches.length}
                      </span>
                    </div>

                    {/* Match rows */}
                    {fileMatches.map((match) => {
                      flatIdx++
                      const isSelected = flatIdx === selectedIdx
                      const currentIdx = flatIdx
                      return (
                        <button
                          key={`${match.file}:${match.lineNo}`}
                          className={[
                            'w-full rounded-md px-3 py-1.5 text-left text-sm transition-colors',
                            isSelected
                              ? 'bg-accent text-accent-foreground'
                              : 'text-foreground hover:bg-accent/50',
                          ].join(' ')}
                          onClick={() => void openResult(match)}
                          onMouseEnter={() => setSelectedIdx(currentIdx)}
                          data-testid="search-result-row"
                        >
                          <span className="mr-2 font-mono text-xs text-muted-foreground">
                            :{match.lineNo}
                          </span>
                          <span className="font-mono text-xs">
                            {truncate(match.lineText, SNIPPET_MAX_LEN)}
                          </span>
                        </button>
                      )
                    })}
                  </div>
                ))}

              {/* Results count */}
              {!error && results.length > 0 && (
                <p className="px-3 py-1 text-right text-xs text-muted-foreground opacity-60">
                  {results.length === 200 ? '200+ matches (capped)' : `${results.length} matches`}
                </p>
              )}
            </div>
          </ScrollArea>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
