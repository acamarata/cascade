/**
 * Purpose: Displays a single tool invocation result inline in the chat stream.
 *   Shows the tool name as a badge, and the result as a collapsible JSON block
 *   (collapsed by default). Status badge (success/error) derived from the
 *   presence of an "error" key in the result object.
 * Inputs: toolName string, result unknown — the raw tool result payload.
 * Outputs: A compact Card with tool name + status badge + collapsible JSON.
 * Constraints: Must not crash on null/undefined/unexpected result shapes.
 *   Pure presentational — no hooks, no side effects. result typed as unknown;
 *   narrowed before rendering.
 * SPORT: T-P3-E02-22
 */
import { WrenchIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

// ─── helpers ─────────────────────────────────────────────────────────────────

/** Returns true when result looks like an error (has an "error" key with a truthy value). */
function isErrorResult(result: unknown): boolean {
  if (result !== null && typeof result === 'object' && !Array.isArray(result)) {
    const rec = result as Record<string, unknown>
    return Boolean(rec['error'])
  }
  return false
}

/** Safely stringify any value for display. */
function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

// ─── ToolCard ─────────────────────────────────────────────────────────────────

export interface ToolCardProps {
  /** Name of the tool that was invoked, e.g. "list_projects". */
  toolName: string
  /** Raw result payload from the tool (may be any shape). */
  result: unknown
}

/**
 * Purpose: Compact card for a single tool invocation result.
 * Inputs: ToolCardProps
 * Outputs: Styled Card with tool name badge, status indicator, and
 *   collapsed-by-default JSON details block.
 * Constraints: Handles null/undefined result gracefully. No crashes on
 *   unexpected shapes.
 * SPORT: T-P3-E02-22
 */
export function ToolCard({ toolName, result }: ToolCardProps) {
  const isError = isErrorResult(result)
  const displayName = toolName || 'unknown_tool'
  const jsonText = safeStringify(result)

  return (
    <div
      className={cn(
        'my-1.5 rounded-lg border text-xs',
        isError
          ? 'border-destructive/40 bg-destructive/5'
          : 'border-muted-foreground/20 bg-muted/40',
      )}
    >
      <div className="px-3 py-2 space-y-1.5">
        {/* Header row */}
        <div className="flex items-center gap-2 flex-wrap">
          <WrenchIcon className="w-3 h-3 text-muted-foreground shrink-0" />
          <code className="font-mono text-[0.8rem] text-foreground/90">{displayName}</code>
          <Badge
            variant={isError ? 'destructive' : 'secondary'}
            className="text-[0.65rem] px-1.5 py-0 leading-none"
          >
            {isError ? 'error' : 'success'}
          </Badge>
        </div>

        {/* Collapsible JSON result */}
        <details className="group">
          <summary
            className={cn(
              'cursor-pointer select-none text-muted-foreground/70',
              'hover:text-muted-foreground transition-colors list-none',
              'flex items-center gap-1 text-[0.7rem]',
            )}
          >
            <span className="group-open:hidden">▶ show result</span>
            <span className="hidden group-open:inline">▼ hide result</span>
          </summary>
          <pre
            className={cn(
              'mt-1.5 overflow-x-auto rounded bg-zinc-900 p-2 text-[0.7rem]',
              'leading-relaxed text-zinc-200 max-h-48',
            )}
          >
            {jsonText}
          </pre>
        </details>
      </div>
    </div>
  )
}
