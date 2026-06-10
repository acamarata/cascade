/**
 * Purpose: Single tool card for wizard Phase 5 — per-tool mode selection.
 * Inputs:
 *   - toolId: ToolId to display
 *   - mode: current ToolMode selection ('cascade-managed' | 'independent')
 *   - isInstalled: whether this tool was found in the scan; false renders grayed/disabled
 *   - onChange: called when the user toggles the mode
 * Outputs: A styled card with tool name, icon, mode toggle, and description text.
 * Constraints:
 *   - Uninstalled tools are displayed (all 7 always shown) but visually grayed out and
 *     non-interactive (opacity-50 + pointer-events-none).
 *   - No Tauri calls — pure presentational component.
 *   - onChange must not be called for uninstalled tools.
 * SPORT: MASTER-COMPONENTS.md — ToolModeCard — wizard step 5
 */

import { cn } from '@/lib/utils'
import type { ToolId, ToolMode } from '../types'

// ---------------------------------------------------------------------------
// Tool display metadata
// ---------------------------------------------------------------------------

interface ToolMeta {
  label: string
  /** Tailwind bg color class for the icon badge. */
  iconColor: string
  /** Single-letter abbreviation for the icon badge. */
  abbr: string
}

const TOOL_META: Record<ToolId, ToolMeta> = {
  'claude-code': { label: 'Claude Code', iconColor: 'bg-orange-500', abbr: 'CC' },
  opencode: { label: 'OpenCode', iconColor: 'bg-purple-600', abbr: 'OC' },
  codex: { label: 'Codex', iconColor: 'bg-emerald-600', abbr: 'CX' },
  cursor: { label: 'Cursor', iconColor: 'bg-sky-500', abbr: 'CR' },
  aider: { label: 'Aider', iconColor: 'bg-rose-500', abbr: 'AI' },
  windsurf: { label: 'Windsurf', iconColor: 'bg-cyan-600', abbr: 'WS' },
  antigravity: { label: 'Antigravity', iconColor: 'bg-violet-600', abbr: 'AG' },
}

// ---------------------------------------------------------------------------
// Mode description copy
// ---------------------------------------------------------------------------

const MODE_DESCRIPTIONS: Record<ToolMode, string> = {
  'cascade-managed':
    'Symlinks added at each cascade tier — your tool reads cascade content transparently',
  independent: "Tool keeps its own files — cascade doesn't touch them",
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface ToolModeCardProps {
  toolId: ToolId
  mode: ToolMode
  isInstalled: boolean
  onChange: (toolId: ToolId, mode: ToolMode) => void
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * ToolModeCard: Displays one AI tool with a two-option mode toggle.
 *
 * Shows the tool name, icon badge, and a Switch-style toggle between
 * "Cascade-managed" (default) and "Independent". Tools that were not
 * found in the scan are shown grayed out and non-interactive.
 *
 * @param toolId      - Identifier for the tool to display
 * @param mode        - Currently selected ToolMode for this tool
 * @param isInstalled - False if tool was not found in the legacy scan
 * @param onChange    - Callback fired when the user flips the toggle
 *
 * @example
 * ```tsx
 * <ToolModeCard
 *   toolId="claude-code"
 *   mode="cascade-managed"
 *   isInstalled={true}
 *   onChange={(id, m) => updateState({ toolModes: { ...toolModes, [id]: m } })}
 * />
 * ```
 */
export function ToolModeCard({ toolId, mode, isInstalled, onChange }: ToolModeCardProps) {
  const meta = TOOL_META[toolId]
  const isCascadeManaged = mode === 'cascade-managed'

  function handleToggle() {
    if (!isInstalled) return
    onChange(toolId, isCascadeManaged ? 'independent' : 'cascade-managed')
  }

  return (
    <div
      className={cn(
        'rounded-lg border bg-card px-4 py-3 transition-colors',
        isInstalled ? 'hover:border-ring/50' : 'opacity-50 pointer-events-none'
      )}
      aria-disabled={!isInstalled}
    >
      <div className="flex items-center gap-3">
        {/* Icon badge */}
        <span
          aria-hidden="true"
          className={cn(
            'flex h-9 w-9 shrink-0 items-center justify-center rounded-md text-xs font-bold text-white',
            meta.iconColor
          )}
        >
          {meta.abbr}
        </span>

        {/* Name + status */}
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium leading-tight">
            {meta.label}
            {!isInstalled && (
              <span className="ml-2 text-xs font-normal text-muted-foreground">Not installed</span>
            )}
          </p>
        </div>

        {/* Toggle */}
        <button
          type="button"
          role="switch"
          aria-checked={isCascadeManaged}
          aria-label={`${meta.label}: toggle between Cascade-managed and Independent`}
          onClick={handleToggle}
          disabled={!isInstalled}
          className={cn(
            'relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent',
            'transition-colors duration-200 ease-in-out',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
            'disabled:pointer-events-none disabled:opacity-50',
            isCascadeManaged ? 'bg-primary' : 'bg-input'
          )}
        >
          <span
            aria-hidden="true"
            className={cn(
              'pointer-events-none inline-block h-4 w-4 rounded-full bg-white shadow-lg',
              'ring-0 transition duration-200 ease-in-out',
              isCascadeManaged ? 'translate-x-4' : 'translate-x-0'
            )}
          />
        </button>
      </div>

      {/* Mode label + description (only for installed tools) */}
      {isInstalled && (
        <p className="mt-2 pl-12 text-xs text-muted-foreground">
          <span className="font-medium text-foreground">
            {isCascadeManaged ? 'Cascade-managed' : 'Independent'}:
          </span>{' '}
          {MODE_DESCRIPTIONS[mode]}
        </p>
      )}
    </div>
  )
}
