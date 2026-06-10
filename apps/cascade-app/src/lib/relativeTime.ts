/**
 * Purpose: Format a Unix epoch millisecond timestamp as a human-readable relative string
 *   (e.g. "just now", "3 days ago", "2 months ago") without a date-fns dependency.
 * Inputs:  mtime — Unix epoch milliseconds (0 = unknown).
 * Outputs: Locale-style relative string.
 * Constraints: Pure function. No external dependencies. Handles 0/negative gracefully.
 * SPORT: MASTER-COMPONENTS.md — relativeTime util (T-P3-E06-12)
 */

export function formatRelativeTime(mtime: number): string {
  if (!mtime || mtime <= 0) return 'unknown'

  const now = Date.now()
  const diffMs = now - mtime
  if (diffMs < 0) return 'just now'

  const seconds = Math.floor(diffMs / 1000)
  if (seconds < 60) return 'just now'

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} ${minutes === 1 ? 'minute' : 'minutes'} ago`

  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} ${hours === 1 ? 'hour' : 'hours'} ago`

  const days = Math.floor(hours / 24)
  if (days < 30) return `${days} ${days === 1 ? 'day' : 'days'} ago`

  const months = Math.floor(days / 30)
  if (months < 12) return `${months} ${months === 1 ? 'month' : 'months'} ago`

  const years = Math.floor(months / 12)
  return `${years} ${years === 1 ? 'year' : 'years'} ago`
}
