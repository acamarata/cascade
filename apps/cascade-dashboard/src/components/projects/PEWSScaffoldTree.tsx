/**
 * Purpose: Accordion tree of a project's PEWS scaffold: phase summary → epics → waves → sprints
 *   → ticket rows. Each level shows a name and status; sprints show a done/total progress bar;
 *   tickets show a weight chip and a status badge.
 * Inputs: projectId string, repo string (the repo whose phase is shown).
 * Outputs: A phase header (name + pct_done Progress) and a nested Accordion (epics/waves/sprints)
 *   with non-collapsible ticket rows at the leaves. Max nesting is 4 levels (CR-A).
 * Constraints: Fetched on-demand via useScaffold (never polled — CR-C). Read-only.
 * SPORT: T-P3-E02-17 PEWSScaffoldTree
 */
import { useScaffold } from '@/hooks/useScaffold'
import {
  Accordion,
  AccordionItem,
  AccordionTrigger,
  AccordionContent,
} from '@/components/ui/accordion'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { statusBadgeClass } from '@/components/projects/statusBadge'
import { cn } from '@/lib/utils'
import type { ScaffoldSprint, ScaffoldTicket } from '@/lib/api'

export interface PEWSScaffoldTreeProps {
  projectId: string
  repo: string
}

function StatusPill({ status }: { status: string }) {
  return (
    <span className={cn('rounded-full border px-2 py-0.5 text-[10px] font-medium', statusBadgeClass(status))}>
      {status}
    </span>
  )
}

function TicketRow({ ticket }: { ticket: ScaffoldTicket }) {
  return (
    <div className="flex items-center justify-between gap-2 py-1 pl-4 text-xs">
      <div className="flex items-center gap-2 min-w-0">
        <Badge variant="outline" className="text-[9px] shrink-0">{ticket.weight}</Badge>
        <span className="text-muted-foreground truncate" title={ticket.title}>
          <span className="font-mono text-foreground/70">{ticket.id}</span> {ticket.title}
        </span>
      </div>
      <StatusPill status={ticket.status} />
    </div>
  )
}

function SprintNode({ sprint }: { sprint: ScaffoldSprint }) {
  const pct = sprint.total > 0 ? Math.round((sprint.done / sprint.total) * 100) : 0
  return (
    <AccordionItem value={sprint.id}>
      <AccordionTrigger className="text-xs">
        <div className="flex items-center gap-2 flex-1 min-w-0">
          <span className="truncate">{sprint.title}</span>
          <span className="text-[10px] text-muted-foreground tabular-nums shrink-0">
            {sprint.done}/{sprint.total}
          </span>
          <Progress value={pct} className="h-1 w-16 shrink-0" />
        </div>
      </AccordionTrigger>
      <AccordionContent>
        {sprint.tickets.map((t) => <TicketRow key={t.id} ticket={t} />)}
      </AccordionContent>
    </AccordionItem>
  )
}

export function PEWSScaffoldTree({ projectId, repo }: PEWSScaffoldTreeProps) {
  const { data, loading, error } = useScaffold(projectId, true)

  if (loading) return <p className="text-xs text-muted-foreground animate-pulse py-2">Loading PEWS…</p>
  if (error) return <p className="text-xs text-destructive py-2">{error}</p>
  if (!data) return <p className="text-xs text-muted-foreground py-2">No scaffold for {repo}.</p>

  return (
    <div className="flex flex-col gap-2 py-2">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-semibold text-foreground truncate">{data.phase_name}</span>
        <span className="text-[10px] text-muted-foreground tabular-nums">{data.pct_done}%</span>
      </div>
      <Progress value={data.pct_done} className="h-1.5" />
      <span className="text-[10px] text-muted-foreground">
        updated {new Date(data.last_updated).toLocaleString()}
      </span>

      <Accordion type="multiple" className="mt-1">
        {data.epics.map((epic) => (
          <AccordionItem key={epic.id} value={epic.id}>
            <AccordionTrigger className="text-xs">
              <span className="flex items-center gap-2">
                <span className="font-mono text-foreground/70">{epic.id}</span>
                <span className="truncate">{epic.title}</span>
                <StatusPill status={epic.status} />
              </span>
            </AccordionTrigger>
            <AccordionContent>
              <Accordion type="multiple" className="pl-3">
                {epic.waves.map((wave) => (
                  <AccordionItem key={wave.id} value={wave.id}>
                    <AccordionTrigger className="text-xs">
                      <span className="flex items-center gap-2">
                        <span className="font-mono text-foreground/70">{wave.id}</span>
                        <span className="truncate">{wave.title}</span>
                      </span>
                    </AccordionTrigger>
                    <AccordionContent>
                      <Accordion type="multiple" className="pl-3">
                        {wave.sprints.map((sprint) => (
                          <SprintNode key={sprint.id} sprint={sprint} />
                        ))}
                      </Accordion>
                    </AccordionContent>
                  </AccordionItem>
                ))}
              </Accordion>
            </AccordionContent>
          </AccordionItem>
        ))}
      </Accordion>
    </div>
  )
}
