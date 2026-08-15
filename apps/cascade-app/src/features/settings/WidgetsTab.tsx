/**
 * Purpose: Settings tab for widget position/size configuration.
 *   Edits WidgetsSettings.positions — per-widget display geometry (x, y,
 *   width, height) keyed by widget slug. Add/remove/edit widgets; persists
 *   via update_settings { widgets }.
 * Inputs:  draft WidgetsSettings; saveSection callback.
 * Outputs: <section> with per-widget geometry editor rows; saves via update_settings { widgets }.
 * Constraints:
 *   - Widget slugs are free-form strings keyed in `positions`.
 *   - Geometry fields are integers (logical pixels); width/height must be > 0.
 *   - WCAG 2.1 AA: all inputs labelled; focus ring on all interactive elements.
 * SPORT: MASTER-COMPONENTS.md — WidgetsTab — T-P3-E07-16
 */

import { useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { WidgetsSettings, WidgetGeometry } from '@/types/settings'

// ── Defaults ──────────────────────────────────────────────────────────────────

/** Default geometry for a newly added widget. */
const DEFAULT_GEOMETRY: WidgetGeometry = {
  x: 100,
  y: 100,
  width: 320,
  height: 200,
}

// ── WidgetsTab ─────────────────────────────────────────────────────────────────

export interface WidgetsTabProps {
  draft: WidgetsSettings
  isSaving: boolean
  error: string | null
  onUpdate: (val: WidgetsSettings) => void
  onSave: () => void
  onDiscard: () => void
}

export function WidgetsTab({ draft, isSaving, error, onUpdate, onSave, onDiscard }: WidgetsTabProps) {
  const entries = Object.entries(draft.positions)

  function addWidget(slug: string) {
    const trimmed = slug.trim()
    if (!trimmed) return
    if (draft.positions[trimmed]) return // already exists — no-op
    onUpdate({
      ...draft,
      positions: { ...draft.positions, [trimmed]: { ...DEFAULT_GEOMETRY } },
    })
  }

  function removeWidget(slug: string) {
    const next = { ...draft.positions }
    delete next[slug]
    onUpdate({ ...draft, positions: next })
  }

  function updateGeometry(slug: string, field: keyof WidgetGeometry, value: number) {
    const current = draft.positions[slug]
    if (!current) return
    onUpdate({
      ...draft,
      positions: { ...draft.positions, [slug]: { ...current, [field]: value } },
    })
  }

  return (
    <section aria-labelledby="widgets-tab-heading" className="space-y-4 max-w-2xl">
      <h2 id="widgets-tab-heading" className="text-base font-semibold">
        Widgets
      </h2>
      <p className="text-sm text-muted-foreground">
        Configure display geometry (position and size) for each platform widget. Changes persist to
        settings.json and apply to the running widget on next launch.
      </p>

      {/* Widget geometry editor rows */}
      {entries.length === 0 ? (
        <div className="rounded-md border border-border px-4 py-6 text-center text-sm text-muted-foreground">
          No widgets configured. Add one below.
        </div>
      ) : (
        <div className="space-y-3">
          {entries.map(([slug, geo]) => (
            <div
              key={slug}
              className="rounded-md border border-border px-4 py-3 space-y-2"
              aria-label={`Widget ${slug} configuration`}
            >
              <div className="flex items-center justify-between">
                <span className="text-sm font-mono font-medium">{slug}</span>
                <button
                  type="button"
                  aria-label={`Remove widget ${slug}`}
                  onClick={() => removeWidget(slug)}
                  className="flex items-center justify-center h-7 w-7 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                </button>
              </div>
              <div className="grid grid-cols-4 gap-2">
                <div className="space-y-1">
                  <label htmlFor={`widget-${slug}-x`} className="block text-xs text-muted-foreground">
                    X
                  </label>
                  <Input
                    id={`widget-${slug}-x`}
                    type="number"
                    value={geo.x}
                    aria-label={`X position for widget ${slug}`}
                    onChange={(e) => updateGeometry(slug, 'x', parseInt(e.target.value, 10) || 0)}
                    className="text-xs font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label htmlFor={`widget-${slug}-y`} className="block text-xs text-muted-foreground">
                    Y
                  </label>
                  <Input
                    id={`widget-${slug}-y`}
                    type="number"
                    value={geo.y}
                    aria-label={`Y position for widget ${slug}`}
                    onChange={(e) => updateGeometry(slug, 'y', parseInt(e.target.value, 10) || 0)}
                    className="text-xs font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label
                    htmlFor={`widget-${slug}-width`}
                    className="block text-xs text-muted-foreground"
                  >
                    Width
                  </label>
                  <Input
                    id={`widget-${slug}-width`}
                    type="number"
                    min={1}
                    value={geo.width}
                    aria-label={`Width for widget ${slug}`}
                    onChange={(e) =>
                      updateGeometry(slug, 'width', Math.max(1, parseInt(e.target.value, 10) || 1))
                    }
                    className="text-xs font-mono"
                  />
                </div>
                <div className="space-y-1">
                  <label
                    htmlFor={`widget-${slug}-height`}
                    className="block text-xs text-muted-foreground"
                  >
                    Height
                  </label>
                  <Input
                    id={`widget-${slug}-height`}
                    type="number"
                    min={1}
                    value={geo.height}
                    aria-label={`Height for widget ${slug}`}
                    onChange={(e) =>
                      updateGeometry(slug, 'height', Math.max(1, parseInt(e.target.value, 10) || 1))
                    }
                    className="text-xs font-mono"
                  />
                </div>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Add widget */}
      <AddWidgetRow onAdd={addWidget} />

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save Widgets'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}

// ── Add widget row ─────────────────────────────────────────────────────────────

function AddWidgetRow({ onAdd }: { onAdd: (slug: string) => void }) {
  const [slug, setSlug] = useState('')

  function handleAdd() {
    if (!slug.trim()) return
    onAdd(slug)
    setSlug('')
  }

  return (
    <div className="flex items-end gap-2">
      <div className="flex-1 space-y-1">
        <label htmlFor="new-widget-slug" className="block text-xs text-muted-foreground">
          New widget slug
        </label>
        <Input
          id="new-widget-slug"
          type="text"
          value={slug}
          placeholder="e.g. macos-widget"
          spellCheck={false}
          onChange={(e) => setSlug(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault()
              handleAdd()
            }
          }}
          className="text-xs font-mono"
        />
      </div>
      <Button type="button" variant="outline" size="sm" onClick={handleAdd} disabled={!slug.trim()}>
        <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
        Add widget
      </Button>
    </div>
  )
}