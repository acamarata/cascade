/**
 * Purpose: /vault/memory route — wraps MemoryViewer in VaultProvider so
 *   setCurrentFile navigates to the source file in the vault editor.
 *   Reads memoryIndex from VaultContext. Provides loading/error pass-through.
 * Inputs:  None — reads VaultContext via useVault().
 * Outputs: Full-height memory browser page at /vault/memory.
 * Constraints:
 *   - Must be wrapped in VaultProvider (done here via MemoryPageWithProvider).
 *   - memoryIndex lives in VaultContext — no direct IPC from this component.
 *   - If VaultContext does not yet expose memoryIndex, falls back to empty array
 *     and sets loading=false so the UI renders the empty state rather than hanging.
 * SPORT: MASTER-COMPONENTS.md — MemoryPage (T-P3-E06-12)
 *        MASTER-ROUTES.md — /vault/memory (T-P3-E06-12)
 */

import React from 'react'
import { Brain } from 'lucide-react'
import { VaultProvider, useVault } from '../context/VaultContext'
import { MemoryViewer } from '../components/vault/MemoryViewer'

// ── Inner component (reads VaultContext) ─────────────────────────────────────

function MemoryPageInner(): React.ReactElement {
  const { memoryIndex, memoryLoading, memoryError, projectPaths, setCurrentFile } = useVault()

  const loading = memoryLoading
  const error = memoryError

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* Page heading */}
      <header className="flex shrink-0 items-center gap-2 border-b border-border px-6 py-3">
        <Brain className="h-5 w-5 text-muted-foreground" aria-hidden="true" />
        <h1 className="text-lg font-semibold">Memory</h1>
        <span className="text-sm text-muted-foreground">
          decisions · lessons · patterns · ideas · inbox
        </span>
      </header>

      {/* Memory viewer — fills remaining height */}
      <div className="flex-1 overflow-hidden">
        <MemoryViewer
          memoryIndex={memoryIndex}
          onOpenFile={(path) => void setCurrentFile(path)}
          loading={loading}
          error={error}
          projectPaths={projectPaths}
        />
      </div>
    </div>
  )
}

// ── Public export ─────────────────────────────────────────────────────────────

/**
 * Memory browser page — wraps inner component in a VaultProvider.
 * Exported at the route level; AppLayout provides the outer chrome.
 */
export function MemoryPage(): React.ReactElement {
  return (
    <VaultProvider>
      <MemoryPageInner />
    </VaultProvider>
  )
}
