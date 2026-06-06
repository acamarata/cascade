/**
 * Purpose: Full-screen loading spinner used as a React Suspense fallback.
 * Inputs:  label?: string — accessible label; defaults to "Loading"
 * Outputs: Centred animated spinner div suitable for top-level Suspense fallback.
 * Constraints: aria-label on the spinner; role=status so screen readers announce it.
 *              Uses Tailwind animate-spin. No external deps beyond Tailwind.
 * SPORT: MASTER-COMPONENTS.md — LoadingSpinner
 */

import { cn } from '@/lib/utils'

interface LoadingSpinnerProps {
  /** Accessible label announced by screen readers. */
  label?: string
  className?: string
}

export function LoadingSpinner({ label = 'Loading', className }: LoadingSpinnerProps) {
  return (
    <div className={cn('flex min-h-screen items-center justify-center', className)}>
      <div
        role="status"
        aria-label={label}
        className="h-10 w-10 animate-spin rounded-full border-4 border-muted border-t-primary"
      />
    </div>
  )
}

/** Alias used as a React.Suspense fallback prop. */
export const SuspenseFallback = LoadingSpinner
