/**
 * Purpose: Vault-wide wikilink index — scans all .md files for [[...]] tokens
 *   and builds an in-memory reverse map {targetNoteName -> [sourcePaths]}.
 *   Used by BacklinksPanel to show inbound links for the current open note.
 * Inputs:  Array of {path, content} pairs representing every .md file in the vault.
 * Outputs: Map<string, string[]> — normalised target note name -> source paths.
 * Constraints: Pure function — no React, no DOM, no Tauri imports. Safe for Vitest
 *   without jsdom. In-memory only; persistence is P4 scope.
 *   Duplicate source paths per target are prevented (a file linking twice to the
 *   same target only appears once in the backlinks list).
 * SPORT: MASTER-COMPONENTS.md — linkIndex service (T-P3-E06-06)
 */

import { findWikilinks } from '../../features/vault/editor/wikilinks/wikilinkParser'

// ── Types ─────────────────────────────────────────────────────────────────────

/** A {path, content} pair representing a single vault note. */
export interface LinkIndexFile {
  path: string
  content: string
}

/** A backlink entry: the source note path and a short snippet around the link. */
export interface BacklinkEntry {
  /** Absolute path to the note that contains the wikilink. */
  sourcePath: string
  /**
   * Short surrounding context — the line that contains the [[TargetNote]] token,
   * trimmed and capped at 120 chars for UI display.
   */
  snippet: string
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/**
 * Normalises a wikilink target name for index lookup.
 * Strips a trailing `.md` extension (so [[Note]] and [[Note.md]] resolve to the
 * same key) and lowercases for case-insensitive matching.
 *
 * @param raw  The raw noteName string from the [[...]] token.
 * @returns    Lowercase name without .md suffix.
 */
export function normaliseTarget(raw: string): string {
  const trimmed = raw.trim()
  const withoutExt = trimmed.endsWith('.md') ? trimmed.slice(0, -3) : trimmed
  return withoutExt.toLowerCase()
}

/**
 * Derives the note name key used for backlink lookup from a file path.
 * E.g. "/vault/sub/My Note.md" -> "my note"
 *
 * @param filePath  Absolute path to a .md file.
 * @returns         Lowercase stem (no directory, no .md extension).
 */
export function noteNameFromPath(filePath: string): string {
  const basename = filePath.split('/').pop() ?? filePath
  return normaliseTarget(basename)
}

/**
 * Extracts a short snippet (the containing line) around a wikilink match.
 * Finds the line in `content` that contains the character at offset `from`,
 * trims it, and caps it at 120 characters.
 *
 * @param content  Full note content string.
 * @param from     Character offset of the wikilink opening `[`.
 * @returns        Trimmed line text, capped at 120 chars.
 */
export function extractSnippet(content: string, from: number): string {
  // Walk backward from `from` to find the start of the line.
  let lineStart = from
  while (lineStart > 0 && content[lineStart - 1] !== '\n') {
    lineStart--
  }
  // Walk forward from `from` to find the end of the line.
  let lineEnd = from
  while (lineEnd < content.length && content[lineEnd] !== '\n') {
    lineEnd++
  }
  const line = content.slice(lineStart, lineEnd).trim()
  return line.length > 120 ? line.slice(0, 117) + '…' : line
}

// ── Index builder ─────────────────────────────────────────────────────────────

/**
 * Builds a forward and reverse wikilink index from an array of vault files.
 *
 * For each file:
 *   1. Uses findWikilinks() to extract all [[...]] spans.
 *   2. For each span, normalises the target name and adds the source path +
 *      snippet to the reverse map entry for that target.
 *
 * Deduplication: a source file appears at most once per target (subsequent
 * duplicate wikilinks from the same source add no extra backlink entry, though
 * the first snippet is kept).
 *
 * @param files  Array of {path, content} pairs for every .md note in the vault.
 * @returns      Map<normalisedTargetName, BacklinkEntry[]>
 */
export function buildLinkIndex(files: LinkIndexFile[]): Map<string, BacklinkEntry[]> {
  const index = new Map<string, BacklinkEntry[]>()

  for (const { path, content } of files) {
    const links = findWikilinks(content)
    // Track which targets this source file has already contributed to avoid
    // duplicate backlink entries from the same source.
    const seenTargets = new Set<string>()

    for (const link of links) {
      const target = normaliseTarget(link.noteName)
      if (seenTargets.has(target)) continue
      seenTargets.add(target)

      const snippet = extractSnippet(content, link.from)
      const entry: BacklinkEntry = { sourcePath: path, snippet }

      const existing = index.get(target)
      if (existing) {
        existing.push(entry)
      } else {
        index.set(target, [entry])
      }
    }
  }

  return index
}
