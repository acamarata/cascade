/**
 * Purpose: Tutorial overlay with 3-step tooltip tour for the Done phase.
 * Inputs:  isOpen prop to control visibility; onClose callback; step navigation via keyboard/buttons.
 * Outputs: Positioned tooltip overlays highlighting key nav items: cascade editor, RAG explorer, inbox.
 *          Keyboard navigable: Esc closes, arrow keys or button clicks advance.
 * Constraints:
 *   - Uses shadcn/ui Popover positioned relative to nav items.
 *   - CSS pointer-events: none on overlay; pointer-events: auto on active step highlight.
 *   - Three steps point to distinct UI elements (editor route, rag route, inbox route).
 *   - Closes on Esc or explicit close button.
 * SPORT: MASTER-COMPONENTS.md — TutorialOverlay
 */

import { useEffect, useState } from 'react'
import { ChevronLeft, ChevronRight, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'

interface TutorialOverlayProps {
  /** Whether the tutorial is open */
  isOpen: boolean
  /** Callback when tutorial is closed */
  onClose: () => void
  /** Optional className for the root container */
  className?: string
}

interface TutorialStep {
  id: string
  title: string
  description: string
  targetSelector: string // CSS selector for the element to highlight
  position: 'top' | 'bottom' | 'left' | 'right'
}

const TUTORIAL_STEPS: TutorialStep[] = [
  {
    id: 'cascade-editor',
    title: 'Cascade Editor',
    description:
      'Write and test cascade configurations. Define your multi-agent workflows and manage instruction hierarchies.',
    targetSelector: '[data-tutorial="cascade-editor"]', // Nav item for editor route
    position: 'bottom',
  },
  {
    id: 'rag-explorer',
    title: 'RAG Explorer',
    description:
      'Search and retrieve documents from your knowledge base. Powered by BGE-M3 embeddings and FTS5 full-text search.',
    targetSelector: '[data-tutorial="rag-explorer"]', // Nav item for RAG route
    position: 'bottom',
  },
  {
    id: 'inbox-viewer',
    title: 'Inbox Viewer',
    description:
      'Manage incoming tasks, notifications, and messages. Route items to the right agents and track completion.',
    targetSelector: '[data-tutorial="inbox-viewer"]', // Nav item for inbox route
    position: 'bottom',
  },
]

/**
 * Tutorial overlay component with multi-step tooltip tour.
 *
 * Highlights key navigation items and provides guided intro to main app features.
 * Keyboard accessible: Esc to close, arrow keys to navigate steps.
 *
 * @example
 * ```tsx
 * <TutorialOverlay isOpen={showTour} onClose={() => setShowTour(false)} />
 * ```
 */
export function TutorialOverlay({ isOpen, onClose, className }: TutorialOverlayProps) {
  const [currentStep, setCurrentStep] = useState(0)
  const step = TUTORIAL_STEPS[currentStep]

  // Handle keyboard navigation
  useEffect(() => {
    if (!isOpen) return

    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
      } else if (e.key === 'ArrowRight' && currentStep < TUTORIAL_STEPS.length - 1) {
        setCurrentStep(currentStep + 1)
      } else if (e.key === 'ArrowLeft' && currentStep > 0) {
        setCurrentStep(currentStep - 1)
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [isOpen, currentStep, onClose])

  if (!isOpen) return null

  return (
    <div className={cn('pointer-events-none fixed inset-0 z-50', className)}>
      {/* Semi-transparent backdrop */}
      <div
        role="button"
        tabIndex={0}
        className="absolute inset-0 bg-black/30 transition-opacity duration-300"
        onClick={onClose}
        onKeyDown={(e) => e.key === 'Escape' && onClose()}
        aria-label="Close tutorial"
      />

      {/* Positioned tooltip popover */}
      <Popover open={isOpen} onOpenChange={(open) => !open && onClose()}>
        <PopoverTrigger asChild>
          <div className="pointer-events-none" />
        </PopoverTrigger>

        <PopoverContent
          className="pointer-events-auto w-80 border-2 border-primary bg-background shadow-2xl"
          side={step.position}
          align="center"
          sideOffset={16}
        >
          {/* Header with close button */}
          <div className="mb-3 flex items-center justify-between">
            <h3 className="text-lg font-semibold text-foreground">{step.title}</h3>
            <button
              onClick={onClose}
              className="text-muted-foreground hover:text-foreground"
              aria-label="Close tutorial"
            >
              <X className="h-4 w-4" />
            </button>
          </div>

          {/* Description */}
          <p className="mb-4 text-sm text-muted-foreground">{step.description}</p>

          {/* Step indicator and navigation */}
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">
              Step {currentStep + 1} of {TUTORIAL_STEPS.length}
            </span>

            <div className="flex gap-2">
              {/* Previous button */}
              <Button
                onClick={() => setCurrentStep(Math.max(0, currentStep - 1))}
                disabled={currentStep === 0}
                size="sm"
                variant="outline"
                className="pointer-events-auto"
                aria-label="Previous step"
              >
                <ChevronLeft className="h-4 w-4" />
              </Button>

              {/* Next button */}
              <Button
                onClick={() => setCurrentStep(Math.min(TUTORIAL_STEPS.length - 1, currentStep + 1))}
                disabled={currentStep === TUTORIAL_STEPS.length - 1}
                size="sm"
                className="pointer-events-auto"
                aria-label="Next step"
              >
                <ChevronRight className="h-4 w-4" />
              </Button>
            </div>
          </div>

          {/* Step indicators (dots) */}
          <div className="mt-4 flex justify-center gap-1">
            {TUTORIAL_STEPS.map((_, idx) => (
              <button
                key={idx}
                onClick={() => setCurrentStep(idx)}
                className={cn(
                  'h-2 w-2 rounded-full transition-colors',
                  idx === currentStep ? 'bg-primary' : 'bg-muted'
                )}
                aria-label={`Go to step ${idx + 1}`}
              />
            ))}
          </div>
        </PopoverContent>
      </Popover>
    </div>
  )
}
