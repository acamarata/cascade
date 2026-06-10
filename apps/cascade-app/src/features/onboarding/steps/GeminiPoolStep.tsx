/**
 * Purpose: Wizard step for provisioning and registering Gemini API keys into
 *   the pool at `~/.cascade/pool/gemini-keys.json`.
 *
 *   Presents one ProvisionCard per Google account discovered by auto-auth.
 *   When all accounts are provisioned or skipped, calls cascade_pool_register_key
 *   for each provisioned account and advances the wizard.
 *
 * Inputs:
 *   - Tauri IPC: cascade_auto_auth_scan → DiscoveredAccount[] (Gemini/Google only)
 *   - Tauri IPC: cascade_pool_register_key(email, apiKey, projectId) → RegisterResult
 *   - WizardContext.goNext() / WizardContext.skipStep()
 *
 * Outputs:
 *   - Per-account ProvisionCard components
 *   - "Register All & Continue" button (enabled when ≥1 account provisioned)
 *   - "Skip All" button
 *   - Registration result feedback per account
 *
 * Constraints:
 *   - API keys are never displayed after provisioning; only presence confirmed.
 *   - Empty account list state: shows "No Google accounts found" with Skip option.
 *   - Registration errors shown inline; step can still advance if ≥1 key registered.
 *   - Renders correctly before any provider is connected (AI-optional).
 *
 * SPORT: MASTER-COMPONENTS.md — GeminiPoolStep — T-P3-E03-39b / T-P3-E03-40
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { AlertCircle, CheckCircle2, Loader2 } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useWizard } from '@/features/onboarding/WizardContext'
import { ProvisionCard } from '@/components/wizard/ProvisionCard'
import { WizardStep } from '../types'

// ---------------------------------------------------------------------------
// Types (mirror cascade_types IPC shapes)
// ---------------------------------------------------------------------------

type RegisterResult =
  | { type: 'success' }
  | { type: 'alreadyRegistered' }
  | { type: 'invalidKey'; message: string }
  | { type: 'error'; message: string }

function getRegisterError(r: RegisterResult): string | null {
  if (r.type === 'invalidKey' || r.type === 'error') {
    return r.message || 'Unknown error'
  }
  return null
}

interface DiscoveredAccount {
  source: string
  emailOrHint: string
  provider: string
  authType: string
  importable: boolean
}

// ---------------------------------------------------------------------------
// Registration status per email
// ---------------------------------------------------------------------------

type RegStatus = 'pending' | 'registering' | 'success' | 'already' | 'error'

interface ProvisionedEntry {
  email: string
  apiKey: string
  projectId: string
}

interface RegState {
  status: RegStatus
  error?: string
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

interface GeminiPoolStepProps {
  className?: string
}

export function GeminiPoolStep({ className }: GeminiPoolStepProps) {
  const { goNext, skipStep } = useWizard()

  const [loading, setLoading] = useState(true)
  const [scanError, setScanError] = useState<string | null>(null)
  // Google accounts discovered by auto-auth scan
  const [accounts, setAccounts] = useState<string[]>([])

  // Provisioned entries waiting for registration
  const [provisioned, setProvisioned] = useState<Map<string, ProvisionedEntry>>(new Map())
  // Skipped account emails
  const [skipped, setSkipped] = useState<Set<string>>(new Set())
  // Registration status per email
  const [regState, setRegState] = useState<Map<string, RegState>>(new Map())
  const [registering, setRegistering] = useState(false)

  // ---------------------------------------------------------------------------
  // Scan for Google accounts on mount
  // ---------------------------------------------------------------------------

  useEffect(() => {
    invoke<DiscoveredAccount[]>('cascade_auto_auth_scan')
      .then((discovered) => {
        const geminiEmails = discovered
          .filter((a) => a.provider === 'google' || a.provider === 'gemini')
          .map((a) => a.emailOrHint)
          // deduplicate
          .filter((e, i, arr) => arr.indexOf(e) === i)
        setAccounts(geminiEmails)
        setLoading(false)
      })
      .catch((err: unknown) => {
        setScanError(String(err))
        setLoading(false)
      })
  }, [])

  // ---------------------------------------------------------------------------
  // Callbacks for ProvisionCard
  // ---------------------------------------------------------------------------

  const handleProvisioned = useCallback(
    (email: string, apiKey: string, projectId: string) => {
      setProvisioned((prev) => {
        const next = new Map(prev)
        next.set(email, { email, apiKey, projectId })
        return next
      })
    },
    [],
  )

  const handleSkip = useCallback((email: string) => {
    setSkipped((prev) => {
      const next = new Set(prev)
      next.add(email)
      return next
    })
  }, [])

  // ---------------------------------------------------------------------------
  // Register all provisioned keys
  // ---------------------------------------------------------------------------

  const handleRegisterAll = useCallback(async () => {
    setRegistering(true)
    const entries = Array.from(provisioned.values())

    for (const entry of entries) {
      setRegState((prev) => {
        const next = new Map(prev)
        next.set(entry.email, { status: 'registering' })
        return next
      })

      try {
        const result = await invoke<RegisterResult>('cascade_pool_register_key', {
          email: entry.email,
          apiKey: entry.apiKey,
          projectId: entry.projectId,
        })

        const err = getRegisterError(result)
        setRegState((prev) => {
          const next = new Map(prev)
          if (result.type === 'success') {
            next.set(entry.email, { status: 'success' })
          } else if (result.type === 'alreadyRegistered') {
            next.set(entry.email, { status: 'already' })
          } else {
            next.set(entry.email, { status: 'error', error: err ?? 'Registration failed' })
          }
          return next
        })
      } catch (err) {
        setRegState((prev) => {
          const next = new Map(prev)
          next.set(entry.email, { status: 'error', error: String(err) })
          return next
        })
      }
    }

    setRegistering(false)
    goNext()
  }, [provisioned, goNext])

  const handleSkipAll = useCallback(() => {
    skipStep(WizardStep.ProviderConnect)
  }, [skipStep])

  // ---------------------------------------------------------------------------
  // Derived state
  // ---------------------------------------------------------------------------

  const allHandled = accounts.length > 0 &&
    accounts.every((e) => provisioned.has(e) || skipped.has(e))

  const provisionedCount = provisioned.size
  const canRegister = provisionedCount > 0

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <section
      className={cn('flex h-full flex-col gap-6 overflow-y-auto px-6 py-8', className)}
      aria-label="Gemini Pool Setup"
    >
      {/* Header */}
      <header className="flex flex-col gap-1">
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">
          Set Up Gemini Key Pool
        </h1>
        <p className="text-sm text-muted-foreground">
          Provision or paste a Gemini API key for each Google account. Keys are stored in{' '}
          <code className="rounded bg-muted px-1 py-0.5 text-xs">~/.cascade/pool/gemini-keys.json</code>{' '}
          and used by the cascade daemon for load-balanced Gemini requests.
        </p>
      </header>

      {/* Loading state */}
      {loading && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground" role="status">
          <Loader2 size={15} className="animate-spin" aria-hidden="true" />
          Scanning for Google accounts…
        </div>
      )}

      {/* Scan error */}
      {!loading && scanError && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-3 text-sm text-destructive"
        >
          <AlertCircle size={15} className="mt-0.5 shrink-0" aria-hidden="true" />
          <div>
            <p className="font-medium">Account scan failed</p>
            <p className="text-xs">{scanError}</p>
          </div>
        </div>
      )}

      {/* Empty: no accounts found */}
      {!loading && !scanError && accounts.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-6 text-center">
          <p className="mb-1 text-sm font-medium text-foreground">No Google accounts found</p>
          <p className="mb-4 text-xs text-muted-foreground">
            No Google or Gemini accounts were detected from installed AI harnesses or environment
            variables. You can add accounts manually later from the Gemini Pool settings.
          </p>
          <button
            type="button"
            onClick={handleSkipAll}
            className="rounded-md border border-border px-4 py-2 text-sm font-medium text-muted-foreground hover:border-primary/50 hover:text-foreground"
          >
            Skip for Now
          </button>
        </div>
      )}

      {/* Account cards */}
      {!loading && accounts.length > 0 && (
        <>
          <ul className="flex flex-col gap-3 list-none p-0 m-0" aria-label="Google accounts">
            {accounts.map((email) => (
              <li key={email}>
                {/* Show result badge if registered */}
                {regState.has(email) ? (
                  <div
                    className={cn(
                      'flex items-center gap-2 rounded-lg border p-3 text-sm',
                      regState.get(email)?.status === 'success' || regState.get(email)?.status === 'already'
                        ? 'border-green-500/50 bg-green-500/5 text-green-700 dark:text-green-400'
                        : 'border-destructive/50 bg-destructive/10 text-destructive',
                    )}
                    role="status"
                    aria-label={`Registration result for ${email}`}
                  >
                    {regState.get(email)?.status === 'registering' && (
                      <Loader2 size={14} className="animate-spin shrink-0" aria-hidden="true" />
                    )}
                    {(regState.get(email)?.status === 'success' || regState.get(email)?.status === 'already') && (
                      <CheckCircle2 size={14} className="shrink-0" aria-hidden="true" />
                    )}
                    {regState.get(email)?.status === 'error' && (
                      <AlertCircle size={14} className="shrink-0" aria-hidden="true" />
                    )}
                    <span className="truncate font-medium" title={email}>{email}</span>
                    <span className="ml-auto shrink-0 text-xs">
                      {regState.get(email)?.status === 'success' && 'Registered'}
                      {regState.get(email)?.status === 'already' && 'Already registered'}
                      {regState.get(email)?.status === 'error' && (regState.get(email)?.error ?? 'Error')}
                      {regState.get(email)?.status === 'registering' && 'Registering…'}
                    </span>
                  </div>
                ) : (
                  <ProvisionCard
                    email={email}
                    onProvisioned={handleProvisioned}
                    onSkip={handleSkip}
                  />
                )}
              </li>
            ))}
          </ul>

          {/* Progress summary */}
          {(provisionedCount > 0 || skipped.size > 0) && (
            <p className="text-xs text-muted-foreground" aria-live="polite">
              {provisionedCount > 0 && `${provisionedCount} key${provisionedCount !== 1 ? 's' : ''} ready to register`}
              {provisionedCount > 0 && skipped.size > 0 && ' · '}
              {skipped.size > 0 && `${skipped.size} skipped`}
            </p>
          )}

          {/* Action bar */}
          <div className="flex items-center gap-3 pt-2">
            <button
              type="button"
              onClick={handleRegisterAll}
              disabled={!canRegister || registering || !allHandled}
              className="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground disabled:opacity-50 hover:bg-primary/90"
              aria-label="Register all provisioned keys and continue"
            >
              {registering ? (
                <span className="flex items-center gap-1.5">
                  <Loader2 size={13} className="animate-spin" aria-hidden="true" />
                  Registering…
                </span>
              ) : (
                `Register ${provisionedCount > 0 ? provisionedCount : ''} Key${provisionedCount !== 1 ? 's' : ''} & Continue`
              )}
            </button>

            <button
              type="button"
              onClick={handleSkipAll}
              disabled={registering}
              className="rounded-md border border-border px-4 py-2 text-sm font-medium text-muted-foreground hover:border-primary/50 hover:text-foreground disabled:opacity-50"
            >
              Skip All
            </button>
          </div>
        </>
      )}
    </section>
  )
}
