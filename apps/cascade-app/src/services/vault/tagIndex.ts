/**
 * Purpose: Tag index builder for the vault — extracts frontmatter tags across
 *   all notes and returns a Map<tag, notePath[]> for the TagBrowser panel.
 * Inputs:  Array of {path, content} pairs (raw markdown file text).
 * Outputs: Map<string, string[]> — lowercased trimmed tag -> note paths.
 * Constraints: No React, no DOM. Pure function — safe for Vitest without jsdom.
 *   Tags come from frontmatter `tags` field only (inline #hashtags are P4).
 *   Supports both array format `tags: [a, b]` and comma string `tags: 'a, b'`.
 * SPORT: MASTER-COMPONENTS.md — tagIndex service (T-P3-E06-08)
 */

import matter from 'gray-matter'

// ── Types ────────────────────────────────────────────────────────────────────

/** A {path, content} pair representing a single vault note. */
export interface VaultFile {
  path: string
  content: string
}

/** Tag entry with its note count, used by TagBrowser for display. */
export interface TagEntry {
  tag: string
  count: number
}

// ── Helpers ──────────────────────────────────────────────────────────────────

/**
 * Normalises a raw tag value to a lowercased, trimmed string.
 * Returns null if the value is empty after trimming.
 */
function normaliseTag(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  const t = raw.trim().toLowerCase()
  return t.length > 0 ? t : null
}

/**
 * Extracts the tags array from a frontmatter data object.
 * Handles both array `['a', 'b']` and comma-separated string `'a, b'` formats.
 *
 * @param data  Parsed frontmatter data from gray-matter.
 * @returns     Array of lowercased trimmed tag strings (deduped within a file).
 */
export function extractTagsFromFrontmatter(data: Record<string, unknown>): string[] {
  const raw = data['tags']
  if (raw === undefined || raw === null) return []

  let candidates: unknown[]

  if (Array.isArray(raw)) {
    candidates = raw
  } else if (typeof raw === 'string') {
    candidates = raw.split(',')
  } else {
    return []
  }

  const seen = new Set<string>()
  const result: string[] = []
  for (const c of candidates) {
    const t = normaliseTag(c)
    if (t !== null && !seen.has(t)) {
      seen.add(t)
      result.push(t)
    }
  }
  return result
}

/**
 * Builds a tag index from an array of vault files.
 *
 * Parses each file's frontmatter with gray-matter, extracts `tags`, and builds
 * a Map from each unique tag to the list of note paths that carry it.
 *
 * Tags are normalised to lowercase trimmed strings. A tag may appear at most
 * once per file even if the frontmatter lists it twice.
 *
 * @param files  Array of {path, content} pairs for every .md note in the vault.
 * @returns      Map<tag, string[]> — sorted alphabetically by tag.
 */
export function buildTagIndex(files: VaultFile[]): Map<string, string[]> {
  const index = new Map<string, string[]>()

  for (const { path, content } of files) {
    let data: Record<string, unknown>
    try {
      const parsed = matter(content)
      data = parsed.data as Record<string, unknown>
    } catch {
      // Malformed frontmatter — skip this file silently.
      continue
    }

    const tags = extractTagsFromFrontmatter(data)
    for (const tag of tags) {
      const existing = index.get(tag)
      if (existing) {
        existing.push(path)
      } else {
        index.set(tag, [path])
      }
    }
  }

  // Return sorted by tag name for stable display order.
  return new Map([...index.entries()].sort((a, b) => a[0].localeCompare(b[0])))
}

/**
 * Converts a tag index Map to a sorted TagEntry array for rendering.
 *
 * @param index  Output of buildTagIndex.
 * @returns      Array of {tag, count} sorted alphabetically.
 */
export function tagIndexToEntries(index: Map<string, string[]>): TagEntry[] {
  return [...index.entries()].map(([tag, paths]) => ({ tag, count: paths.length }))
}

/**
 * Applies an AND filter: returns only note paths that have ALL of the
 * selected tags.  If selectedTags is empty, returns all note paths.
 *
 * @param index         Tag index from buildTagIndex.
 * @param selectedTags  Set of tags that must all be present on a note.
 * @returns             Set of note paths that satisfy the AND filter.
 */
export function filterNotesByTags(
  index: Map<string, string[]>,
  selectedTags: ReadonlySet<string>,
): Set<string> {
  if (selectedTags.size === 0) return new Set() // no filter = show all

  // Start with paths for the first tag, then intersect.
  const tagList = [...selectedTags]
  const firstPaths = index.get(tagList[0])
  if (!firstPaths) return new Set()

  let result = new Set(firstPaths)
  for (let i = 1; i < tagList.length; i++) {
    const paths = new Set(index.get(tagList[i]) ?? [])
    result = new Set([...result].filter((p) => paths.has(p)))
  }
  return result
}
