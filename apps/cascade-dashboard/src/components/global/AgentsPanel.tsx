/**
 * Purpose: GCI agents file-list panel — shows ~/.claude/agents/ definitions with search.
 * Inputs: none.
 * Outputs: FileListPanel wired to /api/gci/agents.
 * Constraints: Read-only display.
 * SPORT: T-P3-E02-09 AgentsPanel
 */
import { FileListPanel } from './FileListPanel'

export function AgentsPanel() {
  return (
    <FileListPanel
      title="Agents"
      endpoint="/api/gci/agents"
      emptyMessage="No agent definitions found."
    />
  )
}
