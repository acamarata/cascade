/**
 * Purpose: Textarea + send button for the in-app chat prompt box.
 *   Enter sends; Shift+Enter inserts newline. Send is disabled while streaming.
 *   Ported from cascade-dashboard GPChatPanel T-P3-E02-21.
 * Inputs: onSend(content:string)→void, isStreaming boolean, disabled boolean.
 * Outputs: Controlled textarea that submits on Enter (not Shift+Enter).
 * Constraints: Textarea auto-grows to 4 rows max; resets on send. No file upload.
 * SPORT: E-P9-03 in-app chat — ChatInput
 */
import { useState, useRef, type KeyboardEvent } from 'react'
import { Send } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

interface ChatInputProps {
  onSend: (content: string) => void
  isStreaming: boolean
  disabled?: boolean
}

export function ChatInput({ onSend, isStreaming, disabled = false }: ChatInputProps) {
  const [value, setValue] = useState('')
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const canSend = value.trim().length > 0 && !isStreaming && !disabled

  function handleSend() {
    const trimmed = value.trim()
    if (!trimmed || isStreaming || disabled) return
    onSend(trimmed)
    setValue('')
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto'
    }
  }

  function handleKeyDown(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  function handleInput() {
    const el = textareaRef.current
    if (!el) return
    // Auto-grow up to ~4 rows (96px ≈ 4 × 24px line-height)
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 96)}px`
  }

  return (
    <div
      className={cn(
        'flex items-end gap-3 border-t border-border/50 px-4 py-3',
        'bg-background shrink-0'
      )}
    >
      <textarea
        ref={textareaRef}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={handleKeyDown}
        onInput={handleInput}
        rows={1}
        placeholder="Message Cascade… (Shift+Enter for newline)"
        disabled={isStreaming || disabled}
        aria-label="Chat message input"
        className={cn(
          'flex-1 resize-none rounded-xl border border-border/60 bg-muted/20',
          'px-4 py-2.5 text-sm ring-offset-background transition-all duration-200',
          'placeholder:text-muted-foreground/70',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 focus-visible:border-primary/50',
          'disabled:cursor-not-allowed disabled:opacity-50',
          'overflow-y-auto min-h-[40px] max-h-[120px]'
        )}
      />
      <Button
        size="icon"
        variant="default"
        onClick={handleSend}
        disabled={!canSend}
        aria-label="Send message"
        className="shrink-0 h-10 w-10 rounded-xl transition-all duration-200 shadow-sm"
      >
        <Send className="h-4 w-4" aria-hidden="true" />
      </Button>
    </div>
  )
}
