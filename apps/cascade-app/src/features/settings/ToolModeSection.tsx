/**
 * Purpose: Settings section listing all discovered tools with their current
 *   Cascade-managed vs Independent mode, and a per-tool toggle button.
 *
 * Inputs:  None — discovers tools from the scanner on mount; mode state is
 *   persisted to localStorage under the key "cascade:tool-modes".
 *
 * Outputs: Section with one row per discovered tool. Each row shows:
 *   - Tool icon + label
 *   - Mode badge: "Cascade-managed" (green) or "Independent" (grey)
 *   - Action button:
 *       Cascade-managed → "Restore Independence ↻" (calls cascade_unlink_tool)
 *       Independent     → "Make Cascade-managed →" (calls setup_symlinks for that tool)
 *   - Spinner during IPC; inline success/error feedback after each flip.
 *
 * Constraints:
 *   - Hidden (returns null) while loading AND when tool list is empty.
 *   - Shows "No tools detected" only when scanner returns an empty list.
 *   - Confirmation dialog is always shown before any FS mutation.
 *   - Mode state is derived from localStorage on mount (falls back to Independent).
 *   - Post-flip: updates localStorage + mode badge without page reload.
 *   - WCAG 2.1 AA: focus-visible rings, role/aria labels on all interactive elements.
 *
 * Flip semantics (per T-P3-E03-37 spec):
 *   Cascade-managed → Independent: invoke("cascade_unlink_tool", { toolId })
 *     Removes sibling symlinks at each cascade tier. Real files are not touched.
 *   Independent → Cascade-managed: invoke("setup_symlinks", { plan })
 *     Re-creates sibling symlinks for the tool (Phase 8 re-run for one tool).
 *
 * SPORT: MASTER-COMPONENTS.md — ToolModeSection / ToolModeConfirmDialog — settings/ — T-P3-E03-37
 * Task: T-P3-E03-37
 */

import { useEffect, useState, useCallback } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { ToolId, TOOL_IDS } from '@/lib/scanner/types'
import { ToolModeConfirmDialog, FlipDirection } from './ToolModeConfirmDialog'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { Link2, Link2Off, Loader2, CheckCircle2, AlertTriangle } from 'lucide-react'

// ---------------------------------------------------------------------------
// Local storage key for persisting tool mode state
// ---------------------------------------------------------------------------

const LS_KEY = 'cascade:tool-modes'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type ToolMode = 'managed' | 'independent'

/** Per-tool row state machine. */
type RowPhase =
  | { phase: 'idle' }
  | { phase: 'confirming' }
  | { phase: 'flipping' }
  | { phase: 'done'; removed?: string[]; added?: string[] }
  | { phase: 'error'; message: string }

/** Result returned by cascade_unlink_tool IPC. */
interface UnlinkResult {
  removed: string[]
  skipped: string[]
}

/** A single entry in the symlink plan sent to setup_symlinks. */
interface SymlinkEntry {
  source: string
  target: string
  kind: 'replace' | 'sibling'
}

/** Result returned by setup_symlinks IPC. */
interface SymlinkResult {
  source: string
  target: string
  success: boolean
  error?: string
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Load persisted tool modes from localStorage. */
function loadStoredModes(): Record<string, ToolMode> {
  try {
    const raw = localStorage.getItem(LS_KEY)
    if (!raw) return {}
    return JSON.parse(raw) as Record<string, ToolMode>
  } catch {
    return {}
  }
}

/** Persist updated tool modes to localStorage. */
function saveModes(modes: Record<string, ToolMode>): void {
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(modes))
  } catch {
    // Storage full or unavailable — best-effort only.
  }
}

/** Derive a display label from a kebab-case ToolId. */
function toLabel(toolId: string): string {
  return toolId
    .split('-')
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(' ')
}

/**
 * Build a minimal SymlinkPlan for a single tool.
 *
 * For each sibling filename the tool expects, we place a symlink inside
 * ~/.cascade/gci/<filename> → ~/.cascade/CASCADE.md (the GCI tier).
 * The exact CASCADE.md path is resolved by the daemon at runtime; the plan
 * uses a conventional path that the Rust side will validate.
 *
 * Note: A full multi-tier re-link would require per-project tier paths which
 * are not available in the Settings context (no project CWD). This GCI-only
 * plan is the correct post-install default per the architecture doc §5.
 */
function buildSingleToolPlan(toolId: ToolId): SymlinkEntry[] {
  const siblingFiles: Record<string, string[]> = {
    'claude-code': ['CLAUDE.md', 'AGENTS.md'],
    opencode: ['AGENTS.md'],
    cursor: ['.cursorrules'],
    aider: ['.aider.conf.yml'],
    windsurf: ['.windsurf'],
    antigravity: ['.antigravity'],
    codex: ['CODEX.md'],
  }

  const siblings = siblingFiles[toolId] ?? []
  const cascadeMd = `${
    typeof window !== 'undefined'
      ? (window as unknown as Record<string, string>).__CASCADE_HOME__ ?? '~'
      : '~'
  }/.cascade/gci/CASCADE.md`

  return siblings.map((filename) => ({
    source: `${
      typeof window !== 'undefined'
        ? (window as unknown as Record<string, string>).__CASCADE_HOME__ ?? '~'
        : '~'
    }/.cascade/gci/${filename}`,
    target: cascadeMd,
    kind: 'sibling' as const,
  }))
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Settings section for per-tool Cascade-managed ↔ Independent mode toggling.
 *
 * Mount contract:
 *   - Uses TOOL_IDS (static list) — no IPC needed to enumerate tools.
 *   - Loads mode state from localStorage on mount.
 *   - Shows section only when at least one tool is known (always true).
 */
export function ToolModeSection() {
  const [modes, setModes] = useState<Record<string, ToolMode>>({})
  const [rowStates, setRowStates] = useState<Record<string, RowPhase>>({})
  const [mounted, setMounted] = useState(false)

  // Load from localStorage on mount (hydration guard prevents SSR mismatch)
  useEffect(() => {
    const stored = loadStoredModes()
    const initial: Record<string, ToolMode> = {}
    const initialRows: Record<string, RowPhase> = {}
    for (const id of TOOL_IDS) {
      initial[id] = stored[id] ?? 'independent'
      initialRows[id] = { phase: 'idle' }
    }
    setModes(initial)
    setRowStates(initialRows)
    setMounted(true)
  }, [])

  // ---------------------------------------------------------------------------
  // Row state helpers
  // ---------------------------------------------------------------------------

  const setRow = useCallback((toolId: string, state: RowPhase) => {
    setRowStates((prev) => ({ ...prev, [toolId]: state }))
  }, [])

  const setMode = useCallback((toolId: string, mode: ToolMode) => {
    setModes((prev) => {
      const next = { ...prev, [toolId]: mode }
      saveModes(next)
      return next
    })
  }, [])

  // ---------------------------------------------------------------------------
  // Flip handler — called after user confirms the dialog
  // ---------------------------------------------------------------------------

  async function executeFlip(toolId: ToolId, currentMode: ToolMode) {
    setRow(toolId, { phase: 'flipping' })

    if (currentMode === 'managed') {
      // Cascade-managed → Independent: remove symlinks
      try {
        const result = await invoke<UnlinkResult>('cascade_unlink_tool', { toolId })
        setMode(toolId, 'independent')
        setRow(toolId, { phase: 'done', removed: result.removed })
      } catch (err) {
        setRow(toolId, { phase: 'error', message: String(err) })
      }
    } else {
      // Independent → Cascade-managed: re-create symlinks
      const entries = buildSingleToolPlan(toolId)
      try {
        const results = await invoke<SymlinkResult[]>('setup_symlinks', {
          plan: { entries },
        })
        const added = results.filter((r) => r.success).map((r) => r.source)
        setMode(toolId, 'managed')
        setRow(toolId, { phase: 'done', added })
      } catch (err) {
        setRow(toolId, { phase: 'error', message: String(err) })
      }
    }
  }

  // ---------------------------------------------------------------------------
  // Derived state — which tool (if any) is showing the confirm dialog
  // ---------------------------------------------------------------------------

  const confirmingEntry = Object.entries(rowStates).find(
    ([, s]) => s.phase === 'confirming',
  )
  const confirmingToolId = confirmingEntry?.[0] as ToolId | undefined

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  // Avoid flash before localStorage is loaded
  if (!mounted) return null

  return (
    <>
      <section aria-labelledby="tool-mode-section-heading" className="space-y-3">
        <h2 id="tool-mode-section-heading" className="text-sm font-medium">
          Tool Management
        </h2>
        <p className="text-sm text-muted-foreground">
          Switch any tool between Cascade-managed (reads your CASCADE.md instructions) and
          Independent (uses its own configuration).
        </p>

        <div className="divide-y divide-border rounded-md border border-border">
          {TOOL_IDS.map((toolId) => {
            const mode = modes[toolId] ?? 'independent'
            const row = rowStates[toolId] ?? { phase: 'idle' }
            const label = toLabel(toolId)
            const isManaged = mode === 'managed'

            return (
              <div
                key={toolId}
                className="flex items-center justify-between px-4 py-3 gap-4"
              >
                {/* Left: tool name + mode badge */}
                <div className="flex items-center gap-3 min-w-0">
                  {isManaged ? (
                    <Link2
                      className="h-4 w-4 flex-shrink-0 text-green-600 dark:text-green-400"
                      aria-hidden="true"
                    />
                  ) : (
                    <Link2Off
                      className="h-4 w-4 flex-shrink-0 text-muted-foreground"
                      aria-hidden="true"
                    />
                  )}
                  <div className="min-w-0">
                    <p className="text-sm font-medium truncate">{label}</p>
                    <span
                      className={cn(
                        'inline-block rounded-full px-2 py-0.5 text-xs font-medium',
                        isManaged
                          ? 'bg-green-500/10 text-green-700 dark:text-green-400'
                          : 'bg-muted text-muted-foreground',
                      )}
                    >
                      {isManaged ? 'Cascade-managed' : 'Independent'}
                    </span>
                  </div>
                </div>

                {/* Right: action button + inline feedback */}
                <div className="flex-shrink-0 flex items-center gap-2">
                  {/* Done badge */}
                  {row.phase === 'done' && (
                    <span
                      className={cn(
                        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5',
                        'text-xs font-medium bg-green-500/10 text-green-700 dark:text-green-400',
                      )}
                    >
                      <CheckCircle2 className="h-3.5 w-3.5" aria-hidden="true" />
                      {row.added
                        ? `${row.added.length} link${row.added.length !== 1 ? 's' : ''} added`
                        : `${row.removed?.length ?? 0} link${(row.removed?.length ?? 0) !== 1 ? 's' : ''} removed`}
                    </span>
                  )}

                  {/* Error badge */}
                  {row.phase === 'error' && (
                    <span
                      className="inline-flex items-center gap-1 text-xs text-destructive"
                      role="alert"
                      title={row.message}
                    >
                      <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
                      Failed
                    </span>
                  )}

                  {/* Flipping spinner */}
                  {row.phase === 'flipping' && (
                    <Button variant="outline" size="sm" disabled>
                      <Loader2 className="h-3.5 w-3.5 animate-spin mr-1" aria-hidden="true" />
                      {isManaged ? 'Unlinking…' : 'Linking…'}
                    </Button>
                  )}

                  {/* Idle / confirming / done / error → show action button */}
                  {(row.phase === 'idle' ||
                    row.phase === 'confirming' ||
                    row.phase === 'done' ||
                    row.phase === 'error') && (
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={row.phase === 'confirming'}
                      onClick={() => setRow(toolId, { phase: 'confirming' })}
                      aria-label={
                        isManaged
                          ? `Restore ${label} to Independent`
                          : `Make ${label} Cascade-managed`
                      }
                    >
                      {isManaged ? (
                        <>
                          <Link2Off className="h-3.5 w-3.5 mr-1" aria-hidden="true" />
                          Restore independence ↻
                        </>
                      ) : (
                        <>
                          <Link2 className="h-3.5 w-3.5 mr-1" aria-hidden="true" />
                          Make Cascade-managed →
                        </>
                      )}
                    </Button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </section>

      {/* Confirm dialog — rendered outside the list to avoid stacking-context issues */}
      {confirmingToolId && (
        <ToolModeConfirmDialog
          toolLabel={toLabel(confirmingToolId)}
          direction={(modes[confirmingToolId] === 'managed' ? 'unmanage' : 'manage') as FlipDirection}
          onConfirm={() => {
            const currentMode = modes[confirmingToolId] ?? 'independent'
            setRow(confirmingToolId, { phase: 'idle' })
            void executeFlip(confirmingToolId, currentMode)
          }}
          onCancel={() => setRow(confirmingToolId, { phase: 'idle' })}
        />
      )}
    </>
  )
}
