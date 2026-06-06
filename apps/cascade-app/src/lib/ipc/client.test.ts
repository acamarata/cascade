/**
 * Purpose: Unit tests for the Cascade IPC client wrappers.
 * Inputs:  Mocked @tauri-apps/api/core invoke function.
 * Outputs: Verified that each ipc.* method calls invoke with the correct command
 *   name + args and returns the typed result; and that errors are thrown as
 *   CascadeIpcError with the correct code field.
 * Constraints: Tests run in vitest; no real Tauri context required (invoke is mocked).
 *   All 9 wrappers must be covered.
 * SPORT: MASTER-LIBS.md — ipc client tests
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { MockedFunction } from 'vitest'

// Mock @tauri-apps/api/core before importing client so the module-level import is mocked.
vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi.fn(),
}))

import { invoke } from '@tauri-apps/api/core'
import { ipc } from './client'
import { CascadeIpcError } from '../../types/errors'

const mockInvoke = invoke as MockedFunction<typeof invoke>

beforeEach(() => {
  mockInvoke.mockReset()
})

// ── cascade_status ────────────────────────────────────────────────────────────

describe('ipc.getStatus', () => {
  it('calls invoke("cascade_status") with no args and returns StatusResult', async () => {
    const result = {
      pid: 1234,
      uptime_secs: 600,
      queue_depth: 0,
      rag_index_fresh: true,
      version: '0.1.0',
      tcp_port: null,
    }
    mockInvoke.mockResolvedValueOnce(result)

    const status = await ipc.getStatus()

    expect(mockInvoke).toHaveBeenCalledOnce()
    expect(mockInvoke).toHaveBeenCalledWith('cascade_status')
    expect(status).toEqual(result)
  })

  it('throws CascadeIpcError("DaemonNotRunning") when daemon is not up', async () => {
    mockInvoke.mockRejectedValueOnce('DaemonNotRunning: daemon socket not found')

    const err = await ipc.getStatus().catch((e: unknown) => e)
    expect(err).toBeInstanceOf(CascadeIpcError)
    expect((err as CascadeIpcError).code).toBe('DaemonNotRunning')
  })
})

// ── cascade_resolve ───────────────────────────────────────────────────────────

describe('ipc.resolve', () => {
  it('calls invoke("cascade_resolve") with tier and format args', async () => {
    const result = { content: '# GCI', format: 'markdown', tier: 'gci' }
    mockInvoke.mockResolvedValueOnce(result)

    const resolved = await ipc.resolve('gci', 'markdown')

    expect(mockInvoke).toHaveBeenCalledWith('cascade_resolve', { tier: 'gci', format: 'markdown' })
    expect(resolved).toEqual(result)
  })

  it('passes undefined args when tier and format are omitted', async () => {
    mockInvoke.mockResolvedValueOnce({ content: '# Full', format: 'markdown', tier: 'full' })

    await ipc.resolve()

    expect(mockInvoke).toHaveBeenCalledWith('cascade_resolve', {
      tier: undefined,
      format: undefined,
    })
  })
})

// ── cascade_search ────────────────────────────────────────────────────────────

describe('ipc.search', () => {
  it('calls invoke("cascade_search") with query and limit', async () => {
    const result = { hits: [{ id: 'c1', score: 0.9, excerpt: 'foo', source: '/a.md' }] }
    mockInvoke.mockResolvedValueOnce(result)

    const found = await ipc.search('delegation mandate', 5)

    expect(mockInvoke).toHaveBeenCalledWith('cascade_search', {
      query: 'delegation mandate',
      limit: 5,
    })
    expect(found.hits).toHaveLength(1)
  })
})

// ── cascade_inbox_list ────────────────────────────────────────────────────────

describe('ipc.inbox.list', () => {
  it('calls invoke("cascade_inbox_list") with limit arg', async () => {
    const result = { items: [] }
    mockInvoke.mockResolvedValueOnce(result)

    await ipc.inbox.list(10)

    expect(mockInvoke).toHaveBeenCalledWith('cascade_inbox_list', { limit: 10 })
  })
})

// ── cascade_inbox_send ────────────────────────────────────────────────────────

describe('ipc.inbox.send', () => {
  it('calls invoke("cascade_inbox_send") with to/subject/body/priority args', async () => {
    // stub returns NotImplemented — still verify the invoke call shape
    mockInvoke.mockRejectedValueOnce(
      'NotImplemented: cascade_inbox_send — awaits T-P4-E01 daemon inbox_send method'
    )

    await expect(ipc.inbox.send('nself', 'Test', 'Body', 'medium')).rejects.toMatchObject({
      code: 'NotImplemented',
    })
    expect(mockInvoke).toHaveBeenCalledWith('cascade_inbox_send', {
      to: 'nself',
      subject: 'Test',
      body: 'Body',
      priority: 'medium',
    })
  })
})

// ── cascade_memory_read ───────────────────────────────────────────────────────

describe('ipc.memory.read', () => {
  it('calls invoke("cascade_memory_read") with project and file args', async () => {
    const result = { content: '# decisions', path: '/proj/.claude/memory/decisions.md' }
    mockInvoke.mockResolvedValueOnce(result)

    const mem = await ipc.memory.read('/proj', 'decisions.md')

    expect(mockInvoke).toHaveBeenCalledWith('cascade_memory_read', {
      project: '/proj',
      file: 'decisions.md',
    })
    expect(mem.content).toBe('# decisions')
  })
})

// ── cascade_memory_write ──────────────────────────────────────────────────────

describe('ipc.memory.write', () => {
  it('calls invoke("cascade_memory_write") with project, file, and content args', async () => {
    const result = { path: '/proj/.claude/memory/decisions.md', bytes: 42 }
    mockInvoke.mockResolvedValueOnce(result)

    const written = await ipc.memory.write('/proj', 'decisions.md', '# new content')

    expect(mockInvoke).toHaveBeenCalledWith('cascade_memory_write', {
      project: '/proj',
      file: 'decisions.md',
      content: '# new content',
    })
    expect(written.bytes).toBe(42)
  })
})

// ── cascade_config_get ────────────────────────────────────────────────────────

describe('ipc.config.get', () => {
  it('calls invoke("cascade_config_get") with key arg', async () => {
    const result = { key: 'daemon.socket_path', value: '/tmp/cascade.sock' }
    mockInvoke.mockResolvedValueOnce(result)

    const cfg = await ipc.config.get('daemon.socket_path')

    expect(mockInvoke).toHaveBeenCalledWith('cascade_config_get', { key: 'daemon.socket_path' })
    expect(cfg.value).toBe('/tmp/cascade.sock')
  })
})

// ── cascade_config_set ────────────────────────────────────────────────────────

describe('ipc.config.set', () => {
  it('calls invoke("cascade_config_set") with key and value args', async () => {
    const result = { key: 'daemon.log_level', previous: 'info' }
    mockInvoke.mockResolvedValueOnce(result)

    const updated = await ipc.config.set('daemon.log_level', 'debug')

    expect(mockInvoke).toHaveBeenCalledWith('cascade_config_set', {
      key: 'daemon.log_level',
      value: 'debug',
    })
    expect(updated.previous).toBe('info')
  })
})

// ── Error handling ────────────────────────────────────────────────────────────

describe('CascadeIpcError wrapping', () => {
  it('parses structured JSON error payload', async () => {
    mockInvoke.mockRejectedValueOnce(
      JSON.stringify({ code: 'IpcTimeout', message: 'request timed out after 5000ms' })
    )

    await expect(ipc.getStatus()).rejects.toMatchObject({
      code: 'IpcTimeout',
      message: 'request timed out after 5000ms',
    })
  })

  it('wraps plain Error objects generically', async () => {
    mockInvoke.mockRejectedValueOnce(new Error('socket closed unexpectedly'))

    const err = await ipc.getStatus().catch((e: unknown) => e)
    expect(err).toBeInstanceOf(CascadeIpcError)
    expect((err as CascadeIpcError).code).toBe('UnknownError')
  })
})
