/**
 * PewsPage.tsx — Route wrapper for the PEWS mutation UI.
 *
 * Purpose: Thin page-level wrapper hosting both the AllProjectsBoard (default
 *   view) and PewsTreePanel (single-project tree). A view toggle switches between
 *   them. Clicking "Open" on a board row switches to the tree for that project.
 *
 * Inputs:  None (route-level; no props).
 * Outputs: Full-height PEWS page with board / tree toggle.
 * Constraints:
 *   - Board is the default view (?view=board).
 *   - Tree view retains the project root selected in the board.
 *   - ?view= query param drives the active view so back/forward works.
 *   - h-full — inherits full layout height from AppLayout.
 * SPORT: MASTER-ROUTES.md — /pews (E-P8-05)
 */

import { useCallback, useState } from 'react'
import { LayoutGrid, Network } from 'lucide-react'
import { PewsTreePanel } from '../features/pews/PewsTreePanel'
import { AllProjectsBoard } from '../features/pews/AllProjectsBoard'

type PewsView = 'board' | 'tree'

/**
 * Purpose: Route entry-point for the PEWS page.
 *   Board = default view showing all projects.
 *   Tree  = single-project drill-down (switched via "Open" or the toggle button).
 * Inputs:  None.
 * Outputs: Full-height panel; view state owned locally.
 */
export function PewsPage() {
  const [view, setView] = useState<PewsView>('board')
  // When the user opens a project from the board, persist the path so the tree
  // starts on the right project.
  const [treeRoot, setTreeRoot] = useState<string | undefined>(undefined)

  const handleOpenProject = useCallback((projectPath: string) => {
    setTreeRoot(projectPath)
    setView('tree')
  }, [])

  return (
    <div className="flex h-full flex-col overflow-hidden" data-page="pews">
      {/* View toggle bar */}
      <div className="flex shrink-0 items-center gap-1 border-b border-border bg-muted/20 px-3 py-1.5">
        <button
          type="button"
          onClick={() => setView('board')}
          aria-pressed={view === 'board'}
          className={[
            'flex items-center gap-1.5 rounded px-2.5 py-1 text-[11px] font-medium transition-colors',
            view === 'board'
              ? 'bg-card text-foreground shadow-sm'
              : 'text-muted-foreground hover:text-foreground hover:bg-accent/50',
          ].join(' ')}
        >
          <LayoutGrid className="h-3.5 w-3.5" aria-hidden="true" />
          All Projects
        </button>
        <button
          type="button"
          onClick={() => setView('tree')}
          aria-pressed={view === 'tree'}
          className={[
            'flex items-center gap-1.5 rounded px-2.5 py-1 text-[11px] font-medium transition-colors',
            view === 'tree'
              ? 'bg-card text-foreground shadow-sm'
              : 'text-muted-foreground hover:text-foreground hover:bg-accent/50',
          ].join(' ')}
        >
          <Network className="h-3.5 w-3.5" aria-hidden="true" />
          Phase Tree
        </button>
      </div>

      {/* View content */}
      <div className="flex-1 overflow-hidden">
        {view === 'board' ? (
          <AllProjectsBoard onOpenProject={handleOpenProject} />
        ) : (
          <PewsTreePanel initialPhaseRoot={treeRoot} />
        )}
      </div>
    </div>
  )
}
