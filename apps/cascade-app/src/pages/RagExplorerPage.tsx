/**
 * Purpose: /rag-explorer route page — thin wrapper for RagExplorerPanel.
 * Inputs:  None.
 * Outputs: Full-height RagExplorerPanel.
 * Constraints: No extra chrome — AppLayout already provides sidebar/topbar.
 * SPORT: MASTER-ROUTES.md — /rag-explorer (T-P4-E01-27)
 */

import { RagExplorerPanel } from '../features/rag/RagExplorerPanel'

export function RagExplorerPage() {
  return <RagExplorerPanel />
}
