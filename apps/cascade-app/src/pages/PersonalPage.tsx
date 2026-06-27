/**
 * Purpose: Personal hub — shows threads/topics from rag-15 personal endpoints.
 *   Placeholder layout if personal endpoint unreachable.
 * Inputs:  None (standalone route page).
 * Outputs: Simple two-panel layout: thread list (left) + topic detail (right).
 * SPORT: MASTER-ROUTES.md — /personal
 */
import { useEffect, useState } from 'react'
import { User, MessageSquare } from 'lucide-react'

interface PersonalThread {
  id: string
  title: string
  updatedAt: number
  messageCount: number
}

const DAEMON_BASE_URL =
  typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
    ? 'http://127.0.0.1:9761'
    : ''

export function PersonalPage() {
  const [threads, setThreads] = useState<PersonalThread[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(`${DAEMON_BASE_URL}/api/memory/personal/threads`)
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const data = (await res.json()) as { threads?: PersonalThread[] }
        if (!cancelled) setThreads(data.threads ?? [])
      } catch {
        if (!cancelled) setError('Personal threads unavailable — daemon may be offline.')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <main id="main-content" className="flex h-full overflow-hidden">
      {/* Thread list panel */}
      <div className="w-64 border-r border-border flex flex-col shrink-0">
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border">
          <User className="h-4 w-4 text-primary" aria-hidden="true" />
          <h1 className="text-sm font-semibold">Personal</h1>
        </div>
        <div className="flex-1 overflow-auto p-2">
          {error && (
            <p className="text-xs text-muted-foreground px-2 py-4">{error}</p>
          )}
          {!error && threads.length === 0 && (
            <p className="text-xs text-muted-foreground px-2 py-4">
              No threads yet. Start a personal chat.
            </p>
          )}
          {threads.map((t) => (
            <button
              key={t.id}
              type="button"
              onClick={() => setSelected(t.id)}
              className={`w-full text-left px-3 py-2 rounded-md text-sm mb-1 transition-colors ${
                selected === t.id
                  ? 'bg-accent text-accent-foreground'
                  : 'hover:bg-accent/50 text-muted-foreground'
              }`}
            >
              <div className="font-medium truncate">{t.title}</div>
              <div className="text-xs opacity-60">{t.messageCount} messages</div>
            </button>
          ))}
        </div>
      </div>

      {/* Detail panel */}
      <div className="flex-1 flex flex-col items-center justify-center text-muted-foreground">
        {selected ? (
          <div className="p-4 w-full max-w-2xl">
            <p className="text-sm text-muted-foreground">Thread: {selected}</p>
          </div>
        ) : (
          <div className="flex flex-col items-center gap-2">
            <MessageSquare className="h-8 w-8 opacity-30" aria-hidden="true" />
            <p className="text-sm">Select a thread or start a new Personal chat</p>
          </div>
        )}
      </div>
    </main>
  )
}
