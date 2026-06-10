// @vitest-environment jsdom
/**
 * Purpose: Unit tests for LibraryItemForm.
 *   Covers: generateSlug, validateForm (required fields, slug pattern, kind-specific),
 *   kind-field switching (kind-specific fields appear/disappear correctly).
 * Inputs:  None — pure unit tests; no IPC calls in logic tests.
 * Constraints: No real Tauri context required. Vitest jsdom environment.
 * SPORT: MASTER-COMPONENTS.md — LibraryItemForm tests — T-P3-E07-06
 */

import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import '@testing-library/jest-dom'

// ---------------------------------------------------------------------------
// Mocks (must precede imports that touch @tauri-apps)
// ---------------------------------------------------------------------------

vi.mock('@tauri-apps/api/core', () => ({
  invoke: vi
    .fn()
    .mockResolvedValue({ id: 'test-slug', path: '/test/.cascade/library/test-slug.yaml' }),
}))

// ---------------------------------------------------------------------------
// Imports
// ---------------------------------------------------------------------------

import { generateSlug, validateForm } from './LibraryItemForm'
import { LibraryItemForm } from './LibraryItemForm'
import type { LibraryItem } from '../types'

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

function buildItem(overrides?: Partial<LibraryItem>): LibraryItem {
  return {
    id: 'test-slug',
    title: 'Test item',
    description: '',
    kind: 'prompt',
    content: 'Some content',
    tags: [],
    harness_targets: [],
    ...overrides,
  }
}

// ---------------------------------------------------------------------------
// generateSlug
// ---------------------------------------------------------------------------

describe('generateSlug', () => {
  it('lowercases the title', () => {
    expect(generateSlug('Hello World')).toBe('hello-world')
  })

  it('replaces non-alphanumeric with hyphens', () => {
    expect(generateSlug('Hello, World!')).toBe('hello-world')
  })

  it('collapses consecutive non-alphanumeric sequences into a single hyphen', () => {
    expect(generateSlug('A  --  B')).toBe('a-b')
  })

  it('strips leading and trailing hyphens', () => {
    expect(generateSlug('  hello  ')).toBe('hello')
    expect(generateSlug('---hello---')).toBe('hello')
  })

  it('returns empty string for empty input', () => {
    expect(generateSlug('')).toBe('')
  })

  it('handles digits in the title', () => {
    expect(generateSlug('Version 2.0 Release')).toBe('version-2-0-release')
  })
})

// ---------------------------------------------------------------------------
// validateForm — required fields
// ---------------------------------------------------------------------------

describe('validateForm — required fields', () => {
  it('returns title error when title is empty', () => {
    const errors = validateForm(buildItem({ title: '', id: 'slug', content: 'body' }))
    expect(errors.title).toBeTruthy()
    expect(errors.slug).toBeUndefined()
    expect(errors.content).toBeUndefined()
  })

  it('returns content error when content is empty', () => {
    const errors = validateForm(buildItem({ title: 'T', id: 'slug', content: '' }))
    expect(errors.content).toBeTruthy()
    expect(errors.title).toBeUndefined()
  })

  it('returns slug error when id is empty', () => {
    const errors = validateForm(buildItem({ title: 'T', id: '', content: 'body' }))
    expect(errors.slug).toBeTruthy()
  })

  it('returns no errors for a fully valid prompt item', () => {
    const errors = validateForm(buildItem({ title: 'T', id: 'my-slug', content: 'body' }))
    expect(Object.keys(errors)).toHaveLength(0)
  })
})

// ---------------------------------------------------------------------------
// validateForm — slug pattern
// ---------------------------------------------------------------------------

describe('validateForm — slug pattern', () => {
  it('rejects slugs with uppercase letters', () => {
    const errors = validateForm(buildItem({ title: 'T', id: 'MySlug', content: 'body' }))
    expect(errors.slug).toBeTruthy()
  })

  it('rejects slugs with spaces', () => {
    const errors = validateForm(buildItem({ title: 'T', id: 'my slug', content: 'body' }))
    expect(errors.slug).toBeTruthy()
  })

  it('rejects slugs with special characters', () => {
    const errors = validateForm(buildItem({ title: 'T', id: 'my_slug!', content: 'body' }))
    expect(errors.slug).toBeTruthy()
  })

  it('accepts valid slugs with hyphens and digits', () => {
    const errors = validateForm(buildItem({ title: 'T', id: 'my-slug-123', content: 'body' }))
    expect(errors.slug).toBeUndefined()
  })

  it('accepts single-word lowercase slugs', () => {
    const errors = validateForm(buildItem({ title: 'T', id: 'hello', content: 'body' }))
    expect(errors.slug).toBeUndefined()
  })
})

// ---------------------------------------------------------------------------
// validateForm — kind-specific: slash-command trigger required
// ---------------------------------------------------------------------------

describe('validateForm — slash-command kind', () => {
  it('requires trigger when kind is slash-command', () => {
    const errors = validateForm(
      buildItem({ title: 'T', id: 'slug', content: 'body', kind: 'slash-command', trigger: '' })
    )
    expect(errors.trigger).toBeTruthy()
  })

  it('accepts slash-command with a trigger', () => {
    const errors = validateForm(
      buildItem({
        title: 'T',
        id: 'slug',
        content: 'body',
        kind: 'slash-command',
        trigger: '/summarize',
      })
    )
    expect(errors.trigger).toBeUndefined()
  })

  it('does not require trigger for non-slash-command kinds', () => {
    for (const kind of ['prompt', 'agent', 'persona'] as const) {
      const errors = validateForm(
        buildItem({ title: 'T', id: 'slug', content: 'body', kind, trigger: undefined })
      )
      expect(errors.trigger).toBeUndefined()
    }
  })
})

// ---------------------------------------------------------------------------
// LibraryItemForm — kind-specific field rendering
// ---------------------------------------------------------------------------

describe('LibraryItemForm — kind-specific fields', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  function renderForm(kind: LibraryItem['kind']) {
    return render(<LibraryItemForm open={true} onOpenChange={vi.fn()} initialItem={{ kind }} />)
  }

  it('does NOT render system_prompt textarea for prompt kind', () => {
    renderForm('prompt')
    expect(screen.queryByLabelText('System prompt')).not.toBeInTheDocument()
  })

  it('renders system_prompt textarea for persona kind', () => {
    renderForm('persona')
    expect(screen.getByLabelText('System prompt')).toBeInTheDocument()
  })

  it('does NOT render trigger input for prompt kind', () => {
    renderForm('prompt')
    expect(screen.queryByLabelText(/trigger/i)).not.toBeInTheDocument()
  })

  it('renders trigger input for slash-command kind', () => {
    renderForm('slash-command')
    expect(screen.getByLabelText(/trigger/i)).toBeInTheDocument()
  })

  it('renders args_description input for slash-command kind', () => {
    renderForm('slash-command')
    expect(screen.getByLabelText(/arguments description/i)).toBeInTheDocument()
  })

  it('does NOT render trigger or args_description for agent kind', () => {
    renderForm('agent')
    expect(screen.queryByLabelText(/trigger/i)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/arguments description/i)).not.toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// LibraryItemForm — slug auto-generation
// ---------------------------------------------------------------------------

describe('LibraryItemForm — slug auto-generation', () => {
  it('auto-generates slug from title when slug has not been manually edited', async () => {
    const user = userEvent.setup()
    render(<LibraryItemForm open={true} onOpenChange={vi.fn()} initialItem={{ kind: 'prompt' }} />)
    const titleInput = screen.getByLabelText(/title/i) as HTMLInputElement
    await user.type(titleInput, 'My Test Prompt')
    const slugInput = screen.getByLabelText(/id \/ slug/i) as HTMLInputElement
    expect(slugInput.value).toBe('my-test-prompt')
  })

  it('stops auto-generating slug after manual slug edit', async () => {
    const user = userEvent.setup()
    render(<LibraryItemForm open={true} onOpenChange={vi.fn()} initialItem={{ kind: 'prompt' }} />)
    const slugInput = screen.getByLabelText(/id \/ slug/i) as HTMLInputElement
    await user.clear(slugInput)
    await user.type(slugInput, 'custom-slug')
    const titleInput = screen.getByLabelText(/title/i) as HTMLInputElement
    await user.type(titleInput, 'New Title')
    // Slug should not be overwritten with auto-generated value.
    expect(slugInput.value).toBe('custom-slug')
  })
})

// ---------------------------------------------------------------------------
// LibraryItemForm — validation on submit
// ---------------------------------------------------------------------------

describe('LibraryItemForm — validation on submit', () => {
  it('shows title error when submitting with empty title', async () => {
    const user = userEvent.setup()
    render(<LibraryItemForm open={true} onOpenChange={vi.fn()} initialItem={{ kind: 'prompt' }} />)
    const submitButton = screen.getByRole('button', { name: /create item/i })
    await user.click(submitButton)
    expect(screen.getByText(/title is required/i)).toBeInTheDocument()
  })

  it('shows content error when submitting without content', async () => {
    const user = userEvent.setup()
    render(<LibraryItemForm open={true} onOpenChange={vi.fn()} initialItem={{ kind: 'prompt' }} />)
    const titleInput = screen.getByLabelText(/title/i)
    await user.type(titleInput, 'My title')
    const submitButton = screen.getByRole('button', { name: /create item/i })
    await user.click(submitButton)
    expect(screen.getByText(/content is required/i)).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// LibraryItemForm — edit mode label
// ---------------------------------------------------------------------------

describe('LibraryItemForm — edit vs create mode', () => {
  it('shows "Edit library item" title when editing an existing item', () => {
    render(<LibraryItemForm open={true} onOpenChange={vi.fn()} initialItem={buildItem()} />)
    expect(screen.getByText('Edit library item')).toBeInTheDocument()
  })

  it('shows "New library item" title for a new item', () => {
    render(<LibraryItemForm open={true} onOpenChange={vi.fn()} initialItem={{ kind: 'prompt' }} />)
    expect(screen.getByText('New library item')).toBeInTheDocument()
  })
})

// ---------------------------------------------------------------------------
// LibraryItemForm — dirty confirm dialog
// ---------------------------------------------------------------------------

describe('LibraryItemForm — dirty-state confirmation', () => {
  it('shows discard confirmation when cancelling with unsaved changes', async () => {
    const user = userEvent.setup()
    render(<LibraryItemForm open={true} onOpenChange={vi.fn()} initialItem={{ kind: 'prompt' }} />)
    const titleInput = screen.getByLabelText(/title/i)
    await user.type(titleInput, 'Changed title')
    const cancelButton = screen.getByRole('button', { name: /cancel/i })
    await user.click(cancelButton)
    expect(screen.getByText(/discard unsaved changes/i)).toBeInTheDocument()
  })

  it('does NOT show discard confirmation when cancelling without changes', async () => {
    const user = userEvent.setup()
    const onOpenChange = vi.fn()
    render(
      <LibraryItemForm open={true} onOpenChange={onOpenChange} initialItem={{ kind: 'prompt' }} />
    )
    const cancelButton = screen.getByRole('button', { name: /cancel/i })
    await user.click(cancelButton)
    expect(screen.queryByText(/discard unsaved changes/i)).not.toBeInTheDocument()
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})
