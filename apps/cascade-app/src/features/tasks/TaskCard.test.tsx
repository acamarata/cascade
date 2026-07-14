// @vitest-environment jsdom
/**
 * Tests for TaskCard rendering and interaction.
 */

import { render, screen, fireEvent } from '@testing-library/react'
import '@testing-library/jest-dom'
import { describe, expect, it, vi } from 'vitest'
import { TaskCard } from './TaskCard'
import type { Task } from '../../types/task'

function makeTask(overrides: Partial<Task> = {}): Task {
  return {
    id: 'task-1',
    title: 'Test card',
    status: 'todo',
    project: 'cascade',
    tags: ['rust', 'ui'],
    priority: 'high',
    blockers: [],
    order: 1000,
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

describe('TaskCard', () => {
  it('renders the task title', () => {
    render(<TaskCard task={makeTask()} onEdit={vi.fn()} onDelete={vi.fn()} onDragStart={vi.fn()} />)
    expect(screen.getByText('Test card')).toBeInTheDocument()
  })

  it('renders tag chips', () => {
    render(<TaskCard task={makeTask()} onEdit={vi.fn()} onDelete={vi.fn()} onDragStart={vi.fn()} />)
    expect(screen.getByText('rust')).toBeInTheDocument()
    expect(screen.getByText('ui')).toBeInTheDocument()
  })

  it('renders priority badge', () => {
    render(
      <TaskCard
        task={makeTask({ priority: 'urgent' })}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
      />
    )
    expect(screen.getByText('urgent')).toBeInTheDocument()
  })

  it('renders assignee when present', () => {
    render(
      <TaskCard
        task={makeTask({ assignee: 'alice@example.com' })}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
      />
    )
    // Shows first segment before @
    expect(screen.getByText('alice')).toBeInTheDocument()
  })

  it('calls onDelete when delete button is clicked', () => {
    const onDelete = vi.fn()
    render(
      <TaskCard task={makeTask()} onEdit={vi.fn()} onDelete={onDelete} onDragStart={vi.fn()} />
    )
    fireEvent.click(screen.getByLabelText('Delete Test card'))
    expect(onDelete).toHaveBeenCalledWith('task-1')
  })

  it('calls onEdit when edit button is clicked', () => {
    const onEdit = vi.fn()
    render(<TaskCard task={makeTask()} onEdit={onEdit} onDelete={vi.fn()} onDragStart={vi.fn()} />)
    fireEvent.click(screen.getByLabelText('Edit Test card'))
    expect(onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 'task-1' }))
  })

  it('calls onEdit on Enter key press', () => {
    const onEdit = vi.fn()
    render(<TaskCard task={makeTask()} onEdit={onEdit} onDelete={vi.fn()} onDragStart={vi.fn()} />)
    const card = screen.getByRole('button', { name: 'Task: Test card' })
    fireEvent.keyDown(card, { key: 'Enter' })
    expect(onEdit).toHaveBeenCalled()
  })

  it('shows +N overflow indicator for more than 3 tags', () => {
    render(
      <TaskCard
        task={makeTask({ tags: ['a', 'b', 'c', 'd', 'e'] })}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
      />
    )
    expect(screen.getByText('+2')).toBeInTheDocument()
  })

  it('renders empty state gracefully with no tags or assignee', () => {
    render(
      <TaskCard
        task={makeTask({ tags: [], assignee: undefined })}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        onDragStart={vi.fn()}
      />
    )
    expect(screen.getByText('Test card')).toBeInTheDocument()
  })
})
