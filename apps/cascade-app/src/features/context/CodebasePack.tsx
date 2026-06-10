/**
 * Purpose: "Pack Codebase" section of the ContextPanel — directory picker,
 *   exclude patterns, pack trigger, and bundle output display (path + copy buttons).
 * Inputs:  None (self-contained; calls pack_codebase_cmd IPC and Tauri dialog).
 * Outputs: Section with dir-picker button, excludes textarea, Pack button,
 *   and result display (output path, Copy path, Copy bundle buttons).
 * Constraints:
 *   - Tauri dialog.open({ directory: true }) for dir picking.
 *   - pack_codebase_cmd IPC returns String (absolute path to the generated bundle).
 *   - Copy bundle: read file via Tauri fs readTextFile → clipboard.
 *   - Large file read is async; spinner shows during both pack + copy operations.
 *   - Error state shows a visible Alert with error message.
 * SPORT: MASTER-COMPONENTS.md — CodebasePack (T-P3-E07-11)
 */

import { useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { open as dialogOpen } from '@tauri-apps/plugin-dialog'
import { readTextFile } from '@tauri-apps/plugin-fs'
import { AlertCircle, CheckCircle2, FolderOpen, Loader2, Package } from 'lucide-react'

// ── helpers ───────────────────────────────────────────────────────────────────

/** Parse exclude patterns textarea: one glob per line, skip blanks. */
export function parseExcludes(text: string): string[] {
  return text
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
}

/** Format byte size as human-readable string. */
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

// ── component ─────────────────────────────────────────────────────────────────

/**
 * Pack Codebase section — embedded at the bottom of ContextPanel.
 * Calls the pack_codebase_cmd Tauri command which returns the output file path (string).
 */
export function CodebasePack() {
  const [rootPath, setRootPath] = useState<string>('')
  const [excludesText, setExcludesText] = useState<string>('node_modules/\n*.log\ndist/\n.git/')
  const [packing, setPacking] = useState(false)
  const [copying, setCopying] = useState(false)
  // outputPath is the bundle file path returned by the Rust command.
  const [outputPath, setOutputPath] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  // ── select directory ─────────────────────────────────────────────────────
  async function handleSelectDir() {
    try {
      const selected = await dialogOpen({ directory: true, multiple: false })
      if (selected && typeof selected === 'string') {
        setRootPath(selected)
        setOutputPath(null)
        setError(null)
      }
    } catch {
      // User cancelled — no-op.
    }
  }

  // ── pack ─────────────────────────────────────────────────────────────────
  async function handlePack() {
    if (!rootPath) return
    setPacking(true)
    setOutputPath(null)
    setError(null)

    const excludes = parseExcludes(excludesText)

    try {
      // pack_codebase_cmd returns a String (absolute path to the bundle file).
      const path = await invoke<string>('pack_codebase_cmd', {
        root: rootPath,
        excludes,
      })
      setOutputPath(path)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setPacking(false)
    }
  }

  // ── copy path ────────────────────────────────────────────────────────────
  async function handleCopyPath() {
    if (!outputPath) return
    await navigator.clipboard.writeText(outputPath)
  }

  // ── copy bundle contents ─────────────────────────────────────────────────
  async function handleCopyBundle() {
    if (!outputPath) return
    setCopying(true)
    try {
      const contents = await readTextFile(outputPath)
      await navigator.clipboard.writeText(contents)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setCopying(false)
    }
  }

  return (
    <section aria-labelledby="pack-codebase-heading" className="flex flex-col gap-3 pt-2">
      <h3
        id="pack-codebase-heading"
        className="flex items-center gap-2 text-sm font-semibold text-foreground"
      >
        <Package className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        Pack Codebase
      </h3>

      {/* Directory picker */}
      <div className="flex gap-2">
        <button
          type="button"
          onClick={() => void handleSelectDir()}
          className="flex items-center gap-1.5 rounded-md border border-border bg-card px-3 py-1.5 text-xs text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground"
          aria-label="Select directory to pack"
        >
          <FolderOpen className="h-3 w-3" aria-hidden="true" />
          Select directory
        </button>
        {rootPath && (
          <code
            className="flex-1 truncate rounded-md bg-muted px-2 py-1.5 text-xs text-muted-foreground"
            title={rootPath}
          >
            {rootPath}
          </code>
        )}
      </div>

      {/* Exclude patterns */}
      <div className="flex flex-col gap-1">
        <label htmlFor="pack-excludes" className="text-xs text-muted-foreground">
          Exclude patterns (one glob per line)
        </label>
        <textarea
          id="pack-excludes"
          value={excludesText}
          onChange={(e) => setExcludesText(e.target.value)}
          placeholder={'node_modules/\n*.log'}
          rows={4}
          className="w-full resize-none rounded-md border border-border bg-background px-3 py-2 font-mono text-xs text-foreground outline-none focus:ring-2 focus:ring-ring"
          aria-label="Exclude glob patterns"
        />
      </div>

      {/* Pack button */}
      <button
        type="button"
        onClick={() => void handlePack()}
        disabled={!rootPath || packing}
        aria-busy={packing}
        className="flex items-center justify-center gap-2 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
      >
        {packing ? (
          <>
            <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" />
            Packing…
          </>
        ) : (
          'Pack'
        )}
      </button>

      {/* Error state */}
      {error && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          <span>{error}</span>
        </div>
      )}

      {/* Success state */}
      {outputPath && (
        <div className="flex flex-col gap-2 rounded-md border border-border bg-card p-3">
          <div className="flex items-center gap-2 text-sm text-foreground">
            <CheckCircle2 className="h-4 w-4 shrink-0 text-green-500" aria-hidden="true" />
            <span>Bundle ready</span>
          </div>

          <code
            className="truncate rounded bg-muted px-2 py-1 text-xs text-muted-foreground"
            title={outputPath}
          >
            {outputPath}
          </code>

          <div className="flex gap-2">
            <button
              type="button"
              onClick={() => void handleCopyPath()}
              className="rounded-md border border-border bg-background px-3 py-1.5 text-xs text-foreground transition-colors hover:bg-accent/50"
            >
              Copy path
            </button>
            <button
              type="button"
              onClick={() => void handleCopyBundle()}
              disabled={copying}
              aria-busy={copying}
              className="flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-xs text-foreground transition-colors hover:bg-accent/50 disabled:opacity-50"
            >
              {copying ? (
                <>
                  <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                  Copying…
                </>
              ) : (
                'Copy bundle'
              )}
            </button>
          </div>
        </div>
      )}
    </section>
  )
}
