/**
 * Purpose: Renders the scrollable list of chat messages (user/assistant bubbles).
 *   Auto-scrolls to the newest message. The last assistant message shows a
 *   blinking cursor while streaming. Tool cards render inline below markdown.
 *   Provider badge shows which backend served each assistant response.
 *   Ported from cascade-dashboard GPChatPanel T-P3-E02-21.
 * Inputs: messages ChatMessage[], isStreaming boolean.
 * Outputs: Scrollable div auto-scrolling to newest message on each update.
 * Constraints: Cursor blink via CSS animation — no JS timer needed.
 * SPORT: E-P9-03 in-app chat — MessageList
 */
import { useEffect, useRef } from 'react'
import { cn } from '@/lib/utils'
import type { ChatMessage } from './useChatHistory'
import { MarkdownMessage } from './MarkdownMessage'
import { ToolCard } from './ToolCard'
import { ProviderBadge } from './ProviderBadge'

interface MessageListProps {
  messages: ChatMessage[]
  isStreaming: boolean
}

/** Blinking cursor appended to the active streaming assistant message. */
function StreamCursor() {
  return (
    <span
      aria-hidden="true"
      className="inline-block w-[2px] h-[1.1em] bg-primary align-middle ml-1 animate-[pulse_1s_infinite]"
    />
  )
}

/** Single user message bubble. */
function UserBubble({ content }: { content: string }) {
  return (
    <div className="flex justify-end my-1">
      <div
        className={cn(
          'max-w-[75%] rounded-2xl rounded-tr-sm px-4 py-2.5 text-sm shadow-sm',
          'bg-gradient-to-br from-primary to-primary/90 text-primary-foreground',
          'whitespace-pre-wrap break-words leading-relaxed',
        )}
      >
        {content}
      </div>
    </div>
  )
}

/** Single assistant message bubble — markdown + tool cards + provider badge. */
function AssistantBubble({
  content,
  toolResults,
  showCursor,
  servedBy,
  isLocalFallback,
}: {
  content: string
  toolResults?: unknown[]
  showCursor: boolean
  servedBy?: string | null
  isLocalFallback?: boolean
}) {
  return (
    <div className="flex justify-start my-1">
      <div
        className={cn(
          'max-w-[85%] rounded-2xl rounded-tl-sm px-4 py-3 shadow-sm',
          'bg-muted/40 text-foreground border border-border/30',
          'break-words leading-relaxed',
        )}
      >
        <MarkdownMessage content={content} />
        {showCursor && <StreamCursor />}
        {toolResults && toolResults.length > 0 && (
          <div className="mt-2 space-y-1.5">
            {toolResults.map((tr, i) => {
              const entry =
                tr !== null && typeof tr === 'object' && !Array.isArray(tr)
                  ? (tr as Record<string, unknown>)
                  : {}
              const toolName =
                typeof entry['name'] === 'string'
                  ? entry['name']
                  : typeof entry['tool_name'] === 'string'
                    ? entry['tool_name']
                    : `tool_${i}`
              return <ToolCard key={i} toolName={toolName} result={tr} />
            })}
          </div>
        )}
        <ProviderBadge servedBy={servedBy} isLocalFallback={isLocalFallback} />
      </div>
    </div>
  )
}

export function MessageList({ messages, isStreaming }: MessageListProps) {
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  if (messages.length === 0) {
    return (
      <div
        role="status"
        aria-live="polite"
        className="flex-1 flex flex-col items-center justify-center gap-2 text-muted-foreground select-none p-6"
      >
        <p className="text-sm text-center">Ask anything about your cascade context…</p>
        <p className="text-xs text-center text-muted-foreground/60">
          Requests are routed through the daemon to your configured providers.
        </p>
      </div>
    )
  }

  return (
    <div
      role="log"
      aria-label="Chat messages"
      aria-live="polite"
      className="flex-1 overflow-y-auto px-3 py-2 space-y-2 min-h-0"
    >
      {messages.map((msg, idx) => {
        const isLast = idx === messages.length - 1
        if (msg.role === 'user') {
          return <UserBubble key={idx} content={msg.content} />
        }
        return (
          <AssistantBubble
            key={idx}
            content={msg.content}
            toolResults={msg.toolResults}
            showCursor={isStreaming && isLast}
            servedBy={msg.servedBy}
            isLocalFallback={msg.isLocalFallback}
          />
        )
      })}
      <div ref={bottomRef} />
    </div>
  )
}
