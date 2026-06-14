// @vitest-environment jsdom
/**
 * Tests for TaskColumn — empty state, card list, and inline add form.
 */

import { render, screen, fireEvent } from '@testing-library/react'
import '@testing-library/jest-dom'
import { describe, expect, it, vi } from 'vitest'
import { TaskColumn } from './TaskColumn'
import type { Task } from '../../types/task'

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: 'task-1',
    title: 'Column task',
    status: 'todo',
    project: 'cascade',
    tags: [],
    priority: 'med',
    blockers: [],
    order: 1000,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('TaskColumn', () => {
  it('renders column header with label and count', () => {
    render(
      <TaskColumn
        status="todo"
        label="To Do"
        tasks={[]}
        draggedId={null}
        onDrop={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
        onAddTask={vi.fn()}
      />,
    )
    expect(screen.getByText('To Do')).toBeInTheDocument()
    expect(screen.getByLabelText('0 tasks')).toBeInTheDocument()
  })

  it('shows empty state when no tasks', () => {
    render(
      <TaskColumn
        status="todo"
        label="To Do"
        tasks={[]}
        draggedId={null}
        onDrop={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
        onAddTask={vi.fn()}
      />,
    )
    expect(screen.getByText('No tasks here')).toBeInTheDocument()
  })

  it('renders task cards', () => {
    const tasks = [makeTask({ id: '1', title: 'First' }), makeTask({ id: '2', title: 'Second' })]
    render(
      <TaskColumn
        status="todo"
        label="To Do"
        tasks={tasks}
        draggedId={null}
        onDrop={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
        onAddTask={vi.fn()}
      />,
    )
    expect(screen.getByText('First')).toBeInTheDocument()
    expect(screen.getByText('Second')).toBeInTheDocument()
    // Empty state should NOT show
    expect(screen.queryByText('No tasks here')).not.toBeInTheDocument()
  })

  it('shows add form when Add task button is clicked', () => {
    render(
      <TaskColumn
        status="todo"
        label="To Do"
        tasks={[]}
        draggedId={null}
        onDrop={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
        onAddTask={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByLabelText('Add task to To Do'))
    expect(screen.getByRole('textbox', { name: 'New task title' })).toBeInTheDocument()
  })

  it('calls onAddTask with title when form is submitted', () => {
    const onAddTask = vi.fn()
    render(
      <TaskColumn
        status="todo"
        label="To Do"
        tasks={[]}
        draggedId={null}
        onDrop={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
        onAddTask={onAddTask}
      />,
    )
    fireEvent.click(screen.getByLabelText('Add task to To Do'))
    fireEvent.change(screen.getByRole('textbox', { name: 'New task title' }), {
      target: { value: 'New item' },
    })
    fireEvent.click(screen.getByRole('button', { name: 'Add' }))
    expect(onAddTask).toHaveBeenCalledWith('todo', 'New item')
  })

  it('hides form when Cancel is clicked', () => {
    render(
      <TaskColumn
        status="todo"
        label="To Do"
        tasks={[]}
        draggedId={null}
        onDrop={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
        onAddTask={vi.fn()}
      />,
    )
    fireEvent.click(screen.getByLabelText('Add task to To Do'))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('textbox', { name: 'New task title' })).not.toBeInTheDocument()
  })
})
