/**
 * Purpose: GCI rules file-list panel — shows ~/.claude/rules/ files with search.
 * Inputs: none.
 * Outputs: FileListPanel wired to /api/gci/rules.
 * Constraints: Read-only display. Search filters purely client-side.
 * SPORT: T-P3-E02-09 RulesPanel
 */
import { FileListPanel } from './FileListPanel'

export function RulesPanel() {
  return (
    <FileListPanel
      title="Rules"
      endpoint="/api/gci/rules"
      emptyMessage="No GCI rules found."
    />
  )
}
