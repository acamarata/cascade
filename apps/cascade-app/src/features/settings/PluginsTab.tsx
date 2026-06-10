/**
 * Purpose: Settings tab for installed plugin management.
 *   Shows a list of plugin slugs from settings.plugins.enabled with an enable/disable
 *   switch per plugin. No add UI — plugins are installed via the filesystem.
 * Inputs:  draft PluginsSettings; saveSection callback.
 * Outputs: <section> with per-plugin Switch rows; saves via update_settings partial.
 * Constraints:
 *   - Plugin installation UI is deferred to P4 — this tab only enables/disables.
 *   - Empty state shows a descriptive placeholder.
 *   - Toggle immediately marks dirty; explicit Save required.
 *   - WCAG 2.1 AA: each switch labelled.
 * SPORT: MASTER-COMPONENTS.md — PluginsTab — T-P3-E07-16
 */

import { Button } from '@/components/ui/button'
import type { PluginsSettings } from '@/types/settings'

export interface PluginsTabProps {
  draft: PluginsSettings
  isSaving: boolean
  error: string | null
  onUpdate: (val: PluginsSettings) => void
  onSave: () => void
  onDiscard: () => void
}

export function PluginsTab({
  draft,
  isSaving,
  error,
  onUpdate,
  onSave,
  onDiscard,
}: PluginsTabProps) {
  // Derive full slug list from both enabled[] and config keys.
  const allSlugs = Array.from(
    new Set([...draft.enabled, ...Object.keys(draft.config)]),
  ).sort()

  function toggle(slug: string) {
    const isEnabled = draft.enabled.includes(slug)
    const nextEnabled = isEnabled
      ? draft.enabled.filter((s) => s !== slug)
      : [...draft.enabled, slug]
    onUpdate({ ...draft, enabled: nextEnabled })
  }

  return (
    <section aria-labelledby="plugins-tab-heading" className="space-y-4">
      <h2 id="plugins-tab-heading" className="text-base font-semibold">
        Plugins
      </h2>
      <p className="text-sm text-muted-foreground">
        Plugins are installed via the filesystem. Enable or disable installed plugins here.
      </p>

      {allSlugs.length === 0 ? (
        <div className="rounded-md border border-border px-4 py-6 text-center text-sm text-muted-foreground">
          No plugins installed. Drop a plugin directory into{' '}
          <code className="font-mono text-xs bg-muted px-1 rounded">~/.cascade/plugins/</code>.
        </div>
      ) : (
        <ul className="space-y-2" aria-label="Installed plugins">
          {allSlugs.map((slug) => {
            const enabled = draft.enabled.includes(slug)
            const switchId = `plugin-switch-${slug}`
            return (
              <li
                key={slug}
                className="flex items-center justify-between rounded-md border border-border px-4 py-2.5 gap-4"
              >
                <span id={switchId} className="text-sm font-mono">
                  {slug}
                </span>
                <button
                  type="button"
                  role="switch"
                  aria-checked={enabled}
                  aria-labelledby={switchId}
                  onClick={() => toggle(slug)}
                  className={[
                    'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
                    enabled ? 'bg-primary' : 'bg-input',
                  ].join(' ')}
                >
                  <span
                    className={[
                      'pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform',
                      enabled ? 'translate-x-4' : 'translate-x-0',
                    ].join(' ')}
                  />
                  <span className="sr-only">{enabled ? 'Enabled' : 'Disabled'}</span>
                </button>
              </li>
            )
          })}
        </ul>
      )}

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save Plugins'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}
