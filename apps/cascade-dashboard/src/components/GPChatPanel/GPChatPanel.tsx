/**
 * Purpose: GP Chat panel shell — expandable/minimized floating panel with
 *   resize handle, header (title + minimize + close), MessageList, and ChatInput.
 * Inputs: sessionId string, onClose ()→void.
 * Outputs: Panel with three visible states: expanded (full), minimized (header only).
 *   Drag handle on top edge for height resize (min 300px, max 80vh).
 * Constraints: Panel is absolutely positioned; z-50 to float above page content.
 *   Resize uses mouse events on the drag handle, not CSS resize (better UX).
 *   useChat hook owns streaming state; this component owns panel geometry.
 * SPORT: T-P3-E02-21 GP Chat React UI
 */
import { useState, useRef, useCallback, type MouseEvent } from 'react'
import { Minus, X, MessageSquare } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useChat } from '@/hooks/useChat'
import { MessageList } from './MessageList'
import { ChatInput } from './ChatInput'

interface GPChatPanelProps {
  sessionId: string
  onClose: () => void
}

const MIN_HEIGHT = 300
const MAX_HEIGHT_VH = 0.8

export function GPChatPanel({ sessionId, onClose }: GPChatPanelProps) {
  const [minimized, setMinimized] = useState(false)
  const [panelHeight, setPanelHeight] = useState(420)

  const { messages, isStreaming, error, sendMessage, clearMessages } =
    useChat(sessionId)

  // ── Drag-to-resize ─────────────────────────────────────────────────────
  const dragging = useRef(false)
  const dragStartY = useRef(0)
  const dragStartHeight = useRef(0)

  const onDragStart = useCallback((e: MouseEvent<HTMLDivElement>) => {
    dragging.current = true
    dragStartY.current = e.clientY
    dragStartHeight.current = panelHeight

    function onMove(ev: globalThis.MouseEvent) {
      if (!dragging.current) return
      const delta = dragStartY.current - ev.clientY
      const maxH = Math.floor(window.innerHeight * MAX_HEIGHT_VH)
      const next = Math.min(maxH, Math.max(MIN_HEIGHT, dragStartHeight.current + delta))
      setPanelHeight(next)
    }

    function onUp() {
      dragging.current = false
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }

    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }, [panelHeight])

  // ── Render ─────────────────────────────────────────────────────────────
  const headerHeight = 44

  return (
    <div
      role="dialog"
      aria-label="GP Chat panel"
      className={cn(
        'fixed bottom-20 right-6 z-50',
        'flex flex-col rounded-xl shadow-2xl border border-border bg-background',
        'transition-[height] duration-150 ease-in-out',
        'w-[360px] max-w-[calc(100vw-2rem)]',
      )}
      style={{ height: minimized ? headerHeight : panelHeight }}
    >
      {/* Drag handle */}
      {!minimized && (
        <div
          onMouseDown={onDragStart}
          title="Drag to resize"
          className={cn(
            'absolute -top-1.5 left-1/2 -translate-x-1/2',
            'w-10 h-3 flex items-center justify-center cursor-ns-resize rounded-full',
            'hover:bg-muted/60 select-none',
          )}
          aria-hidden="true"
        >
          <div className="w-8 h-0.5 rounded-full bg-muted-foreground/40" />
        </div>
      )}

      {/* Header */}
      <div
        className={cn(
          'flex items-center justify-between px-3 rounded-t-xl',
          'border-b border-border bg-muted/30 select-none shrink-0',
        )}
        style={{ height: headerHeight }}
      >
        <div className="flex items-center gap-2 text-sm font-medium text-foreground">
          <MessageSquare className="h-4 w-4 text-primary" aria-hidden="true" />
          GP Chat
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={() => setMinimized((p) => !p)}
            aria-label={minimized ? 'Expand chat panel' : 'Minimize chat panel'}
            className={cn(
              'rounded p-1 text-muted-foreground',
              'hover:bg-muted hover:text-foreground transition-colors',
            )}
          >
            <Minus className="h-4 w-4" aria-hidden="true" />
          </button>
          <button
            onClick={onClose}
            aria-label="Close chat panel"
            className={cn(
              'rounded p-1 text-muted-foreground',
              'hover:bg-muted hover:text-destructive transition-colors',
            )}
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      </div>

      {/* Body — hidden when minimized */}
      {!minimized && (
        <>
          {/* Error banner */}
          {error && (
            <div className="px-3 py-1.5 text-xs text-destructive bg-destructive/10 border-b border-destructive/20 shrink-0">
              {error}
            </div>
          )}

          <MessageList messages={messages} isStreaming={isStreaming} />

          {/* Clear history link */}
          <div className="flex justify-end px-3 py-0.5 shrink-0">
            <button
              onClick={clearMessages}
              disabled={isStreaming}
              className="text-[10px] text-muted-foreground hover:text-foreground transition-colors disabled:opacity-40"
            >
              Clear history
            </button>
          </div>

          <ChatInput
            onSend={sendMessage}
            isStreaming={isStreaming}
          />
        </>
      )}
    </div>
  )
}
