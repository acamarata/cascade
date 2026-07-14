/**
 * Purpose: Persist chat messages, trying the daemon API first and falling back
 *   to localStorage on API error or when the daemon is unreachable.
 *   Loads prior history on mount, appends on each new message, trims to 50,
 *   and exposes a clear function. SSR-safe (typeof window guard).
 *   Ported from cascade-dashboard GPChatPanel (T-P3-E02-20).
 * Inputs:  sessionId string — must be non-empty; used as storage key.
 *          namespace string (optional) — chat scope key ('personal' | 'meta' |
 *          'projects:<id>'), defaults to 'personal'. Mapped to the memory-API's
 *          namespace format (personal/meta/dev-<slug>) via toMemoryNamespace().
 * Outputs: { messages, append, clear }
 * Constraints: Max 50 messages (rolling; oldest trimmed). JSON.parse wrapped in
 *   try/catch — corrupt storage yields empty array. API errors are silently
 *   swallowed; localStorage is always the sync initial state and fallback.
 *   The daemon's /api/memory/chat endpoint requires BOTH `scope` and `namespace`
 *   query/body params (400 without them) and firewalls the "personal" namespace
 *   behind `opt_in: true` (403 without it) — see cascade-rag memory::namespace.
 *   The "personal" namespace additionally requires REAL user consent: the
 *   `memory.personalChatSync` setting (default false, read via get_settings
 *   IPC). Without it, personal chat never contacts the daemon at all
 *   (localStorage-only) — opt_in is asserted only when the user has explicitly
 *   enabled sync in Settings. clear() also purges the server-side copy
 *   (DELETE /api/memory/chat) whenever server persistence is active, so
 *   "Clear chat" is never silently local-only.
 * SPORT: E-P9-03 in-app chat — useChatHistory
 */
import { useState, useCallback, useEffect } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { CascadeSettings } from '../../types/settings'

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
  typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window ? 'http://127.0.0.1:9761' : ''

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

/** Sentinel namespace meaning "never sync to the daemon — local-only". */
const PRIVATE_NAMESPACE = 'personal:private'

/**
 * Map a chat-scope namespace key ('personal' | 'meta' | 'projects:<id>' |
 * 'personal:private') to the memory API's validated namespace format
 * ('personal' | 'meta' | 'dev-<slug>'). Falls back to 'personal' for
 * unrecognized input. Private mode is handled separately by isPrivateNamespace()
 * before this is called — it never reaches the daemon.
 */
function toMemoryNamespace(ns: string): string {
  if (ns === 'personal' || ns === 'meta') return ns
  if (ns.startsWith('projects:')) {
    const slug = ns
      .slice('projects:'.length)
      .toLowerCase()
      .replace(/[^a-z0-9-]/g, '-')
      .replace(/^-+|-+$/g, '')
    return `dev-${slug || 'default'}`
  }
  return 'personal'
}

/** True when the mapped namespace requires the personal-firewall opt_in flag. */
function needsOptIn(memoryNamespace: string): boolean {
  return memoryNamespace === 'personal'
}

/** True when running inside the Tauri shell (settings IPC available). */
function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
}

/**
 * Read the `memory.personalChatSync` consent setting via the get_settings IPC
 * (same pattern PersonalVaultContext uses for `context.personalRoot`).
 * Defaults to false — no Tauri shell, IPC failure, or absent field all mean
 * "no consent": personal chat stays on-device.
 */
export async function loadPersonalChatSync(): Promise<boolean> {
  if (!isTauri()) return false
  try {
    const settings = await invoke<CascadeSettings>('get_settings')
    return settings.memory?.personalChatSync === true
  } catch {
    return false
  }
}

export function useChatHistory(sessionId: string, namespace?: string): UseChatHistoryResult {
  const ns = namespace ?? 'personal'
  // Private/incognito chat (Personal mode's Private toggle): local-only,
  // never touches the daemon's long-term memory store at all.
  const isPrivate = ns === PRIVATE_NAMESPACE
  const memoryNs = toMemoryNamespace(ns)
  const optIn = needsOptIn(memoryNs)
  // Scope identifies the calling surface — distinct from namespace (the data
  // partition). The in-app chat always scopes itself as "cascade-app".
  const scope = 'cascade-app'

  const [messages, setMessages] = useState<ChatMessage[]>(() => loadFromStorage(sessionId))

  // Consent for syncing the "personal" namespace to the daemon. null =
  // settings not yet resolved (treated as no consent — no daemon calls).
  // Non-personal namespaces never consult this.
  const [personalSyncConsent, setPersonalSyncConsent] = useState<boolean | null>(null)

  useEffect(() => {
    if (isPrivate || memoryNs !== 'personal') return
    let cancelled = false
    void loadPersonalChatSync().then((consent) => {
      if (!cancelled) setPersonalSyncConsent(consent)
    })
    return () => {
      cancelled = true
    }
  }, [memoryNs, isPrivate])

  // Server persistence is active only when not private AND either the
  // namespace is non-personal (meta / dev-*) or the user has explicitly
  // enabled personal chat sync in Settings (memory.personalChatSync).
  const serverSync = !isPrivate && (memoryNs !== 'personal' || personalSyncConsent === true)

  // On mount / sessionId change: try API first, fall back to localStorage.
  // Sessions without active server persistence (private, or personal without
  // sync consent) skip the API entirely — localStorage only.
  useEffect(() => {
    // Optimistically load from localStorage while API request is in-flight.
    setMessages(loadFromStorage(sessionId))
    if (!serverSync) return

    let cancelled = false
    ;(async () => {
      try {
        const params = new URLSearchParams({
          scope,
          namespace: memoryNs,
          opt_in: String(optIn),
        })
        const res = await fetch(`${DAEMON_BASE_URL}/api/memory/chat?${params.toString()}`)
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const data = (await res.json()) as { messages?: ChatMessage[] }
        if (!cancelled) {
          // The daemon stores flat chat rows per-namespace (not per-session), so
          // filter client-side is not needed yet — sessionId partitioning is
          // handled by localStorage only until per-session server storage lands.
          setMessages(data.messages ?? loadFromStorage(sessionId))
        }
      } catch {
        // API unavailable — keep localStorage snapshot loaded above.
      }
    })()

    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId, memoryNs, optIn, serverSync])

  const append = useCallback(
    (msg: ChatMessage) => {
      setMessages((prev) => {
        const next = [...prev, msg].slice(-MAX_MESSAGES)
        // Always keep localStorage in sync (session-scoped persistence); the
        // daemon call is best-effort long-term memory, not the source of truth
        // for a specific session's transcript.
        saveToStorage(sessionId, next)
        if (serverSync) {
          ;(async () => {
            try {
              const res = await fetch(`${DAEMON_BASE_URL}/api/memory/chat`, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({
                  scope,
                  namespace: memoryNs,
                  opt_in: optIn,
                  role: msg.role,
                  content: msg.content,
                }),
              })
              if (!res.ok) throw new Error(`HTTP ${res.status}`)
            } catch {
              // Already persisted to localStorage above — nothing further to do.
            }
          })()
        }
        return next
      })
    },
    [sessionId, memoryNs, optIn, serverSync]
  )

  const clear = useCallback(() => {
    setMessages([])
    clearFromStorage(sessionId)
    // When server persistence is active, purge the server-side copy too so
    // "Clear chat" is never silently local-only. Failure is non-fatal: the
    // local clear stands, but warn that server history may persist.
    if (serverSync) {
      ;(async () => {
        try {
          const params = new URLSearchParams({
            scope,
            namespace: memoryNs,
            opt_in: String(optIn),
          })
          const res = await fetch(`${DAEMON_BASE_URL}/api/memory/chat?${params.toString()}`, {
            method: 'DELETE',
          })
          if (!res.ok) throw new Error(`HTTP ${res.status}`)
        } catch (e) {
          console.warn(
            'Failed to clear chat history on the daemon — server history may persist.',
            e
          )
        }
      })()
    }
  }, [sessionId, memoryNs, optIn, serverSync])

  return { messages, append, clear }
}
