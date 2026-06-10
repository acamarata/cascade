/**
 * Purpose: Unit tests for the InboxThreadsTab inbox parser.
 *   Spec requirement (T-P3-E06-13 QA-A): inbox parser extracts correct headers
 *   from fixture message files. Covers Subject, From, Date, Priority extraction,
 *   archive/ path detection, and graceful fallback on missing headers.
 * SPORT: MASTER-COMPONENTS.md — InboxThreadsTab tests (T-P3-E06-13)
 */

import { describe, it, expect } from 'vitest'
import { parseInboxEntry } from './InboxThreadsTab'

// ── Fixtures ──────────────────────────────────────────────────────────────────

const FULL_MESSAGE = `From: cascade-agent
Subject: Upgrade hijri-core to v2
Date: 2026-06-10
Priority: high

The dependency hijri-core should be upgraded to v2.0.0 to support the new
Umm al-Qura calendar corrections. Please review before merging.
`

const MINIMAL_MESSAGE = `From: engineer

No subject header present.
`

const ARCHIVE_PATH = '/home/user/Sites/cascade/.claude/inbox/archive/msg-archived.md'
const ACTIVE_PATH = '/home/user/Sites/cascade/.claude/inbox/msg-active.md'

// ── parseInboxEntry ────────────────────────────────────────────────────────────

describe('parseInboxEntry', () => {
  it('extracts Subject: header as subject', () => {
    const entry = parseInboxEntry(FULL_MESSAGE, '/proj', ACTIVE_PATH, 1000)
    expect(entry.subject).toBe('Upgrade hijri-core to v2')
  })

  it('extracts From: header as from', () => {
    const entry = parseInboxEntry(FULL_MESSAGE, '/proj', ACTIVE_PATH, 1000)
    expect(entry.from).toBe('cascade-agent')
  })

  it('extracts Date: header as date', () => {
    const entry = parseInboxEntry(FULL_MESSAGE, '/proj', ACTIVE_PATH, 1000)
    expect(entry.date).toBe('2026-06-10')
  })

  it('extracts Priority: header as priority', () => {
    const entry = parseInboxEntry(FULL_MESSAGE, '/proj', ACTIVE_PATH, 1000)
    expect(entry.priority).toBe('high')
  })

  it('sets archived=false for messages not under archive/ subdir', () => {
    const entry = parseInboxEntry(FULL_MESSAGE, '/proj', ACTIVE_PATH, 1000)
    expect(entry.archived).toBe(false)
  })

  it('sets archived=true for messages under inbox/archive/ subdir', () => {
    const entry = parseInboxEntry(FULL_MESSAGE, '/proj', ARCHIVE_PATH, 1000)
    expect(entry.archived).toBe(true)
  })

  it('falls back to filename stem when Subject: header is absent', () => {
    const entry = parseInboxEntry(MINIMAL_MESSAGE, '/proj', '/proj/.claude/inbox/my-task.md', 0)
    expect(entry.subject).toBe('my-task')
  })

  it('sets from=unknown when From: header is absent', () => {
    const content = `Subject: Something\n\nBody.`
    const entry = parseInboxEntry(content, '/proj', ACTIVE_PATH, 0)
    expect(entry.from).toBe('unknown')
  })

  it('leaves date empty when Date: header is absent', () => {
    const content = `Subject: No date\n\nBody.`
    const entry = parseInboxEntry(content, '/proj', ACTIVE_PATH, 0)
    expect(entry.date).toBe('')
  })

  it('leaves priority empty when Priority: header is absent', () => {
    const content = `Subject: No priority\n\nBody.`
    const entry = parseInboxEntry(content, '/proj', ACTIVE_PATH, 0)
    expect(entry.priority).toBe('')
  })

  it('does not crash on empty content', () => {
    expect(() => parseInboxEntry('', '/p', ACTIVE_PATH, 0)).not.toThrow()
  })

  it('preserves project and path on the entry', () => {
    const entry = parseInboxEntry(FULL_MESSAGE, '/myproject', ACTIVE_PATH, 500)
    expect(entry.project).toBe('/myproject')
    expect(entry.path).toBe(ACTIVE_PATH)
    expect(entry.mtime).toBe(500)
  })

  it('handles CRLF line endings without breaking header parsing', () => {
    const crlfContent = 'Subject: CRLF test\r\nFrom: bot\r\nDate: 2026-01-01\r\n\r\nBody.\r\n'
    const entry = parseInboxEntry(crlfContent, '/p', ACTIVE_PATH, 0)
    expect(entry.subject).toBe('CRLF test')
    expect(entry.from).toBe('bot')
    expect(entry.date).toBe('2026-01-01')
  })

  it('is case-insensitive for header names', () => {
    const content = 'SUBJECT: Case Test\nFROM: sender\nDATE: 2026-01-01\nPRIORITY: low\n\nBody.'
    const entry = parseInboxEntry(content, '/p', ACTIVE_PATH, 0)
    expect(entry.subject).toBe('Case Test')
    expect(entry.from).toBe('sender')
    expect(entry.date).toBe('2026-01-01')
    expect(entry.priority).toBe('low')
  })
})
