/**
 * Tests for parseSseLine — the pure SSE event parser exported from useChat.ts.
 * No DOM needed; the hook integration is covered by QA-B/C manual tests.
 */
import { test, expect } from 'vitest'
import { parseSseLine } from './useChat'

test('returns null for empty string', () => {
  expect(parseSseLine('')).toBeNull()
})

test('returns null for keepalive comment', () => {
  expect(parseSseLine(': keepalive')).toBeNull()
})

test('parses token event', () => {
  const line = 'data: {"type":"token","content":"hello"}'
  expect(parseSseLine(line)).toEqual({ type: 'token', content: 'hello' })
})

test('parses [DONE] sentinel', () => {
  const line = 'data: [DONE]'
  expect(parseSseLine(line)).toEqual({ type: 'done' })
})

test('parses error event', () => {
  const line = 'data: {"type":"error","message":"internal error"}'
  expect(parseSseLine(line)).toEqual({ type: 'error', message: 'internal error' })
})

test('parses tool_result event', () => {
  const line = 'data: {"type":"tool_result","result":{"ok":true}}'
  expect(parseSseLine(line)).toEqual({ type: 'tool_result', result: { ok: true } })
})

test('returns null for malformed JSON', () => {
  const line = 'data: {not json}'
  expect(parseSseLine(line)).toBeNull()
})

test('returns null for line without data: prefix', () => {
  const line = 'event: token'
  expect(parseSseLine(line)).toBeNull()
})

test('handles multi-line SSE block (picks first data: line)', () => {
  const block = 'event: message\ndata: {"type":"token","content":"world"}'
  expect(parseSseLine(block)).toEqual({ type: 'token', content: 'world' })
})

test('returns null for object missing type field', () => {
  const line = 'data: {"content":"oops"}'
  expect(parseSseLine(line)).toBeNull()
})
