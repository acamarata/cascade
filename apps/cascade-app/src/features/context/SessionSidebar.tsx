/**
 * Purpose: Left sidebar for the Context panel — lists all saved context sessions
 *   with item count badges; allows creating, renaming (inline), and deleting sessions.
 * Inputs:  sessions, activeId, onSelect, onCreate, onDelete callbacks from ContextPanel.
 * Outputs: <aside> landmark with session list + "New Session" button.
 * Constraints:
 *   - Inline session name creation: click "New Session" → input appears, Enter/blur saves.
 *   - Delete requires confirmation only if session has items (prevent accidental deletion).
 *   - a11y: list items are keyboard-navigable; delete button has aria-label.
 * SPORT: MASTER-COMPONENTS.md — SessionSidebar (T-P3-E07-10)
 */

import { useRef, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import type { ContextMeta } from '../../types/context'

// ── props ─────────────────────────────────────────────────────────────────────

interface SessionSidebarProps {
  sessions: ContextMeta[]
  activeId: string | null
  onSelect: (id: string) => void
  onCreate: (title: string) => Promise<void>
  onDelete: (id: string) => Promise<void>
}

// ── component ─────────────────────────────────────────────────────────────────

/**
 * Left sidebar: session list with badge counts and a "New Session" button.
 * Inline name entry on create — focus trap kept minimal for P3.
 */
export function SessionSidebar({
  sessions,
  activeId,
  onSelect,
  onCreate,
  onDelete,
}: SessionSidebarProps) {
  const [creating, setCreating] = useState(false)
  const [newTitle, setNewTitle] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  // ── new session flow ─────────────────────────────────────────────────────
  function handleNewClick() {
    setCreating(true)
    setNewTitle('')
    // Let the DOM settle before focusing.
    setTimeout(() => inputRef.current?.focus(), 0)
  }

  async function handleCreateCommit() {
    const title = newTitle.trim() || 'Untitled Session'
    setCreating(false)
    setNewTitle('')
    await onCreate(title)
  }

  function handleCreateKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter') {
      e.preventDefault()
      void handleCreateCommit()
    } else if (e.key === 'Escape') {
      setCreating(false)
      setNewTitle('')
    }
  }

  // ── delete handler ───────────────────────────────────────────────────────
  async function handleDelete(e: React.MouseEvent, session: ContextMeta) {
    e.stopPropagation()
    const hasItems = session.itemCount > 0
    if (hasItems) {
      const confirmed = window.confirm(
        `Delete "${session.title}" and its ${session.itemCount} item(s)?`
      )
      if (!confirmed) return
    }
    await onDelete(session.id)
  }

  return (
    <aside
      aria-label="Context sessions"
      className="flex w-56 shrink-0 flex-col border-r border-border bg-card"
    >
      {/* Header */}
      <div className="flex h-12 items-center justify-between border-b border-border px-3">
        <span className="text-sm font-semibold text-foreground">Sessions</span>
        <button
          type="button"
          onClick={handleNewClick}
          aria-label="New session"
          className="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
        >
          <Plus className="h-4 w-4" aria-hidden="true" />
        </button>
      </div>

      {/* Session list */}
      <ul className="flex flex-1 flex-col gap-0.5 overflow-y-auto p-2">
        {/* Inline create input */}
        {creating && (
          <li>
            <input
              ref={inputRef}
              type="text"
              value={newTitle}
              onChange={(e) => setNewTitle(e.target.value)}
              onBlur={() => void handleCreateCommit()}
              onKeyDown={handleCreateKeyDown}
              placeholder="Session name…"
              aria-label="New session name"
              className="w-full rounded-md border border-border bg-background px-2 py-1.5 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
            />
          </li>
        )}

        {/* Empty state */}
        {sessions.length === 0 && !creating && (
          <li className="px-2 py-4 text-center text-xs text-muted-foreground">
            No sessions yet.
            <br />
            Click + to create one.
          </li>
        )}

        {/* Session rows */}
        {sessions.map((session) => {
          const isActive = session.id === activeId
          return (
            <li key={session.id}>
              <button
                type="button"
                onClick={() => onSelect(session.id)}
                aria-current={isActive ? 'page' : undefined}
                className={[
                  'group flex w-full items-center justify-between rounded-md px-2 py-1.5 text-left text-sm transition-colors',
                  isActive
                    ? 'bg-accent text-accent-foreground'
                    : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
                ].join(' ')}
              >
                <span className="truncate flex-1">{session.title || 'Untitled'}</span>
                <span className="ml-2 shrink-0 rounded-full bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
                  {session.itemCount}
                </span>
                <button
                  type="button"
                  onClick={(e) => void handleDelete(e, session)}
                  aria-label={`Delete session "${session.title}"`}
                  tabIndex={-1}
                  className="ml-1 hidden shrink-0 rounded p-0.5 text-destructive opacity-0 transition-opacity group-hover:opacity-100 group-hover:block hover:bg-destructive/10 focus:opacity-100"
                >
                  <Trash2 className="h-3 w-3" aria-hidden="true" />
                </button>
              </button>
            </li>
          )
        })}
      </ul>
    </aside>
  )
}
