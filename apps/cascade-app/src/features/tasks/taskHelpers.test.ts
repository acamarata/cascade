// @vitest-environment jsdom
/**
 * Tests for the Kanban task helper functions.
 *   - groupByColumn: column grouping and ordering
 *   - computeDragOrder: drag-move reducer
 *   - applyClientFilter: filter logic
 *   - uniqueTags: tag extraction
 */

import { describe, expect, it } from 'vitest'
import {
  groupByColumn,
  computeDragOrder,
  applyClientFilter,
  uniqueTags,
  uniqueProjects,
} from './taskHelpers'
import type { Task } from '../../types/task'

// ── Fixtures ──────────────────────────────────────────────────────────────────

function makeTask(overrides: Partial<Task>): Task {
  return {
    id: overrides.id ?? 'task-1',
    title: overrides.title ?? 'Test task',
    status: overrides.status ?? 'backlog',
    project: overrides.project ?? 'test-project',
    tags: overrides.tags ?? [],
    priority: overrides.priority ?? 'med',
    blockers: overrides.blockers ?? [],
    order: overrides.order ?? 1000,
    createdAt: overrides.createdAt ?? '2026-01-01T00:00:00Z',
    updatedAt: overrides.updatedAt ?? '2026-01-01T00:00:00Z',
    ...overrides,
  }
}

// ── groupByColumn ─────────────────────────────────────────────────────────────

describe('groupByColumn', () => {
  it('places tasks in correct columns', () => {
    const tasks: Task[] = [
      makeTask({ id: 'a', status: 'todo' }),
      makeTask({ id: 'b', status: 'inProgress' }),
      makeTask({ id: 'c', status: 'done' }),
      makeTask({ id: 'd', status: 'todo' }),
    ]
    const cols = groupByColumn(tasks)
    expect(cols.todo).toHaveLength(2)
    expect(cols.inProgress).toHaveLength(1)
    expect(cols.done).toHaveLength(1)
    expect(cols.backlog).toHaveLength(0)
    expect(cols.review).toHaveLength(0)
  })

  it('sorts tasks within column by order ascending', () => {
    const tasks: Task[] = [
      makeTask({ id: 'b', status: 'todo', order: 2000 }),
      makeTask({ id: 'a', status: 'todo', order: 1000 }),
      makeTask({ id: 'c', status: 'todo', order: 500 }),
    ]
    const cols = groupByColumn(tasks)
    expect(cols.todo.map((t) => t.id)).toEqual(['c', 'a', 'b'])
  })

  it('returns all six status columns even if empty', () => {
    const cols = groupByColumn([])
    const keys = Object.keys(cols)
    expect(keys).toContain('backlog')
    expect(keys).toContain('todo')
    expect(keys).toContain('inProgress')
    expect(keys).toContain('review')
    expect(keys).toContain('done')
    expect(keys).toContain('archived')
  })
})

// ── computeDragOrder ──────────────────────────────────────────────────────────

describe('computeDragOrder', () => {
  it('moves task to a new column', () => {
    const tasks: Task[] = [
      makeTask({ id: 'a', status: 'backlog', order: 1000 }),
      makeTask({ id: 'b', status: 'todo', order: 1000 }),
    ]
    const cols = groupByColumn(tasks)
    const { updatedColumns, newOrder } = computeDragOrder(cols, {
      taskId: 'a',
      toStatus: 'todo',
      toIndex: 0,
    })
    // Task 'a' should now be in 'todo'
    expect(updatedColumns.todo.some((t) => t.id === 'a')).toBe(true)
    // Task 'a' should be gone from 'backlog'
    expect(updatedColumns.backlog.some((t) => t.id === 'a')).toBe(false)
    // New order should be a number
    expect(typeof newOrder).toBe('number')
  })

  it('does not mutate original columns', () => {
    const tasks: Task[] = [makeTask({ id: 'x', status: 'backlog', order: 1000 })]
    const cols = groupByColumn(tasks)
    const originalBacklogLength = cols.backlog.length
    computeDragOrder(cols, { taskId: 'x', toStatus: 'todo', toIndex: 0 })
    // Original should be unchanged
    expect(cols.backlog).toHaveLength(originalBacklogLength)
  })

  it('returns original columns for unknown task id', () => {
    const cols = groupByColumn([])
    const result = computeDragOrder(cols, {
      taskId: 'nonexistent',
      toStatus: 'todo',
      toIndex: 0,
    })
    expect(result.updatedColumns).toBe(cols)
  })
})

// ── applyClientFilter ─────────────────────────────────────────────────────────

describe('applyClientFilter', () => {
  const tasks: Task[] = [
    makeTask({ id: '1', title: 'Rust kanban task', tags: ['rust', 'ui'], status: 'todo' }),
    makeTask({ id: '2', title: 'Another task', tags: ['python'], status: 'done' }),
    makeTask({ id: '3', title: 'Third item', tags: ['rust'], status: 'backlog', assignee: 'alice@example.com' }),
  ]

  it('returns all tasks with empty filter and text', () => {
    expect(applyClientFilter(tasks, {}, '')).toHaveLength(3)
  })

  it('filters by tag', () => {
    const result = applyClientFilter(tasks, { tag: 'rust' }, '')
    expect(result).toHaveLength(2)
    expect(result.map((t) => t.id)).toEqual(expect.arrayContaining(['1', '3']))
  })

  it('filters by text (title)', () => {
    const result = applyClientFilter(tasks, {}, 'kanban')
    expect(result).toHaveLength(1)
    expect(result[0].id).toBe('1')
  })

  it('filters by assignee', () => {
    const result = applyClientFilter(tasks, { assignee: 'alice' }, '')
    expect(result).toHaveLength(1)
    expect(result[0].id).toBe('3')
  })

  it('combines tag + text filters (AND)', () => {
    const result = applyClientFilter(tasks, { tag: 'rust' }, 'kanban')
    expect(result).toHaveLength(1)
    expect(result[0].id).toBe('1')
  })

  it('returns empty array when no match', () => {
    const result = applyClientFilter(tasks, { tag: 'nonexistent' }, '')
    expect(result).toHaveLength(0)
  })
})

// ── uniqueTags ────────────────────────────────────────────────────────────────

describe('uniqueTags', () => {
  it('extracts unique sorted tags', () => {
    const tasks: Task[] = [
      makeTask({ tags: ['rust', 'wasm'] }),
      makeTask({ tags: ['rust', 'ui'] }),
    ]
    expect(uniqueTags(tasks)).toEqual(['rust', 'ui', 'wasm'])
  })

  it('returns empty array for tasks with no tags', () => {
    expect(uniqueTags([makeTask({ tags: [] })])).toEqual([])
  })
})

// ── uniqueProjects ─────────────────────────────────────────────────────────────

describe('uniqueProjects', () => {
  it('extracts unique sorted project slugs', () => {
    const tasks: Task[] = [
      makeTask({ project: 'cascade' }),
      makeTask({ project: 'nself' }),
      makeTask({ project: 'cascade' }),
    ]
    expect(uniqueProjects(tasks)).toEqual(['cascade', 'nself'])
  })

  it('excludes empty project string', () => {
    const tasks: Task[] = [
      makeTask({ project: '' }),
      makeTask({ project: 'cascade' }),
    ]
    expect(uniqueProjects(tasks)).toEqual(['cascade'])
  })
})
