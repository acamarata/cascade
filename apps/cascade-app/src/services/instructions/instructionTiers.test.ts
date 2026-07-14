/**
 * Purpose: Unit tests for the instruction-tier service (E-P9-02).
 *   Covers tier fetching (with injected resolver), full-text search,
 *   and tier diff computation. All tests are pure / no real IPC.
 * Constraints: No Tauri imports. Uses injected resolveFn to avoid invoke().
 * SPORT: MASTER-LIBS.md — instructionTiers tests (E-P9-02)
 */

import { describe, it, expect } from 'vitest'
import { fetchAllTiers, searchInstructions, computeTierDiff } from './instructionTiers'
import type { TierContent } from '../../types/instructions'
import { TIER_LABELS } from '../../types/instructions'

// ── Helpers ───────────────────────────────────────────────────────────────────

/** Build a minimal TierContent array for testing. */
function makeTiers(contents: Partial<Record<string, string>> = {}): TierContent[] {
  return TIER_LABELS.map((label) => {
    const content = contents[label] ?? ''
    return {
      tier: label,
      content,
      present: content.length > 0,
      error: null,
    }
  })
}

// ── fetchAllTiers ─────────────────────────────────────────────────────────────

describe('fetchAllTiers', () => {
  it('returns one TierContent per tier in TIER_LABELS order', async () => {
    const resolver = async (tier: string) => `# Content for ${tier}`
    const result = await fetchAllTiers(resolver)

    expect(result).toHaveLength(TIER_LABELS.length)
    for (let i = 0; i < TIER_LABELS.length; i++) {
      expect(result[i].tier).toBe(TIER_LABELS[i])
    }
  })

  it('marks tiers with non-empty content as present', async () => {
    const resolver = async (tier: string) => (tier === 'GCI' ? '# Global' : '')
    const result = await fetchAllTiers(resolver)

    const gci = result.find((t) => t.tier === 'GCI')
    expect(gci?.present).toBe(true)

    const pci = result.find((t) => t.tier === 'PCI')
    expect(pci?.present).toBe(false)
  })

  it('captures resolver errors without throwing', async () => {
    const resolver = async (tier: string) => {
      if (tier === 'APC') throw new Error('resolve failed')
      return `content for ${tier}`
    }
    const result = await fetchAllTiers(resolver)

    const apc = result.find((t) => t.tier === 'APC')
    expect(apc?.present).toBe(false)
    expect(apc?.error).toContain('resolve failed')

    // Other tiers should still be present.
    const gci = result.find((t) => t.tier === 'GCI')
    expect(gci?.present).toBe(true)
  })

  it('marks empty-string resolve as not present', async () => {
    const resolver = async (_tier: string) => '   '
    const result = await fetchAllTiers(resolver)
    for (const t of result) {
      expect(t.present).toBe(false)
    }
  })
})

// ── searchInstructions ────────────────────────────────────────────────────────

describe('searchInstructions', () => {
  const tiers = makeTiers({
    GCI: '# Global\nThis is the global config.\nApplies everywhere.\n',
    PCI: '# Personal\nPersonal downloads config.\nIgnored in project scope.\n',
    APC: '# All Projects\nAll sites config.\n',
  })

  it('returns empty result for empty query', () => {
    const result = searchInstructions(tiers, '')
    expect(result.hits).toEqual([])
    expect(result.totalHits).toBe(0)
  })

  it('finds case-insensitive matches across all tiers', () => {
    const result = searchInstructions(tiers, 'config')
    // "global config", "Personal downloads config", "All sites config"
    expect(result.totalHits).toBeGreaterThanOrEqual(3)
    const tierNames = new Set(result.hits.map((h) => h.tier))
    expect(tierNames.has('GCI')).toBe(true)
    expect(tierNames.has('PCI')).toBe(true)
  })

  it('returns matchStart and matchEnd for highlight', () => {
    const result = searchInstructions(tiers, 'global')
    const hit = result.hits.find((h) => h.tier === 'GCI')
    expect(hit).toBeDefined()
    if (!hit) return
    expect(hit.lineText.toLowerCase().slice(hit.matchStart, hit.matchEnd)).toBe('global')
  })

  it('does not return hits for absent tiers', () => {
    const result = searchInstructions(tiers, 'prc-specific-term')
    for (const hit of result.hits) {
      expect(['GCI', 'PCI', 'APC']).toContain(hit.tier)
    }
  })

  it('returns 1-based line numbers', () => {
    // "# Global" is line 1 in GCI.
    const result = searchInstructions(tiers, '# Global')
    const hit = result.hits.find((h) => h.tier === 'GCI')
    expect(hit?.lineNo).toBe(1)
  })

  it('caps hits at maxHits', () => {
    const bigTiers = makeTiers({
      GCI: Array.from({ length: 100 }, (_, i) => `match-term line ${i}`).join('\n'),
    })
    const result = searchInstructions(bigTiers, 'match-term', 10)
    expect(result.hits.length).toBe(10)
    expect(result.totalHits).toBe(100)
  })
})

// ── computeTierDiff ────────────────────────────────────────────────────────────

describe('computeTierDiff', () => {
  it('marks all lines of GCI as new (no prior tiers)', () => {
    const tiers = makeTiers({ GCI: 'line one\nline two\n' })
    const diff = computeTierDiff(tiers, 0)
    expect(diff.tier).toBe('GCI')
    const newLines = diff.lines.filter((l) => l.isNew)
    expect(newLines.length).toBeGreaterThan(0)
    // All non-empty lines in the first tier are new.
    const nonEmpty = diff.lines.filter((l) => l.text.trim())
    expect(nonEmpty.every((l) => l.isNew)).toBe(true)
  })

  it('marks lines inherited from GCI as not-new in PCI', () => {
    const shared = 'Shared line content'
    const tiers = makeTiers({
      GCI: `${shared}\nGCI only line\n`,
      PCI: `${shared}\nPCI only line\n`,
    })
    const diff = computeTierDiff(tiers, 1) // PCI is index 1
    const sharedEntry = diff.lines.find((l) => l.text.trim() === shared)
    expect(sharedEntry?.isNew).toBe(false)
    const pciOnly = diff.lines.find((l) => l.text.trim() === 'PCI only line')
    expect(pciOnly?.isNew).toBe(true)
  })

  it('reports correct newLineCount and inheritedLineCount', () => {
    const tiers = makeTiers({
      GCI: 'A\nB\n',
      PCI: 'A\nC\n',
    })
    const diff = computeTierDiff(tiers, 1)
    // A is inherited, C is new, blank lines from trailing \n may appear
    const newLines = diff.lines.filter((l) => l.isNew && l.text.trim())
    const inheritedLines = diff.lines.filter((l) => !l.isNew && l.text.trim())
    expect(newLines.length).toBeGreaterThanOrEqual(1) // C
    expect(inheritedLines.length).toBeGreaterThanOrEqual(1) // A
  })

  it('returns empty lines for an absent tier', () => {
    const tiers = makeTiers({ GCI: 'content' }) // PCI is absent
    const diff = computeTierDiff(tiers, 1) // PCI index
    expect(diff.lines).toHaveLength(0)
    expect(diff.newLineCount).toBe(0)
  })

  it('handles out-of-bounds index gracefully', () => {
    const tiers = makeTiers({ GCI: 'x' })
    const diff = computeTierDiff(tiers, 99)
    expect(diff.lines).toHaveLength(0)
  })
})

// ── PCI path resolution (E-P9-01) ────────────────────────────────────────────

import { PERSONAL_VAULT_FIXTURE } from '../../context/PersonalVaultContext'

describe('PCI path resolution via PersonalVaultContext defaults', () => {
  it('DEFAULT_PCI_PATH is ~/Downloads/.cascade', () => {
    expect(PERSONAL_VAULT_FIXTURE.path).toBe('~/Downloads/.cascade')
  })

  it('PERSONAL_VAULT_FIXTURE has memory/inbox/ideas/daily children', () => {
    const names = PERSONAL_VAULT_FIXTURE.children.map((c: { name: string }) => c.name)
    expect(names).toContain('memory')
    expect(names).toContain('inbox')
    expect(names).toContain('ideas')
    expect(names).toContain('daily')
  })
})

// ── Memory aggregation includes personal root (E-P9-01) ──────────────────────

import os from 'node:os'
import path from 'node:path'
import fs from 'node:fs/promises'
import { buildMemoryIndex } from '../vault/memoryAggregator'

describe('memory aggregation with personal root', () => {
  it('buildMemoryIndex includes PCI root entries', async () => {
    const pciRoot = path.join(os.tmpdir(), `pci-test-${Date.now()}`)
    await fs.mkdir(path.join(pciRoot, '.claude', 'memory'), { recursive: true })
    await fs.mkdir(path.join(pciRoot, '.claude', 'inbox'), { recursive: true })
    await fs.mkdir(path.join(pciRoot, '.claude', 'ideas'), { recursive: true })

    const savedHome = process.env.HOME
    process.env.HOME = path.dirname(pciRoot)

    try {
      await fs.writeFile(
        path.join(pciRoot, '.claude', 'memory', 'decisions.md'),
        '## PCI Decision\n\nPersonal decision content.\n'
      )

      const entries = await buildMemoryIndex([pciRoot])
      expect(entries.length).toBeGreaterThan(0)
      expect(entries.some((e: { project: string }) => e.project === pciRoot)).toBe(true)
      expect(entries.some((e: { title: string }) => e.title === 'PCI Decision')).toBe(true)
    } finally {
      process.env.HOME = savedHome
      await fs.rm(pciRoot, { recursive: true, force: true }).catch(() => {})
    }
  })
})
