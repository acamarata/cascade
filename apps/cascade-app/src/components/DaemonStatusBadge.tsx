/**
 * Purpose: Live daemon status dot in the top bar.
 * Inputs:  Polls `get_daemon_status` every 5s
 * Outputs: Green/red/gray dot + optional uptime
 * Constraints: Non-blocking — does not suspend render on fetch failure.
 * SPORT: MASTER-COMPONENTS.md — DaemonStatusBadge
 */

import { useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'

interface DaemonStatus {
  running: boolean
  pid?: number
  uptime_secs?: number
}

export function DaemonStatusBadge() {
  const [status, setStatus] = useState<DaemonStatus | null>(null)

  useEffect(() => {
    function poll() {
      invoke<DaemonStatus>('get_daemon_status')
        .then(setStatus)
        .catch(() => setStatus({ running: false }))
    }
    poll()
    const id = setInterval(poll, 5_000)
    return () => clearInterval(id)
  }, [])

  const color =
    status === null
      ? 'bg-muted-foreground'
      : status.running
        ? 'bg-emerald-500 shadow-[0_0_8px_rgba(16,185,129,0.5)] animate-pulse'
        : 'bg-rose-500 shadow-[0_0_8px_rgba(244,63,94,0.5)]'

  const label =
    status === null
      ? 'Checking daemon…'
      : status.running
        ? `Daemon running (PID ${status.pid ?? '?'})`
        : 'Daemon stopped'

  return (
    <div className="flex items-center gap-2 bg-muted/30 border border-border/40 rounded-lg px-2.5 py-1" aria-live="polite" aria-label={label}>
      <span className={['h-2 w-2 rounded-full', color].join(' ')} aria-hidden="true" />
      <span className="text-xs font-semibold text-muted-foreground/90">
        {status === null ? '…' : status.running ? 'Running' : 'Stopped'}
      </span>
    </div>
  )
}
