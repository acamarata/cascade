/**
 * Purpose: Main Kanban board view. Renders the five board columns (Backlog/Todo/
 *   InProgress/Review/Done) with drag-and-drop between them, a filter/search bar,
 *   a project selector, and a cross-project "All" view.
 * Inputs:  Optional initial project filter from the TasksPage.
 * Outputs: Full-height kanban board with horizontal scroll.
 * Constraints:
 *   - Drag-drop via HTML5 API (no dnd-kit/react-beautiful-dnd dependency).
 *   - Keyboard fallback: arrow keys within a column, Tab between columns.
 *   - Optimistic local update on drop; IPC call follows.
 *   - All side-effects on IPC errors are surfaced in an error banner.
 * SPORT: MASTER-COMPONENTS.md — KanbanBoard (E-P8-02)
 */

import React, { useCallback, useEffect, useMemo, useState } from 'react'
import { Filter, Plus, RefreshCw, Search } from 'lucide-react'
import { useTasks } from './useTasks'
import { TaskColumn } from './TaskColumn'
import { TaskEditModal } from './TaskEditModal'
import {
  groupByColumn,
  applyClientFilter,
  computeDragOrder,
  uniqueTags,
  uniqueProjects,
} from './taskHelpers'
import type { Task, TaskStatus, TaskFilter } from '../../types/task'
import { BOARD_COLUMNS, COLUMN_LABELS } from '../../types/task'
import { Skeleton } from '../../components/Skeleton'

interface KanbanBoardProps {
  /** Pre-select a specific project; undefined = "All". */
  initialProject?: string
}

/**
 * Full kanban board with column views, filters, and drag-drop.
 *
 * @example
 * ```tsx
 * <KanbanBoard initialProject="cascade" />
 * ```
 */
export function KanbanBoard({ initialProject }: KanbanBoardProps): React.ReactElement {
  const [selectedProject, setSelectedProject] = useState(initialProject ?? '')
  const [textFilter, setTextFilter] = useState('')
  const [tagFilter, setTagFilter] = useState('')
  const [editingTask, setEditingTask] = useState<Task | null | undefined>(undefined) // undefined = closed, null = create
  const [ipcError, setIpcError] = useState<string | null>(null)
  const [draggedId, setDraggedId] = useState<string | null>(null)
  // Optimistic local columns (overrides hook data during in-flight drag)
  const [optimisticColumns, setOptimisticColumns] = useState<ReturnType<typeof groupByColumn> | null>(null)

  const { tasks, loading, error, refetch, createTask, updateTask, deleteTask, moveTask } = useTasks({
    project: selectedProject || undefined,
  })

  // Reset optimistic state when real tasks arrive
  useEffect(() => {
    setOptimisticColumns(null)
  }, [tasks])

  // Derived data
  const allTags = useMemo(() => uniqueTags(tasks), [tasks])
  const allProjects = useMemo(() => uniqueProjects(tasks), [tasks])

  const filtered = useMemo(() => {
    const f: Partial<TaskFilter> = {}
    if (tagFilter) f.tag = tagFilter
    return applyClientFilter(tasks, f, textFilter)
  }, [tasks, tagFilter, textFilter])

  const columns = useMemo(
    () => optimisticColumns ?? groupByColumn(filtered),
    [optimisticColumns, filtered],
  )

  // ── Drag handlers ────────────────────────────────────────────────────────────

  const handleDragStart = useCallback((id: string) => {
    setDraggedId(id)
  }, [])

  const handleDrop = useCallback(
    async (toStatus: TaskStatus, toIndex: number) => {
      if (!draggedId) return
      setDraggedId(null)

      const current = optimisticColumns ?? groupByColumn(filtered)
      const { newOrder, updatedColumns } = computeDragOrder(current, {
        taskId: draggedId,
        toStatus,
        toIndex,
      })

      // Apply optimistic update immediately
      setOptimisticColumns(updatedColumns)

      try {
        await moveTask({ id: draggedId, status: toStatus, order: newOrder })
      } catch (e) {
        setIpcError(String(e))
        setOptimisticColumns(null)
        refetch()
      }
    },
    [draggedId, filtered, optimisticColumns, moveTask, refetch],
  )

  // ── Card actions ─────────────────────────────────────────────────────────────

  async function handleSave(params: Parameters<typeof createTask>[0] | Parameters<typeof updateTask>[0]) {
    try {
      if ('id' in params) {
        await updateTask(params as Parameters<typeof updateTask>[0])
      } else {
        await createTask(params as Parameters<typeof createTask>[0])
      }
      setIpcError(null)
    } catch (e) {
      setIpcError(String(e))
    }
  }

  async function handleDelete(id: string) {
    if (!window.confirm('Delete this task?')) return
    try {
      await deleteTask(id)
      setIpcError(null)
    } catch (e) {
      setIpcError(String(e))
    }
  }

  async function handleAddTask(status: TaskStatus, title: string) {
    try {
      await createTask({
        title,
        status,
        project: selectedProject || '',
      })
      setIpcError(null)
    } catch (e) {
      setIpcError(String(e))
    }
  }

  // ── Render ───────────────────────────────────────────────────────────────────

  return (
    <div className="flex h-full flex-col">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
        {/* Project selector */}
        <div className="flex items-center gap-1.5">
          <label htmlFor="project-select" className="text-xs font-medium text-muted-foreground sr-only">
            Project
          </label>
          <select
            id="project-select"
            value={selectedProject}
            onChange={(e) => setSelectedProject(e.target.value)}
            aria-label="Filter by project"
            className="rounded border border-border bg-background px-2 py-1.5 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <option value="">All Projects</option>
            {allProjects.map((p) => (
              <option key={p} value={p}>{p}</option>
            ))}
          </select>
        </div>

        {/* Text search */}
        <div className="relative flex-1 min-w-[160px] max-w-xs">
          <Search className="absolute left-2.5 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
          <input
            type="search"
            value={textFilter}
            onChange={(e) => setTextFilter(e.target.value)}
            placeholder="Search tasks…"
            aria-label="Search tasks by text"
            className="w-full rounded border border-border bg-background pl-8 pr-3 py-1.5 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        </div>

        {/* Tag filter */}
        {allTags.length > 0 && (
          <div className="flex items-center gap-1.5">
            <Filter className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
            <select
              value={tagFilter}
              onChange={(e) => setTagFilter(e.target.value)}
              aria-label="Filter by tag"
              className="rounded border border-border bg-background px-2 py-1.5 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <option value="">All tags</option>
              {allTags.map((tag) => (
                <option key={tag} value={tag}>{tag}</option>
              ))}
            </select>
          </div>
        )}

        {/* Spacer */}
        <div className="flex-1" aria-hidden="true" />

        {/* Refresh */}
        <button
          type="button"
          onClick={refetch}
          aria-label="Refresh board"
          className="rounded p-1.5 text-muted-foreground hover:bg-accent hover:text-foreground"
        >
          <RefreshCw className="h-4 w-4" aria-hidden="true" />
        </button>

        {/* New task */}
        <button
          type="button"
          onClick={() => setEditingTask(null)}
          className="flex items-center gap-1.5 rounded bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground hover:bg-primary/90"
        >
          <Plus className="h-3.5 w-3.5" aria-hidden="true" />
          New Task
        </button>
      </div>

      {/* Error banner */}
      {(error ?? ipcError) && (
        <div
          role="alert"
          className="mx-4 mt-3 rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-2 text-sm text-destructive"
        >
          {error ?? ipcError}
        </div>
      )}

      {/* Board */}
      {loading ? (
        <div
          className="flex gap-4 overflow-x-auto px-4 py-4"
          aria-busy="true"
          aria-label="Loading board"
        >
          {BOARD_COLUMNS.map((col) => (
            <div key={col} className="flex w-64 shrink-0 flex-col gap-3">
              <Skeleton className="h-9 w-full rounded-xl" />
              {Array.from({ length: 3 }).map((_, i) => (
                <Skeleton key={i} className="h-16 w-full rounded-lg" />
              ))}
            </div>
          ))}
        </div>
      ) : (
        <div
          className="flex gap-4 overflow-x-auto px-4 py-4 flex-1"
          role="region"
          aria-label="Kanban board"
        >
          {BOARD_COLUMNS.map((status) => (
            <TaskColumn
              key={status}
              status={status}
              label={COLUMN_LABELS[status]}
              tasks={columns[status]}
              draggedId={draggedId}
              onDrop={handleDrop}
              onEdit={setEditingTask}
              onDelete={handleDelete}
              onDragStart={handleDragStart}
              onAddTask={handleAddTask}
            />
          ))}
        </div>
      )}

      {/* Edit / create modal */}
      {editingTask !== undefined && (
        <TaskEditModal
          task={editingTask}
          defaultProject={selectedProject}
          onSave={handleSave}
          onClose={() => setEditingTask(undefined)}
        />
      )}
    </div>
  )
}
