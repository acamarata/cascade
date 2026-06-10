/**
 * Purpose: Unit tests for useProviderConnected hook.
 * Inputs:  Mocked @tauri-apps/api/core invoke function.
 * Outputs: Verified that the hook returns anyConnected=false when health IPC
 *   returns empty list or throws; anyConnected=true with connected provider ids.
 * Constraints: Tests run in vitest; no real Tauri context required.
 * SPORT: MASTER-HOOKS.md — useProviderConnected tests (T-P3-E03-42)
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { MockedFunction } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'

// Mock @tauri-apps/api/core before importing hook
vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi.fn(),
}))

import { invoke } from '@tauri-apps/api/core'
import { useProviderConnected } from './useProviderConnected'

const mockInvoke = invoke as MockedFunction<typeof invoke>

beforeEach(() => {
  mockInvoke.mockReset()
})

describe('useProviderConnected', () => {
  it('returns anyConnected=false and empty list when IPC returns empty array', async () => {
    mockInvoke.mockResolvedValueOnce([])

    const { result } = renderHook(() => useProviderConnected())

    // Initial state is false
    expect(result.current.anyConnected).toBe(false)
    expect(result.current.connectedProviders).toEqual([])

    // After resolve, still false (empty list)
    await waitFor(() => {
      expect(mockInvoke).toHaveBeenCalledWith('cascade_providers_health')
    })
    expect(result.current.anyConnected).toBe(false)
    expect(result.current.connectedProviders).toEqual([])
  })

  it('returns anyConnected=false when IPC call throws (daemon not running)', async () => {
    mockInvoke.mockRejectedValueOnce(new Error('DaemonNotRunning'))

    const { result } = renderHook(() => useProviderConnected())

    await waitFor(() => {
      expect(mockInvoke).toHaveBeenCalledWith('cascade_providers_health')
    })

    expect(result.current.anyConnected).toBe(false)
    expect(result.current.connectedProviders).toEqual([])
  })

  it('returns anyConnected=true with provider ids when IPC returns non-empty list', async () => {
    const providers = ['gemini-flash', 'claude-sonnet']
    mockInvoke.mockResolvedValueOnce(providers)

    const { result } = renderHook(() => useProviderConnected())

    await waitFor(() => {
      expect(result.current.anyConnected).toBe(true)
    })

    expect(result.current.connectedProviders).toEqual(providers)
  })

  it('registers and removes a focus event listener on mount/unmount', () => {
    mockInvoke.mockResolvedValue([])
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')

    const { unmount } = renderHook(() => useProviderConnected())

    expect(addSpy).toHaveBeenCalledWith('focus', expect.any(Function))
    unmount()
    expect(removeSpy).toHaveBeenCalledWith('focus', expect.any(Function))
  })

  it('never throws even when IPC rejects with string error', async () => {
    mockInvoke.mockRejectedValueOnce('DaemonNotRunning: socket not found')

    const { result } = renderHook(() => useProviderConnected())

    await waitFor(() => {
      expect(mockInvoke).toHaveBeenCalled()
    })

    // Should not have thrown — hook still accessible with safe defaults
    expect(result.current.anyConnected).toBe(false)
    expect(result.current.connectedProviders).toEqual([])
  })
})
