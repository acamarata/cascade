/**
 * Tests for FileListPanel (and the thin wrapper panels that use it).
 * Covers: data render, loading state, error state + retry, search filter, empty state, count.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { RulesPanel } from '../RulesPanel'
import type { FileListResponse } from '@/lib/api'

const MOCK_FILE_ITEMS = [
  { name: 'delegation-mandate.md', path: '~/.claude/rules/delegation-mandate.md', modified_at: '2026-06-01T00:00:00Z' },
  { name: 'model-strategy.md', path: '~/.claude/rules/model-strategy.md', modified_at: '2026-06-01T00:00:00Z' },
  { name: 'phase-based-development.md', path: '~/.claude/rules/phase-based-development.md', modified_at: '2026-06-01T00:00:00Z' },
]

function mockFetchSuccess(data: FileListResponse) {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      json: async () => data,
    } as Response),
  )
}

function mockFetchError(status = 500) {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: false,
      status,
      statusText: 'Internal Server Error',
    } as Response),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('FileListPanel — via RulesPanel', () => {
  it('shows loading state initially before fetch resolves', async () => {
    // fetch that never resolves — component stays in loading
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(new Promise(() => {})))
    await act(async () => {
      render(<RulesPanel />)
    })
    expect(screen.getByText(/loading/i)).toBeInTheDocument()
  })

  it('renders file list when data loads successfully', async () => {
    mockFetchSuccess({ items: MOCK_FILE_ITEMS, total: 3 })
    await act(async () => {
      render(<RulesPanel />)
    })

    await waitFor(() => {
      expect(screen.getByText('delegation-mandate.md')).toBeInTheDocument()
    })
    expect(screen.getByText('model-strategy.md')).toBeInTheDocument()
    expect(screen.getByText('phase-based-development.md')).toBeInTheDocument()
  })

  it('shows error message and retry button on fetch failure', async () => {
    mockFetchError(500)
    await act(async () => {
      render(<RulesPanel />)
    })

    await waitFor(() => {
      expect(screen.getByText(/500/)).toBeInTheDocument()
      expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    })
  })

  it('retries fetch successfully when retry button is clicked', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({ ok: false, status: 503, statusText: 'Service Unavailable' } as Response)
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({ items: MOCK_FILE_ITEMS, total: 3 }),
      } as Response)
    vi.stubGlobal('fetch', fetchMock)

    await act(async () => {
      render(<RulesPanel />)
    })

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument()
    })

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    })

    await waitFor(() => {
      expect(screen.getByText('delegation-mandate.md')).toBeInTheDocument()
    })

    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('shows empty state message when items array is empty', async () => {
    mockFetchSuccess({ items: [], total: 0 })
    await act(async () => {
      render(<RulesPanel />)
    })

    await waitFor(() => {
      expect(screen.getByText(/no gci rules found/i)).toBeInTheDocument()
    })
  })

  it('filters the list by search query (client-side)', async () => {
    mockFetchSuccess({ items: MOCK_FILE_ITEMS, total: 3 })
    await act(async () => {
      render(<RulesPanel />)
    })

    await waitFor(() => {
      expect(screen.getByText('delegation-mandate.md')).toBeInTheDocument()
    })

    fireEvent.change(screen.getByPlaceholderText(/filter/i), { target: { value: 'model' } })

    expect(screen.getByText('model-strategy.md')).toBeInTheDocument()
    expect(screen.queryByText('delegation-mandate.md')).not.toBeInTheDocument()
    expect(screen.queryByText('phase-based-development.md')).not.toBeInTheDocument()
  })

  it('displays item count from response', async () => {
    mockFetchSuccess({ items: MOCK_FILE_ITEMS, total: 3 })
    await act(async () => {
      render(<RulesPanel />)
    })

    await waitFor(() => {
      // total count should appear somewhere in the panel header/subtext
      expect(screen.getByText(/3/)).toBeInTheDocument()
    })
  })
})
