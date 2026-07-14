/**
 * ProjectSelector.tsx — Combobox project picker for PewsTreePanel.
 *
 * Purpose: Replace the free-text root input in PewsTreePanel with a combobox
 *   populated from the project registry. Keeps a manual-path toggle for custom
 *   project roots not in the registry.
 *
 * Inputs:
 *   value       — current path value (controlled).
 *   onChange    — called with the new absolute path when selection changes.
 *   projects    — list of RegistryProject entries from useProjectRegistry.
 *   loading     — true while the registry is loading.
 * Outputs: Combobox (select) + optional manual-path text input.
 * Constraints:
 *   - "manual" toggle shows the free-text input (original behaviour preserved).
 *   - When registry is loading, shows a disabled skeleton.
 *   - Accessible: labelled via aria-label; native <select> for keyboard/screen-reader.
 * SPORT: MASTER-COMPONENTS.md — ProjectSelector (pews-03)
 */

import { useState } from 'react'
import { Pencil } from 'lucide-react'
import type { RegistryProject } from './useProjectRegistry'

// ── Props ─────────────────────────────────────────────────────────────────────

export interface ProjectSelectorProps {
  /** Current project root path. */
  value: string
  /** Called with the new absolute path on selection/edit. */
  onChange: (path: string) => void
  /** Projects from the registry — drives the combobox options. */
  projects: RegistryProject[]
  /** True while registry fetch is in-flight. */
  loading: boolean
}

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * Purpose: Combobox + manual-path fallback for selecting a PEWS project root.
 * Inputs:  value, onChange, projects, loading.
 * Outputs: A <select> picker and optional free-text override input.
 */
export function ProjectSelector({ value, onChange, projects, loading }: ProjectSelectorProps) {
  // manual = true → show the free-text input instead of the combobox.
  const [manual, setManual] = useState(false)

  // Check if the current value matches a registry entry.
  const matchedId = projects.find((p) => p.path === value)?.id ?? ''

  function handleSelectChange(e: React.ChangeEvent<HTMLSelectElement>) {
    const selectedId = e.target.value
    const found = projects.find((p) => p.id === selectedId)
    if (found) onChange(found.path)
  }

  function toggleManual() {
    setManual((m) => !m)
  }

  if (manual) {
    return (
      <div className="flex items-center gap-1">
        <label htmlFor="pews-root-input" className="text-xs text-muted-foreground">
          Root:
        </label>
        <input
          id="pews-root-input"
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="Enter project root path…"
          className="w-56 rounded border border-border bg-card px-2 py-1 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
          aria-label="Project root path (manual)"
        />
        <button
          type="button"
          onClick={toggleManual}
          title="Switch to project picker"
          aria-label="Switch to project picker"
          className="rounded p-1 text-xs text-muted-foreground hover:bg-accent/50 hover:text-foreground"
        >
          ↩
        </button>
      </div>
    )
  }

  return (
    <div className="flex items-center gap-1">
      <label htmlFor="pews-project-select" className="shrink-0 text-xs text-muted-foreground">
        Project:
      </label>
      {loading ? (
        <div className="h-6 w-44 animate-pulse rounded border border-border bg-muted/30" />
      ) : (
        <select
          id="pews-project-select"
          value={matchedId}
          onChange={handleSelectChange}
          aria-label="Select PEWS project"
          className="rounded border border-border bg-card px-2 py-1 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
        >
          {!matchedId && (
            <option value="" disabled>
              — pick a project —
            </option>
          )}
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
      )}
      {/* Manual fallback toggle */}
      <button
        type="button"
        onClick={toggleManual}
        title="Enter a custom project path"
        aria-label="Enter a custom project path"
        className="rounded p-1 text-muted-foreground hover:bg-accent/50 hover:text-foreground"
      >
        <Pencil className="h-3 w-3" aria-hidden="true" />
      </button>
    </div>
  )
}
