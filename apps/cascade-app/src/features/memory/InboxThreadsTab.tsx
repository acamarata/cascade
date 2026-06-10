/**
 * Purpose: Inbox & Threads tab for the MemoryViewer.
 *   Aggregates inbox messages from {project}/.claude/inbox/ and thread README files
 *   from {project}/.claude/memory/threads/ across all registered project roots.
 *   Renders inbox messages as cards (Subject, From, Date, Priority) with an unread
 *   badge, and thread READMEs in a collapsible section below.
 * Inputs:
 *   projectPaths — absolute paths to registered project roots (from VaultContext).
 *   onOpen       — callback invoked with the source file path when a card is clicked.
 * Outputs: Tabbed content for an "Inbox & Threads" tab in MemoryViewer.
 * Constraints:
 *   - Read-only. No archive writes, no sends.
 *   - Archive detection is path-based: messages under inbox/archive/ are archived.
 *   - Thread section only renders when at least one thread dir is found.
 *   - No crash on missing headers (graceful fallback to filename / "unknown").
 *   - Works in both Tauri webview and Vitest (Node) environments via the same
 *     fs adapter pattern as memoryAggregator.
 * SPORT: MASTER-COMPONENTS.md — InboxThreadsTab (T-P3-E06-13)
 */

import React, { useEffect, useState, useMemo } from 'react'
import { Inbox, MessageSquare, Clock } from 'lucide-react'
import { ScrollArea } from '../../components/ui/scroll-area'
import { projectLabel } from './memoryFilters'
import { formatRelativeTime } from '../../lib/relativeTime'

// ── Types ─────────────────────────────────────────────────────────────────────

export interface InboxEntry {
  /** Absolute path to the project root that owns this message. */
  project: string
  /** Value of the Subject: header (or filename stem as fallback). */
  subject: string
  /** Value of the From: header, or 'unknown' when absent. */
  from: string
  /** Value of the Date: header as a string, or '' when absent. */
  date: string
  /** Value of the Priority: header, or '' when absent. */
  priority: string
  /** Absolute path to the source .md file. */
  path: string
  /** True when the message lives under inbox/archive/ subfolder. */
  archived: boolean
  /** File mtime for sorting (Unix epoch ms). */
  mtime: number
}

export interface ThreadEntry {
  /** Absolute path to the project root that owns this thread. */
  project: string
  /** Thread title (first non-empty line of README.md, or dir name). */
  title: string
  /** Absolute path to the thread README.md. */
  path: string
  /** File mtime for display. */
  mtime: number
}

// ── File-system adapter (same pattern as memoryAggregator) ───────────────────

type FileStat = { mtime: Date | number | null | undefined }

interface FsAdapter {
  readTextFile(path: string): Promise<string>
  readDir(path: string): Promise<Array<{ name?: string | null }>>
  stat(path: string): Promise<FileStat>
}

async function buildFsAdapter(): Promise<FsAdapter> {
  if (typeof process !== 'undefined' && process.versions?.node) {
    const fs = await import('node:fs/promises')
    return {
      async readTextFile(p) { return fs.readFile(p, 'utf8') },
      async readDir(p) {
        const entries = await fs.readdir(p, { withFileTypes: true })
        return entries.map((e) => ({ name: e.name }))
      },
      async stat(p) { const s = await fs.stat(p); return { mtime: s.mtime } },
    }
  }
  const tauriFs = await import('@tauri-apps/plugin-fs')
  return {
    async readTextFile(p) { return tauriFs.readTextFile(p) },
    async readDir(p) { return tauriFs.readDir(p) },
    async stat(p) { return tauriFs.stat(p) as Promise<FileStat> },
  }
}

let _fsAdapter: FsAdapter | null = null
async function getFs(): Promise<FsAdapter> {
  if (!_fsAdapter) _fsAdapter = await buildFsAdapter()
  return _fsAdapter
}

async function getMtime(fs: FsAdapter, path: string): Promise<number> {
  try {
    const s = await fs.stat(path)
    if (!s.mtime) return 0
    if (s.mtime instanceof Date) return s.mtime.getTime()
    if (typeof s.mtime === 'number') return s.mtime
    return 0
  } catch { return 0 }
}

// ── Inbox message parser ──────────────────────────────────────────────────────

/**
 * Purpose: Parse an inbox message file into an InboxEntry.
 * Inputs:  content — file text; project, filePath, mtime — metadata.
 * Outputs: InboxEntry with Subject/From/Date/Priority extracted from headers.
 * Constraints: Never throws. Gracefully handles missing headers.
 */
export function parseInboxEntry(
  content: string,
  project: string,
  filePath: string,
  mtime: number,
): InboxEntry {
  const parts = filePath.replace(/\\/g, '/').split('/')
  const filename = parts[parts.length - 1] ?? ''

  // Archive detection: path-based — message is under .../inbox/archive/
  const normalizedPath = filePath.replace(/\\/g, '/')
  const archived = normalizedPath.includes('/.claude/inbox/archive/')

  const lines = content.replace(/\r\n/g, '\n').split('\n')

  let subject = filename.replace(/\.md$/i, '')
  let from = 'unknown'
  let date = ''
  let priority = ''

  // Parse first block of header lines (before blank line)
  for (const line of lines) {
    if (line.trim() === '') break
    const lower = line.toLowerCase()
    if (lower.startsWith('subject:')) subject = line.slice('subject:'.length).trim() || subject
    else if (lower.startsWith('from:')) from = line.slice('from:'.length).trim() || from
    else if (lower.startsWith('date:')) date = line.slice('date:'.length).trim()
    else if (lower.startsWith('priority:')) priority = line.slice('priority:'.length).trim()
  }

  return { project, subject, from, date, priority, path: filePath, archived, mtime }
}

// ── Per-project inbox + thread scanner ───────────────────────────────────────

async function scanProjectInbox(
  fs: FsAdapter,
  projectRoot: string,
): Promise<{ inbox: InboxEntry[]; threads: ThreadEntry[] }> {
  const inbox: InboxEntry[] = []
  const threads: ThreadEntry[] = []

  // ── Inbox active messages ─────────────────────────────────────────────────
  const inboxDir = `${projectRoot}/.claude/inbox`
  try {
    const files = await fs.readDir(inboxDir)
    for (const entry of files) {
      const name = entry.name
      if (!name || !name.endsWith('.md')) continue
      const filePath = `${inboxDir}/${name}`
      try {
        const content = await fs.readTextFile(filePath)
        const mtime = await getMtime(fs, filePath)
        inbox.push(parseInboxEntry(content, projectRoot, filePath, mtime))
      } catch { /* skip unreadable */ }
    }
  } catch { /* dir absent */ }

  // ── Inbox archive/ messages ───────────────────────────────────────────────
  const archiveDir = `${projectRoot}/.claude/inbox/archive`
  try {
    const archiveFiles = await fs.readDir(archiveDir)
    for (const entry of archiveFiles) {
      const name = entry.name
      if (!name || !name.endsWith('.md')) continue
      const filePath = `${archiveDir}/${name}`
      try {
        const content = await fs.readTextFile(filePath)
        const mtime = await getMtime(fs, filePath)
        inbox.push(parseInboxEntry(content, projectRoot, filePath, mtime))
      } catch { /* skip */ }
    }
  } catch { /* archive dir absent — normal */ }

  // ── Thread READMEs ────────────────────────────────────────────────────────
  const threadsDir = `${projectRoot}/.claude/memory/threads`
  try {
    const threadDirs = await fs.readDir(threadsDir)
    for (const entry of threadDirs) {
      const dirName = entry.name
      if (!dirName) continue
      const readmePath = `${threadsDir}/${dirName}/README.md`
      try {
        const content = await fs.readTextFile(readmePath)
        const mtime = await getMtime(fs, readmePath)
        // Title = first non-empty line (strip leading # if present)
        const firstLine = content
          .replace(/\r\n/g, '\n')
          .split('\n')
          .map((l) => l.replace(/^#+\s*/, '').trim())
          .find((l) => l.length > 0) ?? dirName
        threads.push({ project: projectRoot, title: firstLine, path: readmePath, mtime })
      } catch { /* README absent — skip this thread dir */ }
    }
  } catch { /* threads dir absent */ }

  return { inbox, threads }
}

// ── Public aggregator ─────────────────────────────────────────────────────────

/**
 * Purpose: Scan all project roots and return aggregated inbox entries and thread entries.
 * Inputs:  projectPaths — absolute paths to project roots.
 * Outputs: Promise<{ inbox: InboxEntry[]; threads: ThreadEntry[] }> sorted by mtime desc.
 * Constraints: Never throws. Skips unreadable projects silently.
 */
export async function buildInboxIndex(
  projectPaths: string[],
): Promise<{ inbox: InboxEntry[]; threads: ThreadEntry[] }> {
  if (projectPaths.length === 0) return { inbox: [], threads: [] }

  const fs = await getFs()
  const allInbox: InboxEntry[] = []
  const allThreads: ThreadEntry[] = []

  for (const root of projectPaths) {
    try {
      const { inbox, threads } = await scanProjectInbox(fs, root)
      allInbox.push(...inbox)
      allThreads.push(...threads)
    } catch (err) {
      console.warn(`[inboxIndex] Skipping project ${root}:`, err)
    }
  }

  // Sort by mtime descending.
  allInbox.sort((a, b) => b.mtime - a.mtime)
  allThreads.sort((a, b) => b.mtime - a.mtime)

  return { inbox: allInbox, threads: allThreads }
}

// ── Sub-components ─────────────────────────────────────────────────────────────

interface PriorityBadgeProps { priority: string }

function PriorityBadge({ priority }: PriorityBadgeProps): React.ReactElement | null {
  if (!priority) return null
  const p = priority.toLowerCase()
  const cls =
    p === 'high' || p === 'critical'
      ? 'bg-rose-100 text-rose-700 dark:bg-rose-900/40 dark:text-rose-300'
      : p === 'medium' || p === 'normal'
        ? 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
        : 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300'
  return (
    <span
      className={`inline-flex items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium ${cls}`}
    >
      {priority}
    </span>
  )
}

interface InboxCardProps { entry: InboxEntry; onOpen: (path: string) => void }

function InboxCard({ entry, onOpen }: InboxCardProps): React.ReactElement {
  const label = projectLabel(entry.project)
  const relTime = formatRelativeTime(entry.mtime)
  return (
    <button
      type="button"
      onClick={() => onOpen(entry.path)}
      className={[
        'w-full text-left rounded-lg border bg-card px-4 py-3 transition-colors cursor-pointer',
        'hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
        entry.archived ? 'border-border/50 opacity-60' : 'border-border',
      ].join(' ')}
      aria-label={`Open inbox message: ${entry.subject} from ${label}`}
    >
      {/* Top row: project badge + priority + timestamp */}
      <div className="flex items-center gap-2 mb-1.5 flex-wrap">
        <span
          className="inline-flex items-center rounded-full bg-blue-100 text-blue-800 dark:bg-blue-900/40 dark:text-blue-300 px-2 py-0.5 text-[10px] font-medium"
          title={entry.project}
        >
          {label}
        </span>
        <PriorityBadge priority={entry.priority} />
        {entry.archived && (
          <span className="text-[10px] text-muted-foreground italic">archived</span>
        )}
        <time
          dateTime={entry.mtime ? new Date(entry.mtime).toISOString() : ''}
          className="ml-auto text-[11px] text-muted-foreground shrink-0"
        >
          {relTime}
        </time>
      </div>
      {/* Subject */}
      <p className="text-sm font-semibold leading-snug text-foreground truncate">
        {entry.subject}
      </p>
      {/* From + Date */}
      <p className="mt-0.5 text-xs text-muted-foreground truncate">
        From: {entry.from}{entry.date ? ` · ${entry.date}` : ''}
      </p>
    </button>
  )
}

interface ThreadCardProps { entry: ThreadEntry; onOpen: (path: string) => void }

function ThreadCard({ entry, onOpen }: ThreadCardProps): React.ReactElement {
  const label = projectLabel(entry.project)
  const relTime = formatRelativeTime(entry.mtime)
  return (
    <button
      type="button"
      onClick={() => onOpen(entry.path)}
      className={[
        'w-full text-left rounded-lg border border-border bg-card px-4 py-3 transition-colors cursor-pointer',
        'hover:bg-accent/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
      ].join(' ')}
      aria-label={`Open thread: ${entry.title} from ${label}`}
    >
      <div className="flex items-center gap-2 mb-1">
        <span
          className="inline-flex items-center rounded-full bg-purple-100 text-purple-800 dark:bg-purple-900/40 dark:text-purple-300 px-2 py-0.5 text-[10px] font-medium"
          title={entry.project}
        >
          {label}
        </span>
        <time
          dateTime={entry.mtime ? new Date(entry.mtime).toISOString() : ''}
          className="ml-auto text-[11px] text-muted-foreground shrink-0"
        >
          {relTime}
        </time>
      </div>
      <p className="text-sm font-semibold leading-snug text-foreground truncate flex items-center gap-1.5">
        <MessageSquare className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        {entry.title}
      </p>
    </button>
  )
}

// ── Main export ────────────────────────────────────────────────────────────────

export interface InboxThreadsTabProps {
  /** Absolute project paths to scan (from VaultContext.projectPaths). */
  projectPaths: string[]
  /** Called when user clicks a message or thread card to open the source file. */
  onOpen: (path: string) => void
}

/**
 * Inbox & Threads tab content for MemoryViewer.
 * Renders active inbox messages, archived messages (dimmed), and thread READMEs.
 */
export function InboxThreadsTab({
  projectPaths,
  onOpen,
}: InboxThreadsTabProps): React.ReactElement {
  const [inbox, setInbox] = useState<InboxEntry[]>([])
  const [threads, setThreads] = useState<ThreadEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    buildInboxIndex(projectPaths)
      .then(({ inbox: i, threads: t }) => {
        if (!cancelled) { setInbox(i); setThreads(t) }
      })
      .catch((e) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(projectPaths)])

  // Split inbox into active/archived for display order (active first).
  const activeInbox = useMemo(() => inbox.filter((e) => !e.archived), [inbox])
  const archivedInbox = useMemo(() => inbox.filter((e) => e.archived), [inbox])

  if (loading) {
    return (
      <div
        role="status"
        aria-label="Loading inbox"
        className="flex h-full items-center justify-center text-sm text-muted-foreground"
      >
        Loading inbox…
      </div>
    )
  }

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

  const hasContent = inbox.length > 0 || threads.length > 0

  if (!hasContent) {
    return (
      <div className="flex flex-col items-center justify-center py-12 text-center text-muted-foreground">
        <Inbox className="mb-3 h-8 w-8 opacity-30" aria-hidden="true" />
        <p className="text-sm">No inbox messages or threads found.</p>
        <p className="mt-1 text-xs">Add messages to {'{project}'}/.claude/inbox/</p>
      </div>
    )
  }

  return (
    <ScrollArea className="h-full">
      <div className="flex flex-col gap-4 p-3">
        {/* ── Active inbox messages ────────────────────────────────────────── */}
        {activeInbox.length > 0 && (
          <section>
            <h3 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              <Inbox className="h-3.5 w-3.5" aria-hidden="true" />
              Inbox
            </h3>
            <ul className="flex flex-col gap-2" role="list">
              {activeInbox.map((entry) => (
                <li key={entry.path}>
                  <InboxCard entry={entry} onOpen={onOpen} />
                </li>
              ))}
            </ul>
          </section>
        )}

        {/* ── Threads section — only rendered when threads exist ────────────── */}
        {threads.length > 0 && (
          <section>
            <h3 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              <MessageSquare className="h-3.5 w-3.5" aria-hidden="true" />
              Threads
            </h3>
            <ul className="flex flex-col gap-2" role="list">
              {threads.map((entry) => (
                <li key={entry.path}>
                  <ThreadCard entry={entry} onOpen={onOpen} />
                </li>
              ))}
            </ul>
          </section>
        )}

        {/* ── Archived messages ─────────────────────────────────────────────── */}
        {archivedInbox.length > 0 && (
          <section>
            <h3 className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
              <Clock className="h-3.5 w-3.5" aria-hidden="true" />
              Archived
            </h3>
            <ul className="flex flex-col gap-2" role="list">
              {archivedInbox.map((entry) => (
                <li key={entry.path}>
                  <InboxCard entry={entry} onOpen={onOpen} />
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </ScrollArea>
  )
}
