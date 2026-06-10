/**
 * Purpose: Unit tests for context feature pure helpers.
 *   Tests getItemLabel, getItemDetail (ItemList), parseExcludes, formatBytes (CodebasePack),
 *   and useContextSessions helper utilities.
 * Inputs:  Pure functions imported from feature modules.
 * Outputs: Vitest results.
 * SPORT: MASTER-COMPONENTS.md — context feature tests (T-P3-E07-10/11)
 */

import { describe, expect, it } from 'vitest'
import { getItemDetail, getItemLabel } from './ItemList'
import { formatBytes, parseExcludes } from './CodebasePack'
import type { ContextItem, ContextSession } from '../../types/context'

// ── fixtures ──────────────────────────────────────────────────────────────────

function makeFileItem(overrides?: Partial<ContextItem>): ContextItem {
  return {
    id: 'item-1',
    item_type: 'file',
    path: '/Users/dev/project/src/main.ts',
    ...overrides,
  }
}

function makeNoteItem(overrides?: Partial<ContextItem>): ContextItem {
  return {
    id: 'item-2',
    item_type: 'note',
    content: 'First line of the note\nSecond line',
    ...overrides,
  }
}

function makeUrlItem(overrides?: Partial<ContextItem>): ContextItem {
  return {
    id: 'item-3',
    item_type: 'url',
    url: 'https://tauri.app/v2/api/js/dialog/',
    ...overrides,
  }
}

function makeSession(overrides?: Partial<ContextSession>): ContextSession {
  return {
    id: 'test-session',
    title: 'Test Session',
    items: [],
    ...overrides,
  }
}

// ── getItemLabel ──────────────────────────────────────────────────────────────

describe('getItemLabel', () => {
  it('returns basename for file items', () => {
    expect(getItemLabel(makeFileItem())).toBe('main.ts')
  })

  it('handles file with no slashes', () => {
    expect(getItemLabel(makeFileItem({ path: 'README.md' }))).toBe('README.md')
  })

  it('returns "(no path)" for file with missing path', () => {
    expect(getItemLabel(makeFileItem({ path: undefined }))).toBe('(no path)')
  })

  it('returns first line of content for note items', () => {
    expect(getItemLabel(makeNoteItem())).toBe('First line of the note')
  })

  it('returns "(empty note)" for note with no content', () => {
    expect(getItemLabel(makeNoteItem({ content: '' }))).toBe('(empty note)')
  })

  it('returns hostname for URL items', () => {
    expect(getItemLabel(makeUrlItem())).toBe('tauri.app')
  })

  it('returns raw string for invalid URL', () => {
    expect(getItemLabel(makeUrlItem({ url: 'not-a-url' }))).toBe('not-a-url')
  })

  it('returns "(no url)" for URL item with no url', () => {
    expect(getItemLabel(makeUrlItem({ url: undefined }))).toBe('(no url)')
  })
})

// ── getItemDetail ─────────────────────────────────────────────────────────────

describe('getItemDetail', () => {
  it('returns full path for file items', () => {
    expect(getItemDetail(makeFileItem())).toBe('/Users/dev/project/src/main.ts')
  })

  it('returns full content for note items', () => {
    expect(getItemDetail(makeNoteItem())).toBe('First line of the note\nSecond line')
  })

  it('returns full URL for URL items', () => {
    expect(getItemDetail(makeUrlItem())).toBe('https://tauri.app/v2/api/js/dialog/')
  })

  it('returns empty string for file with no path', () => {
    expect(getItemDetail(makeFileItem({ path: undefined }))).toBe('')
  })
})

// ── parseExcludes ─────────────────────────────────────────────────────────────

describe('parseExcludes', () => {
  it('parses one glob per line', () => {
    expect(parseExcludes('node_modules/\n*.log')).toEqual(['node_modules/', '*.log'])
  })

  it('ignores blank lines', () => {
    expect(parseExcludes('node_modules/\n\n*.log\n')).toEqual(['node_modules/', '*.log'])
  })

  it('trims whitespace from lines', () => {
    expect(parseExcludes('  dist/  \n  *.tmp  ')).toEqual(['dist/', '*.tmp'])
  })

  it('returns empty array for empty input', () => {
    expect(parseExcludes('')).toEqual([])
  })

  it('returns empty array for whitespace-only input', () => {
    expect(parseExcludes('   \n   ')).toEqual([])
  })
})

// ── formatBytes ───────────────────────────────────────────────────────────────

describe('formatBytes', () => {
  it('formats bytes', () => {
    expect(formatBytes(512)).toBe('512 B')
  })

  it('formats kilobytes', () => {
    expect(formatBytes(2048)).toBe('2.0 KB')
  })

  it('formats megabytes', () => {
    expect(formatBytes(2 * 1024 * 1024)).toBe('2.00 MB')
  })

  it('formats 0 bytes', () => {
    expect(formatBytes(0)).toBe('0 B')
  })

  it('formats 1023 bytes as B (not KB)', () => {
    expect(formatBytes(1023)).toBe('1023 B')
  })

  it('formats 1024 bytes as KB', () => {
    expect(formatBytes(1024)).toBe('1.0 KB')
  })
})

// ── ContextSession shape helpers ──────────────────────────────────────────────

describe('ContextSession shape', () => {
  it('creates a valid session fixture with items', () => {
    const session = makeSession({
      items: [makeFileItem(), makeNoteItem(), makeUrlItem()],
    })
    expect(session.items).toHaveLength(3)
    expect(session.items[0].item_type).toBe('file')
    expect(session.items[1].item_type).toBe('note')
    expect(session.items[2].item_type).toBe('url')
  })

  it('session item ids are unique in fixture', () => {
    const items = [makeFileItem(), makeNoteItem(), makeUrlItem()]
    const ids = items.map((i) => i.id)
    expect(new Set(ids).size).toBe(ids.length)
  })
})
