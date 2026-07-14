/**
 * Purpose: Global search page — queries the cascade daemon via cascade_search
 *          Tauri command and renders ranked results with title, excerpt, path, and type.
 * Inputs:  None (manages its own query state).
 * Outputs: Full-page search UI with debounced input, results list, and status states.
 * Constraints: Requires cascade daemon running; gracefully degrades if unavailable.
 * SPORT: MASTER-ROUTES.md — /search
 */

import { useState, useEffect, useRef } from 'react'
import { invoke } from '@tauri-apps/api/core'

interface SearchResult {
  title: string
  excerpt: string
  path: string
  result_type: string
  score: number
}

type Status = 'idle' | 'loading' | 'done' | 'error'

const TYPE_COLORS: Record<string, string> = {
  memory: 'bg-purple-100 text-purple-700',
  task: 'bg-blue-100 text-blue-700',
  idea: 'bg-yellow-100 text-yellow-700',
  doc: 'bg-green-100 text-green-700',
  plan: 'bg-orange-100 text-orange-700',
}

function typeBadgeClass(type: string): string {
  return TYPE_COLORS[type.toLowerCase()] ?? 'bg-gray-100 text-gray-600'
}

export function SearchPage() {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<SearchResult[]>([])
  const [status, setStatus] = useState<Status>('idle')
  const [errorMsg, setErrorMsg] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  useEffect(() => {
    if (!query.trim()) {
      setResults([])
      setStatus('idle')
      return
    }

    setStatus('loading')
    const timer = setTimeout(async () => {
      try {
        const res = await invoke<SearchResult[]>('cascade_search', { query: query.trim() })
        setResults(res ?? [])
        setStatus('done')
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err)
        const isDaemonDown =
          msg.includes('connection') || msg.includes('refused') || msg.includes('daemon')
        setErrorMsg(
          isDaemonDown
            ? 'Cascade daemon is not running. Start it from the menu bar.'
            : `Search failed: ${msg}`
        )
        setResults([])
        setStatus('error')
      }
    }, 300)

    return () => clearTimeout(timer)
  }, [query])

  return (
    <main className="flex-1 flex flex-col p-6 gap-4 max-w-3xl mx-auto w-full">
      {/* Search input */}
      <div className="relative">
        <svg
          className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-gray-400 pointer-events-none"
          xmlns="http://www.w3.org/2000/svg"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          strokeWidth={2}
        >
          <circle cx="11" cy="11" r="8" />
          <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-4.35-4.35" />
        </svg>
        <input
          ref={inputRef}
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search your cascade context…"
          className="w-full pl-10 pr-4 py-2.5 rounded-lg border border-gray-200 bg-white text-sm shadow-sm focus:outline-none focus:ring-2 focus:ring-blue-500 focus:border-transparent"
        />
      </div>

      {/* States */}
      {status === 'idle' && (
        <p className="text-sm text-gray-400 text-center mt-8">
          Type to search your cascade context
        </p>
      )}

      {status === 'loading' && (
        <p className="text-sm text-gray-500 text-center mt-8 animate-pulse">Searching…</p>
      )}

      {status === 'error' && (
        <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 mt-4">
          {errorMsg}
        </div>
      )}

      {status === 'done' && results.length === 0 && (
        <p className="text-sm text-gray-400 text-center mt-8">
          No results for &ldquo;{query}&rdquo;
        </p>
      )}

      {status === 'done' && results.length > 0 && (
        <ul className="flex flex-col gap-2">
          {results.map((r, i) => (
            <li
              key={`${r.path}-${i}`}
              className="rounded-lg border border-gray-100 bg-white px-4 py-3 shadow-sm hover:border-blue-200 hover:shadow-md transition-shadow"
            >
              <div className="flex items-start justify-between gap-3">
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-medium text-gray-900 truncate">{r.title}</p>
                  <p className="text-xs text-gray-500 mt-0.5 line-clamp-2">{r.excerpt}</p>
                  <p className="text-xs text-gray-400 mt-1 font-mono truncate">{r.path}</p>
                </div>
                <span
                  className={`shrink-0 inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${typeBadgeClass(r.result_type)}`}
                >
                  {r.result_type}
                </span>
              </div>
            </li>
          ))}
        </ul>
      )}
    </main>
  )
}
