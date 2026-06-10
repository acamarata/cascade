// @vitest-environment jsdom
/**
 * Purpose: Vitest tests for TemplatePickerCompact — wizard Phase 4 pre-scaffold picker.
 *   Tests: loading state, error state, empty catalog state, render + filter + multi-select,
 *   onChange emission, no-match empty state.
 * Inputs:  Static TemplateEntryIpc[] fixtures, spied onChange.
 * Outputs: Vitest + RTL assertions.
 * SPORT: MASTER-COMPONENTS.md — TemplatePickerCompact tests — T-P3-E05-19
 */

import '@testing-library/jest-dom'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { TemplatePickerCompact } from './TemplatePickerCompact'
import type { TemplateEntryIpc } from '../../types/templates'

// ── Fixtures ──────────────────────────────────────────────────────────────────

function makeEntry(overrides: Partial<TemplateEntryIpc> = {}): TemplateEntryIpc {
  return {
    id: 'gci-base',
    version: '1.0.0',
    tier: 'gci',
    stacks: ['rust'],
    projectShapes: ['app'],
    description: 'Base GCI template.',
    body: '# GCI Base\n',
    ...overrides,
  }
}

const ENTRY_A = makeEntry({ id: 'gci-base', tier: 'gci', stacks: ['rust'], description: 'Base GCI template.' })
const ENTRY_B = makeEntry({ id: 'prc-react', tier: 'prc', stacks: ['react'], description: 'React project template.' })
const ENTRY_C = makeEntry({ id: 'pac-lib', tier: 'pac', stacks: ['rust'], description: 'Library template.' })
const ALL_ENTRIES = [ENTRY_A, ENTRY_B, ENTRY_C]

// ── Tests ─────────────────────────────────────────────────────────────────────

describe('TemplatePickerCompact', () => {
  let onChange: ReturnType<typeof vi.fn>

  beforeEach(() => {
    onChange = vi.fn()
  })

  // ── Loading state ────────────────────────────────────────────────────────

  it('renders loading skeleton when isLoading=true', () => {
    const { container } = render(
      <TemplatePickerCompact
        entries={[]}
        selectedIds={[]}
        onChange={onChange}
        isLoading
      />,
    )
    // Loading state renders a div with aria-busy="true"
    const loadingEl = container.querySelector('[aria-busy="true"]')
    expect(loadingEl).not.toBeNull()
    expect(loadingEl).toHaveAttribute('aria-label', 'Loading templates')
  })

  // ── Error state ──────────────────────────────────────────────────────────

  it('renders error alert when loadError is set', () => {
    render(
      <TemplatePickerCompact
        entries={[]}
        selectedIds={[]}
        onChange={onChange}
        loadError="daemon unavailable"
      />,
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(/daemon unavailable/i)).toBeInTheDocument()
  })

  // ── Empty catalog state ──────────────────────────────────────────────────

  it('renders empty catalog message when entries is empty', () => {
    render(
      <TemplatePickerCompact entries={[]} selectedIds={[]} onChange={onChange} />,
    )
    expect(screen.getByText(/no templates available/i)).toBeInTheDocument()
  })

  // ── Renders entries ──────────────────────────────────────────────────────

  it('renders all entry ids when no filter is set', () => {
    render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    expect(screen.getByText('gci-base')).toBeInTheDocument()
    expect(screen.getByText('prc-react')).toBeInTheDocument()
    expect(screen.getByText('pac-lib')).toBeInTheDocument()
  })

  // ── Search filter ────────────────────────────────────────────────────────

  it('filters cards in real-time by search query in id', () => {
    render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'gci' } })
    expect(screen.getByText('gci-base')).toBeInTheDocument()
    expect(screen.queryByText('prc-react')).not.toBeInTheDocument()
  })

  it('filters cards by search query in description', () => {
    render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'Library' } })
    expect(screen.getByText('pac-lib')).toBeInTheDocument()
    expect(screen.queryByText('prc-react')).not.toBeInTheDocument()
  })

  it('shows no-match message when filters eliminate all cards', () => {
    render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'zzz-nothing' } })
    expect(screen.getByText(/no templates match your filters/i)).toBeInTheDocument()
  })

  // ── Tier filter ──────────────────────────────────────────────────────────

  it('filters cards by tier chip selection', () => {
    render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    const tierSelect = screen.getByLabelText(/filter by tier/i)
    fireEvent.change(tierSelect, { target: { value: 'pac' } })
    expect(screen.getByText('pac-lib')).toBeInTheDocument()
    expect(screen.queryByText('gci-base')).not.toBeInTheDocument()
  })

  // ── Stack filter ─────────────────────────────────────────────────────────

  it('renders the stack chip when stacks are available', () => {
    render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    expect(screen.getByLabelText(/filter by stack/i)).toBeInTheDocument()
  })

  it('filters cards by stack chip selection', () => {
    render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    const stackSelect = screen.getByLabelText(/filter by stack/i)
    fireEvent.change(stackSelect, { target: { value: 'react' } })
    expect(screen.getByText('prc-react')).toBeInTheDocument()
    expect(screen.queryByText('gci-base')).not.toBeInTheDocument()
  })

  // ── Multi-select / onChange emission ─────────────────────────────────────

  it('calls onChange with [id] when a card checkbox is toggled on', () => {
    render(
      <TemplatePickerCompact entries={[ENTRY_A]} selectedIds={[]} onChange={onChange} />,
    )
    const checkbox = screen.getByRole('checkbox', { name: /select template gci-base/i })
    fireEvent.click(checkbox)
    expect(onChange).toHaveBeenCalledWith(['gci-base'])
  })

  it('calls onChange without the id when a checked card is toggled off', () => {
    render(
      <TemplatePickerCompact
        entries={[ENTRY_A]}
        selectedIds={['gci-base']}
        onChange={onChange}
      />,
    )
    const checkbox = screen.getByRole('checkbox', { name: /select template gci-base/i })
    fireEvent.click(checkbox)
    expect(onChange).toHaveBeenCalledWith([])
  })

  it('accumulates multiple selected ids correctly', () => {
    const { rerender } = render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    // Select first
    fireEvent.click(screen.getByRole('checkbox', { name: /select template gci-base/i }))
    expect(onChange).toHaveBeenLastCalledWith(['gci-base'])

    // Re-render with gci-base selected, then select prc-react
    rerender(
      <TemplatePickerCompact
        entries={ALL_ENTRIES}
        selectedIds={['gci-base']}
        onChange={onChange}
      />,
    )
    fireEvent.click(screen.getByRole('checkbox', { name: /select template prc-react/i }))
    expect(onChange).toHaveBeenLastCalledWith(['gci-base', 'prc-react'])
  })

  // ── Selection summary badge ──────────────────────────────────────────────

  it('shows selection count badge when templates are selected', () => {
    render(
      <TemplatePickerCompact
        entries={ALL_ENTRIES}
        selectedIds={['gci-base', 'pac-lib']}
        onChange={onChange}
      />,
    )
    expect(screen.getByText(/2 templates selected/i)).toBeInTheDocument()
  })

  it('does not show count badge when nothing is selected', () => {
    render(
      <TemplatePickerCompact entries={ALL_ENTRIES} selectedIds={[]} onChange={onChange} />,
    )
    expect(screen.queryByText(/selected/i)).not.toBeInTheDocument()
  })
})
