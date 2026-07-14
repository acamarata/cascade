/**
 * Purpose: Settings tab for editing per-model token quota estimates.
 *   Persisted to localStorage via useFleetQuotaConfig. No IPC required.
 * Inputs:  useFleetQuotaConfig hook (localStorage).
 * Outputs: Editable table of Model | Estimate (tokens) rows with inline edit,
 *   Save, and Reset-to-defaults buttons.
 * Constraints:
 *   - No Rust/IPC changes — localStorage only.
 *   - Inline edit: click Edit → show input → click Save to persist.
 *   - Reset reverts all entries to MODEL_QUOTA_ESTIMATE defaults.
 * SPORT: MASTER-COMPONENTS.md — FleetTab (fleet-02)
 */

import { useState } from 'react'
import { useFleetQuotaConfig } from '../../hooks/useFleetQuotaConfig'
import { MODEL_QUOTA_ESTIMATE } from '../../components/widget/QuotaGaugeRow'

/** Format a token count for display: "8.5M", "50K", "0 (local)". */
function formatTokens(n: number): string {
  if (n === 0) return '0 (local/unlimited)'
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(0)}K`
  return String(n)
}

export function FleetTab() {
  const { estimates, setEstimate, reset } = useFleetQuotaConfig()
  const [editingModel, setEditingModel] = useState<string | null>(null)
  const [editValue, setEditValue] = useState('')
  const [saveMsg, setSaveMsg] = useState<string | null>(null)

  const allModels = Object.keys(estimates)

  function startEdit(model: string) {
    setEditingModel(model)
    setEditValue(String(estimates[model] ?? 0))
  }

  function commitEdit(model: string) {
    const parsed = parseInt(editValue, 10)
    if (!isNaN(parsed) && parsed >= 0) {
      setEstimate(model, parsed)
      showSaveMsg('Saved.')
    }
    setEditingModel(null)
    setEditValue('')
  }

  function showSaveMsg(msg: string) {
    setSaveMsg(msg)
    setTimeout(() => setSaveMsg(null), 2000)
  }

  function handleReset() {
    reset()
    setEditingModel(null)
    showSaveMsg('Reset to defaults.')
  }

  const isDefault = (model: string) =>
    MODEL_QUOTA_ESTIMATE[model] !== undefined && estimates[model] === MODEL_QUOTA_ESTIMATE[model]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="text-base font-semibold text-foreground">Fleet Quota Estimates</h2>
          <p className="text-xs text-muted-foreground mt-0.5">
            Per-model token quota estimates used for gauge calculations. Overrides persist in
            browser storage; Reset reverts to built-in defaults.
          </p>
        </div>
        <button
          type="button"
          onClick={handleReset}
          className="shrink-0 rounded-md border border-border px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground hover:border-foreground/30 transition-colors"
        >
          Reset to defaults
        </button>
      </div>

      {saveMsg && (
        <p role="status" aria-live="polite" className="text-xs text-green-500">
          {saveMsg}
        </p>
      )}

      <div className="overflow-x-auto">
        <table className="w-full text-xs border-collapse">
          <thead>
            <tr className="border-b border-border text-muted-foreground">
              <th className="py-2 pr-4 text-left font-medium">Model</th>
              <th className="py-2 pr-4 text-right font-medium">Estimate</th>
              <th className="py-2 text-right font-medium">Action</th>
            </tr>
          </thead>
          <tbody>
            {allModels.map((model) => (
              <tr key={model} className="border-b border-border/50 last:border-0">
                <td className="py-2 pr-4 font-mono text-foreground">{model}</td>
                <td className="py-2 pr-4 text-right tabular-nums">
                  {editingModel === model ? (
                    <input
                      type="number"
                      min={0}
                      value={editValue}
                      onChange={(e) => setEditValue(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') commitEdit(model)
                        if (e.key === 'Escape') {
                          setEditingModel(null)
                          setEditValue('')
                        }
                      }}
                      className="w-32 rounded border border-border bg-background px-2 py-0.5 text-xs text-right text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
                      // eslint-disable-next-line jsx-a11y/no-autofocus
                      autoFocus
                    />
                  ) : (
                    <span
                      className={isDefault(model) ? 'text-muted-foreground' : 'text-foreground'}
                    >
                      {formatTokens(estimates[model] ?? 0)}
                      {!isDefault(model) && (
                        <span className="ml-1 text-[10px] text-yellow-500">(custom)</span>
                      )}
                    </span>
                  )}
                </td>
                <td className="py-2 text-right">
                  {editingModel === model ? (
                    <div className="flex justify-end gap-1">
                      <button
                        type="button"
                        onClick={() => commitEdit(model)}
                        className="rounded px-2 py-0.5 text-[10px] bg-primary text-primary-foreground hover:bg-primary/80 transition-colors"
                      >
                        Save
                      </button>
                      <button
                        type="button"
                        onClick={() => {
                          setEditingModel(null)
                          setEditValue('')
                        }}
                        className="rounded px-2 py-0.5 text-[10px] border border-border text-muted-foreground hover:text-foreground transition-colors"
                      >
                        Cancel
                      </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      onClick={() => startEdit(model)}
                      className="rounded px-2 py-0.5 text-[10px] border border-border text-muted-foreground hover:text-foreground hover:border-foreground/30 transition-colors"
                    >
                      Edit
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
