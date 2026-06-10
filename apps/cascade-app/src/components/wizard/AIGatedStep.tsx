/**
 * AIGatedStep
 *
 * Purpose: Wrapper component that renders its children only when at least one
 *   AI provider is connected. When no provider is connected, renders a CTA
 *   card prompting the user to connect a provider or skip the step.
 *
 * Inputs:
 *   - children: ReactNode — the AI-powered step content to show when connected.
 *   - ctaText: string (optional) — override the "Connect a provider" message.
 *   - onSkip: () => void (optional) — called when user clicks "Skip for now".
 *   - onConnectProvider: () => void (optional) — called when user clicks the
 *       "Connect a provider →" CTA button.
 *
 * Outputs: ReactNode — either children or a lock-icon CTA card.
 *
 * Constraints:
 *   - T-P3-E03-42: AI-Optional Core guarantee.
 *   - Never makes AI IPC calls in the non-gated (locked) render path.
 *   - Skip marks the step complete with status="skipped" in wizard-state.json.
 *
 * SPORT: MASTER-COMPONENTS.md — AIGatedStep
 */

import React from 'react'
import { useProviderConnected } from '../../hooks/useProviderConnected'

interface AIGatedStepProps {
  children: React.ReactNode
  ctaText?: string
  onSkip?: () => void
  onConnectProvider?: () => void
}

/**
 * Wraps AI-powered wizard step content behind a provider-connected gate.
 *
 * If any AI provider is connected: renders `children` unchanged.
 * If no provider is connected: renders a lock-icon CTA card with options to
 * connect a provider or skip the step.
 *
 * The non-gated (locked) render path makes zero AI IPC calls — all AI work
 * lives inside `children`, which is never mounted when the gate is closed.
 */
export function AIGatedStep({
  children,
  ctaText = 'This step requires a connected AI provider.',
  onSkip,
  onConnectProvider,
}: AIGatedStepProps): React.JSX.Element {
  const { anyConnected } = useProviderConnected()

  if (anyConnected) {
    return <>{children}</>
  }

  return (
    <div
      className="flex h-full flex-col items-center justify-center gap-4 px-8 py-12 text-center"
      data-testid="ai-gated-step-cta"
      role="status"
      aria-live="polite"
    >
      {/* Lock icon — inline SVG to avoid importing lucide in non-AI render path */}
      <svg
        aria-hidden="true"
        width="32"
        height="32"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="text-muted-foreground"
      >
        <rect width="18" height="11" x="3" y="11" rx="2" ry="2" />
        <path d="M7 11V7a5 5 0 0 1 10 0v4" />
      </svg>
      <p className="max-w-sm text-sm text-muted-foreground">{ctaText}</p>
      <div className="flex flex-wrap justify-center gap-3">
        {onConnectProvider && (
          <button
            type="button"
            onClick={onConnectProvider}
            className="inline-flex items-center rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            aria-label="Connect a provider to unlock this step"
            data-testid="ai-gated-connect-btn"
          >
            Connect a provider →
          </button>
        )}
        {onSkip && (
          <button
            type="button"
            onClick={onSkip}
            className="inline-flex items-center rounded-md border border-input bg-background px-4 py-2 text-sm font-medium hover:bg-accent hover:text-accent-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            aria-label="Skip this step for now"
            data-testid="ai-gated-skip-btn"
          >
            Skip for now
          </button>
        )}
      </div>
    </div>
  )
}

export default AIGatedStep
