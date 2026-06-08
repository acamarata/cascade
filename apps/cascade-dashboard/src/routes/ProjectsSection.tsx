/**
 * Purpose: Projects section route (/projects). Renders the ASI overview card and the project
 *   grid, both driven by the /api/projects daemon endpoint with 30s auto-refresh.
 * Inputs: none.
 * Outputs: Section heading, ASIOverviewCard, and ProjectGrid (with loading/error/empty states).
 * Constraints: Single fetch via useProjects shared across both cards; no per-card polling here.
 * SPORT: T-P3-E02-16 ProjectsSection
 */
import { useProjects } from '@/hooks/useProjects'
import { ASIOverviewCard } from '@/components/projects/ASIOverviewCard'
import { ProjectGrid } from '@/components/projects/ProjectGrid'

export function ProjectsSection() {
  const { data, loading, error, refetch } = useProjects()

  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold text-foreground">Projects</h1>
        <button
          onClick={() => refetch()}
          className="text-xs text-muted-foreground hover:text-foreground transition-colors"
          aria-label="Refresh projects"
        >
          ↻
        </button>
      </div>

      {loading && !data && (
        <p className="text-xs text-muted-foreground animate-pulse">Loading…</p>
      )}
      {error && <p className="text-xs text-destructive">{error}</p>}

      {data && (
        <>
          <ASIOverviewCard overview={data.asi_overview} />
          <ProjectGrid projects={data.projects} />
        </>
      )}
    </div>
  )
}
