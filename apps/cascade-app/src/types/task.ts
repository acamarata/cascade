/**
 * Purpose: TypeScript types for the Cascade Kanban task IPC layer.
 *   Mirrors cascade_types::task exactly (camelCase via serde rename_all).
 * Inputs:  Derived from crates/cascade-types/src/task.rs (schema v1).
 * Outputs: Exported types consumed by all task feature components and hooks.
 * Constraints:
 *   - Field names are camelCase to match the Rust wire format (serde rename_all = "camelCase").
 *   - Add fields by appending only. Never rename or remove without a versioning ticket.
 * SPORT: MASTER-LIBS.md — task types, src/types/task.ts
 */

/** Board column / lifecycle state for a kanban task. */
export type TaskStatus = 'backlog' | 'todo' | 'inProgress' | 'review' | 'done' | 'archived'

/** Priority level for a kanban task. */
export type TaskPriority = 'low' | 'med' | 'high' | 'urgent'

/** A single kanban card. Matches the Rust Task struct (camelCase wire format). */
export interface Task {
  /** UUID v4 string. */
  id: string
  /** Short card title. */
  title: string
  /** Optional markdown body. */
  description?: string
  /** Current board column. */
  status: TaskStatus
  /** Project path or slug; empty string = global. */
  project: string
  /** Taxonomy tags. */
  tags: string[]
  /** Optional assignee email or username. */
  assignee?: string
  /** Priority level. */
  priority: TaskPriority
  /** IDs of blocking tasks. */
  blockers: string[]
  /** ISO 8601 creation timestamp. */
  createdAt: string
  /** ISO 8601 last-update timestamp. */
  updatedAt: string
  /** Optional ISO 8601 due date. */
  due?: string
  /** Board display order within column (lower = higher). */
  order: number
}

/** Optional filter set for task_list. */
export interface TaskFilter {
  project?: string
  status?: TaskStatus
  tag?: string
  assignee?: string
}

/** Params for task_create. */
export interface TaskCreateParams {
  title: string
  description?: string
  project?: string
  status?: TaskStatus
  tags?: string[]
  assignee?: string
  priority?: TaskPriority
  blockers?: string[]
  due?: string
  order?: number
}

/** Params for task_update (all optional except id). */
export interface TaskUpdateParams {
  id: string
  title?: string
  description?: string | null
  status?: TaskStatus
  project?: string
  tags?: string[]
  assignee?: string | null
  priority?: TaskPriority
  blockers?: string[]
  due?: string | null
  order?: number
}

/** Params for task_move. */
export interface TaskMoveParams {
  id: string
  status: TaskStatus
  order: number
}

/** Result from task_list. */
export interface TaskListResult {
  tasks: Task[]
  total: number
}

/** Result from task_create / task_update / task_move. */
export interface TaskResult {
  task: Task
}

/** Result from task_delete. */
export interface TaskDeleteResult {
  id: string
  deleted: boolean
}

/** The board columns shown in the kanban UI (excludes Archived). */
export const BOARD_COLUMNS: TaskStatus[] = ['backlog', 'todo', 'inProgress', 'review', 'done']

/** Human-readable labels for each column. */
export const COLUMN_LABELS: Record<TaskStatus, string> = {
  backlog: 'Backlog',
  todo: 'To Do',
  inProgress: 'In Progress',
  review: 'Review',
  done: 'Done',
  archived: 'Archived',
}

/** Colour tokens for priority badges. */
export const PRIORITY_COLOURS: Record<TaskPriority, string> = {
  low: 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400',
  med: 'bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300',
  high: 'bg-amber-100 text-amber-700 dark:bg-amber-900 dark:text-amber-300',
  urgent: 'bg-red-100 text-red-700 dark:bg-red-900 dark:text-red-300',
}
