/**
 * Purpose: Per-account ledger panel — collapsible table of completion events
 *   with sortable columns, pagination (50 rows/page), and CSV export.
 * Inputs:  accounts — list of account emails from summary.byAccount;
 *          ledger — AccountLedger | null;
 *          period — current period (passed through to parent for ledger fetch);
 *          selectedAccount — controlled selected email;
 *          onAccountChange — called when user picks a different account;
 *          loading — boolean.
 * Outputs: Collapsible section with account selector, sortable table, pagination,
 *          and a "Export CSV" button that triggers a client-side Blob download.
 * Constraints:
 *   - CSV export is client-side only (Blob + <a download>). No IPC file write.
 *   - Page size is 50 rows.
 *   - Sortable columns: timestamp, model, promptTokens, completionTokens, costUsd.
 *   - Empty state when ledger.entries is empty.
 * SPORT: MASTER-COMPONENTS.md — AccountLedger (usage)
 */

import React, { useState } from 'react'
import { ChevronDown, ChevronUp, Download } from 'lucide-react'
import type { AccountLedger as AccountLedgerType, AccountUsageStat } from '../../types/ipc'

const PAGE_SIZE = 50

type SortCol = 'timestamp' | 'model' | 'promptTokens' | 'completionTokens' | 'costUsd'
type SortDir = 'asc' | 'desc'

interface AccountLedgerProps {
  accounts: AccountUsageStat[]
  ledger: AccountLedgerType | null
  selectedAccount: string
  onAccountChange: (email: string) => void
  loading: boolean
}

function fmtCost(usd: number): string {
  return `$${usd.toFixed(4)}`
}

function fmtTokens(n: number): string {
  return n.toLocaleString()
}

/** Build a CSV string from the ledger entries. */
function buildCsv(ledger: AccountLedgerType): string {
  const header = 'Timestamp,Model,Prompt Tokens,Completion Tokens,Cost USD,Provider,Account'
  const rows = ledger.entries.map((e) =>
    [
      e.timestamp,
      `"${e.model}"`,
      e.promptTokens,
      e.completionTokens,
      e.costUsd.toFixed(6),
      e.provider,
      e.account ?? ledger.account,
    ].join(',')
  )
  return [header, ...rows].join('\n')
}

/** Trigger a CSV download via an invisible anchor tag. */
function downloadCsv(ledger: AccountLedgerType) {
  const csv = buildCsv(ledger)
  const blob = new Blob([csv], { type: 'text/csv;charset=utf-8;' })
  const url = URL.createObjectURL(blob)
  const today = new Date().toISOString().slice(0, 10)
  const a = document.createElement('a')
  a.href = url
  a.download = `usage-${today}.csv`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

/**
 * Per-account completion ledger with sorting, pagination, and CSV export.
 */
export function AccountLedger({
  accounts,
  ledger,
  selectedAccount,
  onAccountChange,
  loading,
}: AccountLedgerProps): React.ReactElement {
  const [open, setOpen] = useState(true)
  const [page, setPage] = useState(0)
  const [sortCol, setSortCol] = useState<SortCol>('timestamp')
  const [sortDir, setSortDir] = useState<SortDir>('desc')

  const entries = ledger?.entries ?? []

  // Sort entries.
  const sorted = [...entries].sort((a, b) => {
    let cmp = 0
    if (sortCol === 'timestamp') cmp = a.timestamp.localeCompare(b.timestamp)
    else if (sortCol === 'model') cmp = a.model.localeCompare(b.model)
    else if (sortCol === 'promptTokens') cmp = a.promptTokens - b.promptTokens
    else if (sortCol === 'completionTokens') cmp = a.completionTokens - b.completionTokens
    else if (sortCol === 'costUsd') cmp = a.costUsd - b.costUsd
    return sortDir === 'asc' ? cmp : -cmp
  })

  const totalPages = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE))
  const currentPage = Math.min(page, totalPages - 1)
  const pageRows = sorted.slice(currentPage * PAGE_SIZE, (currentPage + 1) * PAGE_SIZE)

  function toggleSort(col: SortCol) {
    if (col === sortCol) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortCol(col)
      setSortDir('desc')
    }
    setPage(0)
  }

  function SortIcon({ col }: { col: SortCol }) {
    if (col !== sortCol) return <span className="opacity-30 text-xs">↕</span>
    return <span className="text-xs">{sortDir === 'asc' ? '↑' : '↓'}</span>
  }

  return (
    <section aria-labelledby="ledger-heading" className="rounded-lg border border-border bg-card">
      {/* Collapsible header */}
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center justify-between p-4 text-left"
      >
        <h3 id="ledger-heading" className="text-sm font-semibold">
          Per-Account Ledger
        </h3>
        {open ? (
          <ChevronUp className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronDown className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        )}
      </button>

      {open && (
        <div className="border-t border-border px-4 pb-4 pt-3">
          {/* Account selector + CSV export */}
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <label htmlFor="account-select" className="text-xs text-muted-foreground">
                Account:
              </label>
              <select
                id="account-select"
                value={selectedAccount}
                onChange={(e) => {
                  onAccountChange(e.target.value)
                  setPage(0)
                }}
                className="rounded border border-border bg-background px-2 py-1 text-sm text-foreground"
              >
                <option value="">— select —</option>
                {accounts.map((a) => (
                  <option key={a.email} value={a.email}>
                    {a.email}
                  </option>
                ))}
              </select>
            </div>

            {ledger && ledger.entries.length > 0 && (
              <button
                type="button"
                onClick={() => downloadCsv(ledger)}
                className="flex items-center gap-1.5 rounded border border-border px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
              >
                <Download className="h-3.5 w-3.5" aria-hidden="true" />
                Export CSV
              </button>
            )}
          </div>

          {/* No account selected */}
          {!selectedAccount && (
            <p className="py-4 text-center text-sm text-muted-foreground">
              Select an account to view its completion log.
            </p>
          )}

          {/* Loading */}
          {selectedAccount && loading && (
            <p className="py-4 text-center text-sm text-muted-foreground">Loading…</p>
          )}

          {/* Empty state */}
          {selectedAccount && !loading && entries.length === 0 && (
            <p className="py-4 text-center text-sm text-muted-foreground">
              No entries for this account in the selected period.
            </p>
          )}

          {/* Table */}
          {selectedAccount && !loading && entries.length > 0 && (
            <>
              <div className="overflow-x-auto rounded border border-border">
                <table className="w-full text-sm" aria-label="Account ledger">
                  <thead>
                    <tr className="border-b border-border bg-muted/40">
                      {(
                        [
                          ['timestamp', 'Timestamp'],
                          ['model', 'Model'],
                          ['promptTokens', 'Prompt'],
                          ['completionTokens', 'Completion'],
                          ['costUsd', 'Cost'],
                        ] as [SortCol, string][]
                      ).map(([col, label]) => (
                        <th
                          key={col}
                          scope="col"
                          className="cursor-pointer whitespace-nowrap px-3 py-2 text-left font-medium text-muted-foreground hover:text-foreground"
                          onClick={() => toggleSort(col)}
                        >
                          {label} <SortIcon col={col} />
                        </th>
                      ))}
                      <th
                        scope="col"
                        className="px-3 py-2 text-left font-medium text-muted-foreground"
                      >
                        Provider
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {pageRows.map((e, i) => (
                      <tr
                        key={`${e.timestamp}-${i}`}
                        className="border-b border-border/50 hover:bg-muted/20"
                      >
                        <td className="whitespace-nowrap px-3 py-1.5 tabular-nums text-muted-foreground text-xs">
                          {e.timestamp.replace('T', ' ').slice(0, 19)}
                        </td>
                        <td className="max-w-[160px] truncate px-3 py-1.5">{e.model}</td>
                        <td className="px-3 py-1.5 tabular-nums text-right">
                          {fmtTokens(e.promptTokens)}
                        </td>
                        <td className="px-3 py-1.5 tabular-nums text-right">
                          {fmtTokens(e.completionTokens)}
                        </td>
                        <td className="px-3 py-1.5 tabular-nums text-right">
                          {fmtCost(e.costUsd)}
                        </td>
                        <td className="px-3 py-1.5 text-muted-foreground text-xs">{e.provider}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              {/* Pagination */}
              {totalPages > 1 && (
                <div className="mt-2 flex items-center justify-between text-xs text-muted-foreground">
                  <span>
                    Rows {currentPage * PAGE_SIZE + 1}–
                    {Math.min((currentPage + 1) * PAGE_SIZE, sorted.length)} of {sorted.length}
                  </span>
                  <div className="flex gap-1">
                    <button
                      type="button"
                      disabled={currentPage === 0}
                      onClick={() => setPage((p) => Math.max(0, p - 1))}
                      className="rounded border border-border px-2 py-0.5 hover:bg-accent/50 disabled:opacity-40 disabled:cursor-not-allowed"
                    >
                      Prev
                    </button>
                    <button
                      type="button"
                      disabled={currentPage >= totalPages - 1}
                      onClick={() => setPage((p) => Math.min(totalPages - 1, p + 1))}
                      className="rounded border border-border px-2 py-0.5 hover:bg-accent/50 disabled:opacity-40 disabled:cursor-not-allowed"
                    >
                      Next
                    </button>
                  </div>
                </div>
              )}
            </>
          )}
        </div>
      )}
    </section>
  )
}
