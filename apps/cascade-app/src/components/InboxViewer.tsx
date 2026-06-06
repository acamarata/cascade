/**
 * Purpose: Inbox viewer — list and read PCI inbox messages from registered paths.
 * Inputs:  Inbox paths from AppConfig; messages fetched via `list_inbox` command
 * Outputs: Prioritized message list with subject, sender, type, preview
 * Constraints:
 *   - Refreshes on mount and every 30s.
 *   - Priority coloring: critical=red, high=orange, medium=yellow, low=muted.
 *   - Messages are read-only — no reply UI in this component (future sprint).
 * SPORT: MASTER-COMPONENTS.md — InboxViewer
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { get as getConfig } from '../lib/config'
import { Inbox, RefreshCw, AlertTriangle, ArrowUp, Minus, ArrowDown } from 'lucide-react'

interface InboxMessage {
  id: string
  subject: string
  from: string
  priority: 'critical' | 'high' | 'medium' | 'low'
  message_type: string
  created_at: string
  body_preview: string
}

const priorityMeta: Record<
  InboxMessage['priority'],
  { label: string; className: string; Icon: typeof AlertTriangle }
> = {
  critical: {
    label: 'Critical',
    className: 'text-red-600 dark:text-red-400',
    Icon: AlertTriangle,
  },
  high: {
    label: 'High',
    className: 'text-orange-600 dark:text-orange-400',
    Icon: ArrowUp,
  },
  medium: {
    label: 'Medium',
    className: 'text-yellow-600 dark:text-yellow-400',
    Icon: Minus,
  },
  low: {
    label: 'Low',
    className: 'text-muted-foreground',
    Icon: ArrowDown,
  },
}

export function InboxViewer() {
  const [messages, setMessages] = useState<InboxMessage[]>([])
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<InboxMessage | null>(null)
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const refresh = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      // Get registered inbox paths from config, then fetch messages for each.
      const config = await getConfig()
      const allMessages: InboxMessage[] = []
      for (const path of config.inbox_paths) {
        const msgs = await invoke<InboxMessage[]>('list_inbox', { inboxPath: path })
        allMessages.push(...msgs)
      }
      // Sort: critical → high → medium → low, then by created_at desc.
      const order = { critical: 0, high: 1, medium: 2, low: 3 }
      allMessages.sort((a, b) => {
        const pd = order[a.priority] - order[b.priority]
        if (pd !== 0) return pd
        return b.created_at.localeCompare(a.created_at)
      })
      setMessages(allMessages)
    } catch (err) {
      setError(String(err))
    } finally {
      setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    refresh()
    intervalRef.current = setInterval(refresh, 30_000)
    return () => {
      if (intervalRef.current) clearInterval(intervalRef.current)
    }
  }, [refresh])

  return (
    <div className="flex h-full gap-3">
      {/* Message list */}
      <section aria-label="Inbox messages" className="flex w-72 shrink-0 flex-col gap-2">
        <div className="flex items-center justify-between">
          <h1 className="flex items-center gap-2 text-lg font-semibold">
            <Inbox className="h-5 w-5" aria-hidden="true" />
            Inbox
            {messages.length > 0 && (
              <span className="rounded-full bg-primary/15 px-2 py-0.5 text-xs text-primary">
                {messages.length}
              </span>
            )}
          </h1>
          <button
            type="button"
            onClick={refresh}
            disabled={isLoading}
            className="rounded-md p-1.5 hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50"
            aria-label="Refresh inbox"
          >
            <RefreshCw
              className={['h-4 w-4', isLoading ? 'animate-spin' : ''].join(' ')}
              aria-hidden="true"
            />
          </button>
        </div>

        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}

        <ul className="flex flex-col gap-1 overflow-auto" aria-label="Message list">
          {messages.length === 0 && !isLoading && (
            <li className="text-sm text-muted-foreground">Inbox is empty.</li>
          )}
          {messages.map((msg) => {
            const meta = priorityMeta[msg.priority]
            const PIcon = meta.Icon
            return (
              <li key={msg.id}>
                <button
                  type="button"
                  onClick={() => setSelected(msg)}
                  className={[
                    'w-full rounded-md border px-3 py-2.5 text-left text-sm transition-colors',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                    selected?.id === msg.id
                      ? 'border-primary bg-primary/10'
                      : 'border-border bg-card hover:bg-accent',
                  ].join(' ')}
                  aria-pressed={selected?.id === msg.id}
                >
                  <div className="flex items-center justify-between gap-2 mb-0.5">
                    <span className="truncate font-medium">{msg.subject}</span>
                    <span
                      className={['flex items-center gap-0.5 text-xs', meta.className].join(' ')}
                      aria-label={`Priority: ${meta.label}`}
                    >
                      <PIcon className="h-3 w-3" aria-hidden="true" />
                      {meta.label}
                    </span>
                  </div>
                  <div className="flex items-center gap-1 text-xs text-muted-foreground">
                    <span>{msg.from}</span>
                    <span aria-hidden="true">·</span>
                    <span>{msg.message_type}</span>
                  </div>
                </button>
              </li>
            )
          })}
        </ul>
      </section>

      {/* Message detail */}
      <section
        aria-label="Selected message"
        className="flex-1 overflow-auto rounded-lg border border-border bg-card p-4"
      >
        {selected ? (
          <article>
            <header className="mb-4 space-y-1 border-b border-border pb-4">
              <h2 className="text-base font-semibold">{selected.subject}</h2>
              <dl className="text-xs text-muted-foreground space-y-0.5">
                <div className="flex gap-1.5">
                  <dt className="w-10 shrink-0">From</dt>
                  <dd>{selected.from}</dd>
                </div>
                <div className="flex gap-1.5">
                  <dt className="w-10 shrink-0">Type</dt>
                  <dd className="capitalize">{selected.message_type}</dd>
                </div>
                <div className="flex gap-1.5">
                  <dt className="w-10 shrink-0">Date</dt>
                  <dd>{selected.created_at}</dd>
                </div>
              </dl>
            </header>
            <p className="whitespace-pre-wrap text-sm leading-relaxed">{selected.body_preview}</p>
          </article>
        ) : (
          <p className="text-sm text-muted-foreground">Select a message to read it.</p>
        )}
      </section>
    </div>
  )
}
