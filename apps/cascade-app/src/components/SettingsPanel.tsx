/**
 * Purpose: Settings panel — theme, locale, update channel, inbox paths, RAG paths.
 * Inputs:  AppConfig loaded via `get_config` Tauri command on mount
 * Outputs: Form that persists each change immediately via `set_config`
 * Constraints:
 *   - No save button — every change persists on blur/change.
 *   - Theme change takes effect immediately via useTheme hook.
 * SPORT: MASTER-COMPONENTS.md — SettingsPanel
 */

import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { useTheme } from '../hooks/useTheme'
import { Sun, Moon, Monitor, RefreshCw } from 'lucide-react'

interface AppConfig {
  theme: 'light' | 'dark' | 'system'
  locale: string
  update_channel: 'stable' | 'beta' | 'nightly'
  inbox_paths: string[]
  rag_index_paths: string[]
  daemon_autostart: boolean
}

export function SettingsPanel() {
  const [config, setConfig] = useState<AppConfig | null>(null)
  const { setTheme } = useTheme()

  useEffect(() => {
    invoke<AppConfig>('get_config').then(setConfig).catch(console.error)
  }, [])

  async function updateConfig(key: keyof AppConfig, value: unknown) {
    try {
      await invoke('set_config', { key, value })
      setConfig((prev) => (prev ? { ...prev, [key]: value } : prev))
      if (key === 'theme') setTheme(value as AppConfig['theme'])
    } catch (err) {
      console.error('Failed to update config:', err)
    }
  }

  if (!config) {
    return (
      <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
        Loading settings…
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-2xl space-y-8">
      <h1 className="text-lg font-semibold">Settings</h1>

      {/* Theme */}
      <section aria-labelledby="theme-heading" className="space-y-3">
        <h2 id="theme-heading" className="text-sm font-medium">
          Appearance
        </h2>
        <div className="flex gap-2" role="group" aria-label="Theme selection">
          {(['light', 'dark', 'system'] as const).map((t) => {
            const Icon = t === 'light' ? Sun : t === 'dark' ? Moon : Monitor
            return (
              <button
                key={t}
                type="button"
                onClick={() => updateConfig('theme', t)}
                className={[
                  'flex items-center gap-2 rounded-md border px-3 py-2 text-sm capitalize transition-colors',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                  config.theme === t
                    ? 'border-primary bg-primary/10 text-primary'
                    : 'border-border bg-background hover:bg-accent',
                ].join(' ')}
                aria-pressed={config.theme === t}
              >
                <Icon className="h-4 w-4" aria-hidden="true" />
                {t}
              </button>
            )
          })}
        </div>
      </section>

      {/* Update channel */}
      <section aria-labelledby="update-heading" className="space-y-3">
        <h2 id="update-heading" className="text-sm font-medium">
          Updates
        </h2>
        <div className="flex items-center gap-3">
          <label htmlFor="update-channel" className="text-sm text-muted-foreground w-32">
            Channel
          </label>
          <select
            id="update-channel"
            value={config.update_channel}
            onChange={(e) =>
              updateConfig('update_channel', e.target.value as AppConfig['update_channel'])
            }
            className="rounded-md border border-border bg-background px-3 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="stable">Stable</option>
            <option value="beta">Beta</option>
            <option value="nightly">Nightly</option>
          </select>
          <button
            type="button"
            className="flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            aria-label="Check for updates now"
          >
            <RefreshCw className="h-4 w-4" aria-hidden="true" />
            Check now
          </button>
        </div>
      </section>

      {/* Daemon autostart */}
      <section aria-labelledby="daemon-heading" className="space-y-3">
        <h2 id="daemon-heading" className="text-sm font-medium">
          Daemon
        </h2>
        <label className="flex cursor-pointer items-center gap-3">
          <input
            type="checkbox"
            checked={config.daemon_autostart}
            onChange={(e) => updateConfig('daemon_autostart', e.target.checked)}
            className="h-4 w-4 rounded border-border focus-visible:ring-2 focus-visible:ring-ring"
          />
          <span className="text-sm">Start daemon automatically on login</span>
        </label>
      </section>

      {/* Locale */}
      <section aria-labelledby="locale-heading" className="space-y-3">
        <h2 id="locale-heading" className="text-sm font-medium">
          Language
        </h2>
        <div className="flex items-center gap-3">
          <label htmlFor="locale-select" className="text-sm text-muted-foreground w-32">
            Locale
          </label>
          <select
            id="locale-select"
            value={config.locale}
            onChange={(e) => updateConfig('locale', e.target.value)}
            className="rounded-md border border-border bg-background px-3 py-1.5 text-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="en-US">English (US)</option>
            <option value="ar-SA">Arabic (Saudi Arabia)</option>
          </select>
        </div>
      </section>
    </div>
  )
}
