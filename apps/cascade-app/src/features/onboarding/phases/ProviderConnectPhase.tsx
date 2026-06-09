/**
 * Purpose: Wizard Phase 2 — Connect AI provider before the merge engine is available.
 * Inputs:  WizardContext via useWizard() hook; Tauri commands detect_gemini_pool and
 *          provider_connect (stubs until E-04 implements real OAuth).
 * Outputs: Provider grid (4 cards) + offline toggle + guarded Next button.
 *          Calls updateState({ providerConnected: true }) when ≥1 provider connects.
 * Constraints:
 *   - detect_gemini_pool is called once on mount with a 500ms timeout handled in Rust.
 *   - provider_connect is a stub returning a token string; E-04 fills real OAuth.
 *   - Next button guard: disabled until isAnyConnected().
 *   - No navigation logic — this component signals readiness via the wizard context.
 * SPORT: MASTER-COMPONENTS.md — ProviderConnectPhase
 */

import * as React from 'react'
import { invoke } from '@tauri-apps/api/core'
import {
  ProviderCard,
  type ProviderId,
  type ProviderInfo,
  type ConnectionStatus,
} from './ProviderCard'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// Static provider definitions
// ---------------------------------------------------------------------------

const PROVIDERS: ProviderInfo[] = [
  {
    id: 'gemini',
    name: 'Gemini',
    tagline: 'Recommended — generous free tier',
    iconColor: 'bg-blue-500',
  },
  {
    id: 'anthropic',
    name: 'Anthropic',
    tagline: 'Claude — state-of-the-art reasoning',
    iconColor: 'bg-orange-500',
  },
  {
    id: 'openai',
    name: 'OpenAI',
    tagline: 'GPT-4 and o-series models',
    iconColor: 'bg-emerald-600',
  },
  {
    id: 'opencode',
    name: 'OpenCode Go',
    tagline: 'Open-source, self-hosted option',
    iconColor: 'bg-purple-600',
  },
]

// ---------------------------------------------------------------------------
// Local model variants
// ---------------------------------------------------------------------------

type LocalModelVariant = 'gemma-2-2b' | 'llama-3.2-3b'

interface LocalModelInfo {
  variant: LocalModelVariant
  label: string
  description: string
}

const LOCAL_MODELS: LocalModelInfo[] = [
  {
    variant: 'gemma-2-2b',
    label: 'Gemma 2 2B',
    description: 'Google — lightweight, fast inference',
  },
  {
    variant: 'llama-3.2-3b',
    label: 'Llama 3.2 3B',
    description: 'Meta — general purpose offline model',
  },
]

// ---------------------------------------------------------------------------
// Wizard context interface (minimal — T-P3-E03-02 defines the full shape)
// ---------------------------------------------------------------------------

/**
 * Minimal wizard context shape this phase uses.
 * When WizardContext is implemented (T-P3-E03-02), import useWizard directly.
 * Until then, ProviderConnectPhase accepts these as props for standalone usage.
 */
export interface ProviderConnectPhaseProps {
  /** Navigate to the next wizard step. */
  goNext: () => void
  /** Navigate to the previous wizard step. */
  goBack: () => void
  /**
   * Update shared wizard state with partial values.
   * Must be called with { providerConnected: true } once connection is established.
   */
  updateState: (partial: { providerConnected: boolean }) => void
}

// ---------------------------------------------------------------------------
// ProviderConnectPhase
// ---------------------------------------------------------------------------

/**
 * Purpose: Phase 2 of the onboarding wizard — connect at least one AI provider.
 *
 * Inputs:
 *   - goNext / goBack / updateState from WizardContext (via props or useWizard hook)
 *
 * Outputs:
 *   - 4 provider cards with live connection status
 *   - "Prefer offline" toggle revealing local model download options
 *   - Guarded Next button (disabled until ≥1 provider connected OR local model downloaded)
 *
 * Constraints:
 *   - Gemini Pool auto-detection fires once on mount (detect_gemini_pool Tauri command).
 *   - Tauri commands provider_connect and download_local_model are stubs (E-04 fills real impl).
 *   - Connection timeout (>500ms) in Rust returns false from detect_gemini_pool — graceful fallback.
 *
 * SPORT: MASTER-COMPONENTS.md — ProviderConnectPhase
 */
export function ProviderConnectPhase({
  goNext,
  goBack,
  updateState,
}: ProviderConnectPhaseProps) {
  // -------------------------------------------------------------------------
  // State
  // -------------------------------------------------------------------------

  /** Per-provider connection status map. */
  const [statuses, setStatuses] = React.useState<Record<ProviderId, ConnectionStatus>>(
    () => ({
      gemini: 'disconnected',
      anthropic: 'disconnected',
      openai: 'disconnected',
      opencode: 'disconnected',
    })
  )

  /** Per-provider inline error messages. */
  const [errors, setErrors] = React.useState<Record<ProviderId, string | undefined>>(
    () => ({
      gemini: undefined,
      anthropic: undefined,
      openai: undefined,
      opencode: undefined,
    })
  )

  /** Whether the "Prefer offline" toggle is active. */
  const [offlineMode, setOfflineMode] = React.useState(false)

  /** Which local model variant is downloading (null = none). */
  const [downloadingVariant, setDownloadingVariant] =
    React.useState<LocalModelVariant | null>(null)

  /** Download progress 0-100. */
  const [downloadProgress, setDownloadProgress] = React.useState(0)

  /** Whether any local model has been downloaded successfully. */
  const [localModelReady, setLocalModelReady] = React.useState(false)

  // -------------------------------------------------------------------------
  // Gemini Pool auto-detection on mount
  // -------------------------------------------------------------------------

  React.useEffect(() => {
    let cancelled = false

    async function detectPool() {
      try {
        const detected = await invoke<boolean>('detect_gemini_pool')
        if (cancelled) return

        if (detected) {
          setStatuses((prev) => ({ ...prev, gemini: 'pool_detected' }))
          updateState({ providerConnected: true })
        }
      } catch {
        // Connection timeout or invocation error — graceful fallback (no error shown)
        // The user can still connect manually via the Connect button.
        if (!cancelled) {
          setStatuses((prev) =>
            prev.gemini === 'pool_detected' ? prev : { ...prev, gemini: 'disconnected' }
          )
        }
      }
    }

    void detectPool()
    return () => {
      cancelled = true
    }
  }, [updateState])

  // -------------------------------------------------------------------------
  // Derived state
  // -------------------------------------------------------------------------

  const isAnyConnected =
    localModelReady ||
    Object.values(statuses).some(
      (s) => s === 'connected' || s === 'pool_detected'
    )

  // -------------------------------------------------------------------------
  // Handlers
  // -------------------------------------------------------------------------

  function setProviderStatus(id: ProviderId, status: ConnectionStatus) {
    setStatuses((prev) => ({ ...prev, [id]: status }))
  }

  function setProviderError(id: ProviderId, msg: string | undefined) {
    setErrors((prev) => ({ ...prev, [id]: msg }))
  }

  async function handleConnect(id: ProviderId) {
    setProviderStatus(id, 'connecting')
    setProviderError(id, undefined)

    try {
      // Stub: E-04 implements real OAuth; this wires the UI and handles the token.
      await invoke<string>('provider_connect', { provider: id })
      setProviderStatus(id, 'connected')
      updateState({ providerConnected: true })
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setProviderStatus(id, 'error')

      // Map known error shapes to user-readable messages.
      if (msg.includes('timeout') || msg.includes('Timeout')) {
        setProviderError(id, 'Connection timed out. Check your network and try again.')
      } else if (msg.includes('denied') || msg.includes('Denied')) {
        setProviderError(id, 'OAuth was denied. Please authorize access and try again.')
      } else if (msg.includes('network') || msg.includes('Network')) {
        setProviderError(id, 'Network error. Ensure you are online and try again.')
      } else {
        setProviderError(id, `Connection failed: ${msg}`)
      }
    }
  }

  async function handleDownloadModel(variant: LocalModelVariant) {
    if (downloadingVariant !== null) return
    setDownloadingVariant(variant)
    setDownloadProgress(0)

    try {
      // Stub: E-04 implements actual binary transfer; this drives the progress UI.
      await invoke<void>('download_local_model', { variant })
      setDownloadProgress(100)
      setLocalModelReady(true)
      updateState({ providerConnected: true })
    } catch (err) {
      // Download failed — reset so user can retry.
      console.error('download_local_model failed:', err)
    } finally {
      setDownloadingVariant(null)
    }
  }

  // -------------------------------------------------------------------------
  // Render
  // -------------------------------------------------------------------------

  return (
    <div className="flex h-full flex-col gap-6">
      {/* Heading */}
      <div>
        <h2 className="text-xl font-semibold tracking-tight">Connect an AI provider</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Cascade needs at least one AI provider to power the merge engine in the next step.
          Choose any provider below — or download a local model if you prefer to work offline.
        </p>
      </div>

      {/* Provider grid */}
      <div
        role="list"
        aria-label="AI provider options"
        className="grid gap-3 sm:grid-cols-2"
      >
        {PROVIDERS.map((provider) => (
          <div key={provider.id} role="listitem">
            <ProviderCard
              provider={provider}
              status={statuses[provider.id]}
              errorMessage={errors[provider.id]}
              onConnect={handleConnect}
            />
          </div>
        ))}
      </div>

      {/* Offline toggle */}
      <div className="rounded-lg border bg-muted/40 p-4">
        <label
          htmlFor="offline-toggle"
          className="flex cursor-pointer items-center gap-3"
        >
          {/* Custom toggle */}
          <span
            role="switch"
            aria-checked={offlineMode}
            aria-label="Prefer offline — download a local model"
            id="offline-toggle"
            tabIndex={0}
            onKeyDown={(e) => {
              if (e.key === ' ' || e.key === 'Enter') {
                e.preventDefault()
                setOfflineMode((v) => !v)
              }
            }}
            onClick={() => setOfflineMode((v) => !v)}
            className={cn(
              'relative inline-flex h-5 w-9 shrink-0 cursor-pointer rounded-full border-2 border-transparent',
              'transition-colors duration-200 ease-in-out',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              offlineMode ? 'bg-primary' : 'bg-input'
            )}
          >
            <span
              aria-hidden="true"
              className={cn(
                'pointer-events-none inline-block h-4 w-4 rounded-full bg-white shadow-lg',
                'ring-0 transition duration-200 ease-in-out',
                offlineMode ? 'translate-x-4' : 'translate-x-0'
              )}
            />
          </span>
          <span className="text-sm font-medium">
            Prefer offline
            <span className="ml-2 text-xs text-muted-foreground font-normal">
              Download a local model — no internet required after download
            </span>
          </span>
        </label>

        {/* Local model options (revealed when toggle is on) */}
        {offlineMode && (
          <div
            className="mt-4 flex flex-col gap-3"
            role="list"
            aria-label="Local model options"
          >
            {LOCAL_MODELS.map(({ variant, label, description }) => {
              const isThisDownloading = downloadingVariant === variant

              return (
                <div
                  key={variant}
                  role="listitem"
                  className="flex items-center justify-between rounded-md border bg-background px-3 py-2"
                >
                  <div>
                    <span className="text-sm font-medium">{label}</span>
                    <p className="text-xs text-muted-foreground">{description}</p>
                    {isThisDownloading && (
                      <div
                        role="progressbar"
                        aria-valuenow={downloadProgress}
                        aria-valuemin={0}
                        aria-valuemax={100}
                        aria-label={`Downloading ${label}`}
                        className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-muted"
                      >
                        <div
                          className="h-full bg-primary transition-all duration-300"
                          style={{ width: `${downloadProgress}%` }}
                        />
                      </div>
                    )}
                  </div>

                  <button
                    type="button"
                    disabled={downloadingVariant !== null}
                    aria-label={`Download ${label}`}
                    onClick={() => handleDownloadModel(variant)}
                    className={cn(
                      'ml-4 inline-flex h-8 shrink-0 items-center justify-center rounded-md px-3',
                      'border border-input bg-background text-xs font-medium shadow-sm',
                      'hover:bg-accent hover:text-accent-foreground',
                      'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
                      'disabled:pointer-events-none disabled:opacity-50',
                      'transition-colors'
                    )}
                  >
                    {isThisDownloading ? 'Downloading…' : 'Download'}
                  </button>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {/* Bottom navigation */}
      <div className="mt-auto flex items-center justify-between pt-2">
        <button
          type="button"
          onClick={goBack}
          aria-label="Go back to previous wizard step"
          className={cn(
            'inline-flex h-9 items-center justify-center rounded-md px-4 text-sm font-medium',
            'border border-input bg-background shadow-sm',
            'hover:bg-accent hover:text-accent-foreground',
            'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
            'transition-colors'
          )}
        >
          Back
        </button>

        <button
          type="button"
          disabled={!isAnyConnected}
          onClick={goNext}
          aria-label="Continue to next wizard step"
          aria-disabled={!isAnyConnected}
          className={cn(
            'inline-flex h-9 items-center justify-center rounded-md px-6 text-sm font-medium',
            'bg-primary text-primary-foreground shadow',
            'hover:bg-primary/90',
            'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
            'disabled:pointer-events-none disabled:opacity-50',
            'transition-colors'
          )}
        >
          Next
        </button>
      </div>
    </div>
  )
}
