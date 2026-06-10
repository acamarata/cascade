/**
 * Purpose: Right-panel preview for a selected template.
 *   Shows frontmatter metadata header and Markdown body via react-markdown.
 *   When the template has an upgrade available (upgradeable=true), shows an upgrade
 *   info box above the action bar with changed section counts and an Upgrade button.
 *   Apply button at bottom — opens ApplyDialog (caller wires via onApply).
 * Inputs:
 *   selectedEntry (nullable) — currently selected template
 *   onApply(entry) — called when user clicks Apply; caller opens ApplyDialog
 *   upgradeable — whether a newer version is available for the applied template
 *   oldVersion — currently applied version (shown in upgrade box)
 *   newVersion — available newer version (shown in upgrade box)
 *   changedSections — count or list summary shown in upgrade info
 *   onUpgrade(entry) — called when user clicks Upgrade; caller opens UpgradeConfirmDialog
 * Outputs: Preview panel with metadata table + rendered Markdown + action bar.
 * Constraints:
 *   - react-markdown + remark-gfm for body rendering (GitHub Flavored Markdown).
 *   - Apply button always visible; disabled + aria-disabled when entry is null.
 *   - Scroll: metadata + body scroll together in overflow-y-auto container.
 *   - Upgrade button only visible when upgradeable=true and selectedEntry is set.
 * SPORT: MASTER-COMPONENTS.md — TemplatePreview
 */

import React from 'react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { FileText, ArrowUp } from 'lucide-react'
import type { TemplateEntryIpc } from '../../types/templates'

interface TemplatePreviewProps {
  selectedEntry: TemplateEntryIpc | null
  /** Called when the user clicks Apply. Caller opens ApplyDialog. */
  onApply: (entry: TemplateEntryIpc) => void
  /** Whether a newer version of this template is available. */
  upgradeable?: boolean
  /** Currently applied version string (displayed in upgrade info box). */
  oldVersion?: string
  /** Available newer version string (displayed in upgrade info box). */
  newVersion?: string
  /** Number of sections that will change on upgrade (displayed in upgrade info box). */
  changedSectionCount?: number
  /** Called when the user clicks Upgrade. Caller opens UpgradeConfirmDialog. */
  onUpgrade?: (entry: TemplateEntryIpc) => void
}

/**
 * Metadata row for the preview header.
 */
function MetaRow({ label, value }: { label: string; value: React.ReactNode }): React.ReactElement {
  return (
    <tr>
      <th
        scope="row"
        className="py-0.5 pr-3 text-left text-xs font-medium text-muted-foreground whitespace-nowrap"
      >
        {label}
      </th>
      <td className="py-0.5 text-xs text-foreground">{value}</td>
    </tr>
  )
}

/**
 * Empty-selection placeholder.
 */
function NoSelection(): React.ReactElement {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-3 p-8 text-center">
      <FileText className="h-10 w-10 text-muted-foreground/30" aria-hidden="true" />
      <p className="text-sm text-muted-foreground">Select a template to preview its details.</p>
    </div>
  )
}

/**
 * TemplatePreview panel.
 */
export function TemplatePreview({
  selectedEntry,
  onApply,
  upgradeable = false,
  oldVersion,
  newVersion,
  changedSectionCount,
  onUpgrade,
}: TemplatePreviewProps): React.ReactElement {
  const canApply = selectedEntry !== null
  const showUpgradeBox = upgradeable && selectedEntry !== null && oldVersion && newVersion

  return (
    <section
      aria-label="Template preview"
      className="flex h-full flex-col border-l border-border bg-card"
    >
      {/* Scrollable content area */}
      <div className="flex-1 overflow-y-auto">
        {selectedEntry === null ? (
          <NoSelection />
        ) : (
          <div className="p-4">
            {/* Frontmatter metadata */}
            <div className="mb-4 rounded-md border border-border bg-muted/30 p-3">
              <table aria-label="Template metadata">
                <tbody>
                  <MetaRow label="ID" value={selectedEntry.id} />
                  <MetaRow label="Version" value={`v${selectedEntry.version}`} />
                  <MetaRow label="Tier" value={selectedEntry.tier} />
                  {selectedEntry.stacks.length > 0 && (
                    <MetaRow label="Stacks" value={selectedEntry.stacks.join(', ')} />
                  )}
                  {selectedEntry.projectShapes.length > 0 && (
                    <MetaRow label="Shapes" value={selectedEntry.projectShapes.join(', ')} />
                  )}
                  {selectedEntry.extends && (
                    <MetaRow label="Extends" value={selectedEntry.extends} />
                  )}
                  {selectedEntry.minCascadeVersion && (
                    <MetaRow
                      label="Min cascade"
                      value={selectedEntry.minCascadeVersion}
                    />
                  )}
                </tbody>
              </table>
            </div>

            {/* Upgrade info box */}
            {showUpgradeBox && (
              <div
                aria-label="Upgrade available"
                className="mb-4 flex items-start justify-between gap-3 rounded-md border border-amber-300 bg-amber-50 p-3 dark:border-amber-700 dark:bg-amber-900/20"
              >
                <div>
                  <p className="text-xs font-medium text-amber-800 dark:text-amber-300">
                    This template was applied at v{oldVersion}. v{newVersion} is available
                    {changedSectionCount !== undefined
                      ? ` — ${changedSectionCount} changed section${changedSectionCount !== 1 ? 's' : ''}`
                      : ''}
                    .
                  </p>
                  <p className="mt-0.5 text-xs text-amber-700 dark:text-amber-400">
                    Sections you edited manually are preserved unless force is enabled.
                  </p>
                </div>
                {onUpgrade && (
                  <button
                    type="button"
                    onClick={() => onUpgrade(selectedEntry)}
                    aria-label={`Upgrade ${selectedEntry.id} from v${oldVersion} to v${newVersion}`}
                    className="inline-flex shrink-0 items-center gap-1 rounded-md border border-amber-400 bg-amber-100 px-2.5 py-1.5 text-xs font-medium text-amber-900 transition-colors hover:bg-amber-200 dark:border-amber-600 dark:bg-amber-800/30 dark:text-amber-200 dark:hover:bg-amber-800/50"
                  >
                    <ArrowUp className="h-3 w-3" aria-hidden="true" />
                    Upgrade
                  </button>
                )}
              </div>
            )}

            {/* Markdown body */}
            <article
              className="prose prose-sm dark:prose-invert max-w-none text-foreground"
              aria-label="Template content"
            >
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{selectedEntry.body}</ReactMarkdown>
            </article>
          </div>
        )}
      </div>

      {/* Apply button — always visible, disabled when no selection */}
      <div className="border-t border-border p-3">
        <button
          type="button"
          disabled={!canApply}
          aria-disabled={!canApply}
          onClick={() => {
            if (selectedEntry) onApply(selectedEntry)
          }}
          className="w-full rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground transition-opacity disabled:cursor-not-allowed disabled:opacity-40 hover:enabled:opacity-90"
        >
          Apply Template
        </button>
      </div>
    </section>
  )
}
