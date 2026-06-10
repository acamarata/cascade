/**
 * Purpose: Phase 10 — Done screen with health dashboard and tutorial overlay.
 * Inputs:  WizardContext via useWizard() hook; Tauri command `wizard_mark_complete()`.
 * Outputs: Health dashboard widget showing daemon status and cascade file count;
 *          Tutorial overlay triggered by "Take the tour" button;
 *          "Open Cascade" button writes wizard-complete marker and navigates to main app.
 * Constraints:
 *   - Health dashboard shows: daemon status (running/stopped placeholder), cascade file count,
 *     tools connected (from wizard state).
 *   - wizard_mark_complete() writes ~/.cascade/.wizard-complete before navigation.
 *   - Tutorial overlay has 3 steps pointing at editor, RAG explorer, and inbox routes.
 *   - Navigation clears wizard-complete guard in the app.
 * SPORT: MASTER-COMPONENTS.md — DonePhase
 */

import { useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { useWizard } from '@/features/onboarding/WizardContext'
import { WizardStep } from '@/features/onboarding/types'
import { useNavigate } from 'react-router-dom'
import { CheckCircle2, AlertCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { TutorialOverlay } from '@/features/onboarding/phases/TutorialOverlay'

// TODO: Replace with @/components/ui/card and @/components/ui/badge when added in E-01 follow-up
function Card({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={cn('rounded-lg border bg-card p-4 shadow-sm', className)}>{children}</div>
}
function CardHeader({ children }: { children: React.ReactNode }) {
  return <div className="mb-2">{children}</div>
}
function CardTitle({ children, className }: { children: React.ReactNode; className?: string }) {
  return <h3 className={cn('font-semibold text-card-foreground', className)}>{children}</h3>
}
function CardDescription({ children }: { children: React.ReactNode }) {
  return <p className="text-sm text-muted-foreground">{children}</p>
}
function CardContent({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={className}>{children}</div>
}
function Badge({
  children,
  className,
  variant,
}: {
  children: React.ReactNode
  className?: string
  variant?: string
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-semibold',
        variant === 'secondary' && 'bg-secondary text-secondary-foreground',
        className
      )}
    >
      {children}
    </span>
  )
}

interface DonePhaseProps {
  /** Optional className for styling */
  className?: string
}

export function DonePhase({ className }: DonePhaseProps) {
  const { state, markComplete } = useWizard()
  const navigate = useNavigate()
  const [showTutorial, setShowTutorial] = useState(false)
  const [isNavigating, setIsNavigating] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Placeholder daemon status (real status from P3/E-04 health endpoint)
  // Placeholder: real daemon status from P3/E-04 health endpoint
  const daemonStatus: 'running' | 'stopped' = 'running'

  // Calculate cascade file count from wizard state (placeholder: use completed steps as proxy)
  const cascadeFileCount = state.completedSteps.size

  // Count tools connected (hardcoded to 1 from demo; real count from state in P3/E-04)
  const toolsConnected = state.providerConnected ? 1 : 0

  const handleOpenCascade = async () => {
    setIsNavigating(true)
    setError(null)

    try {
      // Mark wizard as complete — writes ~/.cascade/.wizard-complete marker
      await invoke('wizard_mark_complete')

      // Navigate to the main app (/) and clear wizard state
      markComplete(WizardStep.Done) // Mark Done phase as reached
      navigate('/', { replace: true })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to complete wizard. Please try again.')
      setIsNavigating(false)
    }
  }

  return (
    <div
      className={cn('flex h-full flex-col items-center justify-center gap-6 px-8 py-12', className)}
    >
      {/* Success icon */}
      <div className="flex h-24 w-24 items-center justify-center rounded-full bg-green-100">
        <CheckCircle2 className="h-12 w-12 text-green-600" aria-label="Wizard complete" />
      </div>

      {/* Title and subtitle */}
      <div className="text-center space-y-2">
        <h2 className="text-3xl font-bold text-foreground">{"You're All Set!"}</h2>
        <p className="text-sm text-muted-foreground">
          Cascade is ready to supercharge your AI workflows.
        </p>
      </div>

      {/* Health dashboard widget */}
      <Card className="w-full max-w-md border-green-200 bg-green-50">
        <CardHeader>
          <CardTitle className="text-lg">System Status</CardTitle>
          <CardDescription>Your cascade environment is healthy</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          {/* Daemon status */}
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-foreground">Daemon Status</span>
            <Badge
              variant={daemonStatus === 'running' ? 'default' : 'secondary'}
              className={daemonStatus === 'running' ? 'bg-green-600' : ''}
            >
              {daemonStatus === 'running' ? '● Running' : '○ Stopped'}
            </Badge>
          </div>

          {/* Cascade file count */}
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-foreground">Cascade Files</span>
            <span className="text-sm font-semibold text-foreground">{cascadeFileCount}</span>
          </div>

          {/* Tools connected */}
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-foreground">Tools Connected</span>
            <span className="text-sm font-semibold text-foreground">{toolsConnected}</span>
          </div>
        </CardContent>
      </Card>

      {/* Error alert */}
      {error && (
        <div className="flex gap-2 rounded-md border border-red-200 bg-red-50 p-3 text-sm text-red-700">
          <AlertCircle className="h-4 w-4 flex-shrink-0 mt-0.5" />
          <span>{error}</span>
        </div>
      )}

      {/* Action buttons */}
      <div className="flex w-full max-w-md flex-col gap-3">
        {/* Tutorial button */}
        <Button
          onClick={() => setShowTutorial(true)}
          variant="outline"
          size="lg"
          className="pointer-events-auto"
          disabled={isNavigating}
        >
          Take the Tour
        </Button>

        {/* Open Cascade button */}
        <Button
          onClick={handleOpenCascade}
          size="lg"
          className="pointer-events-auto"
          disabled={isNavigating}
        >
          {isNavigating ? 'Opening...' : 'Open Cascade'}
        </Button>
      </div>

      {/* Tutorial overlay */}
      <TutorialOverlay isOpen={showTutorial} onClose={() => setShowTutorial(false)} />
    </div>
  )
}
