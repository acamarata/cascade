/**
 * Purpose: 404 Not Found page — catch-all route for unmatched paths.
 * Inputs:  None.
 * Outputs: <main> with error message and back link.
 * Constraints: Must be the wildcard (*) route in the router.
 * SPORT: MASTER-ROUTES.md — *
 */

import { Link } from 'react-router-dom'

export function NotFoundPage() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-4 p-6">
      <h1 className="text-2xl font-semibold">Page not found</h1>
      <p className="text-muted-foreground">The page you are looking for does not exist.</p>
      <Link to="/chat" className="text-primary underline underline-offset-4 hover:opacity-80">
        Back to Chat
      </Link>
    </main>
  )
}
