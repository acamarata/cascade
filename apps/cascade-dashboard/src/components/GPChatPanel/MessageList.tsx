/**
 * Purpose: Renders the scrollable list of chat messages (user + assistant bubbles).
 *   The last message animates a blinking cursor when the stream is active.
 *   Assistant messages are rendered as GFM markdown with syntax-highlighted code
 *   blocks (MarkdownMessage). Tool invocation results render as ToolCards inline.
 * Inputs: messages ChatMessage[], isStreaming boolean.
 * Outputs: Scrollable div auto-scrolling to the newest message on each update.
 * Constraints: Cursor blink via CSS animation — no JS timer needed.
 *   Markdown rendering delegated to MarkdownMessage (T-P3-E02-22).
 * SPORT: T-P3-E02-21 GP Chat React UI / T-P3-E02-22 Markdown + ToolCards
 */
import { useEffect, useRef } from 'react'
import { cn } from '@/lib/utils'
import type { ChatMessage } from '@/hooks/useChatHistory'
import { MarkdownMessage } from './MarkdownMessage'
import { ToolCard } from './ToolCard'

interface MessageListProps {
  messages: ChatMessage[]
  isStreaming: boolean
}

/** Blinking cursor appended to streaming assistant messages. */
function StreamCursor() {
  return (
    <span
      aria-hidden="true"
      className="inline-block w-[2px] h-[1em] bg-current align-text-bottom ml-0.5 animate-[blink_1s_step-end_infinite]"
    />
  )
}

/** Single user message bubble. */
function UserBubble({ content }: { content: string }) {
  return (
    <div className="flex justify-end">
      <div
        className={cn(
          'max-w-[75%] rounded-2xl rounded-br-sm px-3 py-2 text-sm',
          'bg-primary text-primary-foreground',
          'whitespace-pre-wrap break-words',
        )}
      >
        {content}
      </div>
    </div>
  )
}

/** Single assistant message bubble — renders markdown + inline ToolCards. */
function AssistantBubble({
  content,
  toolResults,
  showCursor,
}: {
  content: string
  toolResults?: unknown[]
  showCursor: boolean
}) {
  return (
    <div className="flex justify-start">
      <div
        className={cn(
          'max-w-[85%] rounded-2xl rounded-bl-sm px-3 py-2',
          'bg-muted text-muted-foreground',
          'break-words',
        )}
      >
        <MarkdownMessage content={content} />
        {showCursor && <StreamCursor />}
        {toolResults && toolResults.length > 0 && (
          <div className="mt-1 space-y-1">
            {toolResults.map((tr, i) => {
              // Defensively extract toolName from the result if available.
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
      </div>
    </div>
  )
}

export function MessageList({ messages, isStreaming }: MessageListProps) {
  const bottomRef = useRef<HTMLDivElement>(null)

  // Auto-scroll to bottom whenever messages change or a token arrives
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  if (messages.length === 0) {
    return (
      <div className="flex-1 flex items-center justify-center text-xs text-muted-foreground select-none p-4">
        Ask anything about your cascade context…
      </div>
    )
  }

  return (
    <div className="flex-1 overflow-y-auto px-3 py-2 space-y-2 min-h-0">
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
          />
        )
      })}
      <div ref={bottomRef} />
    </div>
  )
}
