/**
 * Purpose: GCI references file-list panel — shows ~/.claude/references/ files with search.
 * Inputs: none.
 * Outputs: FileListPanel wired to /api/gci/references.
 * Constraints: Read-only display.
 * SPORT: T-P3-E02-09 ReferencesPanel
 */
import { FileListPanel } from './FileListPanel'

export function ReferencesPanel() {
  return (
    <FileListPanel
      title="References"
      endpoint="/api/gci/references"
      emptyMessage="No GCI references found."
    />
  )
}
