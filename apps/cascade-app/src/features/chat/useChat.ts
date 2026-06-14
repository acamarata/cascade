/**
 * Purpose: SSE streaming chat client for the cascade daemon /api/chat endpoint
 *   at 127.0.0.1:9761 (the daemon's HTTP server, per E-P7-07 routing).
 *   Ported from cascade-dashboard GPChatPanel (T-P3-E02-21), adapted for the
 *   Tauri desktop context where fetch hits the daemon directly (no Vite proxy).
 *
 * Provider selection order (E-P7-07):
 *   1. explicit `provider` param → selected field in request body
 *   2. ProviderRegistry default_for_task(Chat)
 *   3. First healthy cloud provider
 *   4. First registered local:<id> provider (offline fallback)
 *   5. SSE error event — nothing available
 *
 * SSE event types emitted by daemon:
 *   - served_by    → { provider: string } — which backend answered
 *   - token        → { text: string } — streamed fragment
 *   - tool_result  → tool execution payload
 *   - error        → { message: string }
 *   - data [DONE]  → final sentinel
 *
 * Inputs: sessionId string, optional selectedProvider string override.
 * Outputs: { messages, isStreaming, error, servedBy, sendMessage, clearMessages }
 * Constraints: Uses fetch + ReadableStream (not EventSource — POST body required).
 *   SSE lines parsed manually. AbortController cleanup on unmount.
 * SPORT: E-P9-03 in-app chat — useChat
 */
import { useState, useCallback, useRef } from 'react'
import { useChatHistory, type ChatMessage } from './useChatHistory'

// ── Daemon base URL ───────────────────────────────────────────────────────────
// The cascade daemon HTTP server listens on 127.0.0.1:9761 (dashboard.rs).
// In the Tauri app there is no Vite dev-proxy, so we target the daemon directly.
// In test (jsdom) we use a relative path so mocked fetch works without a real server.
const DAEMON_BASE_URL =
  typeof window !== 'undefined' &&
  '__TAURI_INTERNALS__' in window
    ? 'http://127.0.0.1:9761'
    : ''

// ── SSE event types ───────────────────────────────────────────────────────────

export interface ServedByEvent {
  type: 'served_by'
  provider: string
}

export interface TokenEvent {
  type: 'token'
  text: string
  // Also accept "content" for dashboard compat
  content?: string
}

export interface ToolResultEvent {
  type: 'tool_result'
  name?: string
  tool_name?: string
  [key: string]: unknown
}

export interface ErrorEvent {
  type: 'error'
  message: string
}

export interface DoneEvent {
  type: 'done'
}

export type ChatStreamEvent =
  | ServedByEvent
  | TokenEvent
  | ToolResultEvent
  | ErrorEvent
  | DoneEvent

// ── Pure SSE line parser (exported for unit tests) ────────────────────────────

/**
 * Parse a single SSE line block (may be several `data:` lines joined by \n\n).
 * Handles `event:` prefixes (served_by, token, tool_result, error) as well as
 * bare `data:` lines for the [DONE] sentinel.
 * Returns null for comments, keepalives, or empty blocks.
 */
export function parseSseLine(rawLine: string): ChatStreamEvent | null {
  const trimmed = rawLine.trim()
  if (!trimmed || trimmed.startsWith(':')) return null

  // Extract event type from `event:` line (if present)
  let eventType: string | null = null
  for (const line of trimmed.split('\n')) {
    if (line.startsWith('event:')) {
      eventType = line.slice(6).trim()
      break
    }
  }

  // Extract data from `data:` line
  for (const line of trimmed.split('\n')) {
    if (!line.startsWith('data:')) continue
    const payload = line.slice(5).trim()
    if (payload === '[DONE]') return { type: 'done' }
    try {
      const parsed: unknown = JSON.parse(payload)
      if (parsed !== null && typeof parsed === 'object') {
        const obj = parsed as Record<string, unknown>
        // Use eventType from `event:` header if present, else from `type` field
        const resolvedType = (eventType ?? obj['type']) as string | undefined
        if (!resolvedType) return null
        return { ...obj, type: resolvedType } as ChatStreamEvent
      }
    } catch {
      // malformed — skip
    }
  }
  return null
}

// ── Streaming text reducer (exported for unit tests) ──────────────────────────

/**
 * Pure reducer for the accumulated streaming text buffer.
 * Appends the fragment from a token event.
 */
export function streamingReducer(
  accumulated: string,
  event: ChatStreamEvent,
): string {
  if (event.type === 'token') {
    // Support both `text` (daemon) and `content` (dashboard compat)
    const fragment =
      typeof event.text === 'string'
        ? event.text
        : typeof (event as TokenEvent).content === 'string'
          ? (event as TokenEvent).content!
          : ''
    return accumulated + fragment
  }
  return accumulated
}

// ── Hook ──────────────────────────────────────────────────────────────────────

export interface UseChatResult {
  messages: ChatMessage[]
  isStreaming: boolean
  error: string | null
  /** Provider id that served the last (or current) response. */
  servedBy: string | null
  sendMessage: (content: string, provider?: string) => Promise<void>
  clearMessages: () => void
}

export function useChat(sessionId: string): UseChatResult {
  const { messages, append, clear } = useChatHistory(sessionId)

  // In-flight streaming assistant message (not yet persisted)
  const [streamingContent, setStreamingContent] = useState<string | null>(null)
  const [isStreaming, setIsStreaming] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [servedBy, setServedBy] = useState<string | null>(null)
  // Track tool results accumulated during current stream
  const toolResultsRef = useRef<unknown[]>([])

  // AbortController ref so we can cancel on unmount
  const abortRef = useRef<AbortController | null>(null)

  // Combined view: persisted messages + in-flight partial
  const allMessages: ChatMessage[] = streamingContent !== null
    ? [
        ...messages,
        {
          role: 'assistant' as const,
          content: streamingContent,
          ts: Date.now(),
          servedBy: servedBy ?? undefined,
          isLocalFallback: servedBy?.startsWith('local:') ?? false,
        },
      ]
    : messages

  const sendMessage = useCallback(
    async (content: string, provider?: string): Promise<void> => {
      if (isStreaming) return

      const userMsg: ChatMessage = { role: 'user', content, ts: Date.now() }
      append(userMsg)

      setIsStreaming(true)
      setError(null)
      setStreamingContent('')
      toolResultsRef.current = []

      const controller = new AbortController()
      abortRef.current = controller

      // Build history for context (last 20 turns to cap token cost)
      const contextMsgs = [...messages, userMsg].slice(-20).map((m) => ({
        role: m.role,
        content: m.content,
      }))

      let accumulated = ''
      let resolvedProvider: string | null = null

      try {
        const res = await fetch(`${DAEMON_BASE_URL}/api/chat`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            messages: contextMsgs,
            session_id: sessionId,
            ...(provider ? { provider } : {}),
          }),
          signal: controller.signal,
        })

        if (!res.ok) {
          throw new Error(`HTTP ${res.status} ${res.statusText}`)
        }
        if (!res.body) {
          throw new Error('Response body is null')
        }

        const reader = res.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''

        // eslint-disable-next-line no-constant-condition
        while (true) {
          const { done, value } = await reader.read()
          if (done) break

          buffer += decoder.decode(value, { stream: true })
          const parts = buffer.split('\n\n')
          // Last element may be incomplete — keep in buffer
          buffer = parts.pop() ?? ''

          for (const part of parts) {
            const event = parseSseLine(part)
            if (!event) continue
            if (event.type === 'done') {
              reader.cancel()
              break
            }
            if (event.type === 'served_by') {
              resolvedProvider = event.provider
              setServedBy(event.provider)
            }
            if (event.type === 'token') {
              accumulated = streamingReducer(accumulated, event)
              setStreamingContent(accumulated)
            }
            if (event.type === 'tool_result') {
              toolResultsRef.current = [...toolResultsRef.current, event]
            }
            if (event.type === 'error') {
              throw new Error(event.message)
            }
          }
        }

        // Persist the completed assistant message
        const assistantMsg: ChatMessage = {
          role: 'assistant',
          content: accumulated,
          ts: Date.now(),
          toolResults: toolResultsRef.current.length > 0 ? toolResultsRef.current : undefined,
          servedBy: resolvedProvider ?? undefined,
          isLocalFallback: resolvedProvider?.startsWith('local:') ?? false,
        }
        append(assistantMsg)
      } catch (err: unknown) {
        if (err instanceof Error && err.name === 'AbortError') return
        setError(err instanceof Error ? err.message : String(err))
      } finally {
        setStreamingContent(null)
        setIsStreaming(false)
        abortRef.current = null
        toolResultsRef.current = []
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [isStreaming, messages, sessionId, append],
  )

  const clearMessages = useCallback(() => {
    abortRef.current?.abort()
    setStreamingContent(null)
    setIsStreaming(false)
    setError(null)
    setServedBy(null)
    clear()
  }, [clear])

  return { messages: allMessages, isStreaming, error, servedBy, sendMessage, clearMessages }
}
