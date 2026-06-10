/**
 * Purpose: Animated loading state for Phase 4 AI merge flow.
 *   Shows a progress bar and step labels while the AI merge is running.
 *
 * Inputs:
 *   - toolCount: number of tools being merged (for display).
 *   - fileCount: number of legacy files being merged (for display).
 *
 * Outputs: Full-height loading panel with animated progress and step labels.
 *
 * Constraints:
 *   - No Tauri or WizardContext reads. Pure display component.
 *   - Step labels per spec: "Reading legacy files", "Merging content", "Structuring sections".
 *
 * SPORT: MASTER-COMPONENTS.md — MergeLoadingState — wizard step 4 — ✅
 * Task: T-P3-E03-29
 */

import { useEffect, useState } from 'react'
import { Loader2, GitMerge } from 'lucide-react'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// Motion preference hook
// ---------------------------------------------------------------------------

/** Returns true when the user has opted into reduced motion. */
function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(
    () =>
      typeof window !== 'undefined' &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches,
  )
  useEffect(() => {
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)')
    const handler = (e: MediaQueryListEvent) => setReduced(e.matches)
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [])
  return reduced
}

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface MergeLoadingStateProps {
  /** Number of legacy tools detected from scan. */
  toolCount: number
  /** Number of legacy files to merge. */
  fileCount: number
}

// ---------------------------------------------------------------------------
// Step labels (per spec T-P3-E03-29)
// ---------------------------------------------------------------------------

const LOADING_STEPS = [
  'Reading legacy files',
  'Merging content',
  'Structuring sections',
] as const

const STEP_DURATION_MS = 2_500

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

/**
 * Full-height animated loading state shown while the AI merge is in progress.
 *
 * Cycles through three step labels with a progress bar animation.
 * Non-blocking — the actual progress reflects the fake step cycling,
 * not the real AI call status (since GeminiPool does not stream).
 */
export function MergeLoadingState({ toolCount, fileCount }: MergeLoadingStateProps) {
  const [stepIndex, setStepIndex] = useState(0)
  const reducedMotion = usePrefersReducedMotion()

  // Advance through steps on a timer (visual progress, not real progress)
  useEffect(() => {
    if (stepIndex >= LOADING_STEPS.length - 1) return
    const timer = setTimeout(() => {
      setStepIndex((i) => Math.min(i + 1, LOADING_STEPS.length - 1))
    }, STEP_DURATION_MS)
    return () => clearTimeout(timer)
  }, [stepIndex])

  const progressPct = Math.round(((stepIndex + 1) / LOADING_STEPS.length) * 90)

  return (
    <div
      className="flex h-full flex-col items-center justify-center gap-8 px-8 py-12"
      role="status"
      aria-live="polite"
      aria-label="AI merge in progress"
    >
      {/* Icon */}
      <div className="flex h-16 w-16 items-center justify-center rounded-full bg-primary/10">
        <GitMerge className="h-8 w-8 text-primary" aria-hidden="true" />
      </div>

      {/* Title */}
      <div className="text-center">
        <h2 className="text-xl font-semibold text-foreground">
          Analyzing your configuration…
        </h2>
        <p className="mt-2 text-sm text-muted-foreground">
          Merging{' '}
          <span className="font-medium text-foreground">{fileCount}</span>{' '}
          {fileCount === 1 ? 'file' : 'files'} from{' '}
          <span className="font-medium text-foreground">{toolCount}</span>{' '}
          {toolCount === 1 ? 'tool' : 'tools'}
        </p>
      </div>

      {/* Progress bar */}
      <div className="w-full max-w-sm">
        <div
          className="h-2 w-full rounded-full bg-muted"
          role="progressbar"
          aria-valuenow={progressPct}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <div
            className={cn(
              'h-full rounded-full bg-primary',
              !reducedMotion && 'transition-all duration-1000 ease-in-out',
            )}
            style={{ width: `${progressPct}%` }}
          />
        </div>
      </div>

      {/* Step labels */}
      <div className="flex flex-col items-center gap-2">
        {LOADING_STEPS.map((label, idx) => (
          <div
            key={label}
            className={cn(
              'flex items-center gap-2 text-sm transition-colors',
              idx === stepIndex
                ? 'text-foreground'
                : idx < stepIndex
                  ? 'text-muted-foreground line-through opacity-50'
                  : 'text-muted-foreground opacity-40',
            )}
          >
            {idx === stepIndex ? (
              <Loader2
                className={cn('h-3.5 w-3.5', !reducedMotion && 'animate-spin')}
                aria-hidden="true"
              />
            ) : idx < stepIndex ? (
              <span className="h-3.5 w-3.5 rounded-full bg-primary/30 text-primary flex items-center justify-center text-[9px] font-bold">
                ✓
              </span>
            ) : (
              <span className="h-3.5 w-3.5 rounded-full border border-muted-foreground/30" />
            )}
            {label}
          </div>
        ))}
      </div>
    </div>
  )
}
