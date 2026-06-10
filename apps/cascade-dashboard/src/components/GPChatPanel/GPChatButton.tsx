/**
 * Purpose: Floating action button fixed bottom-right that opens/closes the
 *   GP Chat panel. Owns open state and session_id (stable per browser session).
 *   Renders GPChatPanel when open.
 * Inputs: none — self-contained; mounts in DashboardLayout.
 * Outputs: Fixed-position button + conditionally rendered GPChatPanel.
 * Constraints: session_id generated once via crypto.randomUUID() in useState init.
 *   z-50 so it floats above page content but below modals (z-[100]+).
 *   Badge shows unread count when panel is closed and history has messages
 *   received since last open (approximated: any messages in storage).
 * SPORT: T-P3-E02-21 GP Chat React UI
 */
import { useState } from 'react'
import { MessageSquare } from 'lucide-react'
import { cn } from '@/lib/utils'
import { GPChatPanel } from './GPChatPanel'

/** Generate a stable browser-session UUID (re-used across opens). */
function initSessionId(): string {
  const key = 'gp-chat-session-id'
  const existing = typeof window !== 'undefined'
    ? window.sessionStorage.getItem(key)
    : null
  if (existing) return existing
  const id = typeof crypto !== 'undefined' && crypto.randomUUID
    ? crypto.randomUUID()
    : `sess-${Date.now()}-${Math.random().toString(36).slice(2)}`
  if (typeof window !== 'undefined') {
    window.sessionStorage.setItem(key, id)
  }
  return id
}

export function GPChatButton() {
  const [open, setOpen] = useState(false)
  const [sessionId] = useState<string>(initSessionId)

  return (
    <>
      {/* Floating trigger button */}
      <button
        onClick={() => setOpen((p) => !p)}
        aria-label={open ? 'Close GP Chat' : 'Open GP Chat'}
        aria-expanded={open}
        className={cn(
          'fixed bottom-6 right-6 z-50',
          'flex items-center justify-center',
          'h-12 w-12 rounded-full shadow-lg',
          'bg-primary text-primary-foreground',
          'hover:bg-primary/90 active:scale-95 transition-all duration-150',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
        )}
      >
        <MessageSquare className="h-5 w-5" aria-hidden="true" />
      </button>

      {/* Panel (rendered while open) */}
      {open && (
        <GPChatPanel
          sessionId={sessionId}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}
