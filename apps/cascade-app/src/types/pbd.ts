/**
 * pbd.ts — TypeScript types for the PBD PEWS tree and mutation commands.
 *
 * These types mirror the Rust structs in commands/pbd.rs (camelCase via serde).
 * Used by the PEWS mutation UI (features/pews/) and the live-sync hook.
 *
 * E-P8-05 | SPORT: MASTER-COMPONENTS.md — pbd TS types
 */

import { invoke } from '@tauri-apps/api/core'

// ---------------------------------------------------------------------------
// Status enums — mirroring cascade_core::pbd::schema
// ---------------------------------------------------------------------------

export type PhaseStatus = 'planning' | 'ready_to_build' | 'building' | 'qa' | 'shipped' | 'archived'
export type EpicStatus = 'planned' | 'active' | 'done' | 'archived'
export type WaveStatus = 'queued' | 'active' | 'qa' | 'done'
export type SprintStatus = 'queued' | 'active' | 'qa' | 'done'
export type TicketStatus = 'planned' | 'queue' | 'active' | 'review' | 'blocked' | 'done' | 'archived'

/** All valid status values for a given hierarchy level. */
export const TICKET_STATUSES: TicketStatus[] = [
  'planned', 'queue', 'active', 'review', 'blocked', 'done', 'archived',
]

// ---------------------------------------------------------------------------
// Temporal classification — derived from phase status
// ---------------------------------------------------------------------------

/** Temporal classification of a phase relative to the current build context. */
export type PhaseTemporal = 'past' | 'current' | 'future'

/** Classify a phase status into past/current/future. */
export function classifyPhase(
  phaseId: string,
  phaseStatus: string,
  currentPhaseId: string | null | undefined,
): PhaseTemporal {
  if (phaseId === currentPhaseId) return 'current'
  if (phaseStatus === 'shipped' || phaseStatus === 'archived') return 'past'
  if (phaseStatus === 'building' || phaseStatus === 'qa') return 'current'
  return 'future'
}

// ---------------------------------------------------------------------------
// Hierarchy view types — mirroring commands/pbd.rs wire types
// ---------------------------------------------------------------------------

export interface TicketView {
  id: string
  sprintId: string
  title: string
  status: TicketStatus
  dependsOn: string[]
  repo: string | null
  weight: string | null
  note: string | null
  blockedReason: string | null
}

export interface SprintView {
  id: string
  waveId: string
  title: string
  status: SprintStatus
  tickets: TicketView[]
  note: string | null
}

export interface WaveView {
  id: string
  epicId: string
  title: string
  status: WaveStatus
  sprints: SprintView[]
  note: string | null
}

export interface EpicView {
  id: string
  phaseId: string
  title: string
  status: EpicStatus
  dependsOn: string[]
  waves: WaveView[]
  note: string | null
}

export interface PhaseView {
  id: string
  title: string
  status: PhaseStatus
  epics: EpicView[]
  note: string | null
}

/** Full PEWS tree returned by pbd_get_tree. */
export interface PewsTree {
  phases: PhaseView[]
  currentPhase: string | null
  currentEpic: string | null
  currentWave: string | null
  currentSprint: string | null
  activeTickets: string[]
}

/** Active-pointers snapshot from current.yaml. */
export interface CurrentPointers {
  activePhase?: string
  activeEpic?: string
  activeWave?: string
  activeSprint?: string
  activeTickets: string[]
}

/** Result of a ticket status mutation. */
export interface TicketStatusUpdateResult {
  ticketId: string
  oldStatus: string
  newStatus: string
}

// ---------------------------------------------------------------------------
// Typed Tauri invoke wrappers
// ---------------------------------------------------------------------------

/**
 * Fetch the full PEWS tree for the given project root.
 * Returns all phases (past/current/future) with the full hierarchy.
 */
export function pbdGetTree(phaseRoot: string): Promise<PewsTree> {
  return invoke<PewsTree>('pbd_get_tree', { phaseRoot })
}

/**
 * Return the current.yaml active pointers for the sidebar display.
 */
export function pbdGetCurrent(phaseRoot?: string): Promise<CurrentPointers> {
  return invoke<CurrentPointers>('pbd_get_current', { phaseRoot: phaseRoot ?? null })
}

/**
 * Transition a ticket status on disk (writes YAML + event).
 * Call after an optimistic UI update to persist the change.
 */
export function pbdUpdateTicketStatus(
  phaseRoot: string,
  ticketId: string,
  status: TicketStatus,
  note?: string,
): Promise<TicketStatusUpdateResult> {
  return invoke<TicketStatusUpdateResult>('pbd_update_ticket_status', {
    phaseRoot,
    ticketId,
    status,
    note: note ?? null,
  })
}

/**
 * List tickets with optional status filter.
 */
export function pbdListTickets(
  phaseRoot: string,
  status?: TicketStatus,
): Promise<TicketView[]> {
  return invoke<TicketView[]>('pbd_list_tickets', {
    phaseRoot,
    status: status ?? null,
  })
}

/**
 * Install a file-system watcher on the phases/ directory.
 * The Tauri backend will emit "pbd://changed" events on any write.
 */
export function pbdWatch(phaseRoot: string): Promise<void> {
  return invoke<void>('pbd_watch', { phaseRoot })
}
