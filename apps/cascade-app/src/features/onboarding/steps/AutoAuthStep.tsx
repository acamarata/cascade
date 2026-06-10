/**
 * Purpose: Auto-auth import step for the onboarding wizard.
 *   Scans installed AI harnesses (Claude Code, OpenCode, Codex, Cursor) and
 *   environment variables for existing authentication state. Presents discovered
 *   accounts grouped by source with import checkboxes. Importable env API keys
 *   can be copied into cascade-keychain; non-importable OAuth accounts are
 *   offered an "Connect via OAuth" link for the E-04 provider connect flow.
 *
 * Inputs:
 *   - Tauri IPC: cascade_auto_auth_scan → DiscoveredAccount[]
 *   - Tauri IPC: cascade_auto_auth_import(selected: DiscoveredAccount[]) → ImportResult
 *   - WizardContext.goNext() to advance after import
 *
 * Outputs:
 *   - Renders grouped source cards with per-account checkboxes
 *   - "Import Selected" button triggers keychain write for env API keys
 *   - Empty state when no harnesses detected ("none found — connect manually")
 *   - Per-account import result feedback (success / error / skipped)
 *
 * Constraints:
 *   - AI-optional: this step runs entirely before any provider is connected.
 *   - Read-only scan: no harness config file is modified.
 *   - API key values are never displayed or logged.
 *   - Non-importable accounts (OAuth) show "Connect via OAuth →" not a checkbox.
 *   - Renders correctly on empty scan result.
 *
 * SPORT: MASTER-COMPONENTS.md — AutoAuthStep — T-P3-E03-41
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { AlertCircle, CheckCircle2, Loader2, Link2, KeyRound, User } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useWizard } from '@/features/onboarding/WizardContext'

// ---------------------------------------------------------------------------
// Types (mirror cascade_types::auto_auth via Tauri IPC camelCase serialization)
// ---------------------------------------------------------------------------

type AuthSource = 'claudeCode' | 'openCode' | 'codex' | 'cursor' | 'envVar'
type AuthType = 'oAuthToken' | 'apiKey' | 'envApiKey'

interface DiscoveredAccount {
  source: AuthSource
  emailOrHint: string
  provider: string
  authType: AuthType
  importable: boolean
}

interface ImportResult {
  imported: string[]
  skipped: string[]
  errors: string[]
}

type ScanStatus = 'idle' | 'scanning' | 'done' | 'error'
type ImportStatus = 'idle' | 'importing' | 'done' | 'error'

// ---------------------------------------------------------------------------
// Source metadata (labels + icons)
// ---------------------------------------------------------------------------

const SOURCE_LABELS: Record<AuthSource, string> = {
  claudeCode: 'Claude Code',
  openCode: 'OpenCode',
  codex: 'Codex',
  cursor: 'Cursor',
  envVar: 'Environment Variables',
}

const PROVIDER_LABELS: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  google: 'Google',
  gemini: 'Gemini',
}

// ---------------------------------------------------------------------------
// Helper components
// ---------------------------------------------------------------------------

function Alert({
  children,
  className,
  variant = 'default',
}: {
  children: React.ReactNode
  className?: string
  variant?: 'default' | 'destructive' | 'success'
}) {
  return (
    <div
      role="alert"
      className={cn(
        'flex items-start gap-3 rounded-md border p-4 text-sm',
        variant === 'destructive' && 'border-destructive/50 bg-destructive/10 text-destructive',
        variant === 'success' &&
          'border-green-500/50 bg-green-500/10 text-green-700 dark:text-green-400',
        variant === 'default' && 'border-muted bg-muted/30 text-foreground',
        className
      )}
    >
      {children}
    </div>
  )
}

function ProviderBadge({ provider }: { provider: string }) {
  return (
    <span className="inline-flex items-center rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary">
      {PROVIDER_LABELS[provider] ?? provider}
    </span>
  )
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

interface AutoAuthStepProps {
  /** Optional className for outer container */
  className?: string
}

export function AutoAuthStep({ className }: AutoAuthStepProps) {
  const { goNext } = useWizard()

  const [scanStatus, setScanStatus] = useState<ScanStatus>('idle')
  const [scanError, setScanError] = useState<string | null>(null)
  const [accounts, setAccounts] = useState<DiscoveredAccount[]>([])

  // Selected accounts for import (keyed by emailOrHint)
  const [selected, setSelected] = useState<Set<string>>(new Set())

  const [importStatus, setImportStatus] = useState<ImportStatus>('idle')
  const [importResult, setImportResult] = useState<ImportResult | null>(null)

  // ---------------------------------------------------------------------------
  // Scan on mount
  // ---------------------------------------------------------------------------

  useEffect(() => {
    setScanStatus('scanning')
    setScanError(null)
    invoke<DiscoveredAccount[]>('cascade_auto_auth_scan')
      .then((discovered) => {
        setAccounts(discovered)
        // Pre-select all importable accounts
        const importable = new Set(discovered.filter((a) => a.importable).map((a) => a.emailOrHint))
        setSelected(importable)
        setScanStatus('done')
      })
      .catch((err: unknown) => {
        setScanError(String(err))
        setScanStatus('error')
      })
  }, [])

  // ---------------------------------------------------------------------------
  // Checkbox toggle
  // ---------------------------------------------------------------------------

  const toggleAccount = useCallback((hint: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(hint)) {
        next.delete(hint)
      } else {
        next.add(hint)
      }
      return next
    })
  }, [])

  // ---------------------------------------------------------------------------
  // Import handler
  // ---------------------------------------------------------------------------

  const handleImport = useCallback(async () => {
    const toImport = accounts.filter((a) => a.importable && selected.has(a.emailOrHint))
    if (toImport.length === 0) {
      // Nothing selected — skip straight to next step
      goNext()
      return
    }

    setImportStatus('importing')
    setImportResult(null)

    try {
      const result = await invoke<ImportResult>('cascade_auto_auth_import', {
        selected: toImport,
      })
      setImportResult(result)
      setImportStatus('done')
    } catch (err: unknown) {
      setImportResult({
        imported: [],
        skipped: [],
        errors: [String(err)],
      })
      setImportStatus('error')
    }
  }, [accounts, selected, goNext])

  // ---------------------------------------------------------------------------
  // Group accounts by source
  // ---------------------------------------------------------------------------

  const grouped = accounts.reduce<Record<AuthSource, DiscoveredAccount[]>>(
    (acc, a) => {
      if (!acc[a.source]) acc[a.source] = []
      acc[a.source].push(a)
      return acc
    },
    {} as Record<AuthSource, DiscoveredAccount[]>
  )

  const sourceOrder: AuthSource[] = ['claudeCode', 'openCode', 'codex', 'cursor', 'envVar']
  const orderedGroups = sourceOrder.filter((s) => grouped[s]?.length > 0)

  const importableCount = accounts.filter((a) => a.importable && selected.has(a.emailOrHint)).length

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className={cn('flex h-full flex-col gap-6 overflow-y-auto px-8 py-10', className)}>
      {/* Header */}
      <div className="flex flex-col gap-2">
        <h2 className="text-2xl font-bold text-foreground">Connect existing accounts</h2>
        <p className="text-sm text-muted-foreground">
          Cascade found the following AI tools on your machine. Select accounts to import into
          Cascade&apos;s secure keychain, or connect new accounts via OAuth.
        </p>
      </div>

      {/* Scanning state */}
      {scanStatus === 'scanning' && (
        <div className="flex items-center gap-3 py-8 text-muted-foreground">
          <Loader2 className="h-5 w-5 animate-spin" aria-hidden="true" />
          <span className="text-sm">Scanning for installed AI tools&hellip;</span>
        </div>
      )}

      {/* Scan error */}
      {scanStatus === 'error' && scanError && (
        <Alert variant="destructive">
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          <div>
            <p className="font-medium">Scan failed</p>
            <p className="mt-1 text-xs opacity-80">{scanError}</p>
          </div>
        </Alert>
      )}

      {/* Empty state */}
      {scanStatus === 'done' && accounts.length === 0 && (
        <div className="flex flex-col items-center gap-4 rounded-lg border border-dashed border-muted-foreground/30 py-12 text-center">
          <KeyRound className="h-10 w-10 text-muted-foreground/50" aria-hidden="true" />
          <div>
            <p className="font-medium text-foreground">No harnesses detected</p>
            <p className="mt-1 text-sm text-muted-foreground">
              No existing AI tool accounts were found on this machine. Connect a provider manually
              to get started.
            </p>
          </div>
        </div>
      )}

      {/* Account cards grouped by source */}
      {scanStatus === 'done' && orderedGroups.length > 0 && (
        <div className="flex flex-col gap-4">
          {orderedGroups.map((source) => (
            <SourceCard
              key={source}
              source={source}
              accounts={grouped[source]}
              selected={selected}
              onToggle={toggleAccount}
              importResult={importResult}
            />
          ))}
        </div>
      )}

      {/* Import result feedback */}
      {importResult && importStatus === 'done' && <ImportResultAlert result={importResult} />}

      {/* Action row */}
      {scanStatus === 'done' && (
        <div className="mt-auto flex items-center justify-between pt-4">
          <button
            type="button"
            onClick={goNext}
            className="text-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            Skip — connect manually
          </button>

          {importStatus === 'done' ? (
            <button
              type="button"
              onClick={goNext}
              className="inline-flex items-center gap-2 rounded-md bg-primary px-5 py-2 text-sm font-semibold text-primary-foreground shadow transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
            >
              <CheckCircle2 className="h-4 w-4" aria-hidden="true" />
              Continue
            </button>
          ) : (
            <button
              type="button"
              onClick={handleImport}
              disabled={importStatus === 'importing'}
              className="inline-flex items-center gap-2 rounded-md bg-primary px-5 py-2 text-sm font-semibold text-primary-foreground shadow transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50"
              aria-busy={importStatus === 'importing'}
            >
              {importStatus === 'importing' ? (
                <>
                  <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
                  Importing&hellip;
                </>
              ) : (
                <>
                  Import Selected
                  {importableCount > 0 && (
                    <span className="rounded-full bg-primary-foreground/20 px-1.5 py-0.5 text-xs">
                      {importableCount}
                    </span>
                  )}
                </>
              )}
            </button>
          )}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// SourceCard — one card per harness source
// ---------------------------------------------------------------------------

interface SourceCardProps {
  source: AuthSource
  accounts: DiscoveredAccount[]
  selected: Set<string>
  onToggle: (hint: string) => void
  importResult: ImportResult | null
}

function SourceCard({ source, accounts, selected, onToggle, importResult }: SourceCardProps) {
  return (
    <div className="rounded-lg border border-border bg-card shadow-sm">
      {/* Card header */}
      <div className="flex items-center gap-2 border-b border-border px-4 py-3">
        <User className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <span className="text-sm font-semibold text-foreground">{SOURCE_LABELS[source]}</span>
        <span className="ml-auto text-xs text-muted-foreground">
          {accounts.length} account{accounts.length !== 1 ? 's' : ''}
        </span>
      </div>

      {/* Account list */}
      <ul className="divide-y divide-border">
        {accounts.map((account) => (
          <AccountRow
            key={account.emailOrHint}
            account={account}
            isSelected={selected.has(account.emailOrHint)}
            onToggle={onToggle}
            importResult={importResult}
          />
        ))}
      </ul>
    </div>
  )
}

// ---------------------------------------------------------------------------
// AccountRow — one row per discovered account
// ---------------------------------------------------------------------------

interface AccountRowProps {
  account: DiscoveredAccount
  isSelected: boolean
  onToggle: (hint: string) => void
  importResult: ImportResult | null
}

function AccountRow({ account, isSelected, onToggle, importResult }: AccountRowProps) {
  // Determine per-row result state from ImportResult
  const wasImported = importResult?.imported.includes(account.emailOrHint) ?? false
  const wasSkipped = importResult?.skipped.includes(account.emailOrHint) ?? false
  const rowError = importResult?.errors.find((e) => e.includes(account.provider)) ?? null

  return (
    <li className="flex items-center gap-3 px-4 py-3">
      {/* Importable: checkbox | Non-importable: OAuth link */}
      {account.importable ? (
        <input
          type="checkbox"
          id={`account-${account.emailOrHint}`}
          checked={isSelected}
          onChange={() => onToggle(account.emailOrHint)}
          aria-label={`Import ${account.emailOrHint}`}
          className="h-4 w-4 rounded border-muted-foreground/40 accent-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          disabled={wasImported}
        />
      ) : (
        <div className="flex h-4 w-4 shrink-0 items-center justify-center">
          <Link2 className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
        </div>
      )}

      {/* Account info */}
      <label
        htmlFor={account.importable ? `account-${account.emailOrHint}` : undefined}
        className={cn('flex flex-1 flex-col gap-0.5', account.importable && 'cursor-pointer')}
      >
        <span className="text-sm font-medium text-foreground leading-tight">
          {account.emailOrHint}
        </span>
        <div className="flex items-center gap-2">
          <ProviderBadge provider={account.provider} />
          <span className="text-xs text-muted-foreground capitalize">
            {account.authType === 'envApiKey'
              ? 'Environment variable'
              : account.authType === 'oAuthToken'
                ? 'OAuth token (read-only)'
                : 'API key'}
          </span>
        </div>
      </label>

      {/* Right-side status / action */}
      <div className="ml-auto shrink-0">
        {wasImported && (
          <span className="flex items-center gap-1 text-xs text-green-600 dark:text-green-400">
            <CheckCircle2 className="h-3.5 w-3.5" aria-hidden="true" />
            Imported
          </span>
        )}
        {wasSkipped && <span className="text-xs text-muted-foreground">Skipped</span>}
        {rowError && !wasImported && (
          <span className="flex items-center gap-1 text-xs text-destructive">
            <AlertCircle className="h-3.5 w-3.5" aria-hidden="true" />
            Error
          </span>
        )}
        {/* Non-importable: OAuth link to E-04 connect flow */}
        {!account.importable && !wasImported && !wasSkipped && !rowError && (
          <span
            role="button"
            tabIndex={0}
            className="text-xs font-medium text-primary hover:underline focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            aria-label={`Connect ${account.emailOrHint} via OAuth`}
            // E-04 provider connect flow — linked when that route is scaffolded
            onClick={() => {
              /* TODO T-P3-E04-03: open provider connect flow for account.provider */
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                /* TODO T-P3-E04-03 */
              }
            }}
          >
            Connect via OAuth&nbsp;&rarr;
          </span>
        )}
      </div>
    </li>
  )
}

// ---------------------------------------------------------------------------
// ImportResultAlert — summary after import action
// ---------------------------------------------------------------------------

function ImportResultAlert({ result }: { result: ImportResult }) {
  const hasErrors = result.errors.length > 0
  const hasImported = result.imported.length > 0

  if (!hasErrors && !hasImported) return null

  return (
    <Alert variant={hasErrors ? 'destructive' : 'success'}>
      {hasErrors ? (
        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
      ) : (
        <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
      )}
      <div className="flex-1">
        {hasImported && (
          <p className="text-sm font-medium">
            {result.imported.length} account{result.imported.length !== 1 ? 's' : ''} imported
            successfully.
          </p>
        )}
        {hasErrors && (
          <>
            <p className="text-sm font-medium">
              {result.errors.length} error{result.errors.length !== 1 ? 's' : ''} during import:
            </p>
            <ul className="mt-1 list-disc pl-4 text-xs opacity-80">
              {result.errors.map((e, i) => (
                <li key={i}>{e}</li>
              ))}
            </ul>
          </>
        )}
      </div>
    </Alert>
  )
}
