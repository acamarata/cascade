/**
 * Purpose: Chat client that routes to the GP proxy at port 3762 (Anthropic-compat,
 *   near-free Gemini pool) by default, falling back to the cascade daemon at 9761
 *   (SSE streaming, Conductor-routed T2/T3 Claude) when the proxy is unreachable
 *   OR reports pool exhaustion.
 *
 * Primary path (port 3762):
 *   POST http://127.0.0.1:3762/v1/messages — Anthropic Messages API format.
 *   Returns synchronous JSON: { content: [{type:"text", text:"..."}], ... }
 *   Model: "claude-sonnet-5" (canonical Sonnet id, see cascade-core::model_ids;
 *   the GP adapter maps this to a Gemini Flash model at the proxy).
 *   Health-gated: any non-2xx response (network error, "all Gemini providers
 *   exhausted", or any other upstream failure) is treated as unhealthy and
 *   triggers the fallback path below — chat never silently fails.
 *   Privacy-gated: protected namespaces (personal / personal:private) skip
 *   this path entirely (see isProtectedNamespace) — private chat never
 *   leaves via the GP pool.
 *
 * Fallback path (port 9761):
 *   POST http://127.0.0.1:9761/api/chat — daemon SSE endpoint. This is the same
 *   provider-registry routing chain used by `cascade conductor` (explicit
 *   provider > routing-table default > first healthy cloud > local fallback),
 *   so a GP outage transparently lands on the next-best Claude account/tier.
 *   SSE event types: served_by | token | tool_result | error | data [DONE].
 *
 * Inputs: sessionId string, optional namespace string, optional selectedProvider string override.
 * Outputs: { messages, isStreaming, error, servedBy, sendMessage, clearMessages }
 * SPORT: E-P9-03 in-app chat — useChat
 */
import { useState, useCallback, useRef } from 'react'
import { useChatHistory, type ChatMessage } from './useChatHistory'

// ── Endpoints ─────────────────────────────────────────────────────────────────
const GP_PROXY_URL = 'http://127.0.0.1:3762'
/** Canonical Sonnet model id (cascade-core::model_ids::MODEL_CLAUDE_SONNET). */
const DEFAULT_CHAT_MODEL = 'claude-sonnet-5'
const DAEMON_BASE_URL =
  typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window
    ? 'http://127.0.0.1:9761'
    : ''

// ── Privacy: protected namespaces ─────────────────────────────────────────────

/**
 * Mirror of the daemon's `is_protected_namespace` (chat_handlers.rs).
 *
 * `personal` (consent-gated persistence) and `personal:private` (private
 * mode — "not saved to chat history or Cascade memory") chats must never
 * leave the machine via the GP pool (:3762 → Google). For these namespaces
 * the GP fast-path is skipped entirely; the request goes to the daemon,
 * which applies the same guard to its own GP-first preference and routes
 * through its normal chain (routing default → healthy cloud → local).
 */
export function isProtectedNamespace(namespace?: string): boolean {
  if (!namespace) return false
  const ns = namespace.trim().toLowerCase()
  return (
    ns === 'private' ||
    ns.endsWith(':private') ||
    ns === 'personal' ||
    ns.startsWith('personal:')
  )
}

// ── SSE event types (daemon fallback) ─────────────────────────────────────────

export interface ServedByEvent   { type: 'served_by';   provider: string }
export interface TokenEvent      { type: 'token';        text: string; content?: string }
export interface ToolResultEvent { type: 'tool_result';  name?: string; tool_name?: string; [k: string]: unknown }
export interface ErrorEvent      { type: 'error';        message: string }
export interface DoneEvent       { type: 'done' }

export type ChatStreamEvent =
  | ServedByEvent | TokenEvent | ToolResultEvent | ErrorEvent | DoneEvent

// ── SSE parser (daemon fallback) ──────────────────────────────────────────────

export function parseSseLine(rawLine: string): ChatStreamEvent | null {
  const trimmed = rawLine.trim()
  if (!trimmed || trimmed.startsWith(':')) return null

  let eventType: string | null = null
  for (const line of trimmed.split('\n')) {
    if (line.startsWith('event:')) { eventType = line.slice(6).trim(); break }
  }
  for (const line of trimmed.split('\n')) {
    if (!line.startsWith('data:')) continue
    const payload = line.slice(5).trim()
    if (payload === '[DONE]') return { type: 'done' }
    try {
      const parsed = JSON.parse(payload) as Record<string, unknown>
      const resolvedType = (eventType ?? parsed['type']) as string | undefined
      if (!resolvedType) return null
      return { ...parsed, type: resolvedType } as ChatStreamEvent
    } catch { /* malformed */ }
  }
  return null
}

export function streamingReducer(accumulated: string, event: ChatStreamEvent): string {
  if (event.type === 'token') {
    const frag = typeof event.text === 'string' ? event.text
      : typeof (event as TokenEvent).content === 'string' ? (event as TokenEvent).content!
      : ''
    return accumulated + frag
  }
  return accumulated
}

// ── Hook ──────────────────────────────────────────────────────────────────────

export interface UseChatResult {
  messages: ChatMessage[]
  isStreaming: boolean
  error: string | null
  servedBy: string | null
  sendMessage: (content: string, provider?: string) => Promise<void>
  clearMessages: () => void
}

export function useChat(sessionId: string, namespace?: string): UseChatResult {
  const { messages, append, clear } = useChatHistory(sessionId, namespace)

  const [streamingContent, setStreamingContent] = useState<string | null>(null)
  const [isStreaming, setIsStreaming] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [servedBy, setServedBy] = useState<string | null>(null)
  const toolResultsRef = useRef<unknown[]>([])
  const abortRef = useRef<AbortController | null>(null)

  const allMessages: ChatMessage[] = streamingContent !== null
    ? [
        ...messages,
        {
          role: 'assistant' as const,
          content: streamingContent,
          ts: Date.now(),
          servedBy: servedBy ?? undefined,
          isLocalFallback: false,
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

      const contextMsgs = [...messages, userMsg].slice(-20).map((m) => ({
        role: m.role,
        content: m.content,
      }))

      let accumulated = ''
      let resolvedProvider: string | null = null

      // The user's explicit provider selection (ProviderSelector, "Auto" = null)
      // always wins — skip the GP fast-path entirely so the request goes
      // straight to the daemon's routing chain with `provider` pinned.
      const userForcedProvider = Boolean(provider)
      // Protected namespaces (personal / personal:private) never take the GP
      // fast-path: private chat must not leave via the Google-backed pool.
      // The daemon applies the same guard server-side, so falling through to
      // it cannot re-introduce the leak one hop later.
      const protectedNs = isProtectedNamespace(namespace)

      try {
        // ── Primary: GP proxy (Anthropic-compat, near-free Gemini pool) ──────
        let gpSuccess = false
        if (!userForcedProvider && !protectedNs) {
          try {
            const gpRes = await fetch(`${GP_PROXY_URL}/v1/messages`, {
              method: 'POST',
              headers: {
                'Content-Type': 'application/json',
                ...(namespace ? { 'X-Cascade-Namespace': namespace } : {}),
              },
              body: JSON.stringify({
                model: DEFAULT_CHAT_MODEL,
                max_tokens: 4096,
                messages: contextMsgs,
              }),
              signal: controller.signal,
            })

            // Health gate: any non-2xx (unreachable, "all Gemini providers
            // exhausted", or other upstream failure) falls through to the
            // Conductor-routed daemon fallback below — never a silent failure.
            if (gpRes.ok) {
              type AnthropicContent = { type: string; text?: string }
              type AnthropicResponse = { content?: AnthropicContent[]; model?: string }
              const body = await gpRes.json() as AnthropicResponse
              const textBlock = body.content?.find((b) => b.type === 'text')
              accumulated = textBlock?.text ?? ''
              resolvedProvider = `gp:${body.model ?? 'gemini-2.0-flash'}`
              setServedBy(resolvedProvider)
              // Simulate token-by-token reveal so UI feels responsive
              const words = accumulated.split(' ')
              let revealed = ''
              for (const word of words) {
                revealed += (revealed ? ' ' : '') + word
                setStreamingContent(revealed)
                // Yield to browser between words (non-blocking, ~0ms each)
                await new Promise<void>((r) => setTimeout(r, 0))
              }
              gpSuccess = true
            }
          } catch (gpErr: unknown) {
            if (gpErr instanceof Error && gpErr.name === 'AbortError') throw gpErr
            // GP proxy unreachable — fall through to daemon
          }
        }

        // ── Fallback: cascade daemon SSE at port 9761 (Conductor-routed) ─────
        if (!gpSuccess) {
          const res = await fetch(`${DAEMON_BASE_URL}/api/chat`, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              messages: contextMsgs,
              session_id: sessionId,
              ...(namespace ? { namespace } : {}),
              ...(provider ? { provider } : {}),
            }),
            signal: controller.signal,
          })

          if (!res.ok) throw new Error(`HTTP ${res.status} ${res.statusText}`)
          if (!res.body) throw new Error('Response body is null')

          const reader = res.body.getReader()
          const decoder = new TextDecoder()
          let buffer = ''

          while (true) {
            const { done, value } = await reader.read()
            if (done) break
            buffer += decoder.decode(value, { stream: true })
            const parts = buffer.split('\n\n')
            buffer = parts.pop() ?? ''
            for (const part of parts) {
              const event = parseSseLine(part)
              if (!event) continue
              if (event.type === 'done') { reader.cancel(); break }
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
              if (event.type === 'error') throw new Error(event.message)
            }
          }
        }

        append({
          role: 'assistant',
          content: accumulated,
          ts: Date.now(),
          toolResults: toolResultsRef.current.length > 0 ? toolResultsRef.current : undefined,
          servedBy: resolvedProvider ?? undefined,
          isLocalFallback: resolvedProvider?.startsWith('local:') ?? false,
        })
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
