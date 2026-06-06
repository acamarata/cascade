/**
 * Purpose: Accessible horizontal progress bar with threshold-based color coding.
 * Inputs: value (0–100 percentage), label (optional aria/display label).
 * Outputs: Colored bar element — green <50%, yellow <80%, red ≥80%.
 * Constraints: Pure Tailwind. Accessible via aria-valuenow/min/max.
 * SPORT: T-P3-E02-13 ProgressBar
 */

interface ProgressBarProps {
  value: number
  label?: string
  className?: string
}

function barColor(pct: number): string {
  if (pct < 50) return 'bg-green-500'
  if (pct < 80) return 'bg-yellow-400'
  return 'bg-red-500'
}

export function ProgressBar({ value, label, className = '' }: ProgressBarProps) {
  const clamped = Math.max(0, Math.min(100, value))
  return (
    <div
      role="progressbar"
      aria-valuenow={clamped}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={label}
      className={`w-full rounded-full bg-muted overflow-hidden h-2 ${className}`}
    >
      <div
        className={`h-full rounded-full transition-all duration-300 ${barColor(clamped)}`}
        style={{ width: `${clamped}%` }}
      />
    </div>
  )
}
