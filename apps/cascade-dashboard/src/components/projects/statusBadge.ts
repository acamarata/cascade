/**
 * Purpose: Map a PEWS node status string to a Tailwind class set for its badge, so RepoCardList
 *   and PEWSScaffoldTree render consistent, exhaustive status colors.
 * Inputs: status string (pending | building | done | blocked | failed | anything else).
 * Outputs: a className string for a small status pill.
 * Constraints: Exhaustive over the known status enum; unknown values fall back to zinc (CR-B).
 * SPORT: T-P3-E02-17 status badge color map
 */
export type PewsStatus = 'pending' | 'building' | 'done' | 'blocked' | 'failed'

const STATUS_CLASSES: Record<PewsStatus, string> = {
  pending: 'bg-zinc-500/15 text-zinc-400 border-zinc-500/30',
  building: 'bg-blue-500/15 text-blue-400 border-blue-500/30',
  done: 'bg-green-500/15 text-green-400 border-green-500/30',
  blocked: 'bg-red-500/15 text-red-400 border-red-500/30',
  failed: 'bg-red-500/15 text-red-400 border-red-500/30',
}

const FALLBACK = STATUS_CLASSES.pending

/** Return the badge className for a status; falls back to zinc for unknown values. */
export function statusBadgeClass(status: string): string {
  return STATUS_CLASSES[status as PewsStatus] ?? FALLBACK
}
