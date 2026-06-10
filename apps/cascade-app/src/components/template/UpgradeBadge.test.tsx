/**
 * Purpose: Tests for UpgradeBadge component and upgrade/apply dialog flows.
 *   Tests: badge visibility logic, dialog state transitions, dry-run-before-apply ordering,
 *   upgrade IPC call guarding, UpgradeConfirmDialog rendering.
 * SPORT: MASTER-COMPONENTS.md — UpgradeBadge, UpgradeConfirmDialog, ApplyDialog tests
 */

import '@testing-library/jest-dom'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { describe, it, expect, vi, beforeEach } from 'vitest'
import type { MockedFunction } from 'vitest'

// Mock Tauri invoke BEFORE importing components that use it.
vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi.fn(),
}))

import { invoke } from '@tauri-apps/api/core'
import { UpgradeBadge } from './UpgradeBadge'
import { TemplateCard } from './TemplateCard'
import { UpgradeConfirmDialog } from './UpgradeConfirmDialog'
import { ApplyDialog, extractTemplateVars, defaultTargetPath } from './ApplyDialog'
import type { TemplateEntryIpc } from '../../types/templates'
import type { DryRunPreviewIpc } from './ApplyDialog'

const mockInvoke = invoke as MockedFunction<typeof invoke>

// ── Fixtures ──────────────────────────────────────────────────────────────────

function makeEntry(overrides: Partial<TemplateEntryIpc> = {}): TemplateEntryIpc {
  return {
    id: 'gci-base',
    version: '1.0.0',
    tier: 'gci',
    stacks: ['rust'],
    projectShapes: ['app'],
    description: 'Base GCI template.',
    body: '# GCI Base\n\nThis is the base template.',
    ...overrides,
  }
}

// ── UpgradeBadge ─────────────────────────────────────────────────────────────

describe('UpgradeBadge', () => {
  it('renders with the correct version label', () => {
    render(<UpgradeBadge version="2.0.0" />)
    expect(screen.getByText(/v2\.0\.0/)).toBeInTheDocument()
  })

  it('has an accessible aria-label', () => {
    render(<UpgradeBadge version="1.5.0" />)
    expect(screen.getByRole('status', { name: /upgrade available.*1\.5\.0/i })).toBeInTheDocument()
  })
})

// ── TemplateCard with UpgradeBadge ────────────────────────────────────────────

describe('TemplateCard upgrade badge visibility', () => {
  const entry = makeEntry()

  it('does NOT render UpgradeBadge when upgradeable is false', () => {
    render(<TemplateCard entry={entry} isSelected={false} onClick={() => {}} upgradeable={false} />)
    expect(screen.queryByRole('status', { name: /upgrade available/i })).not.toBeInTheDocument()
  })

  it('renders UpgradeBadge when upgradeable=true and upgradeToVersion is provided', () => {
    render(
      <TemplateCard
        entry={entry}
        isSelected={false}
        onClick={() => {}}
        upgradeable={true}
        upgradeToVersion="2.0.0"
      />
    )
    expect(screen.getByRole('status', { name: /upgrade available.*2\.0\.0/i })).toBeInTheDocument()
  })

  it('does NOT render UpgradeBadge when upgradeable=true but upgradeToVersion is absent', () => {
    render(
      <TemplateCard
        entry={entry}
        isSelected={false}
        onClick={() => {}}
        upgradeable={true}
        // upgradeToVersion deliberately omitted
      />
    )
    expect(screen.queryByRole('status', { name: /upgrade available/i })).not.toBeInTheDocument()
  })
})

// ── UpgradeConfirmDialog ───────────────────────────────────────────────────────

describe('UpgradeConfirmDialog', () => {
  const baseProps = {
    open: true,
    templateId: 'gci-base',
    oldVersion: '1.0.0',
    newVersion: '2.0.0',
    addedSections: ['New Section'],
    changedSections: ['Changed Section'],
    deprecatedSections: [],
    conflictSections: [],
    upgrading: false,
    onConfirm: vi.fn(),
    onCancel: vi.fn(),
  }

  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders the dialog with version info', () => {
    render(<UpgradeConfirmDialog {...baseProps} />)
    expect(screen.getByText(/upgrade template to v2\.0\.0/i)).toBeInTheDocument()
    expect(screen.getByText(/was applied at v1\.0\.0/i)).toBeInTheDocument()
  })

  it('shows added and changed sections in diff summary', () => {
    render(<UpgradeConfirmDialog {...baseProps} />)
    expect(screen.getByText('New Section')).toBeInTheDocument()
    expect(screen.getByText('Changed Section')).toBeInTheDocument()
  })

  it('does NOT show conflict warning when conflictSections is empty', () => {
    render(<UpgradeConfirmDialog {...baseProps} conflictSections={[]} />)
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows conflict warning when conflictSections is non-empty', () => {
    render(<UpgradeConfirmDialog {...baseProps} conflictSections={['My Custom Section']} />)
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(/my custom section/i)).toBeInTheDocument()
  })

  it('calls onConfirm with force=false when Upgrade is clicked (default)', () => {
    const onConfirm = vi.fn()
    render(<UpgradeConfirmDialog {...baseProps} onConfirm={onConfirm} />)
    fireEvent.click(screen.getByRole('button', { name: /confirm upgrade/i }))
    expect(onConfirm).toHaveBeenCalledOnce()
    expect(onConfirm).toHaveBeenCalledWith(false)
  })

  it('calls onConfirm with force=true when force prop is true', () => {
    const onConfirm = vi.fn()
    render(<UpgradeConfirmDialog {...baseProps} force={true} onConfirm={onConfirm} />)
    fireEvent.click(screen.getByRole('button', { name: /confirm upgrade/i }))
    expect(onConfirm).toHaveBeenCalledWith(true)
  })

  it('calls onCancel when Cancel is clicked', () => {
    const onCancel = vi.fn()
    render(<UpgradeConfirmDialog {...baseProps} onCancel={onCancel} />)
    fireEvent.click(screen.getByRole('button', { name: /cancel/i }))
    expect(onCancel).toHaveBeenCalledOnce()
  })

  it('disables buttons while upgrading', () => {
    render(<UpgradeConfirmDialog {...baseProps} upgrading={true} />)
    expect(screen.getByRole('button', { name: /cancel/i })).toBeDisabled()
    expect(screen.getByRole('button', { name: /confirm upgrade/i })).toBeDisabled()
  })
})

// ── ApplyDialog helpers ───────────────────────────────────────────────────────

describe('extractTemplateVars', () => {
  it('extracts unique sorted variable names from body', () => {
    const body = 'Hello {{projectName}}, you are {{role}} in {{projectName}}.'
    expect(extractTemplateVars(body)).toEqual(['projectName', 'role'])
  })

  it('returns empty array when no variables', () => {
    expect(extractTemplateVars('No vars here.')).toEqual([])
  })
})

describe('defaultTargetPath', () => {
  it('returns global path for gci tier', () => {
    expect(defaultTargetPath('gci')).toBe('~/.cascade/CASCADE.md')
  })

  it('returns project-relative path for prc tier', () => {
    expect(defaultTargetPath('prc')).toContain('{projectRoot}')
  })
})

// ── ApplyDialog state transitions ─────────────────────────────────────────────

describe('ApplyDialog', () => {
  const entry = makeEntry({ body: 'Hello {{projectName}}.' })

  const baseProps = {
    open: true,
    entry,
    onApply: vi.fn(),
    onClose: vi.fn(),
    dryRunResult: null as DryRunPreviewIpc | null,
    applyResult: null,
    applying: false,
    error: null,
  }

  beforeEach(() => {
    vi.clearAllMocks()
    mockInvoke.mockResolvedValue({})
  })

  it('renders target path input with sensible default for gci tier', () => {
    render(<ApplyDialog {...baseProps} />)
    const input = screen.getByLabelText(/target path/i) as HTMLInputElement
    expect(input.value).toBe('~/.cascade/CASCADE.md')
  })

  it('renders variable form fields derived from template body', () => {
    render(<ApplyDialog {...baseProps} />)
    expect(screen.getByLabelText(/projectname/i)).toBeInTheDocument()
  })

  it('Apply button is disabled before dry-run is executed', () => {
    render(<ApplyDialog {...baseProps} />)
    const applyBtn = screen.getByRole('button', { name: /apply template to target file/i })
    expect(applyBtn).toBeDisabled()
  })

  it('Apply button is enabled after dryRunResult is provided', () => {
    const dryRunResult: DryRunPreviewIpc = { toApply: ['New Section'], conflicts: [] }
    render(<ApplyDialog {...baseProps} dryRunResult={dryRunResult} />)
    const applyBtn = screen.getByRole('button', { name: /apply template to target file/i })
    expect(applyBtn).not.toBeDisabled()
  })

  it('dry-run preview button calls onApply with dryRun=true', () => {
    const onApply = vi.fn()
    render(<ApplyDialog {...baseProps} onApply={onApply} />)
    fireEvent.click(screen.getByRole('button', { name: /preview changes without applying/i }))
    expect(onApply).toHaveBeenCalledWith('~/.cascade/CASCADE.md', {}, true)
  })

  it('shows dry-run result with section counts when provided', () => {
    const dryRunResult: DryRunPreviewIpc = {
      toApply: ['Section A', 'Section B'],
      conflicts: ['Conflicting'],
    }
    render(<ApplyDialog {...baseProps} dryRunResult={dryRunResult} />)
    expect(screen.getByText('Section A')).toBeInTheDocument()
    expect(screen.getByText('Conflicting')).toBeInTheDocument()
  })

  it('shows conflict alert in dry-run result', () => {
    const dryRunResult: DryRunPreviewIpc = {
      toApply: [],
      conflicts: ['My Section'],
    }
    render(<ApplyDialog {...baseProps} dryRunResult={dryRunResult} />)
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(/not be overwritten/i)).toBeInTheDocument()
  })

  it('Apply does not call onApply unless dry-run result present (dry-run-before-apply ordering)', () => {
    const onApply = vi.fn()
    render(<ApplyDialog {...baseProps} onApply={onApply} dryRunResult={null} />)
    const applyBtn = screen.getByRole('button', { name: /apply template to target file/i })
    expect(applyBtn).toBeDisabled()
    // Clicking a disabled button should have no effect
    fireEvent.click(applyBtn)
    expect(onApply).not.toHaveBeenCalled()
  })

  it('shows error message when error prop is set', () => {
    render(<ApplyDialog {...baseProps} error="daemon unavailable" />)
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(screen.getByText(/daemon unavailable/i)).toBeInTheDocument()
  })

  it('shows success result and Close button after applyResult is provided', async () => {
    render(
      <ApplyDialog
        {...baseProps}
        applyResult={{ applied: ['A', 'B'], conflicts: [], skipped: [] }}
      />
    )
    await waitFor(() => {
      expect(screen.getByText(/template applied successfully/i)).toBeInTheDocument()
    })
    // Target by data-testid to avoid ambiguity with Radix dialog X button
    expect(screen.getByTestId('apply-dialog-close-cancel')).toBeInTheDocument()
  })

  it('calls onClose when Close button is clicked', () => {
    const onClose = vi.fn()
    render(
      <ApplyDialog
        {...baseProps}
        onClose={onClose}
        applyResult={{ applied: ['A'], conflicts: [], skipped: [] }}
      />
    )
    fireEvent.click(screen.getByTestId('apply-dialog-close-cancel'))
    expect(onClose).toHaveBeenCalledOnce()
  })
})
