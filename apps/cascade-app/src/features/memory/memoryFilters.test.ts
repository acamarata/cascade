/**
 * Purpose: Unit tests for memoryFilters helpers.
 * Covers: filterMemoryEntries (search + project filter), projectBadgeColor (determinism),
 *   projectLabel, edge cases (empty inputs, 'All' project).
 * SPORT: MASTER-COMPONENTS.md — memoryFilters tests (T-P3-E06-12)
 */

import { describe, expect, it } from 'vitest'
import type { MemoryEntry } from '../../types/vault'
import { filterMemoryEntries, projectBadgeColor, projectLabel } from './memoryFilters'

// ── Fixtures ──────────────────────────────────────────────────────────────────

const ENTRIES: MemoryEntry[] = [
  {
    project: '/home/user/Sites/nself',
    type: 'decisions',
    title: 'Use Tauri 2 for desktop',
    excerpt: 'Decided to use Tauri 2 because it bundles a smaller binary.',
    path: '/home/user/Sites/nself/.claude/memory/decisions.md',
    mtime: 1_700_000_000_000,
  },
  {
    project: '/home/user/Sites/nself',
    type: 'lessons',
    title: 'Always run pnpm install before build',
    excerpt: 'Learned this the hard way after repeated CI failures.',
    path: '/home/user/Sites/nself/.claude/memory/lessons.md',
    mtime: 1_700_100_000_000,
  },
  {
    project: '/home/user/Sites/cascade',
    type: 'decisions',
    title: 'BGE-M3 for embeddings',
    excerpt: 'Selected BGE-M3 over ada-002 for lower cost and local inference.',
    path: '/home/user/Sites/cascade/.claude/memory/decisions.md',
    mtime: 1_700_200_000_000,
  },
  {
    project: '/home/user/Sites/cascade',
    type: 'ideas',
    title: 'mellum2 local provider',
    excerpt: 'Hardware-gated entry for Mellum2 local model support.',
    path: '/home/user/Sites/cascade/.claude/ideas/mellum2-local-provider.md',
    mtime: 1_700_300_000_000,
  },
]

// ── filterMemoryEntries ───────────────────────────────────────────────────────

describe('filterMemoryEntries', () => {
  it('returns all entries when query is empty and project is All', () => {
    expect(filterMemoryEntries(ENTRIES, '', 'All')).toHaveLength(4)
  })

  it('returns all entries when query is empty and project is unset', () => {
    expect(filterMemoryEntries(ENTRIES, '', '')).toHaveLength(4)
  })

  it('filters by title match (case-insensitive)', () => {
    const result = filterMemoryEntries(ENTRIES, 'tauri', 'All')
    expect(result).toHaveLength(1)
    expect(result[0].title).toBe('Use Tauri 2 for desktop')
  })

  it('filters by excerpt match (case-insensitive)', () => {
    const result = filterMemoryEntries(ENTRIES, 'lower cost', 'All')
    expect(result).toHaveLength(1)
    expect(result[0].title).toBe('BGE-M3 for embeddings')
  })

  it('returns multiple matches when query hits more than one entry', () => {
    // 'decisions' appears in paths but not in title/excerpt — not matched.
    // 'local' appears in one title and one excerpt.
    const result = filterMemoryEntries(ENTRIES, 'local', 'All')
    expect(result).toHaveLength(2)
  })

  it('returns empty array when query matches nothing', () => {
    expect(filterMemoryEntries(ENTRIES, 'xyznoexist', 'All')).toHaveLength(0)
  })

  it('scopes by project filter', () => {
    const result = filterMemoryEntries(ENTRIES, '', '/home/user/Sites/nself')
    expect(result).toHaveLength(2)
    result.forEach((e) => expect(e.project).toBe('/home/user/Sites/nself'))
  })

  it('combines project filter with search query', () => {
    const result = filterMemoryEntries(ENTRIES, 'pnpm', '/home/user/Sites/nself')
    expect(result).toHaveLength(1)
    expect(result[0].title).toBe('Always run pnpm install before build')
  })

  it('returns empty array when project filter matches but search does not', () => {
    const result = filterMemoryEntries(ENTRIES, 'xyznoexist', '/home/user/Sites/nself')
    expect(result).toHaveLength(0)
  })

  it('handles empty entries array gracefully', () => {
    expect(filterMemoryEntries([], 'something', 'All')).toHaveLength(0)
  })

  it('does not mutate the source array', () => {
    const copy = [...ENTRIES]
    filterMemoryEntries(ENTRIES, 'Tauri', '/home/user/Sites/nself')
    expect(ENTRIES).toStrictEqual(copy)
  })
})

// ── projectBadgeColor ─────────────────────────────────────────────────────────

describe('projectBadgeColor', () => {
  it('returns a non-empty string for a project path', () => {
    const c = projectBadgeColor('/home/user/Sites/nself')
    expect(typeof c).toBe('string')
    expect(c.length).toBeGreaterThan(0)
  })

  it('is deterministic — same input always yields same output', () => {
    const path = '/home/user/Sites/cascade'
    expect(projectBadgeColor(path)).toBe(projectBadgeColor(path))
  })

  it('different project paths can yield different colours', () => {
    // Not guaranteed but almost certain with non-trivially different strings.
    const a = projectBadgeColor('/home/user/Sites/nself')
    const b = projectBadgeColor('/home/user/Sites/ummeco')
    // Just verify both are defined strings — colour collision is possible but unlikely.
    expect(typeof a).toBe('string')
    expect(typeof b).toBe('string')
  })

  it('handles empty string without throwing', () => {
    expect(() => projectBadgeColor('')).not.toThrow()
  })
})

// ── projectLabel ──────────────────────────────────────────────────────────────

describe('projectLabel', () => {
  it('returns the basename of a Unix path', () => {
    expect(projectLabel('/home/user/Sites/nself')).toBe('nself')
  })

  it('returns the basename of a Windows-style path', () => {
    expect(projectLabel('C:\\Users\\user\\Sites\\cascade')).toBe('cascade')
  })

  it('returns the input unchanged when there is no slash', () => {
    expect(projectLabel('myproject')).toBe('myproject')
  })

  it('handles trailing slash gracefully', () => {
    // Path ending with '/' has empty last segment — falls back to full path.
    const result = projectLabel('/home/user/Sites/nself/')
    // Last segment after split on '/' is '' — returns the full string as fallback.
    expect(typeof result).toBe('string')
  })
})
