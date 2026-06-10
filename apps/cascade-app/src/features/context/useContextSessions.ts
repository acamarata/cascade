/**
 * Purpose: IPC hook for context session CRUD — list (ContextMeta), get (full session),
 *   upsert, delete. Calls context_list / context_get / context_upsert / context_delete
 *   Tauri invoke. Auto-saves active session on item mutations with 500ms debounce.
 * Inputs:  None at hook call time; actions exposed via returned API.
 * Outputs: { sessions, activeSession, loading, error, selectSession, createSession,
 *            renameSession, upsertSession, deleteSession, addItem, removeItem }
 * Constraints:
 *   - context_list returns Vec<ContextMeta> (no items). context_get loads full session.
 *   - Auto-save debounce: 500 ms on session state change; timer cleared on unmount.
 *   - In test/non-Tauri environments invoke is mocked; hook must not crash.
 * SPORT: MASTER-COMPONENTS.md — useContextSessions (T-P3-E07-10)
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type {
  ContextItem,
  ContextMeta,
  ContextSession,
} from '../../types/context'

// ── helpers ───────────────────────────────────────────────────────────────────

function generateId(): string {
  return `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

function slugify(title: string): string {
  return title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 48) || `session-${Date.now()}`
}

// ── types ─────────────────────────────────────────────────────────────────────

export interface UseContextSessionsResult {
  /** Lightweight session list (no items body — use activeSession for items). */
  sessions: ContextMeta[]
  /** Full session loaded on select (has items). Null when none selected. */
  activeSession: ContextSession | null
  loading: boolean
  error: string | null
  /** Select a session by id — triggers context_get to load full session. */
  selectSession: (id: string) => void
  /** Create a new empty session with the given title. */
  createSession: (title: string) => Promise<void>
  /** Rename the currently active session. */
  renameSession: (title: string) => void
  /** Immediately upsert the active session to disk (bypasses debounce). */
  upsertSession: () => Promise<void>
  /** Delete a session by id. */
  deleteSession: (id: string) => Promise<void>
  /** Append an item to the active session (triggers debounced save). */
  addItem: (item: Omit<ContextItem, 'id' | 'created_at'>) => void
  /** Remove an item from the active session (triggers debounced save). */
  removeItem: (itemId: string) => void
}

// ── hook ──────────────────────────────────────────────────────────────────────

/**
 * Manages context sessions: list metadata from disk, CRUD operations, debounced auto-save.
 * Calls context_list for the sidebar list; context_get when a session is selected.
 *
 * @example
 * ```tsx
 * const { sessions, activeSession, createSession, addItem } = useContextSessions()
 * ```
 */
export function useContextSessions(): UseContextSessionsResult {
  const [sessions, setSessions] = useState<ContextMeta[]>([])
  const [activeSession, setActiveSession] = useState<ContextSession | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  // Debounce timer ref — cleared on unmount to prevent stale writes.
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  // Track latest active session for the debounce closure without re-registering effect.
  const activeSessionRef = useRef<ContextSession | null>(null)
  activeSessionRef.current = activeSession

  // ── fetch session list on mount ──────────────────────────────────────────
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    // context_list returns Vec<ContextMeta> directly (Rust: camelCase serde).
    invoke<ContextMeta[]>('context_list', {})
      .then((result) => {
        if (!cancelled) {
          setSessions(result)
          setLoading(false)
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          // Graceful degradation: empty list when daemon is not running.
          setSessions([])
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [])

  // ── cleanup debounce on unmount ──────────────────────────────────────────
  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [])

  // ── schedule debounced save whenever active session changes ───────────────
  const scheduleSave = useCallback(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      const session = activeSessionRef.current
      if (!session) return
      invoke('context_upsert', { session }).catch(() => {
        // Silent — error surfaced on explicit save.
      })
    }, 500)
  }, [])

  // ── selectSession ─── triggers context_get to load full session ──────────
  const selectSession = useCallback((id: string) => {
    invoke<ContextSession>('context_get', { id })
      .then((session) => {
        setActiveSession(session)
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : String(err))
      })
  }, [])

  // ── createSession ────────────────────────────────────────────────────────
  const createSession = useCallback(async (title: string) => {
    const now = new Date().toISOString()
    const newSession: ContextSession = {
      id: slugify(title) || generateId(),
      title,
      items: [],
      created_at: now,
      updated_at: now,
    }
    const newMeta: ContextMeta = {
      id: newSession.id,
      title: newSession.title,
      itemCount: 0,
    }
    try {
      await invoke('context_upsert', { session: newSession })
      setSessions((prev) => [newMeta, ...prev])
      setActiveSession(newSession)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  // ── renameSession ────────────────────────────────────────────────────────
  const renameSession = useCallback(
    (title: string) => {
      setActiveSession((prev) => {
        if (!prev) return prev
        const updated = { ...prev, title, updated_at: new Date().toISOString() }
        setSessions((s) => s.map((x) => (x.id === updated.id ? { ...x, title } : x)))
        return updated
      })
      scheduleSave()
    },
    [scheduleSave],
  )

  // ── upsertSession (immediate) ─────────────────────────────────────────────
  const upsertSession = useCallback(async () => {
    if (!activeSessionRef.current) return
    if (debounceRef.current) clearTimeout(debounceRef.current)
    try {
      await invoke('context_upsert', { session: activeSessionRef.current })
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  // ── deleteSession ────────────────────────────────────────────────────────
  const deleteSession = useCallback(async (id: string) => {
    try {
      await invoke('context_delete', { id })
      setSessions((prev) => prev.filter((s) => s.id !== id))
      setActiveSession((prev) => (prev?.id === id ? null : prev))
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    }
  }, [])

  // ── addItem ──────────────────────────────────────────────────────────────
  const addItem = useCallback(
    (item: Omit<ContextItem, 'id' | 'created_at'>) => {
      const newItem: ContextItem = {
        ...item,
        id: generateId(),
        created_at: new Date().toISOString(),
      }
      setActiveSession((prev) => {
        if (!prev) return prev
        const updated = {
          ...prev,
          items: [...prev.items, newItem],
          updated_at: new Date().toISOString(),
        }
        setSessions((s) =>
          s.map((x) => (x.id === updated.id ? { ...x, itemCount: updated.items.length } : x)),
        )
        return updated
      })
      scheduleSave()
    },
    [scheduleSave],
  )

  // ── removeItem ───────────────────────────────────────────────────────────
  const removeItem = useCallback(
    (itemId: string) => {
      setActiveSession((prev) => {
        if (!prev) return prev
        const updated = {
          ...prev,
          items: prev.items.filter((i) => i.id !== itemId),
          updated_at: new Date().toISOString(),
        }
        setSessions((s) =>
          s.map((x) => (x.id === updated.id ? { ...x, itemCount: updated.items.length } : x)),
        )
        return updated
      })
      scheduleSave()
    },
    [scheduleSave],
  )

  return {
    sessions,
    activeSession,
    loading,
    error,
    selectSession,
    createSession,
    renameSession,
    upsertSession,
    deleteSession,
    addItem,
    removeItem,
  }
}
