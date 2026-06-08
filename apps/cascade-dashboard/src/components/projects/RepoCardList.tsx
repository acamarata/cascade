/**
 * Purpose: Rendered inside an expanded ProjectCard. Lists each git repo as a compact row with
 *   name, branch badge, relative last-commit time, a CLAUDE.md indicator, and a "View PEWS"
 *   button that slides down the PEWSScaffoldTree for that repo.
 * Inputs: projectId string.
 * Outputs: A list of repo rows; one repo's PEWS tree open at a time (per-list state).
 * Constraints: On-demand fetch via useRepos (no polling). formatDistanceToNow guards missing
 *   dates (CR-B). Aborts on collapse via the hook's AbortController.
 * SPORT: T-P3-E02-17 RepoCardList
 */
import { useState } from 'react'
import { formatDistanceToNow } from 'date-fns'
import { FileText, GitBranch } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useRepos } from '@/hooks/useRepos'
import { PEWSScaffoldTree } from '@/components/projects/PEWSScaffoldTree'
import type { RepoCard } from '@/lib/api'

export interface RepoCardListProps {
  projectId: string
}

/** Relative time from an ISO string; returns '—' for missing/invalid dates. */
function relativeTime(iso: string | undefined): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  return formatDistanceToNow(t, { addSuffix: true })
}

function RepoRow({ repo, projectId }: { repo: RepoCard; projectId: string }) {
  const [showPews, setShowPews] = useState(false)
  return (
    <div className="flex flex-col gap-1.5">
      <div className="flex items-center justify-between gap-2 text-xs">
        <div className="flex items-center gap-2 min-w-0">
          <span className="font-semibold text-foreground truncate">{repo.name}</span>
          <Badge variant="outline" className="text-[9px] gap-1">
            <GitBranch className="h-2.5 w-2.5" />
            {repo.branch}
          </Badge>
          {repo.has_claude_md && (
            <FileText className="h-3 w-3 text-muted-foreground" aria-label="has CLAUDE.md" />
          )}
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <span className="text-[10px] text-muted-foreground">{relativeTime(repo.last_commit_at)}</span>
          <Button
            variant="ghost"
            size="sm"
            className="h-6 text-[10px] px-2"
            onClick={() => setShowPews((v) => !v)}
          >
            {showPews ? 'Hide PEWS' : 'View PEWS'}
          </Button>
        </div>
      </div>
      {showPews && (
        <div className="border-l border-border pl-3">
          <PEWSScaffoldTree projectId={projectId} repo={repo.name} />
        </div>
      )}
    </div>
  )
}

export function RepoCardList({ projectId }: RepoCardListProps) {
  const { data, loading, error } = useRepos(projectId)

  if (loading) return <p className="text-xs text-muted-foreground animate-pulse">Loading repos…</p>
  if (error) return <p className="text-xs text-destructive">{error}</p>
  if (!data || data.repos.length === 0) {
    return <p className="text-xs text-muted-foreground">No repos found.</p>
  }

  return (
    <div className="flex flex-col gap-3">
      {data.repos.map((repo) => (
        <RepoRow key={repo.name} repo={repo} projectId={projectId} />
      ))}
    </div>
  )
}
