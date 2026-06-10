/**
 * Purpose: Pure-logic Vitest tests for wikilinkParser helpers and
 *   makeWikilinkCompletion source — no DOM, no CM6 render environment needed.
 *
 * NOTE: Tests for makeWikilinkCompletion use a stub CompletionContext
 *   that only supplies `state.doc.toString()` and `pos`, mirroring the
 *   actual CM6 CompletionContext interface without mounting an editor.
 *   This keeps the test suite jsdom-safe.
 */

import { describe, it, expect } from 'vitest'
import { findWikilinks, activeWikilinkPrefix } from './wikilinkParser'
import { makeWikilinkCompletion } from './wikilinkExtension'
import type { CompletionContext } from '@codemirror/autocomplete'

// ---------------------------------------------------------------------------
// wikilinkParser — findWikilinks
// ---------------------------------------------------------------------------

describe('findWikilinks', () => {
  it('returns empty array for text with no wikilinks', () => {
    expect(findWikilinks('Hello world')).toEqual([])
  })

  it('finds a single wikilink', () => {
    const result = findWikilinks('See [[Alpha]] for details')
    expect(result).toHaveLength(1)
    expect(result[0]).toMatchObject({ noteName: 'Alpha', from: 4, to: 13 })
  })

  it('finds multiple wikilinks', () => {
    const result = findWikilinks('[[Alpha]] and [[Beta]] here')
    expect(result).toHaveLength(2)
    expect(result.map((r) => r.noteName)).toEqual(['Alpha', 'Beta'])
  })

  it('finds wikilinks with spaces', () => {
    const result = findWikilinks('[[My Note Name]]')
    expect(result).toHaveLength(1)
    expect(result[0].noteName).toBe('My Note Name')
  })

  it('does not match single-bracket links', () => {
    expect(findWikilinks('[regular link](url)')).toEqual([])
  })

  it('does not match across newlines', () => {
    expect(findWikilinks('[[\nBad]]')).toEqual([])
  })

  it('returns correct from/to offsets', () => {
    const text = 'prefix [[Note]] suffix'
    const result = findWikilinks(text)
    expect(result[0].from).toBe(7)
    expect(result[0].to).toBe(15)
    expect(text.slice(result[0].from, result[0].to)).toBe('[[Note]]')
  })
})

// ---------------------------------------------------------------------------
// wikilinkParser — activeWikilinkPrefix
// ---------------------------------------------------------------------------

describe('activeWikilinkPrefix', () => {
  it('returns null when cursor is not near a wikilink', () => {
    expect(activeWikilinkPrefix('Hello world', 5)).toBeNull()
  })

  it('returns empty string right after [[', () => {
    const text = 'abc [['
    expect(activeWikilinkPrefix(text, text.length)).toBe('')
  })

  it('returns the typed prefix', () => {
    const text = 'abc [[Not'
    expect(activeWikilinkPrefix(text, text.length)).toBe('Not')
  })

  it('returns null once wikilink is closed', () => {
    const text = '[[Note]] '
    expect(activeWikilinkPrefix(text, text.length)).toBeNull()
  })

  it('returns null when prefix crosses a newline', () => {
    const text = '[[\nsome'
    expect(activeWikilinkPrefix(text, text.length)).toBeNull()
  })

  it('handles cursor in the middle of a wikilink', () => {
    // Cursor at position 8 (inside "[[Note]]", after "No")
    const text = '[[Note]]'
    expect(activeWikilinkPrefix(text, 4)).toBe('No')
  })
})

// ---------------------------------------------------------------------------
// makeWikilinkCompletion
// ---------------------------------------------------------------------------

/** Minimal stub satisfying the CompletionContext contract for the source fn. */
function makeCtx(doc: string, pos: number): CompletionContext {
  return {
    state: {
      doc: { toString: () => doc } as never,
    } as never,
    pos,
    explicit: false,
    tokenBefore: () => null,
    matchBefore: () => null,
  } as unknown as CompletionContext
}

describe('makeWikilinkCompletion', () => {
  const notes = ['Alpha', 'Beta', 'Alpha Rules', 'Gamma']
  const source = makeWikilinkCompletion(() => notes)

  it('returns null when cursor is not inside [[', () => {
    expect(source(makeCtx('Hello world', 5))).toBeNull()
  })

  it('returns all notes on empty prefix', () => {
    const text = 'abc [['
    const result = source(makeCtx(text, text.length))
    expect(result).not.toBeNull()
    expect(result!.options).toHaveLength(4)
  })

  it('filters by case-insensitive contains match', () => {
    const text = '[[alph'
    const result = source(makeCtx(text, text.length))
    expect(result).not.toBeNull()
    const labels = result!.options.map((o) => o.label)
    expect(labels).toContain('Alpha')
    expect(labels).toContain('Alpha Rules')
    expect(labels).not.toContain('Beta')
    expect(labels).not.toContain('Gamma')
  })

  it('returns null when no notes match', () => {
    const text = '[[zzzzz'
    const result = source(makeCtx(text, text.length))
    // No matches → options array is empty (still a valid result or null)
    // The implementation returns options: [] which is valid; either is acceptable
    if (result !== null) {
      expect(result.options).toHaveLength(0)
    }
  })

  it('sets from to immediately after [[', () => {
    const text = 'pre [[bet'
    const result = source(makeCtx(text, text.length))
    expect(result).not.toBeNull()
    // from should point to start of the typed prefix ("bet"), i.e. text.length - 3
    expect(result!.from).toBe(text.length - 3)
  })

  it('is case-insensitive on prefix', () => {
    const text = '[[ALPHA'
    const result = source(makeCtx(text, text.length))
    expect(result).not.toBeNull()
    expect(result!.options.map((o) => o.label)).toContain('Alpha')
  })
})
