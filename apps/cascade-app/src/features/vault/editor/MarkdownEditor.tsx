/**
 * Purpose: CodeMirror 6 markdown editor for the Cascade vault.
 * Inputs:
 *   - path: absolute vault path of the file being edited
 *   - value: current file content string (controlled from parent / VaultContext)
 *   - onChange: callback fired on every keystroke with the updated content
 *   - onSave: callback invoked on auto-save (2 s debounce) and Cmd+S; receives (path, content)
 *   - readOnly: optional; makes the editor non-interactive when true
 *   - noteNamesFn: optional; () => string[] — enables [[wikilink]] autocomplete when provided
 *   - onNavigate: optional; (noteName: string) => void — called on wikilink click/navigate
 * Outputs:
 *   - Renders a CodeMirror 6 editor with markdown syntax highlighting
 *   - Shows an amber dirty indicator dot in the header when there are unsaved changes
 *   - Clears the dirty dot after onSave resolves
 *   - When noteNamesFn + onNavigate are provided: [[wikilink]] decorations, autocomplete,
 *     Cmd+K insert-link, and click-to-navigate
 * Constraints:
 *   - No global state — all state is local to this component
 *   - CM6 extension array is memoised to prevent full editor rebuild on every render
 *   - debounce ref is cleared on unmount to prevent memory leaks / stale callbacks
 *   - Cmd+S is wired as a CM6 keymap extension (not a DOM listener) so it composes
 *     correctly with CodeMirror focus management (per CR-B guidance)
 *   - readOnly prop is passed via the CM6 EditorState.readOnly facet
 *   - Large files (up to 500 KB) rely on CM6 virtual scroll — no special treatment needed
 *   - wikilink extension is only added when both noteNamesFn and onNavigate are provided
 * SPORT: MASTER-COMPONENTS.md — MarkdownEditor (T-P3-E06-03, T-P3-E06-05)
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import CodeMirror from '@uiw/react-codemirror'
import { markdown, markdownLanguage } from '@codemirror/lang-markdown'
import { languages } from '@codemirror/language-data'
import { oneDark } from '@codemirror/theme-one-dark'
import { EditorView, keymap } from '@codemirror/view'
import { Prec } from '@codemirror/state'
import { useTheme } from '@/hooks/useTheme'
import { wikilinkExtension } from './wikilinks/wikilinkExtension'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export interface MarkdownEditorProps {
  /** Absolute path to the vault file being edited — passed through to onSave. */
  path: string
  /** Controlled content string from the parent / VaultContext. */
  value: string
  /** Fired on every document change with the full updated content. */
  onChange: (content: string) => void
  /**
   * Fired when the content should be persisted (auto-save debounce or Cmd+S).
   * The caller is responsible for invoking `vault_write` IPC.
   */
  onSave: (path: string, content: string) => void
  /** When true the editor is non-interactive (default: false). */
  readOnly?: boolean
  /**
   * When provided together with `onNavigate`, enables [[wikilink]] support:
   * - Decorates [[...]] tokens with a blue dotted-underline style
   * - Opens an autocomplete popup when typing `[[`
   * - Cmd+K inserts a `[[]]` template and triggers the popup
   * Called at completion-trigger time (not captured at mount) so it always
   * returns the current vault note list.
   */
  noteNamesFn?: () => string[]
  /**
   * Called when the user clicks a [[wikilink]] token in the editor.
   * Receives the raw note name (the text between `[[` and `]]`).
   * Also enables the wikilink extension when combined with `noteNamesFn`.
   */
  onNavigate?: (noteName: string) => void
}

// Auto-save debounce delay in milliseconds.
const AUTOSAVE_DELAY_MS = 2000

// ---------------------------------------------------------------------------
// Light-mode theme that maps to the app CSS variables
// ---------------------------------------------------------------------------

const lightTheme = EditorView.theme({
  '&': {
    height: '100%',
    backgroundColor: 'hsl(var(--background))',
    color: 'hsl(var(--foreground))',
    fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace',
    fontSize: '0.875rem',
  },
  '.cm-content': { padding: '12px 16px', caretColor: 'hsl(var(--primary))' },
  '.cm-cursor': { borderLeftColor: 'hsl(var(--primary))' },
  '.cm-selectionBackground, ::selection': { backgroundColor: 'hsl(var(--accent))' },
  '.cm-line': { lineHeight: '1.65' },
  '.cm-activeLine': { backgroundColor: 'hsl(var(--accent) / 0.4)' },
  '.cm-gutters': {
    backgroundColor: 'hsl(var(--background))',
    color: 'hsl(var(--muted-foreground))',
    borderRight: '1px solid hsl(var(--border))',
  },
  '.cm-scroller': { overflowY: 'auto' },
})

// ---------------------------------------------------------------------------
// MarkdownEditor component
// ---------------------------------------------------------------------------

/**
 * CodeMirror 6 markdown editor with syntax highlighting, auto-save, Cmd+S
 * manual save, and a dirty-state indicator.
 */
export function MarkdownEditor({
  path,
  value,
  onChange,
  onSave,
  readOnly = false,
  noteNamesFn,
  onNavigate,
}: MarkdownEditorProps) {
  const { resolvedTheme } = useTheme()
  const isDark = resolvedTheme === 'dark'

  // Dirty tracking: true once the user has typed something unsaved.
  const [isDirty, setIsDirty] = useState(false)

  // Ref holding the latest content so the auto-save callback always reads
  // the current value without capturing stale closure state.
  const latestContent = useRef(value)
  latestContent.current = value

  // Ref holding the latest path to avoid the auto-save keybind from capturing
  // stale path values after a file switch.
  const latestPath = useRef(path)
  latestPath.current = path

  // Debounce timer ref — cleared on unmount to prevent memory leaks.
  const autosaveTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Stable save callback — reads from refs so it never goes stale.
  const triggerSave = useCallback(() => {
    if (autosaveTimer.current) {
      clearTimeout(autosaveTimer.current)
      autosaveTimer.current = null
    }
    onSave(latestPath.current, latestContent.current)
    setIsDirty(false)
  }, [onSave])

  // Handle content changes: update dirty state and start/reset the debounce.
  const handleChange = useCallback(
    (newContent: string) => {
      onChange(newContent)
      setIsDirty(true)

      if (autosaveTimer.current) clearTimeout(autosaveTimer.current)
      autosaveTimer.current = setTimeout(triggerSave, AUTOSAVE_DELAY_MS)
    },
    [onChange, triggerSave],
  )

  // When path changes (different file opened), reset dirty state and any
  // pending auto-save so we don't save to the wrong path.
  useEffect(() => {
    setIsDirty(false)
    if (autosaveTimer.current) {
      clearTimeout(autosaveTimer.current)
      autosaveTimer.current = null
    }
  }, [path])

  // Cleanup on unmount.
  useEffect(() => {
    return () => {
      if (autosaveTimer.current) clearTimeout(autosaveTimer.current)
    }
  }, [])

  // Cmd+S keymap — wired as a CM6 keymap extension (not a DOM listener) so
  // it composes correctly with CodeMirror's internal focus handling (CR-B).
  // Prec.highest ensures it wins over any other Cmd+S binding in the editor.
  const saveKeymap = useMemo(
    () =>
      Prec.highest(
        keymap.of([
          {
            key: 'Mod-s',
            run: () => {
              triggerSave()
              return true
            },
          },
        ]),
      ),
    [triggerSave],
  )

  // Extension list is memoised so it is only rebuilt when theme, readOnly, or
  // wikilink options change — prevents full editor teardown on every parent
  // render (CR-C). noteNamesFn and onNavigate are stable refs (caller must
  // memoise them) but their identity change does trigger a rebuild which is
  // acceptable since the wikilink extension is lightweight.
  const extensions = useMemo(
    () => [
      markdown({ base: markdownLanguage, codeLanguages: languages }),
      saveKeymap,
      EditorView.lineWrapping,
      ...(readOnly ? [EditorView.editable.of(false)] : []),
      ...(!isDark ? [lightTheme] : []),
      // Wikilink support: opt-in by providing both props together
      ...(noteNamesFn && onNavigate
        ? wikilinkExtension({ noteNamesFn, onNavigate })
        : []),
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [isDark, readOnly, saveKeymap, noteNamesFn, onNavigate],
  )

  return (
    <div className="flex h-full flex-col" data-testid="markdown-editor">
      {/* ── Editor header ─────────────────────────────────────────────────── */}
      <div className="flex items-center gap-2 border-b border-border px-3 py-1.5 text-xs text-muted-foreground">
        <span className="truncate font-mono">{path.split('/').pop() ?? path}</span>

        {isDirty && (
          <span
            className="ml-auto inline-block h-2 w-2 shrink-0 rounded-full bg-amber-400"
            aria-label="Unsaved changes"
            title="Unsaved changes"
            data-testid="dirty-indicator"
          />
        )}
      </div>

      {/* ── CodeMirror editor ─────────────────────────────────────────────── */}
      <div className="min-h-0 flex-1 overflow-hidden">
        <CodeMirror
          value={value}
          onChange={handleChange}
          extensions={extensions}
          theme={isDark ? oneDark : 'light'}
          readOnly={readOnly}
          height="100%"
          style={{ height: '100%' }}
          basicSetup={{
            lineNumbers: true,
            foldGutter: true,
            dropCursor: false,
            allowMultipleSelections: false,
            indentOnInput: true,
            bracketMatching: true,
            closeBrackets: true,
            autocompletion: false,
            highlightActiveLine: true,
            highlightSelectionMatches: true,
          }}
        />
      </div>
    </div>
  )
}
