/**
 * Purpose: Phase 1 Welcome content panel for the onboarding wizard.
 * Inputs: None — uses useWizard hook from context.
 * Outputs: 30-second animated explainer of the 6-tier cascade concept.
 * Constraints: CSS animations only (no external animation library).
 *   Respects prefers-reduced-motion (static final frame immediately if set).
 *   No layout shift at 800×600 and larger window sizes.
 *   Skip button calls goNext() immediately (skips the animation).
 * SPORT: MASTER-COMPONENTS.md — WelcomePhase
 */

import { useEffect, useState } from 'react'
import { useWizard } from '@/features/onboarding/WizardContext'

const TIER_LABELS = ['GCI', 'PCI', 'APC', 'PPC', 'PRC', 'PAC'] as const
const ANIMATION_DURATION = 30000 // 30 seconds in milliseconds
const TIER_STAGGER_DELAY = 5000 // Each tier appears every 5 seconds
const TITLE_FADE_IN_DURATION = 1500 // 1.5s for title fade in
const TIER_FADE_IN_DURATION = 1000 // 1s per tier

interface WelcomePhaseProps {
  /** Optional className for styling */
  className?: string
}

export function WelcomePhase({ className }: WelcomePhaseProps) {
  const { goNext } = useWizard()
  const [prefersReducedMotion, setPrefersReducedMotion] = useState(false)

  // Check for prefers-reduced-motion media query at mount
  useEffect(() => {
    const mediaQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
    setPrefersReducedMotion(mediaQuery.matches)

    const handleChange = (e: MediaQueryListEvent) => {
      setPrefersReducedMotion(e.matches)
    }

    mediaQuery.addEventListener('change', handleChange)
    return () => mediaQuery.removeEventListener('change', handleChange)
  }, [])

  // Auto-complete animation after duration (or immediately if prefers-reduced-motion)
  useEffect(() => {
    let timer: NodeJS.Timeout

    if (!prefersReducedMotion) {
      timer = setTimeout(() => {
        // Animation completes; user sees the final state
      }, ANIMATION_DURATION)
    }

    return () => {
      if (timer) clearTimeout(timer)
    }
  }, [prefersReducedMotion])

  const handleSkipAnimation = () => {
    goNext()
  }

  return (
    <div
      className={`relative flex h-full flex-col items-center justify-center gap-8 px-8 py-12 ${className || ''}`}
    >
      <style>{`
        @keyframes fadeInTitle {
          from {
            opacity: 0;
          }
          to {
            opacity: 1;
          }
        }

        @keyframes fadeInTier {
          0% {
            opacity: 0;
            transform: translateY(-10px);
          }
          100% {
            opacity: 1;
            transform: translateY(0);
          }
        }

        @keyframes drawLine {
          from {
            stroke-dashoffset: 100;
          }
          to {
            stroke-dashoffset: 0;
          }
        }

        @keyframes fadeInFinal {
          from {
            opacity: 0;
          }
          to {
            opacity: 1;
          }
        }

        .welcome-title {
          animation: fadeInTitle ${prefersReducedMotion ? '0ms' : `${TITLE_FADE_IN_DURATION}ms`} ease-in-out forwards;
        }

        .welcome-tier-0 {
          animation: fadeInTier ${prefersReducedMotion ? '0ms' : `${TIER_FADE_IN_DURATION}ms`} ease-in-out forwards;
          animation-delay: ${prefersReducedMotion ? '0ms' : `${0 * TIER_STAGGER_DELAY + 2000}ms`};
        }

        .welcome-tier-1 {
          animation: fadeInTier ${prefersReducedMotion ? '0ms' : `${TIER_FADE_IN_DURATION}ms`} ease-in-out forwards;
          animation-delay: ${prefersReducedMotion ? '0ms' : `${1 * TIER_STAGGER_DELAY + 2000}ms`};
        }

        .welcome-tier-2 {
          animation: fadeInTier ${prefersReducedMotion ? '0ms' : `${TIER_FADE_IN_DURATION}ms`} ease-in-out forwards;
          animation-delay: ${prefersReducedMotion ? '0ms' : `${2 * TIER_STAGGER_DELAY + 2000}ms`};
        }

        .welcome-tier-3 {
          animation: fadeInTier ${prefersReducedMotion ? '0ms' : `${TIER_FADE_IN_DURATION}ms`} ease-in-out forwards;
          animation-delay: ${prefersReducedMotion ? '0ms' : `${3 * TIER_STAGGER_DELAY + 2000}ms`};
        }

        .welcome-tier-4 {
          animation: fadeInTier ${prefersReducedMotion ? '0ms' : `${TIER_FADE_IN_DURATION}ms`} ease-in-out forwards;
          animation-delay: ${prefersReducedMotion ? '0ms' : `${4 * TIER_STAGGER_DELAY + 2000}ms`};
        }

        .welcome-tier-5 {
          animation: fadeInTier ${prefersReducedMotion ? '0ms' : `${TIER_FADE_IN_DURATION}ms`} ease-in-out forwards;
          animation-delay: ${prefersReducedMotion ? '0ms' : `${5 * TIER_STAGGER_DELAY + 2000}ms`};
        }

        .welcome-line {
          stroke-dasharray: 100;
          animation: drawLine ${prefersReducedMotion ? '0ms' : '4000ms'} ease-in-out forwards;
          animation-delay: ${prefersReducedMotion ? '0ms' : '2500ms'};
        }

        .welcome-final-text {
          animation: fadeInFinal ${prefersReducedMotion ? '0ms' : `${TIER_FADE_IN_DURATION}ms`} ease-in-out forwards;
          animation-delay: ${prefersReducedMotion ? '0ms' : `${6 * TIER_STAGGER_DELAY + 2000}ms`};
        }

        @media (prefers-reduced-motion: reduce) {
          .welcome-title,
          .welcome-tier-0,
          .welcome-tier-1,
          .welcome-tier-2,
          .welcome-tier-3,
          .welcome-tier-4,
          .welcome-tier-5,
          .welcome-line,
          .welcome-final-text {
            animation: none !important;
            opacity: 1 !important;
            transform: none !important;
          }
        }
      `}</style>

      {/* Title */}
      <h1 className="welcome-title text-center text-4xl font-bold leading-tight text-foreground">
        Cascade: One place for all your AI tools
      </h1>

      {/* Cascade Tiers Visualization */}
      <div className="relative w-full max-w-sm">
        {/* SVG for connecting line */}
        <svg
          className="absolute left-1/2 top-0 h-full w-8 -translate-x-1/2"
          viewBox="0 0 32 400"
          preserveAspectRatio="none"
        >
          <line
            className="welcome-line"
            x1="16"
            y1="0"
            x2="16"
            y2="400"
            stroke="currentColor"
            strokeWidth="2"
            vectorEffect="non-scaling-stroke"
          />
        </svg>

        {/* Tier labels */}
        <div className="flex flex-col gap-12 pt-4">
          {TIER_LABELS.map((label, index) => (
            <div
              key={label}
              className={`welcome-tier-${index} relative flex items-center justify-center rounded-lg border-2 border-primary bg-primary/10 px-6 py-4 text-center text-sm font-semibold text-primary opacity-0`}
            >
              {label}
            </div>
          ))}
        </div>
      </div>

      {/* Final message */}
      <p className="welcome-final-text max-w-md text-center text-lg text-muted-foreground opacity-0">
        Every tool reads the same rules. Written once, understood by all.
      </p>

      {/* Skip button */}
      <button
        onClick={handleSkipAnimation}
        className="focus-visible:ring-ring absolute bottom-6 right-6 rounded-md border border-muted-foreground/30 bg-background px-4 py-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-2"
        aria-label="Skip animation and continue to next step"
      >
        Skip animation
      </button>
    </div>
  )
}
