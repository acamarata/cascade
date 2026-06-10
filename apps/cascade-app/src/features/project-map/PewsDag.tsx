/**
 * Purpose: PEWS DAG tab — renders the ticket dependency graph using ReactFlow.
 *   Nodes are coloured by status (pending/in-progress/done/blocked).
 *   Layout is LR (left-to-right) based on topological column assignment.
 *   Status legend shown below the graph.
 * Inputs:  phaseRoot — absolute path to the project root. Auto-refreshes on focus.
 * Outputs: ReactFlow canvas with custom TicketNode type; status legend strip.
 * Constraints:
 *   - ReactFlow fitView called after data load.
 *   - No dagre — column/row computed in pewsDagToFlow().
 *   - Nodes show ticket ID + truncated title.
 * SPORT: MASTER-COMPONENTS.md — PewsDag (T-P3-E07-13)
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
  MarkerType,
  type Node,
  type Edge,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { Loader2 } from 'lucide-react'
import { getPewsDag } from '../../types/maps'
import { pewsDagToFlow, STATUS_COLORS } from './mapTransforms'

// ── Custom ticket node ────────────────────────────────────────────────────────

type TicketNodeData = Record<string, unknown> & {
  label: string
  ticketId: string
  status: string
  color: string
}

type TicketNode = Node<TicketNodeData>
type TicketEdge = Edge

function TicketNodeComponent({ data }: NodeProps<TicketNode>) {
  const label = data.label as string
  const ticketId = data.ticketId as string
  const status = data.status as string
  const color = data.color as string
  return (
    <div
      className="flex h-full w-full flex-col justify-center rounded-md border-2 px-3 py-2"
      style={{
        background: `${color}22`,
        borderColor: color,
      }}
      aria-label={`Ticket ${ticketId}: ${label}, status: ${status}`}
    >
      <span className="mb-0.5 truncate text-[10px] font-mono text-muted-foreground">
        {ticketId}
      </span>
      <span className="truncate text-xs font-medium text-slate-200">{label}</span>
      <span
        className="mt-0.5 truncate text-[9px] font-medium uppercase tracking-wide"
        style={{ color }}
      >
        {status}
      </span>
    </div>
  )
}

const nodeTypes = { ticketNode: TicketNodeComponent }

// ── Status legend ─────────────────────────────────────────────────────────────

const LEGEND_ITEMS = [
  { status: 'pending', label: 'Pending' },
  { status: 'in-progress', label: 'In Progress' },
  { status: 'done', label: 'Done' },
  { status: 'blocked', label: 'Blocked' },
] as const

function StatusLegend() {
  return (
    <div
      className="flex items-center gap-4 border-t border-border bg-background/80 px-4 py-2"
      aria-label="Status legend"
      role="list"
    >
      {LEGEND_ITEMS.map(({ status, label }) => (
        <span
          key={status}
          className="flex items-center gap-1.5 text-[10px] text-muted-foreground"
          role="listitem"
        >
          <span
            className="h-2.5 w-2.5 rounded-sm"
            style={{ background: STATUS_COLORS[status] }}
            aria-hidden="true"
          />
          {label}
        </span>
      ))}
    </div>
  )
}

// ── Inner (inside ReactFlowProvider) ─────────────────────────────────────────

interface PewsDagInnerProps {
  phaseRoot: string
}

function PewsDagInner({ phaseRoot }: PewsDagInnerProps) {
  const [nodes, setNodes, onNodesChange] = useNodesState<TicketNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<TicketEdge>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const { fitView } = useReactFlow()

  const loadData = useCallback(async () => {
    try {
      setLoading(true)
      setError(null)
      const data = await getPewsDag(phaseRoot)
      const { nodes: n, edges: e } = pewsDagToFlow(data)
      // Add arrowhead markers to all edges
      const edgesWithArrows = e.map((edge) => ({
        ...edge,
        markerEnd: { type: MarkerType.ArrowClosed },
      }))
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      setNodes(n as any)
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      setEdges(edgesWithArrows as any)
      setTimeout(() => fitView({ padding: 0.12, duration: 300 }), 60)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setLoading(false)
    }
  }, [phaseRoot, setNodes, setEdges, fitView])

  useEffect(() => {
    void loadData()
  }, [loadData])

  useEffect(() => {
    const handler = () => void loadData()
    window.addEventListener('focus', handler)
    return () => window.removeEventListener('focus', handler)
  }, [loadData])

  if (loading) {
    return (
      <div className="flex h-full items-center justify-center" role="status" aria-live="polite">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" aria-hidden="true" />
        <span className="ml-2 text-sm text-muted-foreground">Loading PEWS DAG…</span>
      </div>
    )
  }

  if (error) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3" role="alert">
        <p className="text-sm text-destructive">Failed to load PEWS DAG</p>
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
        <p className="text-sm text-muted-foreground">No PEWS tickets found for this phase root.</p>
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
      fitViewOptions={{ padding: 0.12 }}
      proOptions={{ hideAttribution: true }}
      aria-label="PEWS ticket dependency DAG"
    >
      <Background gap={24} color="#1e293b" />
      <Controls />
    </ReactFlow>
  )
}

// ── Exported component ────────────────────────────────────────────────────────

interface PewsDagProps {
  /** Absolute path to the project root. */
  phaseRoot: string
}

export function PewsDag({ phaseRoot }: PewsDagProps) {
  return (
    <div className="flex h-full flex-col overflow-hidden" data-testid="pews-dag">
      <div className="min-h-0 flex-1">
        <ReactFlowProvider>
          <PewsDagInner phaseRoot={phaseRoot} />
        </ReactFlowProvider>
      </div>
      <StatusLegend />
    </div>
  )
}
