/**
 * Purpose: Unit tests for daemonSlice — polling status transitions on success and failure.
 * Inputs:  Mocked ipc.getStatus() via vi.mock.
 * Outputs: Vitest assertions on Zustand store state after polling.
 * Constraints: Real timers used; manual wait via vi.waitFor; resets interval between tests.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { create } from 'zustand'
import { immer } from 'zustand/middleware/immer'

import { CascadeIpcError } from '../types/errors'

vi.mock('../lib/ipc/client', () => ({
  ipc: {
    getStatus: vi.fn(),
  },
}))

import { ipc } from '../lib/ipc/client'
import { createDaemonSlice, _resetPollInterval } from './daemon.slice'
import type { DaemonSlice } from './daemon.slice'
import type { StatusResult } from '../types/ipc'

const mockStatus: StatusResult = {
  pid: 1234,
  uptime_secs: 60,
  queue_depth: 0,
  rag_index_fresh: true,
  version: '0.1.0',
}

function makeStore() {
  return create<DaemonSlice>()(immer((...a) => createDaemonSlice(...a)))
}

describe('daemonSlice', () => {
  beforeEach(() => {
    vi.mocked(ipc.getStatus).mockReset()
    _resetPollInterval()
  })

  afterEach(() => {
    _resetPollInterval()
  })

  it('initial state is disconnected', () => {
    const store = makeStore()
    expect(store.getState().status).toBe('disconnected')
    expect(store.getState().data).toBeNull()
  })

  it('transitions to connected on successful getStatus', async () => {
    vi.mocked(ipc.getStatus).mockResolvedValue(mockStatus)
    const store = makeStore()
    store.getState().startPolling()
    await vi.waitFor(() => {
      expect(store.getState().status).toBe('connected')
    })
    expect(store.getState().data).toEqual(mockStatus)
    expect(store.getState().lastPing).not.toBeNull()
    expect(store.getState().error).toBeNull()
    store.getState().stopPolling()
  })

  it('transitions to error on CascadeIpcError rejection', async () => {
    vi.mocked(ipc.getStatus).mockRejectedValue(
      new CascadeIpcError('DaemonNotRunning', 'not running')
    )
    const store = makeStore()
    store.getState().startPolling()
    await vi.waitFor(() => {
      expect(store.getState().status).toBe('error')
    })
    expect(store.getState().error).toBe('not running')
    store.getState().stopPolling()
  })

  it('stopPolling clears the interval — no further calls after stop', async () => {
    vi.mocked(ipc.getStatus).mockResolvedValue(mockStatus)
    const store = makeStore()
    store.getState().startPolling()
    await vi.waitFor(() => expect(store.getState().status).toBe('connected'))
    store.getState().stopPolling()
    const callCountAfterStop = vi.mocked(ipc.getStatus).mock.calls.length
    // Wait 100ms; interval is 5000ms so no additional calls should arrive.
    await new Promise((r) => setTimeout(r, 100))
    expect(vi.mocked(ipc.getStatus).mock.calls.length).toBe(callCountAfterStop)
  })

  it('startPolling is idempotent — second call does not duplicate poll', async () => {
    vi.mocked(ipc.getStatus).mockResolvedValue(mockStatus)
    const store = makeStore()
    store.getState().startPolling()
    store.getState().startPolling() // second call should be a no-op
    await vi.waitFor(() => expect(store.getState().status).toBe('connected'))
    // Only one immediate poll should have fired on startPolling.
    expect(ipc.getStatus).toHaveBeenCalledTimes(1)
    store.getState().stopPolling()
  })
})
