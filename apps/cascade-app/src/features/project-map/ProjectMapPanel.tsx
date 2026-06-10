/**
 * Purpose: Project map panel — three-tab view for project graph, cascade tier
 *   chain, and PEWS DAG visualizations. Project selector drives tier tree
 *   and PEWS DAG roots.
 * Inputs:  None (self-contained; reads IPC via sub-components).
 * Outputs: Radix Tabs with ProjectGraph / CascadeTierTree / PewsDag.
 * Constraints:
 *   - All three tabs call refetch on window focus (delegated to sub-components).
 *   - Default projectRoot = ~/Sites/acamarata/cascade (always present in dev).
 *   - Tab switching uses Radix @radix-ui/react-tabs (already installed).
 *   - Lazy imports for ProjectGraph and PewsDag to limit initial bundle size.
 * SPORT: MASTER-COMPONENTS.md — ProjectMapPanel (T-P3-E07-13)
 */

import { lazy, Suspense, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { GitFork, Layers, Network } from 'lucide-react'
import { CascadeTierTree } from './CascadeTierTree'

// Lazy-load graph views to avoid inflating initial bundle
const ProjectGraph = lazy(() => import('./ProjectGraph').then((m) => ({ default: m.ProjectGraph })))
const PewsDag = lazy(() => import('./PewsDag').then((m) => ({ default: m.PewsDag })))

// ── Project selector ──────────────────────────────────────────────────────────

/** Well-known cascade project roots for the selector. */
const HOME = '/Users/admin'

const KNOWN_ROOTS = [
  { label: 'cascade (acamarata)', value: `${HOME}/Sites/acamarata/cascade` },
  { label: 'nself', value: `${HOME}/Sites/nself` },
  { label: 'ummeco', value: `${HOME}/Sites/ummeco` },
  { label: 'unyeco', value: `${HOME}/Sites/unyeco` },
]

function fallbackLoader() {
  return (
    <div className="flex h-full items-center justify-center">
      <div className="h-4 w-4 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-muted-foreground" />
    </div>
  )
}

// ── Panel ─────────────────────────────────────────────────────────────────────

export function ProjectMapPanel() {
  const [activeTab, setActiveTab] = useState('project-graph')
  const [projectRoot, setProjectRoot] = useState(KNOWN_ROOTS[0]!.value)

  return (
    <div
      className="flex h-full flex-col overflow-hidden bg-background"
      data-testid="project-map-panel"
    >
      {/* Header */}
      <header className="flex shrink-0 flex-wrap items-center gap-3 border-b border-border bg-card px-4 py-2.5">
        <Network className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-sm font-semibold text-foreground">Project Maps</h1>

        {/* Project selector */}
        <div className="ml-auto flex items-center gap-2">
          <label htmlFor="project-root-select" className="text-xs text-muted-foreground">
            Root:
          </label>
          <select
            id="project-root-select"
            value={projectRoot}
            onChange={(e) => setProjectRoot(e.target.value)}
            className="rounded border border-border bg-card px-2 py-1 text-xs text-foreground focus:outline-none focus:ring-1 focus:ring-ring"
            aria-label="Select project root"
          >
            {KNOWN_ROOTS.map((r) => (
              <option key={r.value} value={r.value}>
                {r.label}
              </option>
            ))}
          </select>
        </div>
      </header>

      {/* Tabs */}
      <Tabs.Root
        value={activeTab}
        onValueChange={setActiveTab}
        className="flex min-h-0 flex-1 flex-col"
      >
        {/* Tab list */}
        <Tabs.List
          className="flex shrink-0 gap-1 border-b border-border bg-card/50 px-4"
          aria-label="Map views"
        >
          <Tabs.Trigger
            value="project-graph"
            className="flex items-center gap-1.5 border-b-2 border-transparent px-3 py-2 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground data-[state=active]:border-primary data-[state=active]:text-foreground"
          >
            <GitFork className="h-3.5 w-3.5" aria-hidden="true" />
            Project Graph
          </Tabs.Trigger>

          <Tabs.Trigger
            value="cascade-tiers"
            className="flex items-center gap-1.5 border-b-2 border-transparent px-3 py-2 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground data-[state=active]:border-primary data-[state=active]:text-foreground"
          >
            <Layers className="h-3.5 w-3.5" aria-hidden="true" />
            Cascade Tiers
          </Tabs.Trigger>

          <Tabs.Trigger
            value="pews-dag"
            className="flex items-center gap-1.5 border-b-2 border-transparent px-3 py-2 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground data-[state=active]:border-primary data-[state=active]:text-foreground"
          >
            <Network className="h-3.5 w-3.5" aria-hidden="true" />
            PEWS DAG
          </Tabs.Trigger>
        </Tabs.List>

        {/* Tab content */}
        <Tabs.Content
          value="project-graph"
          className="min-h-0 flex-1"
          forceMount
          style={{ display: activeTab === 'project-graph' ? 'block' : 'none' }}
        >
          <div className="h-full w-full">
            <Suspense fallback={fallbackLoader()}>
              <ProjectGraph />
            </Suspense>
          </div>
        </Tabs.Content>

        <Tabs.Content
          value="cascade-tiers"
          className="min-h-0 flex-1 overflow-auto"
          forceMount
          style={{ display: activeTab === 'cascade-tiers' ? 'block' : 'none' }}
        >
          <CascadeTierTree projectRoot={projectRoot} />
        </Tabs.Content>

        <Tabs.Content
          value="pews-dag"
          className="min-h-0 flex-1"
          forceMount
          style={{ display: activeTab === 'pews-dag' ? 'block' : 'none' }}
        >
          <div className="h-full w-full">
            <Suspense fallback={fallbackLoader()}>
              <PewsDag phaseRoot={projectRoot} />
            </Suspense>
          </div>
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}
