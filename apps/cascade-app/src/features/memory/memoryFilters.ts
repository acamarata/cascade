/**
 * Purpose: Client-side filter and sort helpers for the MemoryViewer.
 *   filterMemoryEntries: applies search query + project filter to a MemoryEntry[].
 *   projectBadgeColor: deterministic colour bucket for a project path string.
 * Inputs:  entries[], query string, projectFilter string ('All' = no filter).
 * Outputs: filtered MemoryEntry[]; colour class string.
 * Constraints:
 *   - Pure functions — no React, no side effects.
 *   - Case-insensitive search on title + excerpt.
 *   - 'All' project filter returns all entries unmodified.
 *   - Empty query returns all entries (after project filter).
 *   - projectBadgeColor is deterministic: same input always yields same output.
 * SPORT: MASTER-COMPONENTS.md — memoryFilters (T-P3-E06-12)
 */

import type { MemoryEntry } from '../../types/vault'

/**
 * Filter a MemoryEntry array by search query and project.
 *
 * @param entries  - Source array (not mutated).
 * @param query    - Search string; matched case-insensitively on title + excerpt.
 * @param project  - Project filter; 'All' disables project filtering.
 * @returns        New array containing only matching entries.
 */
export function filterMemoryEntries(
  entries: MemoryEntry[],
  query: string,
  project: string,
): MemoryEntry[] {
  let result = entries

  // Project filter.
  if (project && project !== 'All') {
    result = result.filter((e) => e.project === project)
  }

  // Search filter.
  const q = query.trim().toLowerCase()
  if (q) {
    result = result.filter(
      (e) =>
        e.title.toLowerCase().includes(q) ||
        e.excerpt.toLowerCase().includes(q),
    )
  }

  return result
}

/**
 * Map a project path string to one of 8 stable badge colour classes.
 * Uses a simple djb2-style hash so the same project always gets the same colour.
 *
 * @param project - Absolute project root path (or any stable string).
 * @returns A Tailwind bg + text class pair for the project badge.
 */
export function projectBadgeColor(project: string): string {
  let hash = 5381
  for (let i = 0; i < project.length; i++) {
    hash = (hash * 33) ^ project.charCodeAt(i)
    hash = hash >>> 0 // keep 32-bit unsigned
  }
  const COLORS = [
    'bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300',
    'bg-green-100 text-green-800 dark:bg-green-900/40 dark:text-green-300',
    'bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300',
    'bg-amber-100 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
    'bg-rose-100 text-rose-800 dark:bg-rose-900/40 dark:text-rose-300',
    'bg-cyan-100 text-cyan-800 dark:bg-cyan-900/40 dark:text-cyan-300',
    'bg-orange-100 text-orange-800 dark:bg-orange-900/40 dark:text-orange-300',
    'bg-teal-100 text-teal-800 dark:bg-teal-900/40 dark:text-teal-300',
  ] as const
  return COLORS[hash % COLORS.length]
}

/**
 * Derive a short display label from an absolute project root path.
 * Returns the last path segment (basename of the root directory).
 *
 * @param project - Absolute project root path.
 * @returns Short name, e.g. '/Users/x/Sites/nself' → 'nself'.
 */
export function projectLabel(project: string): string {
  const parts = project.replace(/\\/g, '/').split('/')
  const last = parts[parts.length - 1]
  return last || project
}
