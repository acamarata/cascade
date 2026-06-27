/**
 * AllProjectsBoard.tsx — Multi-project phase board for the PEWS hub.
 *
 * Purpose: Show one row per project from the rag-13 project registry (GET
 *   /api/projects from the cascade daemon). Each row displays the project name,
 *   lifecycle status pill, % done progress bar, open-findings count, and Plan /
 *   Open actions. Personal workspaces are excluded by useProjectRegistry.
 *
 * Inputs:  onOpenProject(path) — called when user clicks "Open" to switch the
 *   PEWS tree to that project root.
 * Outputs: Board table + loading / empty / error states.
 * Constraints:
 *   - Lifecycle status vocab from pews-01: planning | ready_to_build | building |
 *     qa | shipped | archived | null (normal / no active phase).
 *   - "Plan" deep-links to /chat?scope=projects&seed=<project-name>.
 *   - Exclude "Personal" workspaces (handled in useProjectRegistry).
 *   - Falls back gracefully when daemon is unreachable (shows error row).
 * SPORT: MASTER-COMPONENTS.md — AllProjectsBoard (pews-03)
 */

import { Loader2, RefreshCw, AlertCircle, ExternalLink, MessageSquare } from 'lucide-react'
import { useNavigate } from 'react-router-dom'
import { useProjectRegistry, type RegistryProject } from './useProjectRegistry'

// ── Lifecycle status pill ─────────────────────────────────────────────────────

/**
 * Map lifecycle status string to a Tailwind color class pair [text, bg].
 * Covers pews-01 vocab + null (normal mode).
 */
function lifecycleColor(status: string | null): { text: string; bg: string } {
  switch (status) {
    case 'planning':       return { text: 'text-violet-400', bg: 'bg-violet-400/15' }
    case 'ready_to_build': return { text: 'text-blue-400',   bg: 'bg-blue-400/15' }
    case 'building':       return { text: 'text-amber-400',  bg: 'bg-amber-400/15' }
    case 'qa':             return { text: 'text-cyan-400',   bg: 'bg-cyan-400/15' }
    case 'shipped':        return { text: 'text-green-400',  bg: 'bg-green-400/15' }
    case 'archived':       return { text: 'text-muted-foreground', bg: 'bg-muted/40' }
    default:               return { text: 'text-muted-foreground', bg: 'bg-muted/20' }
  }
}

function lifecycleLabel(status: string | null): string {
  if (!status) return 'normal'
  return status.replace(/_/g, ' ')
}

interface StatusPillProps {
  status: string | null
}

function StatusPill({ status }: StatusPillProps) {
  const { text, bg } = lifecycleColor(status)
  return (
    <span
      className={`inline-flex items-center rounded px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${text} ${bg}`}
      aria-label={`Status: ${lifecycleLabel(status)}`}
    >
      {lifecycleLabel(status)}
    </span>
  )
}

// ── Progress bar ──────────────────────────────────────────────────────────────

interface ProgressBarProps {
  pct: number
}

function ProgressBar({ pct }: ProgressBarProps) {
  const clamped = Math.max(0, Math.min(100, pct))
  return (
    <div
      className="h-1.5 w-20 rounded-full bg-border"
      role="progressbar"
      aria-valuenow={clamped}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={`${clamped.toFixed(0)}% done`}
    >
      <div
        className="h-full rounded-full bg-primary transition-all duration-300"
        style={{ width: `${clamped}%` }}
      />
    </div>
  )
}

// ── Project row ───────────────────────────────────────────────────────────────

interface ProjectRowProps {
  project: RegistryProject
  onOpen: (path: string) => void
}

function ProjectRow({ project, onOpen }: ProjectRowProps) {
  const navigate = useNavigate()

  function handlePlan() {
    const seed = encodeURIComponent(`Plan phase for project: ${project.name}`)
    void navigate(`/chat?scope=projects&seed=${seed}`)
  }

  function handleOpen() {
    onOpen(project.path)
  }

  return (
    <tr className="border-b border-border last:border-0 hover:bg-accent/20 transition-colors">
      {/* Project name */}
      <td className="px-4 py-2.5">
        <div className="flex flex-col gap-0.5">
          <span className="text-sm font-semibold text-foreground">{project.name}</span>
          {project.activePhaseId && (
            <span className="font-mono text-[9px] text-muted-foreground">
              {project.activePhaseId}
            </span>
          )}
        </div>
      </td>

      {/* Status pill */}
      <td className="px-4 py-2.5">
        <StatusPill status={project.phaseStatus} />
      </td>

      {/* % done */}
      <td className="px-4 py-2.5">
        <div className="flex items-center gap-2">
          <ProgressBar pct={project.pctDone} />
          <span className="text-[10px] text-muted-foreground tabular-nums">
            {project.pctDone > 0 ? `${project.pctDone.toFixed(0)}%` : '—'}
          </span>
        </div>
      </td>

      {/* Open findings */}
      <td className="px-4 py-2.5 text-center">
        {project.openFindings > 0 ? (
          <span className="rounded-full bg-destructive/15 px-2 py-0.5 text-[10px] font-medium text-destructive">
            {project.openFindings}
          </span>
        ) : (
          <span className="text-[10px] text-muted-foreground/40">—</span>
        )}
      </td>

      {/* Actions */}
      <td className="px-4 py-2.5">
        <div className="flex items-center gap-1.5">
          <button
            type="button"
            onClick={handlePlan}
            title="Open planning chat for this project"
            aria-label={`Plan ${project.name}`}
            className="flex items-center gap-1 rounded border border-border px-2 py-1 text-[10px] text-muted-foreground hover:border-primary/40 hover:bg-primary/10 hover:text-primary transition-colors"
          >
            <MessageSquare className="h-3 w-3" aria-hidden="true" />
            Plan
          </button>
          <button
            type="button"
            onClick={handleOpen}
            title="Open PEWS tree for this project"
            aria-label={`Open PEWS tree for ${project.name}`}
            className="flex items-center gap-1 rounded border border-border px-2 py-1 text-[10px] text-muted-foreground hover:border-border/80 hover:bg-accent/50 hover:text-foreground transition-colors"
          >
            <ExternalLink className="h-3 w-3" aria-hidden="true" />
            Open
          </button>
        </div>
      </td>
    </tr>
  )
}

// ── Board ─────────────────────────────────────────────────────────────────────

export interface AllProjectsBoardProps {
  /**
   * Called when the user clicks "Open" on a project row.
   * The parent switches to the tree view with this project root selected.
   */
  onOpenProject: (projectPath: string) => void
}

/**
 * Purpose: Board listing all cascade-managed projects (excluding Personal) with
 *   their lifecycle status, progress, findings count, and quick actions.
 * Inputs:  onOpenProject callback.
 * Outputs: Table of project rows with loading/empty/error states.
 */
export function AllProjectsBoard({ onOpenProject }: AllProjectsBoardProps) {
  const { projects, loading, error, refetch } = useProjectRegistry()

  return (
    <div className="flex h-full flex-col overflow-hidden" data-testid="all-projects-board">
      {/* Header */}
      <header className="flex shrink-0 items-center gap-3 border-b border-border bg-card px-4 py-2.5">
        <h2 className="text-sm font-semibold text-foreground">All Projects</h2>
        <span className="text-[10px] text-muted-foreground">
          {loading ? 'loading…' : `${projects.length} project${projects.length !== 1 ? 's' : ''}`}
        </span>
        <div className="ml-auto">
          <button
            type="button"
            onClick={refetch}
            aria-label="Refresh projects"
            className="rounded p-1 text-muted-foreground hover:bg-accent/50 hover:text-foreground"
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </div>
      </header>

      {/* Body */}
      <div className="flex-1 overflow-auto">
        {/* Loading */}
        {loading && (
          <div
            className="flex h-32 items-center justify-center gap-2"
            role="status"
            aria-live="polite"
          >
            <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" aria-hidden="true" />
            <span className="text-xs text-muted-foreground">Loading projects…</span>
          </div>
        )}

        {/* Error */}
        {!loading && error && (
          <div className="flex h-32 flex-col items-center justify-center gap-2" role="alert">
            <AlertCircle className="h-5 w-5 text-destructive" aria-hidden="true" />
            <p className="text-xs text-destructive">Failed to load projects</p>
            <p className="max-w-xs text-center text-[10px] text-muted-foreground">{error}</p>
            <button
              type="button"
              onClick={refetch}
              className="rounded border border-border px-3 py-1 text-xs hover:bg-accent/50"
            >
              Retry
            </button>
          </div>
        )}

        {/* Empty */}
        {!loading && !error && projects.length === 0 && (
          <div className="flex h-32 flex-col items-center justify-center gap-2">
            <p className="text-sm text-muted-foreground">No projects found</p>
            <p className="text-[10px] text-muted-foreground/60">
              Projects under ~/Sites/ with .claude/CLAUDE.md appear here.
            </p>
          </div>
        )}

        {/* Board table */}
        {!loading && !error && projects.length > 0 && (
          <table className="w-full text-left" aria-label="All projects board">
            <thead>
              <tr className="border-b border-border bg-muted/30">
                <th className="px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Project
                </th>
                <th className="px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Status
                </th>
                <th className="px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Progress
                </th>
                <th className="px-4 py-2 text-center text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Findings
                </th>
                <th className="px-4 py-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Actions
                </th>
              </tr>
            </thead>
            <tbody>
              {projects.map((project) => (
                <ProjectRow
                  key={project.id}
                  project={project}
                  onOpen={onOpenProject}
                />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
