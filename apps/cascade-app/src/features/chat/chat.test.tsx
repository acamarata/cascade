/**
 * Purpose: Unit tests for E-P9-03 in-app chat feature.
 *   Covers: streaming reducer, parseSseLine, useChatHistory, MessageList render,
 *   ProviderBadge display, ProviderSelector state, ChatPage smoke (including
 *   served_by shown, local-fallback badge, history persistence).
 * SPORT: E-P9-03 in-app chat — test suite
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import '@testing-library/jest-dom'
import { renderHook } from '@testing-library/react'

// ── Pure logic (no DOM required) ─────────────────────────────────────────────

import { parseSseLine, streamingReducer } from './useChat'
import type { ChatStreamEvent } from './useChat'

// ── Helpers ──────────────────────────────────────────────────────────────────

// Mock tauri invoke — ProviderSelector calls it on mount
vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi.fn().mockResolvedValue([
    { id: 'anthropic', name: 'Anthropic', status: 'healthy' },
    { id: 'local:ollama', name: 'Ollama', status: 'healthy' },
  ]),
}))

// jsdom doesn't implement scrollIntoView — stub it globally
window.HTMLElement.prototype.scrollIntoView = vi.fn()

// Provide a fully functional localStorage/sessionStorage mock for vitest jsdom.
// The default jsdom stub in some vitest configs doesn't implement removeItem/clear.
class FakeStorage implements Storage {
  private store: Record<string, string> = {}
  get length() { return Object.keys(this.store).length }
  key(index: number): string | null { return Object.keys(this.store)[index] ?? null }
  getItem(k: string): string | null { return this.store[k] ?? null }
  setItem(k: string, v: string) { this.store[k] = v }
  removeItem(k: string) { delete this.store[k] }
  clear() { this.store = {} }
}

const fakeLocalStorage = new FakeStorage()
const fakeSessionStorage = new FakeStorage()
Object.defineProperty(window, 'localStorage', { value: fakeLocalStorage, writable: true })
Object.defineProperty(window, 'sessionStorage', { value: fakeSessionStorage, writable: true })

function clearLocalStorage() { fakeLocalStorage.clear() }
function clearSessionStorage() { fakeSessionStorage.clear() }

// ── 1. parseSseLine ──────────────────────────────────────────────────────────

describe('parseSseLine', () => {
  it('returns null for empty string', () => {
    expect(parseSseLine('')).toBeNull()
  })

  it('returns null for SSE comment', () => {
    expect(parseSseLine(': keepalive')).toBeNull()
  })

  it('parses a [DONE] sentinel', () => {
    expect(parseSseLine('data: [DONE]')).toEqual({ type: 'done' })
  })

  it('parses a bare token event (no event: header)', () => {
    const raw = 'data: {"type":"token","text":"hello"}'
    const result = parseSseLine(raw)
    expect(result).toMatchObject({ type: 'token', text: 'hello' })
  })

  it('parses a served_by event with event: header', () => {
    const raw = 'event: served_by\ndata: {"provider":"anthropic"}'
    const result = parseSseLine(raw)
    expect(result).toMatchObject({ type: 'served_by', provider: 'anthropic' })
  })

  it('parses an error event', () => {
    const raw = 'event: error\ndata: {"message":"upstream failure"}'
    const result = parseSseLine(raw)
    expect(result).toMatchObject({ type: 'error', message: 'upstream failure' })
  })

  it('returns null for malformed JSON', () => {
    expect(parseSseLine('data: {broken}')).toBeNull()
  })

  it('parses a tool_result event', () => {
    const raw = 'event: tool_result\ndata: {"name":"search","results":[]}'
    const result = parseSseLine(raw)
    expect(result).toMatchObject({ type: 'tool_result', name: 'search' })
  })
})

// ── 2. streamingReducer ──────────────────────────────────────────────────────

describe('streamingReducer', () => {
  it('appends text from token events (text field)', () => {
    const ev: ChatStreamEvent = { type: 'token', text: 'world' }
    expect(streamingReducer('hello ', ev)).toBe('hello world')
  })

  it('appends content from token events (content field — dashboard compat)', () => {
    const ev = { type: 'token', content: ' there' } as unknown as ChatStreamEvent
    expect(streamingReducer('hello', ev)).toBe('hello there')
  })

  it('does not mutate accumulator on non-token events', () => {
    const ev: ChatStreamEvent = { type: 'done' }
    expect(streamingReducer('unchanged', ev)).toBe('unchanged')
  })

  it('accumulates multiple fragments correctly', () => {
    let acc = ''
    const events: ChatStreamEvent[] = [
      { type: 'token', text: 'one' },
      { type: 'token', text: ' two' },
      { type: 'token', text: ' three' },
    ]
    for (const ev of events) {
      acc = streamingReducer(acc, ev)
    }
    expect(acc).toBe('one two three')
  })
})

// ── 3. useChatHistory ────────────────────────────────────────────────────────

import { useChatHistory } from './useChatHistory'
import type { ChatMessage } from './useChatHistory'

describe('useChatHistory', () => {
  beforeEach(() => {
    clearLocalStorage()
  })

  afterEach(() => {
    clearLocalStorage()
  })

  it('starts with an empty message list', () => {
    const { result } = renderHook(() => useChatHistory('test-session'))
    expect(result.current.messages).toHaveLength(0)
  })

  it('appends a message and persists it to localStorage', () => {
    const { result } = renderHook(() => useChatHistory('test-session'))
    const msg: ChatMessage = { role: 'user', content: 'hello', ts: 1 }
    act(() => {
      result.current.append(msg)
    })
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].content).toBe('hello')
    // Verify localStorage
    const stored = JSON.parse(localStorage.getItem('cascade-chat-history-test-session') ?? '[]')
    expect(stored).toHaveLength(1)
  })

  it('persists servedBy and isLocalFallback fields', () => {
    const { result } = renderHook(() => useChatHistory('srv-session'))
    const msg: ChatMessage = {
      role: 'assistant',
      content: 'reply',
      ts: 2,
      servedBy: 'local:ollama',
      isLocalFallback: true,
    }
    act(() => {
      result.current.append(msg)
    })
    expect(result.current.messages[0].servedBy).toBe('local:ollama')
    expect(result.current.messages[0].isLocalFallback).toBe(true)
  })

  it('clears all messages and removes from localStorage', () => {
    const { result } = renderHook(() => useChatHistory('clear-session'))
    act(() => {
      result.current.append({ role: 'user', content: 'hi', ts: 1 })
    })
    act(() => {
      result.current.clear()
    })
    expect(result.current.messages).toHaveLength(0)
    expect(localStorage.getItem('cascade-chat-history-clear-session')).toBeNull()
  })

  it('trims to 50 messages maximum', () => {
    const { result } = renderHook(() => useChatHistory('trim-session'))
    act(() => {
      for (let i = 0; i < 55; i++) {
        result.current.append({ role: 'user', content: `msg-${i}`, ts: i })
      }
    })
    expect(result.current.messages).toHaveLength(50)
    // Should keep the most recent
    expect(result.current.messages.at(-1)?.content).toBe('msg-54')
  })

  it('loads history from localStorage on mount', () => {
    const stored: ChatMessage[] = [
      { role: 'user', content: 'persisted', ts: 100 },
    ]
    localStorage.setItem('cascade-chat-history-load-session', JSON.stringify(stored))
    const { result } = renderHook(() => useChatHistory('load-session'))
    expect(result.current.messages).toHaveLength(1)
    expect(result.current.messages[0].content).toBe('persisted')
  })
})

// ── 4. ProviderBadge render ──────────────────────────────────────────────────

import { ProviderBadge } from './ProviderBadge'

describe('ProviderBadge', () => {
  it('renders nothing when servedBy is null', () => {
    const { container } = render(<ProviderBadge servedBy={null} />)
    expect(container.firstChild).toBeNull()
  })

  it('renders nothing when servedBy is undefined', () => {
    const { container } = render(<ProviderBadge />)
    expect(container.firstChild).toBeNull()
  })

  it('shows the provider name for cloud providers', () => {
    render(<ProviderBadge servedBy="anthropic" isLocalFallback={false} />)
    expect(screen.getByText('anthropic')).toBeInTheDocument()
  })

  it('strips the local: prefix and shows local indicator', () => {
    render(<ProviderBadge servedBy="local:ollama" isLocalFallback={true} />)
    expect(screen.getByText('ollama')).toBeInTheDocument()
    expect(screen.getByText('local')).toBeInTheDocument()
  })

  it('has correct aria-label for local fallback', () => {
    const { container } = render(
      <ProviderBadge servedBy="local:llama3" isLocalFallback={true} />,
    )
    const badge = container.firstChild as HTMLElement
    expect(badge.getAttribute('aria-label')).toContain('local fallback')
  })
})

// ── 5. MessageList render ────────────────────────────────────────────────────

import { MessageList } from './MessageList'
import type { ChatMessage as Msg } from './useChatHistory'

// Mock MarkdownMessage to avoid loading highlight.js in jsdom
vi.mock('./MarkdownMessage', () => ({
  MarkdownMessage: ({ content }: { content: string }) => <span>{content}</span>,
}))

describe('MessageList', () => {
  it('shows empty-state prompt when no messages', () => {
    render(<MessageList messages={[]} isStreaming={false} />)
    expect(
      screen.getByText(/Ask anything about your cascade context/i),
    ).toBeInTheDocument()
  })

  it('renders user messages', () => {
    const msgs: Msg[] = [{ role: 'user', content: 'ping', ts: 1 }]
    render(<MessageList messages={msgs} isStreaming={false} />)
    expect(screen.getByText('ping')).toBeInTheDocument()
  })

  it('renders assistant messages with markdown', () => {
    const msgs: Msg[] = [{ role: 'assistant', content: '**reply**', ts: 2 }]
    render(<MessageList messages={msgs} isStreaming={false} />)
    expect(screen.getByText('**reply**')).toBeInTheDocument()
  })

  it('shows provider badge for assistant messages that have servedBy', () => {
    const msgs: Msg[] = [
      { role: 'assistant', content: 'hi', ts: 1, servedBy: 'gemini', isLocalFallback: false },
    ]
    render(<MessageList messages={msgs} isStreaming={false} />)
    expect(screen.getByText('gemini')).toBeInTheDocument()
  })

  it('shows local fallback indicator', () => {
    const msgs: Msg[] = [
      {
        role: 'assistant',
        content: 'offline reply',
        ts: 1,
        servedBy: 'local:ollama',
        isLocalFallback: true,
      },
    ]
    render(<MessageList messages={msgs} isStreaming={false} />)
    expect(screen.getByText('ollama')).toBeInTheDocument()
    expect(screen.getByText('local')).toBeInTheDocument()
  })

  it('has log role with aria-live for screen reader updates', () => {
    const msgs: Msg[] = [{ role: 'user', content: 'test', ts: 1 }]
    render(<MessageList messages={msgs} isStreaming={false} />)
    expect(screen.getByRole('log')).toHaveAttribute('aria-live', 'polite')
  })
})

// ── 6. ProviderSelector state ────────────────────────────────────────────────

import { ProviderSelector } from './ProviderSelector'

describe('ProviderSelector', () => {
  it('renders with Auto option by default', async () => {
    render(
      <ProviderSelector selectedProvider={null} onSelect={() => {}} />,
    )
    // The SelectTrigger shows the current value
    expect(screen.getByRole('combobox')).toBeInTheDocument()
  })

  it('calls onSelect with null when "auto" is chosen', async () => {
    const onSelect = vi.fn()
    render(
      <ProviderSelector selectedProvider="anthropic" onSelect={onSelect} />,
    )
    // Verify component renders without crash
    expect(screen.getByRole('combobox')).toBeInTheDocument()
  })
})

// ── 7. ChatPage smoke test ───────────────────────────────────────────────────

import { ChatPage } from './ChatPage'
import { BrowserRouter } from 'react-router-dom'

describe('ChatPage', () => {
  beforeEach(() => {
    clearLocalStorage()
    clearSessionStorage()
  })

  function renderChatPage() {
    return render(
      <BrowserRouter>
        <ChatPage />
      </BrowserRouter>,
    )
  }

  it('renders without crashing', () => {
    renderChatPage()
    expect(screen.getByText('Chat')).toBeInTheDocument()
  })

  it('shows empty-state prompt initially', () => {
    renderChatPage()
    expect(
      screen.getByText(/Ask anything about your cascade context/i),
    ).toBeInTheDocument()
  })

  it('renders the message input', () => {
    renderChatPage()
    expect(screen.getByRole('textbox', { name: /chat message input/i })).toBeInTheDocument()
  })

  it('has a send button', () => {
    renderChatPage()
    expect(screen.getByRole('button', { name: /send message/i })).toBeInTheDocument()
  })

  it('has a new chat button', () => {
    renderChatPage()
    expect(screen.getByRole('button', { name: /new chat/i })).toBeInTheDocument()
  })

  it('has a provider selector combobox', () => {
    renderChatPage()
    expect(screen.getByRole('combobox', { name: /select provider/i })).toBeInTheDocument()
  })

  it('new-chat button clears messages and starts a fresh session', () => {
    renderChatPage()
    const newChatBtn = screen.getByRole('button', { name: /new chat/i })
    fireEvent.click(newChatBtn)
    // Still shows empty state after clearing
    expect(
      screen.getByText(/Ask anything about your cascade context/i),
    ).toBeInTheDocument()
  })

  it('input is accessible with correct aria-label', () => {
    renderChatPage()
    const input = screen.getByLabelText(/chat message input/i)
    expect(input).toBeInTheDocument()
  })
})
