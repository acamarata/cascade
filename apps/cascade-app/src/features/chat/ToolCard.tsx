/**
 * Purpose: Displays a single tool invocation result inline in the chat stream.
 *   Shows tool name as a badge and result as a collapsible JSON block (collapsed
 *   by default). Status badge (success/error) derived from an "error" key.
 *   Ported from cascade-dashboard GPChatPanel (T-P3-E02-22).
 * Inputs: toolName string, result unknown — raw tool result payload.
 * Outputs: Compact div with tool name + status + collapsible JSON.
 * Constraints: Must not crash on null/undefined/unexpected result shapes.
 *   Pure presentational — no hooks, no side effects.
 * SPORT: E-P9-03 in-app chat — ToolCard
 */
import { WrenchIcon } from 'lucide-react'
import { cn } from '@/lib/utils'

function isErrorResult(result: unknown): boolean {
  if (result !== null && typeof result === 'object' && !Array.isArray(result)) {
    return Boolean((result as Record<string, unknown>)['error'])
  }
  return false
}

function safeStringify(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

export interface ToolCardProps {
  toolName: string
  result: unknown
}

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
        <div className="flex items-center gap-2 flex-wrap">
          <WrenchIcon className="w-3 h-3 text-muted-foreground shrink-0" aria-hidden="true" />
          <code className="font-mono text-[0.8rem] text-foreground/90">{displayName}</code>
          <span
            className={cn(
              'text-[0.65rem] px-1.5 py-0 leading-none rounded-full font-medium',
              isError
                ? 'bg-destructive/20 text-destructive'
                : 'bg-secondary text-secondary-foreground',
            )}
          >
            {isError ? 'error' : 'success'}
          </span>
        </div>
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
