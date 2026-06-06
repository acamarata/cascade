/**
 * Purpose: Renders the GCI/ASI/PPI/PRI/PAI cascade as a vertical indented tier tree.
 * Inputs: none.
 * Outputs: Card with tier tree — each item shows tier badge + label + path + exists check/cross.
 * Constraints: 30s auto-refresh via useGCI. Recursive tree from the daemon response.
 * SPORT: T-P3-E02-09 CascadeDiagramPanel
 */
import { useGCI } from '@/hooks/useGCI'
import type { CascadeTierItem, CascadeDiagramResponse } from '@/lib/api'

const TIER_COLORS: Record<string, string> = {
  GCI: 'bg-purple-100 text-purple-800 dark:bg-purple-900/30 dark:text-purple-300',
  ASI: 'bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-300',
  PPI: 'bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-300',
  PRI: 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300',
  PAI: 'bg-rose-100 text-rose-800 dark:bg-rose-900/30 dark:text-rose-300',
}

interface TierNodeProps {
  item: CascadeTierItem
  depth?: number
}

function TierNode({ item, depth = 0 }: TierNodeProps) {
  const tierColor = TIER_COLORS[item.tier] ?? 'bg-muted text-muted-foreground'

  return (
    <li>
      <div
        className="flex items-start gap-2 py-1.5"
        style={{ paddingLeft: `${depth * 1.25}rem` }}
      >
        <span
          className={`shrink-0 rounded px-1.5 py-0.5 text-xs font-semibold ${tierColor}`}
        >
          {item.tier}
        </span>
        <div className="flex flex-col min-w-0 flex-1">
          <span className="text-sm font-medium text-foreground truncate">{item.label}</span>
          <span className="text-xs text-muted-foreground truncate">{item.path}</span>
        </div>
        <span
          className={`shrink-0 text-sm ${item.exists ? 'text-green-600 dark:text-green-400' : 'text-destructive'}`}
          title={item.exists ? 'File exists' : 'File missing'}
        >
          {item.exists ? '✓' : '✗'}
        </span>
      </div>
      {item.children && item.children.length > 0 && (
        <ul>
          {item.children.map((child, idx) => (
            <TierNode key={`${child.path}-${idx}`} item={child} depth={depth + 1} />
          ))}
        </ul>
      )}
    </li>
  )
}

export function CascadeDiagramPanel() {
  const { data, loading, error, refetch } = useGCI<CascadeDiagramResponse>('/api/gci/cascade-diagram')

  return (
    <div className="rounded-lg border border-border bg-card text-card-foreground p-4 flex flex-col gap-3">
      <h2 className="font-semibold text-base">Cascade Diagram</h2>

      {loading && (
        <div className="flex items-center justify-center h-20 text-muted-foreground text-sm">
          <span className="animate-spin mr-2">⟳</span> Loading…
        </div>
      )}

      {error && !loading && (
        <div className="flex flex-col items-center gap-2 text-destructive text-sm py-4">
          <span>{error}</span>
          <button
            onClick={refetch}
            className="text-xs underline text-muted-foreground hover:text-foreground"
          >
            Retry
          </button>
        </div>
      )}

      {!loading && !error && data && (
        <ul className="text-sm max-h-72 overflow-y-auto divide-y divide-border/50">
          {data.tiers.map((tier, idx) => (
            <TierNode key={`${tier.path}-${idx}`} item={tier} />
          ))}
        </ul>
      )}

      {!loading && !error && !data && (
        <p className="text-sm text-muted-foreground py-4 text-center">No cascade data available.</p>
      )}
    </div>
  )
}
