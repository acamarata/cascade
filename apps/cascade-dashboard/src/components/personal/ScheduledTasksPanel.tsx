/**
 * Purpose: Displays daemon scheduled tasks (launchd jobs, cron-like tasks) with status badges.
 * Inputs: none — fetches /api/personal/scheduled-tasks via usePersonal.
 * Outputs: Table showing task name, schedule, last_run, next_run, status.
 * Constraints: Pure Tailwind (no shadcn/ui). Status badge colored by value.
 * SPORT: T-P3-E02-12 ScheduledTasksPanel
 */
import { usePersonal } from '@/hooks/usePersonal'
import type { ScheduledTasksResponse } from '@/lib/api'

const STATUS_STYLES: Record<string, string> = {
  active: 'bg-green-100 text-green-700 dark:bg-green-900 dark:text-green-300',
  paused: 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400',
  error: 'bg-red-100 text-red-700 dark:bg-red-900 dark:text-red-300',
}

function dash(v: string | null): string {
  return v ?? '—'
}

export function ScheduledTasksPanel() {
  const { data, loading, error } = usePersonal<ScheduledTasksResponse>('/api/personal/scheduled-tasks')

  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-foreground">Scheduled Tasks</h2>
        {data && (
          <span className="text-xs text-muted-foreground">{data.tasks.length} tasks</span>
        )}
      </div>

      {loading && (
        <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>
      )}

      {error && (
        <p className="text-xs text-destructive">{error}</p>
      )}

      {data && data.tasks.length === 0 && (
        <p className="text-xs text-muted-foreground">No scheduled tasks.</p>
      )}

      {data && data.tasks.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-xs">
            <thead>
              <tr className="border-b border-border text-muted-foreground text-left">
                <th className="py-1 pr-3 font-medium">Name</th>
                <th className="py-1 pr-3 font-medium">Schedule</th>
                <th className="py-1 pr-3 font-medium">Last run</th>
                <th className="py-1 pr-3 font-medium">Next run</th>
                <th className="py-1 font-medium">Status</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {data.tasks.map((task) => (
                <tr key={task.name} className="text-foreground">
                  <td className="py-1.5 pr-3 font-mono truncate max-w-[140px]">{task.name}</td>
                  <td className="py-1.5 pr-3 text-muted-foreground">{task.schedule}</td>
                  <td className="py-1.5 pr-3 text-muted-foreground">{dash(task.last_run)}</td>
                  <td className="py-1.5 pr-3 text-muted-foreground">{dash(task.next_run)}</td>
                  <td className="py-1.5">
                    <span
                      className={`rounded px-1 py-0.5 text-[10px] font-medium uppercase ${STATUS_STYLES[task.status] ?? STATUS_STYLES.paused}`}
                    >
                      {task.status}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
