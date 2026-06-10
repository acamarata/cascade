/**
 * Purpose: Unit tests for AIGatedStep wrapper component.
 * Inputs:  Mocked useProviderConnected hook; renders children vs CTA card.
 * Outputs: Verified that when disconnected: CTA card renders (not children);
 *   when connected: children render (not CTA); skip/connect callbacks fire.
 * Constraints: Tests run in vitest + jsdom; no real Tauri context.
 * SPORT: MASTER-COMPONENTS.md — AIGatedStep tests (T-P3-E03-42)
 */

import '@testing-library/jest-dom'
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'

// Mock useProviderConnected so we can control anyConnected per test
vi.mock('../../hooks/useProviderConnected', () => ({
  useProviderConnected: vi.fn(),
}))

import { useProviderConnected } from '../../hooks/useProviderConnected'
import type { MockedFunction } from 'vitest'
import { AIGatedStep } from './AIGatedStep'

const mockUseProviderConnected = useProviderConnected as MockedFunction<typeof useProviderConnected>

// Helper: set hook to return connected state
function mockConnected(anyConnected: boolean, providers: string[] = []) {
  mockUseProviderConnected.mockReturnValue({
    anyConnected,
    connectedProviders: providers,
  })
}

describe('AIGatedStep', () => {
  it('renders CTA card (not children) when no provider is connected', () => {
    mockConnected(false)

    render(
      <AIGatedStep>
        <div data-testid="ai-content">AI-powered content</div>
      </AIGatedStep>
    )

    expect(screen.getByTestId('ai-gated-step-cta')).toBeInTheDocument()
    expect(screen.queryByTestId('ai-content')).not.toBeInTheDocument()
  })

  it('renders children (not CTA) when a provider is connected', () => {
    mockConnected(true, ['gemini-flash'])

    render(
      <AIGatedStep>
        <div data-testid="ai-content">AI-powered content</div>
      </AIGatedStep>
    )

    expect(screen.queryByTestId('ai-gated-step-cta')).not.toBeInTheDocument()
    expect(screen.getByTestId('ai-content')).toBeInTheDocument()
  })

  it('shows default ctaText when not overridden', () => {
    mockConnected(false)

    render(
      <AIGatedStep>
        <span>child</span>
      </AIGatedStep>
    )

    expect(screen.getByText('This step requires a connected AI provider.')).toBeInTheDocument()
  })

  it('shows custom ctaText when provided', () => {
    mockConnected(false)

    render(
      <AIGatedStep ctaText="Custom message for this step.">
        <span>child</span>
      </AIGatedStep>
    )

    expect(screen.getByText('Custom message for this step.')).toBeInTheDocument()
  })

  it('renders "Connect a provider" button when onConnectProvider is provided', () => {
    mockConnected(false)

    render(
      <AIGatedStep onConnectProvider={() => {}}>
        <span>child</span>
      </AIGatedStep>
    )

    expect(screen.getByTestId('ai-gated-connect-btn')).toBeInTheDocument()
  })

  it('does not render "Connect a provider" button when onConnectProvider is not provided', () => {
    mockConnected(false)

    render(
      <AIGatedStep>
        <span>child</span>
      </AIGatedStep>
    )

    expect(screen.queryByTestId('ai-gated-connect-btn')).not.toBeInTheDocument()
  })

  it('calls onConnectProvider when "Connect a provider" button is clicked', () => {
    mockConnected(false)
    const onConnect = vi.fn()

    render(
      <AIGatedStep onConnectProvider={onConnect}>
        <span>child</span>
      </AIGatedStep>
    )

    fireEvent.click(screen.getByTestId('ai-gated-connect-btn'))
    expect(onConnect).toHaveBeenCalledOnce()
  })

  it('renders "Skip for now" button when onSkip is provided', () => {
    mockConnected(false)

    render(
      <AIGatedStep onSkip={() => {}}>
        <span>child</span>
      </AIGatedStep>
    )

    expect(screen.getByTestId('ai-gated-skip-btn')).toBeInTheDocument()
  })

  it('calls onSkip when "Skip for now" button is clicked', () => {
    mockConnected(false)
    const onSkip = vi.fn()

    render(
      <AIGatedStep onSkip={onSkip}>
        <span>child</span>
      </AIGatedStep>
    )

    fireEvent.click(screen.getByTestId('ai-gated-skip-btn'))
    expect(onSkip).toHaveBeenCalledOnce()
  })

  it('does not render "Skip for now" button when onSkip is not provided', () => {
    mockConnected(false)

    render(
      <AIGatedStep>
        <span>child</span>
      </AIGatedStep>
    )

    expect(screen.queryByTestId('ai-gated-skip-btn')).not.toBeInTheDocument()
  })

  it('CTA card has accessible role and aria-live', () => {
    mockConnected(false)

    render(
      <AIGatedStep>
        <span>child</span>
      </AIGatedStep>
    )

    const cta = screen.getByTestId('ai-gated-step-cta')
    expect(cta).toHaveAttribute('role', 'status')
    expect(cta).toHaveAttribute('aria-live', 'polite')
  })
})
