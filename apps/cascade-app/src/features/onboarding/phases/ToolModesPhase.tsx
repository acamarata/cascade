/**
 * Purpose: Wizard Phase 5 — per-tool mode selection (Cascade-managed vs Independent).
 * Inputs:
 *   - WizardContext via useWizard(): state.detectedToolIds, state.toolModes, goNext, goBack, updateState
 * Outputs:
 *   - One ToolModeCard per tool (all 7 always shown; undetected tools grayed out)
 *   - "Select all / Select none" bulk-action buttons
 *   - Persists selections to WizardContext.toolModes via updateState({ toolModes })
 *   - Next always enabled (all-Independent is a valid choice)
 * Constraints:
 *   - Zero Tauri calls — reads from WizardContext only.
 *   - Select all/none only affects installed (detected) tools; uninstalled tools are unchanged.
 *   - If detectedToolIds is null (ScanLegacy not yet completed), all 7 tools appear as uninstalled.
 *   - No navigation logic — delegates to goNext/goBack from WizardContext.
 * SPORT: MASTER-COMPONENTS.md — ToolModesPhase — wizard step 5
 */

import * as React from 'react'
import { useWizard } from '../WizardContext'
import { ToolModeCard } from './ToolModeCard'
import { TOOL_IDS } from '@/lib/scanner/types'
import type { ToolId, ToolMode } from '../types'
import { cn } from '@/lib/utils'

// Default mode for any tool not yet explicitly chosen.
const DEFAULT_MODE: ToolMode = 'cascade-managed'

// ---------------------------------------------------------------------------
// ToolModesPhase
// ---------------------------------------------------------------------------

/**
 * ToolModesPhase: Wizard step 5 — let the user decide per tool whether Cascade
 * manages it (symlinks at each tier) or leaves it fully independent.
 *
 * All 7 known tools are always rendered. Tools that were found in the ScanLegacy
 * step (detectedToolIds) are interactive; tools not in the scan are shown grayed
 * out with a "Not installed" badge and cannot be toggled.
 *
 * Selections are persisted in WizardContext.toolModes as a
 * `Partial<Record<ToolId, ToolMode>>` and survive step navigation (the context
 * reducer preserves state across back/forward).
 *
 * @example
 * ```tsx
 * // Wired via WizardRouter case WizardStep.ToolModes
 * case WizardStep.ToolModes:
 *   return <ToolModesPhase />
 * ```
 */
export function ToolModesPhase() {
  const { state, goNext, goBack, updateState } = useWizard()
  const { detectedToolIds, toolModes } = state

  // Build a Set for O(1) lookup
  const detectedSet: ReadonlySet<ToolId> = React.useMemo(
    () => new Set(detectedToolIds ?? []),
    [detectedToolIds],
  )

  // Resolved mode per tool (falls back to DEFAULT_MODE if not yet set).
  function modeFor(toolId: ToolId): ToolMode {
    return toolModes[toolId] ?? DEFAULT_MODE
  }

  // Whether any tools were actually detected.
  const hasDetectedTools = detectedSet.size > 0

  // -------------------------------------------------------------------------
  // Handlers
  // -------------------------------------------------------------------------

  function handleChange(toolId: ToolId, mode: ToolMode) {
    updateState({
      toolModes: { ...toolModes, [toolId]: mode },
    })
  }

  function handleSelectAll() {
    // Only sets detected tools; uninstalled tools are left unchanged.
    const updated: Partial<Record<ToolId, ToolMode>> = { ...toolModes }
    for (const id of detectedSet) {
      updated[id] = 'cascade-managed'
    }
    updateState({ toolModes: updated })
  }

  function handleSelectNone() {
    // Only sets detected tools; uninstalled tools are left unchanged.
    const updated: Partial<Record<ToolId, ToolMode>> = { ...toolModes }
    for (const id of detectedSet) {
      updated[id] = 'independent'
    }
    updateState({ toolModes: updated })
  }

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------

  return (
    <div className="flex h-full flex-col gap-6">
      {/* Heading */}
      <div>
        <h2 className="text-xl font-semibold tracking-tight">Choose how Cascade manages each tool</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          For each tool, choose whether Cascade adds symlinks at each instruction tier, or leaves the
          tool's own files untouched. You can change this later.
        </p>
      </div>

      {/* Zero-tools empty state */}
      {!hasDetectedTools && (
        <div className="rounded-lg border border-dashed bg-muted/30 px-6 py-8 text-center">
          <p className="text-sm text-muted-foreground">
            No tools were detected in the legacy scan. You can continue — all tools default to
            Cascade-managed once installed.
          </p>
        </div>
      )}

      {/* Bulk actions (only visible when ≥1 tool detected) */}
      {hasDetectedTools && (
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={handleSelectAll}
            className={cn(
              'inline-flex h-7 items-center justify-center rounded-md px-3 text-xs font-medium',
              'border border-input bg-background shadow-sm',
              'hover:bg-accent hover:text-accent-foreground',
              'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
              'transition-colors',
            )}
          >
            Select all Cascade-managed
          </button>
          <button
            type="button"
            onClick={handleSelectNone}
            className={cn(
              'inline-flex h-7 items-center justify-center rounded-md px-3 text-xs font-medium',
              'border border-input bg-background shadow-sm',
              'hover:bg-accent hover:text-accent-foreground',
              'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
              'transition-colors',
            )}
          >
            Select all Independent
          </button>
        </div>
      )}

      {/* Tool cards — all 7 always rendered */}
      <div
        role="list"
        aria-label="AI tool mode selections"
        className="flex flex-col gap-2"
      >
        {TOOL_IDS.map((toolId) => (
          <div key={toolId} role="listitem">
            <ToolModeCard
              toolId={toolId}
              mode={modeFor(toolId)}
              isInstalled={detectedSet.has(toolId)}
              onChange={handleChange}
            />
          </div>
        ))}
      </div>

      {/* Bottom navigation */}
      <div className="mt-auto flex items-center justify-between pt-2">
        <button
          type="button"
          onClick={goBack}
          aria-label="Go back to previous wizard step"
          className={cn(
            'inline-flex h-9 items-center justify-center rounded-md px-4 text-sm font-medium',
            'border border-input bg-background shadow-sm',
            'hover:bg-accent hover:text-accent-foreground',
            'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
            'transition-colors',
          )}
        >
          Back
        </button>

        {/* Next is always enabled — all-Independent is a valid user choice */}
        <button
          type="button"
          onClick={goNext}
          aria-label="Continue to next wizard step"
          className={cn(
            'inline-flex h-9 items-center justify-center rounded-md px-6 text-sm font-medium',
            'bg-primary text-primary-foreground shadow',
            'hover:bg-primary/90',
            'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
            'transition-colors',
          )}
        >
          Next
        </button>
      </div>
    </div>
  )
}
