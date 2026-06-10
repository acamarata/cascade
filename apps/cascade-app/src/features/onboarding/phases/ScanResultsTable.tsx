/**
 * Purpose: Displays a summary table of legacy tool homes discovered by the scanner.
 * Inputs:  homes — LegacyToolHome[] from scan_global_homes / scan_dev_tree Tauri commands.
 * Outputs: Table with one row per tool that has files; accordion expands per-project paths;
 *          empty state when no homes; footer summary of totals.
 * Constraints:
 *   - Read-only display only — no destructive operations.
 *   - Tools with zero files are hidden from the table.
 *   - Uses inline shadcn-compatible components (Table, Accordion) built from primitives
 *     until ui/table and ui/accordion are scaffolded.
 *   - Human-readable size formatting (B / KB / MB / GB).
 * SPORT: MASTER-COMPONENTS.md — ScanResultsTable — wizard step 3 — ✅
 */

import { useState } from 'react'
import { ChevronDown, ChevronRight, FileText, FolderOpen } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { LegacyToolHome } from '@/lib/scanner/types'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Human-readable tool name map. */
const TOOL_LABELS: Record<string, string> = {
  'claude-code': 'Claude Code',
  opencode: 'OpenCode',
  codex: 'Codex',
  cursor: 'Cursor',
  aider: 'Aider',
  windsurf: 'Windsurf',
  antigravity: 'Antigravity',
}

/**
 * Format a byte count into a human-readable string.
 * @param bytes - Raw byte count.
 * @returns String like "12 KB", "3.4 MB".
 */
function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

// ---------------------------------------------------------------------------
// Sub-components
// ---------------------------------------------------------------------------

interface PathListProps {
  paths: string[]
  label: string
}

/** Renders a collapsed/expanded list of paths. */
function PathList({ paths, label }: PathListProps) {
  if (paths.length === 0) return <span className="text-muted-foreground">—</span>
  if (paths.length === 1) {
    return (
      <span className="font-mono text-xs text-foreground" title={paths[0]}>
        {paths[0].length > 40 ? `…${paths[0].slice(-37)}` : paths[0]}
      </span>
    )
  }
  return (
    <span className="text-xs text-muted-foreground">
      {paths.length} {label}
    </span>
  )
}

interface ExpandedRowProps {
  home: LegacyToolHome
}

/** Expanded accordion content for a tool row showing all paths. */
function ExpandedRow({ home }: ExpandedRowProps) {
  const allPaths = [...home.globalPaths, ...home.perProjectPaths]
  return (
    <div className="bg-muted/30 px-4 py-3">
      <div className="space-y-2">
        {home.globalPaths.length > 0 && (
          <div>
            <p className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">
              Global locations
            </p>
            <ul className="space-y-0.5">
              {home.globalPaths.map((p) => (
                <li key={p} className="flex items-center gap-1.5">
                  <FolderOpen
                    className="h-3 w-3 flex-shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                  <span className="font-mono text-xs text-foreground break-all">{p}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {home.perProjectPaths.length > 0 && (
          <div>
            <p className="mb-1 text-xs font-medium text-muted-foreground uppercase tracking-wide">
              Per-project locations ({home.perProjectPaths.length})
            </p>
            <ul className="max-h-36 space-y-0.5 overflow-y-auto">
              {home.perProjectPaths.map((p) => (
                <li key={p} className="flex items-center gap-1.5">
                  <FileText
                    className="h-3 w-3 flex-shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                  <span className="font-mono text-xs text-foreground break-all">{p}</span>
                </li>
              ))}
            </ul>
          </div>
        )}
        {allPaths.length === 0 && (
          <p className="text-xs text-muted-foreground italic">No paths recorded.</p>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ScanResultsTable
// ---------------------------------------------------------------------------

export interface ScanResultsTableProps {
  /** Combined list of tool homes (global + dev-tree merged by toolId). */
  homes: LegacyToolHome[]
  /** Optional className for the container. */
  className?: string
}

/**
 * Summary table of legacy tool homes discovered during the scan phase.
 *
 * Columns: Tool | Global Home | Per-Project Locations | Total Files | Size
 * Rows: one per tool with at least one file (tools with totalFiles === 0 are hidden).
 * Accordion: each row is expandable to show the full path list.
 * Empty state: shown when homes array is empty or all entries have zero files.
 * Footer: total summary across all visible tools.
 *
 * @param homes - LegacyToolHome[] — the raw scan output from Tauri commands.
 */
export function ScanResultsTable({ homes, className }: ScanResultsTableProps) {
  const [expandedRows, setExpandedRows] = useState<Set<string>>(new Set())

  // Filter to only tools that actually have files
  const visibleHomes = homes.filter((h) => h.totalFiles > 0)

  const toggleRow = (toolId: string) => {
    setExpandedRows((prev) => {
      const next = new Set(prev)
      if (next.has(toolId)) {
        next.delete(toolId)
      } else {
        next.add(toolId)
      }
      return next
    })
  }

  // Totals across all visible tools
  const totalTools = visibleHomes.length
  const totalFiles = visibleHomes.reduce((acc, h) => acc + h.totalFiles, 0)
  const totalBytes = visibleHomes.reduce((acc, h) => acc + h.totalSizeBytes, 0)

  // Empty state
  if (visibleHomes.length === 0) {
    return (
      <div
        className={cn(
          'flex flex-col items-center justify-center gap-2 rounded-md border border-dashed border-border py-10 text-center',
          className
        )}
        role="status"
        aria-label="No legacy configs found"
      >
        <FileText className="h-8 w-8 text-muted-foreground" aria-hidden="true" />
        <p className="text-sm font-medium text-foreground">No legacy configs found</p>
        <p className="max-w-xs text-xs text-muted-foreground">
          No instruction files were detected for any supported AI coding tool in the scanned
          locations.
        </p>
      </div>
    )
  }

  return (
    <div className={cn('w-full overflow-hidden rounded-md border border-border', className)}>
      {/* Non-destructive notice */}
      <div className="border-b border-border bg-muted/20 px-4 py-2">
        <p className="text-xs text-muted-foreground">
          <span className="font-medium text-foreground">Read-only scan.</span> Nothing is modified,
          moved, or deleted at this step.
        </p>
      </div>

      {/* Table header */}
      <div
        className="grid items-center border-b border-border bg-muted/50 px-4 py-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground"
        style={{ gridTemplateColumns: '1fr 180px 110px 80px 70px' }}
        role="row"
        aria-label="Table header"
      >
        <span>Tool</span>
        <span>Global Home</span>
        <span>Per-Project</span>
        <span className="text-right">Files</span>
        <span className="text-right">Size</span>
      </div>

      {/* Table rows */}
      <div role="table" aria-label="Legacy tool homes">
        {visibleHomes.map((home) => {
          const isExpanded = expandedRows.has(home.toolId)
          const label = TOOL_LABELS[home.toolId] ?? home.toolId

          return (
            <div key={home.toolId} role="rowgroup">
              {/* Main row */}
              <button
                type="button"
                className={cn(
                  'grid w-full cursor-pointer items-center border-b border-border px-4 py-3 text-left',
                  'hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset',
                  'transition-colors',
                  isExpanded && 'bg-muted/20'
                )}
                style={{ gridTemplateColumns: '1fr 180px 110px 80px 70px' }}
                onClick={() => toggleRow(home.toolId)}
                aria-expanded={isExpanded}
                aria-controls={`scan-row-${home.toolId}`}
                role="row"
              >
                {/* Tool name + expand icon */}
                <span className="flex items-center gap-2" role="cell">
                  {isExpanded ? (
                    <ChevronDown
                      className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground"
                      aria-hidden="true"
                    />
                  ) : (
                    <ChevronRight
                      className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground"
                      aria-hidden="true"
                    />
                  )}
                  <span className="text-sm font-medium text-foreground">{label}</span>
                </span>

                {/* Global paths */}
                <span className="overflow-hidden" role="cell">
                  <PathList paths={home.globalPaths} label="locations" />
                </span>

                {/* Per-project count */}
                <span className="text-xs text-muted-foreground" role="cell">
                  {home.perProjectPaths.length > 0
                    ? `${home.perProjectPaths.length} project${home.perProjectPaths.length !== 1 ? 's' : ''}`
                    : '—'}
                </span>

                {/* File count */}
                <span className="text-right text-sm tabular-nums text-foreground" role="cell">
                  {home.totalFiles.toLocaleString()}
                </span>

                {/* Size */}
                <span className="text-right text-xs tabular-nums text-muted-foreground" role="cell">
                  {formatBytes(home.totalSizeBytes)}
                </span>
              </button>

              {/* Expanded path list */}
              {isExpanded && (
                <div id={`scan-row-${home.toolId}`} role="cell">
                  <ExpandedRow home={home} />
                </div>
              )}
            </div>
          )
        })}
      </div>

      {/* Footer summary */}
      <div className="border-t border-border bg-muted/20 px-4 py-2">
        <p className="text-xs text-muted-foreground">
          Found{' '}
          <span className="font-medium text-foreground">
            {totalTools} tool home{totalTools !== 1 ? 's' : ''}
          </span>
          {', '}
          <span className="font-medium text-foreground">
            {totalFiles.toLocaleString()} file{totalFiles !== 1 ? 's' : ''}
          </span>
          {', '}
          <span className="font-medium text-foreground">{formatBytes(totalBytes)}</span> of
          instruction content
        </p>
      </div>
    </div>
  )
}
