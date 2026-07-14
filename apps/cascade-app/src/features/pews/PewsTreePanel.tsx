/**
 * PewsTreePanel.tsx — PEWS mutation UI: full Phase→Epic→Wave→Sprint→Ticket tree.
 *
 * Purpose: Visualize and edit the complete PEWS hierarchy across past/current/future
 *   phases. Status is color-coded. Ticket status is editable via a dropdown.
 *   Depends-on edges are displayed inline under each ticket.
 *
 * Inputs:  phaseRoot — absolute path to the project root (from project selector).
 * Outputs: Collapsible tree with status dropdowns, status legend, active-pointers
 *   sidebar, and depends_on display.
 *
 * States handled:
 *   - loading: spinner
 *   - empty (no phases): call-to-action message
 *   - error: alert with retry
 *   - loaded: full tree
 *
 * Constraints:
 *   - a11y: all interactive elements have aria-labels; status selects have labels.
 *   - No external graph library for the tree view — pure CSS/DOM collapsible tree.
 *   - Depends-on edges are displayed as inline text tags (not a canvas graph).
 *   - Responsive to collapsed sidebar.
 * SPORT: MASTER-COMPONENTS.md — PewsTreePanel (E-P8-05)
 */

import { useCallback, useEffect, useState } from 'react'
import { ChevronDown, ChevronRight, Loader2, Network, RefreshCw } from 'lucide-react'

import { homeDir } from '@tauri-apps/api/path'
import type {
  PhaseView,
  EpicView,
  WaveView,
  SprintView,
  TicketView,
  TicketStatus,
} from '../../types/pbd'
import { TICKET_STATUSES } from '../../types/pbd'
import { usePewsTree } from './usePewsTree'
import {
  classifyAllPhases,
  phaseStatusColor,
  ticketStatusColor,
  buildDependsOnEdges,
  ticketStatusCounts,
  TICKET_STATUS_COLORS,
} from './pewsTree'
import { ProjectSelector } from './ProjectSelector'
import { BuildProgressPanel } from './BuildProgressPanel'
import { useProjectRegistry } from './useProjectRegistry'

// ── Status legend ─────────────────────────────────────────────────────────────

const LEGEND_ITEMS = [
  { status: 'planned', label: 'Planned' },
  { status: 'queue', label: 'Queue' },
  { status: 'active', label: 'Active' },
  { status: 'review', label: 'Review' },
  { status: 'blocked', label: 'Blocked' },
  { status: 'done', label: 'Done' },
  { status: 'archived', label: 'Archived' },
] as const

function StatusLegend() {
  return (
    <div
      className="flex flex-wrap items-center gap-3 border-t border-border bg-background/80 px-4 py-2"
      aria-label="Ticket status legend"
      role="list"
    >
      {LEGEND_ITEMS.map(({ status, label }) => (
        <span
          key={status}
          className="flex items-center gap-1.5 text-[10px] text-muted-foreground"
          role="listitem"
        >
          <span
            className="h-2.5 w-2.5 rounded-sm"
            style={{ background: TICKET_STATUS_COLORS[status] }}
            aria-hidden="true"
          />
          {label}
        </span>
      ))}
    </div>
  )
}

// ── Ticket status select ──────────────────────────────────────────────────────

interface TicketStatusSelectProps {
  ticketId: string
  currentStatus: string
  onUpdate: (ticketId: string, newStatus: TicketStatus) => void
  disabled?: boolean
}

function TicketStatusSelect({
  ticketId,
  currentStatus,
  onUpdate,
  disabled,
}: TicketStatusSelectProps) {
  const color = ticketStatusColor(currentStatus)
  return (
    <select
      value={currentStatus}
      onChange={(e) => onUpdate(ticketId, e.target.value as TicketStatus)}
      disabled={disabled}
      aria-label={`Status for ticket ${ticketId}`}
      className="rounded border border-border bg-card px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-50"
      style={{ color }}
      onClick={(e) => e.stopPropagation()}
    >
      {TICKET_STATUSES.map((s) => (
        <option key={s} value={s} style={{ color: ticketStatusColor(s) }}>
          {s}
        </option>
      ))}
    </select>
  )
}

// ── Ticket row ────────────────────────────────────────────────────────────────

interface TicketRowProps {
  ticket: TicketView
  isActive: boolean
  dependsOnTitles: string[]
  onUpdateStatus: (ticketId: string, newStatus: TicketStatus) => void
  pendingUpdate: boolean
}

function TicketRow({
  ticket,
  isActive,
  dependsOnTitles,
  onUpdateStatus,
  pendingUpdate,
}: TicketRowProps) {
  const color = ticketStatusColor(ticket.status)
  return (
    <li
      className={[
        'ml-4 border-l border-border pl-3 py-1',
        isActive ? 'bg-accent/20 rounded-r' : '',
      ].join(' ')}
      aria-current={isActive ? 'true' : undefined}
    >
      <div className="flex items-start gap-2">
        {/* Status indicator dot */}
        <span
          className="mt-1.5 h-2 w-2 shrink-0 rounded-full"
          style={{ background: color }}
          aria-hidden="true"
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            {/* Ticket ID */}
            <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
              {ticket.id}
            </span>
            {/* Title */}
            <span className="truncate text-xs font-medium text-foreground">{ticket.title}</span>
            {/* Weight badge */}
            {ticket.weight && (
              <span className="shrink-0 rounded bg-border px-1 py-0.5 text-[9px] text-muted-foreground">
                {ticket.weight}
              </span>
            )}
            {/* Repo badge */}
            {ticket.repo && (
              <span className="shrink-0 rounded bg-border px-1 py-0.5 text-[9px] text-muted-foreground">
                {ticket.repo}
              </span>
            )}
            {/* Status dropdown */}
            <TicketStatusSelect
              ticketId={ticket.id}
              currentStatus={ticket.status}
              onUpdate={onUpdateStatus}
              disabled={pendingUpdate}
            />
          </div>
          {/* Blocked reason */}
          {ticket.blockedReason && (
            <p className="mt-0.5 text-[10px] text-destructive">{ticket.blockedReason}</p>
          )}
          {/* Note */}
          {ticket.note && <p className="mt-0.5 text-[10px] text-muted-foreground">{ticket.note}</p>}
          {/* Depends-on edges */}
          {dependsOnTitles.length > 0 && (
            <div className="mt-1 flex flex-wrap gap-1">
              <span className="text-[9px] text-muted-foreground">depends on:</span>
              {dependsOnTitles.map((title) => (
                <span
                  key={title}
                  className="rounded border border-border bg-card px-1 py-0.5 text-[9px] text-muted-foreground"
                >
                  {title}
                </span>
              ))}
            </div>
          )}
        </div>
      </div>
    </li>
  )
}

// ── Sprint section ────────────────────────────────────────────────────────────

interface SprintSectionProps {
  sprint: SprintView
  activeTickets: string[]
  dependsOnMap: Map<string, string[]>
  onUpdateStatus: (ticketId: string, newStatus: TicketStatus) => void
  pendingTicketId: string | null
  defaultOpen?: boolean
}

function SprintSection({
  sprint,
  activeTickets,
  dependsOnMap,
  onUpdateStatus,
  pendingTicketId,
  defaultOpen,
}: SprintSectionProps) {
  const [open, setOpen] = useState(defaultOpen ?? true)
  const color = ticketStatusColor(sprint.status)

  return (
    <div className="ml-4 border-l border-border pl-2">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 py-0.5 text-left text-[11px] font-medium text-muted-foreground hover:text-foreground"
      >
        {open ? (
          <ChevronDown className="h-3 w-3 shrink-0" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-3 w-3 shrink-0" aria-hidden="true" />
        )}
        <span
          className="h-1.5 w-1.5 shrink-0 rounded-full"
          style={{ background: color }}
          aria-hidden="true"
        />
        <span>{sprint.title}</span>
        <span className="ml-auto text-[9px] opacity-60">{sprint.id}</span>
        <span
          className="ml-1 rounded px-1 py-0.5 text-[9px] uppercase"
          style={{ color, background: `${color}22` }}
        >
          {sprint.status}
        </span>
      </button>

      {open && sprint.tickets.length > 0 && (
        <ul className="mt-0.5" aria-label={`Tickets in ${sprint.title}`}>
          {sprint.tickets.map((ticket) => (
            <TicketRow
              key={ticket.id}
              ticket={ticket}
              isActive={activeTickets.includes(ticket.id)}
              dependsOnTitles={dependsOnMap.get(ticket.id) ?? []}
              onUpdateStatus={onUpdateStatus}
              pendingUpdate={pendingTicketId === ticket.id}
            />
          ))}
        </ul>
      )}
      {open && sprint.tickets.length === 0 && (
        <p className="ml-4 py-1 text-[10px] text-muted-foreground/60">No tickets</p>
      )}
    </div>
  )
}

// ── Wave section ──────────────────────────────────────────────────────────────

interface WaveSectionProps {
  wave: WaveView
  activeTickets: string[]
  dependsOnMap: Map<string, string[]>
  onUpdateStatus: (ticketId: string, newStatus: TicketStatus) => void
  pendingTicketId: string | null
  defaultOpen?: boolean
}

function WaveSection({
  wave,
  activeTickets,
  dependsOnMap,
  onUpdateStatus,
  pendingTicketId,
  defaultOpen,
}: WaveSectionProps) {
  const [open, setOpen] = useState(defaultOpen ?? true)

  return (
    <div className="ml-3 border-l border-border pl-2">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 py-0.5 text-left text-[11px] font-semibold text-foreground/80 hover:text-foreground"
      >
        {open ? (
          <ChevronDown className="h-3 w-3 shrink-0" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-3 w-3 shrink-0" aria-hidden="true" />
        )}
        <span>Wave: {wave.title}</span>
        <span className="ml-auto text-[9px] text-muted-foreground">{wave.id}</span>
      </button>

      {open &&
        wave.sprints.map((sprint) => (
          <SprintSection
            key={sprint.id}
            sprint={sprint}
            activeTickets={activeTickets}
            dependsOnMap={dependsOnMap}
            onUpdateStatus={onUpdateStatus}
            pendingTicketId={pendingTicketId}
          />
        ))}
    </div>
  )
}

// ── Epic section ──────────────────────────────────────────────────────────────

interface EpicSectionProps {
  epic: EpicView
  activeTickets: string[]
  dependsOnMap: Map<string, string[]>
  onUpdateStatus: (ticketId: string, newStatus: TicketStatus) => void
  pendingTicketId: string | null
  defaultOpen?: boolean
}

function EpicSection({
  epic,
  activeTickets,
  dependsOnMap,
  onUpdateStatus,
  pendingTicketId,
  defaultOpen,
}: EpicSectionProps) {
  const [open, setOpen] = useState(defaultOpen ?? true)

  return (
    <div className="ml-2 border-l border-border pl-2">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 py-1 text-left text-xs font-bold text-foreground hover:text-foreground"
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        )}
        <span>{epic.title}</span>
        <span className="ml-auto text-[9px] text-muted-foreground">{epic.id}</span>
        <span className="ml-1 text-[9px] text-muted-foreground">{epic.status}</span>
      </button>

      {open &&
        epic.waves.map((wave) => (
          <WaveSection
            key={wave.id}
            wave={wave}
            activeTickets={activeTickets}
            dependsOnMap={dependsOnMap}
            onUpdateStatus={onUpdateStatus}
            pendingTicketId={pendingTicketId}
          />
        ))}
    </div>
  )
}

// ── Phase section ─────────────────────────────────────────────────────────────

interface PhaseSectionProps {
  phase: PhaseView
  temporal: 'past' | 'current' | 'future'
  activeTickets: string[]
  dependsOnMap: Map<string, string[]>
  onUpdateStatus: (ticketId: string, newStatus: TicketStatus) => void
  pendingTicketId: string | null
}

function PhaseSection({
  phase,
  temporal,
  activeTickets,
  dependsOnMap,
  onUpdateStatus,
  pendingTicketId,
}: PhaseSectionProps) {
  // Current phases start open; past/future start closed
  const [open, setOpen] = useState(temporal === 'current')
  const color = phaseStatusColor(phase.status)

  const temporalBadgeClass =
    temporal === 'past'
      ? 'text-muted-foreground bg-muted/40'
      : temporal === 'current'
        ? 'text-primary bg-primary/10'
        : 'text-violet-400 bg-violet-400/10'

  return (
    <section
      className={[
        'rounded-lg border border-border bg-card',
        temporal === 'current' ? 'border-primary/30' : '',
      ].join(' ')}
      aria-label={`Phase ${phase.id}: ${phase.title}`}
    >
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left hover:bg-accent/30"
      >
        {open ? (
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        )}
        <span
          className="h-3 w-3 shrink-0 rounded-full"
          style={{ background: color }}
          aria-hidden="true"
        />
        <span className="text-sm font-bold text-foreground">{phase.title}</span>
        <span className="ml-1 text-xs text-muted-foreground">{phase.id}</span>
        <span
          className={`ml-auto rounded px-1.5 py-0.5 text-[10px] font-medium uppercase ${temporalBadgeClass}`}
        >
          {temporal}
        </span>
        <span
          className="rounded px-1.5 py-0.5 text-[10px] font-medium uppercase"
          style={{ color, background: `${color}22` }}
        >
          {phase.status}
        </span>
      </button>

      {open && (
        <div className="px-2 pb-2">
          {phase.epics.length === 0 ? (
            <p className="py-2 pl-4 text-xs text-muted-foreground/60">No epics</p>
          ) : (
            phase.epics.map((epic) => (
              <EpicSection
                key={epic.id}
                epic={epic}
                activeTickets={activeTickets}
                dependsOnMap={dependsOnMap}
                onUpdateStatus={onUpdateStatus}
                pendingTicketId={pendingTicketId}
                defaultOpen={temporal === 'current'}
              />
            ))
          )}
        </div>
      )}
    </section>
  )
}

// ── Active pointers sidebar ───────────────────────────────────────────────────

interface ActivePointersSidebarProps {
  currentPhase: string | null
  currentEpic: string | null
  currentWave: string | null
  currentSprint: string | null
  activeTickets: string[]
}

function ActivePointersSidebar({
  currentPhase,
  currentEpic,
  currentWave,
  currentSprint,
  activeTickets,
}: ActivePointersSidebarProps) {
  const hasPointers = currentPhase || currentEpic || currentWave || currentSprint

  if (!hasPointers && activeTickets.length === 0) {
    return null
  }

  return (
    <aside
      className="w-48 shrink-0 overflow-auto border-l border-border bg-card px-3 py-3"
      aria-label="Active phase pointers"
    >
      <h2 className="mb-2 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
        Active Pointers
      </h2>
      <dl className="space-y-1.5 text-xs">
        {currentPhase && (
          <>
            <dt className="text-[9px] text-muted-foreground uppercase">Phase</dt>
            <dd className="truncate font-mono text-[10px] text-foreground">{currentPhase}</dd>
          </>
        )}
        {currentEpic && (
          <>
            <dt className="text-[9px] text-muted-foreground uppercase">Epic</dt>
            <dd className="truncate font-mono text-[10px] text-foreground">{currentEpic}</dd>
          </>
        )}
        {currentWave && (
          <>
            <dt className="text-[9px] text-muted-foreground uppercase">Wave</dt>
            <dd className="truncate font-mono text-[10px] text-foreground">{currentWave}</dd>
          </>
        )}
        {currentSprint && (
          <>
            <dt className="text-[9px] text-muted-foreground uppercase">Sprint</dt>
            <dd className="truncate font-mono text-[10px] text-foreground">{currentSprint}</dd>
          </>
        )}
        {activeTickets.length > 0 && (
          <>
            <dt className="mt-2 text-[9px] text-muted-foreground uppercase">
              Active Tickets ({activeTickets.length})
            </dt>
            {activeTickets.map((t) => (
              <dd key={t} className="truncate font-mono text-[10px] text-primary">
                {t}
              </dd>
            ))}
          </>
        )}
      </dl>
    </aside>
  )
}

// ── Main panel ────────────────────────────────────────────────────────────────

interface PewsTreePanelProps {
  /** Initial project root (overrideable via the in-panel selector). */
  initialPhaseRoot?: string
}

/**
 * Purpose: Full-height PEWS mutation panel — project selector, tree, sidebar,
 *   status legend. Wires usePewsTree for data and live sync.
 *
 * Inputs:  initialPhaseRoot — defaults to cascade repo root.
 * Outputs: Scrollable Phase→Epic→Wave→Sprint→Ticket tree with mutation dropdowns.
 */
export function PewsTreePanel({ initialPhaseRoot }: PewsTreePanelProps) {
  const [phaseRoot, setPhaseRoot] = useState(initialPhaseRoot ?? '')

  // Resolve the user's home directory at runtime — avoids hardcoding any path.
  useEffect(() => {
    if (initialPhaseRoot) return
    homeDir()
      .then((dir) => setPhaseRoot(dir))
      .catch(() => {
        // Not in Tauri context (e.g. Vitest) — leave as empty string.
      })
  }, [initialPhaseRoot])

  // Project registry — drives the ProjectSelector combobox.
  const { projects: registryProjects, loading: registryLoading } = useProjectRegistry()

  // Derive the project id from the current path for BuildProgressPanel.
  const activeProject = registryProjects.find((p) => p.path === phaseRoot)
  const isBuilding = activeProject?.phaseStatus === 'building'

  const { tree, loading, error, updateTicketStatus, refetch } = usePewsTree(phaseRoot)

  const [pendingTicketId, setPendingTicketId] = useState<string | null>(null)
  const [mutationError, setMutationError] = useState<string | null>(null)

  const handleUpdateStatus = useCallback(
    async (ticketId: string, newStatus: TicketStatus) => {
      setPendingTicketId(ticketId)
      setMutationError(null)
      try {
        await updateTicketStatus(ticketId, newStatus)
      } catch (err) {
        setMutationError(err instanceof Error ? err.message : String(err))
      } finally {
        setPendingTicketId(null)
      }
    },
    [updateTicketStatus]
  )

  // Derived data
  const temporalMap = tree ? classifyAllPhases(tree) : new Map()

  // depends_on map: ticketId → list of dependency titles
  const dependsOnMap = new Map<string, string[]>()
  if (tree) {
    const edges = buildDependsOnEdges(tree)
    for (const edge of edges) {
      const existing = dependsOnMap.get(edge.toId) ?? []
      existing.push(edge.fromTitle)
      dependsOnMap.set(edge.toId, existing)
    }
  }

  // Status counts for header
  const statusCounts = tree ? ticketStatusCounts(tree) : {}

  return (
    <div
      className="flex h-full flex-col overflow-hidden bg-background"
      data-testid="pews-tree-panel"
    >
      {/* Header */}
      <header className="flex shrink-0 flex-wrap items-center gap-3 border-b border-border bg-card px-4 py-2.5">
        <Network className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-sm font-semibold text-foreground">PEWS Phases</h1>

        {/* Status counts */}
        {Object.keys(statusCounts).length > 0 && (
          <div className="flex flex-wrap items-center gap-1.5" aria-label="Ticket counts by status">
            {Object.entries(statusCounts).map(([status, count]) => (
              <span
                key={status}
                className="rounded px-1.5 py-0.5 text-[9px] font-medium uppercase"
                style={{
                  color: ticketStatusColor(status),
                  background: `${ticketStatusColor(status)}22`,
                }}
              >
                {count} {status}
              </span>
            ))}
          </div>
        )}

        {/* Actions */}
        <div className="ml-auto flex items-center gap-2">
          {/* Project selector combobox (registry-driven; manual-path fallback inside) */}
          <ProjectSelector
            value={phaseRoot}
            onChange={setPhaseRoot}
            projects={registryProjects}
            loading={registryLoading}
          />

          {/* Refresh */}
          <button
            type="button"
            onClick={() => void refetch()}
            aria-label="Refresh PEWS tree"
            className="rounded p-1 text-muted-foreground hover:bg-accent/50 hover:text-foreground"
          >
            <RefreshCw className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </div>
      </header>

      {/* Mutation error banner */}
      {mutationError && (
        <div
          role="alert"
          className="shrink-0 border-b border-destructive/30 bg-destructive/10 px-4 py-1.5 text-xs text-destructive"
        >
          Mutation failed: {mutationError}
        </div>
      )}

      {/* Build progress panel — shown only when the active project is building */}
      {isBuilding && activeProject && (
        <BuildProgressPanel projectPath={phaseRoot} projectId={activeProject.id} />
      )}

      {/* Body */}
      <div className="flex min-h-0 flex-1 overflow-hidden">
        {/* Tree main area */}
        <main className="flex-1 overflow-auto p-4" id="main-content" tabIndex={-1}>
          {/* Loading */}
          {loading && (
            <div
              className="flex h-full items-center justify-center"
              role="status"
              aria-live="polite"
            >
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" />
              <span className="ml-2 text-sm text-muted-foreground">Loading PEWS tree...</span>
            </div>
          )}

          {/* Error */}
          {!loading && error && (
            <div className="flex h-full flex-col items-center justify-center gap-3" role="alert">
              <p className="text-sm text-destructive">Failed to load PEWS tree</p>
              <p className="max-w-sm text-center text-xs text-muted-foreground">{error}</p>
              <button
                type="button"
                onClick={() => void refetch()}
                className="rounded border border-border px-3 py-1.5 text-xs hover:bg-accent/50"
              >
                Retry
              </button>
            </div>
          )}

          {/* Empty */}
          {!loading && !error && tree && tree.phases.length === 0 && (
            <div className="flex h-full flex-col items-center justify-center gap-2">
              <Network className="h-8 w-8 text-muted-foreground/40" aria-hidden="true" />
              <p className="text-sm text-muted-foreground">No phases found</p>
              <p className="text-xs text-muted-foreground/60">
                Run <code className="font-mono">/plan</code> to create a phase.
              </p>
            </div>
          )}

          {/* Tree */}
          {!loading && !error && tree && tree.phases.length > 0 && (
            <div className="flex flex-col gap-3" role="tree" aria-label="PEWS phase tree">
              {tree.phases.map((phase) => (
                <PhaseSection
                  key={phase.id}
                  phase={phase}
                  temporal={temporalMap.get(phase.id) ?? 'future'}
                  activeTickets={tree.activeTickets}
                  dependsOnMap={dependsOnMap}
                  onUpdateStatus={handleUpdateStatus}
                  pendingTicketId={pendingTicketId}
                />
              ))}
            </div>
          )}
        </main>

        {/* Active pointers sidebar */}
        {tree && (
          <ActivePointersSidebar
            currentPhase={tree.currentPhase}
            currentEpic={tree.currentEpic}
            currentWave={tree.currentWave}
            currentSprint={tree.currentSprint}
            activeTickets={tree.activeTickets}
          />
        )}
      </div>

      {/* Status legend */}
      <StatusLegend />
    </div>
  )
}
