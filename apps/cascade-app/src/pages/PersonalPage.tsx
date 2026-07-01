/**
 * Purpose: Personal hub — shows threads/topics from the daemon's personal-workspace
 *   API (/api/personal/threads), and the encrypted personal vault (cascade-personal
 *   IPC) on a second tab.
 * Inputs:  None (standalone route page).
 * Outputs: Tab bar ("Threads" | "Encrypted Vault") with two panels:
 *   - Threads: two-panel layout (thread list left, detail right).
 *   - Encrypted Vault: PersonalEncryptedVaultPanel.
 * Constraints:
 *   - GET /api/personal/threads returns a raw Thread[] array (not { threads }).
 *   - GET /api/personal/threads/:id returns the single Thread; there is no
 *     server-side endpoint yet to list a thread's chat/task history, so the
 *     detail panel shows thread metadata only (title/sensitivity/created_at) —
 *     no fabricated message list.
 * SPORT: MASTER-ROUTES.md — /personal
 */
import { useEffect, useState } from 'react'
import { User, MessageSquare } from 'lucide-react'
import { PersonalEncryptedVaultPanel } from '../features/personal/PersonalEncryptedVaultPanel'

/** Mirrors cascade_core::threads::store::Thread. */
interface PersonalThread {
  id: string
  parent_id: string | null
  title: string
  sensitivity: string
  created_at: string
  archived: boolean
}

const DAEMON_BASE_URL =
  typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
    ? 'http://127.0.0.1:9761'
    : ''

type Tab = 'threads' | 'vault'

export function PersonalPage() {
  const [threads, setThreads] = useState<PersonalThread[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [selectedThread, setSelectedThread] = useState<PersonalThread | null>(null)
  const [detailError, setDetailError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [activeTab, setActiveTab] = useState<Tab>('threads')

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(`${DAEMON_BASE_URL}/api/personal/threads`)
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        // Endpoint returns a raw array, not { threads: [...] }.
        const data = (await res.json()) as PersonalThread[]
        if (!cancelled) setThreads(Array.isArray(data) ? data : [])
      } catch {
        if (!cancelled) setError('Personal threads unavailable — daemon may be offline.')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  // Fetch the selected thread's detail on selection change.
  useEffect(() => {
    if (!selected) {
      setSelectedThread(null)
      return
    }
    let cancelled = false
    setDetailError(null)
    ;(async () => {
      try {
        const res = await fetch(`${DAEMON_BASE_URL}/api/personal/threads/${encodeURIComponent(selected)}`)
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const data = (await res.json()) as PersonalThread
        if (!cancelled) setSelectedThread(data)
      } catch {
        if (!cancelled) setDetailError('Thread detail unavailable — daemon may be offline.')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [selected])

  return (
    <div className="flex flex-col h-full overflow-hidden">
      {/* Tab bar */}
      <div className="flex items-center gap-0 border-b border-border px-4 shrink-0">
        <button
          type="button"
          onClick={() => setActiveTab('threads')}
          className={`px-4 py-2.5 text-sm font-medium border-b-2 transition-colors -mb-px ${
            activeTab === 'threads'
              ? 'border-primary text-foreground'
              : 'border-transparent text-muted-foreground hover:text-foreground hover:border-border'
          }`}
        >
          Threads
        </button>
        <button
          type="button"
          onClick={() => setActiveTab('vault')}
          className={`px-4 py-2.5 text-sm font-medium border-b-2 transition-colors -mb-px ${
            activeTab === 'vault'
              ? 'border-primary text-foreground'
              : 'border-transparent text-muted-foreground hover:text-foreground hover:border-border'
          }`}
        >
          Encrypted Vault
        </button>
      </div>

      {/* Threads panel */}
      {activeTab === 'threads' && (
        <main id="main-content" className="flex flex-1 overflow-hidden">
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
                  <div className="text-xs opacity-60">{t.sensitivity}</div>
                </button>
              ))}
            </div>
          </div>

          {/* Detail panel */}
          <div className="flex-1 flex flex-col items-center justify-center text-muted-foreground">
            {selected && detailError && (
              <p className="text-sm text-muted-foreground">{detailError}</p>
            )}
            {selected && !detailError && selectedThread && (
              <div className="p-4 w-full max-w-2xl self-start">
                <h2 className="text-lg font-semibold text-foreground mb-2">
                  {selectedThread.title}
                </h2>
                <dl className="text-sm space-y-1">
                  <div className="flex gap-2">
                    <dt className="text-muted-foreground">Sensitivity:</dt>
                    <dd>{selectedThread.sensitivity}</dd>
                  </div>
                  <div className="flex gap-2">
                    <dt className="text-muted-foreground">Created:</dt>
                    <dd>{selectedThread.created_at}</dd>
                  </div>
                  <div className="flex gap-2">
                    <dt className="text-muted-foreground">Archived:</dt>
                    <dd>{selectedThread.archived ? 'Yes' : 'No'}</dd>
                  </div>
                </dl>
              </div>
            )}
            {selected && !detailError && !selectedThread && (
              <p className="text-sm text-muted-foreground">Loading thread…</p>
            )}
            {!selected && (
              <div className="flex flex-col items-center gap-2">
                <MessageSquare className="h-8 w-8 opacity-30" aria-hidden="true" />
                <p className="text-sm">Select a thread or start a new Personal chat</p>
              </div>
            )}
          </div>
        </main>
      )}

      {/* Encrypted Vault panel */}
      {activeTab === 'vault' && (
        <div className="flex-1 overflow-hidden">
          <PersonalEncryptedVaultPanel />
        </div>
      )}
    </div>
  )
}
