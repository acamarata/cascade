/**
 * Purpose: A single project tile in the Projects grid. Shows name, truncated description,
 *   repo-count badge, and an active-phase badge when the project has a running phase.
 *   Clicking the card toggles a per-card expand state that reveals the repo list (T-P3-E02-17).
 * Inputs: project: ProjectSummary.
 * Outputs: A bordered, clickable card with a chevron that rotates on expand. Expanded content
 *   is delegated to RepoCardList (rendered only while expanded).
 * Constraints: Expand state is LOCAL to each card (useState), never shared across the grid.
 *   Description is truncated client-side at 80 chars; handles undefined gracefully.
 * SPORT: T-P3-E02-16 ProjectCard (expand wiring extended in T-P3-E02-17)
 */
import { useState } from 'react'
import { ChevronDown } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import { RepoCardList } from '@/components/projects/RepoCardList'
import type { ProjectSummary } from '@/lib/api'

export interface ProjectCardProps {
  project: ProjectSummary
}

/** Truncate a description to a max length, appending an ellipsis when cut. */
function truncate(text: string | undefined, max = 80): string {
  if (!text) return ''
  return text.length > max ? `${text.slice(0, max).trimEnd()}…` : text
}

export function ProjectCard({ project }: ProjectCardProps) {
  const [expanded, setExpanded] = useState(false)

  return (
    <div className="rounded-lg border border-border bg-card flex flex-col">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        aria-expanded={expanded}
        className="flex items-start justify-between gap-2 p-4 text-left hover:bg-accent/40 transition-colors rounded-lg"
      >
        <div className="flex flex-col gap-1 min-w-0">
          <div className="flex items-center gap-2">
            <h3 className="text-sm font-semibold text-foreground truncate">{project.name}</h3>
            {project.active_phase_id && (
              <Badge variant="default" className="text-[10px]">
                {project.active_phase_id}
              </Badge>
            )}
          </div>
          <p className="text-xs text-muted-foreground">{truncate(project.description)}</p>
        </div>

        <div className="flex items-center gap-2 shrink-0">
          <Badge variant="outline" className="text-[10px]">
            {project.repo_count} {project.repo_count === 1 ? 'repo' : 'repos'}
          </Badge>
          <ChevronDown
            className={cn(
              'h-4 w-4 text-muted-foreground transition-transform duration-200',
              expanded && 'rotate-180',
            )}
          />
        </div>
      </button>

      {expanded && (
        <div className="border-t border-border px-4 py-3">
          <RepoCardList projectId={project.id} />
        </div>
      )}
    </div>
  )
}
