/**
 * Purpose: Settings panel section listing archived tools and offering per-tool
 *   restore via RestoreConfirmDialog. Calls list_archived_tools on mount.
 *
 * Inputs:  None — fetches archived tool list from IPC on mount.
 * Outputs: Section with one row per archived tool; Restore button opens dialog.
 *
 * Constraints:
 *   - Hidden (returns null) when the tool list is empty.
 *   - Shows "No archived tool homes" only when the list loads empty.
 *   - Post-restore: row's Restore button replaced with "Restored" badge.
 *   - Loading and error states are rendered inline.
 *   - WCAG 2.1 AA: focus-visible rings, role/aria labels.
 *
 * SPORT: MASTER-COMPONENTS.md — RestoreToolSection — settings/ — T-P3-E03-22
 * Task: T-P3-E03-22
 */

import { useEffect, useState } from 'react'
import { ToolArchive, RestoreResult } from '@/lib/archive/types'
import { listArchivedTools } from '@/lib/archive/archiveApi'
import { RestoreConfirmDialog } from './RestoreConfirmDialog'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { Archive, CheckCircle2, Loader2 } from 'lucide-react'

// ---------------------------------------------------------------------------
// Local state types
// ---------------------------------------------------------------------------

type RowState =
  | { phase: 'idle' }
  | { phase: 'confirming' }
  | { phase: 'restored'; result: RestoreResult }

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Settings section that surfaces archived tools and lets the user restore them.
 *
 * Mount contract:
 *   - Fetches via listArchivedTools() on mount.
 *   - Returns null while loading (avoids empty section flash).
 *   - Shows section only when at least one archived tool exists.
 */
export function RestoreToolSection() {
  const [tools, setTools] = useState<ToolArchive[] | null>(null)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [rowStates, setRowStates] = useState<Record<string, RowState>>({})

  useEffect(() => {
    listArchivedTools()
      .then((list) => {
        setTools(list)
        const initial: Record<string, RowState> = {}
        for (const t of list) initial[t.toolId] = { phase: 'idle' }
        setRowStates(initial)
      })
      .catch((err) => setLoadError(String(err)))
  }, [])

  // Still loading — render nothing (avoid flash of empty section)
  if (tools === null && loadError === null) return null

  // IPC error
  if (loadError) {
    return (
      <section aria-labelledby="restore-section-heading" className="space-y-3">
        <h2 id="restore-section-heading" className="text-sm font-medium">
          Tool Restore
        </h2>
        <p className="text-sm text-destructive">
          Could not load archived tools: {loadError}
        </p>
      </section>
    )
  }

  // No archived tools — hide section entirely (per acceptance criteria)
  if (!tools || tools.length === 0) return null

  function setRow(toolId: string, state: RowState) {
    setRowStates((prev) => ({ ...prev, [toolId]: state }))
  }

  const confirming = Object.entries(rowStates).find(([, s]) => s.phase === 'confirming')
  const activeToolId = confirming?.[0]
  const activeArchive = activeToolId ? tools.find((t) => t.toolId === activeToolId) : undefined

  return (
    <>
      <section aria-labelledby="restore-section-heading" className="space-y-3">
        <h2 id="restore-section-heading" className="text-sm font-medium">
          Tool Restore
        </h2>
        <p className="text-sm text-muted-foreground">
          These tools had their legacy home directories archived during setup. You can restore them
          at any time.
        </p>

        <div className="divide-y divide-border rounded-md border border-border">
          {tools.map((tool) => {
            const row = rowStates[tool.toolId] ?? { phase: 'idle' }
            const toolLabel = tool.toolId
              .split('-')
              .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
              .join(' ')
            const archivedDate = new Date(tool.archivedAt).toLocaleDateString(undefined, {
              year: 'numeric',
              month: 'short',
              day: 'numeric',
            })

            return (
              <div
                key={tool.toolId}
                className="flex items-center justify-between px-4 py-3 gap-4"
              >
                {/* Left: tool info */}
                <div className="flex items-center gap-3 min-w-0">
                  <Archive
                    className="h-4 w-4 flex-shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                  <div className="min-w-0">
                    <p className="text-sm font-medium truncate">{toolLabel}</p>
                    <p className="text-xs text-muted-foreground truncate">
                      {tool.files.length} file{tool.files.length !== 1 ? 's' : ''} · archived{' '}
                      {archivedDate}
                    </p>
                  </div>
                </div>

                {/* Right: action */}
                <div className="flex-shrink-0">
                  {row.phase === 'idle' && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => setRow(tool.toolId, { phase: 'confirming' })}
                    >
                      Restore
                    </Button>
                  )}

                  {row.phase === 'confirming' && (
                    <Button variant="outline" size="sm" disabled>
                      <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" aria-hidden="true" />
                      Restore
                    </Button>
                  )}

                  {row.phase === 'restored' && (
                    <span
                      className={cn(
                        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5',
                        'text-xs font-medium bg-green-500/10 text-green-700 dark:text-green-400',
                      )}
                    >
                      <CheckCircle2 className="h-3.5 w-3.5" aria-hidden="true" />
                      Restored
                    </span>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </section>

      {/* Dialog rendered outside the table to avoid stacking-context issues */}
      {activeArchive && (
        <RestoreConfirmDialog
          archive={activeArchive}
          onRestore={(res) => setRow(activeArchive.toolId, { phase: 'restored', result: res })}
          onCancel={() => setRow(activeArchive.toolId, { phase: 'idle' })}
        />
      )}
    </>
  )
}
