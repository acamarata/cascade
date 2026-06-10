/**
 * Purpose: Unit tests for TemplateBrowser page and helper functions.
 *   Tests: filter helpers, loading state, error state, empty state,
 *   card rendering, search filtering, card selection + preview population,
 *   Apply button disabled state.
 * Inputs:  Mocked @tauri-apps/api/core invoke.
 * Outputs: Vitest + RTL assertions.
 * SPORT: MASTER-COMPONENTS.md — TemplateBrowser tests
 */

import '@testing-library/jest-dom'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { MockedFunction } from 'vitest'

// Mock Tauri invoke BEFORE importing components that use it.
vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi.fn(),
}))

import { invoke } from '@tauri-apps/api/core'
import { TemplateBrowser, extractStacks, extractShapes, applyFilter } from './TemplateBrowser'
import type { TemplateEntryIpc } from '../types/templates'

const mockInvoke = invoke as MockedFunction<typeof invoke>

// ── Fixtures ──────────────────────────────────────────────────────────────────

function makeEntry(overrides: Partial<TemplateEntryIpc> = {}): TemplateEntryIpc {
  return {
    id: 'gci-base',
    version: '1.0.0',
    tier: 'gci',
    stacks: ['rust', 'tauri'],
    projectShapes: ['app', 'lib'],
    description: 'Base GCI template for all projects.',
    body: '# GCI Base\n\nThis is the base template.',
    ...overrides,
  }
}

const ENTRY_A = makeEntry({ id: 'gci-base', tier: 'gci', stacks: ['rust'], projectShapes: ['app'] })
const ENTRY_B = makeEntry({ id: 'prc-react', tier: 'prc', stacks: ['react'], projectShapes: ['app'], description: 'React project template.' })
const ENTRY_C = makeEntry({ id: 'pac-lib', tier: 'pac', stacks: ['rust'], projectShapes: ['lib'], description: 'Library template.' })

const ALL_ENTRIES = [ENTRY_A, ENTRY_B, ENTRY_C]

// ── Helper unit tests ─────────────────────────────────────────────────────────

describe('extractStacks', () => {
  it('returns sorted unique stacks', () => {
    expect(extractStacks(ALL_ENTRIES)).toEqual(['react', 'rust'])
  })
  it('returns empty array for empty catalog', () => {
    expect(extractStacks([])).toEqual([])
  })
})

describe('extractShapes', () => {
  it('returns sorted unique shapes', () => {
    expect(extractShapes(ALL_ENTRIES)).toEqual(['app', 'lib'])
  })
})

describe('applyFilter', () => {
  it('returns all entries with empty filter', () => {
    expect(applyFilter(ALL_ENTRIES, { search: '', tier: '', stack: '', shape: '' })).toHaveLength(3)
  })

  it('filters by search term in id', () => {
    const result = applyFilter(ALL_ENTRIES, { search: 'gci', tier: '', stack: '', shape: '' })
    expect(result).toHaveLength(1)
    expect(result[0]!.id).toBe('gci-base')
  })

  it('filters by search term in description', () => {
    const result = applyFilter(ALL_ENTRIES, { search: 'React project', tier: '', stack: '', shape: '' })
    expect(result).toHaveLength(1)
    expect(result[0]!.id).toBe('prc-react')
  })

  it('filters by tier', () => {
    const result = applyFilter(ALL_ENTRIES, { search: '', tier: 'pac', stack: '', shape: '' })
    expect(result).toHaveLength(1)
    expect(result[0]!.id).toBe('pac-lib')
  })

  it('filters by stack', () => {
    const result = applyFilter(ALL_ENTRIES, { search: '', tier: '', stack: 'react', shape: '' })
    expect(result).toHaveLength(1)
    expect(result[0]!.id).toBe('prc-react')
  })

  it('filters by shape', () => {
    const result = applyFilter(ALL_ENTRIES, { search: '', tier: '', stack: '', shape: 'lib' })
    expect(result).toHaveLength(1)
    expect(result[0]!.id).toBe('pac-lib')
  })

  it('combines multiple filters with AND logic', () => {
    const result = applyFilter(ALL_ENTRIES, { search: '', tier: 'pac', stack: 'rust', shape: 'lib' })
    expect(result).toHaveLength(1)
    expect(result[0]!.id).toBe('pac-lib')
  })

  it('returns empty when no entries match', () => {
    const result = applyFilter(ALL_ENTRIES, { search: 'zzz-nonexistent', tier: '', stack: '', shape: '' })
    expect(result).toHaveLength(0)
  })
})

// ── TemplateBrowser integration tests ─────────────────────────────────────────

function renderBrowser() {
  return render(
    <MemoryRouter>
      <TemplateBrowser />
    </MemoryRouter>,
  )
}

describe('TemplateBrowser', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    // Default: template_list_upgradeable returns empty upgrade map (no badges).
    mockInvoke.mockImplementation((cmd: string) => {
      if (cmd === 'template_list_upgradeable') return Promise.resolve({})
      return Promise.resolve([])
    })
  })

  it('shows loading state initially', () => {
    // invoke never resolves during this test
    mockInvoke.mockReturnValue(new Promise(() => {}))
    renderBrowser()
    expect(screen.getByRole('status', { name: /loading templates/i })).toBeInTheDocument()
  })

  it('renders cards after successful IPC fetch', async () => {
    mockInvoke.mockResolvedValueOnce(ALL_ENTRIES)
    renderBrowser()
    await waitFor(() => {
      expect(screen.getByText('gci-base')).toBeInTheDocument()
      expect(screen.getByText('prc-react')).toBeInTheDocument()
      expect(screen.getByText('pac-lib')).toBeInTheDocument()
    })
  })

  it('shows error state on IPC failure', async () => {
    mockInvoke.mockRejectedValueOnce(new Error('daemon unavailable'))
    renderBrowser()
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument()
      expect(screen.getByText(/daemon unavailable/i)).toBeInTheDocument()
    })
  })

  it('retries on retry button click', async () => {
    mockInvoke
      .mockRejectedValueOnce(new Error('timeout'))
      .mockResolvedValueOnce([ENTRY_A])
    renderBrowser()
    await waitFor(() => screen.getByRole('alert'))
    fireEvent.click(screen.getByRole('button', { name: /retry/i }))
    await waitFor(() => screen.getByText('gci-base'))
    // 3 calls: initial template_list (rejected) + retry template_list + retry template_list_upgradeable
    expect(mockInvoke).toHaveBeenCalledTimes(3)
  })

  it('shows empty state when catalog is empty', async () => {
    mockInvoke.mockResolvedValueOnce([])
    renderBrowser()
    await waitFor(() => {
      expect(screen.getByText(/no templates found/i)).toBeInTheDocument()
    })
  })

  it('filters cards in real-time when typing in search box', async () => {
    mockInvoke.mockResolvedValueOnce(ALL_ENTRIES)
    renderBrowser()
    await waitFor(() => screen.getByText('gci-base'))

    const searchInput = screen.getByRole('searchbox', { name: /search templates/i })
    fireEvent.change(searchInput, { target: { value: 'gci' } })

    expect(screen.getByText('gci-base')).toBeInTheDocument()
    expect(screen.queryByText('prc-react')).not.toBeInTheDocument()
  })

  it('shows empty-with-filter message when filters eliminate all cards', async () => {
    mockInvoke.mockResolvedValueOnce(ALL_ENTRIES)
    renderBrowser()
    await waitFor(() => screen.getByText('gci-base'))

    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'zzz-nothing' } })
    expect(screen.getByText(/no templates match the current filters/i)).toBeInTheDocument()
  })

  it('populates preview panel when a card is selected', async () => {
    mockInvoke.mockResolvedValueOnce(ALL_ENTRIES)
    renderBrowser()
    await waitFor(() => screen.getByText('gci-base'))

    // Click first card
    fireEvent.click(screen.getByText('gci-base'))

    // Preview panel should show the template body (from react-markdown render)
    await waitFor(() => {
      expect(screen.getByRole('article', { name: /template content/i })).toBeInTheDocument()
    })
  })

  it('Apply button is disabled when no template is selected', async () => {
    mockInvoke.mockResolvedValueOnce(ALL_ENTRIES)
    renderBrowser()
    await waitFor(() => screen.getByText('gci-base'))

    const applyBtn = screen.getByRole('button', { name: /apply template/i })
    expect(applyBtn).toBeDisabled()
    expect(applyBtn).toHaveAttribute('aria-disabled', 'true')
  })

  it('Apply button is enabled after selecting a template', async () => {
    mockInvoke.mockResolvedValueOnce(ALL_ENTRIES)
    renderBrowser()
    await waitFor(() => screen.getByText('gci-base'))

    fireEvent.click(screen.getByText('gci-base'))

    const applyBtn = screen.getByRole('button', { name: /apply template/i })
    expect(applyBtn).not.toBeDisabled()
  })

  it('grid has role=grid and cards have aria-selected', async () => {
    mockInvoke.mockResolvedValueOnce([ENTRY_A])
    renderBrowser()
    await waitFor(() => screen.getByText('gci-base'))

    const grid = screen.getByRole('grid', { name: /templates/i })
    expect(grid).toBeInTheDocument()

    const card = screen.getByRole('gridcell')
    expect(card).toHaveAttribute('aria-selected', 'false')

    fireEvent.click(card)
    expect(card).toHaveAttribute('aria-selected', 'true')
  })
})
