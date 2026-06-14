/**
 * Purpose: In-app prompt box page for the Tauri desktop app (E-P9-03).
 *   Replaces the DashboardPage placeholder. Full-page chat interface:
 *   message list (user/assistant), streaming token display, markdown + code
 *   highlighting, provider/model selector, local-LLM fallback indicator,
 *   new-chat / clear, session persistence.
 *
 *   Routing: requests are sent through the cascade daemon's routed /api/chat
 *   endpoint (127.0.0.1:9761) following the P7 E-P7-07 priority chain:
 *   selected > default > healthy cloud > local fallback.
 *
 *   The prompt box runs with cascade context available — tools exposed by the
 *   daemon (provide_harness_context, search, pbd tools) are dispatched server-side.
 *
 * Inputs:  None (standalone route page).
 * Outputs: Full-height flex chat panel inside the AppLayout main area.
 * Constraints: Session id is stable per browser session (sessionStorage key).
 *   Provider selector is optional — null means auto (daemon routing decides).
 *   a11y: main landmark, live regions, aria-labels on all interactive elements.
 * SPORT: E-P9-03 in-app chat — ChatPage, /chat route
 */
import { useRef, useState, useId } from 'react'
import { MessageSquare, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { useChat } from './useChat'
import { MessageList } from './MessageList'
import { ChatInput } from './ChatInput'
import { ProviderSelector } from './ProviderSelector'

// ── Session id helpers ────────────────────────────────────────────────────────

/** Generate a short random session id. */
function newSessionId(): string {
  return `chat-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

const SESSION_STORAGE_KEY = 'cascade-app-chat-session-id'

/** Get or create a stable session id for this browser session. */
function getOrCreateSessionId(): string {
  if (typeof window === 'undefined') return newSessionId()
  try {
    const existing = window.sessionStorage.getItem(SESSION_STORAGE_KEY)
    if (existing) return existing
    const id = newSessionId()
    window.sessionStorage.setItem(SESSION_STORAGE_KEY, id)
    return id
  } catch {
    return newSessionId()
  }
}

// ── ChatPage ──────────────────────────────────────────────────────────────────

export function ChatPage() {
  const headingId = useId()
  // Stable session id across re-renders; reset on new-chat
  const [sessionId, setSessionId] = useState(() => getOrCreateSessionId())
  const [selectedProvider, setSelectedProvider] = useState<string | null>(null)

  const { messages, isStreaming, error, servedBy, sendMessage, clearMessages } =
    useChat(sessionId)

  // Track last served provider for header display
  const lastServedBy = servedBy
    ?? messages.filter((m) => m.role === 'assistant' && m.servedBy).at(-1)?.servedBy
    ?? null

  const isLocalFallback = lastServedBy?.startsWith('local:') ?? false

  // Auto-focus input after mount
  const chatInputRef = useRef<HTMLDivElement>(null)

  function handleNewChat() {
    clearMessages()
    const id = newSessionId()
    try {
      window.sessionStorage.setItem(SESSION_STORAGE_KEY, id)
    } catch { /* ignore */ }
    setSessionId(id)
  }

  function handleSend(content: string) {
    sendMessage(content, selectedProvider ?? undefined)
  }

  return (
    <main
      id="main-content"
      aria-labelledby={headingId}
      className="flex flex-col h-full overflow-hidden"
    >
      {/* ── Header ─────────────────────────────────────────────── */}
      <div
        className={cn(
          'flex items-center justify-between px-4 py-2 shrink-0',
          'border-b border-border bg-card',
        )}
      >
        <div className="flex items-center gap-2">
          <MessageSquare className="h-4 w-4 text-primary" aria-hidden="true" />
          <h1 id={headingId} className="text-sm font-semibold">
            Chat
          </h1>
          {/* Active provider indicator */}
          {lastServedBy && (
            <span
              className={cn(
                'text-[0.65rem] px-1.5 py-0.5 rounded-full font-medium border',
                isLocalFallback
                  ? 'bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/30'
                  : 'bg-muted text-muted-foreground border-muted-foreground/20',
              )}
              aria-label={`Served by ${lastServedBy}${isLocalFallback ? ' (local fallback)' : ''}`}
            >
              {isLocalFallback
                ? `⚡ ${lastServedBy.replace(/^local:/, '')} (local)`
                : lastServedBy}
            </span>
          )}
        </div>

        <div className="flex items-center gap-2">
          {/* Provider selector — null = auto (daemon routing) */}
          <ProviderSelector
            selectedProvider={selectedProvider}
            onSelect={setSelectedProvider}
          />
          {/* New chat */}
          <Button
            variant="ghost"
            size="sm"
            onClick={handleNewChat}
            disabled={isStreaming}
            aria-label="Start a new chat session"
            className="h-7 gap-1.5 text-xs"
          >
            <RotateCcw className="h-3 w-3" aria-hidden="true" />
            <span className="hidden sm:inline">New chat</span>
          </Button>
        </div>
      </div>

      {/* ── Error banner ───────────────────────────────────────── */}
      {error && (
        <div
          role="alert"
          className="px-4 py-2 text-xs text-destructive bg-destructive/10 border-b border-destructive/20 shrink-0"
        >
          {error}
        </div>
      )}

      {/* ── Message list ───────────────────────────────────────── */}
      <div className="flex-1 overflow-hidden">
        <MessageList messages={messages} isStreaming={isStreaming} />
      </div>

      {/* ── Streaming indicator ────────────────────────────────── */}
      {isStreaming && (
        <div
          role="status"
          aria-live="polite"
          className="px-4 py-1 text-[0.65rem] text-muted-foreground/60 shrink-0"
        >
          Receiving…
        </div>
      )}

      {/* ── Input ──────────────────────────────────────────────── */}
      <div ref={chatInputRef}>
        <ChatInput onSend={handleSend} isStreaming={isStreaming} />
      </div>
    </main>
  )
}
