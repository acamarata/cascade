/**
 * Purpose: Daily notes service — create or open today's vault note at the
 *   configured daily-notes path (vaultRoot/dailyFolder/YYYY-MM-DD.md).
 *   If the note does not exist it is created from a template after variable
 *   substitution; if it already exists the file is opened without modification
 *   (idempotent).
 * Inputs:  vaultRoot (string), dailyFolder (string, default "daily"),
 *          templateContent (string | null — null uses DEFAULT_DAILY_TEMPLATE).
 * Outputs: Absolute path of the daily note as a string.
 * Constraints: Uses vault_write IPC to create; assumes vault_read (in VaultContext)
 *   is used externally to open the file once the path is returned.
 *   Date is derived from the user's LOCAL timezone (not UTC) — test in local TZ.
 *   Pure functions (buildDailyPath, applyDailyTemplate) have no side-effects.
 * SPORT: MASTER-COMPONENTS.md — dailyNotes service (T-P3-E06-10)
 */

import { invoke } from '@tauri-apps/api/core'

// ── Constants ────────────────────────────────────────────────────────────────

/** Default template written to a new daily note. */
export const DEFAULT_DAILY_TEMPLATE = `---
date: {{date}}
---

## Notes

`

// ── Pure helpers ─────────────────────────────────────────────────────────────

/**
 * Build the absolute path for today's daily note.
 *
 * @param vaultRoot  - Absolute vault root (e.g. "~/.cascade" or "/Users/x/.cascade").
 * @param dailyFolder - Subdirectory within the vault (default: "daily").
 * @param date        - Date to use; defaults to today (local timezone).
 * @returns Absolute path string, e.g. "~/.cascade/daily/2026-06-10.md".
 */
export function buildDailyPath(
  vaultRoot: string,
  dailyFolder: string,
  date: Date = new Date(),
): string {
  const yyyy = date.getFullYear()
  const mm = String(date.getMonth() + 1).padStart(2, '0')
  const dd = String(date.getDate()).padStart(2, '0')
  const filename = `${yyyy}-${mm}-${dd}.md`
  // Normalise trailing slash on root
  const root = vaultRoot.replace(/\/$/, '')
  const folder = dailyFolder.replace(/^\//, '').replace(/\/$/, '')
  return `${root}/${folder}/${filename}`
}

// ── IPC helpers ──────────────────────────────────────────────────────────────

function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

/**
 * Check whether a file exists in the vault by attempting a vault_read.
 * Returns true if vault_read succeeds, false on notFound error.
 * Rethrows all other errors (IO, traversalDenied).
 */
async function vaultFileExists(path: string): Promise<boolean> {
  try {
    await invoke('vault_read', { path })
    return true
  } catch (err) {
    // Tauri serialises VaultError as an object with a `kind` field.
    const kind = (err as { kind?: string })?.kind
    if (kind === 'notFound') return false
    throw err
  }
}

// ── Main service function ─────────────────────────────────────────────────────

/**
 * Open (or create) today's daily note.
 *
 * 1. Derives today's path via buildDailyPath.
 * 2. If the file does not exist: calls vault_write with substituted template content.
 * 3. Always returns the path so the caller (VaultContext.setCurrentFile) can open it.
 *
 * In non-Tauri (Vitest/Storybook) environments the IPC calls are skipped and
 * the path is returned directly for testing.
 *
 * @param vaultRoot       - Vault root directory.
 * @param dailyFolder     - Subdirectory for daily notes (default: "daily").
 * @param templateContent - Raw template string; uses DEFAULT_DAILY_TEMPLATE if null.
 * @param date            - Date override for testing; defaults to new Date().
 * @returns Absolute path of today's daily note.
 */
export async function openDailyNote(
  vaultRoot: string,
  dailyFolder: string = 'daily',
  templateContent: string | null = null,
  date: Date = new Date(),
): Promise<string> {
  const path = buildDailyPath(vaultRoot, dailyFolder, date)
  const template = templateContent ?? DEFAULT_DAILY_TEMPLATE

  if (!isTauri()) {
    // Dev/Vitest: skip IPC, return path for pure unit testing
    return path
  }

  const exists = await vaultFileExists(path)
  if (!exists) {
    const dateStr = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
    const content = template
      .replace(/\{\{date\}\}/g, dateStr)
      .replace(/\{\{time\}\}/g, new Date().toTimeString().slice(0, 5))
      .replace(/\{\{title\}\}/g, dateStr)
    await invoke('vault_write', { path, content })
  }

  return path
}
