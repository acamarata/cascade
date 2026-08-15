// @vitest-environment jsdom
/**
 * Purpose: Tests for CascadeTierTree's "Create" action (T-P7-E10-03) — the
 *   button previously only opened Finder; it now calls create_cascade_tier
 *   and refreshes the tree on success.
 * Inputs:  None — invoke() mocked, no real Tauri context required.
 * Constraints: Vitest jsdom environment.
 * SPORT: MASTER-COMPONENTS.md — CascadeTierTree (T-P7-E10-03)
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi.fn(),
}))

import { invoke } from '@tauri-apps/api/core'
import { CascadeTierTree } from './CascadeTierTree'

const mockInvoke = vi.mocked(invoke)

const MISSING_TIER = {
  tier: 'PRC' as const,
  name: 'Project Cascade Instructions',
  path: '/tmp/project/.cascade/CASCADE.md',
  exists: false,
}

const PRESENT_TIER = {
  tier: 'GCI' as const,
  name: 'Global Cascade Instructions',
  path: '/home/user/.cascade/CASCADE.md',
  exists: true,
}

describe('CascadeTierTree Create button (T-P7-E10-03)', () => {
  beforeEach(() => {
    mockInvoke.mockReset()
  })

  it('calls create_cascade_tier with the tier and path, then refreshes the tree', async () => {
    mockInvoke.mockImplementation((cmd: string) => {
      if (cmd === 'get_cascade_tier_tree') {
        return Promise.resolve([MISSING_TIER])
      }
      if (cmd === 'create_cascade_tier') {
        return Promise.resolve({
          dirsCreated: ['.cascade/', '.cascade/skills/'],
          filesWritten: ['.cascade/CASCADE.md'],
        })
      }
      return Promise.reject(new Error(`unexpected command: ${cmd}`))
    })

    render(<CascadeTierTree projectRoot="/tmp/project" />)

    const createButton = await screen.findByLabelText('Create PRC at /tmp/project/.cascade/CASCADE.md')
    fireEvent.click(createButton)

    await waitFor(() => {
      expect(mockInvoke).toHaveBeenCalledWith('create_cascade_tier', {
        tier: 'PRC',
        path: '/tmp/project/.cascade/CASCADE.md',
      })
    })

    // Refresh (get_cascade_tier_tree) is called again after a successful create.
    await waitFor(() => {
      const calls = mockInvoke.mock.calls.filter(([cmd]) => cmd === 'get_cascade_tier_tree')
      expect(calls.length).toBeGreaterThanOrEqual(2)
    })
  })

  it('shows an error message and does not refresh when create_cascade_tier fails', async () => {
    mockInvoke.mockImplementation((cmd: string) => {
      if (cmd === 'get_cascade_tier_tree') {
        return Promise.resolve([MISSING_TIER])
      }
      if (cmd === 'create_cascade_tier') {
        return Promise.reject(new Error('permission denied'))
      }
      return Promise.reject(new Error(`unexpected command: ${cmd}`))
    })

    render(<CascadeTierTree projectRoot="/tmp/project" />)

    const createButton = await screen.findByLabelText('Create PRC at /tmp/project/.cascade/CASCADE.md')
    fireEvent.click(createButton)

    expect(await screen.findByRole('alert')).toHaveTextContent('permission denied')

    const refreshCalls = mockInvoke.mock.calls.filter(([cmd]) => cmd === 'get_cascade_tier_tree')
    expect(refreshCalls.length).toBe(1)
  })

  it('does not render a Create button for a tier that already exists', async () => {
    mockInvoke.mockImplementation((cmd: string) => {
      if (cmd === 'get_cascade_tier_tree') {
        return Promise.resolve([PRESENT_TIER])
      }
      return Promise.reject(new Error(`unexpected command: ${cmd}`))
    })

    render(<CascadeTierTree projectRoot="/tmp/project" />)

    await screen.findByText('GCI')
    expect(screen.queryByLabelText(/Create GCI/)).not.toBeInTheDocument()
  })

  it('disables the Create button and shows "Creating…" while the call is in flight', async () => {
    let resolveCreate: (() => void) | undefined
    mockInvoke.mockImplementation((cmd: string) => {
      if (cmd === 'get_cascade_tier_tree') {
        return Promise.resolve([MISSING_TIER])
      }
      if (cmd === 'create_cascade_tier') {
        return new Promise((resolve) => {
          resolveCreate = () => resolve({ dirsCreated: [], filesWritten: [] })
        })
      }
      return Promise.reject(new Error(`unexpected command: ${cmd}`))
    })

    render(<CascadeTierTree projectRoot="/tmp/project" />)

    const createButton = await screen.findByLabelText('Create PRC at /tmp/project/.cascade/CASCADE.md')
    fireEvent.click(createButton)

    await waitFor(() => {
      expect(screen.getByLabelText('Create PRC at /tmp/project/.cascade/CASCADE.md')).toBeDisabled()
    })
    expect(screen.getByText('Creating…')).toBeInTheDocument()

    resolveCreate?.()
  })
})
