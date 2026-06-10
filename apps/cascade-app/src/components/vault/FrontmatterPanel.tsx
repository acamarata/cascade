/**
 * Purpose: Displays YAML frontmatter metadata extracted from a markdown file.
 *   Parses the frontmatter block using gray-matter and renders it as a key/value
 *   table beneath the editor. Shows nothing when there is no frontmatter.
 * Inputs:
 *   - content: raw markdown string (may or may not contain frontmatter)
 *   - className: optional extra Tailwind classes for the wrapper
 * Outputs:
 *   - Renders a compact key/value table for fields like title, date, tags, status.
 *   - Returns null when no frontmatter is present (zero DOM footprint).
 * Constraints:
 *   - Handles malformed YAML gracefully — never throws; logs warn and returns null.
 *   - Tags arrays are rendered as comma-separated strings.
 *   - Date objects are formatted as ISO date strings.
 * SPORT: MASTER-COMPONENTS.md — FrontmatterPanel (T-P3-E06-04)
 */

import { useMemo } from 'react'
import matter from 'gray-matter'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Well-known frontmatter keys rendered first, in order. */
const PRIORITY_KEYS = ['title', 'date', 'tags', 'status', 'author', 'draft']

/**
 * Parses frontmatter from a markdown string using gray-matter.
 * Returns null on any error or when no frontmatter is present.
 */
export function parseFrontmatter(content: string): Record<string, unknown> | null {
  if (!content) return null
  try {
    const parsed = matter(content)
    const data = parsed.data
    if (!data || Object.keys(data).length === 0) return null
    return data
  } catch (e) {
    console.warn('[FrontmatterPanel] Failed to parse frontmatter:', e)
    return null
  }
}

/**
 * Formats a frontmatter value for display.
 * Arrays → comma-separated string.
 * Date objects → ISO date string (date part only).
 * Everything else → String().
 */
export function formatFrontmatterValue(value: unknown): string {
  if (Array.isArray(value)) {
    return value.map((v) => String(v)).join(', ')
  }
  if (value instanceof Date) {
    return value.toISOString().slice(0, 10)
  }
  if (value === null || value === undefined) {
    return '—'
  }
  return String(value)
}

/**
 * Returns frontmatter keys sorted with priority keys first, rest alphabetical.
 */
export function sortFrontmatterKeys(keys: string[]): string[] {
  const priority = PRIORITY_KEYS.filter((k) => keys.includes(k))
  const rest = keys.filter((k) => !PRIORITY_KEYS.includes(k)).sort()
  return [...priority, ...rest]
}

// ---------------------------------------------------------------------------
// Props
// ---------------------------------------------------------------------------

export interface FrontmatterPanelProps {
  /** Raw markdown content (frontmatter parsed internally). */
  content: string
  /** Optional extra Tailwind classes for the wrapper. */
  className?: string
}

// ---------------------------------------------------------------------------
// FrontmatterPanel component
// ---------------------------------------------------------------------------

/**
 * Compact frontmatter metadata panel.
 * Shows a key/value table for YAML frontmatter fields.
 * Returns null when there is no frontmatter or parsing fails.
 */
export function FrontmatterPanel({ content, className = '' }: FrontmatterPanelProps) {
  const data = useMemo(() => parseFrontmatter(content), [content])

  if (!data) return null

  const keys = sortFrontmatterKeys(Object.keys(data))

  return (
    <div
      className={`border-t border-border bg-muted/40 px-4 py-2 ${className}`}
      data-testid="frontmatter-panel"
    >
      <p className="mb-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
        Frontmatter
      </p>
      <table className="w-full text-xs" aria-label="Frontmatter metadata">
        <tbody>
          {keys.map((key) => (
            <tr key={key} className="group">
              <td className="w-28 shrink-0 py-0.5 pr-3 font-mono text-muted-foreground group-hover:text-foreground">
                {key}
              </td>
              <td
                className="py-0.5 text-foreground"
                data-testid={`frontmatter-value-${key}`}
              >
                {formatFrontmatterValue(data[key])}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
