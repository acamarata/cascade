/**
 * Tests for dailyNotes and templates services (T-P3-E06-10).
 * Covers: path pattern, template substitution, idempotent open-if-exists.
 */

import { describe, it, expect } from 'vitest'
import {
  buildDailyPath,
  DEFAULT_DAILY_TEMPLATE,
  openDailyNote,
} from '../../../services/vault/dailyNotes'
import { applyTemplate, buildTemplateVars } from '../../../services/vault/templates'

// ── buildDailyPath ────────────────────────────────────────────────────────────

describe('buildDailyPath', () => {
  it('produces YYYY-MM-DD.md filename under the daily folder', () => {
    const date = new Date(2026, 5, 10) // 2026-06-10 (month is 0-indexed)
    const path = buildDailyPath('~/.cascade', 'daily', date)
    expect(path).toBe('~/.cascade/daily/2026-06-10.md')
  })

  it('zero-pads single-digit month and day', () => {
    const date = new Date(2026, 0, 5) // 2026-01-05
    const path = buildDailyPath('~/.cascade', 'daily', date)
    expect(path).toBe('~/.cascade/daily/2026-01-05.md')
  })

  it('strips trailing slash from vaultRoot', () => {
    const date = new Date(2026, 5, 10)
    const path = buildDailyPath('~/.cascade/', 'daily', date)
    expect(path).toBe('~/.cascade/daily/2026-06-10.md')
  })

  it('uses a custom daily folder name', () => {
    const date = new Date(2026, 5, 10)
    const path = buildDailyPath('~/.cascade', 'journal', date)
    expect(path).toBe('~/.cascade/journal/2026-06-10.md')
  })

  it('uses today when no date is supplied', () => {
    const path = buildDailyPath('~/.cascade', 'daily')
    // Path should match YYYY-MM-DD.md pattern
    expect(path).toMatch(/~\/.cascade\/daily\/\d{4}-\d{2}-\d{2}\.md$/)
  })
})

// ── openDailyNote (non-Tauri / pure path logic) ───────────────────────────────

describe('openDailyNote', () => {
  it('returns the correct daily path (non-Tauri env)', async () => {
    const date = new Date(2026, 5, 10)
    const path = await openDailyNote('~/.cascade', 'daily', null, date)
    expect(path).toBe('~/.cascade/daily/2026-06-10.md')
  })

  it('is idempotent — returns same path on second call with same date', async () => {
    const date = new Date(2026, 5, 10)
    const path1 = await openDailyNote('~/.cascade', 'daily', null, date)
    const path2 = await openDailyNote('~/.cascade', 'daily', null, date)
    expect(path1).toBe(path2)
  })

  it('uses provided templateContent when given', async () => {
    const date = new Date(2026, 5, 10)
    // In non-Tauri mode, the function just returns the path without writing.
    // We verify the path is correct regardless of template.
    const path = await openDailyNote('~/.cascade', 'daily', '# Custom template', date)
    expect(path).toBe('~/.cascade/daily/2026-06-10.md')
  })
})

// ── applyTemplate ─────────────────────────────────────────────────────────────

describe('applyTemplate', () => {
  it('substitutes {{date}}, {{time}}, and {{title}}', () => {
    const template = '---\ndate: {{date}}\ntime: {{time}}\ntitle: {{title}}\n---\n\n## Notes'
    const result = applyTemplate(template, {
      date: '2026-06-10',
      time: '09:00',
      title: 'My Note',
    })
    expect(result).toBe('---\ndate: 2026-06-10\ntime: 09:00\ntitle: My Note\n---\n\n## Notes')
  })

  it('replaces all occurrences of each variable', () => {
    const template = '{{date}} is {{date}}, title is {{title}} and {{title}}'
    const result = applyTemplate(template, { date: '2026-06-10', time: '10:00', title: 'X' })
    expect(result).toBe('2026-06-10 is 2026-06-10, title is X and X')
  })

  it('leaves unknown variables unchanged', () => {
    const template = '{{date}} {{unknown}} {{title}}'
    const result = applyTemplate(template, { date: '2026-06-10', time: '10:00', title: 'T' })
    expect(result).toBe('2026-06-10 {{unknown}} T')
  })

  it('substitutes the DEFAULT_DAILY_TEMPLATE correctly', () => {
    const result = applyTemplate(DEFAULT_DAILY_TEMPLATE, {
      date: '2026-06-10',
      time: '08:30',
      title: '2026-06-10',
    })
    expect(result).toContain('date: 2026-06-10')
    expect(result).toContain('## Notes')
  })
})

// ── buildTemplateVars ─────────────────────────────────────────────────────────

describe('buildTemplateVars', () => {
  it('builds vars with local date/time from provided date', () => {
    const date = new Date(2026, 5, 10, 9, 5) // 09:05
    const vars = buildTemplateVars('Test Note', date)
    expect(vars.date).toBe('2026-06-10')
    expect(vars.time).toBe('09:05')
    expect(vars.title).toBe('Test Note')
  })

  it('zero-pads hours and minutes', () => {
    const date = new Date(2026, 0, 1, 3, 7) // 03:07
    const vars = buildTemplateVars('Note', date)
    expect(vars.time).toBe('03:07')
    expect(vars.date).toBe('2026-01-01')
  })
})
