/**
 * Purpose: Panel UI for the cascade-personal encrypted vault. Lets the user
 *   browse collections, view/add records, and inspect the exposure log.
 * Inputs:  None (standalone; consumes usePersonalEncryptedVault internally).
 * Outputs: Rendered panel with: mode toggle, collection list, record table,
 *   add-record form, and exposure log table.
 * Constraints:
 *   - Tailwind-only styling (no CSS modules, no external component libs).
 *   - TypeScript strict — no `any`, `unknown` narrowed before use.
 *   - JSON textarea input is validated before submit; malformed JSON shows
 *     an inline error without submitting.
 *   - All IPC errors surface in the hook's `error` field, shown in a banner.
 * SPORT: MASTER-COMPONENTS.md — PersonalEncryptedVaultPanel, rag-16-ui
 */

import { useState } from 'react'
import {
  usePersonalEncryptedVault,
  type CascadeMode,
} from './usePersonalEncryptedVault'

// ---------------------------------------------------------------------------
// Sensitivity badge colour mapping
// ---------------------------------------------------------------------------

function sensitivityClass(sensitivity: string): string {
  switch (sensitivity.toLowerCase()) {
    case 'high':
      return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
    case 'medium':
      return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/40 dark:text-yellow-300'
    default:
      return 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300'
  }
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function PersonalEncryptedVaultPanel() {
  const {
    collections,
    records,
    exposureLog,
    selectedCollection,
    mode,
    loading,
    error,
    setMode,
    selectCollection,
    addRecord,
  } = usePersonalEncryptedVault()

  const [jsonInput, setJsonInput] = useState('')
  const [jsonError, setJsonError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)

  // ------------------------------------------------------------------
  // Add-record submit handler
  // ------------------------------------------------------------------

  async function handleAddRecord(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setJsonError(null)
    if (!selectedCollection) return

    let parsed: Record<string, unknown>
    try {
      const raw: unknown = JSON.parse(jsonInput)
      if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
        setJsonError('Payload must be a JSON object, e.g. {"key": "value"}')
        return
      }
      parsed = raw as Record<string, unknown>
    } catch {
      setJsonError('Invalid JSON — check syntax and try again.')
      return
    }

    setSubmitting(true)
    try {
      await addRecord(selectedCollection, parsed)
      setJsonInput('')
    } catch (err: unknown) {
      const msg = err instanceof Error ? err.message : String(err)
      setJsonError(`Save failed: ${msg}`)
    } finally {
      setSubmitting(false)
    }
  }

  // ------------------------------------------------------------------
  // Render
  // ------------------------------------------------------------------

  return (
    <div className="flex h-full overflow-hidden">
      {/* ---------------------------------------------------------------- */}
      {/* Left: collections                                                */}
      {/* ---------------------------------------------------------------- */}
      <div className="w-56 border-r border-border flex flex-col shrink-0">
        {/* Mode toggle */}
        <div className="flex items-center gap-1 p-3 border-b border-border">
          {(['normal', 'personal'] as CascadeMode[]).map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => setMode(m)}
              className={`flex-1 py-1 rounded text-xs font-medium transition-colors capitalize ${
                mode === m
                  ? 'bg-primary text-primary-foreground'
                  : 'bg-muted text-muted-foreground hover:bg-accent hover:text-accent-foreground'
              }`}
            >
              {m}
            </button>
          ))}
        </div>

        {/* Collection list */}
        <div className="flex-1 overflow-auto p-2">
          {collections.length === 0 && !loading && (
            <p className="text-xs text-muted-foreground px-2 py-4">
              No collections found.
            </p>
          )}
          {collections.map((col) => (
            <button
              key={col.name}
              type="button"
              onClick={() => selectCollection(col.name)}
              className={`w-full text-left px-3 py-2 rounded-md mb-1 transition-colors ${
                selectedCollection === col.name
                  ? 'bg-accent text-accent-foreground'
                  : 'hover:bg-accent/50 text-muted-foreground'
              }`}
            >
              <div className="text-sm font-medium truncate">{col.label}</div>
              <span
                className={`inline-block mt-0.5 px-1.5 py-0.5 rounded text-[10px] font-semibold ${sensitivityClass(col.sensitivity)}`}
              >
                {col.sensitivity}
              </span>
            </button>
          ))}
        </div>
      </div>

      {/* ---------------------------------------------------------------- */}
      {/* Right: records + exposure log                                    */}
      {/* ---------------------------------------------------------------- */}
      <div className="flex-1 flex flex-col overflow-hidden">
        {/* Error banner */}
        {error && (
          <div className="mx-4 mt-3 px-3 py-2 rounded bg-destructive/10 text-destructive text-xs">
            {error}
          </div>
        )}

        {/* Loading indicator */}
        {loading && (
          <p className="px-4 pt-3 text-xs text-muted-foreground animate-pulse">
            Loading…
          </p>
        )}

        {!selectedCollection && !loading && (
          <div className="flex-1 flex items-center justify-center text-muted-foreground text-sm">
            Select a collection to view records.
          </div>
        )}

        {selectedCollection && (
          <div className="flex-1 overflow-auto p-4 flex flex-col gap-6">
            {/* -------------------------------------------------------- */}
            {/* Records table                                             */}
            {/* -------------------------------------------------------- */}
            <section>
              <h2 className="text-sm font-semibold mb-2">
                Records — {selectedCollection}
              </h2>
              {records.length === 0 ? (
                <p className="text-xs text-muted-foreground">No records in this collection.</p>
              ) : (
                <div className="overflow-x-auto rounded border border-border">
                  <table className="w-full text-xs">
                    <thead className="bg-muted/50">
                      <tr>
                        <th className="text-left px-3 py-2 font-medium text-muted-foreground w-20">
                          ID
                        </th>
                        <th className="text-left px-3 py-2 font-medium text-muted-foreground">
                          Payload
                        </th>
                        <th className="text-left px-3 py-2 font-medium text-muted-foreground w-36 whitespace-nowrap">
                          Updated
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {records.map((rec) => (
                        <tr
                          key={rec.id}
                          className="border-t border-border hover:bg-accent/30 transition-colors"
                        >
                          <td className="px-3 py-2 font-mono text-muted-foreground">
                            {rec.id.slice(0, 8)}
                          </td>
                          <td className="px-3 py-2 font-mono break-all">
                            {JSON.stringify(rec.payload)}
                          </td>
                          <td className="px-3 py-2 text-muted-foreground whitespace-nowrap">
                            {rec.updated_at}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>

            {/* -------------------------------------------------------- */}
            {/* Add-record form                                           */}
            {/* -------------------------------------------------------- */}
            <section>
              <h2 className="text-sm font-semibold mb-2">Add Record</h2>
              <form onSubmit={(e) => void handleAddRecord(e)} className="flex flex-col gap-2">
                <textarea
                  value={jsonInput}
                  onChange={(e) => setJsonInput(e.target.value)}
                  placeholder='{"key": "value"}'
                  rows={4}
                  className="w-full rounded border border-border bg-background px-3 py-2 text-xs font-mono resize-y focus:outline-none focus:ring-1 focus:ring-primary"
                />
                {jsonError && (
                  <p className="text-xs text-destructive">{jsonError}</p>
                )}
                <button
                  type="submit"
                  disabled={submitting || !jsonInput.trim()}
                  className="self-start px-4 py-1.5 rounded bg-primary text-primary-foreground text-xs font-medium disabled:opacity-50 hover:bg-primary/90 transition-colors"
                >
                  {submitting ? 'Saving…' : 'Submit'}
                </button>
              </form>
            </section>

            {/* -------------------------------------------------------- */}
            {/* Exposure log                                              */}
            {/* -------------------------------------------------------- */}
            <section>
              <h2 className="text-sm font-semibold mb-2">Exposure Log</h2>
              {exposureLog.length === 0 ? (
                <p className="text-xs text-muted-foreground">No exposure events recorded.</p>
              ) : (
                <div className="overflow-x-auto rounded border border-border">
                  <table className="w-full text-xs">
                    <thead className="bg-muted/50">
                      <tr>
                        <th className="text-left px-3 py-2 font-medium text-muted-foreground w-12">
                          #
                        </th>
                        <th className="text-left px-3 py-2 font-medium text-muted-foreground">
                          Exposed to
                        </th>
                        <th className="text-left px-3 py-2 font-medium text-muted-foreground w-24">
                          Item ID
                        </th>
                        <th className="text-left px-3 py-2 font-medium text-muted-foreground w-36 whitespace-nowrap">
                          Exposed at
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {exposureLog.map((entry) => (
                        <tr
                          key={entry.id}
                          className="border-t border-border hover:bg-accent/30 transition-colors"
                        >
                          <td className="px-3 py-2 text-muted-foreground">{entry.id}</td>
                          <td className="px-3 py-2 font-mono">{entry.exposed_to}</td>
                          <td className="px-3 py-2 font-mono text-muted-foreground">
                            {entry.item_id ?? '—'}
                          </td>
                          <td className="px-3 py-2 text-muted-foreground whitespace-nowrap">
                            {entry.exposed_at}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>
          </div>
        )}
      </div>
    </div>
  )
}
