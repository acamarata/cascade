/**
 * Purpose: Inbox page — loads cascade_inbox_list via Tauri IPC and renders
 *   inbox items with expand-on-click, priority badges, and graceful fallback
 *   when the daemon is not running.
 * Inputs:  None — fetches via Tauri invoke on mount.
 * Outputs: Full-height page listing InboxItems; supports loading/empty/error states.
 * Constraints:
 *   - Gracefully handles daemon-offline: shows empty state, no crash.
 *   - No external UI library; Tailwind + dark theme only.
 *   - Expanding an item is inline (no modal/drawer).
 * SPORT: MASTER-ROUTES.md — /inbox
 */

import React, { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { Inbox, ChevronDown, ChevronUp } from 'lucide-react'

// ── Types ─────────────────────────────────────────────────────────────────────

interface InboxItem {
  id: string
  subject: string
  body: string
  priority: string
  created_at: string
  read: boolean
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/**
 * Returns Tailwind classes for the priority badge.
 *
 * @param priority - "high" | "med" | "low" or any string
 */
function priorityClasses(priority: string): string {
  switch (priority.toLowerCase()) {
    case 'high':
      return 'bg-red-500/20 text-red-400 border border-red-500/40'
    case 'med':
    case 'medium':
      return 'bg-yellow-500/20 text-yellow-400 border border-yellow-500/40'
    default:
      return 'bg-zinc-700/60 text-zinc-400 border border-zinc-600/40'
  }
}

/**
 * Formats an ISO timestamp to a human-readable local string.
 *
 * @param iso - ISO 8601 date string
 */
function formatDate(iso: string): string {
  try {
    return new Intl.DateTimeFormat(undefined, {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    }).format(new Date(iso))
  } catch {
    return iso
  }
}

// ── Sub-components ────────────────────────────────────────────────────────────

interface InboxItemRowProps {
  item: InboxItem
  expanded: boolean
  onToggle: (id: string) => void
}

/**
 * Single inbox item row — collapses to title+preview, expands to full body.
 *
 * @param item - The inbox item to render.
 * @param expanded - Whether the full body is visible.
 * @param onToggle - Called with the item id to toggle expansion.
 */
function InboxItemRow({ item, expanded, onToggle }: InboxItemRowProps): React.ReactElement {
  const preview = item.body.length > 100 ? item.body.slice(0, 100) + '…' : item.body

  return (
    <button
      type="button"
      onClick={() => onToggle(item.id)}
      className={[
        'w-full text-left rounded-lg border px-4 py-3 transition-colors',
        'focus:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        item.read
          ? 'border-border bg-card hover:bg-accent/40'
          : 'border-border bg-card/80 hover:bg-accent/40',
      ].join(' ')}
      aria-expanded={expanded}
    >
      {/* Top row: subject + meta */}
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-center gap-2 min-w-0">
          {!item.read && (
            <span className="mt-0.5 h-2 w-2 shrink-0 rounded-full bg-blue-500" aria-label="Unread" />
          )}
          <span
            className={[
              'truncate text-sm font-medium leading-snug',
              item.read ? 'text-foreground/80' : 'text-foreground',
            ].join(' ')}
          >
            {item.subject}
          </span>
        </div>

        <div className="flex shrink-0 items-center gap-2">
          <span
            className={[
              'rounded px-1.5 py-0.5 text-xs font-medium uppercase tracking-wide',
              priorityClasses(item.priority),
            ].join(' ')}
          >
            {item.priority}
          </span>
          <span className="text-xs text-muted-foreground whitespace-nowrap">
            {formatDate(item.created_at)}
          </span>
          {expanded ? (
            <ChevronUp className="h-4 w-4 text-muted-foreground" />
          ) : (
            <ChevronDown className="h-4 w-4 text-muted-foreground" />
          )}
        </div>
      </div>

      {/* Body — preview when collapsed, full when expanded */}
      <div className="mt-1.5 text-sm text-muted-foreground leading-relaxed">
        {expanded ? (
          <pre className="whitespace-pre-wrap font-sans">{item.body}</pre>
        ) : (
          <span>{preview}</span>
        )}
      </div>
    </button>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

/**
 * Inbox page — fetches cascade_inbox_list on mount and renders the item list.
 * Silently falls back to an empty list if the daemon is offline or the command
 * errors, so the page never crashes.
 *
 * @example
 * ```tsx
 * <Route path="/inbox" element={<InboxPage />} />
 * ```
 */
export function InboxPage(): React.ReactElement {
  const [items, setItems] = useState<InboxItem[]>([])
  const [loading, setLoading] = useState(true)
  const [expandedIds, setExpandedIds] = useState<Set<string>>(new Set())

  useEffect(() => {
    let cancelled = false

    async function load() {
      try {
        const result = await invoke<unknown>('cascade_inbox_list')
        if (!cancelled && Array.isArray(result)) {
          setItems(result as InboxItem[])
        }
      } catch {
        // Daemon offline or command not available — show empty state silently.
      } finally {
        if (!cancelled) setLoading(false)
      }
    }

    void load()
    return () => {
      cancelled = true
    }
  }, [])

  const handleToggle = useCallback((id: string) => {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (next.has(id)) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }, [])

  const unreadCount = items.filter((i) => !i.read).length

  return (
    <div className="flex h-full flex-col">
      {/* Page header */}
      <header className="flex shrink-0 items-center gap-3 border-b border-border px-6 py-4">
        <Inbox className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-xl font-semibold">Inbox</h1>
        {!loading && unreadCount > 0 && (
          <span className="flex h-5 min-w-5 items-center justify-center rounded-full bg-blue-500 px-1.5 text-xs font-semibold text-white">
            {unreadCount}
          </span>
        )}
        {!loading && items.length > 0 && (
          <span className="ml-auto text-sm text-muted-foreground">
            {items.length} message{items.length !== 1 ? 's' : ''}
          </span>
        )}
      </header>

      {/* Content */}
      <div className="flex-1 overflow-y-auto px-6 py-4">
        {loading ? (
          <div className="flex items-center justify-center py-16 text-sm text-muted-foreground">
            Loading…
          </div>
        ) : items.length === 0 ? (
          <div className="flex flex-col items-center justify-center gap-3 py-16 text-center">
            <Inbox className="h-10 w-10 text-muted-foreground/40" aria-hidden="true" />
            <p className="text-sm text-muted-foreground">No messages</p>
          </div>
        ) : (
          <ul className="flex flex-col gap-2" role="list">
            {items.map((item) => (
              <li key={item.id}>
                <InboxItemRow
                  item={item}
                  expanded={expandedIds.has(item.id)}
                  onToggle={handleToggle}
                />
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}
