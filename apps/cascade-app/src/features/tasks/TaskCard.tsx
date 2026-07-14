/**
 * Purpose: Individual kanban card rendering title, tags, priority badge, and
 *   assignee. Supports drag-source via HTML5 drag-and-drop API.
 *   Exposes onEdit and onDelete callbacks for inline actions.
 * Inputs:  task (Task), onEdit, onDelete, onDragStart.
 * Outputs: Draggable <div> card.
 * Constraints:
 *   - Keyboard move is handled at the column level (arrow key focus + Enter).
 *   - No external dnd library — HTML5 draggable + dragstart events.
 *   - Priority badge uses PRIORITY_COLOURS token map.
 * SPORT: MASTER-COMPONENTS.md — TaskCard (E-P8-02)
 */

import React from 'react'
import { Pencil, Trash2, GripVertical } from 'lucide-react'
import type { Task } from '../../types/task'
import { PRIORITY_COLOURS } from '../../types/task'

interface TaskCardProps {
  task: Task
  onEdit: (task: Task) => void
  onDelete: (id: string) => void
  /** Called when drag starts; parent stores dragged task id. */
  onDragStart: (id: string) => void
}

/**
 * Kanban card — displays task title, tags, priority, and assignee.
 *
 * @example
 * ```tsx
 * <TaskCard task={t} onEdit={edit} onDelete={del} onDragStart={start} />
 * ```
 */
export function TaskCard({
  task,
  onEdit,
  onDelete,
  onDragStart,
}: TaskCardProps): React.ReactElement {
  return (
    <div
      draggable
      aria-label={`Task: ${task.title}`}
      role="button"
      tabIndex={0}
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = 'move'
        e.dataTransfer.setData('text/plain', task.id)
        onDragStart(task.id)
      }}
      onKeyDown={(e) => {
        // Enter or Space opens edit
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onEdit(task)
        }
      }}
      className="group rounded-lg border border-border bg-card p-3 shadow-sm cursor-grab active:cursor-grabbing hover:border-primary/40 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring transition-colors"
    >
      {/* Header row: grip + title */}
      <div className="flex items-start gap-2">
        <GripVertical
          className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground/40 group-hover:text-muted-foreground/70"
          aria-hidden="true"
        />
        <span className="flex-1 text-sm font-medium leading-snug break-words">{task.title}</span>

        {/* Actions — visible on hover/focus */}
        <div className="flex shrink-0 gap-1 opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 transition-opacity">
          <button
            type="button"
            aria-label={`Edit ${task.title}`}
            onClick={(e) => {
              e.stopPropagation()
              onEdit(task)
            }}
            className="rounded p-0.5 hover:bg-accent text-muted-foreground hover:text-foreground"
          >
            <Pencil className="h-3 w-3" aria-hidden="true" />
          </button>
          <button
            type="button"
            aria-label={`Delete ${task.title}`}
            onClick={(e) => {
              e.stopPropagation()
              onDelete(task.id)
            }}
            className="rounded p-0.5 hover:bg-destructive/10 text-muted-foreground hover:text-destructive"
          >
            <Trash2 className="h-3 w-3" aria-hidden="true" />
          </button>
        </div>
      </div>

      {/* Footer: tags + priority + assignee */}
      <div className="mt-2 flex flex-wrap items-center gap-1">
        {/* Priority badge */}
        <span
          className={`rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${PRIORITY_COLOURS[task.priority]}`}
          aria-label={`Priority: ${task.priority}`}
        >
          {task.priority}
        </span>

        {/* Tag chips */}
        {task.tags.slice(0, 3).map((tag) => (
          <span
            key={tag}
            className="rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground font-medium"
          >
            {tag}
          </span>
        ))}
        {task.tags.length > 3 && (
          <span className="text-[10px] text-muted-foreground">+{task.tags.length - 3}</span>
        )}

        {/* Assignee */}
        {task.assignee && (
          <span
            className="ml-auto rounded-full bg-primary/10 px-2 py-0.5 text-[10px] text-primary font-medium"
            aria-label={`Assigned to ${task.assignee}`}
          >
            {task.assignee.split('@')[0]}
          </span>
        )}
      </div>
    </div>
  )
}
