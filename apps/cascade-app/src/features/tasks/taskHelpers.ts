/**
 * Purpose: Pure helper functions for the Kanban board — grouping, filtering,
 *   drag-move state reducer, and unique tag extraction.
 * Inputs:  Task arrays, filter criteria, drag-drop params.
 * Outputs: Grouped task maps, filtered arrays, updated task arrays.
 * Constraints:
 *   - All functions are pure (no side effects, no IPC).
 *   - Used by both the board component and unit tests.
 * SPORT: MASTER-COMPONENTS.md — taskHelpers (E-P8-02)
 */

import type { Task, TaskStatus, TaskFilter, TaskPriority } from '../../types/task'

// ── Column grouping ────────────────────────────────────────────────────────────

/** Map from status column to ordered task array. */
export type TaskColumns = Record<TaskStatus, Task[]>

/**
 * Group a flat task list into per-column buckets, sorted by order then createdAt.
 *
 * @param tasks  Flat array of tasks from IPC.
 * @returns      Record keyed by TaskStatus (all board columns present, even if empty).
 *
 * @example
 * const cols = groupByColumn(tasks)
 * cols.inProgress // → Task[]
 */
export function groupByColumn(tasks: Task[]): TaskColumns {
  const cols: TaskColumns = {
    backlog: [],
    todo: [],
    inProgress: [],
    review: [],
    done: [],
    archived: [],
  }

  for (const task of tasks) {
    const col = task.status as TaskStatus
    if (col in cols) {
      cols[col].push(task)
    }
  }

  // Sort each column: ascending order, then ascending createdAt as tiebreaker.
  for (const status of Object.keys(cols) as TaskStatus[]) {
    cols[status].sort((a, b) => {
      if (a.order !== b.order) return a.order - b.order
      return a.createdAt < b.createdAt ? -1 : 1
    })
  }

  return cols
}

// ── In-memory filter ───────────────────────────────────────────────────────────

/**
 * Filter a task array client-side using a TaskFilter + optional text query.
 *
 * @param tasks   Source tasks (may already be IPC-filtered by project).
 * @param filter  Optional structured filter (status, tag, assignee).
 * @param text    Optional free-text query — matched against title + description.
 * @returns       Filtered (never mutated) task array.
 *
 * @example
 * const visible = applyClientFilter(tasks, { tag: 'rust' }, 'kanban')
 */
export function applyClientFilter(
  tasks: Task[],
  filter: Partial<TaskFilter>,
  text: string
): Task[] {
  let result = tasks

  if (filter.status) {
    result = result.filter((t) => t.status === filter.status)
  }

  if (filter.tag) {
    const tag = filter.tag.toLowerCase()
    result = result.filter((t) => t.tags.some((tg) => tg.toLowerCase() === tag))
  }

  if (filter.assignee) {
    const a = filter.assignee.toLowerCase()
    result = result.filter((t) => (t.assignee ?? '').toLowerCase().includes(a))
  }

  if (text.trim()) {
    const q = text.trim().toLowerCase()
    result = result.filter(
      (t) => t.title.toLowerCase().includes(q) || (t.description ?? '').toLowerCase().includes(q)
    )
  }

  return result
}

// ── Drag-move reducer ──────────────────────────────────────────────────────────

export interface DragMoveParams {
  /** Task being moved. */
  taskId: string
  /** Destination column status. */
  toStatus: TaskStatus
  /** Index within the destination column (0 = top). */
  toIndex: number
}

/**
 * Compute the new `order` value for a task being dragged to a column position.
 *
 * Uses integer spacing (1000 per slot) so future re-orders can insert without
 * renumbering. The order value is the arithmetic mean of neighbours when
 * inserting between two existing tasks; falls back to edge values otherwise.
 *
 * @param columns   Current column state.
 * @param params    Where the card is being dropped.
 * @returns         { newOrder, updatedColumns } — do NOT commit to IPC until confirmed.
 *
 * @example
 * const { newOrder } = computeDragOrder(cols, { taskId: 'x', toStatus: 'todo', toIndex: 1 })
 */
export function computeDragOrder(
  columns: TaskColumns,
  { taskId, toStatus, toIndex }: DragMoveParams
): { newOrder: number; updatedColumns: TaskColumns } {
  // Clone the columns shallowly.
  const updated: TaskColumns = { ...columns }
  for (const s of Object.keys(updated) as TaskStatus[]) {
    updated[s] = [...updated[s]]
  }

  // Remove from source column.
  let movedTask: Task | undefined
  for (const s of Object.keys(updated) as TaskStatus[]) {
    const idx = updated[s].findIndex((t) => t.id === taskId)
    if (idx !== -1) {
      ;[movedTask] = updated[s].splice(idx, 1)
      break
    }
  }

  if (!movedTask) return { newOrder: 0, updatedColumns: columns }

  // Compute order.
  const destCol = updated[toStatus]
  const clampedIdx = Math.max(0, Math.min(toIndex, destCol.length))
  const prev = destCol[clampedIdx - 1]?.order ?? 0
  const next = destCol[clampedIdx]?.order ?? prev + 2000
  const newOrder = Math.floor((prev + next) / 2) || clampedIdx * 1000

  // Insert the moved task.
  const updatedTask: Task = { ...movedTask, status: toStatus, order: newOrder }
  destCol.splice(clampedIdx, 0, updatedTask)
  updated[toStatus] = destCol

  return { newOrder, updatedColumns: updated }
}

// ── Unique tag extraction ──────────────────────────────────────────────────────

/**
 * Extract a sorted, deduplicated list of all tags present in the task array.
 *
 * @param tasks  Source array.
 * @returns      Sorted string array of unique tags.
 *
 * @example
 * uniqueTags([{ tags: ['rust','wasm'] }, { tags: ['rust'] }]) // → ['rust','wasm']
 */
export function uniqueTags(tasks: Task[]): string[] {
  const set = new Set<string>()
  for (const t of tasks) {
    for (const tag of t.tags) set.add(tag)
  }
  return Array.from(set).sort()
}

// ── Unique projects extraction ─────────────────────────────────────────────────

/**
 * Extract sorted, deduplicated project slugs from the task array.
 *
 * @param tasks  Source array.
 * @returns      Sorted string array of unique non-empty project slugs.
 *
 * @example
 * uniqueProjects(tasks) // → ['cascade','nself']
 */
export function uniqueProjects(tasks: Task[]): string[] {
  const set = new Set<string>()
  for (const t of tasks) {
    if (t.project) set.add(t.project)
  }
  return Array.from(set).sort()
}

// ── Priority sort weight ───────────────────────────────────────────────────────

const PRIORITY_WEIGHT: Record<TaskPriority, number> = {
  urgent: 0,
  high: 1,
  med: 2,
  low: 3,
}

/**
 * Compare tasks by priority descending (urgent > high > med > low).
 * Use as a comparator for Array.sort().
 */
export function comparePriority(a: Task, b: Task): number {
  return PRIORITY_WEIGHT[a.priority] - PRIORITY_WEIGHT[b.priority]
}
