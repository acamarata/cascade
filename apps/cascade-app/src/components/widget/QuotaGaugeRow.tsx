/**
 * Purpose: Render a single per-model quota consumption row: model name, tokens used,
 *   cost, and a colour-coded progress bar relative to a configurable estimate.
 * Inputs:  { model, tokensUsed, costUsd }
 * Outputs: A row with model label, "X / Y tokens" text, $X.XX cost, progress bar.
 * Constraints:
 *   - Progress bar = min(tokensUsed / MODEL_QUOTA_ESTIMATE[model] * 100, 100).
 *   - Shows "?" percentage when model is not in the estimate map (no divide-by-zero).
 *   - Colours: bg-green-500 ≤50%, bg-yellow-400 51–80%, bg-red-500 >80%.
 *   - Full Tailwind class strings (not template literals) to prevent purging.
 *   - P4 will make MODEL_QUOTA_ESTIMATE user-configurable; for now it is a static map.
 * SPORT: MASTER-COMPONENTS.md — QuotaGaugeRow
 */

import React from 'react'

/** Default estimated monthly token quota per model (P3 defaults; user-configurable in P4). */
export const MODEL_QUOTA_ESTIMATE: Record<string, number> = {
  'claude-opus-4-8': 2_000_000,
  'claude-sonnet-4-6': 10_000_000,
  'claude-haiku-3-5': 50_000_000,
  'claude-haiku-3': 100_000_000,
  'gemini-1.5-pro': 2_000_000,
  'gemini-1.5-flash': 20_000_000,
  'gemini-2.0-flash': 20_000_000,
  'gpt-4o': 2_000_000,
  'gpt-4o-mini': 20_000_000,
  'qwen2.5-coder:7b': 0, // local — unlimited
}

/** Derive the progress bar width percentage (0–100) and colour class. */
export function getGaugeState(
  tokensUsed: number,
  estimate: number | undefined
): { pct: number | null; colorClass: string } {
  if (!estimate || estimate <= 0) {
    // Unknown or local-unlimited model — no percentage possible.
    return { pct: null, colorClass: 'bg-muted-foreground' }
  }
  const raw = (tokensUsed / estimate) * 100
  const pct = Math.min(raw, 100)
  let colorClass: string
  if (pct > 80) {
    colorClass = 'bg-red-500'
  } else if (pct > 50) {
    colorClass = 'bg-yellow-400'
  } else {
    colorClass = 'bg-green-500'
  }
  return { pct, colorClass }
}

/** Format a token count for display: "8,500,000" or "8.5M". */
function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(0)}K`
  return String(n)
}

export interface QuotaGaugeRowProps {
  /** Model identifier, e.g. "claude-sonnet-4-6" */
  model: string
  /** Tokens consumed this period */
  tokensUsed: number
  /** Cost in USD this period */
  costUsd: number
}

/**
 * A single quota gauge row for one model.
 *
 * Renders model name + token/cost summary + progress bar.
 * Unknown models show "?" in place of a percentage.
 */
export function QuotaGaugeRow({
  model,
  tokensUsed,
  costUsd,
}: QuotaGaugeRowProps): React.ReactElement {
  const estimate = MODEL_QUOTA_ESTIMATE[model]
  const { pct, colorClass } = getGaugeState(tokensUsed, estimate)

  const pctLabel = pct === null ? (estimate === 0 ? 'local' : '?') : `${Math.round(pct)}%`

  const estimateLabel = estimate == null ? '?' : estimate === 0 ? '∞' : formatTokens(estimate)

  return (
    <div className="space-y-1 py-2">
      <div className="flex items-center justify-between text-xs">
        <span className="font-mono text-foreground truncate max-w-[160px]" title={model}>
          {model}
        </span>
        <span className="text-muted-foreground ml-2 shrink-0">
          {formatTokens(tokensUsed)} / {estimateLabel} · ${costUsd.toFixed(2)} · {pctLabel}
        </span>
      </div>

      {/* Progress bar — only shown when estimate is known and > 0 */}
      {estimate !== 0 && (
        <div
          className="h-1.5 w-full rounded-full bg-muted overflow-hidden"
          role="progressbar"
          aria-label={`${model} quota usage`}
          aria-valuenow={pct !== null ? Math.round(pct) : 0}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <div
            className={['h-full rounded-full transition-all', colorClass].join(' ')}
            style={{ width: `${pct !== null ? pct : 0}%` }}
          />
        </div>
      )}
    </div>
  )
}
