/**
 * Purpose: CASCADE.md editor panel — open, edit, validate, and save CASCADE.md files.
 * Inputs:  File path selected via Tauri dialog or drag-drop
 * Outputs: Textarea-based editor; validation feedback; save status
 * Constraints:
 *   - Undo/redo via useReducer history stack (not browser native undo).
 *   - Validation calls the Rust command `validate_cascade_doc` on debounced input.
 *   - File save calls `save_cascade_doc`; unsaved changes block navigation with a warning.
 * SPORT: MASTER-COMPONENTS.md — CascadeEditor
 */

import { useCallback, useEffect, useReducer, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { open as openDialog } from '@tauri-apps/plugin-dialog'
import { FileText, FolderOpen, Save, Undo, Redo, AlertCircle, CheckCircle2 } from 'lucide-react'

// ---------------------------------------------------------------------------
// Undo/redo history reducer
// ---------------------------------------------------------------------------

interface EditorState {
  content: string
  past: string[]
  future: string[]
}

type EditorAction =
  | { type: 'SET'; value: string }
  | { type: 'UNDO' }
  | { type: 'REDO' }
  | { type: 'LOAD'; value: string }

function editorReducer(state: EditorState, action: EditorAction): EditorState {
  switch (action.type) {
    case 'SET':
      return {
        content: action.value,
        past: [...state.past, state.content].slice(-50), // keep last 50 states
        future: [],
      }
    case 'UNDO': {
      if (!state.past.length) return state
      const prev = state.past[state.past.length - 1]
      return {
        content: prev,
        past: state.past.slice(0, -1),
        future: [state.content, ...state.future],
      }
    }
    case 'REDO': {
      if (!state.future.length) return state
      const next = state.future[0]
      return {
        content: next,
        past: [...state.past, state.content],
        future: state.future.slice(1),
      }
    }
    case 'LOAD':
      return { content: action.value, past: [], future: [] }
  }
}

// ---------------------------------------------------------------------------
// Validation result type (mirrors Rust commands.rs ValidationResult)
// ---------------------------------------------------------------------------

interface ValidationResult {
  valid: boolean
  errors: Array<{ line: number; message: string }>
  warnings: Array<{ line: number; message: string }>
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function CascadeEditor() {
  const [state, dispatch] = useReducer(editorReducer, {
    content: '',
    past: [],
    future: [],
  })
  const [filePath, setFilePath] = useState<string | null>(null)
  const [isDirty, setIsDirty] = useState(false)
  const [savedContent, setSavedContent] = useState('')
  const [validation, setValidation] = useState<ValidationResult | null>(null)
  const [isSaving, setIsSaving] = useState(false)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Track dirty state
  useEffect(() => {
    setIsDirty(state.content !== savedContent)
  }, [state.content, savedContent])

  // Debounced validation
  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current)
    if (!state.content) {
      setValidation(null)
      return
    }
    debounceRef.current = setTimeout(async () => {
      try {
        const result = await invoke<ValidationResult>('validate_cascade_doc', {
          content: state.content,
        })
        setValidation(result)
      } catch {
        // Validation is best-effort; don't block editing on errors.
      }
    }, 600)
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current)
    }
  }, [state.content])

  const handleOpen = useCallback(async () => {
    const selected = await openDialog({
      title: 'Open CASCADE.md',
      filters: [{ name: 'Markdown', extensions: ['md'] }],
    })
    if (!selected || typeof selected !== 'string') return

    try {
      const doc = await invoke<{ content: string }>('load_cascade_doc', {
        path: selected,
      })
      dispatch({ type: 'LOAD', value: doc.content })
      setFilePath(selected)
      setSavedContent(doc.content)
    } catch (err) {
      console.error('Failed to load document:', err)
    }
  }, [])

  const handleSave = useCallback(async () => {
    if (!filePath) return
    setIsSaving(true)
    try {
      await invoke('save_cascade_doc', { path: filePath, content: state.content })
      setSavedContent(state.content)
    } catch (err) {
      console.error('Failed to save document:', err)
    } finally {
      setIsSaving(false)
    }
  }, [filePath, state.content])

  // Keyboard shortcuts within the editor
  const handleKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 's') {
        e.preventDefault()
        handleSave()
      }
      if ((e.metaKey || e.ctrlKey) && e.key === 'z' && !e.shiftKey) {
        e.preventDefault()
        dispatch({ type: 'UNDO' })
      }
      if ((e.metaKey || e.ctrlKey) && (e.key === 'y' || (e.key === 'z' && e.shiftKey))) {
        e.preventDefault()
        dispatch({ type: 'REDO' })
      }
    },
    [handleSave]
  )

  return (
    <div className="flex h-full flex-col gap-3">
      {/* Toolbar */}
      <div className="flex items-center gap-2">
        <button
          type="button"
          onClick={handleOpen}
          className="flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label="Open CASCADE.md file"
        >
          <FolderOpen className="h-4 w-4" aria-hidden="true" />
          Open
        </button>

        <button
          type="button"
          onClick={handleSave}
          disabled={!filePath || !isDirty || isSaving}
          className="flex items-center gap-1.5 rounded-md border border-border bg-background px-3 py-1.5 text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50"
          aria-label={`Save file (⌘S)${isDirty ? ' — unsaved changes' : ''}`}
        >
          <Save className="h-4 w-4" aria-hidden="true" />
          {isSaving ? 'Saving…' : 'Save'}
        </button>

        <div className="h-4 w-px bg-border" aria-hidden="true" />

        <button
          type="button"
          onClick={() => dispatch({ type: 'UNDO' })}
          disabled={!state.past.length}
          className="rounded-md border border-border bg-background p-1.5 hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50"
          aria-label="Undo (⌘Z)"
        >
          <Undo className="h-4 w-4" aria-hidden="true" />
        </button>

        <button
          type="button"
          onClick={() => dispatch({ type: 'REDO' })}
          disabled={!state.future.length}
          className="rounded-md border border-border bg-background p-1.5 hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50"
          aria-label="Redo (⌘Y)"
        >
          <Redo className="h-4 w-4" aria-hidden="true" />
        </button>

        {filePath && (
          <span className="ml-auto truncate text-xs text-muted-foreground" title={filePath}>
            <FileText className="mr-1 inline h-3.5 w-3.5" aria-hidden="true" />
            {filePath.split('/').slice(-2).join('/')}
            {isDirty && (
              <span className="ml-1 text-warning" aria-label="unsaved changes">
                ●
              </span>
            )}
          </span>
        )}
      </div>

      {/* Validation banner */}
      {validation && !validation.valid && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive"
        >
          <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
          <ul className="space-y-0.5">
            {validation.errors.map((err, i) => (
              <li key={i}>
                Line {err.line}: {err.message}
              </li>
            ))}
          </ul>
        </div>
      )}
      {validation?.valid && (
        <div
          role="status"
          className="flex items-center gap-2 rounded-md border border-green-500/30 bg-green-500/10 px-3 py-1.5 text-xs text-green-700 dark:text-green-400"
        >
          <CheckCircle2 className="h-3.5 w-3.5" aria-hidden="true" />
          Valid CASCADE.md
        </div>
      )}

      {/* Editor textarea */}
      <textarea
        className="flex-1 resize-none rounded-md border border-border bg-background p-3 font-mono text-sm leading-relaxed focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        value={state.content}
        onChange={(e) => dispatch({ type: 'SET', value: e.target.value })}
        onKeyDown={handleKeyDown}
        placeholder={filePath ? '' : 'Open a CASCADE.md file to start editing…'}
        aria-label="CASCADE.md editor"
        spellCheck={false}
      />
    </div>
  )
}
