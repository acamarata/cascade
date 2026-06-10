/**
 * Purpose: Phase 9 — Daemon install panel for the onboarding wizard.
 * Inputs:  WizardContext via useWizard() hook; Tauri command `install_daemon()` (stub from P2/E-03).
 * Outputs: Progress spinner while install runs; platform-specific label; success/error state.
 *          Calls updateState({ daemonInstalled: true }) on success.
 * Constraints:
 *   - Platform detection: window.__TAURI__.os.type() maps 'Darwin'→macOS, 'Linux'→Linux,
 *     'Windows_NT'→Windows.
 *   - Shows admin permission note: "macOS will request administrator permission once"
 *   - install_daemon() is a stub in P2/E-03; returns success for demo.
 *   - No next button auto-advance; user clicks "Continue" in WizardLayout footer.
 * SPORT: MASTER-COMPONENTS.md — DaemonInstallPhase
 */

import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { useWizard } from '@/features/onboarding/WizardContext'
import { AlertCircle, CheckCircle2, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'

// Tauri 2 platform detection via window.__TAURI_INTERNALS__ (os plugin not yet installed)
async function getPlatformType(): Promise<string> {
  try {
    // @ts-expect-error — Tauri 2 internal API; replace with @tauri-apps/plugin-os when added
    const platform = window.__TAURI_INTERNALS__?.metadata?.currentWindow?.platform ?? 'unknown'
    return platform
  } catch {
    return 'unknown'
  }
}

// Minimal inline alert components until @/components/ui/alert is scaffolded (E-01 follow-up)
function Alert({
  children,
  className,
  variant,
}: {
  children: React.ReactNode
  className?: string
  variant?: string
}) {
  return (
    <div
      role="alert"
      className={cn(
        'rounded-md border p-4',
        variant === 'destructive' && 'border-destructive text-destructive',
        className
      )}
    >
      {children}
    </div>
  )
}
function AlertTitle({ children }: { children: React.ReactNode }) {
  return <p className="font-semibold">{children}</p>
}
function AlertDescription({ children }: { children: React.ReactNode }) {
  return <p className="text-sm text-muted-foreground">{children}</p>
}

type PlatformType = 'macOS' | 'Linux' | 'Windows'

interface DaemonInstallPhaseProps {
  /** Optional className for styling */
  className?: string
}

export function DaemonInstallPhase({ className }: DaemonInstallPhaseProps) {
  const { updateState } = useWizard()
  const [platform, setPlatform] = useState<PlatformType | null>(null)
  const [isInstalling, setIsInstalling] = useState(false)
  const [isSuccess, setIsSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Detect platform on mount
  useEffect(() => {
    const detectPlatform = async () => {
      try {
        const osType = await getPlatformType()
        let detectedPlatform: PlatformType
        if (osType === 'Darwin') {
          detectedPlatform = 'macOS'
        } else if (osType === 'Linux') {
          detectedPlatform = 'Linux'
        } else if (osType === 'Windows_NT') {
          detectedPlatform = 'Windows'
        } else {
          detectedPlatform = 'Linux' // fallback
        }
        setPlatform(detectedPlatform)
      } catch (err) {
        console.error('Failed to detect platform:', err)
        setPlatform('Linux') // fallback
      }
    }

    detectPlatform()
  }, [])

  // Auto-start install on mount once platform is detected
  useEffect(() => {
    if (!platform || isInstalling || isSuccess) return

    const installDaemon = async () => {
      setIsInstalling(true)
      setError(null)

      try {
        // Invoke the Tauri command (stub in P2/E-03, returns success)
        await invoke('install_daemon')

        setIsInstalling(false)
        setIsSuccess(true)
        updateState({ daemonInstalled: true })
      } catch (err) {
        setError(
          err instanceof Error
            ? err.message
            : 'Daemon installation failed. Please check your system permissions.'
        )
        setIsInstalling(false)
      }
    }

    installDaemon()
  }, [platform, isInstalling, isSuccess, updateState])

  // Platform-specific label
  const getPlatformLabel = (): string => {
    switch (platform) {
      case 'macOS':
        return 'Installing LaunchAgent'
      case 'Linux':
        return 'Installing systemd unit'
      case 'Windows':
        return 'Installing Windows Service'
      default:
        return 'Installing daemon'
    }
  }

  return (
    <div
      className={cn('flex h-full flex-col items-center justify-center gap-6 px-8 py-12', className)}
    >
      {/* Progress indicator */}
      <div className="flex h-20 w-20 items-center justify-center rounded-full bg-primary/10">
        {isSuccess ? (
          <CheckCircle2 className="h-10 w-10 text-green-600" aria-label="Installation complete" />
        ) : isInstalling ? (
          <Loader2 className="h-10 w-10 animate-spin text-primary" aria-label="Installing" />
        ) : (
          <Loader2 className="h-10 w-10 animate-spin text-primary" />
        )}
      </div>

      {/* Title */}
      <h2 className="text-2xl font-semibold text-foreground">{getPlatformLabel()}</h2>

      {/* Status text */}
      {isSuccess && (
        <p className="text-center text-sm text-green-700">
          Daemon installed successfully. The cascade daemon is now running in the background.
        </p>
      )}

      {isInstalling && (
        <div className="space-y-2 text-center">
          <p className="text-sm text-muted-foreground">Starting the cascade daemon...</p>
          {platform === 'macOS' && (
            <p className="text-xs text-muted-foreground">
              macOS will request administrator permission once.
            </p>
          )}
        </div>
      )}

      {/* Error state */}
      {error && (
        <Alert variant="destructive" className="max-w-md">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Installation Failed</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}

      {/* Admin note for macOS */}
      {platform === 'macOS' && (
        <Alert className="max-w-md">
          <AlertCircle className="h-4 w-4" />
          <AlertTitle>Administrator Permission</AlertTitle>
          <AlertDescription>
            macOS will request administrator permission once to install the system service.
          </AlertDescription>
        </Alert>
      )}
    </div>
  )
}
