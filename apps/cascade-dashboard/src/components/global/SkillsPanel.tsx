/**
 * Purpose: GCI skills file-list panel — shows ~/.claude/skills/ bundles with search.
 * Inputs: none.
 * Outputs: FileListPanel wired to /api/gci/skills.
 * Constraints: Read-only display.
 * SPORT: T-P3-E02-09 SkillsPanel
 */
import { FileListPanel } from './FileListPanel'

export function SkillsPanel() {
  return (
    <FileListPanel
      title="Skills"
      endpoint="/api/gci/skills"
      emptyMessage="No skill bundles found."
    />
  )
}
