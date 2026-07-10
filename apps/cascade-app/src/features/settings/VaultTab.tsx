/**
 * Purpose: Settings tab for read-only vault.env key display.
 *   Reads key names (never values) via read_vault_keys IPC when available; renders masked placeholders.
 *   "Open in Editor" button opens the vault path when the IPC command is available.
 * Inputs:  None (calls IPC on mount for key names only).
 * Outputs: <section> list of key names; Open in Editor button.
 * Constraints:
 *   - NEVER reads or displays vault values — key names only via IPC.
 *   - Shell open via @tauri-apps/plugin-opener; falls back gracefully in browser.
 *   - Refresh button reloads key names without full page remount.
 *   - WCAG 2.1 AA: list roles, button labelled.
 * SPORT: MASTER-COMPONENTS.md — VaultTab — T-P3-E07-16
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { ExternalLink, RefreshCw, KeyRound } from 'lucide-react'
import { Button } from '@/components/ui/button'

// ── IPC helpers ───────────────────────────────────────────────────────────────

// TODO(rust-wave): implement read_vault_keys + vault_path IPC in src-tauri.

function isMissingCommandError(err: unknown): boolean {
  const msg = String(err).toLowerCase()
  return msg.includes('not found') || msg.includes('unknown command') || msg.includes('no such')
}

async function openVaultInEditor() {
  const { openPath } = await import('@tauri-apps/plugin-opener')
  const path = await invoke<string>('vault_path')
  await openPath(path)
}

// ── VaultTab ──────────────────────────────────────────────────────────────────

export function VaultTab() {
  const [keys, setKeys] = useState<string[]>([])
  const [isLoading, setIsLoading] = useState(true)
  const [isOpening, setIsOpening] = useState(false)
  const [integrationPending, setIntegrationPending] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)

  const reload = useCallback(async () => {
    setIsLoading(true)
    setLoadError(null)
    try {
      const result = await invoke<string[]>('read_vault_keys')
      setKeys(result)
      setIntegrationPending(false)
    } catch (err) {
      setKeys([])
      if (isMissingCommandError(err)) {
        setIntegrationPending(true)
      } else {
        setLoadError(String(err))
      }
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  async function handleOpen() {
    setIsOpening(true)
    setLoadError(null)
    try {
      await openVaultInEditor()
      setIntegrationPending(false)
    } catch (err) {
      if (isMissingCommandError(err)) {
        setIntegrationPending(true)
      } else {
        setLoadError(String(err))
      }
    } finally {
      setIsOpening(false)
    }
  }

  return (
    <section aria-labelledby="vault-tab-heading" className="space-y-4 max-w-xl">
      <div className="flex items-center justify-between gap-4">
        <h2 id="vault-tab-heading" className="text-base font-semibold">
          Vault
        </h2>
        <div className="flex gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={reload}
            disabled={isLoading}
            aria-label="Refresh vault keys"
          >
            <RefreshCw
              className={['h-4 w-4', isLoading ? 'animate-spin' : ''].join(' ')}
              aria-hidden="true"
            />
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={handleOpen}
            disabled={isOpening || integrationPending}
          >
            <ExternalLink className="h-4 w-4 mr-1.5" aria-hidden="true" />
            Open in Editor
          </Button>
        </div>
      </div>

      <p className="text-sm text-muted-foreground">
        Variables stored in{' '}
        <code className="font-mono text-xs bg-muted px-1 rounded">vault.env</code>. Values are never
        displayed — key names only.
      </p>

      {isLoading ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : integrationPending ? (
        <div className="rounded-md border border-border px-4 py-6 text-center text-sm text-muted-foreground">
          Vault integration not yet wired to the daemon.
        </div>
      ) : loadError ? (
        <div role="alert" className="rounded-md border border-destructive/30 bg-destructive/10 px-4 py-3 text-sm text-destructive">
          {loadError}
        </div>
      ) : keys.length === 0 ? (
        <div className="rounded-md border border-border px-4 py-6 text-center text-sm text-muted-foreground">
          No vault keys found. Add variables to{' '}
          <code className="font-mono text-xs bg-muted px-1 rounded">~/.claude/vault.env</code>.
        </div>
      ) : (
        <ul className="space-y-1.5" aria-label="Vault key names">
          {keys.map((key) => (
            <li
              key={key}
              className="flex items-center gap-2 rounded-md border border-border px-3 py-2"
            >
              <KeyRound className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span className="text-sm font-mono flex-1">{key}</span>
              <span className="text-xs text-muted-foreground tracking-widest">••••••••</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}
