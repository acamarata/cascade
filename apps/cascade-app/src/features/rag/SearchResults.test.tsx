/**
 * Purpose: Unit tests for SearchResults component.
 *   Covers: citation cards render, loading state, error state, empty state,
 *   accessibility attributes, vault-open seam, score display, heading badge.
 * SPORT: MASTER-COMPONENTS.md — SearchResults tests (T-P4-E01-27)
 */

import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SearchResults } from './SearchResults'
import type { RagCitation } from './types'

// ── Fixtures ──────────────────────────────────────────────────────────────────

const CITATION_A: RagCitation = {
  path: '/home/user/docs/setup.md',
  heading_path: '## Setup > ### Install',
  snippet: 'Run pnpm install to set up dependencies.',
  rrf_score: 0.94567,
}

const CITATION_B: RagCitation = {
  path: '/home/user/docs/config.md',
  heading_path: '',
  snippet: 'Edit cascade.toml to configure the daemon.',
  rrf_score: 0.72,
}

const TWO_CITATIONS = [CITATION_A, CITATION_B]

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('SearchResults', () => {
  it('renders null when no citations, no loading, no error', () => {
    const { container } = render(
      <SearchResults citations={[]} loading={false} error={null} />,
    )
    expect(container.firstChild).toBeNull()
  })

  it('renders a loading spinner when loading=true', () => {
    render(<SearchResults citations={[]} loading={true} error={null} />)
    // aria-busy container present
    expect(document.querySelector('[aria-busy="true"]')).not.toBeNull()
  })

  it('renders sr-only loading text', () => {
    render(<SearchResults citations={[]} loading={true} error={null} />)
    expect(screen.getByText('Searching…')).toBeTruthy()
  })

  it('renders an error banner when error is provided', () => {
    render(<SearchResults citations={[]} loading={false} error="daemon unavailable" />)
    expect(screen.getByRole('alert')).toBeTruthy()
    expect(screen.getByText(/daemon unavailable/i)).toBeTruthy()
  })

  it('renders citation cards in a role=list container', () => {
    render(<SearchResults citations={TWO_CITATIONS} loading={false} error={null} />)
    expect(screen.getByRole('list', { name: /citations/i })).toBeTruthy()
  })

  it('renders the correct count of listitem elements', () => {
    render(<SearchResults citations={TWO_CITATIONS} loading={false} error={null} />)
    expect(screen.getAllByRole('listitem')).toHaveLength(2)
  })

  it('shows rank badge for each citation', () => {
    render(<SearchResults citations={TWO_CITATIONS} loading={false} error={null} />)
    expect(screen.getByLabelText('Rank 1')).toBeTruthy()
    expect(screen.getByLabelText('Rank 2')).toBeTruthy()
  })

  it('displays the rrf_score with 3 decimal places', () => {
    render(<SearchResults citations={[CITATION_A]} loading={false} error={null} />)
    // Score badge uses title attribute and aria-label — check the rendered text.
    expect(screen.getByText('0.946')).toBeTruthy()
  })

  it('displays the heading_path badge when present', () => {
    render(<SearchResults citations={[CITATION_A]} loading={false} error={null} />)
    expect(screen.getByText('## Setup > ### Install')).toBeTruthy()
  })

  it('does not render a heading badge when heading_path is empty', () => {
    render(<SearchResults citations={[CITATION_B]} loading={false} error={null} />)
    // CITATION_B has empty heading_path — badge should not render
    expect(screen.queryByText('## Setup > ### Install')).toBeNull()
  })

  it('displays the snippet text', () => {
    render(<SearchResults citations={[CITATION_A]} loading={false} error={null} />)
    expect(screen.getByText(/pnpm install/i)).toBeTruthy()
  })

  it('shows result count', () => {
    render(<SearchResults citations={TWO_CITATIONS} loading={false} error={null} />)
    expect(screen.getByText(/2 results/i)).toBeTruthy()
  })

  it('shows singular "result" when count is 1', () => {
    render(<SearchResults citations={[CITATION_A]} loading={false} error={null} />)
    expect(screen.getByText(/1 result$/i)).toBeTruthy()
  })

  it('shows query latency when queryMs is provided', () => {
    render(
      <SearchResults citations={[CITATION_A]} loading={false} error={null} queryMs={42} />,
    )
    expect(screen.getByText('42ms')).toBeTruthy()
  })

  it('does not show latency when queryMs is null', () => {
    render(
      <SearchResults citations={[CITATION_A]} loading={false} error={null} queryMs={null} />,
    )
    expect(screen.queryByText(/ms$/)).toBeNull()
  })

  it('calls onOpenFile when the file path button is clicked', async () => {
    const handler = vi.fn()
    render(
      <SearchResults
        citations={[CITATION_A]}
        loading={false}
        error={null}
        onOpenFile={handler}
      />,
    )
    const btn = screen.getByRole('button', { name: /open setup\.md in vault/i })
    await userEvent.click(btn)
    expect(handler).toHaveBeenCalledOnce()
    expect(handler).toHaveBeenCalledWith(CITATION_A.path)
  })

  it('does not show the ExternalLink icon when onOpenFile is absent', () => {
    const { container } = render(
      <SearchResults citations={[CITATION_A]} loading={false} error={null} />,
    )
    // When no onOpenFile, the button aria-label is missing the "Open ... in vault" text.
    expect(screen.queryByRole('button', { name: /open .* in vault/i })).toBeNull()
    // The file name is still visible as text but not a vault-open aria-label button.
    expect(container.querySelector('[aria-label*="in vault"]')).toBeNull()
  })

  it('renders multiple data-testid=citation-card elements', () => {
    const { container } = render(
      <SearchResults citations={TWO_CITATIONS} loading={false} error={null} />,
    )
    expect(container.querySelectorAll('[data-testid="citation-card"]')).toHaveLength(2)
  })
})
