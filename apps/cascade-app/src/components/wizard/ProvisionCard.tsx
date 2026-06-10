/**
 * Purpose: Per-account provisioning card for the Gemini Pool wizard step.
 *   Renders a mode selector (Full Auto / Guided / Manual), a Start button,
 *   progress feedback while provisioning is in flight, and the result state
 *   (success / error with retry / skip).
 *
 * Inputs:
 *   - email: string — Google account email shown as the card title.
 *   - onProvisioned(email, apiKey, projectId): callback when provisioning succeeds.
 *   - onSkip(email): callback when the user chooses to skip this account.
 *
 * Outputs:
 *   - Tauri IPC calls: cascade_provision_google_start, cascade_provision_google_status,
 *     cascade_provision_google_cancel, cascade_pool_register_key.
 *   - Visual card with mode radio group, progress stepper (Full Auto),
 *     browser-open prompt (Guided), text input (Manual / Guided paste),
 *     and result feedback.
 *
 * Constraints:
 *   - API keys are never displayed after provisioning — only the presence is confirmed.
 *   - Polling is stopped on unmount to prevent state updates on dead components.
 *   - No direct cascade_pool_register_key call here — parent (GeminiPoolStep) owns
 *     the final registration so it can batch multiple accounts.
 *
 * SPORT: MASTER-COMPONENTS.md — ProvisionCard — T-P3-E03-39b
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { CheckCircle2, AlertCircle, Loader2, ExternalLink, KeyRound } from 'lucide-react'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// Types (mirrors Rust cascade_types::provision via Tauri camelCase IPC)
// ---------------------------------------------------------------------------

type ProvisionMode = 'fullAuto' | 'guided' | 'manual'

interface ProvisionRequest {
  accountEmail: string
  mode: ProvisionMode
  /** OAuth token (Full Auto only) */
  clientId?: string
  /** API key (Manual only) */
  clientSecret?: string
}

interface ProvisionStatus {
  accountEmail: string
  status: string
  done: boolean
  error: string | null
}

// ProvisionResult variants — internally tagged, camelCase (mirrors cascade-types provision.rs)
type ProvisionResult =
  | { type: 'success'; apiKey: string; projectId: string }
  | { type: 'guidedPending'; step: number; nextUrl: string }
  | { type: 'error'; message: string }

function getResultError(r: ProvisionResult): string {
  if (r.type !== 'error') return ''
  return r.message ?? 'Unknown error'
}

// ---------------------------------------------------------------------------
// Full-Auto progress steps (cosmetic labels)
// ---------------------------------------------------------------------------

const FULL_AUTO_STEPS = [
  'Creating GCP project…',
  'Waiting for project activation…',
  'Enabling Gemini API…',
  'Creating API key…',
  'Retrieving key string…',
]

// ---------------------------------------------------------------------------
// Inline UI helpers
// ---------------------------------------------------------------------------

function RadioOption({
  id,
  value,
  current,
  label,
  description,
  disabled,
  onChange,
}: {
  id: string
  value: ProvisionMode
  current: ProvisionMode
  label: string
  description: string
  disabled?: boolean
  onChange: (v: ProvisionMode) => void
}) {
  const checked = current === value
  return (
    <label
      htmlFor={id}
      className={cn(
        'flex cursor-pointer items-start gap-3 rounded-md border p-3 transition-colors',
        checked ? 'border-primary bg-primary/5' : 'border-border hover:border-primary/50',
        disabled && 'cursor-not-allowed opacity-50',
      )}
    >
      <input
        id={id}
        type="radio"
        name={`mode-${id.split('-')[0]}`}
        value={value}
        checked={checked}
        disabled={disabled}
        onChange={() => onChange(value)}
        className="mt-0.5 accent-primary"
        aria-describedby={`${id}-desc`}
      />
      <div className="flex flex-col gap-0.5">
        <span className="text-sm font-medium text-foreground">{label}</span>
        <span id={`${id}-desc`} className="text-xs text-muted-foreground">
          {description}
        </span>
      </div>
    </label>
  )
}

function ProgressStep({ label, active, done }: { label: string; active: boolean; done: boolean }) {
  return (
    <li className="flex items-center gap-2 text-sm">
      {done ? (
        <CheckCircle2 size={15} className="shrink-0 text-green-500" aria-hidden="true" />
      ) : active ? (
        <Loader2 size={15} className="shrink-0 animate-spin text-primary" aria-hidden="true" />
      ) : (
        <span className="h-3.5 w-3.5 shrink-0 rounded-full border border-muted-foreground/40" aria-hidden="true" />
      )}
      <span className={cn(active && 'text-foreground', !active && !done && 'text-muted-foreground', done && 'text-muted-foreground line-through')}>
        {label}
      </span>
    </li>
  )
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export interface ProvisionCardProps {
  /** Google account email for this card. */
  email: string
  /** Called when provisioning succeeds and key is ready for pool registration. */
  onProvisioned: (email: string, apiKey: string, projectId: string) => void
  /** Called when the user clicks Skip for this account. */
  onSkip: (email: string) => void
}

type CardPhase = 'idle' | 'provisioning' | 'done' | 'error'

export function ProvisionCard({ email, onProvisioned, onSkip }: ProvisionCardProps) {
  const [mode, setMode] = useState<ProvisionMode>('manual')
  const [phase, setPhase] = useState<CardPhase>('idle')
  const [errorMsg, setErrorMsg] = useState<string | null>(null)
  const [statusMsg, setStatusMsg] = useState<string>('')
  const [activeStep, setActiveStep] = useState<number>(-1) // index into FULL_AUTO_STEPS
  const [doneSteps, setDoneSteps] = useState<number[]>([])
  const [guidedUrl, setGuidedUrl] = useState<string | null>(null)
  const [pasteKey, setPasteKey] = useState<string>('')
  const [manualKey, setManualKey] = useState<string>('')

  const pollRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const mountedRef = useRef(true)

  useEffect(() => {
    mountedRef.current = true
    return () => {
      mountedRef.current = false
      if (pollRef.current) clearTimeout(pollRef.current)
    }
  }, [])

  // ---------------------------------------------------------------------------
  // Polling
  // ---------------------------------------------------------------------------

  const pollStatus = useCallback(() => {
    invoke<ProvisionStatus>('cascade_provision_google_status', { email })
      .then((s) => {
        if (!mountedRef.current) return
        setStatusMsg(s.status)

        // Map status string to a cosmetic step index for Full Auto
        const stepMap: Record<string, number> = {
          'creating_project': 0,
          'waiting_for_activation': 1,
          'enabling_api': 2,
          'creating_key': 3,
          'retrieving_key': 4,
        }
        const idx = stepMap[s.status] ?? -1
        if (idx >= 0) {
          setActiveStep(idx)
          setDoneSteps(Array.from({ length: idx }, (_, i) => i))
        }

        if (s.done) {
          setPhase('done')
        } else if (s.error) {
          setErrorMsg(s.error)
          setPhase('error')
        } else {
          pollRef.current = setTimeout(pollStatus, 1200)
        }
      })
      .catch((err: unknown) => {
        if (!mountedRef.current) return
        setErrorMsg(String(err))
        setPhase('error')
      })
  }, [email])

  // ---------------------------------------------------------------------------
  // Start provisioning
  // ---------------------------------------------------------------------------

  const handleStart = useCallback(async () => {
    setPhase('provisioning')
    setErrorMsg(null)
    setStatusMsg('')
    setActiveStep(-1)
    setDoneSteps([])
    setGuidedUrl(null)

    const req: ProvisionRequest = {
      accountEmail: email,
      mode,
      ...(mode === 'manual' ? { clientSecret: manualKey } : {}),
    }

    try {
      const result = await invoke<ProvisionResult>('cascade_provision_google_start', { req })

      if (!mountedRef.current) return

      if (result.type === 'success') {
        setActiveStep(FULL_AUTO_STEPS.length - 1)
        setDoneSteps(Array.from({ length: FULL_AUTO_STEPS.length }, (_, i) => i))
        setPhase('done')
        // Register key into pool via parent callback
        onProvisioned(email, result.apiKey, result.projectId)
      } else if (result.type === 'guidedPending') {
        setGuidedUrl(result.nextUrl)
        setStatusMsg(`Step ${result.step + 1} of 3`)
        setPhase('idle')
      } else if (result.type === 'error') {
        setErrorMsg(getResultError(result))
        setPhase('error')
      } else {
        // Full auto in progress — start polling
        pollRef.current = setTimeout(pollStatus, 1200)
      }
    } catch (err) {
      if (!mountedRef.current) return
      setErrorMsg(String(err))
      setPhase('error')
    }
  }, [email, mode, manualKey, onProvisioned, pollStatus])

  // ---------------------------------------------------------------------------
  // Cancel
  // ---------------------------------------------------------------------------

  const handleCancel = useCallback(async () => {
    if (pollRef.current) clearTimeout(pollRef.current)
    await invoke('cascade_provision_google_cancel', { email }).catch(() => {})
    setPhase('idle')
    setStatusMsg('')
  }, [email])

  // ---------------------------------------------------------------------------
  // Guided: open console URL and then paste key
  // ---------------------------------------------------------------------------

  const handleOpenGuided = useCallback(() => {
    if (!guidedUrl) return
    invoke('plugin:opener|open_url', { url: guidedUrl }).catch(() => {
      window.open(guidedUrl, '_blank', 'noopener,noreferrer')
    })
  }, [guidedUrl])

  const handleGuidedPaste = useCallback(async () => {
    if (!pasteKey.trim()) return
    setPhase('provisioning')
    setErrorMsg(null)

    const req: ProvisionRequest = {
      accountEmail: email,
      mode: 'guided',
      clientSecret: pasteKey.trim(),
    }

    try {
      const result = await invoke<ProvisionResult>('cascade_provision_google_start', { req })
      if (!mountedRef.current) return

      if (result.type === 'success') {
        setPhase('done')
        onProvisioned(email, result.apiKey, result.projectId)
      } else if (result.type === 'guidedPending') {
        setGuidedUrl(result.nextUrl)
        setStatusMsg(`Step ${result.step + 1} of 3`)
        setPhase('idle')
      } else {
        setErrorMsg(getResultError(result))
        setPhase('error')
      }
    } catch (err) {
      if (!mountedRef.current) return
      setErrorMsg(String(err))
      setPhase('error')
    }
  }, [email, pasteKey, onProvisioned])

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  const isProvisioning = phase === 'provisioning'
  const isDone = phase === 'done'

  return (
    <article
      role="region"
      aria-label={`Provision Gemini key for ${email}`}
      className={cn(
        'rounded-lg border bg-card p-4 shadow-sm transition-colors',
        isDone && 'border-green-500/50 bg-green-500/5',
        phase === 'error' && 'border-destructive/50',
      )}
    >
      {/* Header */}
      <div className="mb-3 flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <KeyRound size={16} className="shrink-0 text-muted-foreground" aria-hidden="true" />
          <span className="truncate text-sm font-medium text-foreground" title={email}>
            {email}
          </span>
        </div>
        {isDone && (
          <CheckCircle2 size={16} className="shrink-0 text-green-500" aria-label="Provisioned" />
        )}
      </div>

      {/* Done state */}
      {isDone && (
        <p className="text-xs text-green-700 dark:text-green-400">
          API key provisioned and ready for pool registration.
        </p>
      )}

      {/* Active / idle states */}
      {!isDone && (
        <>
          {/* Mode selector (disabled during provisioning) */}
          <div className="mb-3 grid gap-2" role="radiogroup" aria-label="Provisioning mode">
            <RadioOption
              id={`${email}-full-auto`}
              value="fullAuto"
              current={mode}
              label="Full Auto"
              description="Creates GCP project, enables Gemini API, and generates an API key automatically."
              disabled={isProvisioning}
              onChange={setMode}
            />
            <RadioOption
              id={`${email}-guided`}
              value="guided"
              current={mode}
              label="Guided"
              description="Opens Google Cloud Console at each step; paste the key when done."
              disabled={isProvisioning}
              onChange={setMode}
            />
            <RadioOption
              id={`${email}-manual`}
              value="manual"
              current={mode}
              label="Manual"
              description="Paste an existing Gemini API key to validate and register."
              disabled={isProvisioning}
              onChange={setMode}
            />
          </div>

          {/* Manual: API key input */}
          {mode === 'manual' && !isProvisioning && (
            <div className="mb-3">
              <label htmlFor={`${email}-manual-key`} className="mb-1 block text-xs font-medium text-foreground">
                API Key
              </label>
              <input
                id={`${email}-manual-key`}
                type="password"
                value={manualKey}
                onChange={(e) => setManualKey(e.target.value)}
                placeholder="AIza…"
                autoComplete="off"
                className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
              />
            </div>
          )}

          {/* Guided: open console URL */}
          {mode === 'guided' && guidedUrl && !isProvisioning && (
            <div className="mb-3 space-y-2">
              <button
                type="button"
                onClick={handleOpenGuided}
                className="inline-flex items-center gap-1.5 text-sm text-primary underline-offset-2 hover:underline"
              >
                <ExternalLink size={13} aria-hidden="true" />
                Open Google Cloud Console
              </button>
              <div>
                <label htmlFor={`${email}-paste-key`} className="mb-1 block text-xs font-medium text-foreground">
                  Paste API Key
                </label>
                <input
                  id={`${email}-paste-key`}
                  type="password"
                  value={pasteKey}
                  onChange={(e) => setPasteKey(e.target.value)}
                  placeholder="AIza…"
                  autoComplete="off"
                  className="w-full rounded-md border border-input bg-background px-3 py-1.5 text-sm placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-primary/50"
                />
              </div>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={handleGuidedPaste}
                  disabled={!pasteKey.trim()}
                  className="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground disabled:opacity-50 hover:bg-primary/90"
                >
                  Validate & Register
                </button>
                {statusMsg && (
                  <span className="self-center text-xs text-muted-foreground">{statusMsg}</span>
                )}
              </div>
            </div>
          )}

          {/* Full-auto progress stepper */}
          {mode === 'fullAuto' && isProvisioning && (
            <ol className="mb-3 space-y-1.5" aria-label="Provisioning progress">
              {FULL_AUTO_STEPS.map((label, i) => (
                <ProgressStep
                  key={label}
                  label={label}
                  active={activeStep === i}
                  done={doneSteps.includes(i)}
                />
              ))}
            </ol>
          )}

          {/* Error feedback */}
          {phase === 'error' && errorMsg && (
            <div
              role="alert"
              className="mb-3 flex items-start gap-2 rounded-md border border-destructive/50 bg-destructive/10 p-2.5 text-xs text-destructive"
            >
              <AlertCircle size={13} className="mt-0.5 shrink-0" aria-hidden="true" />
              <span>{errorMsg}</span>
            </div>
          )}

          {/* Action buttons */}
          <div className="flex items-center gap-2">
            {!isProvisioning && (
              <>
                {/* Start / Retry */}
                {(mode !== 'guided' || !guidedUrl) && (
                  <button
                    type="button"
                    onClick={handleStart}
                    disabled={mode === 'manual' && !manualKey.trim()}
                    className="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground disabled:opacity-50 hover:bg-primary/90"
                    aria-label={phase === 'error' ? `Retry provisioning for ${email}` : `Start provisioning for ${email}`}
                  >
                    {phase === 'error' ? 'Retry' : 'Start'}
                  </button>
                )}

                {/* Skip */}
                <button
                  type="button"
                  onClick={() => onSkip(email)}
                  className="rounded-md border border-border px-3 py-1.5 text-xs font-medium text-muted-foreground hover:border-primary/50 hover:text-foreground"
                  aria-label={`Skip provisioning for ${email}`}
                >
                  Skip
                </button>
              </>
            )}

            {isProvisioning && (
              <>
                <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <Loader2 size={12} className="animate-spin" aria-hidden="true" />
                  {statusMsg || 'Provisioning…'}
                </span>
                <button
                  type="button"
                  onClick={handleCancel}
                  className="ml-auto rounded-md border border-border px-3 py-1.5 text-xs font-medium text-muted-foreground hover:border-destructive/50 hover:text-destructive"
                  aria-label={`Cancel provisioning for ${email}`}
                >
                  Cancel
                </button>
              </>
            )}
          </div>
        </>
      )}
    </article>
  )
}
