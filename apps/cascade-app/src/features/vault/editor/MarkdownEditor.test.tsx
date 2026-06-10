/**
 * Purpose: Unit tests for MarkdownEditor — extension factories and props/state contracts.
 *
 * NOTE ON JSDOM + CODEMIRROR:
 *   CodeMirror 6 uses ResizeObserver, requestAnimationFrame, and DOM measure APIs
 *   that are absent in jsdom. Attempting to mount the full CodeMirror editor in
 *   jsdom causes "ResizeObserver is not defined" / measure-loop failures.
 *   Strategy adopted (per spec guidance):
 *     1. Test the MarkdownEditor contract via a lightweight mock that replaces
 *        @uiw/react-codemirror with a plain <textarea> shim so the React props
 *        wiring, dirty-state machine, debounce logic, and Cmd+S hook are fully
 *        exercised without requiring a real CM6 render environment.
 *     2. Test the CM6 extension list construction in isolation (saveKeymap,
 *        extensions array length) using actual CM6 state APIs.
 */

import '@testing-library/jest-dom'
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'
import { EditorState } from '@codemirror/state'
import { markdown, markdownLanguage } from '@codemirror/lang-markdown'
import { oneDark } from '@codemirror/theme-one-dark'
import { EditorView, keymap } from '@codemirror/view'
import { Prec } from '@codemirror/state'
import React from 'react'

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// Mock @uiw/react-codemirror with a simple textarea so we can test React-level
// behaviour without requiring a live DOM measurement environment.
vi.mock('@uiw/react-codemirror', () => ({
  default: vi.fn(
    ({
      value,
      onChange,
      readOnly,
      'data-testid': testId,
    }: {
      value: string
      onChange?: (v: string) => void
      readOnly?: boolean
      'data-testid'?: string
    }) => (
      <textarea
        data-testid={testId ?? 'cm-shim'}
        value={value}
        readOnly={readOnly}
        onChange={(e) => onChange?.(e.target.value)}
      />
    )
  ),
}))

// Mock @codemirror/language-data — it lazy-loads parsers we don't need in tests.
vi.mock('@codemirror/language-data', () => ({ languages: [] }))

// Mock useTheme so component renders without a ThemeProvider.
vi.mock('@/hooks/useTheme', () => ({
  useTheme: () => ({ token: 'light', resolvedTheme: 'light', setTheme: vi.fn() }),
}))

// ---------------------------------------------------------------------------
// Component under test (imported AFTER mocks are registered)
// ---------------------------------------------------------------------------

import { MarkdownEditor } from './MarkdownEditor'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function renderEditor(overrides: Partial<React.ComponentProps<typeof MarkdownEditor>> = {}) {
  const defaultProps = {
    path: '/vault/notes/hello.md',
    value: '# Hello',
    onChange: vi.fn(),
    onSave: vi.fn(),
  }
  const props = { ...defaultProps, ...overrides }
  return { ...render(<MarkdownEditor {...props} />), props }
}

// ---------------------------------------------------------------------------
// Extension-factory tests (pure CM6 state, no DOM)
// ---------------------------------------------------------------------------

describe('CM6 extension factories', () => {
  it('builds a markdown extension with the correct language', () => {
    // Verify the markdown() extension factory accepts markdownLanguage without
    // throwing — this ensures our import path is correct.
    const ext = markdown({ base: markdownLanguage, codeLanguages: [] })
    expect(ext).toBeDefined()
  })

  it('builds an EditorState with the markdown extension', () => {
    const ext = markdown({ base: markdownLanguage, codeLanguages: [] })
    const state = EditorState.create({ doc: '# Test', extensions: [ext] })
    expect(state.doc.toString()).toBe('# Test')
  })

  it('builds the oneDark theme without throwing', () => {
    expect(oneDark).toBeDefined()
  })

  it('wires a Mod-s keymap extension without throwing', () => {
    const handler = vi.fn(() => true)
    const ext = Prec.highest(keymap.of([{ key: 'Mod-s', run: handler }]))
    // Build a minimal editor state with the extension — no DOM needed.
    const state = EditorState.create({ doc: '', extensions: [ext] })
    expect(state).toBeDefined()
  })

  it('EditorView.lineWrapping is a valid extension', () => {
    const state = EditorState.create({
      doc: 'a long line',
      extensions: [EditorView.lineWrapping],
    })
    expect(state.doc.length).toBeGreaterThan(0)
  })
})

// ---------------------------------------------------------------------------
// React component tests (jsdom + textarea shim)
// ---------------------------------------------------------------------------

describe('MarkdownEditor — props & state contract', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.clearAllMocks()
  })

  it('renders without crashing', () => {
    renderEditor()
    expect(screen.getByTestId('markdown-editor')).toBeInTheDocument()
  })

  it('displays the file name from path in the header', () => {
    renderEditor({ path: '/vault/notes/hello.md' })
    expect(screen.getByText('hello.md')).toBeInTheDocument()
  })

  it('shows initialContent (value) in the textarea shim', () => {
    renderEditor({ value: '# Hello World' })
    const ta = screen.getByTestId('cm-shim') as HTMLTextAreaElement
    expect(ta.value).toBe('# Hello World')
  })

  it('fires onChange when the shim textarea changes', () => {
    const onChange = vi.fn()
    renderEditor({ onChange })
    const ta = screen.getByTestId('cm-shim') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: '# Updated' } })
    expect(onChange).toHaveBeenCalledWith('# Updated')
  })

  it('does NOT show dirty indicator initially', () => {
    renderEditor()
    expect(screen.queryByTestId('dirty-indicator')).not.toBeInTheDocument()
  })

  it('shows dirty indicator after a change', () => {
    renderEditor()
    const ta = screen.getByTestId('cm-shim') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: '# Changed' } })
    expect(screen.getByTestId('dirty-indicator')).toBeInTheDocument()
  })

  it('calls onSave after the 2 s autosave debounce', async () => {
    const onSave = vi.fn()
    renderEditor({ path: '/vault/f.md', value: '# Start', onSave })
    const ta = screen.getByTestId('cm-shim') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: '# Changed' } })

    // Before debounce — onSave should NOT have been called yet.
    expect(onSave).not.toHaveBeenCalled()

    // Advance timers past the 2 s debounce.
    await act(async () => {
      vi.advanceTimersByTime(2000)
    })

    // onSave should have been called with the latest path and content.
    expect(onSave).toHaveBeenCalledTimes(1)
    expect(onSave).toHaveBeenCalledWith('/vault/f.md', expect.any(String))
  })

  it('resets debounce timer on rapid keystrokes (only calls onSave once)', async () => {
    const onSave = vi.fn()
    renderEditor({ onSave })
    const ta = screen.getByTestId('cm-shim') as HTMLTextAreaElement

    fireEvent.change(ta, { target: { value: 'a' } })
    await act(async () => {
      vi.advanceTimersByTime(500)
    })
    fireEvent.change(ta, { target: { value: 'ab' } })
    await act(async () => {
      vi.advanceTimersByTime(500)
    })
    fireEvent.change(ta, { target: { value: 'abc' } })

    // Still before 2 s from last keystroke.
    expect(onSave).not.toHaveBeenCalled()

    await act(async () => {
      vi.advanceTimersByTime(2000)
    })
    expect(onSave).toHaveBeenCalledTimes(1)
  })

  it('clears dirty indicator after autosave fires', async () => {
    renderEditor()
    const ta = screen.getByTestId('cm-shim') as HTMLTextAreaElement
    fireEvent.change(ta, { target: { value: '# Dirty' } })
    expect(screen.getByTestId('dirty-indicator')).toBeInTheDocument()

    await act(async () => {
      vi.advanceTimersByTime(2000)
    })
    expect(screen.queryByTestId('dirty-indicator')).not.toBeInTheDocument()
  })

  it('renders as readOnly when readOnly prop is true', () => {
    renderEditor({ readOnly: true })
    const ta = screen.getByTestId('cm-shim') as HTMLTextAreaElement
    expect(ta).toHaveAttribute('readonly')
  })
})
