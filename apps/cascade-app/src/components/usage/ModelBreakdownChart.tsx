/**
 * Purpose: Horizontal bar chart showing token usage (and cost) per model.
 *   Sortable by tokens or cost.
 * Inputs:  byModel — ModelUsageStat[]; loading — boolean
 * Outputs: Recharts horizontal BarChart with model names on Y-axis.
 * Constraints:
 *   - Minimum bar visibility: at least 1 pixel (recharts handles this via minPointSize).
 *   - Sort toggle is local state.
 *   - Cost shown in the Tooltip and as a legend label, not as a second axis.
 *   - Empty state when byModel is empty.
 * SPORT: MASTER-COMPONENTS.md — ModelBreakdownChart (usage)
 */

import React, { useState } from 'react'
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
  Cell,
} from 'recharts'
import type { ModelUsageStat } from '../../types/ipc'

interface ModelBreakdownChartProps {
  byModel: ModelUsageStat[]
  loading: boolean
}

type SortKey = 'tokens' | 'cost'

function fmtTokens(n: number): string {
  return n.toLocaleString()
}

function fmtCost(usd: number): string {
  return `$${usd.toFixed(4)}`
}

/**
 * Horizontal bar chart: token usage per model with sort controls.
 */
export function ModelBreakdownChart({
  byModel,
  loading,
}: ModelBreakdownChartProps): React.ReactElement {
  const [sortKey, setSortKey] = useState<SortKey>('tokens')

  const sorted = [...byModel].sort((a, b) =>
    sortKey === 'tokens' ? b.tokens - a.tokens : b.costUsd - a.costUsd
  )

  const chartData = sorted.map((m) => ({
    model: m.model.length > 24 ? `${m.model.slice(0, 22)}…` : m.model,
    tokens: m.tokens,
    costUsd: m.costUsd,
  }))

  const barHeight = Math.max(chartData.length * 32, 80)

  if (!loading && byModel.length === 0) {
    return (
      <section
        aria-labelledby="model-chart-heading"
        className="rounded-lg border border-border bg-card p-4"
      >
        <h3 id="model-chart-heading" className="text-sm font-semibold">
          By Model
        </h3>
        <p className="mt-4 text-sm text-muted-foreground">No model data for this period.</p>
      </section>
    )
  }

  return (
    <section
      aria-labelledby="model-chart-heading"
      className="rounded-lg border border-border bg-card p-4"
    >
      <div className="mb-3 flex items-center justify-between">
        <h3 id="model-chart-heading" className="text-sm font-semibold">
          By Model
        </h3>
        {/* Sort toggle */}
        <div className="flex gap-1">
          {(['tokens', 'cost'] as SortKey[]).map((key) => (
            <button
              key={key}
              type="button"
              onClick={() => setSortKey(key)}
              aria-pressed={sortKey === key}
              className={[
                'rounded px-2 py-0.5 text-xs font-medium transition-colors',
                sortKey === key
                  ? 'bg-primary text-primary-foreground'
                  : 'bg-muted text-muted-foreground hover:bg-accent/50 hover:text-foreground',
              ].join(' ')}
            >
              {key === 'tokens' ? 'Tokens' : 'Cost'}
            </button>
          ))}
        </div>
      </div>

      <ResponsiveContainer width="100%" height={barHeight}>
        <BarChart
          layout="vertical"
          data={chartData}
          margin={{ top: 4, right: 60, bottom: 0, left: 0 }}
        >
          <CartesianGrid strokeDasharray="3 3" horizontal={false} stroke="hsl(var(--border))" />
          <XAxis
            type="number"
            tick={{ fontSize: 10, fill: 'hsl(var(--muted-foreground))' }}
            axisLine={false}
            tickLine={false}
            tickFormatter={(v: number) => (v >= 1000 ? `${(v / 1000).toFixed(0)}k` : String(v))}
          />
          <YAxis
            type="category"
            dataKey="model"
            width={140}
            tick={{ fontSize: 11, fill: 'hsl(var(--foreground))' }}
            axisLine={false}
            tickLine={false}
          />
          <Tooltip
            contentStyle={{
              background: 'hsl(var(--popover))',
              border: '1px solid hsl(var(--border))',
              borderRadius: 6,
              fontSize: 12,
            }}
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            formatter={(value: any, name: any) =>
              name === 'tokens'
                ? [fmtTokens(Number(value) || 0), 'Tokens']
                : [fmtCost(Number(value) || 0), 'Cost']
            }
            labelStyle={{ color: 'hsl(var(--foreground))', fontWeight: 600 }}
          />
          <Bar
            dataKey={sortKey === 'tokens' ? 'tokens' : 'costUsd'}
            radius={[0, 3, 3, 0]}
            minPointSize={2}
          >
            {chartData.map((_, i) => (
              <Cell key={i} fill={`hsl(var(--primary) / ${Math.max(0.4, 1 - i * 0.08)})`} />
            ))}
          </Bar>
        </BarChart>
      </ResponsiveContainer>

      {/* Cost legend per model (below chart) */}
      <ul className="mt-2 space-y-0.5">
        {sorted.slice(0, 8).map((m) => (
          <li
            key={m.model}
            className="flex items-center justify-between text-xs text-muted-foreground"
          >
            <span className="truncate max-w-[60%]">{m.model}</span>
            <span className="tabular-nums">{fmtCost(m.costUsd)}</span>
          </li>
        ))}
      </ul>
    </section>
  )
}
