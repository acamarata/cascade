/**
 * Purpose: Full Context page — SessionSidebar (left) + session detail (right).
 *   Right panel: session title (inline edit), item list, CodebasePack section.
 *   All session mutations go through useContextSessions with debounced auto-save.
 * Inputs:  None (self-contained page; reads IPC via useContextSessions).
 * Outputs: Full-height two-pane layout: sidebar + detail.
 * Constraints:
 *   - Session title edit: click to show input; Enter/blur commits rename.
 *   - Empty state: placeholder when no session is selected.
 *   - a11y: main landmark, session heading is h2, Pack section is aside.
 * SPORT: MASTER-COMPONENTS.md — ContextPanel (T-P3-E07-10)
 */

import { useRef, useState } from 'react'
import { Pencil } from 'lucide-react'
import { CodebasePack } from './CodebasePack'
import { ItemList } from './ItemList'
import { SessionSidebar } from './SessionSidebar'
import { useContextSessions } from './useContextSessions'

// ── component ─────────────────────────────────────────────────────────────────

/**
 * Context page — two-pane layout: session list sidebar + session detail.
 * Mounts the CodebasePack section below the item list.
 */
export function ContextPanel() {
  const {
    sessions,
    activeSession,
    loading,
    error,
    selectSession,
    createSession,
    renameSession,
    deleteSession,
    addItem,
    removeItem,
  } = useContextSessions()

  // ── inline session rename state ──────────────────────────────────────────
  const [editingTitle, setEditingTitle] = useState(false)
  const [titleDraft, setTitleDraft] = useState('')
  const titleInputRef = useRef<HTMLInputElement>(null)

  function handleEditTitleClick() {
    if (!activeSession) return
    setTitleDraft(activeSession.title)
    setEditingTitle(true)
    setTimeout(() => titleInputRef.current?.focus(), 0)
  }

  function handleTitleCommit() {
    const title = titleDraft.trim()
    if (title && activeSession) {
      renameSession(title)
    }
    setEditingTitle(false)
  }

  function handleTitleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter') {
      e.preventDefault()
      handleTitleCommit()
    } else if (e.key === 'Escape') {
      setEditingTitle(false)
    }
  }

  // ── render ───────────────────────────────────────────────────────────────
  return (
    <div className="flex h-full overflow-hidden">
      {/* Left sidebar: session list */}
      <SessionSidebar
        sessions={sessions}
        activeId={activeSession?.id ?? null}
        onSelect={selectSession}
        onCreate={createSession}
        onDelete={deleteSession}
      />

      {/* Right: session detail */}
      <main
        id="context-detail"
        aria-label="Session detail"
        className="flex flex-1 flex-col overflow-y-auto p-6"
      >
        {/* Loading */}
        {loading && (
          <div className="flex flex-1 items-center justify-center">
            <span className="text-sm text-muted-foreground">Loading sessions…</span>
          </div>
        )}

        {/* Error banner (non-blocking — shown above content) */}
        {error && !loading && (
          <div
            role="alert"
            className="mb-4 rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive"
          >
            {error}
          </div>
        )}

        {/* Empty state — no session selected */}
        {!loading && !activeSession && (
          <div className="flex flex-1 flex-col items-center justify-center gap-2 text-muted-foreground">
            <p className="text-sm">Select a session or create a new one.</p>
          </div>
        )}

        {/* Session detail */}
        {!loading && activeSession && (
          <div className="flex flex-col gap-6">
            {/* Session heading — click to rename */}
            <div className="flex items-center gap-2">
              {editingTitle ? (
                <input
                  ref={titleInputRef}
                  type="text"
                  value={titleDraft}
                  onChange={(e) => setTitleDraft(e.target.value)}
                  onBlur={handleTitleCommit}
                  onKeyDown={handleTitleKeyDown}
                  aria-label="Rename session"
                  className="flex-1 rounded-md border border-border bg-background px-2 py-1 text-lg font-semibold text-foreground outline-none focus:ring-2 focus:ring-ring"
                />
              ) : (
                <button
                  type="button"
                  onClick={handleEditTitleClick}
                  aria-label={`Rename session: ${activeSession.title}`}
                  className="group flex items-center gap-2 text-left"
                >
                  <h2 className="text-lg font-semibold text-foreground">
                    {activeSession.title || 'Untitled'}
                  </h2>
                  <Pencil
                    className="h-4 w-4 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100"
                    aria-hidden="true"
                  />
                </button>
              )}
            </div>

            {/* Pinned items */}
            <section aria-labelledby="items-heading">
              <h3 id="items-heading" className="mb-3 text-sm font-medium text-muted-foreground">
                Pinned items ({activeSession.items.length})
              </h3>
              <ItemList
                items={activeSession.items}
                onAdd={addItem}
                onRemove={removeItem}
              />
            </section>

            {/* Divider */}
            <hr className="border-border" />

            {/* Codebase pack */}
            <CodebasePack />
          </div>
        )}
      </main>
    </div>
  )
}
