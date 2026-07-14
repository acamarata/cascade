/**
 * Purpose: Read-only Cascade meta view — shows config/tier tree from the daemon.
 * Inputs:  None (standalone route page).
 * Outputs: Config/tier display panel.
 * SPORT: MASTER-ROUTES.md — /settings/cascade
 */
import { useEffect, useState } from 'react'
import { MountainSnow } from 'lucide-react'

const DAEMON_BASE_URL =
  typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window ? 'http://127.0.0.1:9761' : ''

export function CascadeMetaPage() {
  const [config, setConfig] = useState<Record<string, unknown> | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const res = await fetch(`${DAEMON_BASE_URL}/api/config`)
        if (!res.ok) throw new Error(`HTTP ${res.status}`)
        const data = (await res.json()) as Record<string, unknown>
        if (!cancelled) setConfig(data)
      } catch {
        if (!cancelled) setError('Config unavailable — daemon may be offline.')
      }
    })()
    return () => {
      cancelled = true
    }
  }, [])

  return (
    <main id="main-content" className="flex flex-col h-full overflow-auto p-6">
      <div className="flex items-center gap-2 mb-6">
        <MountainSnow className="h-5 w-5 text-primary" aria-hidden="true" />
        <h1 className="text-base font-semibold">Cascade Meta</h1>
      </div>
      {error && <p className="text-sm text-muted-foreground">{error}</p>}
      {config && (
        <pre className="text-xs bg-muted rounded-lg p-4 overflow-auto max-h-[calc(100vh-12rem)] whitespace-pre-wrap break-words">
          {JSON.stringify(config, null, 2)}
        </pre>
      )}
      {!config && !error && <p className="text-sm text-muted-foreground">Loading config...</p>}
    </main>
  )
}
