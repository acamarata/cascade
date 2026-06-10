/**
 * Purpose: BacklinksPanel — shows all vault notes that contain a [[wikilink]]
 *   pointing to the currently open note. Reads from the live link index provided
 *   via props; clicking a row navigates to the source note via setCurrentFile.
 * Inputs:  currentFile (open note path), linkIndex (Map from buildLinkIndex),
 *          setCurrentFile (VaultContext navigate fn).
 * Outputs: Rendered list of backlink rows with source title + snippet, or an
 *          empty state when no notes link here.
 * Constraints: No IPC calls — all data is passed in. Pure presentational + one
 *   click handler. Tailwind + shadcn classes only. No cm6 or gray-matter imports.
 * SPORT: MASTER-COMPONENTS.md — BacklinksPanel (T-P3-E06-06)
 */

import { Link } from 'lucide-react'
import type { BacklinkEntry } from '../../services/vault/linkIndex'
import { noteNameFromPath } from '../../services/vault/linkIndex'

// ── Props ─────────────────────────────────────────────────────────────────────

interface BacklinksPanelProps {
  /** Absolute path to the currently open note, or null when no file is open. */
  currentFile: string | null
  /**
   * The vault-wide reverse link index produced by buildLinkIndex().
   * Null while the index is still being built (loading state).
   */
  linkIndex: Map<string, BacklinkEntry[]> | null
  /** Navigate to a note — calls VaultContext.setCurrentFile. */
  setCurrentFile: (path: string) => void
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/** Derives a human-readable title from a file path (basename without extension). */
function titleFromPath(filePath: string): string {
  const basename = filePath.split('/').pop() ?? filePath
  return basename.endsWith('.md') ? basename.slice(0, -3) : basename
}

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * BacklinksPanel
 *
 * Reads the backlinks for the current note from `linkIndex` and renders them
 * as a scrollable list. Each row shows the source note title and a one-line
 * snippet of the surrounding context.
 *
 * Empty state: "No backlinks" message when nothing links here.
 * Loading state: shows nothing while index is null (initial build).
 */
export function BacklinksPanel({ currentFile, linkIndex, setCurrentFile }: BacklinksPanelProps) {
  // ── Derive backlinks for the current file ────────────────────────────────────

  let backlinks: BacklinkEntry[] = []

  if (currentFile !== null && linkIndex !== null) {
    const key = noteNameFromPath(currentFile)
    backlinks = linkIndex.get(key) ?? []
  }

  // ── Render: no file selected ─────────────────────────────────────────────────

  if (currentFile === null) {
    return (
      <div className="flex flex-col gap-1 p-3">
        <SectionHeader />
        <p className="text-xs text-muted-foreground">No note selected.</p>
      </div>
    )
  }

  // ── Render: index still loading ──────────────────────────────────────────────

  if (linkIndex === null) {
    return (
      <div className="flex flex-col gap-1 p-3">
        <SectionHeader />
        <p className="text-xs text-muted-foreground">Building index…</p>
      </div>
    )
  }

  // ── Render: empty state ──────────────────────────────────────────────────────

  if (backlinks.length === 0) {
    return (
      <div className="flex flex-col gap-1 p-3" data-testid="backlinks-panel" aria-label="Backlinks">
        <SectionHeader count={0} />
        <p className="text-xs text-muted-foreground" data-testid="backlinks-empty">
          No backlinks
        </p>
      </div>
    )
  }

  // ── Render: backlink rows ─────────────────────────────────────────────────────

  return (
    <div
      className="flex flex-col overflow-hidden"
      data-testid="backlinks-panel"
      aria-label="Backlinks"
    >
      <div className="flex h-8 shrink-0 items-center gap-1.5 border-b border-border px-3">
        <Link className="h-3 w-3 text-muted-foreground" aria-hidden="true" />
        <span className="text-xs font-medium text-foreground">Backlinks</span>
        <span className="ml-auto rounded-full bg-muted px-1.5 text-[10px] font-semibold text-muted-foreground">
          {backlinks.length}
        </span>
      </div>

      <ul
        className="flex flex-1 flex-col overflow-y-auto"
        aria-label={`${backlinks.length} backlink${backlinks.length === 1 ? '' : 's'}`}
      >
        {backlinks.map((entry) => (
          <li key={entry.sourcePath}>
            <button
              type="button"
              onClick={() => setCurrentFile(entry.sourcePath)}
              className="group flex w-full flex-col gap-0.5 px-3 py-2 text-left transition-colors hover:bg-accent"
              data-testid="backlink-row"
              title={entry.sourcePath}
            >
              <span className="truncate text-xs font-medium text-foreground group-hover:text-accent-foreground">
                {titleFromPath(entry.sourcePath)}
              </span>
              {entry.snippet && (
                <span className="line-clamp-1 text-[11px] text-muted-foreground">
                  {entry.snippet}
                </span>
              )}
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

// ── Sub-component ─────────────────────────────────────────────────────────────

/** Compact section header for the loading / no-file states. */
function SectionHeader({ count }: { count?: number }) {
  return (
    <div className="flex items-center gap-1.5">
      <Link className="h-3 w-3 text-muted-foreground" aria-hidden="true" />
      <span className="text-xs font-medium text-foreground">Backlinks</span>
      {count !== undefined && (
        <span className="ml-auto rounded-full bg-muted px-1.5 text-[10px] font-semibold text-muted-foreground">
          {count}
        </span>
      )}
    </div>
  )
}
