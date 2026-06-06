/**
 * Purpose: Announces route changes to screen readers via an aria-live region.
 *   Satisfies WCAG 2.4.2 (page titled) at the assistive-technology layer for SPA
 *   navigation where the browser does not naturally announce route transitions.
 * Inputs:  None (reads current location via useLocation).
 * Outputs: An off-screen aria-live="polite" aria-atomic="true" region that
 *   announces the new page title on every route change.
 * Constraints: Must be rendered inside a Router context. Message is derived from
 *   document.title (set by react-helmet-async) — use after HelmetProvider wraps
 *   the tree so the title is already updated when location changes fire.
 *   prefers-reduced-motion: no animation involved; announcements are unaffected.
 * SPORT: MASTER-COMPONENTS.md — RouteAnnouncer
 */

import { useEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'

export function RouteAnnouncer() {
  const location = useLocation()
  const [message, setMessage] = useState('')
  // Track whether initial mount has passed so we don't announce on first render.
  const mounted = useRef(false)

  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true
      return
    }
    // Use the document title set by react-helmet-async, fall back to path.
    const title = document.title || location.pathname
    setMessage(`Navigated to ${title}`)
  }, [location])

  return (
    <div role="status" aria-live="polite" aria-atomic="true" className="sr-only">
      {message}
    </div>
  )
}
