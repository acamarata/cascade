/**
 * pewsTree.ts — Pure data helpers for the PEWS mutation UI.
 *
 * Purpose: stateless transforms and reducers over the PewsTree data structure.
 *   No React or Tauri imports — fully testable in Vitest.
 *
 * Responsibilities:
 *   - Status color mapping (all levels)
 *   - Temporal classification of phases
 *   - Optimistic mutation reducer (apply + reconcile)
 *   - depends_on edge model (all ticket pairs with dependencies)
 *   - Sync-event handler (merge new tree into existing state)
 *
 * Inputs:  PewsTree, TicketStatus, sync events from pbd://changed.
 * Outputs: Derived view data, updated PewsTree (immutable).
 * Constraints:
 *   - All functions are pure — no side-effects.
 *   - Tree mutations return a NEW PewsTree; never mutate in place.
 * SPORT: MASTER-COMPONENTS.md — pewsTree (E-P8-05)
 */

import type { PewsTree, PhaseView, TicketView, TicketStatus, PhaseTemporal } from '../../types/pbd'
import { classifyPhase } from '../../types/pbd'

// ── Status color maps ─────────────────────────────────────────────────────────

/** Visual color per ticket status — used for node coloring and badges. */
export const TICKET_STATUS_COLORS: Record<string, string> = {
  planned: '#6b7280', // gray-500
  queue: '#8b5cf6', // violet-500
  active: '#3b82f6', // blue-500
  review: '#f59e0b', // amber-500
  blocked: '#ef4444', // red-500
  done: '#22c55e', // green-500
  archived: '#374151', // gray-700
}

/** Visual color per phase status. */
export const PHASE_STATUS_COLORS: Record<string, string> = {
  planning: '#6b7280', // gray-500
  ready_to_build: '#8b5cf6', // violet-500
  building: '#3b82f6', // blue-500
  qa: '#f59e0b', // amber-500
  shipped: '#22c55e', // green-500
  archived: '#374151', // gray-700
}

/** Visual color per epic status. */
export const EPIC_STATUS_COLORS: Record<string, string> = {
  planned: '#6b7280',
  active: '#3b82f6',
  done: '#22c55e',
  archived: '#374151',
}

/** Get the display color for a ticket status string. Falls back to gray. */
export function ticketStatusColor(status: string): string {
  return TICKET_STATUS_COLORS[status] ?? '#6b7280'
}

/** Get the display color for a phase status string. Falls back to gray. */
export function phaseStatusColor(status: string): string {
  return PHASE_STATUS_COLORS[status] ?? '#6b7280'
}

// ── Temporal helpers ──────────────────────────────────────────────────────────

/**
 * Classify all phases in a tree as past / current / future.
 * Returns a Map<phaseId, PhaseTemporal>.
 */
export function classifyAllPhases(tree: PewsTree): Map<string, PhaseTemporal> {
  const result = new Map<string, PhaseTemporal>()
  for (const phase of tree.phases) {
    result.set(phase.id, classifyPhase(phase.id, phase.status, tree.currentPhase))
  }
  return result
}

// ── depends_on edge model ─────────────────────────────────────────────────────

/** A directed dependency edge between two tickets. */
export interface DependsOnEdge {
  /** The ticket that must complete first (the dependency). */
  fromId: string
  /** The ticket that depends on fromId. */
  toId: string
  /** fromId ticket title for display. */
  fromTitle: string
  /** toId ticket title for display. */
  toTitle: string
}

/**
 * Extract all depends_on edges from the full PEWS tree.
 * Returns edges as (dependency → dependent) pairs.
 *
 * If fromId is not found in the tree (e.g. cross-phase), fromTitle = fromId.
 */
export function buildDependsOnEdges(tree: PewsTree): DependsOnEdge[] {
  // Build id → title lookup for fast resolution
  const titleMap = new Map<string, string>()
  for (const phase of tree.phases) {
    for (const epic of phase.epics) {
      for (const wave of epic.waves) {
        for (const sprint of wave.sprints) {
          for (const ticket of sprint.tickets) {
            titleMap.set(ticket.id, ticket.title)
          }
        }
      }
    }
  }

  const edges: DependsOnEdge[] = []
  for (const phase of tree.phases) {
    for (const epic of phase.epics) {
      for (const wave of epic.waves) {
        for (const sprint of wave.sprints) {
          for (const ticket of sprint.tickets) {
            for (const dep of ticket.dependsOn) {
              edges.push({
                fromId: dep,
                toId: ticket.id,
                fromTitle: titleMap.get(dep) ?? dep,
                toTitle: ticket.title,
              })
            }
          }
        }
      }
    }
  }
  return edges
}

// ── Optimistic mutation reducer ───────────────────────────────────────────────

/** An optimistic ticket status change — applied before the IPC round-trip. */
export interface OptimisticStatusChange {
  ticketId: string
  newStatus: TicketStatus
  /** The status to roll back to on IPC error. */
  previousStatus: TicketStatus
}

/**
 * Apply an optimistic ticket status change to the tree.
 * Returns a new PewsTree with the ticket status updated.
 * Does NOT mutate the input tree.
 */
export function applyOptimisticStatusChange(
  tree: PewsTree,
  change: OptimisticStatusChange
): PewsTree {
  return {
    ...tree,
    phases: tree.phases.map((phase) => ({
      ...phase,
      epics: phase.epics.map((epic) => ({
        ...epic,
        waves: epic.waves.map((wave) => ({
          ...wave,
          sprints: wave.sprints.map((sprint) => ({
            ...sprint,
            tickets: sprint.tickets.map((ticket) =>
              ticket.id === change.ticketId ? { ...ticket, status: change.newStatus } : ticket
            ),
          })),
        })),
      })),
    })),
  }
}

/**
 * Reconcile the tree after an IPC call succeeds or fails.
 *
 * On success: returns the confirmed tree unchanged (the optimistic state
 *   is already correct — a re-fetch from pbd_get_tree is the source of truth).
 * On failure: rolls back the ticket to its previousStatus.
 *
 * In practice, consumers call `pbdGetTree()` on pbd://changed events, so
 * reconcile is only needed for explicit rollback on error.
 */
export function reconcileOnError(tree: PewsTree, change: OptimisticStatusChange): PewsTree {
  return applyOptimisticStatusChange(tree, {
    ticketId: change.ticketId,
    newStatus: change.previousStatus,
    previousStatus: change.newStatus,
  })
}

// ── Sync-event handler ────────────────────────────────────────────────────────

/**
 * Merge a freshly-fetched PewsTree into the existing state.
 *
 * Purpose: called when a pbd://changed event is received and a new tree has
 *   been fetched from the IPC. Preserves the UI's expand/collapse state by
 *   returning the new tree as-is (the UI layer manages expansion separately).
 *
 * This function exists as a pure seam so the sync handler can be tested
 * independently of React state updates.
 *
 * @param _current  The previous tree (reserved for future differential merge).
 * @param incoming  The freshly-fetched tree from pbd_get_tree.
 * @returns The incoming tree (canonical source of truth).
 */
export function mergeSyncEvent(_current: PewsTree, incoming: PewsTree): PewsTree {
  // Currently a straight replacement — the incoming tree IS the source of truth.
  // Future: preserve per-node UI state (e.g. expansion) while updating data fields.
  return incoming
}

// ── Flatten helpers ───────────────────────────────────────────────────────────

/** All tickets in the tree as a flat list (for list views and filtering). */
export function flattenTickets(tree: PewsTree): TicketView[] {
  const result: TicketView[] = []
  for (const phase of tree.phases) {
    for (const epic of phase.epics) {
      for (const wave of epic.waves) {
        for (const sprint of wave.sprints) {
          result.push(...sprint.tickets)
        }
      }
    }
  }
  return result
}

/** Count tickets by status across the whole tree. */
export function ticketStatusCounts(tree: PewsTree): Record<string, number> {
  const counts: Record<string, number> = {}
  for (const ticket of flattenTickets(tree)) {
    counts[ticket.status] = (counts[ticket.status] ?? 0) + 1
  }
  return counts
}

/** Find a ticket by id in the tree. Returns null if not found. */
export function findTicketById(tree: PewsTree, id: string): TicketView | null {
  for (const ticket of flattenTickets(tree)) {
    if (ticket.id === id) return ticket
  }
  return null
}

/** Find the phase that owns a given phase id. */
export function findPhaseById(tree: PewsTree, id: string): PhaseView | null {
  return tree.phases.find((p) => p.id === id) ?? null
}
