/**
 * Purpose: RAG Explorer panel — top-level composition of SourceList,
 *   IngestProgress, SearchBar, SearchResults, and IndexStats.
 *   Wires all IPC hooks and the Tauri file-drop event for drag-and-drop ingest.
 * Inputs:  None (self-contained).
 * Outputs: Full-height panel for the /rag-explorer route.
 * Constraints:
 *   - Tauri 2 FileDrop: listens on 'tauri://file-drop' via @tauri-apps/api/event.
 *     Falls back gracefully when not in a Tauri context (Vitest / browser).
 *   - SourceList.onDrop handles native DataTransfer.files (browser dev mode).
 *   - Both paths invoke('rag_ingest_file', { path }) — graceful on daemon absence.
 *   - After ingest completes (useIngestProgress.onComplete), refetches sources
 *     and index stats.
 *   - Project selector updates a local state; NOT yet wired to daemon (P5).
 * SPORT: MASTER-COMPONENTS.md — RagExplorerPanel (T-P4-E01-27)
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { Database } from 'lucide-react'
import { invoke } from '@tauri-apps/api/core'
import { homeDir } from '@tauri-apps/api/path'
import { SourceList } from './SourceList'
import { IngestProgress } from './IngestProgress'
import { SearchBar } from './SearchBar'
import { SearchResults } from './SearchResults'
import { IndexStats } from './IndexStats'
import { useRagSources } from './useRagSources'
import { useRagSearch } from './useRagSearch'
import { useRagIndexStats } from './useRagIndexStats'
import { useIngestProgress } from './useIngestProgress'

// ── Panel ─────────────────────────────────────────────────────────────────────

export function RagExplorerPanel() {
  const {
    sources,
    loading: sourcesLoading,
    error: sourcesError,
    refetch: refetchSources,
  } = useRagSources()
  const {
    citations,
    loading: searchLoading,
    error: searchError,
    queryMs,
    search,
    clear,
  } = useRagSearch()
  const {
    stats,
    loading: statsLoading,
    error: statsError,
    refetch: refetchStats,
  } = useRagIndexStats()

  // Called when ingest completes — refresh sources + stats
  const handleIngestComplete = useCallback(() => {
    refetchSources()
    refetchStats()
  }, [refetchSources, refetchStats])

  const { progress, active: ingestActive } = useIngestProgress(handleIngestComplete)

  const [projectRoot, setProjectRoot] = useState('')

  // Resolve the user's home directory at runtime — avoids hardcoding any path.
  useEffect(() => {
    homeDir()
      .then((dir) => setProjectRoot(dir))
      .catch(() => {
        // Not in Tauri context (e.g. Vitest) — leave as empty string.
      })
  }, [])

  // ── Ingest trigger ─────────────────────────────────────────────────────────

  const triggerIngest = useCallback(async (path: string) => {
    try {
      await invoke('rag_ingest_file', { path })
    } catch (_err) {
      // Daemon not yet wired (T-P4-E01-29) — progress event won't fire; graceful.
    }
  }, [])

  // ── Wire Tauri 2 file-drop event ──────────────────────────────────────────
  const unlistenRef = useRef<(() => void) | undefined>(undefined)

  useEffect(() => {
    import('@tauri-apps/api/event')
      .then(({ listen }) =>
        listen<{ paths: string[] }>('tauri://file-drop', (event) => {
          const paths = event.payload?.paths
          if (Array.isArray(paths) && paths.length > 0) {
            void triggerIngest(paths[0]!)
          }
        })
      )
      .then((fn) => {
        unlistenRef.current = fn
      })
      .catch(() => {
        // Not in Tauri context — no-op.
      })

    return () => {
      unlistenRef.current?.()
    }
  }, [triggerIngest])

  const handleDrop = useCallback(
    (path: string) => {
      void triggerIngest(path)
    },
    [triggerIngest]
  )

  // ── Render ─────────────────────────────────────────────────────────────────

  return (
    <div className="flex h-full flex-col overflow-hidden bg-background">
      {/* Page header */}
      <header className="flex shrink-0 items-center gap-2 border-b border-border px-6 py-3">
        <Database className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-lg font-semibold">RAG Explorer</h1>
        <span className="text-sm text-muted-foreground">sources · search · index stats</span>

        {/* Project root input */}
        <div className="ml-auto flex items-center gap-2">
          <label htmlFor="rag-project-input" className="text-xs text-muted-foreground">
            Project
          </label>
          <input
            id="rag-project-input"
            type="text"
            value={projectRoot}
            onChange={(e) => setProjectRoot(e.target.value)}
            placeholder="Enter project root path…"
            className="h-7 w-64 rounded-md border border-input bg-background px-2 text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
            aria-label="Project root path for RAG index"
          />
        </div>
      </header>

      {/* Scrollable content */}
      <div className="flex-1 space-y-6 overflow-y-auto px-6 py-4">
        {/* Index Stats */}
        <IndexStats stats={stats} loading={statsLoading} error={statsError} />

        {/* Ingest progress (hidden when idle) */}
        <IngestProgress progress={progress} active={ingestActive} />

        {/* Source list */}
        <section>
          <h2 className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Indexed Sources
          </h2>
          <SourceList
            sources={sources}
            loading={sourcesLoading}
            error={sourcesError}
            onDrop={handleDrop}
          />
        </section>

        {/* Search */}
        <section>
          <h2 className="mb-3 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
            Semantic Search
          </h2>
          <SearchBar
            onSearch={(q) => {
              clear()
              search(q)
            }}
            loading={searchLoading}
          />
          {(citations.length > 0 || searchError) && (
            <div className="mt-4">
              <SearchResults
                citations={citations}
                loading={searchLoading}
                error={searchError}
                queryMs={queryMs}
              />
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
