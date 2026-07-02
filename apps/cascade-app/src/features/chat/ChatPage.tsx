/**
 * Purpose: In-app prompt box page for the Tauri desktop app (E-P9-03).
 *   Replaces the DashboardPage placeholder. Full-page chat interface:
 *   message list (user/assistant), streaming token display, markdown + code
 *   highlighting, provider/model selector, local-LLM fallback indicator,
 *   new-chat / clear, session persistence, and scope switcher.
 *
 *   Scope switcher: Personal | Cascade | Projects — three real, visible modes:
 *     - Personal: personal-life assistant, scoped to the personal workspace
 *       (context.personalRoot setting, default ~/Downloads). A Private toggle
 *       nested inside Personal mode routes to the firewalled "personal"
 *       namespace (opt_in required) instead of the default incognito session
 *       (never persisted to the daemon's long-term memory at all — sessionStorage
 *       only). There is no separate top-level "private" mode — it lives inside
 *       Personal per the E-P9-03 scoping contract.
 *     - Cascade: self-referential scope — questions about Cascade's own
 *       settings/accounts/status. Namespace "meta" (cascade's own knowledge).
 *     - Projects: scoped to a registered project directory. Namespace
 *       "dev-<slug>" (per-project developer RAG namespace).
 *   Scope drives the ?scope= URL param and namespace-partitioned sessions.
 *
 *   Routing: requests are sent through the cascade daemon's routed /api/chat
 *   endpoint (127.0.0.1:9761) following the P7 E-P7-07 priority chain:
 *   selected > default > healthy cloud > local fallback. Chat history persists
 *   through /api/memory/chat, scoped+namespaced per cascade-rag's personal
 *   firewall (see useChatHistory.ts). Personal-namespace persistence requires
 *   the memory.personalChatSync setting (default false) — without it, Personal
 *   chat is localStorage-only and a note in the UI says so.
 *
 *   The prompt box runs with cascade context available — tools exposed by the
 *   daemon (provide_harness_context, search, pbd tools) are dispatched server-side.
 *
 * Inputs:  None (standalone route page).
 * Outputs: Full-height flex chat panel inside the AppLayout main area.
 * Constraints: Session id is stable per browser session (sessionStorage key).
 *   Provider selector is optional — null means auto (daemon routing decides).
 *   Private mode is session-only state (not persisted to localStorage) — it
 *   always resets to off on reload, matching incognito semantics.
 *   a11y: main landmark, live regions, aria-labels on all interactive elements.
 * SPORT: E-P9-03 in-app chat — ChatPage, /chat route
 */
import { useRef, useState, useId, useEffect } from 'react'
import { MessageSquare, RotateCcw, Lock } from 'lucide-react'
import { useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { useChat } from './useChat'
import { loadPersonalChatSync } from './useChatHistory'
import { MessageList } from './MessageList'
import { ChatInput } from './ChatInput'
import { ProviderSelector } from './ProviderSelector'
import { useChatScope } from '../../store'
import type { ChatScope } from '../../store/chatScope.slice'

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
  const [searchParams, setSearchParams] = useSearchParams()
  const { chatScope, selectedProjectId, setScope } = useChatScope()

  // Init scope from URL param on first render. All three scopes are real,
  // visible modes — "cascade" is no longer settings-only.
  const urlScope = searchParams.get('scope')
  useEffect(() => {
    if (urlScope === 'personal' || urlScope === 'projects' || urlScope === 'cascade') {
      setScope(urlScope as ChatScope)
    }
    // Only run on mount (urlScope is stable from router).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Stable session id across re-renders; reset on new-chat or scope change.
  const [sessionId, setSessionId] = useState(() => getOrCreateSessionId())
  const [selectedProvider, setSelectedProvider] = useState<string | null>(null)

  // Private/incognito sub-mode — only meaningful inside Personal scope.
  // Session-only (never persisted): reloading the app always starts non-private.
  const [isPrivate, setIsPrivate] = useState(false)

  // Whether the user has consented to syncing Personal chat to Cascade memory
  // (memory.personalChatSync setting, default false). Drives the on-device
  // note shown in Personal mode when sync is off. useChatHistory reads the
  // same setting independently to gate all daemon persistence calls.
  const [personalChatSync, setPersonalChatSync] = useState(false)
  useEffect(() => {
    let cancelled = false
    void loadPersonalChatSync().then((consent) => {
      if (!cancelled) setPersonalChatSync(consent)
    })
    return () => {
      cancelled = true
    }
  }, [])

  // Derive namespace from scope. "personal" (private off) persists to the
  // daemon only with real consent (memory.personalChatSync — see
  // useChatHistory's firewall handling); private mode additionally skips
  // server-side persistence entirely by using a namespace the daemon never
  // long-term stores.
  const namespace =
    chatScope === 'projects'
      ? `projects:${selectedProjectId ?? 'default'}`
      : chatScope === 'cascade'
        ? 'meta'
        : isPrivate
          ? 'personal:private'
          : 'personal'

  const { messages, isStreaming, error, servedBy, sendMessage, clearMessages } =
    useChat(sessionId, namespace)

  // Consume a `seed` URL param (e.g. from AllProjectsBoard's "Plan" action:
  // `/chat?scope=projects&seed=<prompt>`) — auto-send it once as the first
  // message, then strip it from the URL so remounts/refreshes don't resend.
  useEffect(() => {
    const seed = searchParams.get('seed')
    if (!seed) return
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        next.delete('seed')
        return next
      },
      { replace: true },
    )
    void sendMessage(seed, selectedProvider ?? undefined)
    // Only run once on mount — sendMessage/selectedProvider are read at call time.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Track last served provider for header display.
  const lastServedBy =
    servedBy ??
    messages.filter((m) => m.role === 'assistant' && m.servedBy).at(-1)?.servedBy ??
    null

  const isLocalFallback = lastServedBy?.startsWith('local:') ?? false

  const chatInputRef = useRef<HTMLDivElement>(null)

  function handleNewChat() {
    clearMessages()
    const id = newSessionId()
    try {
      window.sessionStorage.setItem(SESSION_STORAGE_KEY, id)
    } catch { /* ignore */ }
    setSessionId(id)
  }

  function handleScopeChange(scope: ChatScope) {
    setScope(scope)
    setSearchParams({ scope }, { replace: true })
    setIsPrivate(false) // leaving/entering a scope always resets private mode
    handleNewChat()
  }

  function handleTogglePrivate() {
    setIsPrivate((prev) => !prev)
    handleNewChat()
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

      {/* ── Scope switcher ─────────────────────────────────────── */}
      <div className="flex items-center justify-between gap-2 px-4 py-1.5 border-b border-border bg-background shrink-0">
        <div className="flex items-center gap-1">
          {(['personal', 'cascade', 'projects'] as const).map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => handleScopeChange(s)}
              aria-pressed={chatScope === s}
              className={cn(
                'px-3 py-1 rounded-full text-xs font-medium transition-colors',
                chatScope === s
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:bg-accent/50 hover:text-foreground',
              )}
            >
              {s === 'personal' ? 'Personal' : s === 'cascade' ? 'Cascade' : 'Projects'}
            </button>
          ))}
        </div>

        {/* Private/incognito sub-mode — only available inside Personal scope */}
        {chatScope === 'personal' && (
          <button
            type="button"
            onClick={handleTogglePrivate}
            aria-pressed={isPrivate}
            aria-label={isPrivate ? 'Private chat is on — click to turn off' : 'Turn on private chat'}
            title="Private: not saved to chat history or Cascade memory. Messages are still processed by the selected model provider."
            className={cn(
              'flex items-center gap-1 px-2.5 py-1 rounded-full text-xs font-medium transition-colors border',
              isPrivate
                ? 'bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/30'
                : 'text-muted-foreground border-transparent hover:bg-accent/50 hover:text-foreground',
            )}
          >
            <Lock className="h-3 w-3" aria-hidden="true" />
            Private
          </button>
        )}
      </div>

      {/* ── Personal sync-off note ─────────────────────────────── */}
      {chatScope === 'personal' && !isPrivate && !personalChatSync && (
        <div className="px-4 py-1 text-[0.65rem] text-muted-foreground/70 border-b border-border bg-background shrink-0">
          History stays on this device. Enable personal chat sync in Settings to
          persist to Cascade memory.
        </div>
      )}

      {/* ── Error banner ───────────────────────────────────────── */}
      {error && (() => {
        const isConnectionError = /connection refused|failed to fetch|network error|econnrefused/i.test(error)
        return isConnectionError ? (
          <div
            role="alert"
            className="px-4 py-2.5 text-xs bg-amber-500/10 border-b border-amber-500/30 text-amber-700 dark:text-amber-400 shrink-0"
          >
            <span className="font-medium">Cascade daemon is not running.</span>{' '}
            Start it with:{' '}
            <code className="font-mono bg-amber-500/15 px-1 py-0.5 rounded text-amber-800 dark:text-amber-300">
              cascade daemon start
            </code>
          </div>
        ) : (
          <div
            role="alert"
            className="px-4 py-2 text-xs text-destructive bg-destructive/10 border-b border-destructive/20 shrink-0"
          >
            {error}
          </div>
        )
      })()}

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
