/**
 * Purpose: Memory browser — tabbed view over VaultContext.memoryIndex.
 *   Four tabs: Decisions | Lessons | Patterns | Ideas. Each tab shows
 *   a filtered ScrollArea of MemoryCard components. A search input and
 *   project filter dropdown scope the visible entries. Clicking a card
 *   calls VaultContext.setCurrentFile to open the source file in the editor.
 * Inputs:  Reads memoryIndex + setCurrentFile from VaultContext (via useVault).
 * Outputs: Tabbed memory browser panel.
 * Constraints:
 *   - Search + project filter state is NOT reset when switching tabs (persists).
 *   - Empty / loading / error states handled per-tab.
 *   - Reads memoryIndex from VaultContext — no direct IPC calls.
 *   - 'inbox' kind entries are excluded from these four tabs (see T-P3-E06-13).
 *   - a11y: Tabs use Radix semantics; search input has aria-label; filter has aria-label.
 * SPORT: MASTER-COMPONENTS.md — MemoryViewer (T-P3-E06-12)
 */

import React, { useState, useMemo, useEffect } from 'react'
import { Brain, Lightbulb, BookOpen, Repeat2, Search, Inbox } from 'lucide-react'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '../ui/tabs'
import { ScrollArea } from '../ui/scroll-area'
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from '../ui/select'
import { MemoryCard } from './MemoryCard'
import { filterMemoryEntries } from '../../features/memory/memoryFilters'
import {
  InboxThreadsTab,
  buildInboxIndex,
} from '../../features/memory/InboxThreadsTab'
import type { MemoryEntry, MemoryEntryKind } from '../../types/vault'

// ── Tab configuration ─────────────────────────────────────────────────────────

interface TabConfig {
  kind: MemoryEntryKind
  label: string
  icon: React.ReactNode
}

const TABS: TabConfig[] = [
  {
    kind: 'decisions',
    label: 'Decisions',
    icon: <Brain className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />,
  },
  {
    kind: 'lessons',
    label: 'Lessons',
    icon: <BookOpen className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />,
  },
  {
    kind: 'patterns',
    label: 'Patterns',
    icon: <Repeat2 className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />,
  },
  {
    kind: 'ideas',
    label: 'Ideas',
    icon: <Lightbulb className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />,
  },
]

// ── Sub-components ────────────────────────────────────────────────────────────

interface EmptyStateProps {
  query: string
  project: string
}

function EmptyState({ query, project }: EmptyStateProps): React.ReactElement {
  const hasFilter = Boolean(query || (project && project !== 'All'))
  return (
    <div className="flex flex-col items-center justify-center py-12 text-center text-muted-foreground">
      <Brain className="mb-3 h-8 w-8 opacity-30" aria-hidden="true" />
      {hasFilter ? (
        <p className="text-sm">No entries match your filter.</p>
      ) : (
        <p className="text-sm">No entries in this category yet.</p>
      )}
    </div>
  )
}

// ── Tab content panel ─────────────────────────────────────────────────────────

interface TabPanelProps {
  entries: MemoryEntry[]
  query: string
  project: string
  onOpen: (path: string) => void
}

function TabPanel({ entries, query, project, onOpen }: TabPanelProps): React.ReactElement {
  const filtered = useMemo(
    () => filterMemoryEntries(entries, query, project),
    [entries, query, project],
  )

  if (filtered.length === 0) {
    return <EmptyState query={query} project={project} />
  }

  return (
    <ScrollArea className="h-full">
      <ul className="flex flex-col gap-2 p-3" role="list">
        {filtered.map((entry) => (
          <li key={`${entry.path}-${entry.title}`}>
            <MemoryCard entry={entry} onOpen={onOpen} />
          </li>
        ))}
      </ul>
    </ScrollArea>
  )
}

// ── Public component ──────────────────────────────────────────────────────────

interface MemoryViewerProps {
  /** Flat memory index — all kinds. Passed explicitly to decouple from VaultContext. */
  memoryIndex: MemoryEntry[]
  /** Called when a card is clicked to open the source file. */
  onOpenFile: (path: string) => void
  /** True while the memory index is being loaded. */
  loading?: boolean
  /** Error message to display instead of tabs, or null. */
  error?: string | null
  /**
   * Absolute project root paths for the Inbox & Threads tab.
   * Defaults to [] — empty inbox in dev. Passed from VaultContext.projectPaths.
   * T-P3-E06-13
   */
  projectPaths?: string[]
}

/**
 * Tabbed memory browser panel.
 * Search + project filter state persists across tab switches.
 */
export function MemoryViewer({
  memoryIndex,
  onOpenFile,
  loading = false,
  error = null,
  projectPaths = [],
}: MemoryViewerProps): React.ReactElement {
  const [query, setQuery] = useState('')
  const [project, setProject] = useState('All')
  // Unread inbox count for the badge on the Inbox & Threads tab.
  const [unreadCount, setUnreadCount] = useState(0)

  // Derive unique project names from the full index (not filtered) for the dropdown.
  const projectOptions = useMemo(() => {
    const seen = new Set<string>()
    for (const e of memoryIndex) {
      seen.add(e.project)
    }
    return Array.from(seen).sort()
  }, [memoryIndex])

  // Partition entries by kind (inbox excluded — handled by T-P3-E06-13).
  const byKind = useMemo(() => {
    const map = new Map<MemoryEntryKind, MemoryEntry[]>()
    for (const tab of TABS) map.set(tab.kind, [])
    for (const e of memoryIndex) {
      const bucket = map.get(e.type as MemoryEntryKind)
      if (bucket) bucket.push(e)
    }
    return map
  }, [memoryIndex])

  // Unread count badge for the Inbox & Threads tab (non-archived messages only).
  // Re-computed whenever projectPaths changes.
  useEffect(() => {
    let cancelled = false
    buildInboxIndex(projectPaths)
      .then(({ inbox }) => {
        if (!cancelled) setUnreadCount(inbox.filter((e) => !e.archived).length)
      })
      .catch(() => { /* best-effort */ })
    return () => { cancelled = true }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(projectPaths)])

  // ── Loading state ───────────────────────────────────────────────────────────
  if (loading) {
    return (
      <div
        role="status"
        aria-label="Loading memory index"
        className="flex h-full items-center justify-center text-sm text-muted-foreground"
      >
        Loading memory…
      </div>
    )
  }

  // ── Error state ─────────────────────────────────────────────────────────────
  if (error) {
    return (
      <div
        role="alert"
        className="m-4 rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-sm text-destructive"
      >
        {error}
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* ── Header: search + project filter ──────────────────────────────────── */}
      <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2">
        {/* Search input */}
        <div className="relative flex-1">
          <Search
            className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <input
            type="search"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search memory…"
            aria-label="Search memory entries"
            className={[
              'h-8 w-full rounded-md border border-input bg-background pl-8 pr-3 text-sm',
              'placeholder:text-muted-foreground',
              'focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1',
            ].join(' ')}
          />
        </div>

        {/* Project filter */}
        {projectOptions.length > 0 && (
          <div className="w-36 shrink-0">
            <Select value={project} onValueChange={setProject}>
              <SelectTrigger aria-label="Filter by project">
                <SelectValue placeholder="All projects" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="All">All projects</SelectItem>
                {projectOptions.map((p) => (
                  <SelectItem key={p} value={p} title={p}>
                    {p.split('/').pop() ?? p}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        )}
      </div>

      {/* ── Tabbed body ───────────────────────────────────────────────────────── */}
      <Tabs defaultValue="decisions" className="flex flex-1 flex-col overflow-hidden">
        <TabsList className="mx-3 mt-2 w-auto shrink-0 self-start">
          {TABS.map((t) => (
            <TabsTrigger
              key={t.kind}
              value={t.kind}
              className="flex items-center gap-1.5"
            >
              {t.icon}
              <span>{t.label}</span>
              <span className="ml-0.5 rounded-full bg-muted-foreground/20 px-1.5 py-0.5 text-[10px] font-medium tabular-nums">
                {byKind.get(t.kind)?.length ?? 0}
              </span>
            </TabsTrigger>
          ))}
          {/* T-P3-E06-13: Inbox & Threads tab */}
          <TabsTrigger value="inbox" className="flex items-center gap-1.5">
            <Inbox className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span>Inbox</span>
            {unreadCount > 0 && (
              <span
                className="ml-0.5 rounded-full bg-primary/80 px-1.5 py-0.5 text-[10px] font-medium tabular-nums text-primary-foreground"
                aria-label={`${unreadCount} unread messages`}
              >
                {unreadCount}
              </span>
            )}
          </TabsTrigger>
        </TabsList>

        {TABS.map((t) => (
          <TabsContent
            key={t.kind}
            value={t.kind}
            className="flex-1 overflow-hidden focus-visible:outline-none"
          >
            <TabPanel
              entries={byKind.get(t.kind) ?? []}
              query={query}
              project={project}
              onOpen={onOpenFile}
            />
          </TabsContent>
        ))}

        {/* T-P3-E06-13: Inbox & Threads content panel */}
        <TabsContent
          value="inbox"
          className="flex-1 overflow-hidden focus-visible:outline-none"
        >
          <InboxThreadsTab projectPaths={projectPaths} onOpen={onOpenFile} />
        </TabsContent>
      </Tabs>
    </div>
  )
}
