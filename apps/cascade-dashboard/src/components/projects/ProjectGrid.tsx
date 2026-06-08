/**
 * Purpose: Responsive 2-column grid of ProjectCard tiles.
 * Inputs: projects: ProjectSummary[].
 * Outputs: A grid (1 column on narrow, 2 columns from md up) mapping each project to a card.
 * Constraints: Pure layout — no fetching, no expand state (each ProjectCard owns its own).
 * SPORT: T-P3-E02-16 ProjectGrid
 */
import { ProjectCard } from '@/components/projects/ProjectCard'
import type { ProjectSummary } from '@/lib/api'

export interface ProjectGridProps {
  projects: ProjectSummary[]
}

export function ProjectGrid({ projects }: ProjectGridProps) {
  if (projects.length === 0) {
    return <p className="text-xs text-muted-foreground">No projects found.</p>
  }

  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
      {projects.map((project) => (
        <ProjectCard key={project.id} project={project} />
      ))}
    </div>
  )
}
