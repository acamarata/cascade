/**
 * Purpose: TemplateBrowser page — browse, filter, preview, apply, and upgrade cascade templates.
 *   Fetches the template catalog from the Tauri `template_list` IPC command on mount.
 *   Fetches upgrade availability via `template_list_upgradeable` (set of ids with newer versions).
 *   Left panel: filter bar + responsive card grid (60% width).
 *   Right panel: metadata header + optional upgrade info + Markdown preview + Apply button (40%).
 *   ApplyDialog: target path + vars form + dry-run preview → template_apply.
 *   UpgradeConfirmDialog: section diff + confirm → template_upgrade.
 *   Toast: shown after successful apply or upgrade.
 * Inputs:  None (self-contained; reads IPC via invoke).
 * Outputs: Two-panel template browser at /templates route.
 * Constraints:
 *   - IPC commands: `template_list`, `template_list_upgradeable`, `template_apply`,
 *     `template_upgrade` (registered by T-P3-E05-15, T-P3-E05-16, T-P3-E05-17).
 *   - All filters combine with AND logic; realtime — no submit needed.
 *   - Keyboard navigation: Tab to grid, arrow keys move between cards, Enter selects.
 *   - Accessible: role=grid on card grid, aria-selected on selected card.
 *   - Apply button opens ApplyDialog (multi-step: path+vars → dry-run → confirm).
 *   - Upgrade button in preview opens UpgradeConfirmDialog (section diff → confirm).
 *   - Non-destructive: dry-run MUST be executed before apply is enabled.
 * SPORT: MASTER-COMPONENTS.md — TemplateBrowser
 */

import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { LayoutGrid } from 'lucide-react'
import type { ApplyResultIpc, TemplateEntryIpc, UpgradeResultIpc } from '../types/templates'
import { TemplateCard } from '../components/template/TemplateCard'
import { TemplateFilterBar, type TemplateFilter } from '../components/template/TemplateFilterBar'
import { TemplatePreview } from '../components/template/TemplatePreview'
import { ApplyDialog, type DryRunPreviewIpc } from '../components/template/ApplyDialog'
import { UpgradeConfirmDialog } from '../components/template/UpgradeConfirmDialog'

const INITIAL_FILTER: TemplateFilter = { search: '', tier: '', stack: '', shape: '' }

// ── Helpers ───────────────────────────────────────────────────────────────────

/**
 * Derives sorted unique stack tags from a template catalog.
 */
export function extractStacks(entries: TemplateEntryIpc[]): string[] {
  const set = new Set<string>()
  for (const e of entries) {
    for (const s of e.stacks) set.add(s)
  }
  return [...set].sort()
}

/**
 * Derives sorted unique project-shape tags from a template catalog.
 */
export function extractShapes(entries: TemplateEntryIpc[]): string[] {
  const set = new Set<string>()
  for (const e of entries) {
    for (const s of e.projectShapes) set.add(s)
  }
  return [...set].sort()
}

/**
 * Applies all filter fields (AND logic) to the template catalog.
 */
export function applyFilter(
  entries: TemplateEntryIpc[],
  filter: TemplateFilter
): TemplateEntryIpc[] {
  const q = filter.search.trim().toLowerCase()
  return entries.filter((e) => {
    if (q && !e.id.toLowerCase().includes(q) && !e.description.toLowerCase().includes(q)) {
      return false
    }
    if (filter.tier && e.tier !== filter.tier) return false
    if (filter.stack && !e.stacks.includes(filter.stack)) return false
    if (filter.shape && !e.projectShapes.includes(filter.shape)) return false
    return true
  })
}

// ── Upgrade state shape ───────────────────────────────────────────────────────

/**
 * Data returned by template_list_upgradeable.
 * Maps template id → { oldVersion, newVersion, addedSections, changedSections, deprecatedSections }
 */
interface UpgradeInfo {
  oldVersion: string
  newVersion: string
  addedSections: string[]
  changedSections: string[]
  deprecatedSections: string[]
  conflictSections: string[]
}

// ── Toast ─────────────────────────────────────────────────────────────────────

interface ToastMsg {
  id: number
  message: string
  variant: 'success' | 'error'
}

let _toastSeq = 0

// ── Sub-components ────────────────────────────────────────────────────────────

function LoadingState(): React.ReactElement {
  return (
    <div
      role="status"
      aria-live="polite"
      aria-label="Loading templates"
      className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center"
    >
      <div
        className="h-8 w-8 animate-spin rounded-full border-2 border-primary border-t-transparent"
        aria-hidden="true"
      />
      <p className="text-sm text-muted-foreground">Loading templates…</p>
    </div>
  )
}

function ErrorState({
  message,
  onRetry,
}: {
  message: string
  onRetry: () => void
}): React.ReactElement {
  return (
    <div
      role="alert"
      className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center"
    >
      <p className="text-sm font-medium text-destructive">{message}</p>
      <button
        type="button"
        onClick={onRetry}
        className="rounded-md border border-border px-3 py-1.5 text-sm text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
      >
        Retry
      </button>
    </div>
  )
}

function EmptyState({ hasFilter }: { hasFilter: boolean }): React.ReactElement {
  return (
    <div
      aria-live="polite"
      className="flex flex-1 flex-col items-center justify-center gap-2 p-8 text-center"
    >
      <LayoutGrid className="h-10 w-10 text-muted-foreground/30" aria-hidden="true" />
      <p className="text-sm text-muted-foreground">
        {hasFilter ? 'No templates match the current filters.' : 'No templates found.'}
      </p>
    </div>
  )
}

function ToastStack({
  toasts,
  onDismiss,
}: {
  toasts: ToastMsg[]
  onDismiss: (id: number) => void
}): React.ReactElement {
  return (
    <div
      aria-live="polite"
      aria-atomic="false"
      className="fixed bottom-4 right-4 z-50 flex flex-col gap-2"
    >
      {toasts.map((t) => (
        <div
          key={t.id}
          role="status"
          className={[
            'flex items-center gap-2 rounded-md border px-4 py-3 text-sm shadow-lg',
            t.variant === 'success'
              ? 'border-green-300 bg-green-50 text-green-900 dark:border-green-700 dark:bg-green-900/20 dark:text-green-200'
              : 'border-destructive bg-destructive/10 text-destructive',
          ].join(' ')}
        >
          <span className="flex-1">{t.message}</span>
          <button
            type="button"
            onClick={() => onDismiss(t.id)}
            aria-label="Dismiss notification"
            className="ml-2 rounded opacity-70 hover:opacity-100"
          >
            ×
          </button>
        </div>
      ))}
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

/**
 * TemplateBrowser page: searchable grid (left) + preview panel (right).
 */
export function TemplateBrowser(): React.ReactElement {
  const [entries, setEntries] = useState<TemplateEntryIpc[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filter, setFilter] = useState<TemplateFilter>(INITIAL_FILTER)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [upgradeMap, setUpgradeMap] = useState<Map<string, UpgradeInfo>>(new Map())

  // ── ApplyDialog state ──
  const [applyDialogOpen, setApplyDialogOpen] = useState(false)
  const [applyEntry, setApplyEntry] = useState<TemplateEntryIpc | null>(null)
  const [dryRunResult, setDryRunResult] = useState<DryRunPreviewIpc | null>(null)
  const [applyResult, setApplyResult] = useState<ApplyResultIpc | null>(null)
  const [applying, setApplying] = useState(false)
  const [applyError, setApplyError] = useState<string | null>(null)

  // ── UpgradeConfirmDialog state ──
  const [upgradeDialogOpen, setUpgradeDialogOpen] = useState(false)
  const [upgradeEntry, setUpgradeEntry] = useState<TemplateEntryIpc | null>(null)
  const [upgrading, setUpgrading] = useState(false)

  // ── Toast ──
  const [toasts, setToasts] = useState<ToastMsg[]>([])

  function pushToast(message: string, variant: 'success' | 'error' = 'success') {
    const id = ++_toastSeq
    setToasts((prev) => [...prev, { id, message, variant }])
    setTimeout(() => setToasts((prev) => prev.filter((t) => t.id !== id)), 5000)
  }

  function dismissToast(id: number) {
    setToasts((prev) => prev.filter((t) => t.id !== id))
  }

  // ── IPC fetch ──

  const fetchTemplates = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const result = await invoke<TemplateEntryIpc[]>('template_list', { filter: {} })
      setEntries(result)

      // template_list_upgradeable is optional — may not be registered in early phases.
      let upgradeableRaw: Record<string, UpgradeInfo> = {}
      try {
        const u = await invoke<Record<string, UpgradeInfo>>('template_list_upgradeable')
        if (u && typeof u === 'object') upgradeableRaw = u
      } catch {
        // Silently ignore — upgrade badges simply won't appear.
      }
      setUpgradeMap(new Map(Object.entries(upgradeableRaw)))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void fetchTemplates()
  }, [fetchTemplates])

  // ── Derived state ──

  const availableStacks = useMemo(() => extractStacks(entries), [entries])
  const availableShapes = useMemo(() => extractShapes(entries), [entries])
  const filtered = useMemo(() => applyFilter(entries, filter), [entries, filter])

  const selectedEntry = useMemo(
    () =>
      filtered.find((e) => e.id === selectedId) ?? entries.find((e) => e.id === selectedId) ?? null,
    [filtered, entries, selectedId]
  )

  const hasFilter =
    filter.search !== '' || filter.tier !== '' || filter.stack !== '' || filter.shape !== ''

  const selectedUpgradeInfo = selectedEntry ? upgradeMap.get(selectedEntry.id) : undefined

  // ── Keyboard navigation across card grid ──

  function handleGridKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    if (filtered.length === 0) return
    const currentIdx = filtered.findIndex((t) => t.id === selectedId)

    if (e.key === 'ArrowDown') {
      e.preventDefault()
      const next = Math.min(currentIdx + 1, filtered.length - 1)
      setSelectedId(filtered[next]?.id ?? null)
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      const prev = Math.max(currentIdx - 1, 0)
      setSelectedId(filtered[prev]?.id ?? null)
    } else if (e.key === 'Escape') {
      setSelectedId(null)
    }
  }

  // ── Apply flow ──

  function handleOpenApply(entry: TemplateEntryIpc) {
    setApplyEntry(entry)
    setDryRunResult(null)
    setApplyResult(null)
    setApplyError(null)
    setApplyDialogOpen(true)
  }

  async function handleApplyInvoke(
    targetPath: string,
    variables: Record<string, string>,
    dryRun: boolean
  ) {
    if (!applyEntry) return
    setApplying(true)
    setApplyError(null)
    try {
      if (dryRun) {
        const result = await invoke<DryRunPreviewIpc>('template_apply', {
          id: applyEntry.id,
          target: targetPath,
          variables,
          dryRun: true,
        })
        setDryRunResult(result)
      } else {
        const result = await invoke<ApplyResultIpc>('template_apply', {
          id: applyEntry.id,
          target: targetPath,
          variables,
          dryRun: false,
        })
        setApplyResult(result)
        pushToast(
          `Applied ${applyEntry.id}: ${result.applied.length} section${result.applied.length !== 1 ? 's' : ''} added.`,
          'success'
        )
        // Refresh template list so upgrade badges update.
        void fetchTemplates()
      }
    } catch (err) {
      setApplyError(err instanceof Error ? err.message : String(err))
    } finally {
      setApplying(false)
    }
  }

  function handleCloseApply() {
    setApplyDialogOpen(false)
    // Defer clearing so closing animation completes.
    setTimeout(() => {
      setApplyEntry(null)
      setDryRunResult(null)
      setApplyResult(null)
      setApplyError(null)
    }, 200)
  }

  // ── Upgrade flow ──

  function handleOpenUpgrade(entry: TemplateEntryIpc) {
    setUpgradeEntry(entry)
    setUpgradeDialogOpen(true)
  }

  async function handleUpgradeConfirm(force: boolean) {
    if (!upgradeEntry) return
    setUpgrading(true)
    try {
      const info = upgradeMap.get(upgradeEntry.id)
      const result = await invoke<UpgradeResultIpc>('template_upgrade', {
        id: upgradeEntry.id,
        target: info?.oldVersion ?? '',
        force,
      })
      setUpgradeDialogOpen(false)
      pushToast(
        `Upgraded ${upgradeEntry.id}: ${result.upgraded.length} updated, ${result.added.length} added, ${result.deprecated.length} deprecated.`,
        'success'
      )
      // Refresh to clear the upgrade badge.
      void fetchTemplates()
    } catch (err) {
      pushToast(err instanceof Error ? err.message : String(err), 'error')
      setUpgradeDialogOpen(false)
    } finally {
      setUpgrading(false)
      setUpgradeEntry(null)
    }
  }

  function handleUpgradeCancel() {
    setUpgradeDialogOpen(false)
    setTimeout(() => setUpgradeEntry(null), 200)
  }

  // ── Render ──

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Page heading (visually hidden for screen readers) */}
      <h1 className="sr-only">Template Browser</h1>

      {/* Filter bar — full width */}
      <TemplateFilterBar
        filter={filter}
        onFilterChange={setFilter}
        availableStacks={availableStacks}
        availableShapes={availableShapes}
      />

      {/* Main content: grid (60%) + preview (40%) */}
      <div className="flex flex-1 overflow-hidden">
        {/* Left: card grid */}
        <section
          aria-label="Template list"
          className="flex w-3/5 flex-col overflow-hidden border-r border-border"
        >
          {loading ? (
            <LoadingState />
          ) : error !== null ? (
            <ErrorState message={error} onRetry={() => void fetchTemplates()} />
          ) : filtered.length === 0 ? (
            <EmptyState hasFilter={hasFilter} />
          ) : (
            <div
              role="grid"
              aria-label="Templates"
              aria-rowcount={filtered.length}
              tabIndex={0}
              onKeyDown={handleGridKeyDown}
              className="flex-1 overflow-y-auto p-3"
            >
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2">
                {filtered.map((entry) => {
                  const upgradeInfo = upgradeMap.get(entry.id)
                  return (
                    <TemplateCard
                      key={entry.id}
                      entry={entry}
                      isSelected={entry.id === selectedId}
                      onClick={() => setSelectedId(entry.id === selectedId ? null : entry.id)}
                      upgradeable={upgradeInfo !== undefined}
                      upgradeToVersion={upgradeInfo?.newVersion}
                    />
                  )
                })}
              </div>
            </div>
          )}
        </section>

        {/* Right: preview panel */}
        <div className="w-2/5 overflow-hidden">
          <TemplatePreview
            selectedEntry={selectedEntry}
            onApply={handleOpenApply}
            upgradeable={selectedUpgradeInfo !== undefined}
            oldVersion={selectedUpgradeInfo?.oldVersion}
            newVersion={selectedUpgradeInfo?.newVersion}
            changedSectionCount={
              selectedUpgradeInfo
                ? selectedUpgradeInfo.addedSections.length +
                  selectedUpgradeInfo.changedSections.length +
                  selectedUpgradeInfo.deprecatedSections.length
                : undefined
            }
            onUpgrade={handleOpenUpgrade}
          />
        </div>
      </div>

      {/* Apply dialog */}
      <ApplyDialog
        open={applyDialogOpen}
        entry={applyEntry}
        onApply={handleApplyInvoke}
        onClose={handleCloseApply}
        dryRunResult={dryRunResult}
        applyResult={applyResult}
        applying={applying}
        error={applyError}
      />

      {/* Upgrade confirm dialog */}
      {upgradeEntry && upgradeMap.has(upgradeEntry.id) && (
        <UpgradeConfirmDialog
          open={upgradeDialogOpen}
          templateId={upgradeEntry.id}
          oldVersion={upgradeMap.get(upgradeEntry.id)!.oldVersion}
          newVersion={upgradeMap.get(upgradeEntry.id)!.newVersion}
          addedSections={upgradeMap.get(upgradeEntry.id)!.addedSections}
          changedSections={upgradeMap.get(upgradeEntry.id)!.changedSections}
          deprecatedSections={upgradeMap.get(upgradeEntry.id)!.deprecatedSections}
          conflictSections={upgradeMap.get(upgradeEntry.id)!.conflictSections}
          upgrading={upgrading}
          onConfirm={handleUpgradeConfirm}
          onCancel={handleUpgradeCancel}
        />
      )}

      {/* Toast notifications */}
      <ToastStack toasts={toasts} onDismiss={dismissToast} />
    </div>
  )
}
