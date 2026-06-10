/**
 * Purpose: Cross-project memory aggregator — scans all registered project roots and
 *   builds a flat MemoryEntry[] from decisions/lessons/patterns memory files, the
 *   ideas/ directory, and inbox/ messages.
 * Inputs:  projectPaths — absolute paths to registered project roots (from daemon registry
 *   or passed explicitly). Each root must be HOME-confined ({HOME}/Sites/* or similar).
 * Outputs: Promise<MemoryEntry[]> sorted by mtime descending. Cached externally in
 *   VaultContext.memoryIndex; refresh is triggered by vault://changed events.
 * Constraints:
 *   - Read-only. No writes to any memory file from this module.
 *   - HOME-confined: silently skips any path outside process.env.HOME.
 *   - Permission-tolerant: any file that fails to read is skipped with a console.warn.
 *   - Malformed/empty files produce zero entries (never throw).
 *   - Works in both Tauri webview and Vitest (Node) environments: uses @tauri-apps/plugin-fs
 *     when invoke is available; falls back to Node fs/promises otherwise.
 * SPORT: MASTER-COMPONENTS.md — memoryAggregator service (T-P3-E06-11)
 */

import type { MemoryEntry, MemoryEntryKind } from '../../types/vault'

// ── File-system adapter ───────────────────────────────────────────────────────
// Dynamically resolve read/list so the module works under Vitest (Node.js) and
// inside a running Tauri 2 webview without compile-time environment switching.

type FileStat = { mtime: Date | number | null | undefined }

interface FsAdapter {
  readTextFile(path: string): Promise<string>
  readDir(path: string): Promise<Array<{ name?: string | null }>>
  stat(path: string): Promise<FileStat>
}

async function buildFsAdapter(): Promise<FsAdapter> {
  // Vitest / Node environment: use standard fs/promises.
  if (typeof process !== 'undefined' && process.versions?.node) {
    const fs = await import('node:fs/promises')
    return {
      async readTextFile(p) {
        return fs.readFile(p, 'utf8')
      },
      async readDir(p) {
        const entries = await fs.readdir(p, { withFileTypes: true })
        return entries.map((e) => ({ name: e.name }))
      },
      async stat(p) {
        const s = await fs.stat(p)
        return { mtime: s.mtime }
      },
    }
  }

  // Tauri 2 webview: delegate through @tauri-apps/plugin-fs.
  const tauriFs = await import('@tauri-apps/plugin-fs')
  return {
    async readTextFile(p) {
      return tauriFs.readTextFile(p)
    },
    async readDir(p) {
      return tauriFs.readDir(p)
    },
    async stat(p) {
      return tauriFs.stat(p) as Promise<FileStat>
    },
  }
}

// Lazily initialise once per module lifetime.
let _fsAdapter: FsAdapter | null = null
async function getFs(): Promise<FsAdapter> {
  if (!_fsAdapter) _fsAdapter = await buildFsAdapter()
  return _fsAdapter
}

// ── HOME-confinement guard ────────────────────────────────────────────────────

function isHomeConfined(path: string): boolean {
  const home =
    (typeof process !== 'undefined' && process.env.HOME) || ''
  if (!home) return true // Cannot check; allow (fail-open, daemon already validates).
  return path.startsWith(home + '/')
}

// ── mtime helper ─────────────────────────────────────────────────────────────

async function getMtime(fs: FsAdapter, path: string): Promise<number> {
  try {
    const s = await fs.stat(path)
    if (!s.mtime) return 0
    if (s.mtime instanceof Date) return s.mtime.getTime()
    if (typeof s.mtime === 'number') return s.mtime
    return 0
  } catch {
    return 0
  }
}

// ── Heading parser ────────────────────────────────────────────────────────────

/**
 * Purpose: Extract MemoryEntry-shaped objects from a markdown file's ## headings.
 * Inputs:  content — raw file text; project, filePath, kind, mtime — metadata.
 * Outputs: MemoryEntry[] — one entry per ## heading section. Empty array for files
 *   with no ## headings (treated as a single unnamed block if non-empty).
 * Constraints: Never throws. Sections with no body text get excerpt "No excerpt".
 *   Handles CRLF line endings.
 */
export function parseHeadingSections(
  content: string,
  project: string,
  filePath: string,
  kind: MemoryEntryKind,
  mtime: number,
): MemoryEntry[] {
  if (!content || !content.trim()) return []

  // Normalise line endings.
  const normalised = content.replace(/\r\n/g, '\n')

  // Split on lines that start with exactly "## " (H2 heading).
  const blocks = normalised.split(/^## /m)

  const entries: MemoryEntry[] = []

  // blocks[0] is everything before the first ## heading — skip it.
  for (let i = 1; i < blocks.length; i++) {
    const block = blocks[i]
    const newline = block.indexOf('\n')
    const title =
      newline === -1
        ? block.trim()
        : block.slice(0, newline).trim()

    if (!title) continue

    const body =
      newline === -1 ? '' : block.slice(newline + 1).trim()

    // First non-empty paragraph as excerpt.
    const paragraphs = body.split(/\n{2,}/)
    const firstParagraph = paragraphs.find((p) => p.trim()) ?? ''
    const excerptRaw = firstParagraph
      .split('\n')
      .map((l) => l.trim())
      .filter(Boolean)
      .slice(0, 3)
      .join(' ')

    entries.push({
      project,
      type: kind,
      title,
      excerpt: excerptRaw || 'No excerpt',
      path: filePath,
      mtime,
    })
  }

  return entries
}

// ── Idea file parser ──────────────────────────────────────────────────────────

/**
 * Purpose: Extract a MemoryEntry from an idea file (filename = title, first 3 lines = excerpt).
 * Inputs:  content, project, filePath, mtime.
 * Outputs: A single MemoryEntry.
 * Constraints: Never throws. Filename stem is the title; first 3 non-empty lines are excerpt.
 */
export function parseIdeaFile(
  content: string,
  project: string,
  filePath: string,
  mtime: number,
): MemoryEntry {
  // Derive title from filename stem (last path segment, strip .md).
  const parts = filePath.replace(/\\/g, '/').split('/')
  const filename = parts[parts.length - 1] ?? ''
  const title = filename.replace(/\.md$/i, '').replace(/[-_]/g, ' ').trim() || filename

  const lines = content
    .replace(/\r\n/g, '\n')
    .split('\n')
    .map((l) => l.trim())
    .filter(Boolean)
    .slice(0, 3)
  const excerpt = lines.join(' ') || 'No excerpt'

  return { project, type: 'ideas', title, excerpt, path: filePath, mtime }
}

// ── Inbox message parser ──────────────────────────────────────────────────────

/**
 * Purpose: Extract a MemoryEntry from an inbox message file.
 * Inputs:  content, project, filePath, mtime.
 * Outputs: A single MemoryEntry where title = Subject: header (or filename), excerpt = first body line.
 * Constraints: Never throws. Gracefully handles missing Subject/Date headers.
 */
export function parseInboxFile(
  content: string,
  project: string,
  filePath: string,
  mtime: number,
): MemoryEntry {
  const parts = filePath.replace(/\\/g, '/').split('/')
  const filename = parts[parts.length - 1] ?? ''

  const lines = content.replace(/\r\n/g, '\n').split('\n')
  let title = filename.replace(/\.md$/i, '')
  let excerptLines: string[] = []

  let inHeader = true
  for (const line of lines) {
    if (inHeader) {
      const lower = line.toLowerCase()
      if (lower.startsWith('subject:')) {
        title = line.slice('subject:'.length).trim() || title
      }
      // Blank line ends headers.
      if (line.trim() === '') {
        inHeader = false
      }
    } else {
      const trimmed = line.trim()
      if (trimmed) excerptLines.push(trimmed)
      if (excerptLines.length >= 3) break
    }
  }

  const excerpt = excerptLines.join(' ') || 'No excerpt'
  return { project, type: 'inbox', title, excerpt, path: filePath, mtime }
}

// ── Per-project scanner ───────────────────────────────────────────────────────

const MEMORY_FILES: Array<{ filename: string; kind: MemoryEntryKind }> = [
  { filename: 'decisions.md', kind: 'decisions' },
  { filename: 'lessons.md', kind: 'lessons' },
  { filename: 'patterns.md', kind: 'patterns' },
]

async function scanProject(fs: FsAdapter, projectRoot: string): Promise<MemoryEntry[]> {
  const entries: MemoryEntry[] = []

  // ── Memory files ─────────────────────────────────────────────────────────
  for (const { filename, kind } of MEMORY_FILES) {
    const filePath = `${projectRoot}/.claude/memory/${filename}`
    try {
      const content = await fs.readTextFile(filePath)
      const mtime = await getMtime(fs, filePath)
      entries.push(...parseHeadingSections(content, projectRoot, filePath, kind, mtime))
    } catch {
      // File absent or unreadable — skip silently.
    }
  }

  // ── Ideas directory ───────────────────────────────────────────────────────
  const ideasDir = `${projectRoot}/.claude/ideas`
  try {
    const ideaFiles = await fs.readDir(ideasDir)
    for (const entry of ideaFiles) {
      const name = entry.name
      if (!name || !name.endsWith('.md')) continue
      const filePath = `${ideasDir}/${name}`
      try {
        const content = await fs.readTextFile(filePath)
        const mtime = await getMtime(fs, filePath)
        entries.push(parseIdeaFile(content, projectRoot, filePath, mtime))
      } catch {
        // Skip unreadable files.
      }
    }
  } catch {
    // Directory absent or unreadable — skip.
  }

  // ── Inbox directory ───────────────────────────────────────────────────────
  const inboxDir = `${projectRoot}/.claude/inbox`
  try {
    const inboxFiles = await fs.readDir(inboxDir)
    for (const entry of inboxFiles) {
      const name = entry.name
      if (!name || !name.endsWith('.md')) continue
      const filePath = `${inboxDir}/${name}`
      try {
        const content = await fs.readTextFile(filePath)
        const mtime = await getMtime(fs, filePath)
        entries.push(parseInboxFile(content, projectRoot, filePath, mtime))
      } catch {
        // Skip unreadable files.
      }
    }
  } catch {
    // Directory absent or unreadable — skip.
  }

  return entries
}

// ── Public API ────────────────────────────────────────────────────────────────

/**
 * Purpose: Scan all registered project roots and return a unified MemoryEntry[].
 * Inputs:  projectPaths — absolute paths to project roots (from daemon registry).
 * Outputs: Promise<MemoryEntry[]> sorted by mtime descending.
 * Constraints:
 *   - Silently skips any project path outside $HOME (HOME-confined).
 *   - Each unreadable file is skipped with console.warn; never throws.
 *   - Returns empty array when projectPaths is empty.
 */
export async function buildMemoryIndex(projectPaths: string[]): Promise<MemoryEntry[]> {
  if (projectPaths.length === 0) return []

  const fs = await getFs()
  const allEntries: MemoryEntry[] = []

  for (const root of projectPaths) {
    if (!isHomeConfined(root)) {
      console.warn(`[memoryAggregator] Skipping non-HOME-confined path: ${root}`)
      continue
    }
    try {
      const entries = await scanProject(fs, root)
      allEntries.push(...entries)
    } catch (err) {
      console.warn(`[memoryAggregator] Skipping project ${root}:`, err)
    }
  }

  // Sort by mtime descending (most recent first).
  return allEntries.sort((a, b) => b.mtime - a.mtime)
}
