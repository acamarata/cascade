/**
 * Purpose: Accounts management page — data-rich quota table across every
 *   provider account, with refresh, add-account, re-auth, remove, and a
 *   per-account detail drawer (Epic 1, T1.2).
 * Inputs:  None (self-contained; reads IPC via useAccounts).
 * Outputs: Full table page at /accounts.
 * Constraints:
 *   - Auto-polls every 30 s (via useAccounts).
 *   - Color coding green/yellow/red per widget convention.
 *   - Re-auth shown only for claude/gemini; remove confirms first.
 *   - Add-account is informational for now (shows the terminal command).
 * SPORT: MASTER-ROUTES.md — /accounts (T1.2)
 */

import React, { useCallback, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { RefreshCw, Users } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { useAccounts } from '@/features/accounts/useAccounts'
import { AccountDetailDrawer } from '@/features/accounts/AccountDetailDrawer'
import {
  accountLabel,
  canReauth,
  gfpCapacity,
  isGfpPool,
  pct,
  statusColor,
  utilColor,
  type AccountQuota,
} from '@/features/accounts/types'

/** Provider options for the add-account dialog. */
const PROVIDERS = [
  { id: 'claude', name: 'Claude', cmd: 'cascade-reauth <new-account-id>' },
  { id: 'codex', name: 'Codex', cmd: 'cascade-codex auth' },
  { id: 'gemini', name: 'Gemini', cmd: 'cascade-agy-auth' },
  { id: 'opencode', name: 'OpenCode', cmd: 'cascade-opencode auth' },
  { id: 'gfp', name: 'GFP (pool)', cmd: 'cascade-gemini pool add' },
] as const

export function AccountsPage(): React.ReactElement {
  const { accounts, loading, error, refetch } = useAccounts()
  const [selected, setSelected] = useState<AccountQuota | null>(null)
  const [addOpen, setAddOpen] = useState(false)
  const [addProvider, setAddProvider] = useState<string>('claude')
  const [actionBusy, setActionBusy] = useState(false)
  const [actionMsg, setActionMsg] = useState<string | null>(null)

  const handleRefresh = useCallback(async () => {
    setActionMsg(null)
    try {
      await invoke('accounts_refresh')
    } catch (err) {
      setActionMsg(err instanceof Error ? err.message : String(err))
    }
    // Daemon writes quota.json shortly after; re-fetch after a short delay.
    setTimeout(() => {
      void refetch()
    }, 2000)
  }, [refetch])

  const handleReauth = useCallback(
    async (accountId: string) => {
      setActionBusy(true)
      setActionMsg(null)
      try {
        await invoke('account_reauth', { accountId })
        setActionMsg(`Re-auth completed for ${accountId}.`)
        await refetch()
      } catch (err) {
        setActionMsg(err instanceof Error ? err.message : String(err))
      } finally {
        setActionBusy(false)
      }
    },
    [refetch]
  )

  const handleRemove = useCallback(
    async (accountId: string) => {
      if (!window.confirm(`Remove account "${accountId}" from the roster? Auth tokens are kept.`)) {
        return
      }
      setActionBusy(true)
      setActionMsg(null)
      try {
        await invoke('account_remove', { accountId })
        setActionMsg(`Removed ${accountId} from the roster.`)
        setSelected(null)
        await refetch()
      } catch (err) {
        setActionMsg(err instanceof Error ? err.message : String(err))
      } finally {
        setActionBusy(false)
      }
    },
    [refetch]
  )

  const selectedProvider = PROVIDERS.find((p) => p.id === addProvider) ?? PROVIDERS[0]

  return (
    <main className="flex-1 space-y-4 p-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Users className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
          <h1 className="text-2xl font-semibold">Accounts</h1>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="sm" onClick={() => setAddOpen(true)}>
            Add Account
          </Button>
          <Button variant="outline" size="sm" onClick={handleRefresh}>
            <RefreshCw className="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />
            Refresh
          </Button>
        </div>
      </div>

      {/* Action message */}
      {actionMsg && (
        <div className="rounded-md border border-border bg-muted/40 p-2 text-sm" role="status">
          {actionMsg}
        </div>
      )}

      {/* Error banner */}
      {error && (
        <div
          role="alert"
          className="rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive"
        >
          {error.includes('daemon') || error.includes('running')
            ? 'Cascade daemon is not running. Start it to see account data.'
            : error}
        </div>
      )}

      {/* Loading */}
      {loading && accounts.length === 0 && (
        <p className="py-10 text-center text-sm text-muted-foreground">Loading accounts…</p>
      )}

      {/* Empty */}
      {!loading && !error && accounts.length === 0 && (
        <div className="flex flex-col items-center justify-center py-16 text-center">
          <Users className="mb-3 h-10 w-10 text-muted-foreground/40" aria-hidden="true" />
          <p className="text-sm text-muted-foreground">
            No accounts found. Add one to start tracking quota.
          </p>
        </div>
      )}

      {/* Table */}
      {accounts.length > 0 && (
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-border bg-muted/30 text-left text-xs text-muted-foreground">
                <th className="px-3 py-2 font-medium">Label</th>
                <th className="px-3 py-2 font-medium">Provider</th>
                <th className="px-3 py-2 text-right font-medium">5h%</th>
                <th className="px-3 py-2 text-right font-medium">Wk%</th>
                <th className="px-3 py-2 text-right font-medium">M Credit%</th>
                <th className="px-3 py-2 font-medium">Reset 5h</th>
                <th className="px-3 py-2 font-medium">Reset Wk</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-3 py-2 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {accounts.map((acc) => {
                const u = acc.usage
                const fiveH = u?.five_hour?.utilization
                const wk = u?.seven_day?.utilization
                const credit = u?.extra_usage?.is_enabled ? u?.extra_usage?.utilization : null
                const pool = isGfpPool(acc)

                return (
                  <tr
                    key={acc.account}
                    className={`border-b border-border/50 last:border-0 hover:bg-accent/30 ${pool ? 'opacity-60' : 'cursor-pointer'}`}
                    onClick={pool ? undefined : () => setSelected(acc)}
                  >
                    <td className="px-3 py-2">
                      <span
                        className={`rounded px-1.5 py-0.5 text-xs font-semibold ${pool ? 'bg-muted text-muted-foreground' : 'bg-primary text-primary-foreground'}`}
                      >
                        {accountLabel(acc)}
                      </span>
                    </td>
                    <td className="px-3 py-2 text-muted-foreground">{acc.provider}</td>

                    {pool ? (
                      /* GP pool row — spans quota columns with capacity summary */
                      <td
                        colSpan={5}
                        className="px-3 py-2 text-xs text-muted-foreground/70 italic"
                        title="Gemini Flash free tier · 1500 req/day · 15 RPM per key · round-robin"
                      >
                        {gfpCapacity(acc.key_count)}
                      </td>
                    ) : (
                      <>
                        <td className={`px-3 py-2 text-right font-medium ${utilColor(fiveH)}`}>
                          {pct(fiveH)}
                        </td>
                        <td className={`px-3 py-2 text-right font-medium ${utilColor(wk)}`}>
                          {pct(wk)}
                        </td>
                        <td className={`px-3 py-2 text-right font-medium ${utilColor(credit)}`}>
                          {credit == null ? '—' : pct(credit)}
                        </td>
                        <td className="px-3 py-2 text-xs text-muted-foreground">
                          {u?.five_hour?.resets_in ?? '—'}
                        </td>
                        <td className="px-3 py-2 text-xs text-muted-foreground">
                          {u?.seven_day?.resets_in ?? '—'}
                        </td>
                      </>
                    )}

                    <td className="px-3 py-2">
                      {pool ? (
                        <span className="rounded px-1.5 py-0.5 text-xs font-medium bg-slate-500/15 text-slate-400">
                          pool
                        </span>
                      ) : (
                        <span
                          className={`rounded px-1.5 py-0.5 text-xs font-medium ${statusColor(acc.status)}`}
                        >
                          {acc.status ?? 'unknown'}
                        </span>
                      )}
                    </td>
                    <td className="px-3 py-2" onClick={(e) => e.stopPropagation()}>
                      {!pool && (
                        <div className="flex gap-1.5">
                          {canReauth(acc.provider) && (
                            <Button
                              variant="ghost"
                              size="sm"
                              disabled={actionBusy}
                              onClick={() => handleReauth(acc.account)}
                            >
                              Re-auth
                            </Button>
                          )}
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={actionBusy}
                            onClick={() => handleRemove(acc.account)}
                          >
                            Remove
                          </Button>
                        </div>
                      )}
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {/* Detail drawer */}
      <AccountDetailDrawer
        account={selected}
        onClose={() => setSelected(null)}
        onReauth={handleReauth}
        onRemove={handleRemove}
        busy={actionBusy}
      />

      {/* Add-account dialog */}
      <Dialog open={addOpen} onOpenChange={setAddOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add account</DialogTitle>
            <DialogDescription>
              Pick a provider, then run the auth command in your terminal. Full in-app auth is
              coming in a later release.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2">
              {PROVIDERS.map((p) => (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => setAddProvider(p.id)}
                  className={`rounded-md border px-3 py-1.5 text-sm transition-colors ${
                    addProvider === p.id
                      ? 'border-primary bg-primary/10 text-foreground'
                      : 'border-border text-muted-foreground hover:bg-accent/40'
                  }`}
                >
                  {p.name}
                </button>
              ))}
            </div>
            <div className="rounded-md bg-muted/50 p-3">
              <p className="mb-1 text-xs text-muted-foreground">Launch auth in terminal:</p>
              <code className="select-all font-mono text-sm">{selectedProvider.cmd}</code>
            </div>
          </div>
        </DialogContent>
      </Dialog>
    </main>
  )
}
