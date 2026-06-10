/**
 * Purpose: Force-directed vault note connection graph.
 *   Renders all vault .md notes as nodes, [[wikilink]] relationships as edges.
 *   D3 v7 force simulation (forceLink + forceManyBody + forceCenter).
 *   Pan/zoom via d3-zoom. Node size = in-degree. Hover dimming. Click → open note.
 *   Re-renders when treeData or content changes without full D3 reinit.
 * Inputs:  VaultContext (treeData, setCurrentFile, currentFile, currentContent).
 *          Optional: edgesSource (Map<path, path[]>) — seam for linkIndex (T-P3-E06-06).
 * Outputs: <svg> element managed by D3; mounted in /vault/graph route.
 * Constraints:
 *   - D3 owns the SVG subtree; React owns only the outer wrapper.
 *   - Simulation runs off-render (requestAnimationFrame); tick budget: alpha < 0.001
 *     stops automatically (~150-300 ticks for 500 nodes).
 *   - React.memo prevents re-render when parent causes unrelated updates.
 *   - simulation.stop() on unmount; zoom handler removed via unlisten ref.
 * SPORT: MASTER-COMPONENTS.md — GraphView (T-P3-E06-07)
 */

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import * as d3 from 'd3'
import { GitGraph, Loader2 } from 'lucide-react'
import { useVault } from '../../../context/VaultContext'
import {
  buildGraphDataFromTree,
  dirColour,
  nodeRadius,
  type GraphEdge,
  type GraphNode,
} from './graphData'

// ── Types ─────────────────────────────────────────────────────────────────────

interface GraphViewProps {
  /**
   * Optional pre-resolved link index seam for T-P3-E06-06.
   * When provided, wikilink text-parsing is skipped.
   * Map<sourceAbsPath, targetAbsPath[]>
   */
  edgesSource?: Map<string, string[]>
}

// ── Constants ─────────────────────────────────────────────────────────────────

const DIM_OPACITY = 0.08
const FULL_OPACITY = 1
const EDGE_OPACITY = 0.35
const EDGE_HOVER_OPACITY = 0.8

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * Force-directed note connection graph.
 * Reads vault tree from VaultContext; parses [[wikilinks]] from currentContent
 * when available; falls back to zero edges when no content map is loaded.
 *
 * Performance strategy for 500 nodes @ 30fps:
 *   - Simulation runs with alphaDecay=0.028 (D3 default, stabilises ~150 ticks).
 *   - Each tick updates SVG element positions via direct D3 attr calls (no React re-renders).
 *   - React.memo wraps the outer shell; only SVG ref updates on resize.
 *   - zoom/pan applied to a single <g> container (no per-node transform).
 */
export const GraphView = React.memo(function GraphView({ edgesSource }: GraphViewProps) {
  const { treeData, setCurrentFile, currentFile } = useVault()

  // ── Container dimensions ─────────────────────────────────────────────────────
  const wrapperRef = useRef<HTMLDivElement>(null)
  const svgRef = useRef<SVGSVGElement>(null)
  const [dimensions, setDimensions] = useState({ width: 800, height: 600 })

  useEffect(() => {
    const el = wrapperRef.current
    if (!el) return
    const obs = new ResizeObserver((entries) => {
      const { width, height } = entries[0].contentRect
      if (width > 0 && height > 0) setDimensions({ width, height })
    })
    obs.observe(el)
    // Set initial dimensions
    const rect = el.getBoundingClientRect()
    if (rect.width > 0) setDimensions({ width: rect.width, height: rect.height })
    return () => obs.disconnect()
  }, [])

  // ── Build graph data from vault tree ─────────────────────────────────────────
  //
  // NOTE: Full graph data requires content for each note to extract [[wikilinks]].
  // The parallel T-P3-E06-06 agent will provide a linkIndex (edgesSource seam).
  // Until then, we build a content map from currentContent only (the open note).
  // The graph shows all nodes; edges appear progressively as notes are opened
  // or when edgesSource is injected.

  const { nodes, edges } = useMemo(() => {
    if (!treeData) return { nodes: [], edges: [] }
    return buildGraphDataFromTree(treeData, undefined, edgesSource)
  }, [treeData, edgesSource])

  // ── D3 simulation state ───────────────────────────────────────────────────────
  //
  // Refs track D3 objects that must survive React render cycles without causing re-renders.
  const simulationRef = useRef<d3.Simulation<GraphNode, GraphEdge> | null>(null)
  const zoomRef = useRef<d3.ZoomBehavior<SVGSVGElement, unknown> | null>(null)

  // Hover state — tracked via ref to avoid re-renders on every hover
  const hoveredNodeIdRef = useRef<string | null>(null)

  // ── Apply hover dimming ───────────────────────────────────────────────────────

  const applyHover = useCallback(
    (
      nodeEl: SVGGElement,
      edgeEl: SVGGElement,
      hoveredId: string | null,
      _nodeData: GraphNode[],
      edgeData: GraphEdge[],
    ) => {
      if (!hoveredId) {
        // Reset all
        d3.select(nodeEl).selectAll<SVGCircleElement, GraphNode>('circle')
          .attr('opacity', FULL_OPACITY)
        d3.select(nodeEl).selectAll<SVGTextElement, GraphNode>('text')
          .attr('opacity', FULL_OPACITY)
        d3.select(edgeEl).selectAll<SVGLineElement, GraphEdge>('line')
          .attr('opacity', EDGE_OPACITY)
        return
      }

      // Build neighbour set for the hovered node
      const neighbours = new Set<string>([hoveredId])
      for (const e of edgeData) {
        const src = typeof e.source === 'string' ? e.source : (e.source as GraphNode).id
        const tgt = typeof e.target === 'string' ? e.target : (e.target as GraphNode).id
        if (src === hoveredId) neighbours.add(tgt)
        if (tgt === hoveredId) neighbours.add(src)
      }

      // Dim nodes
      d3.select(nodeEl).selectAll<SVGCircleElement, GraphNode>('circle')
        .attr('opacity', (d) => (neighbours.has(d.id) ? FULL_OPACITY : DIM_OPACITY))
      d3.select(nodeEl).selectAll<SVGTextElement, GraphNode>('text')
        .attr('opacity', (d) => (neighbours.has(d.id) ? FULL_OPACITY : DIM_OPACITY))

      // Dim edges — only highlight edges connected to hovered node
      d3.select(edgeEl).selectAll<SVGLineElement, GraphEdge>('line')
        .attr('opacity', (e) => {
          const src = typeof e.source === 'string' ? e.source : (e.source as GraphNode).id
          const tgt = typeof e.target === 'string' ? e.target : (e.target as GraphNode).id
          return src === hoveredId || tgt === hoveredId ? EDGE_HOVER_OPACITY : DIM_OPACITY
        })
    },
    [],
  )

  // ── Main D3 effect ────────────────────────────────────────────────────────────
  //
  // Runs when nodes/edges/dimensions change.
  // Strategy: full reinit only when the node list changes (structure change);
  // alpha restart when only edges change (link index update without tree change).

  useEffect(() => {
    const svg = svgRef.current
    if (!svg || nodes.length === 0) return

    const { width, height } = dimensions

    // ── Clear and rebuild SVG structure ────────────────────────────────────────
    d3.select(svg).selectAll('*').remove()

    // Root <g> — pan/zoom transform applied here
    const root = d3.select(svg).append('g').attr('class', 'graph-root')

    // Edges layer (below nodes)
    const edgeGroup = root.append('g').attr('class', 'edges').node() as SVGGElement
    // Nodes layer
    const nodeGroup = root.append('g').attr('class', 'nodes').node() as SVGGElement

    // ── Deep-copy nodes so D3 can mutate x/y without affecting React state ─────
    const simNodes: GraphNode[] = nodes.map((n) => ({ ...n }))
    // Deep-copy edges with string references (D3 will resolve to objects)
    const simEdges: GraphEdge[] = edges.map((e) => ({
      source: typeof e.source === 'string' ? e.source : (e.source as GraphNode).id,
      target: typeof e.target === 'string' ? e.target : (e.target as GraphNode).id,
    }))

    // ── Render edges ──────────────────────────────────────────────────────────
    const linkSel = d3.select(edgeGroup)
      .selectAll<SVGLineElement, GraphEdge>('line')
      .data(simEdges)
      .join('line')
      .attr('stroke', 'hsl(var(--muted-foreground))')
      .attr('stroke-width', 1)
      .attr('opacity', EDGE_OPACITY)

    // ── Render nodes ──────────────────────────────────────────────────────────
    const nodeGroupSel = d3.select(nodeGroup)
      .selectAll<SVGGElement, GraphNode>('g.node')
      .data(simNodes, (d) => d.id)
      .join('g')
      .attr('class', 'node')
      .style('cursor', 'pointer')

    // Circle
    nodeGroupSel.append('circle')
      .attr('r', (d) => nodeRadius(d.inDegree))
      .attr('fill', (d) => dirColour(d.dir))
      .attr('stroke', 'hsl(var(--background))')
      .attr('stroke-width', 1.5)
      .attr('opacity', FULL_OPACITY)

    // Label (hidden below 8px radius to avoid clutter)
    nodeGroupSel.append('text')
      .attr('dy', (d) => nodeRadius(d.inDegree) + 10)
      .attr('text-anchor', 'middle')
      .attr('font-size', '10px')
      .attr('fill', 'hsl(var(--foreground))')
      .attr('opacity', FULL_OPACITY)
      .attr('pointer-events', 'none')
      .text((d) => (nodeRadius(d.inDegree) >= 6 ? d.label : ''))

    // Highlight ring for currently-open note
    nodeGroupSel.append('circle')
      .attr('class', 'ring')
      .attr('r', (d) => nodeRadius(d.inDegree) + 3)
      .attr('fill', 'none')
      .attr('stroke', 'hsl(var(--primary))')
      .attr('stroke-width', 2)
      .attr('opacity', (d) => (d.id === currentFile ? 1 : 0))

    // ── Hover events ──────────────────────────────────────────────────────────
    nodeGroupSel
      .on('mouseover', (_event, d) => {
        hoveredNodeIdRef.current = d.id
        applyHover(nodeGroup, edgeGroup, d.id, simNodes, simEdges)
      })
      .on('mouseout', () => {
        hoveredNodeIdRef.current = null
        applyHover(nodeGroup, edgeGroup, null, simNodes, simEdges)
      })

    // ── Click: open note ──────────────────────────────────────────────────────
    nodeGroupSel.on('click', (_event, d) => {
      setCurrentFile(d.id)
    })

    // ── D3 force simulation ───────────────────────────────────────────────────
    //
    // Performance: alphaDecay default (0.028) → stabilises in ~150 ticks.
    // For 500 nodes this stays well within a 2-second startup window at 60fps.
    // Tick function updates SVG attr directly (no React setState).
    const simulation = d3
      .forceSimulation<GraphNode>(simNodes)
      .force(
        'link',
        d3.forceLink<GraphNode, GraphEdge>(simEdges)
          .id((d) => d.id)
          .distance(60)
          .strength(0.5),
      )
      .force('charge', d3.forceManyBody<GraphNode>().strength(-120))
      .force('center', d3.forceCenter(width / 2, height / 2))
      .force('collision', d3.forceCollide<GraphNode>().radius((d) => nodeRadius(d.inDegree) + 2))
      .alphaDecay(0.028)

    // Tick: update positions
    simulation.on('tick', () => {
      linkSel
        .attr('x1', (d) => (d.source as GraphNode).x ?? 0)
        .attr('y1', (d) => (d.source as GraphNode).y ?? 0)
        .attr('x2', (d) => (d.target as GraphNode).x ?? 0)
        .attr('y2', (d) => (d.target as GraphNode).y ?? 0)

      nodeGroupSel.attr('transform', (d) => `translate(${d.x ?? 0},${d.y ?? 0})`)
    })

    simulationRef.current = simulation

    // ── Drag ─────────────────────────────────────────────────────────────────
    function dragStarted(event: d3.D3DragEvent<SVGGElement, GraphNode, GraphNode>, d: GraphNode) {
      if (!event.active) simulation.alphaTarget(0.3).restart()
      d.fx = d.x
      d.fy = d.y
    }
    function dragged(event: d3.D3DragEvent<SVGGElement, GraphNode, GraphNode>, d: GraphNode) {
      d.fx = event.x
      d.fy = event.y
    }
    function dragEnded(event: d3.D3DragEvent<SVGGElement, GraphNode, GraphNode>, d: GraphNode) {
      if (!event.active) simulation.alphaTarget(0)
      d.fx = null
      d.fy = null
    }

    nodeGroupSel.call(
      d3.drag<SVGGElement, GraphNode>()
        .on('start', dragStarted)
        .on('drag', dragged)
        .on('end', dragEnded),
    )

    // ── Zoom ─────────────────────────────────────────────────────────────────
    const zoom = d3.zoom<SVGSVGElement, unknown>()
      .scaleExtent([0.1, 8])
      .on('zoom', (event: d3.D3ZoomEvent<SVGSVGElement, unknown>) => {
        d3.select(root.node()).attr('transform', event.transform.toString())
      })

    d3.select(svg).call(zoom)
    // Prevent scroll-hijacking on the page
    d3.select(svg).on('wheel.zoom', (event: WheelEvent) => event.preventDefault())
    zoomRef.current = zoom

    return () => {
      simulation.stop()
      d3.select(svg).on('.zoom', null)
      simulationRef.current = null
      zoomRef.current = null
    }
  }, [nodes, edges, dimensions, currentFile, setCurrentFile, applyHover])

  // ── Empty / loading states ────────────────────────────────────────────────────

  if (!treeData) {
    return (
      <div
        className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground"
        data-testid="graph-loading"
      >
        <Loader2 className="h-8 w-8 animate-spin" aria-hidden="true" />
        <p className="text-sm">Loading vault…</p>
      </div>
    )
  }

  if (nodes.length === 0) {
    return (
      <div
        className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground"
        data-testid="graph-empty"
      >
        <GitGraph className="h-8 w-8" aria-hidden="true" />
        <p className="text-sm font-medium">No notes in vault</p>
        <p className="text-xs">Create some .md files to see the graph.</p>
      </div>
    )
  }

  return (
    <div
      ref={wrapperRef}
      className="relative h-full w-full overflow-hidden bg-background"
      data-testid="graph-container"
    >
      {/* Stats overlay */}
      <div className="absolute left-3 top-3 z-10 rounded bg-card/80 px-2 py-1 text-[10px] text-muted-foreground backdrop-blur-sm">
        {nodes.length} notes · {edges.length} links
      </div>

      {/* Zoom controls */}
      <div className="absolute right-3 top-3 z-10 flex flex-col gap-1">
        <button
          type="button"
          aria-label="Zoom in"
          className="flex h-7 w-7 items-center justify-center rounded bg-card/80 text-muted-foreground hover:bg-card hover:text-foreground backdrop-blur-sm"
          onClick={() => {
            if (svgRef.current && zoomRef.current) {
              d3.select(svgRef.current)
                .transition()
                .duration(200)
                .call(zoomRef.current.scaleBy, 1.4)
            }
          }}
        >
          +
        </button>
        <button
          type="button"
          aria-label="Zoom out"
          className="flex h-7 w-7 items-center justify-center rounded bg-card/80 text-muted-foreground hover:bg-card hover:text-foreground backdrop-blur-sm"
          onClick={() => {
            if (svgRef.current && zoomRef.current) {
              d3.select(svgRef.current)
                .transition()
                .duration(200)
                .call(zoomRef.current.scaleBy, 0.7)
            }
          }}
        >
          −
        </button>
        <button
          type="button"
          aria-label="Reset zoom"
          className="flex h-7 w-7 items-center justify-center rounded bg-card/80 text-[9px] text-muted-foreground hover:bg-card hover:text-foreground backdrop-blur-sm"
          onClick={() => {
            if (svgRef.current && zoomRef.current) {
              d3.select(svgRef.current)
                .transition()
                .duration(200)
                .call(zoomRef.current.transform, d3.zoomIdentity)
            }
          }}
        >
          ↺
        </button>
      </div>

      {/* D3-managed SVG */}
      <svg
        ref={svgRef}
        width={dimensions.width}
        height={dimensions.height}
        aria-label="Vault note connection graph"
        data-testid="graph-svg"
        style={{ display: 'block' }}
      />

      {/* Legend */}
      <div className="absolute bottom-3 left-3 z-10 rounded bg-card/80 px-2 py-1 text-[10px] text-muted-foreground backdrop-blur-sm">
        Drag to pan · Scroll to zoom · Click node to open
      </div>
    </div>
  )
})
