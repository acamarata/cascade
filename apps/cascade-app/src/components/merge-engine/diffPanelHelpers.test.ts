/**
 * Purpose: Unit tests for diffPanelHelpers pure functions.
 *   Covers status reducer, conflict lookup, highlight resolution, and CSS class helpers.
 *
 * Task: T-P3-E03-28
 */

import { describe, it, expect } from 'vitest'
import {
  nextSectionStatus,
  findConflictForSection,
  resolveHighlightSourceId,
  sectionBorderClass,
  sectionStatusBadgeClass,
} from './diffPanelHelpers'
import type { MergeSection, MergeConflict } from '@/features/onboarding/merge/types'

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

function makeSection(overrides: Partial<MergeSection> = {}): MergeSection {
  return {
    id: 'sec-001',
    title: 'Code Quality',
    sourceFiles: [
      {
        toolId: 'claude-code' as const,
        path: '/home/.claude/CLAUDE.md',
        content: '# Rules',
        kind: 'instructions' as const,
      },
    ],
    proposedContent: '# Code Quality\n\nBe strict.',
    status: 'pending',
    ...overrides,
  }
}

function makeConflict(sectionA: MergeSection, sectionB: MergeSection): MergeConflict {
  return { sectionA, sectionB, reason: 'Overlapping rules' }
}

// ---------------------------------------------------------------------------
// nextSectionStatus
// ---------------------------------------------------------------------------

describe('nextSectionStatus', () => {
  it('approve: pending → approved', () => {
    expect(nextSectionStatus('pending', 'approve')).toBe('approved')
  })

  it('approve: approved → pending (toggle)', () => {
    expect(nextSectionStatus('approved', 'approve')).toBe('pending')
  })

  it('approve: rejected → approved', () => {
    expect(nextSectionStatus('rejected', 'approve')).toBe('approved')
  })

  it('reject: pending → rejected', () => {
    expect(nextSectionStatus('pending', 'reject')).toBe('rejected')
  })

  it('reject: rejected → pending (toggle)', () => {
    expect(nextSectionStatus('rejected', 'reject')).toBe('pending')
  })

  it('reject: approved → rejected', () => {
    expect(nextSectionStatus('approved', 'reject')).toBe('rejected')
  })

  it('edit: any status → edited', () => {
    expect(nextSectionStatus('pending', 'edit')).toBe('edited')
    expect(nextSectionStatus('approved', 'edit')).toBe('edited')
    expect(nextSectionStatus('rejected', 'edit')).toBe('edited')
    expect(nextSectionStatus('edited', 'edit')).toBe('edited')
  })
})

// ---------------------------------------------------------------------------
// findConflictForSection
// ---------------------------------------------------------------------------

describe('findConflictForSection', () => {
  it('returns null when no conflicts', () => {
    const section = makeSection()
    expect(findConflictForSection(section.id, [])).toBeNull()
  })

  it('finds conflict where section is sectionA', () => {
    const a = makeSection({ id: 'a' })
    const b = makeSection({ id: 'b' })
    const conflict = makeConflict(a, b)
    expect(findConflictForSection('a', [conflict])).toBe(conflict)
  })

  it('finds conflict where section is sectionB', () => {
    const a = makeSection({ id: 'a' })
    const b = makeSection({ id: 'b' })
    const conflict = makeConflict(a, b)
    expect(findConflictForSection('b', [conflict])).toBe(conflict)
  })

  it('returns null for unrelated section', () => {
    const a = makeSection({ id: 'a' })
    const b = makeSection({ id: 'b' })
    const conflict = makeConflict(a, b)
    expect(findConflictForSection('c', [conflict])).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// resolveHighlightSourceId
// ---------------------------------------------------------------------------

describe('resolveHighlightSourceId', () => {
  it('returns null when no path selected', () => {
    const section = makeSection()
    expect(resolveHighlightSourceId(section, null)).toBeNull()
  })

  it('returns path when source file exists in section', () => {
    const section = makeSection()
    const path = '/home/.claude/CLAUDE.md'
    expect(resolveHighlightSourceId(section, path)).toBe(path)
  })

  it('returns null when path does not match any source file', () => {
    const section = makeSection()
    expect(resolveHighlightSourceId(section, '/nonexistent.md')).toBeNull()
  })
})

// ---------------------------------------------------------------------------
// sectionBorderClass
// ---------------------------------------------------------------------------

describe('sectionBorderClass', () => {
  it('returns green for approved', () => {
    expect(sectionBorderClass('approved')).toContain('green')
  })

  it('returns red for rejected', () => {
    expect(sectionBorderClass('rejected')).toContain('red')
  })

  it('returns yellow for edited', () => {
    expect(sectionBorderClass('edited')).toContain('yellow')
  })

  it('returns border-border for pending', () => {
    expect(sectionBorderClass('pending')).toBe('border-border')
  })
})

// ---------------------------------------------------------------------------
// sectionStatusBadgeClass
// ---------------------------------------------------------------------------

describe('sectionStatusBadgeClass', () => {
  it('contains green for approved', () => {
    expect(sectionStatusBadgeClass('approved')).toContain('green')
  })

  it('contains red for rejected', () => {
    expect(sectionStatusBadgeClass('rejected')).toContain('red')
  })

  it('contains yellow for edited', () => {
    expect(sectionStatusBadgeClass('edited')).toContain('yellow')
  })

  it('uses muted for pending', () => {
    expect(sectionStatusBadgeClass('pending')).toContain('muted')
  })
})
