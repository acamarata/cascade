/**
 * Purpose: /vault/graph route page — wraps GraphView in VaultProvider.
 *   Accessible from the vault toolbar via a "Graph" button.
 * Inputs:  None — reads VaultContext internally via GraphView.
 * Outputs: Full-height graph canvas.
 * Constraints: VaultProvider must wrap GraphView so context is available.
 * SPORT: MASTER-ROUTES.md — /vault/graph (T-P3-E06-07)
 */

import { useMemo } from 'react'
import { Link } from 'react-router-dom'
import { ArrowLeft } from 'lucide-react'
import { VaultProvider } from '../context/VaultContext'
import { GraphView } from '../features/vault/graph/GraphView'
import { useLinkIndex } from '../hooks/useLinkIndex'

function VaultGraphPageInner() {
  // Feed the graph real edges: invert the backlink index (target → sources)
  // into outgoing edges (source → targets) for the edgesSource seam.
  const backlinks = useLinkIndex()
  const edgesSource = useMemo(() => {
    if (!backlinks) return undefined
    const outgoing = new Map<string, string[]>()
    for (const [target, entries] of backlinks) {
      for (const entry of entries) {
        const targets = outgoing.get(entry.sourcePath) ?? []
        targets.push(target)
        outgoing.set(entry.sourcePath, targets)
      }
    }
    return outgoing
  }, [backlinks])

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Toolbar */}
      <header className="flex h-10 shrink-0 items-center gap-3 border-b border-border bg-card px-3">
        <Link
          to="/vault"
          className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground"
          aria-label="Back to vault"
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
          Vault
        </Link>
        <span className="text-xs font-medium text-foreground">Graph view</span>
      </header>

      {/* Graph canvas */}
      <div className="min-h-0 flex-1">
        <GraphView edgesSource={edgesSource} />
      </div>
    </div>
  )
}

export function VaultGraphPage() {
  return (
    <VaultProvider>
      <VaultGraphPageInner />
    </VaultProvider>
  )
}
