/**
 * Purpose: Tasks page — hosts the KanbanBoard with per-project and cross-project views.
 *   Route: /tasks (replaces the InboxPage stub at /inbox, which now links here).
 * Inputs:  None (project selection is internal board state).
 * Outputs: Full-height page with KanbanBoard.
 * Constraints:
 *   - Header bar with page title and description for context.
 *   - Delegates all board logic to KanbanBoard.
 * SPORT: MASTER-ROUTES.md — /tasks (E-P8-02)
 *        MASTER-COMPONENTS.md — TasksPage (E-P8-02)
 */

import React from 'react'
import { KanbanBoard } from '../features/tasks/KanbanBoard'

/**
 * Tasks page — renders the Kanban board under the app chrome.
 *
 * @example
 * ```tsx
 * <Route path="/tasks" element={<TasksPage />} />
 * ```
 */
export function TasksPage(): React.ReactElement {
  return (
    <div className="flex h-full flex-col">
      {/* Page header */}
      <div className="border-b border-border px-6 py-4">
        <h1 className="text-xl font-semibold">Tasks</h1>
        <p className="mt-0.5 text-sm text-muted-foreground">
          Kanban board — drag cards between columns or use the keyboard to move them.
        </p>
      </div>

      {/* Board — fills remaining height */}
      <div className="flex-1 overflow-hidden">
        <KanbanBoard />
      </div>
    </div>
  )
}
