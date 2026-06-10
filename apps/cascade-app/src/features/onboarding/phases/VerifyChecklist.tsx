/**
 * Purpose: VerifyChecklist — per-tool, per-kind checklist for wizard Phase 6.
 *   Derives checklist items from mergeResult.sections sourceFile attributions.
 *   Each tool × kind combination found in any section becomes one checklist row.
 *   Items are auto-verified when at least one source file of that kind was processed.
 *
 * Inputs:
 *   - detectedToolIds: ToolId[] from WizardContext.state.detectedToolIds (which tools
 *     were found at scan time; null if scan not yet complete)
 *   - mergeResults: Partial<Record<string, RichMergeResult>> from WizardContext
 *   - onRerunMerge: callback triggered when user clicks "Re-run merge" for a kind
 *   - onAllResolved: callback fires when every item is verified or skipped
 *
 * Outputs:
 *   - Rendered checklist rows grouped by tool
 *   - Auto-verified items (any source file of that kind appears in mergeResult.sections)
 *   - Per-item action buttons: Accept ✓ / Re-run merge 🔄 / Skip ⏭
 *   - Counts summary: X verified, Y skipped, Z pending
 *
 * Constraints:
 *   - No Tauri calls — purely derives state from props.
 *   - Auto-verification runs on mount and whenever mergeResults changes.
 *   - All items start 'unchecked' unless auto-verified.
 *   - onAllResolved fires when zero items remain 'unchecked'.
 *   - If mergeResults is empty and detectedToolIds is non-empty, shows
 *     one 'unchecked' row per detected tool to prompt the user.
 *
 * SPORT: MASTER-COMPONENTS.md — VerifyChecklist — wizard step 6 — ✅
 * Task: T-P3-E03-32
 */

import * as React from 'react'
import { CheckCircle2, HelpCircle, SkipForward, RefreshCw } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { ToolId, FileKind } from '@/lib/scanner/types'
import type { WizardMergeResult as RichMergeResult } from '@/features/onboarding/types'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Checklist item lifecycle state. */
export type CheckItemStatus = 'verified' | 'skipped' | 'unchecked'

/** One checklist row: a single tool × kind combination. */
export interface CheckItem {
  /** Stable id: `${toolId}:${kind}` */
  id: string
  toolId: ToolId
  kind: FileKind
  /** Number of source files of this kind that were processed in merge results. */
  sourcesProcessed: number
  status: CheckItemStatus
}

// ---------------------------------------------------------------------------
// Derivation helpers
// ---------------------------------------------------------------------------

/**
 * Build checklist items from mergeResults sourceFile attributions.
 * Auto-verifies items where sourcesProcessed > 0.
 * Falls back to one synthetic 'other' row per detectedToolId when no merge
 * data exists for that tool (prompts the user to re-run merge).
 */
function deriveItems(
  detectedToolIds: ToolId[] | null,
  mergeResults: Partial<Record<string, RichMergeResult>>,
): CheckItem[] {
  // Build a map: toolId → kind → count of processed source files.
  const counts = new Map<ToolId, Map<FileKind, number>>()

  for (const result of Object.values(mergeResults)) {
    if (!result) continue
    for (const section of result.sections) {
      for (const sf of section.sourceFiles) {
        const toolId = sf.toolId as ToolId
        if (!counts.has(toolId)) counts.set(toolId, new Map())
        const kindMap = counts.get(toolId)!
        kindMap.set(sf.kind, (kindMap.get(sf.kind) ?? 0) + 1)
      }
    }
  }

  const items: CheckItem[] = []

  // For each tool found in merge data, emit one row per kind.
  for (const [toolId, kindMap] of counts.entries()) {
    for (const [kind, processed] of kindMap.entries()) {
      items.push({
        id: `${toolId}:${kind}`,
        toolId,
        kind,
        sourcesProcessed: processed,
        status: processed > 0 ? 'verified' : 'unchecked',
      })
    }
  }

  // For any detected tool that has NO merge data, add a synthetic 'unchecked' row
  // so the user sees it and can trigger a re-run.
  if (detectedToolIds) {
    for (const toolId of detectedToolIds) {
      if (!counts.has(toolId)) {
        items.push({
          id: `${toolId}:other`,
          toolId,
          kind: 'other',
          sourcesProcessed: 0,
          status: 'unchecked',
        })
      }
    }
  }

  return items
}

// ---------------------------------------------------------------------------
// Label helpers
// ---------------------------------------------------------------------------

/** Human-readable label for a FileKind. */
function kindLabel(kind: FileKind): string {
  const labels: Record<FileKind, string> = {
    instructions: 'Instructions',
    memory: 'Memory',
    docs: 'Docs',
    tasks: 'Tasks',
    ideas: 'Ideas',
    other: 'Other files',
  }
  return labels[kind] ?? kind
}

/** Human-readable label for a ToolId. */
function toolLabel(toolId: ToolId): string {
  const labels: Record<ToolId, string> = {
    'claude-code': 'Claude Code',
    opencode: 'OpenCode',
    codex: 'Codex',
    cursor: 'Cursor',
    aider: 'Aider',
    windsurf: 'Windsurf',
    antigravity: 'Antigravity',
  }
  return labels[toolId] ?? toolId
}

// ---------------------------------------------------------------------------
// StatusIcon
// ---------------------------------------------------------------------------

function StatusIcon({ status }: { status: CheckItemStatus }) {
  // Icons are decorative here — adjacent text in the row conveys status.
  // Screen readers get the status from the li content; icons are aria-hidden.
  if (status === 'verified') {
    return <CheckCircle2 className="h-4 w-4 text-green-500 shrink-0" aria-hidden="true" />
  }
  if (status === 'skipped') {
    return <SkipForward className="h-4 w-4 text-muted-foreground shrink-0" aria-hidden="true" />
  }
  return <HelpCircle className="h-4 w-4 text-yellow-500 shrink-0" aria-hidden="true" />
}

// ---------------------------------------------------------------------------
// VerifyChecklist props & component
// ---------------------------------------------------------------------------

export interface VerifyChecklistProps {
  detectedToolIds: ToolId[] | null
  mergeResults: Partial<Record<string, RichMergeResult>>
  onRerunMerge: (toolId: ToolId, kind: FileKind) => void
  onAllResolved: () => void
}

/**
 * Renders a per-tool, per-kind checklist for wizard Phase 6.
 *
 * Items are auto-verified when at least one source file of that kind from that
 * tool appears in the merge results. The user can manually accept, skip, or
 * re-run merge for each item.
 *
 * `onAllResolved` fires when every item reaches 'verified' or 'skipped',
 * enabling the parent's "Proceed to archive" button.
 *
 * @param detectedToolIds - Tools found during ScanLegacy step.
 * @param mergeResults    - Per-tier merge results from WizardContext.
 * @param onRerunMerge    - Called when user requests a re-run for one kind.
 * @param onAllResolved   - Called when all items are verified or skipped.
 */
export function VerifyChecklist({
  detectedToolIds,
  mergeResults,
  onRerunMerge,
  onAllResolved,
}: VerifyChecklistProps) {
  const [items, setItems] = React.useState<CheckItem[]>(() =>
    deriveItems(detectedToolIds, mergeResults),
  )

  // Re-derive when mergeResults changes (e.g. after a re-run merge).
  React.useEffect(() => {
    setItems(prev => {
      const next = deriveItems(detectedToolIds, mergeResults)
      // Preserve manually set statuses across re-derives.
      const prevById = new Map(prev.map(i => [i.id, i]))
      return next.map(item => {
        const existing = prevById.get(item.id)
        // Keep user-chosen 'skipped'. Re-verified stays as verified from new data.
        if (existing?.status === 'skipped') return { ...item, status: 'skipped' as CheckItemStatus }
        return item
      })
    })
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mergeResults])

  // Fire onAllResolved when every item is verified or skipped.
  const allResolved = items.length > 0 && items.every(i => i.status !== 'unchecked')
  React.useEffect(() => {
    if (allResolved) onAllResolved()
  }, [allResolved, onAllResolved])

  const setStatus = React.useCallback((id: string, status: CheckItemStatus) => {
    setItems(prev => prev.map(i => (i.id === id ? { ...i, status } : i)))
  }, [])

  if (items.length === 0) {
    return (
      <div className="rounded-md border border-dashed p-6 text-center text-sm text-muted-foreground">
        No legacy content found. Nothing to verify.
      </div>
    )
  }

  // Group by toolId for sectioned rendering.
  const byTool = new Map<ToolId, CheckItem[]>()
  for (const item of items) {
    const group = byTool.get(item.toolId) ?? []
    group.push(item)
    byTool.set(item.toolId, group)
  }

  const verified = items.filter(i => i.status === 'verified').length
  const skipped = items.filter(i => i.status === 'skipped').length
  const pending = items.filter(i => i.status === 'unchecked').length

  return (
    <div className="flex flex-col gap-4">
      {/* Summary badge row */}
      <div className="flex items-center gap-3 text-xs text-muted-foreground" aria-label={`${verified} verified, ${skipped} skipped${pending > 0 ? `, ${pending} pending` : ''}`}>
        <span className="flex items-center gap-1">
          <CheckCircle2 className="h-3 w-3 text-green-500" aria-hidden="true" />
          {verified} verified
        </span>
        <span className="flex items-center gap-1">
          <SkipForward className="h-3 w-3" aria-hidden="true" />
          {skipped} skipped
        </span>
        {pending > 0 && (
          <span className="flex items-center gap-1 text-yellow-600 dark:text-yellow-400">
            <HelpCircle className="h-3 w-3" aria-hidden="true" />
            {pending} pending
          </span>
        )}
      </div>

      {/* Per-tool sections */}
      {Array.from(byTool.entries()).map(([toolId, toolItems]) => (
        <div key={toolId} className="rounded-md border">
          {/* Tool header */}
          <div className="border-b bg-muted/40 px-4 py-2">
            <span className="text-sm font-medium text-foreground">{toolLabel(toolId)}</span>
          </div>

          {/* Checklist rows */}
          <ul className="divide-y">
            {toolItems.map(item => (
              <li
                key={item.id}
                aria-label={`${kindLabel(item.kind)}: ${item.status}`}
                className={cn(
                  'flex items-center gap-3 px-4 py-3 text-sm',
                  item.status === 'verified' && 'bg-green-500/5',
                  item.status === 'skipped' && 'opacity-60',
                )}
              >
                <StatusIcon status={item.status} />

                {/* Kind label + count */}
                <div className="flex-1 min-w-0">
                  <span className="font-medium text-foreground">{kindLabel(item.kind)}</span>
                  {item.sourcesProcessed > 0 && (
                    <span className="ml-2 text-muted-foreground">
                      — {item.sourcesProcessed} file{item.sourcesProcessed !== 1 ? 's' : ''} captured
                    </span>
                  )}
                  {item.sourcesProcessed === 0 && (
                    <span className="ml-2 text-yellow-600 dark:text-yellow-400">
                      — not yet processed
                    </span>
                  )}
                </div>

                {/* Action buttons — only for unchecked items */}
                {item.status === 'unchecked' && (
                  <div className="flex items-center gap-1 shrink-0">
                    {/* Accept */}
                    <button
                      type="button"
                      onClick={() => setStatus(item.id, 'verified')}
                      aria-label={`Accept ${kindLabel(item.kind)} for ${toolLabel(item.toolId)}`}
                      className="rounded px-2 py-1 text-xs font-medium text-green-700 hover:bg-green-500/10 dark:text-green-400 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      Accept
                    </button>

                    {/* Re-run merge */}
                    <button
                      type="button"
                      onClick={() => onRerunMerge(item.toolId, item.kind)}
                      aria-label={`Re-run AI merge for ${kindLabel(item.kind)} from ${toolLabel(item.toolId)}`}
                      className="flex items-center gap-1 rounded px-2 py-1 text-xs font-medium text-muted-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      <RefreshCw className="h-3 w-3" aria-hidden="true" />
                      Re-run
                    </button>

                    {/* Skip */}
                    <button
                      type="button"
                      onClick={() => setStatus(item.id, 'skipped')}
                      aria-label={`Skip ${kindLabel(item.kind)} for ${toolLabel(item.toolId)}`}
                      className="rounded px-2 py-1 text-xs font-medium text-muted-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      Skip
                    </button>
                  </div>
                )}

                {/* Undo for resolved items */}
                {item.status !== 'unchecked' && (
                  <button
                    type="button"
                    onClick={() => setStatus(item.id, 'unchecked')}
                    aria-label={`Undo ${item.status} for ${kindLabel(item.kind)} from ${toolLabel(item.toolId)}`}
                    className="shrink-0 rounded px-2 py-1 text-xs text-muted-foreground hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    Undo
                  </button>
                )}
              </li>
            ))}
          </ul>
        </div>
      ))}
    </div>
  )
}
