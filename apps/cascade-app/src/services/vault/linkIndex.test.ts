/**
 * Purpose: Vitest unit tests for linkIndex service.
 *   Covers buildLinkIndex, normaliseTarget, noteNameFromPath, extractSnippet.
 * Inputs:  Inline LinkIndexFile fixtures.
 * Outputs: Test assertions.
 * Constraints: No DOM, no React, no Tauri — pure function tests.
 * SPORT: MASTER-COMPONENTS.md — linkIndex service (T-P3-E06-06)
 */

import { describe, it, expect } from 'vitest'
import {
  buildLinkIndex,
  normaliseTarget,
  noteNameFromPath,
  extractSnippet,
  type LinkIndexFile,
} from './linkIndex'

// ── normaliseTarget ───────────────────────────────────────────────────────────

describe('normaliseTarget', () => {
  it('lowercases a plain note name', () => {
    expect(normaliseTarget('My Note')).toBe('my note')
  })

  it('strips trailing .md extension', () => {
    expect(normaliseTarget('My Note.md')).toBe('my note')
  })

  it('handles lowercase name without extension', () => {
    expect(normaliseTarget('cascade')).toBe('cascade')
  })

  it('trims surrounding whitespace', () => {
    expect(normaliseTarget('  Note  ')).toBe('note')
  })
})

// ── noteNameFromPath ──────────────────────────────────────────────────────────

describe('noteNameFromPath', () => {
  it('extracts lowercase stem from an absolute path', () => {
    expect(noteNameFromPath('/vault/sub/My Note.md')).toBe('my note')
  })

  it('handles a bare filename', () => {
    expect(noteNameFromPath('cascade.md')).toBe('cascade')
  })

  it('handles a path without .md extension', () => {
    expect(noteNameFromPath('/vault/no-ext')).toBe('no-ext')
  })
})

// ── extractSnippet ────────────────────────────────────────────────────────────

describe('extractSnippet', () => {
  it('extracts the full line containing the wikilink', () => {
    const content = 'First line\nSee [[My Note]] for details\nThird line'
    // [[My Note]] starts at index 16 (after "First line\nSee ")
    const from = content.indexOf('[[My Note]]')
    expect(extractSnippet(content, from)).toBe('See [[My Note]] for details')
  })

  it('extracts a single-line document', () => {
    const content = '[[Cascade]] is great'
    expect(extractSnippet(content, 0)).toBe('[[Cascade]] is great')
  })

  it('truncates lines longer than 120 chars with an ellipsis', () => {
    const longLine = 'x'.repeat(50) + '[[Note]]' + 'y'.repeat(80)
    const from = 50
    const result = extractSnippet(longLine, from)
    // Source line is 138 chars; capped at 117 + '…' = 118 chars total
    expect(result.length).toBe(118)
    expect(result.endsWith('…')).toBe(true)
  })

  it('trims leading and trailing whitespace from the line', () => {
    const content = '   [[Note]] with leading spaces   '
    const from = 3
    expect(extractSnippet(content, from)).toBe('[[Note]] with leading spaces')
  })
})

// ── buildLinkIndex ────────────────────────────────────────────────────────────

describe('buildLinkIndex', () => {
  it('returns empty map for empty file list', () => {
    const index = buildLinkIndex([])
    expect(index.size).toBe(0)
  })

  it('builds a reverse index from a single linking file', () => {
    const files: LinkIndexFile[] = [
      { path: '/vault/source.md', content: 'See [[Target Note]] for more.' },
    ]
    const index = buildLinkIndex(files)
    const entries = index.get('target note')
    expect(entries).toBeDefined()
    expect(entries).toHaveLength(1)
    expect(entries![0].sourcePath).toBe('/vault/source.md')
    expect(entries![0].snippet).toContain('[[Target Note]]')
  })

  it('aggregates multiple source files pointing to the same target', () => {
    const files: LinkIndexFile[] = [
      { path: '/vault/a.md', content: 'See [[Shared Target]] here.' },
      { path: '/vault/b.md', content: 'Also [[Shared Target]] is mentioned.' },
    ]
    const index = buildLinkIndex(files)
    const entries = index.get('shared target')
    expect(entries).toHaveLength(2)
    expect(entries!.map((e) => e.sourcePath)).toEqual(['/vault/a.md', '/vault/b.md'])
  })

  it('handles a note that links to multiple different targets', () => {
    const files: LinkIndexFile[] = [
      { path: '/vault/hub.md', content: '[[Alpha]] and [[Beta]] and [[Gamma]]' },
    ]
    const index = buildLinkIndex(files)
    expect(index.get('alpha')?.[0].sourcePath).toBe('/vault/hub.md')
    expect(index.get('beta')?.[0].sourcePath).toBe('/vault/hub.md')
    expect(index.get('gamma')?.[0].sourcePath).toBe('/vault/hub.md')
  })

  it('deduplicates the source file when it links to the same target twice', () => {
    const files: LinkIndexFile[] = [
      { path: '/vault/dup.md', content: '[[Note]] first mention, [[Note]] second mention.' },
    ]
    const index = buildLinkIndex(files)
    const entries = index.get('note')
    // source should appear only once despite two [[Note]] tokens
    expect(entries).toHaveLength(1)
    expect(entries![0].sourcePath).toBe('/vault/dup.md')
  })

  it('normalises [[Note.md]] and [[Note]] to the same target key', () => {
    const files: LinkIndexFile[] = [
      { path: '/vault/a.md', content: 'See [[Target.md]] here.' },
      { path: '/vault/b.md', content: 'See [[Target]] here.' },
    ]
    const index = buildLinkIndex(files)
    const entries = index.get('target')
    expect(entries).toHaveLength(2)
    expect(entries!.map((e) => e.sourcePath)).toEqual(['/vault/a.md', '/vault/b.md'])
  })

  it('does not index files that contain no wikilinks', () => {
    const files: LinkIndexFile[] = [
      { path: '/vault/plain.md', content: 'No links here, just plain text.' },
    ]
    const index = buildLinkIndex(files)
    expect(index.size).toBe(0)
  })

  it('is case-insensitive for the same [[Target]] across different casings', () => {
    const files: LinkIndexFile[] = [
      { path: '/vault/a.md', content: '[[CASCADE]]' },
      { path: '/vault/b.md', content: '[[cascade]]' },
      { path: '/vault/c.md', content: '[[Cascade]]' },
    ]
    const index = buildLinkIndex(files)
    expect(index.get('cascade')).toHaveLength(3)
  })

  it('includes a snippet string in each BacklinkEntry', () => {
    const files: LinkIndexFile[] = [
      { path: '/vault/src.md', content: 'Line one\nSee [[Target]] for info\nLine three' },
    ]
    const index = buildLinkIndex(files)
    const entry = index.get('target')?.[0]
    expect(entry?.snippet).toBe('See [[Target]] for info')
  })
})
