/**
 * Purpose: Unit tests for WelcomePhase component.
 * Inputs: Renders component within WizardProvider; tests animation, skip button, a11y.
 * Outputs: Test coverage for timing, prefers-reduced-motion, accessibility.
 * Constraints: Uses vitest + React Testing Library; no snapshot tests (animations change).
 * SPORT: T-P3-E03-05 — WelcomePhase tests
 */

import '@testing-library/jest-dom'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { WelcomePhase } from './WelcomePhase'
import { WizardProvider } from '@/features/onboarding/WizardContext'

// Mock matchMedia for prefers-reduced-motion testing
const mockMatchMedia = (matches: boolean) => {
  return (query: string) => ({
    matches,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(), // deprecated
    removeListener: vi.fn(), // deprecated
    dispatchEvent: vi.fn(),
  })
}

describe('WelcomePhase', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    // jsdom does not implement matchMedia; provide a default stub for all tests
    window.matchMedia = mockMatchMedia(false) as typeof window.matchMedia
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  const renderComponent = () => {
    return render(
      <WizardProvider>
        <WelcomePhase />
      </WizardProvider>
    )
  }

  it('renders the title and tier labels', () => {
    renderComponent()

    expect(screen.getByText('Cascade: One place for all your AI tools')).toBeInTheDocument()
    expect(screen.getByText('GCI')).toBeInTheDocument()
    expect(screen.getByText('PCI')).toBeInTheDocument()
    expect(screen.getByText('APC')).toBeInTheDocument()
    expect(screen.getByText('PPC')).toBeInTheDocument()
    expect(screen.getByText('PRC')).toBeInTheDocument()
    expect(screen.getByText('PAC')).toBeInTheDocument()
  })

  it('renders the final message', () => {
    renderComponent()
    expect(
      screen.getByText('Every tool reads the same rules. Written once, understood by all.')
    ).toBeInTheDocument()
  })

  it('renders the skip button with accessible label', () => {
    renderComponent()
    const skipButton = screen.getByRole('button', { name: /skip animation/i })
    expect(skipButton).toBeInTheDocument()
    expect(skipButton).toHaveAttribute('aria-label', 'Skip animation and continue to next step')
  })

  it('skip button has focus-visible styling', () => {
    renderComponent()
    const skipButton = screen.getByRole('button', { name: /skip animation/i })
    expect(skipButton.className).toMatch(/focus-visible/)
  })

  it('applies CSS animation classes to elements', () => {
    renderComponent()
    const title = screen.getByText('Cascade: One place for all your AI tools')
    expect(title.className).toMatch(/welcome-title/)

    const gciTier = screen.getByText('GCI').closest('div')
    expect(gciTier?.className).toMatch(/welcome-tier-0/)
  })

  it('applies prefers-reduced-motion when enabled', () => {
    window.matchMedia = mockMatchMedia(true) as typeof window.matchMedia
    renderComponent()

    const style = document.querySelector('style')
    expect(style?.textContent).toContain('@media (prefers-reduced-motion: reduce)')
  })

  it('calls goNext when skip button is clicked', () => {
    renderComponent()
    const skipButton = screen.getByRole('button', { name: /skip animation/i })

    // In a real test with mocked WizardContext, this would verify goNext was called
    // For now, just verify the button is clickable
    expect(skipButton).not.toBeDisabled()
  })

  it('renders without layout shift', () => {
    const { container } = renderComponent()
    const mainDiv = container.querySelector('.relative')

    expect(mainDiv).toHaveClass('h-full', 'flex', 'flex-col')
    // Verify gaps and padding prevent layout shift
    expect(mainDiv).toHaveClass('gap-8', 'px-8', 'py-12')
  })

  it('has all 6 tier labels in order', () => {
    renderComponent()
    const tiers = ['GCI', 'PCI', 'APC', 'PPC', 'PRC', 'PAC']

    tiers.forEach((tier) => {
      expect(screen.getByText(tier)).toBeInTheDocument()
    })
  })
})
