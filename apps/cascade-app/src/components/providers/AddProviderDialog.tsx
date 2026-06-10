/**
 * Purpose: Dialog for adding a new AI provider connection.
 *   Two-step flow: (1) pick a provider; (2) complete the auth (API key or OAuth).
 *   Generic OpenAI-compatible option shows base_url + optional api_key + model_id.
 * Inputs:
 *   - open: boolean — dialog open state
 *   - onOpenChange: (open: boolean) => void — controlled open
 *   - onSuccess: () => void — called after successful add; caller should refresh list
 * Outputs: Radix Dialog with 2-step form
 * Constraints:
 *   - API key MUST be wiped from state after successful save (security: T-P3-E04-24 CR-A).
 *   - API key input type="password"; never log or display stored value.
 *   - OAuth poll timeout = 120 s; show clear error after timeout (CR-B).
 *   - All IPC calls gracefully degraded: command-not-found → show "Not yet available" error.
 *   - WCAG 2.1 AA: focus rings, aria labels, escape to close.
 * SPORT: MASTER-COMPONENTS.md — AddProviderDialog — components/providers/ — T-P3-E04-24
 */

import { useState, useRef, useEffect, useCallback } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { openUrl as shellOpen } from '@tauri-apps/plugin-opener'
import { Loader2, Check, AlertCircle, ChevronLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { cn } from '@/lib/utils'
import { PROVIDER_CATALOG } from '@/types/providers'
import type { ProviderCatalogEntry } from '@/types/providers'

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const OAUTH_POLL_INTERVAL_MS = 2_000
const OAUTH_TIMEOUT_MS = 120_000

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

/**
 * Wraps an invoke call and converts "command not found" errors to a
 * user-readable string so the UI works even before Rust wiring lands.
 *
 * Returns [result, null] on success or [null, errorMessage] on failure.
 */
async function safeInvoke<T>(
  cmd: string,
  args?: Record<string, unknown>
): Promise<[T | null, string | null]> {
  try {
    const result = await invoke<T>(cmd, args)
    return [result, null]
  } catch (err) {
    const msg = String(err)
    if (msg.includes('not found') || msg.includes('unknown command') || msg.includes('No such')) {
      return [null, 'Not yet available — daemon wiring in progress.']
    }
    return [null, msg]
  }
}

// ---------------------------------------------------------------------------
// Step 1 — Provider picker
// ---------------------------------------------------------------------------

interface ProviderPickerProps {
  onSelect: (entry: ProviderCatalogEntry) => void
}

function ProviderPicker({ onSelect }: ProviderPickerProps) {
  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">Choose the AI provider you want to connect.</p>
      <ul
        role="listbox"
        aria-label="Select a provider"
        className="grid gap-2 max-h-80 overflow-y-auto pr-1"
      >
        {PROVIDER_CATALOG.map((entry) => (
          <li key={entry.id} role="option" aria-selected={false}>
            <button
              type="button"
              className={cn(
                'w-full rounded-md border border-border bg-background px-4 py-3 text-left',
                'hover:bg-accent hover:border-primary/40 transition-colors',
                'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
              )}
              onClick={() => onSelect(entry)}
            >
              <div className="flex items-center justify-between gap-2">
                <div>
                  <p className="text-sm font-medium">{entry.name}</p>
                  <p className="text-xs text-muted-foreground">{entry.description}</p>
                </div>
                <span className="shrink-0 text-xs text-muted-foreground capitalize">
                  {entry.authMethod === 'api_key'
                    ? 'API Key'
                    : entry.authMethod === 'oauth'
                      ? 'OAuth'
                      : 'No Auth'}
                </span>
              </div>
            </button>
          </li>
        ))}
      </ul>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Step 2a — API key form
// ---------------------------------------------------------------------------

interface ApiKeyFormProps {
  entry: ProviderCatalogEntry
  onSuccess: () => void
  onBack: () => void
}

function ApiKeyForm({ entry, onSuccess, onBack }: ApiKeyFormProps) {
  const [apiKey, setApiKey] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [testOk, setTestOk] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!apiKey.trim()) {
      setError('API key is required.')
      return
    }
    setSubmitting(true)
    setError(null)

    const cmd =
      entry.id === 'generic' ? 'cascade_providers_add_generic' : 'cascade_providers_add_apikey'

    const [, err] = await safeInvoke<void>(cmd, { id: entry.id, key: apiKey.trim() })
    // Wipe key from state immediately regardless of outcome (CR-A)
    setApiKey('')
    setSubmitting(false)

    if (err) {
      setError(err)
      return
    }
    setTestOk(true)
    // Brief success flash before closing
    setTimeout(onSuccess, 600)
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4" noValidate>
      <p className="text-sm text-muted-foreground">
        Enter your {entry.name} API key. It will be stored in the system keychain.
      </p>

      <div className="space-y-1.5">
        <label htmlFor="api-key-input" className="text-sm font-medium">
          API Key
        </label>
        <Input
          id="api-key-input"
          ref={inputRef}
          type="password"
          placeholder="sk-…"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          autoComplete="off"
          aria-required="true"
          aria-invalid={error !== null}
          aria-describedby={error ? 'api-key-error' : undefined}
          disabled={submitting || testOk}
        />
        {error && (
          <p
            id="api-key-error"
            role="alert"
            className="flex items-center gap-1.5 text-xs text-destructive"
          >
            <AlertCircle className="h-3 w-3 shrink-0" aria-hidden="true" />
            {error}
          </p>
        )}
      </div>

      <div className="flex justify-between gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onBack} disabled={submitting}>
          <ChevronLeft className="h-4 w-4 mr-1" aria-hidden="true" />
          Back
        </Button>
        <Button type="submit" disabled={submitting || testOk || !apiKey.trim()}>
          {submitting && <Loader2 className="h-4 w-4 mr-2 animate-spin" aria-hidden="true" />}
          {testOk && <Check className="h-4 w-4 mr-2" aria-hidden="true" />}
          {testOk ? 'Connected!' : submitting ? 'Saving…' : 'Save & Connect'}
        </Button>
      </div>
    </form>
  )
}

// ---------------------------------------------------------------------------
// Step 2b — Generic endpoint form
// ---------------------------------------------------------------------------

interface GenericFormProps {
  entry: ProviderCatalogEntry
  onSuccess: () => void
  onBack: () => void
}

function GenericEndpointForm({ entry, onSuccess, onBack }: GenericFormProps) {
  const [baseUrl, setBaseUrl] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [modelId, setModelId] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!baseUrl.trim()) {
      setError('Base URL is required.')
      return
    }
    setSubmitting(true)
    setError(null)

    const [, err] = await safeInvoke<void>('cascade_providers_add_generic', {
      id: entry.id,
      base_url: baseUrl.trim(),
      key: apiKey.trim() || null,
      model_id: modelId.trim() || null,
    })
    // Wipe key immediately (CR-A)
    setApiKey('')
    setSubmitting(false)

    if (err) {
      setError(err)
      return
    }
    setSaved(true)
    setTimeout(onSuccess, 600)
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4" noValidate>
      <p className="text-sm text-muted-foreground">
        Connect any OpenAI-compatible endpoint (e.g. LM Studio, vLLM, Ollama HTTP).
      </p>

      <div className="space-y-1.5">
        <label htmlFor="generic-base-url" className="text-sm font-medium">
          Base URL{' '}
          <span className="text-destructive" aria-hidden="true">
            *
          </span>
        </label>
        <Input
          id="generic-base-url"
          type="url"
          placeholder="http://localhost:11434/v1"
          value={baseUrl}
          onChange={(e) => setBaseUrl(e.target.value)}
          aria-required="true"
          aria-invalid={error !== null && !baseUrl.trim()}
          disabled={submitting || saved}
        />
      </div>

      <div className="space-y-1.5">
        <label htmlFor="generic-api-key" className="text-sm font-medium text-muted-foreground">
          API Key <span className="text-xs">(optional)</span>
        </label>
        <Input
          id="generic-api-key"
          type="password"
          placeholder="Leave blank if not required"
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          autoComplete="off"
          disabled={submitting || saved}
        />
      </div>

      <div className="space-y-1.5">
        <label htmlFor="generic-model-id" className="text-sm font-medium text-muted-foreground">
          Default Model ID <span className="text-xs">(optional)</span>
        </label>
        <Input
          id="generic-model-id"
          type="text"
          placeholder="e.g. qwen2.5-coder:7b"
          value={modelId}
          onChange={(e) => setModelId(e.target.value)}
          disabled={submitting || saved}
        />
      </div>

      {error && (
        <p role="alert" className="flex items-center gap-1.5 text-xs text-destructive">
          <AlertCircle className="h-3 w-3 shrink-0" aria-hidden="true" />
          {error}
        </p>
      )}

      <div className="flex justify-between gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onBack} disabled={submitting}>
          <ChevronLeft className="h-4 w-4 mr-1" aria-hidden="true" />
          Back
        </Button>
        <Button type="submit" disabled={submitting || saved || !baseUrl.trim()}>
          {submitting && <Loader2 className="h-4 w-4 mr-2 animate-spin" aria-hidden="true" />}
          {saved && <Check className="h-4 w-4 mr-2" aria-hidden="true" />}
          {saved ? 'Connected!' : submitting ? 'Saving…' : 'Save & Connect'}
        </Button>
      </div>
    </form>
  )
}

// ---------------------------------------------------------------------------
// Step 2c — OAuth flow
// ---------------------------------------------------------------------------

type OAuthPhase = 'idle' | 'opening' | 'waiting' | 'success' | 'error' | 'timeout'

interface OAuthFormProps {
  entry: ProviderCatalogEntry
  onSuccess: () => void
  onBack: () => void
}

function OAuthForm({ entry, onSuccess, onBack }: OAuthFormProps) {
  const [phase, setPhase] = useState<OAuthPhase>('idle')
  const [error, setError] = useState<string | null>(null)
  const pollTimer = useRef<ReturnType<typeof setInterval> | null>(null)
  const timeoutTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Clean up timers on unmount
  useEffect(() => {
    return () => {
      if (pollTimer.current) clearInterval(pollTimer.current)
      if (timeoutTimer.current) clearTimeout(timeoutTimer.current)
    }
  }, [])

  const stopPolling = useCallback(() => {
    if (pollTimer.current) {
      clearInterval(pollTimer.current)
      pollTimer.current = null
    }
    if (timeoutTimer.current) {
      clearTimeout(timeoutTimer.current)
      timeoutTimer.current = null
    }
  }, [])

  async function startOAuth() {
    setPhase('opening')
    setError(null)

    // Step 1: get OAuth URL from daemon
    const [oauthUrl, startErr] = await safeInvoke<string>('cascade_providers_oauth_start', {
      id: entry.id,
    })
    if (startErr || !oauthUrl) {
      setPhase('error')
      setError(startErr ?? 'No OAuth URL returned.')
      return
    }

    // Step 2: open browser
    try {
      await shellOpen(oauthUrl)
    } catch (openErr) {
      setPhase('error')
      setError(`Could not open browser: ${String(openErr)}`)
      return
    }

    setPhase('waiting')

    // Step 3: poll for completion
    const started = Date.now()

    timeoutTimer.current = setTimeout(() => {
      stopPolling()
      setPhase('timeout')
      setError(
        `OAuth did not complete within ${OAUTH_TIMEOUT_MS / 1000} seconds. Please try again.`
      )
    }, OAUTH_TIMEOUT_MS)

    pollTimer.current = setInterval(async () => {
      const elapsed = Date.now() - started
      if (elapsed >= OAUTH_TIMEOUT_MS) return // timeout handler takes over

      const [status, pollErr] = await safeInvoke<string>('cascade_providers_oauth_status', {
        id: entry.id,
      })
      if (pollErr) {
        // Ignore transient poll errors; timeout will fire if it persists
        return
      }
      if (status === 'connected') {
        stopPolling()
        setPhase('success')
        setTimeout(onSuccess, 600)
      }
    }, OAUTH_POLL_INTERVAL_MS)
  }

  function handleBack() {
    stopPolling()
    onBack()
  }

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        Connect your {entry.name} account via OAuth. Your browser will open to authorise access.
      </p>

      {phase === 'waiting' && (
        <div
          role="status"
          aria-live="polite"
          className="flex items-center gap-2 rounded-md border border-border bg-muted/40 px-4 py-3 text-sm"
        >
          <Loader2
            className="h-4 w-4 animate-spin shrink-0 text-muted-foreground"
            aria-hidden="true"
          />
          Waiting for browser authorisation… (times out in {OAUTH_TIMEOUT_MS / 1000}s)
        </div>
      )}

      {phase === 'success' && (
        <div
          role="status"
          aria-live="polite"
          className="flex items-center gap-2 rounded-md border border-green-300 bg-green-50 dark:bg-green-900/20 dark:border-green-800 px-4 py-3 text-sm text-green-700 dark:text-green-400"
        >
          <Check className="h-4 w-4 shrink-0" aria-hidden="true" />
          Connected successfully!
        </div>
      )}

      {(phase === 'error' || phase === 'timeout') && error && (
        <p role="alert" className="flex items-center gap-1.5 text-xs text-destructive">
          <AlertCircle className="h-3 w-3 shrink-0" aria-hidden="true" />
          {error}
        </p>
      )}

      <div className="flex justify-between gap-2">
        <Button
          type="button"
          variant="ghost"
          size="sm"
          onClick={handleBack}
          disabled={phase === 'opening' || phase === 'waiting'}
        >
          <ChevronLeft className="h-4 w-4 mr-1" aria-hidden="true" />
          Back
        </Button>
        <Button
          type="button"
          onClick={startOAuth}
          disabled={phase === 'opening' || phase === 'waiting' || phase === 'success'}
        >
          {(phase === 'opening' || phase === 'waiting') && (
            <Loader2 className="h-4 w-4 mr-2 animate-spin" aria-hidden="true" />
          )}
          {phase === 'success' && <Check className="h-4 w-4 mr-2" aria-hidden="true" />}
          {phase === 'success'
            ? 'Connected!'
            : phase === 'opening'
              ? 'Opening browser…'
              : phase === 'waiting'
                ? 'Waiting…'
                : `Connect with ${entry.name}`}
        </Button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Step 2d — No-auth form (e.g. Ollama)
// ---------------------------------------------------------------------------

interface NoAuthFormProps {
  entry: ProviderCatalogEntry
  onSuccess: () => void
  onBack: () => void
}

function NoAuthForm({ entry, onSuccess, onBack }: NoAuthFormProps) {
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)

  async function handleConnect() {
    setSubmitting(true)
    setError(null)
    const [, err] = await safeInvoke<void>('cascade_providers_add_apikey', {
      id: entry.id,
      key: '',
    })
    setSubmitting(false)
    if (err) {
      setError(err)
      return
    }
    setSaved(true)
    setTimeout(onSuccess, 600)
  }

  return (
    <div className="space-y-4">
      <p className="text-sm text-muted-foreground">
        {entry.name} does not require authentication. Cascade will connect using default settings.
      </p>
      {error && (
        <p role="alert" className="flex items-center gap-1.5 text-xs text-destructive">
          <AlertCircle className="h-3 w-3 shrink-0" aria-hidden="true" />
          {error}
        </p>
      )}
      <div className="flex justify-between gap-2">
        <Button type="button" variant="ghost" size="sm" onClick={onBack} disabled={submitting}>
          <ChevronLeft className="h-4 w-4 mr-1" aria-hidden="true" />
          Back
        </Button>
        <Button type="button" onClick={handleConnect} disabled={submitting || saved}>
          {submitting && <Loader2 className="h-4 w-4 mr-2 animate-spin" aria-hidden="true" />}
          {saved && <Check className="h-4 w-4 mr-2" aria-hidden="true" />}
          {saved ? 'Connected!' : submitting ? 'Connecting…' : 'Connect'}
        </Button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main dialog
// ---------------------------------------------------------------------------

interface AddProviderDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Called after a provider is successfully added. Caller should refresh list. */
  onSuccess: () => void
}

type DialogStep = { kind: 'pick' } | { kind: 'auth'; entry: ProviderCatalogEntry }

/**
 * Two-step dialog for adding an AI provider.
 *
 * Step 1: provider picker (all PROVIDER_CATALOG entries listed).
 * Step 2: auth UI appropriate for the chosen provider:
 *   - api_key providers (non-generic): ApiKeyForm
 *   - generic: GenericEndpointForm
 *   - oauth: OAuthForm
 *   - none: NoAuthForm
 *
 * On success, calls onSuccess so the parent can refresh the providers list.
 *
 * @param open - Controlled open state
 * @param onOpenChange - Close handler
 * @param onSuccess - Refresh callback
 */
export function AddProviderDialog({ open, onOpenChange, onSuccess }: AddProviderDialogProps) {
  const [step, setStep] = useState<DialogStep>({ kind: 'pick' })

  // Reset to picker when dialog closes
  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) setStep({ kind: 'pick' })
    onOpenChange(nextOpen)
  }

  function handleProviderSelect(entry: ProviderCatalogEntry) {
    setStep({ kind: 'auth', entry })
  }

  function handleBack() {
    setStep({ kind: 'pick' })
  }

  function handleSuccess() {
    onSuccess()
    handleOpenChange(false)
  }

  const title = step.kind === 'pick' ? 'Add Provider' : `Connect ${step.entry.name}`
  const description =
    step.kind === 'pick'
      ? 'Select an AI provider to add to Cascade.'
      : `Set up your ${step.entry.name} connection.`

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>

        {step.kind === 'pick' && <ProviderPicker onSelect={handleProviderSelect} />}

        {step.kind === 'auth' &&
          step.entry.authMethod === 'api_key' &&
          step.entry.id !== 'generic' && (
            <ApiKeyForm entry={step.entry} onSuccess={handleSuccess} onBack={handleBack} />
          )}

        {step.kind === 'auth' && step.entry.id === 'generic' && (
          <GenericEndpointForm entry={step.entry} onSuccess={handleSuccess} onBack={handleBack} />
        )}

        {step.kind === 'auth' && step.entry.authMethod === 'oauth' && (
          <OAuthForm entry={step.entry} onSuccess={handleSuccess} onBack={handleBack} />
        )}

        {step.kind === 'auth' && step.entry.authMethod === 'none' && (
          <NoAuthForm entry={step.entry} onSuccess={handleSuccess} onBack={handleBack} />
        )}
      </DialogContent>
    </Dialog>
  )
}
