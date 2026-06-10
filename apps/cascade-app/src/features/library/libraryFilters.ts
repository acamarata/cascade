/**
 * Purpose: Pure client-side filter helpers for library items.
 *   Used by LibraryPanel to derive the visible item list from raw items +
 *   active search query + active tag selection.
 * Inputs:  LibraryItem[], searchQuery string, selectedTags string[].
 * Outputs: Filtered LibraryItem[]; uniqueTags string[].
 * Constraints:
 *   - All filtering is case-insensitive.
 *   - Tag filter applies AND logic: item must have ALL selected tags.
 *   - uniqueTags is derived from the items BEFORE search/tag filters so the
 *     full tag palette stays visible while the user narrows results.
 * SPORT: MASTER-LIBS.md — libraryFilters (T-P3-E07-05)
 */

import type { LibraryItem } from '../../types/library'

/**
 * Filter items by search query (title + description, case-insensitive).
 *
 * @param items       Source array.
 * @param query       Search string (empty = no-op).
 * @returns           Filtered array.
 */
export function filterBySearch(items: LibraryItem[], query: string): LibraryItem[] {
  const q = query.trim().toLowerCase()
  if (!q) return items
  return items.filter(
    (item) => item.title.toLowerCase().includes(q) || item.description.toLowerCase().includes(q)
  )
}

/**
 * Filter items by tag selection (AND logic).
 *
 * @param items         Source array.
 * @param selectedTags  Tags that must ALL be present on the item.
 * @returns             Filtered array.
 */
export function filterByTags(items: LibraryItem[], selectedTags: string[]): LibraryItem[] {
  if (selectedTags.length === 0) return items
  return items.filter((item) => selectedTags.every((t) => item.tags.includes(t)))
}

/**
 * Extract sorted unique tags from an item array.
 *
 * @param items  Source array (typically the tab-scoped items before search/tag filter).
 * @returns      Sorted unique tag strings.
 */
export function uniqueTags(items: LibraryItem[]): string[] {
  const set = new Set<string>()
  for (const item of items) {
    for (const tag of item.tags) {
      set.add(tag)
    }
  }
  return Array.from(set).sort()
}

/**
 * Apply both filters in sequence: search then tag.
 * Convenience wrapper used by LibraryPanel.
 */
export function applyFilters(
  items: LibraryItem[],
  searchQuery: string,
  selectedTags: string[]
): LibraryItem[] {
  return filterByTags(filterBySearch(items, searchQuery), selectedTags)
}
