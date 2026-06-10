/**
 * Purpose: Featured card at the top of the Projects section showing the ASI (All Sites
 *   Instructions) project path, its first-line description, and a link chip to the rules.
 * Inputs: overview: AsiOverview ({ path, description }).
 * Outputs: A bordered card with heading, description, path chip, and "View cascade rules" chip.
 * Constraints: Pure presentational — no fetching. Description shown verbatim from the daemon.
 * SPORT: T-P3-E02-16 ASIOverviewCard
 */
import { FileText, ExternalLink } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import type { AsiOverview } from '@/lib/api'

export interface ASIOverviewCardProps {
  overview: AsiOverview
}

export function ASIOverviewCard({ overview }: ASIOverviewCardProps) {
  return (
    <div className="rounded-lg border border-border bg-card p-4 flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <h2 className="text-sm font-semibold text-foreground flex items-center gap-1.5">
          <FileText className="h-4 w-4 text-muted-foreground" />
          ASI — All Sites Instructions
        </h2>
        <Badge variant="secondary" className="font-mono text-[10px]">
          <ExternalLink className="h-3 w-3 mr-1" />
          View cascade rules
        </Badge>
      </div>

      <p className="text-sm text-muted-foreground">
        {overview.description || 'No description available.'}
      </p>

      <code className="text-[11px] text-muted-foreground/80 font-mono truncate" title={overview.path}>
        {overview.path}
      </code>
    </div>
  )
}
