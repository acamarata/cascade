/**
 * Purpose: SSE streaming chat client for the cascade daemon /api/chat endpoint.
 *   Manages in-flight messages, streaming state, and appends completed assistant
 *   turns to useChatHistory on stream end.
 * Inputs: sessionId string (passed to POST body + useChatHistory).
 * Outputs: { messages, isStreaming, error, sendMessage, clearMessages }
 * Constraints: Uses fetch + ReadableStream (no EventSource — POST body required).
 *   Parses SSE lines manually. Cleans up reader on unmount via abortRef.
 *   No markdown processing here — that is T-P3-E02-22.
 * SPORT: T-P3-E02-21 GP Chat React UI
 */
import { useState, useCallback, useRef } from 'react'
import { useChatHistory, type ChatMessage } from '@/hooks/useChatHistory'
import type { ChatStreamEvent } from '@/lib/api'

// ── Pure SSE parser (exported for unit tests) ──────────────────────────────

/**
 * Parse a single SSE line block (may be several `data:` lines joined by \n\n).
 * Returns null if the block is a comment, keepalive, or empty.
 */
export function parseSseLine(rawLine: string): ChatStreamEvent | null {
  const trimmed = rawLine.trim()
  if (!trimmed || trimmed.startsWith(':')) return null

  for (const line of trimmed.split('\n')) {
    if (!line.startsWith('data:')) continue
    const payload = line.slice(5).trim()
    if (payload === '[DONE]') return { type: 'done' }
    try {
      const parsed: unknown = JSON.parse(payload)
      if (
        parsed !== null &&
        typeof parsed === 'object' &&
        'type' in parsed
      ) {
        return parsed as ChatStreamEvent
      }
    } catch {
      // malformed — skip
    }
  }
  return null
}

// ── Hook ──────────────────────────────────────────────────────────────────

export interface UseChatResult {
  messages: ChatMessage[]
  isStreaming: boolean
  error: string | null
  sendMessage: (content: string) => Promise<void>
  clearMessages: () => void
}

export function useChat(sessionId: string): UseChatResult {
  const { messages, append, clear } = useChatHistory(sessionId)

  // In-flight streaming assistant message (not yet persisted)
  const [streamingContent, setStreamingContent] = useState<string | null>(null)
  const [isStreaming, setIsStreaming] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // AbortController ref so we can cancel on unmount
  const abortRef = useRef<AbortController | null>(null)

  // Combined view: persisted messages + in-flight partial
  const allMessages: ChatMessage[] = streamingContent !== null
    ? [
        ...messages,
        { role: 'assistant' as const, content: streamingContent, ts: Date.now() },
      ]
    : messages

  const sendMessage = useCallback(
    async (content: string): Promise<void> => {
      if (isStreaming) return

      const userMsg: ChatMessage = { role: 'user', content, ts: Date.now() }
      append(userMsg)

      setIsStreaming(true)
      setError(null)
      setStreamingContent('')

      const controller = new AbortController()
      abortRef.current = controller

      // Build history for context (last 20 turns to cap token cost)
      const contextMsgs = [...messages, userMsg].slice(-20).map((m) => ({
        role: m.role,
        content: m.content,
      }))

      let accumulated = ''

      try {
        const res = await fetch('/api/chat', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ messages: contextMsgs, session_id: sessionId }),
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
            if (event.type === 'token' && typeof event.content === 'string') {
              accumulated += event.content
              setStreamingContent(accumulated)
            }
            if (event.type === 'error') {
              throw new Error(
                typeof event.message === 'string' ? event.message : 'Stream error',
              )
            }
          }
        }

        // Persist the completed assistant message
        const assistantMsg: ChatMessage = {
          role: 'assistant',
          content: accumulated,
          ts: Date.now(),
        }
        append(assistantMsg)
      } catch (err: unknown) {
        if (err instanceof Error && err.name === 'AbortError') return
        setError(err instanceof Error ? err.message : String(err))
      } finally {
        setStreamingContent(null)
        setIsStreaming(false)
        abortRef.current = null
      }
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [isStreaming, messages, sessionId, append],
  )

  const clearMessages = useCallback(() => {
    // Cancel any in-flight stream
    abortRef.current?.abort()
    setStreamingContent(null)
    setIsStreaming(false)
    setError(null)
    clear()
  }, [clear])

  return { messages: allMessages, isStreaming, error, sendMessage, clearMessages }
}
