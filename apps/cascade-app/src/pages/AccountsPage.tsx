/**
 * Purpose: Accounts management page — data-rich quota table across every
 *   provider account, with refresh, add-account, re-auth, remove, and a
 *   per-account detail drawer (Epic 1, T1.2).
 * Inputs:  None (self-contained; reads IPC via useAccounts).
 * Outputs: Full table page at /accounts.
 * Constraints:
 *   - Auto-polls every 4 s (via useAccounts).
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

function getProviderBadgeStyles(provider: string, isPool: boolean): string {
  if (isPool) {
    return 'bg-slate-500/10 text-slate-400 border border-slate-500/20'
  }
  switch (provider?.toLowerCase()) {
    case 'claude':
      return 'bg-amber-500/10 text-amber-500 border border-amber-500/20'
    case 'codex':
      return 'bg-blue-500/10 text-blue-500 border border-blue-500/20'
    case 'gemini':
      return 'bg-violet-500/10 text-violet-500 border border-violet-500/20'
    case 'opencode':
      return 'bg-emerald-500/10 text-emerald-500 border border-emerald-500/20'
    default:
      return 'bg-muted text-muted-foreground border border-border'
  }
}

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
        <div className="overflow-hidden rounded-xl border border-border/50 bg-card/30 shadow-md">
          <table className="w-full text-sm border-collapse">
            <thead>
              <tr className="border-b border-border/60 bg-muted/40 text-left text-xs font-semibold text-muted-foreground/80">
                <th className="px-4 py-3">Label</th>
                <th className="px-4 py-3">Provider</th>
                <th className="px-4 py-3 text-right">5h%</th>
                <th className="px-4 py-3 text-right">Wk%</th>
                <th className="px-4 py-3 text-right">M Credit%</th>
                <th className="px-4 py-3">Reset 5h</th>
                <th className="px-4 py-3">Reset Wk</th>
                <th className="px-4 py-3">Status</th>
                <th className="px-4 py-3">Actions</th>
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
                    className={`border-b border-border/40 last:border-0 hover:bg-muted/20 transition-colors ${pool ? 'opacity-70' : 'cursor-pointer'}`}
                    onClick={pool ? undefined : () => setSelected(acc)}
                  >
                    <td className="px-4 py-3">
                      <span
                        className={`rounded px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider ${getProviderBadgeStyles(acc.provider, pool)}`}
                      >
                        {accountLabel(acc)}
                      </span>
                    </td>
                    <td className="px-4 py-3 text-muted-foreground capitalize font-medium">{acc.provider}</td>

                    {pool ? (
                      /* GP pool row — spans quota columns with capacity summary */
                      <td
                        colSpan={5}
                        className="px-4 py-3 text-xs text-muted-foreground/80 italic font-medium"
                        title="Gemini Flash free tier · 1500 req/day · 15 RPM per key · round-robin"
                      >
                        {gfpCapacity(acc.key_count)}
                      </td>
                    ) : (
                      <>
                        <td className={`px-4 py-3 text-right font-bold tabular-nums ${utilColor(fiveH)}`}>
                          {pct(fiveH)}
                        </td>
                        <td className={`px-4 py-3 text-right font-bold tabular-nums ${utilColor(wk)}`}>
                          {pct(wk)}
                        </td>
                        <td className={`px-4 py-3 text-right font-bold tabular-nums ${utilColor(credit)}`}>
                          {credit == null ? '—' : pct(credit)}
                        </td>
                        <td className="px-4 py-3 text-xs text-muted-foreground/80 tabular-nums font-medium">
                          {u?.five_hour?.resets_in ?? '—'}
                        </td>
                        <td className="px-4 py-3 text-xs text-muted-foreground/80 tabular-nums font-medium">
                          {u?.seven_day?.resets_in ?? '—'}
                        </td>
                      </>
                    )}

                    <td className="px-4 py-3">
                      {pool ? (
                        <span className="rounded px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider bg-slate-500/10 text-slate-400 border border-slate-500/20">
                          pool
                        </span>
                      ) : (
                        <span
                          className={`rounded px-2 py-0.5 text-[10px] font-bold uppercase tracking-wider border ${statusColor(acc.status)}`}
                        >
                          {acc.status ?? 'unknown'}
                        </span>
                      )}
                    </td>
                    <td className="px-4 py-3" onClick={(e) => e.stopPropagation()}>
                      {!pool && (
                        <div className="flex gap-2">
                          {canReauth(acc.provider) && (
                            <Button
                              variant="ghost"
                              size="sm"
                              disabled={actionBusy}
                              onClick={() => handleReauth(acc.account)}
                              className="h-7 px-2.5 text-xs font-medium hover:bg-muted/80"
                            >
                              Re-auth
                            </Button>
                          )}
                          <Button
                            variant="ghost"
                            size="sm"
                            disabled={actionBusy}
                            onClick={() => handleRemove(acc.account)}
                            className="h-7 px-2.5 text-xs font-medium text-destructive hover:text-destructive hover:bg-destructive/10"
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
