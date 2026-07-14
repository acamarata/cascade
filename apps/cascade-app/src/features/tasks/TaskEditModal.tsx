/**
 * Purpose: Modal dialog for creating or editing a kanban task.
 *   Handles title, description, project, status, priority, tags, and assignee fields.
 * Inputs:  task (Task | null for create), project (pre-fill), onSave, onClose.
 * Outputs: Form that calls onSave with create/update params on submit.
 * Constraints:
 *   - Uses native <dialog> element for a11y; falls back to overlay <div> for Tauri WebView.
 *   - No external form library — controlled React state.
 *   - Tags are comma-separated on input, stored as string[].
 *   - Escape key closes.
 * SPORT: MASTER-COMPONENTS.md — TaskEditModal (E-P8-02)
 */

import React, { useEffect, useRef, useState } from 'react'
import { X } from 'lucide-react'
import type {
  Task,
  TaskStatus,
  TaskPriority,
  TaskCreateParams,
  TaskUpdateParams,
} from '../../types/task'
import { BOARD_COLUMNS, COLUMN_LABELS } from '../../types/task'

interface TaskEditModalProps {
  /** Task to edit; null = create new. */
  task: Task | null
  /** Pre-fill project when creating. */
  defaultProject?: string
  /** Called with create/update params on save. */
  onSave: (params: TaskCreateParams | TaskUpdateParams) => void
  onClose: () => void
}

const PRIORITIES: TaskPriority[] = ['low', 'med', 'high', 'urgent']

/**
 * Modal for creating or editing a task.
 *
 * @example
 * ```tsx
 * <TaskEditModal task={null} defaultProject="cascade" onSave={...} onClose={...} />
 * ```
 */
export function TaskEditModal({
  task,
  defaultProject = '',
  onSave,
  onClose,
}: TaskEditModalProps): React.ReactElement {
  const isEdit = task !== null
  const ref = useRef<HTMLDivElement>(null)

  const [title, setTitle] = useState(task?.title ?? '')
  const [description, setDescription] = useState(task?.description ?? '')
  const [project, setProject] = useState(task?.project ?? defaultProject)
  const [status, setStatus] = useState<TaskStatus>(task?.status ?? 'backlog')
  const [priority, setPriority] = useState<TaskPriority>(task?.priority ?? 'med')
  const [tagsRaw, setTagsRaw] = useState(task?.tags.join(', ') ?? '')
  const [assignee, setAssignee] = useState(task?.assignee ?? '')

  // Close on Escape
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  // Focus trap — first focusable element
  useEffect(() => {
    ref.current?.querySelector<HTMLElement>('input, textarea, select, button')?.focus()
  }, [])

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const tags = tagsRaw
      .split(',')
      .map((t) => t.trim())
      .filter(Boolean)

    if (isEdit && task) {
      const params: TaskUpdateParams = {
        id: task.id,
        title: title.trim() || undefined,
        description: description.trim() ? description.trim() : null,
        status,
        project: project.trim() || undefined,
        tags,
        assignee: assignee.trim() ? assignee.trim() : null,
        priority,
      }
      onSave(params)
    } else {
      const params: TaskCreateParams = {
        title: title.trim(),
        description: description.trim() || undefined,
        project: project.trim(),
        status,
        tags,
        assignee: assignee.trim() || undefined,
        priority,
      }
      onSave(params)
    }
    onClose()
  }

  return (
    /* Backdrop */
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/50"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
      role="presentation"
    >
      <div
        ref={ref}
        role="dialog"
        aria-modal="true"
        aria-label={isEdit ? 'Edit task' : 'Create task'}
        className="relative w-full max-w-lg rounded-xl border border-border bg-background shadow-xl p-6"
      >
        <button
          type="button"
          onClick={onClose}
          aria-label="Close dialog"
          className="absolute right-4 top-4 rounded-md p-1 text-muted-foreground hover:bg-accent"
        >
          <X className="h-4 w-4" aria-hidden="true" />
        </button>

        <h2 className="mb-5 text-lg font-semibold">{isEdit ? 'Edit Task' : 'New Task'}</h2>

        <form onSubmit={handleSubmit} className="flex flex-col gap-4">
          {/* Title */}
          <div>
            <label
              className="mb-1 block text-xs font-medium text-muted-foreground"
              htmlFor="task-title"
            >
              Title <span aria-hidden="true">*</span>
            </label>
            <input
              id="task-title"
              required
              type="text"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              placeholder="Task title…"
              className="w-full rounded border border-border bg-muted/30 px-3 py-2 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          {/* Description */}
          <div>
            <label
              className="mb-1 block text-xs font-medium text-muted-foreground"
              htmlFor="task-desc"
            >
              Description
            </label>
            <textarea
              id="task-desc"
              rows={3}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Optional markdown description…"
              className="w-full rounded border border-border bg-muted/30 px-3 py-2 text-sm resize-none focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          {/* Row: Project + Status */}
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label
                className="mb-1 block text-xs font-medium text-muted-foreground"
                htmlFor="task-project"
              >
                Project
              </label>
              <input
                id="task-project"
                type="text"
                value={project}
                onChange={(e) => setProject(e.target.value)}
                placeholder="project-slug"
                className="w-full rounded border border-border bg-muted/30 px-3 py-2 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
            <div>
              <label
                className="mb-1 block text-xs font-medium text-muted-foreground"
                htmlFor="task-status"
              >
                Status
              </label>
              <select
                id="task-status"
                value={status}
                onChange={(e) => setStatus(e.target.value as TaskStatus)}
                className="w-full rounded border border-border bg-muted/30 px-3 py-2 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {BOARD_COLUMNS.map((s) => (
                  <option key={s} value={s}>
                    {COLUMN_LABELS[s]}
                  </option>
                ))}
              </select>
            </div>
          </div>

          {/* Row: Priority + Assignee */}
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label
                className="mb-1 block text-xs font-medium text-muted-foreground"
                htmlFor="task-priority"
              >
                Priority
              </label>
              <select
                id="task-priority"
                value={priority}
                onChange={(e) => setPriority(e.target.value as TaskPriority)}
                className="w-full rounded border border-border bg-muted/30 px-3 py-2 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {PRIORITIES.map((p) => (
                  <option key={p} value={p}>
                    {p.charAt(0).toUpperCase() + p.slice(1)}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <label
                className="mb-1 block text-xs font-medium text-muted-foreground"
                htmlFor="task-assignee"
              >
                Assignee
              </label>
              <input
                id="task-assignee"
                type="text"
                value={assignee}
                onChange={(e) => setAssignee(e.target.value)}
                placeholder="email or username"
                className="w-full rounded border border-border bg-muted/30 px-3 py-2 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              />
            </div>
          </div>

          {/* Tags */}
          <div>
            <label
              className="mb-1 block text-xs font-medium text-muted-foreground"
              htmlFor="task-tags"
            >
              Tags <span className="font-normal">(comma-separated)</span>
            </label>
            <input
              id="task-tags"
              type="text"
              value={tagsRaw}
              onChange={(e) => setTagsRaw(e.target.value)}
              placeholder="rust, kanban, ui"
              className="w-full rounded border border-border bg-muted/30 px-3 py-2 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            />
          </div>

          {/* Actions */}
          <div className="flex justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={onClose}
              className="rounded px-4 py-1.5 text-sm text-muted-foreground hover:bg-accent"
            >
              Cancel
            </button>
            <button
              type="submit"
              className="rounded bg-primary px-4 py-1.5 text-sm font-medium text-primary-foreground hover:bg-primary/90"
            >
              {isEdit ? 'Save Changes' : 'Create Task'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}
