/**
 * Purpose: Daemon status page — health, uptime, logs summary, quick actions.
 * Inputs:  DaemonStatus via `get_daemon_status`; refreshes every 5s
 * Outputs: Status card with start/stop/restart buttons
 * SPORT: MASTER-COMPONENTS.md — StatusPage
 */

import { useEffect, useState } from "react";
import { invoke } from "@tauri-apps/api/core";
import { Activity, Play, Square, RotateCcw } from "lucide-react";

interface DaemonStatus {
  running: boolean;
  pid?: number;
  uptime_secs?: number;
  version?: string;
}

function formatUptime(secs: number): string {
  if (secs < 60) return `${secs}s`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ${secs % 60}s`;
  return `${Math.floor(secs / 3600)}h ${Math.floor((secs % 3600) / 60)}m`;
}

export function StatusPage() {
  const [status, setStatus] = useState<DaemonStatus | null>(null);

  function refresh() {
    invoke<DaemonStatus>("get_daemon_status")
      .then(setStatus)
      .catch(() => setStatus({ running: false }));
  }

  useEffect(() => {
    refresh();
    const id = setInterval(refresh, 5_000);
    return () => clearInterval(id);
  }, []);

  async function startDaemon() {
    await invoke("start_daemon").catch(console.error);
    refresh();
  }
  async function stopDaemon() {
    await invoke("stop_daemon").catch(console.error);
    refresh();
  }
  async function restartDaemon() {
    await invoke("stop_daemon").catch(console.error);
    await invoke("start_daemon").catch(console.error);
    refresh();
  }

  return (
    <div className="space-y-6">
      <h1 className="flex items-center gap-2 text-lg font-semibold">
        <Activity className="h-5 w-5" aria-hidden="true" />
        Daemon Status
      </h1>

      <div className="rounded-lg border border-border bg-card p-6 space-y-4">
        <div className="flex items-center gap-3">
          <span
            className={[
              "h-3 w-3 rounded-full",
              status === null ? "bg-muted-foreground animate-pulse" :
              status.running ? "bg-green-500" : "bg-red-500",
            ].join(" ")}
            aria-hidden="true"
          />
          <span className="text-sm font-medium">
            {status === null ? "Checking…" : status.running ? "Running" : "Stopped"}
          </span>
        </div>

        {status?.running && (
          <dl className="grid grid-cols-2 gap-3 text-sm">
            {status.pid && (
              <>
                <dt className="text-muted-foreground">PID</dt>
                <dd className="font-mono">{status.pid}</dd>
              </>
            )}
            {status.uptime_secs !== undefined && (
              <>
                <dt className="text-muted-foreground">Uptime</dt>
                <dd>{formatUptime(status.uptime_secs)}</dd>
              </>
            )}
            {status.version && (
              <>
                <dt className="text-muted-foreground">Version</dt>
                <dd className="font-mono">{status.version}</dd>
              </>
            )}
          </dl>
        )}

        <div className="flex gap-2 pt-2">
          {!status?.running && (
            <button
              type="button"
              onClick={startDaemon}
              className="flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-sm text-primary-foreground hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Play className="h-4 w-4" aria-hidden="true" />
              Start
            </button>
          )}
          {status?.running && (
            <>
              <button
                type="button"
                onClick={restartDaemon}
                className="flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <RotateCcw className="h-4 w-4" aria-hidden="true" />
                Restart
              </button>
              <button
                type="button"
                onClick={stopDaemon}
                className="flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <Square className="h-4 w-4" aria-hidden="true" />
                Stop
              </button>
            </>
          )}
        </div>
      </div>
    </div>
  );
}
