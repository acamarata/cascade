/**
 * Purpose: A single Kanban column — header with count, drop zone, task cards,
 *   and an inline "add card" form.
 * Inputs:  status, label, tasks, draggedId, onDrop, onEdit, onDelete,
 *          onDragStart, onAddTask.
 * Outputs: <section> landmark with a header, scrollable card list, and add button.
 * Constraints:
 *   - Drop events call onDrop(status, insertIndex) — computed from mouse Y.
 *   - dragover preventDefault enables dropping.
 *   - Keyboard navigation: arrow keys within column cards handled at column level.
 *   - Empty state shows a "no tasks" illustration text.
 * SPORT: MASTER-COMPONENTS.md — TaskColumn (E-P8-02)
 */

import React, { useRef, useState } from 'react'
import { Plus } from 'lucide-react'
import type { Task, TaskStatus } from '../../types/task'
import { TaskCard } from './TaskCard'

interface TaskColumnProps {
  status: TaskStatus
  label: string
  tasks: Task[]
  /** ID of the currently-dragged task (null when not dragging). */
  draggedId: string | null
  onDrop: (toStatus: TaskStatus, toIndex: number) => void
  onEdit: (task: Task) => void
  onDelete: (id: string) => void
  onDragStart: (id: string) => void
  onAddTask: (status: TaskStatus, title: string) => void
}

/**
 * A Kanban column with drop zone and inline create form.
 *
 * @example
 * ```tsx
 * <TaskColumn status="todo" label="To Do" tasks={[]} ... />
 * ```
 */
export function TaskColumn({
  status,
  label,
  tasks,
  draggedId,
  onDrop,
  onEdit,
  onDelete,
  onDragStart,
  onAddTask,
}: TaskColumnProps): React.ReactElement {
  const [isOver, setIsOver] = useState(false)
  const [addingTitle, setAddingTitle] = useState('')
  const [showAdd, setShowAdd] = useState(false)
  const columnRef = useRef<HTMLDivElement>(null)

  function computeDropIndex(e: React.DragEvent<HTMLDivElement>): number {
    if (!columnRef.current) return tasks.length
    const cards = columnRef.current.querySelectorAll<HTMLElement>('[role="article"]')
    let idx = tasks.length
    for (let i = 0; i < cards.length; i++) {
      const rect = cards[i].getBoundingClientRect()
      if (e.clientY < rect.top + rect.height / 2) {
        idx = i
        break
      }
    }
    return idx
  }

  function handleDragOver(e: React.DragEvent<HTMLDivElement>) {
    e.preventDefault()
    e.dataTransfer.dropEffect = 'move'
    setIsOver(true)
  }

  function handleDragLeave() {
    setIsOver(false)
  }

  function handleDrop(e: React.DragEvent<HTMLDivElement>) {
    e.preventDefault()
    setIsOver(false)
    const idx = computeDropIndex(e)
    onDrop(status, idx)
  }

  function handleAddSubmit(e: React.FormEvent) {
    e.preventDefault()
    const title = addingTitle.trim()
    if (!title) return
    onAddTask(status, title)
    setAddingTitle('')
    setShowAdd(false)
  }

  // Keyboard move within column: focus management on arrow keys
  function handleColumnKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    if (!columnRef.current) return
    const cards = Array.from(
      columnRef.current.querySelectorAll<HTMLElement>('[role="article"]'),
    )
    const focused = document.activeElement
    const idx = cards.indexOf(focused as HTMLElement)
    if (idx === -1) return
    if (e.key === 'ArrowDown' && idx < cards.length - 1) {
      e.preventDefault()
      cards[idx + 1].focus()
    } else if (e.key === 'ArrowUp' && idx > 0) {
      e.preventDefault()
      cards[idx - 1].focus()
    }
  }

  return (
    <section
      aria-label={`${label} column (${tasks.length} tasks)`}
      className="flex w-64 shrink-0 flex-col rounded-xl border border-border bg-muted/30"
    >
      {/* Column header */}
      <header className="flex items-center justify-between px-3 py-2.5 border-b border-border">
        <span className="text-sm font-semibold">{label}</span>
        <span
          className="rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground font-medium"
          aria-label={`${tasks.length} tasks`}
        >
          {tasks.length}
        </span>
      </header>

      {/* Card list — drop zone */}
      <div
        ref={columnRef}
        role="list"
        aria-label={`${label} tasks`}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
        onKeyDown={handleColumnKeyDown}
        className={`flex flex-1 flex-col gap-2 overflow-y-auto p-2 min-h-[120px] transition-colors ${
          isOver && draggedId !== null ? 'bg-primary/5 ring-1 ring-inset ring-primary/30' : ''
        }`}
      >
        {tasks.length === 0 && !isOver && (
          <p className="mt-4 text-center text-xs text-muted-foreground/60 select-none">
            No tasks here
          </p>
        )}

        {tasks.map((task) => (
          <TaskCard
            key={task.id}
            task={task}
            onEdit={onEdit}
            onDelete={onDelete}
            onDragStart={onDragStart}
          />
        ))}

        {/* Drop placeholder when dragging over */}
        {isOver && draggedId !== null && (
          <div
            aria-hidden="true"
            className="h-10 rounded-lg border-2 border-dashed border-primary/40 bg-primary/5"
          />
        )}
      </div>

      {/* Inline add form */}
      {showAdd ? (
        <form
          onSubmit={handleAddSubmit}
          className="border-t border-border p-2 flex flex-col gap-1.5"
        >
          <input
            autoFocus
            type="text"
            placeholder="Card title…"
            value={addingTitle}
            onChange={(e) => setAddingTitle(e.target.value)}
            aria-label="New task title"
            className="w-full rounded border border-border bg-background px-2 py-1 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
          <div className="flex gap-1">
            <button
              type="submit"
              className="flex-1 rounded bg-primary px-2 py-1 text-xs font-medium text-primary-foreground hover:bg-primary/90"
            >
              Add
            </button>
            <button
              type="button"
              onClick={() => setShowAdd(false)}
              className="rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent"
            >
              Cancel
            </button>
          </div>
        </form>
      ) : (
        <button
          type="button"
          onClick={() => setShowAdd(true)}
          aria-label={`Add task to ${label}`}
          className="flex items-center gap-1.5 px-3 py-2 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/60 border-t border-border rounded-b-xl transition-colors"
        >
          <Plus className="h-3 w-3" aria-hidden="true" />
          Add task
        </button>
      )}
    </section>
  )
}
