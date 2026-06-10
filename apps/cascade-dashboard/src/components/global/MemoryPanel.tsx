/**
 * Purpose: GCI memory file-list panel — shows ~/.claude/memory/ (and project memory) files.
 * Inputs: none.
 * Outputs: FileListPanel wired to /api/gci/memory.
 * Constraints: Read-only display.
 * SPORT: T-P3-E02-09 MemoryPanel
 */
import { FileListPanel } from './FileListPanel'

export function MemoryPanel() {
  return (
    <FileListPanel
      title="Memory"
      endpoint="/api/gci/memory"
      emptyMessage="No memory files found."
    />
  )
}
