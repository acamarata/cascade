/**
 * Purpose: Left panel of DiffPanel — lists all legacy source files that contributed
 *   to a MergeSection, grouped by tool. Clicking a file fires onFileSelect so
 *   ProposedContent can highlight attributed text.
 *
 * Inputs:
 *   - sourceFiles: MergeSourceFile[] — source files for the active section.
 *   - selectedPath: string | null — currently highlighted file path.
 *   - onFileSelect: (path: string | null) => void — called on click; null = deselect.
 *
 * Outputs: Accordion-per-tool list. Each file row shows kind badge + path.
 *
 * Constraints:
 *   - No WizardContext reads. Props-only.
 *   - aria-expanded on accordion triggers (WCAG 2.1 AA).
 *   - Responsive: parent controls visibility at 800px breakpoint.
 *   - ≤300 lines.
 *
 * SPORT: MASTER-COMPONENTS.md — SourceFileList — merge-engine/
 * Task: T-P3-E03-28
 */

import { useState } from 'react'
import { FileText, ChevronDown, ChevronRight } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { MergeSourceFile } from '@/features/onboarding/merge/types'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface SourceFileListProps {
  sourceFiles: MergeSourceFile[]
  selectedPath: string | null
  onFileSelect: (path: string | null) => void
}

// ---------------------------------------------------------------------------
// Helper — group by toolId
// ---------------------------------------------------------------------------

function groupByTool(files: MergeSourceFile[]): Map<string, MergeSourceFile[]> {
  const map = new Map<string, MergeSourceFile[]>()
  for (const f of files) {
    const existing = map.get(f.toolId) ?? []
    existing.push(f)
    map.set(f.toolId, existing)
  }
  return map
}

// ---------------------------------------------------------------------------
// Sub-component: ToolGroup accordion
// ---------------------------------------------------------------------------

interface ToolGroupProps {
  toolId: string
  files: MergeSourceFile[]
  selectedPath: string | null
  onFileSelect: (path: string | null) => void
}

function ToolGroup({ toolId, files, selectedPath, onFileSelect }: ToolGroupProps) {
  const [open, setOpen] = useState(true)

  return (
    <div className="border border-border rounded-md overflow-hidden">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={`tool-group-${toolId}`}
        onClick={() => setOpen((v) => !v)}
        className={cn(
          'flex w-full items-center justify-between px-3 py-2 text-sm font-medium',
          'bg-muted/40 hover:bg-muted/70 transition-colors',
          'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
        )}
      >
        <span className="font-mono text-xs uppercase tracking-wide text-muted-foreground">
          {toolId}
        </span>
        <span className="flex items-center gap-1 text-muted-foreground">
          <span className="text-xs">{files.length}</span>
          {open ? (
            <ChevronDown className="h-3.5 w-3.5" aria-hidden="true" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5" aria-hidden="true" />
          )}
        </span>
      </button>

      {open && (
        <ul id={`tool-group-${toolId}`} className="divide-y divide-border/50">
          {files.map((file) => {
            const isSelected = selectedPath === file.path
            return (
              <li key={file.path}>
                <button
                  type="button"
                  onClick={() => onFileSelect(isSelected ? null : file.path)}
                  className={cn(
                    'flex w-full items-start gap-2 px-3 py-2 text-left text-xs transition-colors',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                    isSelected ? 'bg-yellow-50 dark:bg-yellow-900/20' : 'hover:bg-accent'
                  )}
                  aria-pressed={isSelected}
                >
                  <FileText
                    className="mt-0.5 h-3.5 w-3.5 shrink-0 text-muted-foreground"
                    aria-hidden="true"
                  />
                  <span className="flex min-w-0 flex-col gap-0.5">
                    <span className="truncate font-mono text-foreground">{file.path}</span>
                    <span className="rounded bg-muted px-1 py-0.5 text-[10px] text-muted-foreground w-fit">
                      {file.kind}
                    </span>
                  </span>
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// SourceFileList
// ---------------------------------------------------------------------------

/**
 * Groups source files by tool and renders an accordion per tool.
 * Clicking a file sets it as the highlight target in ProposedContent.
 */
export function SourceFileList({ sourceFiles, selectedPath, onFileSelect }: SourceFileListProps) {
  const grouped = groupByTool(sourceFiles)

  if (sourceFiles.length === 0) {
    return (
      <p className="px-3 py-4 text-sm text-muted-foreground">No source files for this section.</p>
    )
  }

  return (
    <div className="flex flex-col gap-2">
      {Array.from(grouped.entries()).map(([toolId, files]) => (
        <ToolGroup
          key={toolId}
          toolId={toolId}
          files={files}
          selectedPath={selectedPath}
          onFileSelect={onFileSelect}
        />
      ))}
    </div>
  )
}
