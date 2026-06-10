/**
 * Purpose: Settings tab for MCP server definitions.
 *   DataTable with name/command/args/env columns; add/remove rows.
 *   Args: comma-separated string input (parsed to string[] on save).
 *   Env: key=value pairs textarea (parsed to Record<string,string> on save).
 * Inputs:  draft McpServerEntry[]; saveSection callback.
 * Outputs: <section> table; saves mcpServers[] via update_settings partial.
 * Constraints:
 *   - Args input is comma-separated; split on save (trimmed, filtered empty).
 *   - Env textarea is key=value per line; invalid lines are silently skipped.
 *   - WCAG 2.1 AA: table roles; all inputs labelled.
 * SPORT: MASTER-COMPONENTS.md — McpServersTab — T-P3-E07-16
 */

import { useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { McpServerEntry } from '@/types/settings'

// ── Env parsing helpers ───────────────────────────────────────────────────────

/**
 * Serializes a Record<string, string> to key=value lines for textarea display.
 */
export function envToText(env: Record<string, string>): string {
  return Object.entries(env)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}

/**
 * Parses key=value textarea text to a Record<string, string>.
 * Lines that do not contain '=' are silently skipped.
 */
export function textToEnv(text: string): Record<string, string> {
  const result: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const eqIdx = line.indexOf('=')
    if (eqIdx === -1) continue
    const key = line.slice(0, eqIdx).trim()
    const val = line.slice(eqIdx + 1)
    if (key) result[key] = val
  }
  return result
}

/**
 * Serializes a string[] to comma-separated display.
 */
export function argsToText(args: string[]): string {
  return args.join(', ')
}

/**
 * Parses comma-separated string to string[] (trimmed, empty filtered).
 */
export function textToArgs(text: string): string[] {
  return text
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
}

// ── McpServersTab ─────────────────────────────────────────────────────────────

export interface McpServersTabProps {
  draft: McpServerEntry[]
  isSaving: boolean
  error: string | null
  onUpdate: (val: McpServerEntry[]) => void
  onSave: () => void
  onDiscard: () => void
}

export function McpServersTab({
  draft,
  isSaving,
  error,
  onUpdate,
  onSave,
  onDiscard,
}: McpServersTabProps) {
  // Local textarea state per-row index (args and env as display strings).
  const [argsText, setArgsText] = useState<Record<number, string>>(() =>
    Object.fromEntries(draft.map((srv, i) => [i, argsToText(srv.args)])),
  )
  const [envText, setEnvText] = useState<Record<number, string>>(() =>
    Object.fromEntries(draft.map((srv, i) => [i, envToText(srv.env)])),
  )

  function addRow() {
    const idx = draft.length
    onUpdate([...draft, { name: '', command: '', args: [], env: {}, enabled: true }])
    setArgsText((prev) => ({ ...prev, [idx]: '' }))
    setEnvText((prev) => ({ ...prev, [idx]: '' }))
  }

  function removeRow(idx: number) {
    onUpdate(draft.filter((_, i) => i !== idx))
    setArgsText((prev) => {
      const next = { ...prev }
      delete next[idx]
      return next
    })
    setEnvText((prev) => {
      const next = { ...prev }
      delete next[idx]
      return next
    })
  }

  function updateField<K extends 'name' | 'command' | 'enabled'>(
    idx: number,
    key: K,
    val: McpServerEntry[K],
  ) {
    const next = [...draft]
    next[idx] = { ...next[idx], [key]: val }
    onUpdate(next)
  }

  function commitArgs(idx: number, text: string) {
    setArgsText((prev) => ({ ...prev, [idx]: text }))
    const next = [...draft]
    next[idx] = { ...next[idx], args: textToArgs(text) }
    onUpdate(next)
  }

  function commitEnv(idx: number, text: string) {
    setEnvText((prev) => ({ ...prev, [idx]: text }))
    const next = [...draft]
    next[idx] = { ...next[idx], env: textToEnv(text) }
    onUpdate(next)
  }

  return (
    <section aria-labelledby="mcp-servers-tab-heading" className="space-y-4">
      <h2 id="mcp-servers-tab-heading" className="text-base font-semibold">
        MCP Servers
      </h2>
      <p className="text-sm text-muted-foreground">
        Define Model Context Protocol servers to launch alongside the daemon.
      </p>

      <div className="rounded-md border border-border overflow-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border bg-muted/50">
              <th scope="col" className="px-3 py-2 text-left font-medium w-32">
                Name
              </th>
              <th scope="col" className="px-3 py-2 text-left font-medium w-40">
                Command
              </th>
              <th scope="col" className="px-3 py-2 text-left font-medium w-40">
                Args (comma-sep)
              </th>
              <th scope="col" className="px-3 py-2 text-left font-medium">
                Env (key=value)
              </th>
              <th scope="col" className="px-3 py-2 text-center font-medium w-20">
                Enabled
              </th>
              <th scope="col" className="px-3 py-2 w-10">
                <span className="sr-only">Remove</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {draft.length === 0 && (
              <tr>
                <td colSpan={6} className="px-3 py-4 text-center text-muted-foreground text-sm">
                  No MCP servers configured. Click Add to define one.
                </td>
              </tr>
            )}
            {draft.map((srv, idx) => (
              <tr key={idx} className="border-b border-border last:border-0 align-top">
                <td className="px-3 py-2">
                  <Input
                    type="text"
                    value={srv.name}
                    placeholder="server-name"
                    aria-label={`Name for MCP server ${idx + 1}`}
                    onChange={(e) => updateField(idx, 'name', e.target.value)}
                    className="text-xs"
                  />
                </td>
                <td className="px-3 py-2">
                  <Input
                    type="text"
                    value={srv.command}
                    placeholder="/usr/bin/npx"
                    aria-label={`Command for MCP server ${idx + 1}`}
                    onChange={(e) => updateField(idx, 'command', e.target.value)}
                    className="text-xs font-mono"
                  />
                </td>
                <td className="px-3 py-2">
                  <Input
                    type="text"
                    value={argsText[idx] ?? argsToText(srv.args)}
                    placeholder="-y, @modelcontextprotocol/server-filesystem"
                    aria-label={`Args for MCP server ${idx + 1}`}
                    onChange={(e) => setArgsText((prev) => ({ ...prev, [idx]: e.target.value }))}
                    onBlur={(e) => commitArgs(idx, e.target.value)}
                    className="text-xs font-mono"
                  />
                </td>
                <td className="px-3 py-2">
                  <textarea
                    value={envText[idx] ?? envToText(srv.env)}
                    placeholder={'HOME=/root\nNODE_ENV=production'}
                    aria-label={`Env vars for MCP server ${idx + 1}`}
                    rows={3}
                    onChange={(e) => setEnvText((prev) => ({ ...prev, [idx]: e.target.value }))}
                    onBlur={(e) => commitEnv(idx, e.target.value)}
                    className="w-full rounded-md border border-input bg-transparent px-2 py-1 text-xs font-mono resize-y shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  />
                </td>
                <td className="px-3 py-2 text-center">
                  <button
                    type="button"
                    role="switch"
                    aria-checked={srv.enabled}
                    aria-label={`${srv.enabled ? 'Disable' : 'Enable'} MCP server ${idx + 1}`}
                    onClick={() => updateField(idx, 'enabled', !srv.enabled)}
                    className={[
                      'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors',
                      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
                      srv.enabled ? 'bg-primary' : 'bg-input',
                    ].join(' ')}
                  >
                    <span
                      className={[
                        'pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform',
                        srv.enabled ? 'translate-x-4' : 'translate-x-0',
                      ].join(' ')}
                    />
                    <span className="sr-only">{srv.enabled ? 'Enabled' : 'Disabled'}</span>
                  </button>
                </td>
                <td className="px-3 py-2">
                  <button
                    type="button"
                    aria-label={`Remove MCP server ${idx + 1}`}
                    onClick={() => removeRow(idx)}
                    className="flex items-center justify-center h-7 w-7 rounded-md text-muted-foreground hover:text-destructive hover:bg-destructive/10 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    <Trash2 className="h-4 w-4" aria-hidden="true" />
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <Button type="button" variant="outline" size="sm" onClick={addRow}>
        <Plus className="h-4 w-4 mr-1.5" aria-hidden="true" />
        Add server
      </Button>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save MCP Servers'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}
