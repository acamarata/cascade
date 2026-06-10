/**
 * Purpose: Renders the list of pinned context items in the active session.
 *   Provides per-item type icons, display content, and a remove button.
 *   Also provides inline "Add" UIs for file, note, and URL items.
 * Inputs:  items array, onAdd, onRemove callbacks from ContextPanel.
 * Outputs: Ordered list of item rows + add-item controls at the bottom.
 * Constraints:
 *   - Add File: Tauri dialog.open({ multiple: false }) → adds file item.
 *   - Add Note: inline Textarea; on blur (with content) appends note item.
 *   - Add URL: inline Input; on Enter/blur with valid URL appends url item.
 *   - Items must have aria labels for screen readers.
 * SPORT: MASTER-COMPONENTS.md — ItemList (T-P3-E07-10)
 */

import { useRef, useState } from 'react'
import { open as dialogOpen } from '@tauri-apps/plugin-dialog'
import { File, Link, Plus, StickyNote, Trash2 } from 'lucide-react'
import type { ContextItem, ContextItemType } from '../../types/context'

// ── helpers ───────────────────────────────────────────────────────────────────

/**
 * Returns a human-readable label for a context item based on its type.
 * File: relative path basename. Note: first line. URL: hostname.
 */
export function getItemLabel(item: ContextItem): string {
  switch (item.item_type) {
    case 'file': {
      const p = item.path ?? ''
      return p.split('/').pop() || p || '(no path)'
    }
    case 'note':
      return (item.content ?? '').split('\n')[0]?.trim() || '(empty note)'
    case 'url': {
      try {
        return new URL(item.url ?? '').hostname
      } catch {
        return item.url ?? '(no url)'
      }
    }
  }
}

/**
 * Returns the full display text for an item (used as title/tooltip).
 */
export function getItemDetail(item: ContextItem): string {
  switch (item.item_type) {
    case 'file':
      return item.path ?? ''
    case 'note':
      return item.content ?? ''
    case 'url':
      return item.url ?? ''
  }
}

function ItemIcon({ type }: { type: ContextItemType }) {
  switch (type) {
    case 'file':
      return <File className="h-4 w-4 shrink-0 text-blue-500" aria-hidden="true" />
    case 'note':
      return <StickyNote className="h-4 w-4 shrink-0 text-amber-500" aria-hidden="true" />
    case 'url':
      return <Link className="h-4 w-4 shrink-0 text-green-500" aria-hidden="true" />
  }
}

// ── props ─────────────────────────────────────────────────────────────────────

interface ItemListProps {
  items: ContextItem[]
  onAdd: (item: Omit<ContextItem, 'id' | 'created_at'>) => void
  onRemove: (itemId: string) => void
}

// ── component ─────────────────────────────────────────────────────────────────

/**
 * Renders pinned items + add-item controls (file, note, URL) for an active session.
 */
export function ItemList({ items, onAdd, onRemove }: ItemListProps) {
  const [addMode, setAddMode] = useState<'note' | 'url' | null>(null)
  const [noteText, setNoteText] = useState('')
  const [urlText, setUrlText] = useState('')
  const noteRef = useRef<HTMLTextAreaElement>(null)
  const urlRef = useRef<HTMLInputElement>(null)

  // ── add file (Tauri dialog) ─────────────────────────────────────────────
  async function handleAddFile() {
    try {
      const selected = await dialogOpen({ multiple: false, directory: false })
      if (selected && typeof selected === 'string') {
        onAdd({ item_type: 'file', path: selected })
      }
    } catch {
      // User cancelled dialog — no-op.
    }
  }

  // ── add note ────────────────────────────────────────────────────────────
  function handleNoteBlur() {
    const content = noteText.trim()
    if (content) {
      onAdd({ item_type: 'note', content })
    }
    setNoteText('')
    setAddMode(null)
  }

  function handleNoteKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Escape') {
      setNoteText('')
      setAddMode(null)
    }
  }

  // ── add URL ──────────────────────────────────────────────────────────────
  function handleUrlCommit() {
    const url = urlText.trim()
    if (url) {
      onAdd({ item_type: 'url', url })
    }
    setUrlText('')
    setAddMode(null)
  }

  function handleUrlKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter') {
      e.preventDefault()
      handleUrlCommit()
    } else if (e.key === 'Escape') {
      setUrlText('')
      setAddMode(null)
    }
  }

  // ── open note/url inline inputs with focus ───────────────────────────────
  function openAddMode(mode: 'note' | 'url') {
    setAddMode(mode)
    setTimeout(() => {
      if (mode === 'note') noteRef.current?.focus()
      else urlRef.current?.focus()
    }, 0)
  }

  return (
    <div className="flex flex-col gap-2">
      {/* Item list */}
      {items.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">
          No items pinned. Add a file, note, or URL below.
        </p>
      ) : (
        <ul role="list" className="flex flex-col gap-1">
          {items.map((item) => (
            <li
              key={item.id}
              className="group flex items-center gap-2 rounded-md border border-border bg-card px-3 py-2"
            >
              <ItemIcon type={item.item_type} />
              <span
                className="flex-1 truncate text-sm text-foreground"
                title={getItemDetail(item)}
              >
                {getItemLabel(item)}
              </span>
              <button
                type="button"
                onClick={() => onRemove(item.id)}
                aria-label={`Remove ${item.item_type} item: ${getItemLabel(item)}`}
                className="shrink-0 rounded p-1 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 hover:bg-destructive/10 hover:text-destructive focus:opacity-100"
              >
                <Trash2 className="h-3 w-3" aria-hidden="true" />
              </button>
            </li>
          ))}
        </ul>
      )}

      {/* Inline note input */}
      {addMode === 'note' && (
        <textarea
          ref={noteRef}
          value={noteText}
          onChange={(e) => setNoteText(e.target.value)}
          onBlur={handleNoteBlur}
          onKeyDown={handleNoteKeyDown}
          placeholder="Type your note… (Tab to finish, Esc to cancel)"
          aria-label="New note content"
          rows={3}
          className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring resize-none"
        />
      )}

      {/* Inline URL input */}
      {addMode === 'url' && (
        <input
          ref={urlRef}
          type="url"
          value={urlText}
          onChange={(e) => setUrlText(e.target.value)}
          onBlur={handleUrlCommit}
          onKeyDown={handleUrlKeyDown}
          placeholder="https://example.com"
          aria-label="New URL"
          className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground outline-none focus:ring-2 focus:ring-ring"
        />
      )}

      {/* Add-item buttons */}
      <div className="flex gap-2 pt-1" role="group" aria-label="Add item">
        <button
          type="button"
          onClick={() => void handleAddFile()}
          className="flex items-center gap-1.5 rounded-md border border-border bg-card px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
        >
          <Plus className="h-3 w-3" aria-hidden="true" />
          Add File
        </button>
        <button
          type="button"
          onClick={() => openAddMode('note')}
          className="flex items-center gap-1.5 rounded-md border border-border bg-card px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
        >
          <Plus className="h-3 w-3" aria-hidden="true" />
          Add Note
        </button>
        <button
          type="button"
          onClick={() => openAddMode('url')}
          className="flex items-center gap-1.5 rounded-md border border-border bg-card px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
        >
          <Plus className="h-3 w-3" aria-hidden="true" />
          Add URL
        </button>
      </div>
    </div>
  )
}
