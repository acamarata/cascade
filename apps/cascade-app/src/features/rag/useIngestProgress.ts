/**
 * Purpose: React hook that subscribes to the Tauri event
 *   "rag-ingest-progress" and tracks live ingest state.
 *   Automatically resets when a new ingest starts (percent < previous).
 * Inputs:  onComplete — optional callback invoked when progress.done === true.
 * Outputs: { progress, active }
 *   - progress: latest RagIngestProgress payload, or null when idle.
 *   - active: true while an ingest is in progress (not done).
 * Constraints:
 *   - Unsubscribes automatically on component unmount.
 *   - Safe in Vitest (Node) — listen() is mocked by @tauri-apps/api test stubs
 *     or gracefully fails to register without crashing.
 * SPORT: MASTER-COMPONENTS.md — useIngestProgress (T-P4-E01-27)
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import type { RagIngestProgress } from './types'

interface UseIngestProgressResult {
  progress: RagIngestProgress | null
  active: boolean
}

export function useIngestProgress(onComplete?: () => void): UseIngestProgressResult {
  const [progress, setProgress] = useState<RagIngestProgress | null>(null)
  const onCompleteRef = useRef(onComplete)
  onCompleteRef.current = onComplete

  const clearProgress = useCallback(() => setProgress(null), [])

  useEffect(() => {
    let unlisten: (() => void) | undefined

    // Dynamically import to avoid crashing in Vitest / non-Tauri environments.
    import('@tauri-apps/api/event')
      .then(({ listen }) =>
        listen<RagIngestProgress>('rag-ingest-progress', (event) => {
          const payload = event.payload
          setProgress(payload)
          if (payload.done) {
            onCompleteRef.current?.()
            // Clear progress after a short delay so the user sees 100%.
            setTimeout(clearProgress, 1500)
          }
        }),
      )
      .then((fn) => {
        unlisten = fn
      })
      .catch(() => {
        // Not in a Tauri context (e.g. Vitest) — no-op.
      })

    return () => {
      unlisten?.()
    }
  }, [clearProgress])

  return {
    progress,
    active: progress !== null && !progress.done,
  }
}
