/**
 * Purpose: Unit tests for the cross-project memory aggregator service.
 * Inputs:  Fixture directories created in tmpdir; mocked fs adapter via vi.mock.
 *   All tests use the pure parser exports (no real IPC or filesystem).
 * Outputs: Verified heading parsing, idea file parsing, inbox subject parsing,
 *   kind classification, malformed/empty file handling, and sort order.
 * Constraints: No real Tauri IPC. Node fs/promises is used in Vitest environment
 *   automatically (via the adapter detection in memoryAggregator.ts).
 *   #[serial(global_env)] equivalent: tests that touch process.env.HOME use
 *   beforeEach/afterEach to isolate the HOME value.
 * SPORT: MASTER-COMPONENTS.md — memoryAggregator tests (T-P3-E06-11)
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import os from 'node:os'
import path from 'node:path'
import fs from 'node:fs/promises'

import {
  parseHeadingSections,
  parseIdeaFile,
  parseInboxFile,
  buildMemoryIndex,
} from './memoryAggregator'

// ── Helper: build a temp project skeleton ─────────────────────────────────────

async function makeTempProject(name: string): Promise<string> {
  const root = path.join(os.tmpdir(), `cascade-test-${name}-${Date.now()}`)
  await fs.mkdir(path.join(root, '.claude', 'memory'), { recursive: true })
  await fs.mkdir(path.join(root, '.claude', 'ideas'), { recursive: true })
  await fs.mkdir(path.join(root, '.claude', 'inbox'), { recursive: true })
  return root
}

async function cleanTempProject(root: string): Promise<void> {
  try {
    await fs.rm(root, { recursive: true, force: true })
  } catch {
    // best-effort
  }
}

// ── parseHeadingSections ───────────────────────────────────────────────────────

describe('parseHeadingSections', () => {
  it('extracts titles and excerpts from a multi-section markdown file', () => {
    const content = `
# Top-level heading (ignored)

## Decision: Use Tauri 2

We chose Tauri 2 over Electron to reduce binary size and leverage native OS APIs.

## Lesson: pnpm only

All JS packages must use pnpm. npm/yarn are never used.

Second paragraph of lesson (not in excerpt).
`
    const entries = parseHeadingSections(
      content,
      '/home/user/proj',
      '/home/user/proj/.claude/memory/decisions.md',
      'decisions',
      1000
    )

    expect(entries).toHaveLength(2)

    expect(entries[0].title).toBe('Decision: Use Tauri 2')
    expect(entries[0].type).toBe('decisions')
    expect(entries[0].excerpt).toContain('Tauri 2')
    expect(entries[0].project).toBe('/home/user/proj')
    expect(entries[0].mtime).toBe(1000)

    expect(entries[1].title).toBe('Lesson: pnpm only')
    expect(entries[1].excerpt).toContain('pnpm')
  })

  it('returns empty array for an empty file', () => {
    expect(
      parseHeadingSections('', '/p', '/p/.claude/memory/decisions.md', 'decisions', 0)
    ).toEqual([])
  })

  it('returns empty array for a file with no ## headings', () => {
    const content = 'Just some text\nwith no headings at all.\n'
    expect(
      parseHeadingSections(content, '/p', '/p/.claude/memory/decisions.md', 'decisions', 0)
    ).toEqual([])
  })

  it('returns "No excerpt" when heading section body is empty', () => {
    const content = `## Empty Section\n\n`
    const entries = parseHeadingSections(content, '/p', '/p/decisions.md', 'lessons', 0)
    expect(entries).toHaveLength(1)
    expect(entries[0].excerpt).toBe('No excerpt')
  })

  it('handles CRLF line endings without breaking parsing', () => {
    const content = '## CRLF Title\r\n\r\nSome body text here.\r\n'
    const entries = parseHeadingSections(content, '/p', '/p/decisions.md', 'patterns', 0)
    expect(entries).toHaveLength(1)
    expect(entries[0].title).toBe('CRLF Title')
    expect(entries[0].excerpt).toBe('Some body text here.')
  })

  it('does not crash on a file that is only whitespace', () => {
    const content = '   \n\t\n  '
    const entries = parseHeadingSections(content, '/p', '/p/decisions.md', 'decisions', 0)
    expect(entries).toEqual([])
  })

  it('respects all three memory kinds', () => {
    const content = '## Entry\n\nBody.\n'
    const kinds = ['decisions', 'lessons', 'patterns'] as const
    for (const kind of kinds) {
      const [entry] = parseHeadingSections(content, '/p', '/p/file.md', kind, 0)
      expect(entry.type).toBe(kind)
    }
  })
})

// ── parseIdeaFile ──────────────────────────────────────────────────────────────

describe('parseIdeaFile', () => {
  it('uses the filename stem as title', () => {
    const entry = parseIdeaFile(
      'Some idea text.',
      '/proj',
      '/proj/.claude/ideas/my-great-idea.md',
      500
    )
    expect(entry.title).toBe('my great idea')
    expect(entry.type).toBe('ideas')
    expect(entry.mtime).toBe(500)
  })

  it('returns first 3 non-empty lines as excerpt', () => {
    const content = 'Line one.\nLine two.\nLine three.\nLine four (not included).'
    const entry = parseIdeaFile(content, '/p', '/p/.claude/ideas/idea.md', 0)
    expect(entry.excerpt).toBe('Line one. Line two. Line three.')
  })

  it('returns "No excerpt" for an empty file', () => {
    const entry = parseIdeaFile('', '/p', '/p/.claude/ideas/empty.md', 0)
    expect(entry.excerpt).toBe('No excerpt')
  })

  it('handles filename with underscores', () => {
    const entry = parseIdeaFile('text', '/p', '/p/.claude/ideas/use_rust_for_rag.md', 0)
    expect(entry.title).toBe('use rust for rag')
  })
})

// ── parseInboxFile ─────────────────────────────────────────────────────────────

describe('parseInboxFile', () => {
  it('extracts Subject: header as title', () => {
    const content = `From: agent\nSubject: Upgrade hijri-core to v2\nDate: 2026-06-10\n\nBody of message here.`
    const entry = parseInboxFile(content, '/proj', '/proj/.claude/inbox/001-msg.md', 800)
    expect(entry.title).toBe('Upgrade hijri-core to v2')
    expect(entry.type).toBe('inbox')
    expect(entry.excerpt).toContain('Body of message')
  })

  it('falls back to filename stem when Subject: header is missing', () => {
    const content = `From: agent\n\nBody text.`
    const entry = parseInboxFile(content, '/p', '/p/.claude/inbox/my-message.md', 0)
    expect(entry.title).toBe('my-message')
  })

  it('gracefully handles missing blank-line separator (no body)', () => {
    const content = `Subject: No body follows`
    const entry = parseInboxFile(content, '/p', '/p/.claude/inbox/msg.md', 0)
    expect(entry.title).toBe('No body follows')
    expect(entry.excerpt).toBe('No excerpt')
  })

  it('does not crash on empty content', () => {
    const entry = parseInboxFile('', '/p', '/p/.claude/inbox/empty.md', 0)
    expect(entry.excerpt).toBe('No excerpt')
  })
})

// ── buildMemoryIndex — integration with real temp filesystem ──────────────────

describe('buildMemoryIndex', () => {
  let originalHome: string | undefined
  let tempRoots: string[] = []

  beforeEach(() => {
    originalHome = process.env.HOME
    tempRoots = []
  })

  afterEach(async () => {
    if (originalHome !== undefined) process.env.HOME = originalHome
    else delete process.env.HOME
    for (const root of tempRoots) {
      await cleanTempProject(root)
    }
  })

  it('returns empty array for empty projectPaths', async () => {
    const result = await buildMemoryIndex([])
    expect(result).toEqual([])
  })

  it('returns MemoryEntry[] with all kinds from a well-formed project', async () => {
    const root = await makeTempProject('full')
    tempRoots.push(root)

    // Set HOME to parent so HOME-confinement passes.
    process.env.HOME = path.dirname(root)

    // Write memory files.
    await fs.writeFile(
      path.join(root, '.claude', 'memory', 'decisions.md'),
      '## Decision A\n\nWe decided X.\n\n## Decision B\n\nWe decided Y.\n'
    )
    await fs.writeFile(
      path.join(root, '.claude', 'memory', 'lessons.md'),
      '## Lesson 1\n\nDo not use npm.\n'
    )
    await fs.writeFile(
      path.join(root, '.claude', 'memory', 'patterns.md'),
      '## Pattern Alpha\n\nAlways DRY.\n'
    )

    // Write an idea.
    await fs.writeFile(
      path.join(root, '.claude', 'ideas', 'cool-feature.md'),
      'First line of idea.\nSecond line.\n'
    )

    // Write an inbox message.
    await fs.writeFile(
      path.join(root, '.claude', 'inbox', 'msg-001.md'),
      'Subject: Consider upgrading\n\nPlease upgrade the dependency.\n'
    )

    const entries = await buildMemoryIndex([root])

    // Should have: 2 decisions + 1 lesson + 1 pattern + 1 idea + 1 inbox = 6
    expect(entries.length).toBe(6)

    const kinds = entries.map((e) => e.type)
    expect(kinds).toContain('decisions')
    expect(kinds).toContain('lessons')
    expect(kinds).toContain('patterns')
    expect(kinds).toContain('ideas')
    expect(kinds).toContain('inbox')

    // All entries reference the correct project root.
    for (const entry of entries) {
      expect(entry.project).toBe(root)
    }

    // All entries have non-empty titles and excerpts.
    for (const entry of entries) {
      expect(entry.title).toBeTruthy()
      expect(entry.excerpt).toBeTruthy()
    }
  })

  it('skips a malformed (unreadable) file without throwing', async () => {
    const root = await makeTempProject('malformed')
    tempRoots.push(root)
    process.env.HOME = path.dirname(root)

    // Write one valid memory file and intentionally provide a dir where a file is expected
    // (simulates an unreadable/malformed entry — the dir won't be read as text).
    await fs.writeFile(
      path.join(root, '.claude', 'memory', 'lessons.md'),
      '## Good Lesson\n\nContent here.\n'
    )
    // Create a sub-directory as an "idea" so readTextFile will fail on it.
    await fs.mkdir(path.join(root, '.claude', 'ideas', 'not-a-file.md'), { recursive: true })

    await expect(buildMemoryIndex([root])).resolves.not.toThrow()
    const entries = await buildMemoryIndex([root])
    expect(entries.some((e) => e.type === 'lessons')).toBe(true)
  })

  it('silently skips non-HOME-confined project paths', async () => {
    const root = await makeTempProject('outside')
    tempRoots.push(root)

    // Set HOME somewhere that root is outside of.
    process.env.HOME = '/some/other/path'

    await fs.writeFile(path.join(root, '.claude', 'memory', 'decisions.md'), '## D\n\nBody.\n')

    const entries = await buildMemoryIndex([root])
    expect(entries).toEqual([])
  })

  it('sorts entries by mtime descending', async () => {
    const root = await makeTempProject('sort')
    tempRoots.push(root)
    process.env.HOME = path.dirname(root)

    // Write three files with different access times.
    const decisionsPath = path.join(root, '.claude', 'memory', 'decisions.md')
    const lessonsPath = path.join(root, '.claude', 'memory', 'lessons.md')

    await fs.writeFile(decisionsPath, '## Older\n\nOld content.\n')
    // Small delay to ensure different mtime.
    await new Promise((r) => setTimeout(r, 20))
    await fs.writeFile(lessonsPath, '## Newer\n\nNew content.\n')

    const entries = await buildMemoryIndex([root])
    expect(entries.length).toBeGreaterThanOrEqual(2)

    // Verify descending order.
    for (let i = 1; i < entries.length; i++) {
      expect(entries[i - 1].mtime).toBeGreaterThanOrEqual(entries[i].mtime)
    }
  })

  it('aggregates entries across multiple project roots', async () => {
    const root1 = await makeTempProject('multi-a')
    const root2 = await makeTempProject('multi-b')
    tempRoots.push(root1, root2)

    // Use a common ancestor as HOME.
    process.env.HOME = path.dirname(root1)

    await fs.writeFile(path.join(root1, '.claude', 'memory', 'decisions.md'), '## D1\n\nBody.\n')
    await fs.writeFile(path.join(root2, '.claude', 'memory', 'lessons.md'), '## L2\n\nBody.\n')

    const entries = await buildMemoryIndex([root1, root2])
    const projects = new Set(entries.map((e) => e.project))
    expect(projects.has(root1)).toBe(true)
    expect(projects.has(root2)).toBe(true)
  })
})
