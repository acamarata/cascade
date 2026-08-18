/**
 * Purpose: Unit tests for the encrypted personal vault UI (T-P7-E21-01).
 *   Covers PersonalEncryptedVaultPanel rendering states, collection selection,
 *   mode toggle, add-record form validation, and exposure log display.
 *   Hook is tested through the component with mocked usePersonalEncryptedVault.
 * Constraints: No real Tauri IPC; hook is mocked to exercise all UI branches.
 * SPORT: MASTER-COMPONENTS.md — PersonalEncryptedVaultPanel tests (T-P7-E21-01)
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import { PersonalEncryptedVaultPanel } from './PersonalEncryptedVaultPanel'
import type { CollectionInfo, VaultRecord, ExposureEntry, CascadeMode } from './usePersonalEncryptedVault'

// ── Mock hook ─────────────────────────────────────────────────────────────────

const mockSetMode = vi.fn()
const mockSelectCollection = vi.fn()
const mockAddRecord = vi.fn()
const mockRefreshCollections = vi.fn()

const BASE_HOOK_STATE = {
  collections: [] as CollectionInfo[],
  records: [] as VaultRecord[],
  exposureLog: [] as ExposureEntry[],
  selectedCollection: null as string | null,
  mode: 'normal' as CascadeMode,
  loading: false,
  error: null as string | null,
  setMode: mockSetMode,
  selectCollection: mockSelectCollection,
  addRecord: mockAddRecord,
  refreshCollections: mockRefreshCollections,
}

vi.mock('./usePersonalEncryptedVault', () => ({
  usePersonalEncryptedVault: vi.fn(() => BASE_HOOK_STATE),
}))

import { usePersonalEncryptedVault } from './usePersonalEncryptedVault'
import type { MockedFunction } from 'vitest'

const mockHook = usePersonalEncryptedVault as MockedFunction<typeof usePersonalEncryptedVault>

// ── Fixtures ──────────────────────────────────────────────────────────────────

const COLLECTIONS: CollectionInfo[] = [
  { name: 'notes', label: 'Personal Notes', sensitivity: 'normal' },
  { name: 'secrets', label: 'Secrets', sensitivity: 'high' },
]

const RECORDS: VaultRecord[] = [
  {
    id: 'rec-abc-123',
    collection: 'notes',
    payload: { content: 'hello world' },
    created_at: '2026-01-01',
    updated_at: '2026-01-02',
  },
]

const EXPOSURE_LOG: ExposureEntry[] = [
  {
    id: 1,
    collection: 'notes',
    item_id: 'rec-abc-123',
    exposed_to: 'anthropic/claude',
    granted_by: 'user',
    exposed_at: '2026-01-03T10:00:00Z',
  },
]

function hookWith(overrides: Partial<typeof BASE_HOOK_STATE>) {
  mockHook.mockReturnValue({ ...BASE_HOOK_STATE, ...overrides })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

beforeEach(() => {
  mockHook.mockReturnValue(BASE_HOOK_STATE)
  mockSetMode.mockReset()
  mockSelectCollection.mockReset()
  mockAddRecord.mockReset()
  mockRefreshCollections.mockReset()
})

describe('PersonalEncryptedVaultPanel — loading state', () => {
  it('shows loading indicator when loading=true and no collections', () => {
    hookWith({ loading: true })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('Loading…')).toBeInTheDocument()
  })
})

describe('PersonalEncryptedVaultPanel — error state', () => {
  it('displays error banner when error is set', () => {
    hookWith({ error: 'Vault unavailable: keychain locked' })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText(/Vault unavailable: keychain locked/)).toBeInTheDocument()
  })
})

describe('PersonalEncryptedVaultPanel — empty collections', () => {
  it('shows "No collections found" when collections is empty and not loading', () => {
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('No collections found.')).toBeInTheDocument()
  })
})

describe('PersonalEncryptedVaultPanel — collection list', () => {
  it('renders a button for each collection', () => {
    hookWith({ collections: COLLECTIONS })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('Personal Notes')).toBeInTheDocument()
    expect(screen.getByText('Secrets')).toBeInTheDocument()
  })

  it('shows sensitivity badge for each collection', () => {
    hookWith({ collections: COLLECTIONS })
    render(<PersonalEncryptedVaultPanel />)
    // 'normal' appears in both the mode toggle and the sensitivity badge (2 elements).
    // 'high' is unique to the sensitivity badge.
    expect(screen.getAllByText('normal').length).toBeGreaterThanOrEqual(2)
    expect(screen.getByText('high')).toBeInTheDocument()
  })

  it('calls selectCollection with the collection name when clicked', () => {
    hookWith({ collections: COLLECTIONS })
    render(<PersonalEncryptedVaultPanel />)
    fireEvent.click(screen.getByText('Personal Notes'))
    expect(mockSelectCollection).toHaveBeenCalledWith('notes')
  })

  it('highlights the selected collection', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: RECORDS })
    render(<PersonalEncryptedVaultPanel />)
    // The active collection button has bg-accent class — check aria via selected text.
    expect(screen.getByText('Personal Notes').closest('button')).toHaveClass('bg-accent')
  })
})

describe('PersonalEncryptedVaultPanel — mode toggle', () => {
  it('renders normal and personal mode buttons', () => {
    hookWith({ collections: COLLECTIONS })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByRole('button', { name: 'normal' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'personal' })).toBeInTheDocument()
  })

  it('calls setMode("personal") when personal button is clicked', () => {
    hookWith({ collections: COLLECTIONS })
    render(<PersonalEncryptedVaultPanel />)
    fireEvent.click(screen.getByRole('button', { name: 'personal' }))
    expect(mockSetMode).toHaveBeenCalledWith('personal')
  })

  it('calls setMode("normal") when normal button is clicked', () => {
    hookWith({ collections: COLLECTIONS, mode: 'personal' })
    render(<PersonalEncryptedVaultPanel />)
    fireEvent.click(screen.getByRole('button', { name: 'normal' }))
    expect(mockSetMode).toHaveBeenCalledWith('normal')
  })

  it('marks the active mode button as primary', () => {
    hookWith({ collections: COLLECTIONS, mode: 'personal' })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByRole('button', { name: 'personal' })).toHaveClass('bg-primary')
    expect(screen.getByRole('button', { name: 'normal' })).not.toHaveClass('bg-primary')
  })
})

describe('PersonalEncryptedVaultPanel — no collection selected', () => {
  it('shows "Select a collection" prompt when nothing is selected', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: null })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('Select a collection to view records.')).toBeInTheDocument()
  })
})

describe('PersonalEncryptedVaultPanel — records table', () => {
  it('renders records table when collection is selected and has records', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: RECORDS })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('Records — notes')).toBeInTheDocument()
    // Shows truncated ID (first 8 chars of rec-abc-123)
    expect(screen.getByText('rec-abc-')).toBeInTheDocument()
  })

  it('shows "No records" when collection is selected but empty', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('No records in this collection.')).toBeInTheDocument()
  })

  it('shows record payload as JSON', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: RECORDS })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText(/hello world/)).toBeInTheDocument()
  })
})

describe('PersonalEncryptedVaultPanel — exposure log', () => {
  it('renders exposure log section heading', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('Exposure Log')).toBeInTheDocument()
  })

  it('shows "No exposure events recorded" when log is empty', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [], exposureLog: [] })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('No exposure events recorded.')).toBeInTheDocument()
  })

  it('renders exposure log entries', () => {
    hookWith({
      collections: COLLECTIONS,
      selectedCollection: 'notes',
      records: [],
      exposureLog: EXPOSURE_LOG,
    })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByText('anthropic/claude')).toBeInTheDocument()
  })
})

describe('PersonalEncryptedVaultPanel — add-record form', () => {
  it('renders add-record form when a collection is selected', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByPlaceholderText('{"key": "value"}')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Submit' })).toBeInTheDocument()
  })

  it('submit button is disabled when textarea is empty', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    expect(screen.getByRole('button', { name: 'Submit' })).toBeDisabled()
  })

  it('shows JSON error for malformed input', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    const textarea = screen.getByPlaceholderText('{"key": "value"}')
    fireEvent.change(textarea, { target: { value: 'not json' } })
    fireEvent.submit(textarea.closest('form')!)
    expect(screen.getByText('Invalid JSON — check syntax and try again.')).toBeInTheDocument()
  })

  it('shows error for non-object JSON (array)', () => {
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    const textarea = screen.getByPlaceholderText('{"key": "value"}')
    fireEvent.change(textarea, { target: { value: '[1, 2, 3]' } })
    fireEvent.submit(textarea.closest('form')!)
    expect(screen.getByText('Payload must be a JSON object, e.g. {"key": "value"}')).toBeInTheDocument()
  })

  it('calls addRecord with parsed payload on valid JSON submit', async () => {
    mockAddRecord.mockResolvedValue('new-id-xyz')
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    const textarea = screen.getByPlaceholderText('{"key": "value"}')
    fireEvent.change(textarea, { target: { value: '{"key": "value"}' } })
    fireEvent.submit(textarea.closest('form')!)
    await waitFor(() => expect(mockAddRecord).toHaveBeenCalledWith('notes', { key: 'value' }))
  })

  it('clears textarea after successful addRecord', async () => {
    mockAddRecord.mockResolvedValue('new-id-xyz')
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    const textarea = screen.getByPlaceholderText('{"key": "value"}')
    fireEvent.change(textarea, { target: { value: '{"key": "value"}' } })
    fireEvent.submit(textarea.closest('form')!)
    await waitFor(() => expect((textarea as HTMLTextAreaElement).value).toBe(''))
  })

  it('shows save error when addRecord throws', async () => {
    mockAddRecord.mockRejectedValue(new Error('DB locked'))
    hookWith({ collections: COLLECTIONS, selectedCollection: 'notes', records: [] })
    render(<PersonalEncryptedVaultPanel />)
    const textarea = screen.getByPlaceholderText('{"key": "value"}')
    fireEvent.change(textarea, { target: { value: '{"key": "value"}' } })
    fireEvent.submit(textarea.closest('form')!)
    await waitFor(() => expect(screen.getByText(/Save failed: DB locked/)).toBeInTheDocument())
  })
})
