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
    status === null ? 'bg-muted-foreground' : status.running ? 'bg-green-500' : 'bg-red-500'

  const label =
    status === null
      ? 'Checking daemon…'
      : status.running
        ? `Daemon running (PID ${status.pid ?? '?'})`
        : 'Daemon stopped'

  return (
    <div className="flex items-center gap-1.5" aria-live="polite" aria-label={label}>
      <span className={['h-2 w-2 rounded-full', color].join(' ')} aria-hidden="true" />
      <span className="text-xs text-muted-foreground">
        {status === null ? '…' : status.running ? 'Running' : 'Stopped'}
      </span>
    </div>
  )
}
