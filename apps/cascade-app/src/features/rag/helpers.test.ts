/**
 * Purpose: Unit tests for RAG type helpers and pure utility functions
 *   used in the RAG Explorer panel.
 * SPORT: MASTER-COMPONENTS.md — RAG Explorer helpers tests (T-P4-E01-27)
 */

import { describe, expect, it } from 'vitest'
import type { RagCitation, RagSource, RagIndexStats, RagIngestProgress } from './types'

// ── Type shape tests (compile-time + runtime) ─────────────────────────────────

describe('RagSource type', () => {
  it('accepts a fully populated source', () => {
    const src: RagSource = {
      path: '/home/user/docs/setup.md',
      chunk_count: 42,
      indexed_at: '2026-06-10T10:00:00Z',
      status: 'indexed',
    }
    expect(src.chunk_count).toBe(42)
    expect(src.status).toBe('indexed')
  })

  it('accepts null indexed_at (not yet indexed)', () => {
    const src: RagSource = {
      path: '/tmp/draft.md',
      chunk_count: 0,
      indexed_at: null,
      status: 'pending',
    }
    expect(src.indexed_at).toBeNull()
  })
})

describe('RagCitation type', () => {
  it('creates a citation with all required fields', () => {
    const c: RagCitation = {
      path: '/docs/api.md',
      heading_path: '## API > ### auth',
      snippet: 'Use Bearer token for auth.',
      rrf_score: 0.812,
    }
    expect(c.rrf_score).toBeGreaterThan(0)
    expect(c.snippet).toContain('Bearer')
  })

  it('allows empty heading_path', () => {
    const c: RagCitation = {
      path: '/docs/readme.md',
      heading_path: '',
      snippet: 'Hello world.',
      rrf_score: 0.5,
    }
    expect(c.heading_path).toBe('')
  })
})

describe('RagIndexStats type', () => {
  it('creates stats with last_updated null', () => {
    const stats: RagIndexStats = {
      total_files: 0,
      total_chunks: 0,
      index_size_bytes: 0,
      last_updated: null,
    }
    expect(stats.last_updated).toBeNull()
  })

  it('creates populated stats', () => {
    const stats: RagIndexStats = {
      total_files: 15,
      total_chunks: 320,
      index_size_bytes: 1024 * 512,
      last_updated: '2026-06-10T08:30:00Z',
    }
    expect(stats.total_files).toBe(15)
    expect(stats.index_size_bytes).toBe(524288)
  })
})

describe('RagIngestProgress type', () => {
  it('models an in-progress ingest', () => {
    const prog: RagIngestProgress = {
      file_name: 'large-doc.md',
      chunks_done: 30,
      chunks_total: 100,
      percent: 30,
      done: false,
    }
    expect(prog.done).toBe(false)
    expect(prog.percent).toBe(30)
  })

  it('models a completed ingest at 100%', () => {
    const prog: RagIngestProgress = {
      file_name: 'large-doc.md',
      chunks_done: 100,
      chunks_total: 100,
      percent: 100,
      done: true,
    }
    expect(prog.done).toBe(true)
    expect(prog.percent).toBe(100)
  })

  it('handles unknown total (chunks_total = 0)', () => {
    const prog: RagIngestProgress = {
      file_name: 'stream.md',
      chunks_done: 5,
      chunks_total: 0,
      percent: 50,
      done: false,
    }
    expect(prog.chunks_total).toBe(0)
  })
})

// ── fmtBytes utility (inline pure function tests) ─────────────────────────────

// Extract the logic for independent testing (mirrors IndexStats.tsx fmtBytes).
function fmtBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`
}

describe('fmtBytes', () => {
  it('formats bytes as B when < 1024', () => {
    expect(fmtBytes(512)).toBe('512 B')
  })

  it('formats bytes as KB when >= 1024 and < 1MB', () => {
    expect(fmtBytes(2048)).toBe('2.0 KB')
  })

  it('formats bytes as MB when >= 1MB', () => {
    expect(fmtBytes(1024 * 1024 * 1.5)).toBe('1.50 MB')
  })

  it('returns 0 B for zero', () => {
    expect(fmtBytes(0)).toBe('0 B')
  })
})
