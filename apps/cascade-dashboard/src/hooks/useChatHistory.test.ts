// @vitest-environment jsdom
/**
 * Tests for useChatHistory — localStorage persistence, 50-message trim, clear.
 */
import { renderHook, act } from '@testing-library/react'
import { vi, test, expect, beforeEach, afterEach } from 'vitest'
import { useChatHistory, type ChatMessage } from './useChatHistory'

// ---- localStorage stub -------------------------------------------------------

const store: Record<string, string> = {}

beforeEach(() => {
  Object.keys(store).forEach((k) => delete store[k])
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => store[k] ?? null,
    setItem: (k: string, v: string) => { store[k] = v },
    removeItem: (k: string) => { delete store[k] },
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

// ---- helpers -----------------------------------------------------------------

function makeMsg(role: 'user' | 'assistant', content: string): ChatMessage {
  return { role, content, ts: Date.now() }
}

// ---- tests -------------------------------------------------------------------

test('starts with empty messages when localStorage is empty', () => {
  const { result } = renderHook(() => useChatHistory('sess-1'))
  expect(result.current.messages).toEqual([])
})

test('appends a message and persists to localStorage', () => {
  const { result } = renderHook(() => useChatHistory('sess-1'))
  act(() => {
    result.current.append(makeMsg('user', 'hello'))
  })
  expect(result.current.messages).toHaveLength(1)
  expect(result.current.messages[0].content).toBe('hello')
  const raw = store['gp-chat-history-sess-1']
  expect(raw).toBeDefined()
  const parsed = JSON.parse(raw) as ChatMessage[]
  expect(parsed[0].content).toBe('hello')
})

test('loads existing messages from localStorage on mount', () => {
  const existing: ChatMessage[] = [makeMsg('assistant', 'prior')]
  store['gp-chat-history-sess-2'] = JSON.stringify(existing)
  const { result } = renderHook(() => useChatHistory('sess-2'))
  expect(result.current.messages).toHaveLength(1)
  expect(result.current.messages[0].content).toBe('prior')
})

test('trims to 50 messages when 51st is appended', () => {
  const { result } = renderHook(() => useChatHistory('sess-3'))
  act(() => {
    for (let i = 0; i < 51; i++) {
      result.current.append(makeMsg('user', `msg-${i}`))
    }
  })
  expect(result.current.messages).toHaveLength(50)
  // Oldest (msg-0) must be gone; newest (msg-50) must be present.
  expect(result.current.messages[0].content).toBe('msg-1')
  expect(result.current.messages[49].content).toBe('msg-50')
})

test('clear removes localStorage entry and resets state', () => {
  const { result } = renderHook(() => useChatHistory('sess-4'))
  act(() => {
    result.current.append(makeMsg('user', 'to be cleared'))
  })
  expect(result.current.messages).toHaveLength(1)
  act(() => {
    result.current.clear()
  })
  expect(result.current.messages).toEqual([])
  expect(store['gp-chat-history-sess-4']).toBeUndefined()
})

test('handles corrupt localStorage gracefully (returns empty array)', () => {
  store['gp-chat-history-sess-5'] = '{{not valid json'
  const { result } = renderHook(() => useChatHistory('sess-5'))
  expect(result.current.messages).toEqual([])
})

test('sessions are isolated (different keys)', () => {
  const { result: r1 } = renderHook(() => useChatHistory('sess-a'))
  const { result: r2 } = renderHook(() => useChatHistory('sess-b'))
  act(() => {
    r1.current.append(makeMsg('user', 'only in a'))
  })
  expect(r1.current.messages).toHaveLength(1)
  expect(r2.current.messages).toHaveLength(0)
})
