/**
 * Purpose: Mock layer for @tauri-apps/api/core invoke used across wizard integration tests.
 *   Provides a per-command handler registry that returns canned responses.
 *   Mirrors the pattern in WizardContext.test.tsx (vi.mock) applied to all wizard commands.
 *
 * Inputs: None — import and call setupWizardMocks() in beforeEach.
 * Outputs: vi.mock setup + helper to override per-command responses.
 * Constraints:
 *   - No real file system writes; no real AI calls.
 *   - All commands listed in the wizard flow must have a handler registered.
 *   - resetMocks() restores defaults between tests (prevents state bleed).
 *
 * SPORT: MASTER-COMPONENTS.md — wizard E2E test suite — e2e/mocks/tauriMocks.ts
 * Task: T-P3-E03-34
 */

import { vi } from 'vitest'
import type { WizardCheckpoint } from '@/features/onboarding/types'
import type { LegacyToolHome } from '@/lib/scanner/types'
import type { MergeResult, MergeSourceFile } from '@/features/onboarding/merge/types'
import type { ToolArchive } from '@/lib/archive/types'

// ---------------------------------------------------------------------------
// In-memory checkpoint store for resume tests
// ---------------------------------------------------------------------------

let _savedCheckpoint: WizardCheckpoint | null = null

/** Reset the in-memory checkpoint store (call in beforeEach). */
export function clearSavedCheckpoint(): void {
  _savedCheckpoint = null
}

/** Read the checkpoint stored by mock wizard_save_checkpoint. */
export function getSavedCheckpoint(): WizardCheckpoint | null {
  return _savedCheckpoint
}

// ---------------------------------------------------------------------------
// Default canned responses per command
// ---------------------------------------------------------------------------

export const MOCK_SCAN_HOMES: LegacyToolHome[] = [
  {
    toolId: 'claude-code',
    globalPaths: ['/tmp/test-home/.claude'],
    perProjectPaths: ['/tmp/test-home/Sites/project/.claude'],
    totalFiles: 4,
    totalSizeBytes: 8192,
  },
  {
    toolId: 'codex',
    globalPaths: ['/tmp/test-home/.codex'],
    perProjectPaths: [],
    totalFiles: 2,
    totalSizeBytes: 1024,
  },
]

export const MOCK_MERGE_RESULT: MergeResult = {
  tier: 4 /* WizardStep.MergeContent — numeric const enum */,
  sections: [
    {
      id: 'section-a',
      title: 'Code Quality Rules',
      sourceFiles: [],
      proposedContent: '# Code Quality\nAlways use strict mode.',
      status: 'pending',
    },
    {
      id: 'section-b',
      title: 'Git Conventions',
      sourceFiles: [],
      proposedContent: '# Git\nConventional commits required.',
      status: 'pending',
    },
  ],
  generatedAt: new Date().toISOString(),
  modelUsed: 'test-model',
  promptHash: 'abc123',
}

export const MOCK_MERGE_SOURCE_FILES: MergeSourceFile[] = [
  {
    toolId: 'claude-code',
    path: '/tmp/test-home/.claude/CLAUDE.md',
    content: '# My rules\n- Always use strict mode.',
    kind: 'instructions',
  },
]

export const MOCK_TOOL_ARCHIVE: ToolArchive = {
  toolId: 'claude-code',
  originalRoot: '/tmp/test-home/.claude',
  archiveRoot: '/tmp/test-home/.cascade/legacy/claude-code',
  archivedAt: new Date().toISOString(),
  fileCount: 4,
  sizeBytes: 8192,
}

// ---------------------------------------------------------------------------
// Command handler registry
// ---------------------------------------------------------------------------

type CommandHandler = (args: Record<string, unknown>) => unknown

const defaultHandlers: Record<string, CommandHandler> = {
  // Startup / checkpoint
  check_wizard_status: () => ({ NeverRun: true }),
  wizard_save_checkpoint: (args) => {
    _savedCheckpoint = args['checkpoint'] as WizardCheckpoint
  },
  wizard_load_checkpoint: () => _savedCheckpoint,
  wizard_clear_checkpoint: () => {
    _savedCheckpoint = null
  },
  wizard_mark_complete: () => undefined,

  // Provider detection
  detect_gemini_pool: () => true,
  provider_connect: () => 'mock-token-abc',
  download_local_model: () => undefined,
  // T-42 AI-optional gating: report one connected provider so AI-gated steps
  // (merge phase) render their AI flow in the default e2e walk-through.
  cascade_providers_health: () => ['gemini-pool'],

  // Scanner
  scan_global_homes: () => MOCK_SCAN_HOMES,
  scan_dev_tree: () => MOCK_SCAN_HOMES,

  // Merge
  read_legacy_content: () => MOCK_MERGE_SOURCE_FILES,
  run_ai_merge: () => MOCK_MERGE_RESULT,
  detect_merge_conflicts: () => [],
  write_cascade_content: () => undefined,

  // Verify
  cascade_resolve: () => ({
    content: '# Global Cascade Instructions\n\nAlways use strict mode.',
    format: 'markdown',
    tier: 'global',
  }),

  // Archive
  archive_legacy_tools: () => MOCK_TOOL_ARCHIVE,
  archive_preflight: () => ({
    toolId: 'claude-code',
    originalRoot: '/tmp/test-home/.claude',
    archiveRoot: '/tmp/test-home/.cascade/legacy/claude-code',
    fileCount: 4,
    sizeBytes: 8192,
    hasConflict: false,
    alreadyArchived: false,
  }),
  list_archived_tools: () => [],
  read_archive_manifest: () => null,

  // Symlinks
  setup_symlinks: () => [
    { source: '/tmp/test-home/.claude', target: '/tmp/test-home/.cascade/links/claude-code', kind: 'replace', status: 'created' },
  ],

  // Daemon
  install_daemon: () => undefined,
}

let activeHandlers: Record<string, CommandHandler> = { ...defaultHandlers }

/**
 * Override a single command's response for one test.
 * Call before the action that triggers the command.
 */
export function overrideCommand(command: string, handler: CommandHandler): void {
  activeHandlers[command] = handler
}

/**
 * Reset all overrides to defaults. Call in beforeEach.
 */
export function resetMocks(): void {
  activeHandlers = { ...defaultHandlers }
  _savedCheckpoint = null
}

/**
 * The vi.mock factory for '@tauri-apps/api/core'.
 * Call setupTauriMock() once at the top of a test file (before any imports that use invoke).
 *
 * Usage:
 *   vi.mock('@tauri-apps/api/core', () => buildTauriCoreMock())
 *   vi.mock('@tauri-apps/plugin-dialog', () => buildTauriDialogMock())
 *
 * Note: vi.mock is hoisted, so call these at file scope in the test file.
 */
export function buildTauriCoreMock() {
  return {
    invoke: vi.fn((command: string, args: Record<string, unknown> = {}) => {
      const handler = activeHandlers[command]
      if (!handler) {
        console.warn(`[tauriMocks] Unhandled command: ${command}`)
        return Promise.resolve(undefined)
      }
      try {
        return Promise.resolve(handler(args))
      } catch (err) {
        return Promise.reject(err)
      }
    }),
  }
}

export function buildTauriDialogMock() {
  return {
    open: vi.fn(() => Promise.resolve('/tmp/test-sites')),
  }
}
