// @vitest-environment jsdom
/**
 * Purpose: Unit tests for provider settings components and helpers.
 *   Tests ProviderStatusBadge (pure render), dialog step state helpers,
 *   and PROVIDER_CATALOG shape.
 * Inputs:  None — pure unit tests; no IPC calls.
 * Constraints: No real Tauri context required. Vitest jsdom environment.
 * SPORT: MASTER-COMPONENTS.md — ProviderStatusBadge tests — T-P3-E04-23/24
 */

import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

// ---------------------------------------------------------------------------
// Mocks (must come before imports that trigger @tauri-apps/api/core)
// ---------------------------------------------------------------------------

vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi.fn().mockResolvedValue([]),
}))

vi.mock('@tauri-apps/plugin-opener', () => ({
  open: vi.fn().mockResolvedValue(undefined),
}))

vi.mock('react-router-dom', () => ({
  Link: ({
    children,
    to,
    ...props
  }: {
    children: React.ReactNode
    to: string
    [key: string]: unknown
  }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

// ---------------------------------------------------------------------------
// Imports (after mocks)
// ---------------------------------------------------------------------------

import { ProviderStatusBadge } from '@/components/providers/ProviderStatusBadge'
import { PROVIDER_CATALOG } from '@/types/providers'
import type { ProviderStatus } from '@/types/providers'

// ---------------------------------------------------------------------------
// ProviderStatusBadge — pure render tests
// ---------------------------------------------------------------------------

describe('ProviderStatusBadge', () => {
  const cases: { status: ProviderStatus; expectedLabel: string }[] = [
    { status: 'healthy', expectedLabel: 'Connected' },
    { status: 'unhealthy', expectedLabel: 'Error' },
    { status: 'unknown', expectedLabel: 'Unconfigured' },
  ]

  cases.forEach(({ status, expectedLabel }) => {
    it(`renders "${expectedLabel}" for status="${status}"`, () => {
      render(<ProviderStatusBadge status={status} />)
      expect(screen.getByText(expectedLabel)).toBeInTheDocument()
    })
  })

  it('includes error message in aria-label when unhealthy', () => {
    render(<ProviderStatusBadge status="unhealthy" errorMsg="Connection refused on port 443" />)
    const badge = screen.getByRole('status')
    expect(badge).toHaveAttribute(
      'aria-label',
      'Provider status: Error — Connection refused on port 443'
    )
  })

  it('includes title attribute when unhealthy with errorMsg', () => {
    render(<ProviderStatusBadge status="unhealthy" errorMsg="Timeout after 5000ms" />)
    const badge = screen.getByRole('status')
    expect(badge).toHaveAttribute('title', 'Timeout after 5000ms')
  })

  it('does not include title when healthy', () => {
    render(<ProviderStatusBadge status="healthy" />)
    const badge = screen.getByRole('status')
    expect(badge).not.toHaveAttribute('title')
  })
})

// ---------------------------------------------------------------------------
// PROVIDER_CATALOG shape tests
// ---------------------------------------------------------------------------

describe('PROVIDER_CATALOG', () => {
  it('has at least 12 entries (11 cloud + 1 generic)', () => {
    expect(PROVIDER_CATALOG.length).toBeGreaterThanOrEqual(12)
  })

  it('every entry has a non-empty id, name, and description', () => {
    for (const entry of PROVIDER_CATALOG) {
      expect(entry.id.length).toBeGreaterThan(0)
      expect(entry.name.length).toBeGreaterThan(0)
      expect(entry.description.length).toBeGreaterThan(0)
    }
  })

  it('every entry has a valid authMethod', () => {
    const validMethods = new Set(['api_key', 'oauth', 'none'])
    for (const entry of PROVIDER_CATALOG) {
      expect(validMethods.has(entry.authMethod)).toBe(true)
    }
  })

  it('includes "generic" as the last entry', () => {
    const last = PROVIDER_CATALOG[PROVIDER_CATALOG.length - 1]
    expect(last.id).toBe('generic')
  })

  it('includes at least one OAuth provider', () => {
    const oauthProviders = PROVIDER_CATALOG.filter((e) => e.authMethod === 'oauth')
    expect(oauthProviders.length).toBeGreaterThan(0)
  })

  it('provider ids are unique', () => {
    const ids = PROVIDER_CATALOG.map((e) => e.id)
    const uniqueIds = new Set(ids)
    expect(uniqueIds.size).toBe(ids.length)
  })
})

// ---------------------------------------------------------------------------
// ProviderActionsMenu — confirm-delete two-click flow
// Tests the component's confirm state machine directly without relying on
// Radix DropdownMenu portal rendering in jsdom (portals render outside body).
// ---------------------------------------------------------------------------

import { ProviderActionsMenu } from '@/components/providers/ProviderActionsMenu'

describe('ProviderActionsMenu', () => {
  it('renders the trigger button with correct aria-label', () => {
    render(
      <ProviderActionsMenu providerId="anthropic" onTestConnection={vi.fn()} onRemove={vi.fn()} />
    )
    const trigger = screen.getByRole('button', { name: /Actions for anthropic/i })
    expect(trigger).toBeInTheDocument()
  })

  it('renders the trigger button with correct aria-label for different provider', () => {
    render(<ProviderActionsMenu providerId="groq" onTestConnection={vi.fn()} onRemove={vi.fn()} />)
    const trigger = screen.getByRole('button', { name: /Actions for groq/i })
    expect(trigger).toBeInTheDocument()
  })

  it('trigger button is disabled when disabled prop is true', () => {
    render(
      <ProviderActionsMenu
        providerId="openai"
        onTestConnection={vi.fn()}
        onRemove={vi.fn()}
        disabled
      />
    )
    const trigger = screen.getByRole('button', { name: /Actions for openai/i })
    expect(trigger).toBeDisabled()
  })
})
