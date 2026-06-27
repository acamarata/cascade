/**
 * Purpose: Persist chat messages, trying the daemon API first and falling back
 *   to localStorage on API error or when the daemon is unreachable.
 *   Loads prior history on mount, appends on each new message, trims to 50,
 *   and exposes a clear function. SSR-safe (typeof window guard).
 *   Ported from cascade-dashboard GPChatPanel (T-P3-E02-20).
 * Inputs:  sessionId string — must be non-empty; used as storage key.
 *          namespace string (optional) — routing namespace, defaults to 'personal'.
 * Outputs: { messages, append, clear }
 * Constraints: Max 50 messages (rolling; oldest trimmed). JSON.parse wrapped in
 *   try/catch — corrupt storage yields empty array. API errors are silently
 *   swallowed; localStorage is always the sync initial state and fallback.
 * SPORT: E-P9-03 in-app chat — useChatHistory
 */
import { useState, useCallback, useEffect } from 'react'

export interface ChatMessage {
  /** Speaker role */
  role: 'user' | 'assistant'
  /** Plain-text or markdown content */
  content: string
  /** Unix timestamp (ms) */
  ts: number
  /** Optional tool invocation results from the assistant turn */
  toolResults?: unknown[]
  /** Which provider served this response (from served_by SSE event) */
  servedBy?: string
  /** True when the provider is a local LLM (provider id starts with "local:") */
  isLocalFallback?: boolean
}

export interface UseChatHistoryResult {
  messages: ChatMessage[]
  /** Append a message and persist. Trims history to MAX_MESSAGES. */
  append: (msg: ChatMessage) => void
  /** Remove all messages for this session from storage and reset state. */
  clear: () => void
}

/** Maximum number of messages retained per session. */
const MAX_MESSAGES = 50

const DAEMON_BASE_URL =
  typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
    ? 'http://127.0.0.1:9761'
    : ''

/** Build the localStorage key for a given session. */
function storageKey(sessionId: string): string {
  return `cascade-chat-history-${sessionId}`
}

/** Read messages from localStorage; returns [] on missing key or parse failure. */
function loadFromStorage(sessionId: string): ChatMessage[] {
  if (typeof window === 'undefined') return []
  try {
    const raw = window.localStorage.getItem(storageKey(sessionId))
    if (!raw) return []
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed as ChatMessage[]
  } catch {
    return []
  }
}

/** Write messages array to localStorage. */
function saveToStorage(sessionId: string, messages: ChatMessage[]): void {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(storageKey(sessionId), JSON.stringify(messages))
  } catch {
    // Quota exceeded or private-browsing restriction — silently ignore.
  }
}

/** Delete session from localStorage. */
function clearFromStorage(sessionId: string): void {
  if (typeof window !== 'undefined') {
    window.localStorage.removeItem(storageKey(sessionId))
  }
}

export function useChatHistory(
  sessionId: string,
  namespace?: string,
): UseChatHistoryResult {
  const ns = namespace ?? 'personal'

  const [messages, setMessages] = useState<ChatMessage[]>(() =>
    loadFromStorage(sessionId),
  )

  // On mount / sessionId change: try API first, fall back to localStorage.
  useEffect(() => {
    // Optimistically load from localStorage while API request is in-flight.
    setMessages(loadFromStorage(sessionId))

    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(
          `${DAEMON_BASE_URL}/api/memory/chat?namespace=${encodeURIComponent(ns)}&session_id=${encodeURIComponent(sessionId)}`,
        )
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const data = (await res.json()) as { messages?: ChatMessage[] }
        if (!cancelled) {
          setMessages(data.messages ?? [])
        }
      } catch {
        // API unavailable — keep localStorage snapshot loaded above.
      }
    })()

    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, ns])

  const append = useCallback(
    (msg: ChatMessage) => {
      setMessages((prev) => {
        const next = [...prev, msg].slice(-MAX_MESSAGES)
        // Async persist to API; fall back to localStorage on failure.
        ;(async () => {
          try {
            const res = await fetch(`${DAEMON_BASE_URL}/api/memory/chat`, {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ namespace: ns, session_id: sessionId, messages: next }),
            })
            if (!res.ok) throw new Error(`HTTP ${res.status}`)
          } catch {
            saveToStorage(sessionId, next)
          }
        })()
        return next
      })
    },
    [sessionId, ns],
  )

  const clear = useCallback(() => {
    setMessages([])
    ;(async () => {
      try {
        const res = await fetch(
          `${DAEMON_BASE_URL}/api/memory/chat?namespace=${encodeURIComponent(ns)}&session_id=${encodeURIComponent(sessionId)}`,
          { method: 'DELETE' },
        )
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
      } catch {
        clearFromStorage(sessionId)
      }
    })()
  }, [sessionId, ns])

  return { messages, append, clear }
}
