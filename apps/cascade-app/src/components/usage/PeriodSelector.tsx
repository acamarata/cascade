/**
 * Purpose: Navigation control for selecting a usage period (Day / Week / Month)
 *   with previous / next arrows to step through windows.
 * Inputs:  period — current UsagePeriod; onChange — callback on change
 * Outputs: A toolbar with kind tabs + prev/next arrows that calls onChange.
 * Constraints:
 *   - offset must stay in -120..0; arrows clamp at those bounds.
 *   - Pure controlled component — no internal state.
 * SPORT: MASTER-COMPONENTS.md — PeriodSelector (usage)
 */

import React from 'react'
import { ChevronLeft, ChevronRight } from 'lucide-react'
import type { UsagePeriod, UsagePeriodKind } from '../../types/ipc'

interface PeriodSelectorProps {
  period: UsagePeriod
  onChange: (period: UsagePeriod) => void
}

const KINDS: UsagePeriodKind[] = ['day', 'week', 'month']
const KIND_LABELS: Record<UsagePeriodKind, string> = {
  day: 'Day',
  week: 'Week',
  month: 'Month',
}

/**
 * Period navigation toolbar.
 *
 * Renders kind tabs (Day / Week / Month) and prev/next arrow buttons.
 * Clicking a different kind resets offset to 0 (current window).
 */
export function PeriodSelector({ period, onChange }: PeriodSelectorProps): React.ReactElement {
  function setKind(kind: UsagePeriodKind) {
    onChange({ kind, offset: 0 })
  }

  function stepBack() {
    if (period.offset > -120) {
      onChange({ ...period, offset: period.offset - 1 })
    }
  }

  function stepForward() {
    if (period.offset < 0) {
      onChange({ ...period, offset: period.offset + 1 })
    }
  }

  return (
    <div className="flex items-center gap-2" role="group" aria-label="Usage period selector">
      {/* Kind tabs */}
      <div className="flex rounded-md border border-border overflow-hidden">
        {KINDS.map((kind) => (
          <button
            key={kind}
            type="button"
            onClick={() => setKind(kind)}
            aria-pressed={period.kind === kind}
            className={[
              'px-3 py-1 text-sm font-medium transition-colors',
              period.kind === kind
                ? 'bg-primary text-primary-foreground'
                : 'bg-background text-muted-foreground hover:bg-accent/50 hover:text-foreground',
            ].join(' ')}
          >
            {KIND_LABELS[kind]}
          </button>
        ))}
      </div>

      {/* Prev / next arrows */}
      <button
        type="button"
        onClick={stepBack}
        disabled={period.offset <= -120}
        aria-label="Previous period"
        className="flex h-7 w-7 items-center justify-center rounded-md border border-border text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <ChevronLeft className="h-4 w-4" aria-hidden="true" />
      </button>

      <button
        type="button"
        onClick={stepForward}
        disabled={period.offset >= 0}
        aria-label="Next period"
        className="flex h-7 w-7 items-center justify-center rounded-md border border-border text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground disabled:opacity-40 disabled:cursor-not-allowed"
      >
        <ChevronRight className="h-4 w-4" aria-hidden="true" />
      </button>
    </div>
  )
}
