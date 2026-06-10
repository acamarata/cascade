/**
 * Purpose: Phase 3 — Legacy scan panel for the onboarding wizard.
 * Inputs:  WizardContext (useWizard), Tauri commands scan_global_homes / scan_dev_tree,
 *          optional Tauri dialog::open for folder picking.
 * Outputs: Runs global scan on mount; shows dev-tree path picker; renders ScanResultsTable.
 *          Calls updateState({ scanResult: derivedScanResult }) and
 *          markComplete(WizardStep.ScanLegacy) when both scans finish.
 * Constraints:
 *   - scan_global_homes and scan_dev_tree are Tauri command stubs (P3/E-03-10/11);
 *     they currently return empty arrays. UI must handle empty + populated + loading + error.
 *   - LegacyToolHome[] returned by both commands; merged by toolId for the summary table.
 *   - dev-tree path defaults to ~/Sites/ (common monorepo root on macOS/Linux).
 *   - Folder picker uses Tauri dialog::open with directory:true.
 *   - No destructive operations — read-only display only.
 *   - Next is semantically gated by calling markComplete; WizardLayout's global Next
 *     does not currently enforce this gate (tracked as a gap — see T-P3-E03-04).
 * SPORT: MASTER-COMPONENTS.md — ScanLegacyPhase — wizard step 3 — ✅
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { open as dialogOpen } from '@tauri-apps/plugin-dialog'
import { AlertCircle, FolderOpen, Loader2, RefreshCw, ScanLine } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { LegacyToolHome } from '@/lib/scanner/types'
import { useWizard } from '@/features/onboarding/WizardContext'
import { WizardStep } from '@/features/onboarding/types'
import { ScanResultsTable } from './ScanResultsTable'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Union of all scan states. */
type ScanStatus = 'idle' | 'scanning-global' | 'scanning-devtree' | 'done' | 'error'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Merge two LegacyToolHome arrays by toolId.
 * If a tool appears in both arrays its paths and counts are combined.
 */
function mergeHomes(a: LegacyToolHome[], b: LegacyToolHome[]): LegacyToolHome[] {
  const map = new Map<string, LegacyToolHome>()

  for (const home of [...a, ...b]) {
    const existing = map.get(home.toolId)
    if (existing) {
      map.set(home.toolId, {
        toolId: home.toolId,
        globalPaths: [...new Set([...existing.globalPaths, ...home.globalPaths])],
        perProjectPaths: [...new Set([...existing.perProjectPaths, ...home.perProjectPaths])],
        totalFiles: existing.totalFiles + home.totalFiles,
        totalSizeBytes: existing.totalSizeBytes + home.totalSizeBytes,
      })
    } else {
      map.set(home.toolId, { ...home })
    }
  }

  return Array.from(map.values()).sort((a, b) => a.toolId.localeCompare(b.toolId))
}

// ---------------------------------------------------------------------------
// Inline minimal alert (until @/components/ui/alert is scaffolded)
// ---------------------------------------------------------------------------

function Alert({
  children,
  className,
  variant,
}: {
  children: React.ReactNode
  className?: string
  variant?: 'default' | 'destructive'
}) {
  return (
    <div
      role="alert"
      className={cn(
        'flex items-start gap-3 rounded-md border p-4 text-sm',
        variant === 'destructive'
          ? 'border-destructive/50 bg-destructive/10 text-destructive'
          : 'border-border bg-muted/30 text-foreground',
        className
      )}
    >
      {children}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

interface ScanLegacyPhaseProps {
  /** Optional className forwarded to the outer container. */
  className?: string
}

/**
 * Phase 3 content panel: scans the filesystem for legacy AI tool configs.
 *
 * Flow:
 * 1. On mount: calls scan_global_homes() → shows results immediately.
 * 2. Dev-tree picker: text input defaults to ~/Sites/; folder dialog available.
 * 3. On "Scan dev tree": calls scan_dev_tree(root) → merges results into table.
 * 4. When both scans done: calls markComplete(ScanLegacy) + updateState({ scanResult }).
 */
export function ScanLegacyPhase({ className }: ScanLegacyPhaseProps) {
  const { updateState, markComplete } = useWizard()

  // Global scan state
  const [globalHomes, setGlobalHomes] = useState<LegacyToolHome[]>([])
  const [globalError, setGlobalError] = useState<string | null>(null)
  const [globalDone, setGlobalDone] = useState(false)

  // Dev tree scan state
  const [devRoot, setDevRoot] = useState<string>('~/Sites/')
  const [devTreeHomes, setDevTreeHomes] = useState<LegacyToolHome[]>([])
  const [devTreeError, setDevTreeError] = useState<string | null>(null)
  const [devTreeDone, setDevTreeDone] = useState(false)

  // Overall scan status (used for spinner logic)
  const [scanStatus, setScanStatus] = useState<ScanStatus>('idle')

  // Guard against double-firing the initial global scan
  const globalScanFired = useRef(false)

  // ---------------------------------------------------------------------------
  // Derived state
  // ---------------------------------------------------------------------------

  const mergedHomes = mergeHomes(globalHomes, devTreeHomes)
  const bothDone = globalDone && devTreeDone
  const isScanning = scanStatus === 'scanning-global' || scanStatus === 'scanning-devtree'

  // ---------------------------------------------------------------------------
  // Scan: global homes
  // ---------------------------------------------------------------------------

  const runGlobalScan = useCallback(async () => {
    setScanStatus('scanning-global')
    setGlobalError(null)
    setGlobalDone(false)
    try {
      const homes = await invoke<LegacyToolHome[]>('scan_global_homes', {
        toolIds: ['claude-code', 'opencode', 'codex', 'cursor', 'aider', 'windsurf', 'antigravity'],
      })
      setGlobalHomes(homes)
      setGlobalDone(true)
      setScanStatus('idle')
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setGlobalError(msg)
      setScanStatus('error')
    }
  }, [])

  // Run global scan automatically on mount (once)
  useEffect(() => {
    if (globalScanFired.current) return
    globalScanFired.current = true
    void runGlobalScan()
  }, [runGlobalScan])

  // ---------------------------------------------------------------------------
  // Scan: dev tree
  // ---------------------------------------------------------------------------

  const runDevTreeScan = useCallback(async (root: string) => {
    setScanStatus('scanning-devtree')
    setDevTreeError(null)
    setDevTreeDone(false)
    try {
      const homes = await invoke<LegacyToolHome[]>('scan_dev_tree', { root })
      setDevTreeHomes(homes)
      setDevTreeDone(true)
      setScanStatus('idle')
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      setDevTreeError(msg)
      setScanStatus('error')
    }
  }, [])

  // ---------------------------------------------------------------------------
  // Folder picker
  // ---------------------------------------------------------------------------

  const handleFolderPick = useCallback(async () => {
    try {
      const selected = await dialogOpen({
        directory: true,
        multiple: false,
        title: 'Select your development tree root',
        defaultPath: devRoot.replace('~', ''),
      })
      if (selected && typeof selected === 'string') {
        setDevRoot(selected)
      }
    } catch {
      // User cancelled or dialog unsupported — silently ignore
    }
  }, [devRoot])

  // ---------------------------------------------------------------------------
  // Mark complete when both scans finish
  // ---------------------------------------------------------------------------

  useEffect(() => {
    if (!bothDone) return

    // Derive a compact summary compatible with WizardState.scanResult
    const totalFiles = mergedHomes.reduce((acc, h) => acc + h.totalFiles, 0)
    const totalSizeBytes = mergedHomes.reduce((acc, h) => acc + h.totalSizeBytes, 0)

    updateState({
      scanResult: {
        legacyConfigPath: globalHomes[0]?.globalPaths[0] ?? '',
        legacyVaultPath: '',
        fileCount: totalFiles,
        totalSizeBytes,
      },
      // Hand detected tools to ToolModesPhase (T-13 contract): only tools with
      // at least one found path count as detected.
      detectedToolIds: mergedHomes
        .filter((h) => h.globalPaths.length > 0 || h.perProjectPaths.length > 0)
        .map((h) => h.toolId),
    })

    markComplete(WizardStep.ScanLegacy)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bothDone])

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className={cn('flex h-full flex-col gap-6 px-8 py-10', className)}>
      {/* Header */}
      <div>
        <h2 className="flex items-center gap-2 text-2xl font-semibold text-foreground">
          <ScanLine className="h-6 w-6 text-primary" aria-hidden="true" />
          Scan Legacy Configs
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Cascade will locate your existing AI tool instruction files. Nothing is modified at this
          step — this is a read-only scan.
        </p>
      </div>

      {/* Global scan status */}
      <section aria-label="Global scan status">
        <div className="mb-2 flex items-center justify-between">
          <p className="text-sm font-medium text-foreground">Global home directories</p>
          <button
            type="button"
            onClick={() => void runGlobalScan()}
            disabled={isScanning}
            className={cn(
              'flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium',
              'border border-border text-muted-foreground hover:bg-muted/50',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'disabled:cursor-not-allowed disabled:opacity-50',
              'transition-colors'
            )}
            aria-label="Re-scan global home directories"
          >
            <RefreshCw className="h-3 w-3" aria-hidden="true" />
            Re-scan
          </button>
        </div>

        {scanStatus === 'scanning-global' && (
          <div
            className="flex items-center gap-2 text-sm text-muted-foreground"
            role="status"
            aria-live="polite"
          >
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Scanning global home directories…
          </div>
        )}

        {globalError && scanStatus === 'error' && (
          <Alert variant="destructive" className="text-xs">
            <AlertCircle className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
            <div>
              <p className="font-medium">Global scan failed</p>
              <p className="mt-0.5 text-xs opacity-80">{globalError}</p>
            </div>
          </Alert>
        )}
      </section>

      {/* Dev tree picker */}
      <section aria-label="Dev tree root picker">
        <p className="mb-2 text-sm font-medium text-foreground">Development tree root</p>
        <p className="mb-3 text-xs text-muted-foreground">
          Select the root folder of your development tree. Cascade will scan all subdirectories for
          per-project AI tool configs.
        </p>
        <div className="flex items-center gap-2">
          <input
            type="text"
            value={devRoot}
            onChange={(e) => setDevRoot(e.target.value)}
            placeholder="~/Sites/"
            className={cn(
              'flex-1 rounded-md border border-input bg-background px-3 py-1.5 text-sm',
              'text-foreground placeholder:text-muted-foreground',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'disabled:cursor-not-allowed disabled:opacity-50'
            )}
            disabled={isScanning}
            aria-label="Dev tree root path"
          />
          <button
            type="button"
            onClick={() => void handleFolderPick()}
            disabled={isScanning}
            className={cn(
              'flex items-center gap-1.5 rounded-md border border-border px-3 py-1.5 text-xs font-medium',
              'text-muted-foreground hover:bg-muted/50',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'disabled:cursor-not-allowed disabled:opacity-50',
              'transition-colors'
            )}
            aria-label="Browse for folder"
          >
            <FolderOpen className="h-3.5 w-3.5" aria-hidden="true" />
            Browse
          </button>
          <button
            type="button"
            onClick={() => void runDevTreeScan(devRoot)}
            disabled={isScanning || !devRoot.trim()}
            className={cn(
              'flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium',
              'text-primary-foreground hover:bg-primary/90',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              'disabled:cursor-not-allowed disabled:opacity-50',
              'transition-colors'
            )}
            aria-label="Scan dev tree"
          >
            {scanStatus === 'scanning-devtree' ? (
              <>
                <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
                Scanning…
              </>
            ) : (
              <>
                <ScanLine className="h-3.5 w-3.5" aria-hidden="true" />
                Scan
              </>
            )}
          </button>
        </div>

        {devTreeError && (
          <Alert variant="destructive" className="mt-3 text-xs">
            <AlertCircle className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
            <div>
              <p className="font-medium">Dev tree scan failed</p>
              <p className="mt-0.5 text-xs opacity-80">{devTreeError}</p>
            </div>
          </Alert>
        )}

        {scanStatus === 'scanning-devtree' && (
          <div
            className="mt-3 flex items-center gap-2 text-sm text-muted-foreground"
            role="status"
            aria-live="polite"
          >
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Scanning dev tree — this may take a moment for large trees…
          </div>
        )}
      </section>

      {/* Results table */}
      {(globalDone || devTreeDone) && (
        <section aria-label="Scan results" className="flex-1">
          <p className="mb-2 text-sm font-medium text-foreground">
            Results
            {!bothDone && (
              <span className="ml-2 text-xs text-muted-foreground font-normal">
                (partial — dev tree scan pending)
              </span>
            )}
          </p>
          <ScanResultsTable homes={mergedHomes} />
        </section>
      )}

      {/* Waiting state before any scan completes */}
      {!globalDone && !devTreeDone && scanStatus !== 'scanning-global' && (
        <div
          className="flex flex-1 items-center justify-center text-sm text-muted-foreground"
          role="status"
          aria-live="polite"
        >
          {scanStatus === 'error' ? 'Scan failed. Use Re-scan to retry.' : 'Scan starting…'}
        </div>
      )}
    </div>
  )
}
