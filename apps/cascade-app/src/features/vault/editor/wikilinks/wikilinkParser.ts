/**
 * Purpose: Pure-logic helpers for [[wikilink]] parsing — no DOM, no CM6 deps.
 *   Shared between the CodeMirror extension (highlighting + autocomplete) and
 *   the remark plugin (preview rendering).
 * Inputs:  A document string or cursor offset + document string.
 * Outputs: Wikilink ranges; active wikilink prefix at a cursor position.
 * Constraints: No side-effects; no imports from CM6 or React.
 * SPORT: MASTER-COMPONENTS.md — wikilinkParser (T-P3-E06-05)
 */

/** A matched wikilink span within a document. */
export interface WikilinkRange {
  /** Start offset (inclusive), points to the first `[`. */
  from: number
  /** End offset (exclusive), points just past the closing `]`. */
  to: number
  /** Inner note name (contents between `[[` and `]]`). */
  noteName: string
}

/**
 * Extract all `[[...]]` wikilink ranges from `text`.
 * Matches only the basic `[[NoteName]]` form (no aliasing, no nested brackets).
 */
export function findWikilinks(text: string): WikilinkRange[] {
  const result: WikilinkRange[] = []
  const re = /\[\[([^\][\r\n]+)\]\]/g
  let m: RegExpExecArray | null
  while ((m = re.exec(text)) !== null) {
    result.push({ from: m.index, to: m.index + m[0].length, noteName: m[1] })
  }
  return result
}

/**
 * If the cursor (0-based `pos`) is inside an open `[[...` sequence (the user is
 * actively typing a wikilink that is not yet closed), return the prefix text the
 * user has typed so far (may be empty right after `[[`).
 * Returns `null` when the cursor is not inside an open wikilink.
 *
 * Looks back up to 512 characters to keep cost bounded.
 */
export function activeWikilinkPrefix(text: string, pos: number): string | null {
  const lookback = Math.max(0, pos - 512)
  const slice = text.slice(lookback, pos)
  // Find the last `[[` that was not closed before `pos`
  const lastOpen = slice.lastIndexOf('[[')
  if (lastOpen === -1) return null
  const afterOpen = slice.slice(lastOpen + 2)
  // If there is a `]]` or a lone `]` in the prefix the link was closed already
  if (afterOpen.includes(']]') || afterOpen.includes(']')) return null
  // Wikilinks don't span newlines
  if (afterOpen.includes('\n')) return null
  return afterOpen
}
