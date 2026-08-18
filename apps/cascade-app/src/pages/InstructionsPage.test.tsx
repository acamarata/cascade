/**
 * Purpose: Unit tests for InstructionsPage component (T-P7-E21-02).
 *   Covers: loading state, global error, list view tier cards, search view,
 *   diff view, view-mode switching, refresh trigger, present/absent tier display.
 *   instructionTiers service is mocked so no real IPC is required.
 * Constraints: fetchAllTiers is injected via vi.mock; TIER_LABELS order is preserved.
 * SPORT: MASTER-COMPONENTS.md — InstructionsPage tests (T-P7-E21-02)
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import type { MockedFunction } from 'vitest'

// Mock the instruction tiers service before importing InstructionsPage.
vi.mock('../services/instructions/instructionTiers', () => ({
  fetchAllTiers: vi.fn(),
  searchInstructions: vi.fn(() => ({ hits: [], totalHits: 0 })),
  computeTierDiff: vi.fn(() => ({
    tier: 'GCI',
    lines: [],
    newLineCount: 0,
    inheritedLineCount: 0,
  })),
}))

import { within } from '@testing-library/react'
import { InstructionsPage } from './InstructionsPage'
import {
  fetchAllTiers,
  searchInstructions,
  computeTierDiff,
} from '../services/instructions/instructionTiers'
import type { TierContent } from '../types/instructions'
import { TIER_LABELS } from '../types/instructions'

const mockFetchAllTiers = fetchAllTiers as MockedFunction<typeof fetchAllTiers>
const mockSearchInstructions = searchInstructions as MockedFunction<typeof searchInstructions>
const mockComputeTierDiff = computeTierDiff as MockedFunction<typeof computeTierDiff>

// ── Fixtures ──────────────────────────────────────────────────────────────────

function makeTiers(overrides: Partial<Record<string, Partial<TierContent>>> = {}): TierContent[] {
  return TIER_LABELS.map((label) => ({
    tier: label,
    content: '',
    present: false,
    error: null,
    ...overrides[label],
  }))
}

// GCI: 2 lines (no trailing newline so split gives exactly 2 elements).
// PCI: 3 lines (trailing newline adds an empty element to split result).
const TIERS_WITH_GCI: TierContent[] = makeTiers({
  GCI: { content: '# Global Instructions\nApplies everywhere.', present: true },
  PCI: { content: '# Personal\nDownloads config.\n', present: true },
})

const TIERS_ALL_ABSENT = makeTiers()

// ── Tests ─────────────────────────────────────────────────────────────────────

beforeEach(() => {
  mockFetchAllTiers.mockReset()
  mockSearchInstructions.mockReset()
  mockSearchInstructions.mockReturnValue({ hits: [], totalHits: 0 })
  mockComputeTierDiff.mockReset()
  mockComputeTierDiff.mockReturnValue({
    tier: 'GCI',
    lines: [],
    newLineCount: 0,
    inheritedLineCount: 0,
  })
})

describe('InstructionsPage — loading state', () => {
  it('shows "Loading instruction tiers…" while fetch is in progress', async () => {
    // Promise that never resolves — keeps loading state active.
    mockFetchAllTiers.mockReturnValue(new Promise(() => {}))
    render(<InstructionsPage />)
    await waitFor(() =>
      expect(screen.getByText('Loading instruction tiers…')).toBeInTheDocument()
    )
  })
})

describe('InstructionsPage — error state', () => {
  it('displays the error message when fetchAllTiers rejects', async () => {
    mockFetchAllTiers.mockRejectedValue(new Error('daemon not running'))
    render(<InstructionsPage />)
    await waitFor(() =>
      expect(screen.getByText(/daemon not running/)).toBeInTheDocument()
    )
  })
})

describe('InstructionsPage — page header', () => {
  it('renders the "Instructions" heading', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
  })

  it('shows resolved tier count in header', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    // 2 of 6 present
    await waitFor(() => expect(screen.getByText(/2\/6 tiers resolved/)).toBeInTheDocument())
  })

  it('shows 0 resolved when all tiers are absent', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_ALL_ABSENT)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText(/0\/6 tiers resolved/)).toBeInTheDocument())
  })
})

describe('InstructionsPage — list view', () => {
  it('renders a tier card for each tier label', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    // Each tier has a badge with its label abbreviation.
    for (const label of TIER_LABELS) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0)
    }
  })

  it('shows "absent" label for tiers that are not present', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    // APC, PPC, PRC, PAC are absent in TIERS_WITH_GCI
    expect(screen.getAllByText('absent').length).toBeGreaterThan(0)
  })

  it('shows line count for present tiers', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    // GCI content has no trailing newline → 2 lines; PCI has trailing newline → 3.
    await waitFor(() => expect(screen.getByText('2 lines')).toBeInTheDocument())
    expect(screen.getByText('3 lines')).toBeInTheDocument()
  })

  it('expands a tier card when its header is clicked', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    // The article element has aria-label="Tier GCI"; the toggle button is inside it.
    const gciArticle = screen.getByRole('article', { name: 'Tier GCI' })
    fireEvent.click(within(gciArticle).getByRole('button'))
    await waitFor(() =>
      expect(screen.getByText(/Applies everywhere/)).toBeInTheDocument()
    )
  })
})

describe('InstructionsPage — view mode switching', () => {
  it('switches to Search view when Search button is clicked', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /Search/i }))
    await waitFor(() =>
      expect(screen.getByPlaceholderText('Search all instruction tiers…')).toBeInTheDocument()
    )
  })

  it('switches to Diff view when Diff button is clicked', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /Diff/i }))
    await waitFor(() =>
      expect(screen.getByRole('region', { name: 'Tier diff' })).toBeInTheDocument()
    )
  })

  it('returns to List view when List button is clicked from Search', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /Search/i }))
    fireEvent.click(screen.getByRole('button', { name: 'List' }))
    await waitFor(() =>
      expect(screen.queryByPlaceholderText('Search all instruction tiers…')).toBeNull()
    )
  })
})

describe('InstructionsPage — search view', () => {
  it('shows "Enter a search term" prompt when query is empty', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /Search/i }))
    await waitFor(() =>
      expect(
        screen.getByText('Enter a search term to find content across all instruction tiers.')
      ).toBeInTheDocument()
    )
  })

  it('shows match count when searchInstructions returns hits', async () => {
    mockSearchInstructions.mockReturnValue({
      hits: [
        { tier: 'GCI', lineNo: 1, lineText: 'Applies everywhere.', matchStart: 0, matchEnd: 7 },
      ],
      totalHits: 1,
    })
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /Search/i }))
    const input = await screen.findByPlaceholderText('Search all instruction tiers…')
    fireEvent.change(input, { target: { value: 'Applies' } })
    await waitFor(() => expect(screen.getByText('1 match')).toBeInTheDocument())
  })

  it('shows "No matches found" for a query with no results', async () => {
    mockSearchInstructions.mockReturnValue({ hits: [], totalHits: 0 })
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /Search/i }))
    const input = await screen.findByPlaceholderText('Search all instruction tiers…')
    fireEvent.change(input, { target: { value: 'xyzzy' } })
    await waitFor(() => expect(screen.getByText(/No matches found/)).toBeInTheDocument())
  })
})

describe('InstructionsPage — diff view', () => {
  it('renders tier selector nav in diff mode', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    mockComputeTierDiff.mockReturnValue({
      tier: 'GCI',
      lines: [{ text: '# Global Instructions', isNew: true }],
      newLineCount: 1,
      inheritedLineCount: 0,
    })
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: /Diff/i }))
    await waitFor(() =>
      expect(screen.getByRole('navigation', { name: 'Select tier for diff' })).toBeInTheDocument()
    )
  })
})

describe('InstructionsPage — refresh', () => {
  it('calls fetchAllTiers again when reload button is clicked', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_WITH_GCI)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    expect(mockFetchAllTiers).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Reload instruction tiers' }))
    await waitFor(() => expect(mockFetchAllTiers).toHaveBeenCalledTimes(2))
  })
})

describe('InstructionsPage — tier error', () => {
  it('shows "No CASCADE.md found for this tier" for absent tier when expanded', async () => {
    mockFetchAllTiers.mockResolvedValue(TIERS_ALL_ABSENT)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    const gciArticle = screen.getByRole('article', { name: 'Tier GCI' })
    fireEvent.click(within(gciArticle).getByRole('button'))
    await waitFor(() =>
      expect(screen.getByText('No CASCADE.md found for this tier.')).toBeInTheDocument()
    )
  })

  it('shows error text for a tier with an error', async () => {
    const tiersWithError = makeTiers({
      GCI: { error: 'permission denied', present: false, content: '' },
    })
    mockFetchAllTiers.mockResolvedValue(tiersWithError)
    render(<InstructionsPage />)
    await waitFor(() => expect(screen.getByText('Instructions')).toBeInTheDocument())
    // GCI should show "Error" label in collapsed state
    expect(screen.getByText('Error')).toBeInTheDocument()
    // Expand it via the article's inner button
    const gciArticle = screen.getByRole('article', { name: 'Tier GCI' })
    fireEvent.click(within(gciArticle).getByRole('button'))
    await waitFor(() =>
      expect(screen.getByText('permission denied')).toBeInTheDocument()
    )
  })
})
