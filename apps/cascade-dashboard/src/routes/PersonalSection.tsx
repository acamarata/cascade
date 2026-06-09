/**
 * Purpose: Personal section route — per-user activity (threads, ideas/inbox, CRD chains,
 *   scheduled tasks) and usage (fleet quota, account ledger, analytics chart).
 * Inputs: none.
 * Outputs: Tabbed page: Activity tab + Usage tab, each rendering its panels.
 * Constraints: Tab state is local. Panels self-fetch their /api/personal/* endpoints.
 * SPORT: T-P3-E02-12, T-P3-E02-13, T-P3-E02-24 — Personal Section React
 */
import { useState } from 'react'
import { ThreadsPanel } from '@/components/personal/ThreadsPanel'
import { IdeasInboxPanel } from '@/components/personal/IdeasInboxPanel'
import { CRDChainsPanel } from '@/components/personal/CRDChainsPanel'
import { ScheduledTasksPanel } from '@/components/personal/ScheduledTasksPanel'
import { FleetQuotaPanel } from '@/components/personal/FleetQuotaPanel'
import { AccountLedgerPanel } from '@/components/personal/AccountLedgerPanel'
import { WeeklyUsageChart } from '@/components/Analytics/WeeklyUsageChart'

type Tab = 'activity' | 'usage'

export function PersonalSection() {
  const [activeTab, setActiveTab] = useState<Tab>('activity')

  return (
    <div className="flex flex-col gap-4">
      <div>
        <h1 className="text-xl font-semibold text-foreground mb-1">Personal</h1>
        <p className="text-muted-foreground text-sm">
          Your activity, threads, ideas, and account quota/usage.
        </p>
      </div>

      <div className="flex gap-1 border-b border-border">
        <button
          onClick={() => setActiveTab('activity')}
          className={`px-4 py-2 text-sm font-medium rounded-t transition-colors ${
            activeTab === 'activity'
              ? 'border-b-2 border-primary text-primary -mb-px'
              : 'text-muted-foreground hover:text-foreground'
          }`}
        >
          Activity
        </button>
        <button
          onClick={() => setActiveTab('usage')}
          className={`px-4 py-2 text-sm font-medium rounded-t transition-colors ${
            activeTab === 'usage'
              ? 'border-b-2 border-primary text-primary -mb-px'
              : 'text-muted-foreground hover:text-foreground'
          }`}
        >
          Usage
        </button>
      </div>

      {activeTab === 'activity' ? (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <ThreadsPanel />
          <IdeasInboxPanel />
          <CRDChainsPanel />
          <ScheduledTasksPanel />
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          <FleetQuotaPanel />
          <AccountLedgerPanel />
          <WeeklyUsageChart />
        </div>
      )}
    </div>
  )
}
