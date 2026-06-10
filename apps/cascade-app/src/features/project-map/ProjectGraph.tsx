/**
 * Purpose: Project graph tab — renders the projects→repos→apps hierarchy as a
 *   ReactFlow graph. Nodes are colour-coded by type. Clicking a node shows a
 *   popover with the path and an "Open in Finder" button.
 * Inputs:  get_project_graph IPC via getProjectGraph(). Auto-refreshes on focus.
 * Outputs: ReactFlow canvas with custom ProjectNode type.
 * Constraints:
 *   - ReactFlow fitView called after data load to prevent off-screen initial render.
 *   - Tauri shell::open used for "Open in Finder" — no-ops if path is empty.
 *   - Loading / error / empty states rendered.
 * SPORT: MASTER-COMPONENTS.md — ProjectGraph (T-P3-E07-13)
 */

import { useCallback, useEffect, useState } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  useNodesState,
  useEdgesState,
  useReactFlow,
  ReactFlowProvider,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { FolderOpen, Loader2 } from 'lucide-react'
import { open } from '@tauri-apps/plugin-shell'
import { getProjectGraph } from '../../types/maps'
import { projectGraphToFlow } from './mapTransforms'

// ── Custom node ───────────────────────────────────────────────────────────────

// Keep data as Record<string, unknown> so useNodesState([]) is compatible.
// Accessor helpers cast at point of use.
function ProjectNodeComponent({ data, selected }: NodeProps) {
  const [popoverOpen, setPopoverOpen] = useState(false)
  const label = data['label'] as string
  const nodeType = data['nodeType'] as string
  const path = data['path'] as string
  const color = data['color'] as string

  function handleOpenFinder() {
    if (path) {
      open(path).catch(() => {/* ignore shell errors */})
    }
    setPopoverOpen(false)
  }

  return (
    <div
      role="button"
      tabIndex={0}
      aria-label={`${nodeType} node: ${label}`}
      aria-pressed={selected}
      onClick={() => setPopoverOpen((v) => !v)}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') setPopoverOpen((v) => !v)
      }}
      className="relative flex h-full w-full cursor-pointer items-center gap-2 rounded-md border-2 px-3 transition-shadow"
      style={{
        background: selected ? `${color}33` : '#1e293b',
        borderColor: selected ? color : '#334155',
        boxShadow: selected ? `0 0 0 2px ${color}55` : undefined,
      }}
    >
      <span
        className="h-2.5 w-2.5 shrink-0 rounded-full"
        style={{ background: color }}
        aria-hidden="true"
      />
      <span className="truncate text-xs font-medium text-slate-200">{label}</span>
      <span className="ml-auto shrink-0 rounded bg-slate-700 px-1 py-0.5 text-[10px] text-slate-400">
        {nodeType}
      </span>

      {/* Popover */}
      {popoverOpen && (
        <div
          role="tooltip"
          className="absolute left-full top-0 z-50 ml-2 w-64 rounded-md border border-border bg-card p-3 shadow-lg"
          onClick={(e) => e.stopPropagation()}
        >
          <p className="mb-1 text-xs font-semibold text-foreground">{label}</p>
          <p className="mb-2 break-all text-[10px] text-muted-foreground">{path || '(no path)'}</p>
          {path && (
            <button
              type="button"
              onClick={handleOpenFinder}
              className="flex items-center gap-1.5 rounded border border-border px-2 py-1 text-xs text-muted-foreground hover:bg-accent/50 hover:text-foreground"
            >
              <FolderOpen className="h-3.5 w-3.5" aria-hidden="true" />
              Open in Finder
            </button>
          )}
        </div>
      )}
    </div>
  )
}

const nodeTypes = { projectNode: ProjectNodeComponent }

// ── Inner component (inside ReactFlowProvider) ────────────────────────────────

function ProjectGraphInner() {
  const [nodes, setNodes, onNodesChange] = useNodesState([])
  const [edges, setEdges, onEdgesChange] = useEdgesState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const { fitView } = useReactFlow()

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      setError(null)
      const data = await getProjectGraph()
      const { nodes: n, edges: e } = projectGraphToFlow(data)
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      setNodes(n as any)
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      setEdges(e as any)
      // fitView after state settles
      setTimeout(() => fitView({ padding: 0.15, duration: 300 }), 60)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [setNodes, setEdges, fitView])

  // Initial load
  useEffect(() => {
    void loadData()
  }, [loadData])

  // Auto-refresh on focus
  useEffect(() => {
    const handler = () => void loadData()
    window.addEventListener('focus', handler)
    return () => window.removeEventListener('focus', handler)
  }, [loadData])

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center" role="status" aria-live="polite">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" />
        <span className="ml-2 text-sm text-muted-foreground">Loading project graph…</span>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3" role="alert">
        <p className="text-sm text-destructive">Failed to load project graph</p>
        <p className="max-w-xs text-center text-xs text-muted-foreground">{error}</p>
        <button
          type="button"
          onClick={() => void loadData()}
          className="rounded border border-border px-3 py-1.5 text-xs hover:bg-accent/50"
        >
          Retry
        </button>
      </div>
    )
  }

  if (nodes.length === 0) {
    return (
      <div className="flex h-full items-center justify-center">
        <p className="text-sm text-muted-foreground">No projects found in ~/Sites.</p>
      </div>
    )
  }

  return (
    <ReactFlow
      nodes={nodes}
      edges={edges}
      onNodesChange={onNodesChange}
      onEdgesChange={onEdgesChange}
      nodeTypes={nodeTypes}
      fitView
      fitViewOptions={{ padding: 0.15 }}
      proOptions={{ hideAttribution: true }}
      aria-label="Project graph — projects, repos, and apps"
    >
      <Background gap={20} color="#1e293b" />
      <Controls />
    </ReactFlow>
  )
}

// ── Exported component ────────────────────────────────────────────────────────

/**
 * Project graph canvas. Wraps inner component in ReactFlowProvider.
 */
export function ProjectGraph() {
  return (
    <div className="h-full w-full" data-testid="project-graph">
      <ReactFlowProvider>
        <ProjectGraphInner />
      </ReactFlowProvider>
    </div>
  )
}
