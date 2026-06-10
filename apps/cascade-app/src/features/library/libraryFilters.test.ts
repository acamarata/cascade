/**
 * Purpose: Unit tests for library client-side filter helpers.
 * Inputs:  Pure functions from libraryFilters.ts.
 * Outputs: Vitest results verifying search, tag-AND logic, and uniqueTags.
 * SPORT: MASTER-COMPONENTS.md — libraryFilters tests (T-P3-E07-05)
 */

import { describe, it, expect } from 'vitest'
import { filterBySearch, filterByTags, uniqueTags, applyFilters } from './libraryFilters'
import type { LibraryItem } from '../../types/library'

// ── Fixtures ──────────────────────────────────────────────────────────────────

function makeItem(overrides: Partial<LibraryItem> & { id: string }): LibraryItem {
  return {
    id: overrides.id,
    title: overrides.title ?? `Item ${overrides.id}`,
    description: overrides.description ?? '',
    kind: overrides.kind ?? 'prompt',
    content: overrides.content ?? '',
    tags: overrides.tags ?? [],
    harness_targets: overrides.harness_targets ?? [],
  }
}

const ITEMS: LibraryItem[] = [
  makeItem({
    id: 'a',
    title: 'TypeScript refactor',
    description: 'Refactor to TS strict mode',
    tags: ['typescript', 'refactor'],
  }),
  makeItem({
    id: 'b',
    title: 'Write TypeScript tests',
    description: 'Generate vitest unit tests',
    tags: ['typescript', 'testing'],
  }),
  makeItem({
    id: 'c',
    title: 'Code review',
    description: 'Review pull requests carefully',
    tags: ['review'],
  }),
  makeItem({ id: 'd', title: 'Deploy checklist', description: '', tags: [] }),
]

// ── filterBySearch ────────────────────────────────────────────────────────────

describe('filterBySearch', () => {
  it('returns all items when query is empty', () => {
    expect(filterBySearch(ITEMS, '')).toHaveLength(4)
    expect(filterBySearch(ITEMS, '  ')).toHaveLength(4)
  })

  it('matches on title (case-insensitive)', () => {
    const result = filterBySearch(ITEMS, 'typescript')
    expect(result.map((i) => i.id)).toEqual(['a', 'b'])
  })

  it('matches on description (case-insensitive)', () => {
    const result = filterBySearch(ITEMS, 'vitest')
    expect(result.map((i) => i.id)).toEqual(['b'])
  })

  it('returns empty array when no match', () => {
    expect(filterBySearch(ITEMS, 'zzz-no-match')).toHaveLength(0)
  })

  it('is case-insensitive on both title and description', () => {
    // 'Code review': title contains 'review'; description 'Review pull requests carefully' also matches.
    // That is still 1 item. No other item contains 'review' in title or description.
    expect(filterBySearch(ITEMS, 'REVIEW')).toHaveLength(1)
  })
})

// ── filterByTags ──────────────────────────────────────────────────────────────

describe('filterByTags', () => {
  it('returns all items when no tags selected', () => {
    expect(filterByTags(ITEMS, [])).toHaveLength(4)
  })

  it('filters to items containing the selected tag', () => {
    const result = filterByTags(ITEMS, ['typescript'])
    expect(result.map((i) => i.id)).toEqual(['a', 'b'])
  })

  it('applies AND logic — only items with ALL selected tags pass', () => {
    const result = filterByTags(ITEMS, ['typescript', 'refactor'])
    expect(result.map((i) => i.id)).toEqual(['a'])
  })

  it('returns empty array when no item satisfies AND constraint', () => {
    const result = filterByTags(ITEMS, ['typescript', 'review'])
    expect(result).toHaveLength(0)
  })

  it('handles items with no tags', () => {
    const result = filterByTags(ITEMS, ['typescript'])
    expect(result.every((i) => i.tags.includes('typescript'))).toBe(true)
  })
})

// ── uniqueTags ────────────────────────────────────────────────────────────────

describe('uniqueTags', () => {
  it('returns sorted unique tags from item array', () => {
    expect(uniqueTags(ITEMS)).toEqual(['refactor', 'review', 'testing', 'typescript'])
  })

  it('returns empty array for items with no tags', () => {
    const noTagItems = [makeItem({ id: 'x', tags: [] })]
    expect(uniqueTags(noTagItems)).toEqual([])
  })

  it('deduplicates tags across items', () => {
    const tags = uniqueTags(ITEMS)
    expect(new Set(tags).size).toBe(tags.length)
  })
})

// ── applyFilters ──────────────────────────────────────────────────────────────

describe('applyFilters', () => {
  it('passes through all items when no search and no tags', () => {
    expect(applyFilters(ITEMS, '', [])).toHaveLength(4)
  })

  it('applies search and tag filter in combination', () => {
    // search 'typescript' → a, b; then tag 'refactor' → a only
    const result = applyFilters(ITEMS, 'typescript', ['refactor'])
    expect(result.map((i) => i.id)).toEqual(['a'])
  })

  it('returns empty array when combined filters yield no results', () => {
    expect(applyFilters(ITEMS, 'deploy', ['typescript'])).toHaveLength(0)
  })
})

// ── Loading / empty / error state (structural checks) ────────────────────────

describe('filter state shapes', () => {
  it('filterBySearch with empty list returns empty list', () => {
    expect(filterBySearch([], 'foo')).toEqual([])
  })

  it('filterByTags with empty list returns empty list', () => {
    expect(filterByTags([], ['foo'])).toEqual([])
  })

  it('uniqueTags with empty list returns empty list', () => {
    expect(uniqueTags([])).toEqual([])
  })
})
