/**
 * Purpose: Vault note templates service — list template files from the vault's
 *   templates folder, and apply variable substitution to a template string.
 *   Templates are plain .md files; variables are {{date}}, {{time}}, {{title}}.
 * Inputs:  vaultRoot (string), templatesFolder (string, default "templates").
 * Outputs: Array of {name, path} for listTemplates; substituted string for applyTemplate.
 * Constraints: applyTemplate is a pure function (no side-effects, fully testable).
 *   listTemplates uses vault_list IPC and filters for .md files only.
 *   All variables default gracefully (unknown variables are left as-is).
 * SPORT: MASTER-COMPONENTS.md — templates service (T-P3-E06-10)
 */

import { invoke } from '@tauri-apps/api/core'
import type { VaultNode } from '../../types/vault'

// ── Types ────────────────────────────────────────────────────────────────────

export interface TemplateEntry {
  /** Display name derived from filename stem (without .md extension). */
  name: string
  /** Absolute path — pass to vault_read to get content. */
  path: string
}

export interface TemplateVars {
  /** ISO date string YYYY-MM-DD (local timezone). */
  date: string
  /** HH:MM time string (local timezone). */
  time: string
  /** User-supplied note title. */
  title: string
}

// ── Pure helpers ─────────────────────────────────────────────────────────────

/**
 * Apply {{date}}, {{time}}, and {{title}} variable substitution to a template string.
 * Unknown variables ({{foo}}) are left unchanged.
 *
 * @param templateContent - Raw template text with {{variable}} placeholders.
 * @param vars            - Values to substitute.
 * @returns Substituted string.
 */
export function applyTemplate(templateContent: string, vars: TemplateVars): string {
  return templateContent
    .replace(/\{\{date\}\}/g, vars.date)
    .replace(/\{\{time\}\}/g, vars.time)
    .replace(/\{\{title\}\}/g, vars.title)
}

/**
 * Build TemplateVars from the current local time and a caller-supplied title.
 *
 * @param title - The intended note title.
 * @param now   - Date override for testing; defaults to new Date().
 * @returns TemplateVars populated with local date/time and the given title.
 */
export function buildTemplateVars(title: string, now: Date = new Date()): TemplateVars {
  const yyyy = now.getFullYear()
  const mm = String(now.getMonth() + 1).padStart(2, '0')
  const dd = String(now.getDate()).padStart(2, '0')
  const hh = String(now.getHours()).padStart(2, '0')
  const min = String(now.getMinutes()).padStart(2, '0')
  return {
    date: `${yyyy}-${mm}-${dd}`,
    time: `${hh}:${min}`,
    title,
  }
}

// ── IPC helpers ──────────────────────────────────────────────────────────────

function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

// ── Main service functions ────────────────────────────────────────────────────

/**
 * List all .md template files in the vault templates folder.
 *
 * @param vaultRoot       - Vault root directory.
 * @param templatesFolder - Templates subdirectory (default: "templates").
 * @returns Array of TemplateEntry sorted by name ascending.
 */
export async function listTemplates(
  vaultRoot: string,
  templatesFolder: string = 'templates'
): Promise<TemplateEntry[]> {
  if (!isTauri()) {
    // Dev fixture — return empty list (no IPC in Vitest)
    return []
  }

  const root = vaultRoot.replace(/\/$/, '')
  const folder = templatesFolder.replace(/^\//, '').replace(/\/$/, '')
  const templatesPath = `${root}/${folder}`

  let node: VaultNode
  try {
    node = await invoke<VaultNode>('vault_list', { root: templatesPath })
  } catch {
    // Templates folder may not exist yet — return empty list gracefully
    return []
  }

  return node.children
    .filter((child) => !child.isDir && child.name.endsWith('.md'))
    .map((child) => ({
      name: child.name.replace(/\.md$/, ''),
      path: child.path,
    }))
    .sort((a, b) => a.name.localeCompare(b.name))
}

/**
 * Read a template file and apply variable substitution.
 *
 * @param templatePath - Absolute path to the .md template file.
 * @param vars         - Variables to substitute.
 * @returns Substituted note content string.
 */
export async function applyTemplateFromPath(
  templatePath: string,
  vars: TemplateVars
): Promise<string> {
  const content = await invoke<string>('vault_read', { path: templatePath })
  return applyTemplate(content, vars)
}
