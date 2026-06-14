/**
 * Purpose: Persist chat messages in localStorage keyed by session_id.
 *   Loads prior history on mount, appends on each new message, trims to 50,
 *   and exposes a clear function. SSR-safe (typeof window guard).
 *   Ported from cascade-dashboard GPChatPanel (T-P3-E02-20).
 * Inputs: sessionId string — must be non-empty; used as localStorage key suffix.
 * Outputs: { messages, append, clear }
 * Constraints: Max 50 messages (rolling; oldest trimmed). JSON.parse wrapped in
 *   try/catch — corrupt storage yields empty array.
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
  /** Remove all messages for this session from localStorage and reset state. */
  clear: () => void
}

/** Maximum number of messages retained per session. */
const MAX_MESSAGES = 50

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

export function useChatHistory(sessionId: string): UseChatHistoryResult {
  const [messages, setMessages] = useState<ChatMessage[]>(() =>
    loadFromStorage(sessionId),
  )

  // Re-load when sessionId changes (switching sessions).
  useEffect(() => {
    setMessages(loadFromStorage(sessionId))
  }, [sessionId])

  const append = useCallback(
    (msg: ChatMessage) => {
      setMessages((prev) => {
        const next = [...prev, msg].slice(-MAX_MESSAGES)
        saveToStorage(sessionId, next)
        return next
      })
    },
    [sessionId],
  )

  const clear = useCallback(() => {
    if (typeof window !== 'undefined') {
      window.localStorage.removeItem(storageKey(sessionId))
    }
    setMessages([])
  }, [sessionId])

  return { messages, append, clear }
}
