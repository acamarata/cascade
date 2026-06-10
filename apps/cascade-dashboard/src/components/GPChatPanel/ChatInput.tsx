/**
 * Purpose: Textarea + send button for GP Chat panel input row.
 *   Enter sends; Shift+Enter inserts newline. Send is disabled while streaming.
 * Inputs: onSend(content:string)→void, isStreaming boolean, disabled boolean.
 * Outputs: Controlled textarea that submits on Enter (not Shift+Enter).
 * Constraints: Textarea auto-grows to 3 rows max; resets on send.
 *   No file upload or tool selection (out of scope for this ticket).
 * SPORT: T-P3-E02-21 GP Chat React UI
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
    // Reset textarea height
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
    // Auto-grow up to ~3 rows (72px ≈ 3 × 24px line-height)
    el.style.height = 'auto'
    el.style.height = `${Math.min(el.scrollHeight, 72)}px`
  }

  return (
    <div
      className={cn(
        'flex items-end gap-2 border-t border-border px-3 py-2',
        'bg-background',
      )}
    >
      <textarea
        ref={textareaRef}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={handleKeyDown}
        onInput={handleInput}
        rows={1}
        placeholder="Ask something… (Shift+Enter for newline)"
        disabled={isStreaming || disabled}
        aria-label="Chat message"
        className={cn(
          'flex-1 resize-none rounded-md border border-input bg-transparent',
          'px-3 py-1.5 text-sm ring-offset-background',
          'placeholder:text-muted-foreground',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
          'disabled:cursor-not-allowed disabled:opacity-50',
          'overflow-y-auto min-h-[36px]',
        )}
      />
      <Button
        size="icon"
        variant="default"
        onClick={handleSend}
        disabled={!canSend}
        aria-label="Send message"
        className="shrink-0 h-9 w-9"
      >
        <Send className="h-4 w-4" />
      </Button>
    </div>
  )
}
