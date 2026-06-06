/**
 * Purpose: Displays the redacted GCI settings.json snapshot as a recursive JSON tree.
 * Inputs: none.
 * Outputs: Card with expandable/collapsible JSON tree; redacted_fields shown as red REDACTED badges.
 * Constraints: Fetches once + 30s refresh. Redacted fields replaced client-side from the
 *   `redacted_fields` array in the response.
 * SPORT: T-P3-E02-09 SettingsSnapshotPanel
 */
import { useGCI } from '@/hooks/useGCI'
import type { SettingsNode, SettingsValue, SettingsSnapshotResponse } from '@/lib/api'

function RedactedBadge() {
  return (
    <span className="inline-block rounded px-1.5 py-0.5 text-xs font-semibold bg-destructive/10 text-destructive border border-destructive/30">
      REDACTED
    </span>
  )
}

interface JsonNodeProps {
  value: SettingsValue
  redactedFields: Set<string>
  keyName?: string
  depth?: number
}

function JsonNode({ value, redactedFields, keyName, depth = 0 }: JsonNodeProps) {
  const isRedacted = keyName !== undefined && redactedFields.has(keyName)

  if (isRedacted) {
    return <RedactedBadge />
  }

  if (value === null) {
    return <span className="text-muted-foreground italic">null</span>
  }

  if (typeof value === 'boolean') {
    return <span className="text-blue-500">{value ? 'true' : 'false'}</span>
  }

  if (typeof value === 'number') {
    return <span className="text-amber-600 dark:text-amber-400">{value}</span>
  }

  if (typeof value === 'string') {
    return (
      <span className="text-green-700 dark:text-green-400">
        &quot;{value}&quot;
      </span>
    )
  }

  if (Array.isArray(value)) {
    if (value.length === 0) {
      return <span className="text-muted-foreground">[]</span>
    }
    return (
      <span className="block">
        <span className="text-muted-foreground">[</span>
        <span className="block pl-4">
          {value.map((item, idx) => (
            <span key={idx} className="block">
              <JsonNode value={item} redactedFields={redactedFields} depth={depth + 1} />
              {idx < value.length - 1 && <span className="text-muted-foreground">,</span>}
            </span>
          ))}
        </span>
        <span className="text-muted-foreground">]</span>
      </span>
    )
  }

  if (typeof value === 'object') {
    const entries = Object.entries(value as SettingsNode)
    if (entries.length === 0) {
      return <span className="text-muted-foreground">{'{}'}</span>
    }
    return (
      <span className="block">
        <span className="text-muted-foreground">{'{'}</span>
        <span className="block pl-4">
          {entries.map(([k, v], idx) => (
            <span key={k} className="block">
              <span className="text-foreground font-medium">&quot;{k}&quot;</span>
              <span className="text-muted-foreground">: </span>
              <JsonNode value={v} redactedFields={redactedFields} keyName={k} depth={depth + 1} />
              {idx < entries.length - 1 && <span className="text-muted-foreground">,</span>}
            </span>
          ))}
        </span>
        <span className="text-muted-foreground">{'}'}</span>
      </span>
    )
  }

  return null
}

export function SettingsSnapshotPanel() {
  const { data, loading, error, refetch } = useGCI<SettingsSnapshotResponse>('/api/gci/settings-snapshot')
  const redactedSet = new Set<string>(data?.redacted_fields ?? [])

  return (
    <div className="rounded-lg border border-border bg-card text-card-foreground p-4 flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <h2 className="font-semibold text-base">Settings Snapshot</h2>
        {data && redactedSet.size > 0 && (
          <span className="text-xs text-muted-foreground">{redactedSet.size} redacted</span>
        )}
      </div>

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
        <div className="overflow-auto max-h-72 text-xs font-mono leading-relaxed">
          <JsonNode value={data.settings} redactedFields={redactedSet} />
        </div>
      )}

      {!loading && !error && !data && (
        <p className="text-sm text-muted-foreground py-4 text-center">No settings data available.</p>
      )}
    </div>
  )
}
