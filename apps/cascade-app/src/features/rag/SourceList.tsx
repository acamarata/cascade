/**
 * Purpose: Displays a table of indexed RAG sources with path, chunk count,
 *   last-indexed timestamp, and status badge. Supports drag-and-drop of
 *   files onto the table to trigger `rag_ingest_file`.
 * Inputs:
 *   sources  — array of RagSource rows.
 *   loading  — true while fetch is in-flight.
 *   error    — error string or null.
 *   onDrop   — callback invoked with the dropped file path on drag-and-drop.
 * Outputs: <section> with a table or empty-state message.
 * Constraints:
 *   - Uses HTML5 drag-and-drop API; Tauri FileDrop fires the 'tauri://file-drop'
 *     event (wired by RagExplorerPanel), but this component also handles the
 *     native DataTransfer.files path for parity in browser dev mode.
 *   - Must not crash when sources = [].
 *   - role="list" / role="row" for screen readers (complemented by <table>).
 *   - Timestamps use toLocaleDateString for compact display.
 * SPORT: MASTER-COMPONENTS.md — SourceList (T-P4-E01-27)
 */

import React, { useCallback, useRef, useState } from 'react'
import { FileText, Loader2 } from 'lucide-react'
import type { RagSource } from './types'

// ── Status badge ──────────────────────────────────────────────────────────────

const STATUS_CLASSES: Record<string, string> = {
  indexed: 'bg-green-500/15 text-green-700 dark:text-green-400',
  indexing: 'bg-blue-500/15 text-blue-700 dark:text-blue-400',
  error: 'bg-red-500/15 text-red-600 dark:text-red-400',
  pending: 'bg-muted text-muted-foreground',
}

function StatusBadge({ status }: { status: string }) {
  const cls = STATUS_CLASSES[status] ?? STATUS_CLASSES['pending']!
  return (
    <span
      className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${cls}`}
    >
      {status}
    </span>
  )
}

// ── Empty state ───────────────────────────────────────────────────────────────

function EmptyState({ isDragging }: { isDragging: boolean }) {
  return (
    <div
      className={[
        'flex flex-col items-center justify-center gap-2 rounded-lg border-2 border-dashed py-12 text-center',
        isDragging ? 'border-primary bg-primary/5' : 'border-muted-foreground/25',
      ].join(' ')}
      aria-live="polite"
    >
      <FileText
        className={`h-8 w-8 ${isDragging ? 'text-primary' : 'text-muted-foreground/40'}`}
        aria-hidden="true"
      />
      <p className="text-sm text-muted-foreground">
        {isDragging ? 'Drop to ingest file' : 'No indexed sources yet'}
      </p>
      <p className="text-xs text-muted-foreground/60">Drag a file here to ingest it</p>
    </div>
  )
}

// ── Props ─────────────────────────────────────────────────────────────────────

interface SourceListProps {
  sources: RagSource[]
  loading: boolean
  error: string | null
  onDrop?: (path: string) => void
}

// ── Component ─────────────────────────────────────────────────────────────────

export function SourceList({ sources, loading, error, onDrop }: SourceListProps) {
  const [isDragging, setIsDragging] = useState(false)
  const dragCounter = useRef(0)

  const handleDragEnter = useCallback((e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    e.stopPropagation()
    dragCounter.current += 1
    setIsDragging(true)
  }, [])

  const handleDragLeave = useCallback((e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    e.stopPropagation()
    dragCounter.current -= 1
    if (dragCounter.current <= 0) {
      dragCounter.current = 0
      setIsDragging(false)
    }
  }, [])

  const handleDragOver = useCallback((e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'copy'
  }, [])

  const handleDrop = useCallback(
    (e: React.DragEvent<HTMLDivElement>) => {
      e.preventDefault()
      e.stopPropagation()
      dragCounter.current = 0
      setIsDragging(false)

      if (!onDrop) return

      const files = Array.from(e.dataTransfer.files)
      if (files.length > 0) {
        // In Tauri the File object exposes .path on the native side.
        // In browser dev mode it's undefined — fall back to file.name.
        const first = files[0]!
        const path = (first as File & { path?: string }).path ?? first.name
        if (path) onDrop(path)
      }
    },
    [onDrop]
  )

  // ── Loading state ──────────────────────────────────────────────────────────

  if (loading) {
    return (
      <div className="flex items-center justify-center py-12" aria-busy="true" aria-live="polite">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" />
        <span className="sr-only">Loading sources…</span>
      </div>
    )
  }

  // ── Error state ────────────────────────────────────────────────────────────

  if (error && sources.length === 0) {
    return (
      <div
        role="alert"
        className="rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3 text-sm text-destructive"
      >
        <span className="font-medium">Error loading sources: </span>
        {error}
        <p className="mt-1 text-xs text-muted-foreground">
          Daemon may not be running. T-P4-E01-29 wires live search.
        </p>
      </div>
    )
  }

  // ── Main render ────────────────────────────────────────────────────────────

  return (
    <section
      aria-label="Indexed sources"
      onDragEnter={handleDragEnter}
      onDragLeave={handleDragLeave}
      onDragOver={handleDragOver}
      onDrop={handleDrop}
    >
      {sources.length === 0 ? (
        <EmptyState isDragging={isDragging} />
      ) : (
        <div
          className={[
            'overflow-hidden rounded-lg border',
            isDragging ? 'border-primary ring-2 ring-primary/30' : 'border-border',
          ].join(' ')}
        >
          <table className="w-full text-sm" aria-label="Indexed sources">
            <thead className="bg-muted/40">
              <tr>
                <th scope="col" className="px-4 py-2 text-left font-medium text-muted-foreground">
                  Path
                </th>
                <th scope="col" className="px-4 py-2 text-right font-medium text-muted-foreground">
                  Chunks
                </th>
                <th scope="col" className="px-4 py-2 text-left font-medium text-muted-foreground">
                  Indexed
                </th>
                <th scope="col" className="px-4 py-2 text-left font-medium text-muted-foreground">
                  Status
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {sources.map((src) => (
                <tr key={src.path} className="hover:bg-muted/30">
                  <td
                    className="max-w-xs truncate px-4 py-2 font-mono text-xs text-foreground"
                    title={src.path}
                  >
                    {src.path}
                  </td>
                  <td className="px-4 py-2 text-right tabular-nums text-muted-foreground">
                    {src.chunk_count.toLocaleString()}
                  </td>
                  <td className="px-4 py-2 text-muted-foreground">
                    {src.indexed_at
                      ? new Date(src.indexed_at).toLocaleDateString(undefined, {
                          month: 'short',
                          day: 'numeric',
                          hour: '2-digit',
                          minute: '2-digit',
                        })
                      : '—'}
                  </td>
                  <td className="px-4 py-2">
                    <StatusBadge status={src.status} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>

          {isDragging && (
            <div className="border-t border-primary/30 bg-primary/5 px-4 py-2 text-center text-xs text-primary">
              Drop to ingest file
            </div>
          )}
        </div>
      )}
    </section>
  )
}
