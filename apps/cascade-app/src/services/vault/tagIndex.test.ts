/**
 * Purpose: Vitest unit tests for tagIndex service.
 *   Covers buildTagIndex, extractTagsFromFrontmatter, filterNotesByTags,
 *   and tagIndexToEntries helpers.
 * Inputs:  Inline VaultFile fixtures.
 * Outputs: Test assertions.
 * Constraints: No DOM, no React, no Tauri — pure function tests.
 * SPORT: MASTER-COMPONENTS.md — tagIndex service (T-P3-E06-08)
 */

import { describe, it, expect } from 'vitest'
import {
  buildTagIndex,
  extractTagsFromFrontmatter,
  filterNotesByTags,
  tagIndexToEntries,
  type VaultFile,
} from './tagIndex'

// ── extractTagsFromFrontmatter ────────────────────────────────────────────────

describe('extractTagsFromFrontmatter', () => {
  it('returns empty array when tags field is absent', () => {
    expect(extractTagsFromFrontmatter({})).toEqual([])
  })

  it('returns empty array when tags is null', () => {
    expect(extractTagsFromFrontmatter({ tags: null })).toEqual([])
  })

  it('handles array of strings', () => {
    expect(extractTagsFromFrontmatter({ tags: ['Obsidian', 'Test', 'RAG'] })).toEqual([
      'obsidian',
      'test',
      'rag',
    ])
  })

  it('handles comma-separated string', () => {
    expect(extractTagsFromFrontmatter({ tags: 'obsidian, test, rag' })).toEqual([
      'obsidian',
      'test',
      'rag',
    ])
  })

  it('normalises tags to lowercase and trims whitespace', () => {
    expect(extractTagsFromFrontmatter({ tags: ['  AI  ', 'LLM'] })).toEqual(['ai', 'llm'])
  })

  it('deduplicates tags within a single file', () => {
    expect(extractTagsFromFrontmatter({ tags: ['rust', 'rust', 'RUST'] })).toEqual(['rust'])
  })

  it('ignores empty strings after split/trim', () => {
    expect(extractTagsFromFrontmatter({ tags: ',, ,rust, ' })).toEqual(['rust'])
  })

  it('returns empty array for unsupported tag type (number)', () => {
    expect(extractTagsFromFrontmatter({ tags: 42 })).toEqual([])
  })
})

// ── buildTagIndex ─────────────────────────────────────────────────────────────

describe('buildTagIndex', () => {
  it('returns empty map for empty file list', () => {
    const index = buildTagIndex([])
    expect(index.size).toBe(0)
  })

  it('indexes a single file with array tags', () => {
    const files: VaultFile[] = [
      {
        path: '/vault/note-a.md',
        content: '---\ntags: [obsidian, test]\n---\n# Note A',
      },
    ]
    const index = buildTagIndex(files)
    expect(index.get('obsidian')).toEqual(['/vault/note-a.md'])
    expect(index.get('test')).toEqual(['/vault/note-a.md'])
  })

  it('indexes a single file with comma-string tags', () => {
    const files: VaultFile[] = [
      {
        path: '/vault/note-b.md',
        content: '---\ntags: "rust, cascade"\n---\n# Note B',
      },
    ]
    const index = buildTagIndex(files)
    expect(index.get('rust')).toEqual(['/vault/note-b.md'])
    expect(index.get('cascade')).toEqual(['/vault/note-b.md'])
  })

  it('aggregates the same tag across multiple files', () => {
    const files: VaultFile[] = [
      { path: '/vault/a.md', content: '---\ntags: [rust]\n---' },
      { path: '/vault/b.md', content: '---\ntags: [rust, python]\n---' },
      { path: '/vault/c.md', content: '---\ntags: [python]\n---' },
    ]
    const index = buildTagIndex(files)
    expect(index.get('rust')).toEqual(['/vault/a.md', '/vault/b.md'])
    expect(index.get('python')).toEqual(['/vault/b.md', '/vault/c.md'])
  })

  it('skips files with no frontmatter tags', () => {
    const files: VaultFile[] = [
      { path: '/vault/no-tags.md', content: '# Just a heading\nNo frontmatter.' },
    ]
    const index = buildTagIndex(files)
    expect(index.size).toBe(0)
  })

  it('skips files with malformed frontmatter gracefully', () => {
    // gray-matter handles most malformed YAML without throwing;
    // we just verify no crash and empty-tag files are skipped.
    const files: VaultFile[] = [
      { path: '/vault/broken.md', content: '---\n: bad yaml ::\n---\n# Broken' },
    ]
    expect(() => buildTagIndex(files)).not.toThrow()
  })

  it('normalises tags to lowercase in the index', () => {
    const files: VaultFile[] = [
      { path: '/vault/upper.md', content: '---\ntags: [RUST, Cascade]\n---' },
    ]
    const index = buildTagIndex(files)
    expect(index.has('RUST')).toBe(false)
    expect(index.has('rust')).toBe(true)
    expect(index.has('cascade')).toBe(true)
  })

  it('returns tags sorted alphabetically', () => {
    const files: VaultFile[] = [
      { path: '/vault/z.md', content: '---\ntags: [zebra, apple, mango]\n---' },
    ]
    const index = buildTagIndex(files)
    expect([...index.keys()]).toEqual(['apple', 'mango', 'zebra'])
  })
})

// ── tagIndexToEntries ─────────────────────────────────────────────────────────

describe('tagIndexToEntries', () => {
  it('converts a Map to TagEntry array with correct counts', () => {
    const files: VaultFile[] = [
      { path: '/vault/a.md', content: '---\ntags: [rust, cascade]\n---' },
      { path: '/vault/b.md', content: '---\ntags: [rust]\n---' },
    ]
    const index = buildTagIndex(files)
    const entries = tagIndexToEntries(index)

    const cascadeEntry = entries.find((e) => e.tag === 'cascade')
    const rustEntry = entries.find((e) => e.tag === 'rust')

    expect(cascadeEntry?.count).toBe(1)
    expect(rustEntry?.count).toBe(2)
  })

  it('returns empty array for empty index', () => {
    expect(tagIndexToEntries(new Map())).toEqual([])
  })
})

// ── filterNotesByTags ─────────────────────────────────────────────────────────

describe('filterNotesByTags', () => {
  const files: VaultFile[] = [
    { path: '/vault/a.md', content: '---\ntags: [rust, cascade]\n---' },
    { path: '/vault/b.md', content: '---\ntags: [rust, python]\n---' },
    { path: '/vault/c.md', content: '---\ntags: [python]\n---' },
    { path: '/vault/d.md', content: '---\ntags: [cascade]\n---' },
  ]

  it('returns empty set when no tags are selected', () => {
    const index = buildTagIndex(files)
    expect(filterNotesByTags(index, new Set()).size).toBe(0)
  })

  it('filters to notes with a single tag', () => {
    const index = buildTagIndex(files)
    const result = filterNotesByTags(index, new Set(['rust']))
    expect([...result].sort()).toEqual(['/vault/a.md', '/vault/b.md'])
  })

  it('AND-filters two tags — only notes with both tags', () => {
    const index = buildTagIndex(files)
    const result = filterNotesByTags(index, new Set(['rust', 'cascade']))
    expect([...result]).toEqual(['/vault/a.md'])
  })

  it('returns empty set when AND filter has no intersection', () => {
    const index = buildTagIndex(files)
    const result = filterNotesByTags(index, new Set(['rust', 'python', 'cascade']))
    // a.md has rust+cascade but not python; b.md has rust+python but not cascade
    expect(result.size).toBe(0)
  })

  it('returns empty set when a selected tag does not exist', () => {
    const index = buildTagIndex(files)
    const result = filterNotesByTags(index, new Set(['nonexistent']))
    expect(result.size).toBe(0)
  })
})
