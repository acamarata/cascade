/**
 * Purpose: IPC hook for fetching and mutating kanban tasks via Tauri invoke.
 *   Manages loading/error state; exposes refetch + mutation helpers.
 * Inputs:  Optional project filter; optional status filter.
 * Outputs: { tasks, loading, error, refetch, createTask, updateTask,
 *            deleteTask, moveTask } tuple.
 * Constraints:
 *   - No @tanstack/react-query; use useState + useEffect pattern (per project standard).
 *   - Re-fetches when project filter changes.
 *   - In non-Tauri environments invoke is mocked; hook must not crash.
 * SPORT: MASTER-COMPONENTS.md — useTasks (E-P8-02)
 */

import { useCallback, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type {
  Task,
  TaskCreateParams,
  TaskUpdateParams,
  TaskMoveParams,
  TaskListResult,
  TaskResult,
  TaskDeleteResult,
} from '../../types/task'

interface UseTasksOptions {
  /** Restrict to a specific project slug; undefined = all projects. */
  project?: string
}

interface UseTasksResult {
  tasks: Task[]
  loading: boolean
  error: string | null
  refetch: () => void
  createTask: (params: TaskCreateParams) => Promise<Task>
  updateTask: (params: TaskUpdateParams) => Promise<Task>
  deleteTask: (id: string) => Promise<boolean>
  moveTask: (params: TaskMoveParams) => Promise<Task>
}

/**
 * Fetch and manage kanban tasks via Tauri IPC.
 *
 * @param options  Optional filter (project slug).
 * @returns        Task data + loading/error state + mutation helpers.
 *
 * @example
 * ```tsx
 * const { tasks, loading, moveTask } = useTasks({ project: 'cascade' })
 * ```
 */
export function useTasks({ project }: UseTasksOptions = {}): UseTasksResult {
  const [tasks, setTasks] = useState<Task[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const refetch = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    const filter = project ? { project } : {}
    invoke<TaskListResult>('task_list', { filter })
      .then((res) => {
        if (!cancelled) {
          setTasks(res.tasks)
          setLoading(false)
        }
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      })

    return () => {
      cancelled = true
    }
  }, [project, tick])

  const createTask = useCallback(
    async (params: TaskCreateParams): Promise<Task> => {
      const res = await invoke<TaskResult>('task_create', params as unknown as Record<string, unknown>)
      refetch()
      return res.task
    },
    [refetch],
  )

  const updateTask = useCallback(
    async (params: TaskUpdateParams): Promise<Task> => {
      const res = await invoke<TaskResult>('task_update', params as unknown as Record<string, unknown>)
      refetch()
      return res.task
    },
    [refetch],
  )

  const deleteTask = useCallback(
    async (id: string): Promise<boolean> => {
      const res = await invoke<TaskDeleteResult>('task_delete', { id })
      refetch()
      return res.deleted
    },
    [refetch],
  )

  const moveTask = useCallback(
    async (params: TaskMoveParams): Promise<Task> => {
      const res = await invoke<TaskResult>('task_move', params as unknown as Record<string, unknown>)
      refetch()
      return res.task
    },
    [refetch],
  )

  return { tasks, loading, error, refetch, createTask, updateTask, deleteTask, moveTask }
}
