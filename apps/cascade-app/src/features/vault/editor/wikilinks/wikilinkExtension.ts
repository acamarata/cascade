/**
 * Purpose: CodeMirror 6 extension bundle for [[wikilink]] support.
 *   Provides:
 *     1. Decoration — marks [[...]] spans with `.cm-wikilink` CSS class (blue dotted underline).
 *     2. Autocomplete — opens a completion popup when cursor is inside `[[`; items
 *        come from a caller-supplied `noteNamesFn` so the extension is testable without
 *        a live VaultContext.
 *     3. Cmd+K keymap — inserts `[[]]` at the cursor and positions cursor between the brackets,
 *        then triggers the autocomplete popup.
 *     4. Click navigation — calls `onNavigate(noteName)` when the user clicks (or Cmd+clicks)
 *        a decorated wikilink span.
 * Inputs:
 *   - noteNamesFn: () => string[]  — returns the current vault note names (call-time, not
 *     captured at construction time, so the extension always sees fresh data).
 *   - onNavigate: (noteName: string) => void — called on wikilink click.
 * Outputs: An Extension[] ready to be spread into the CodeMirror extensions array.
 * Constraints:
 *   - Uses incremental update (ViewPlugin.fromClass with update()) to avoid rescanning the
 *     full document on every keystroke (only re-scans affected line ranges per CR-A guidance).
 *   - Does NOT fire on regular [link] syntax — only on double-bracket `[[`.
 *   - Cmd+K inserts the wikilink template and triggers startCompletion from
 *     @codemirror/autocomplete.
 * SPORT: MASTER-COMPONENTS.md — wikilinkExtension (T-P3-E06-05)
 */

import {
  ViewPlugin,
  DecorationSet,
  Decoration,
  EditorView,
  ViewUpdate,
  keymap,
  MatchDecorator,
} from '@codemirror/view'
import { Extension } from '@codemirror/state'
import {
  autocompletion,
  CompletionContext,
  CompletionResult,
  startCompletion,
} from '@codemirror/autocomplete'
import { activeWikilinkPrefix } from './wikilinkParser'

// ---------------------------------------------------------------------------
// CSS class for decorated wikilinks (injected via EditorView.baseTheme below)
// ---------------------------------------------------------------------------

const wikilinkTheme = EditorView.baseTheme({
  '.cm-wikilink': {
    color: 'var(--blue-400, #60a5fa)',
    textDecoration: 'underline dotted',
    cursor: 'pointer',
  },
})

// ---------------------------------------------------------------------------
// Decoration plugin — uses MatchDecorator for incremental updates (CR-A)
// ---------------------------------------------------------------------------

function makeWikilinkPlugin(): Extension {
  const decorator = new MatchDecorator({
    regexp: /\[\[([^\][\r\n]+)\]\]/g,
    decoration: () =>
      Decoration.mark({ class: 'cm-wikilink', inclusive: false }),
  })

  return ViewPlugin.fromClass(
    class {
      decorations: DecorationSet
      constructor(view: EditorView) {
        this.decorations = decorator.createDeco(view)
      }
      update(update: ViewUpdate) {
        // MatchDecorator.updateDeco is incremental — only rescans changed ranges (CR-A)
        this.decorations = decorator.updateDeco(update, this.decorations)
      }
    },
    { decorations: (v) => v.decorations },
  )
}

// ---------------------------------------------------------------------------
// Autocomplete source
// ---------------------------------------------------------------------------

/**
 * Build a CompletionSource for wikilinks backed by `noteNamesFn`.
 * Returns completions when cursor is inside `[[prefix` (not yet closed).
 */
export function makeWikilinkCompletion(
  noteNamesFn: () => string[],
): (context: CompletionContext) => CompletionResult | null {
  return (context: CompletionContext): CompletionResult | null => {
    const { state, pos } = context
    const docText = state.doc.toString()
    const prefix = activeWikilinkPrefix(docText, pos)
    if (prefix === null) return null

    const lower = prefix.toLowerCase()
    const names = noteNamesFn()
    const options = names
      .filter((n) => n.toLowerCase().includes(lower))
      .map((n) => ({
        label: n,
        apply: (view: EditorView, _completion: { label: string }, _from: number, to: number) => {
          // Replace from the start of `[[` to the current cursor position.
          // We use the closure `pos` + `prefix` captured at completion-trigger
          // time rather than the `_from` provided by CM6, because CM6's `from`
          // points to the completion `from` (after `[[`) while we need to
          // replace the full `[[prefix` span including the opening brackets.
          const wikilinkStart = pos - 2 - prefix.length
          view.dispatch({
            changes: { from: wikilinkStart, to, insert: `[[${n}]]` },
            selection: { anchor: wikilinkStart + n.length + 4 },
          })
        },
      }))

    // `from` is the position right after `[[`, covering the typed prefix
    const from = pos - prefix.length
    return { from, options, validFor: /^[^\][\r\n]*$/ }
  }
}

// ---------------------------------------------------------------------------
// Cmd+K keymap — insert [[]] template and open autocomplete
// ---------------------------------------------------------------------------

function makeInsertWikilinkKeymap(view: EditorView): boolean {
  const { state } = view
  const { from: selFrom, to: selTo } = state.selection.main
  // Insert [[]] with cursor between the inner brackets
  view.dispatch({
    changes: { from: selFrom, to: selTo, insert: '[[]]' },
    selection: { anchor: selFrom + 2 },
  })
  startCompletion(view)
  return true
}

// ---------------------------------------------------------------------------
// Click-to-navigate event handler
// ---------------------------------------------------------------------------

function makeClickHandler(
  onNavigate: (noteName: string) => void,
): Extension {
  return EditorView.domEventHandlers({
    click(event, view) {
      const target = event.target as HTMLElement
      // Walk up the DOM in case the click hit an inner span
      let el: HTMLElement | null = target
      while (el && !el.classList.contains('cm-wikilink')) {
        el = el.parentElement
      }
      if (!el) return false

      // Find the document position for the clicked element
      const pos = view.posAtDOM(el)
      const docText = view.state.doc.toString()
      // Scan for [[...]] containing pos
      const re = /\[\[([^\][\r\n]+)\]\]/g
      let m: RegExpExecArray | null
      while ((m = re.exec(docText)) !== null) {
        const start = m.index
        const end = start + m[0].length
        if (pos >= start && pos < end) {
          onNavigate(m[1])
          return true
        }
      }
      return false
    },
  })
}

// ---------------------------------------------------------------------------
// Public factory — returns the complete extension bundle
// ---------------------------------------------------------------------------

export interface WikilinkExtensionOptions {
  /** Returns fresh vault note names each time — called on every completion trigger. */
  noteNamesFn: () => string[]
  /** Called when a decorated [[wikilink]] is clicked in the editor. */
  onNavigate: (noteName: string) => void
}

/**
 * Returns a CodeMirror Extension[] implementing [[wikilink]] support:
 * decoration, autocomplete, Cmd+K insert, and click navigation.
 */
export function wikilinkExtension(options: WikilinkExtensionOptions): Extension[] {
  const { noteNamesFn, onNavigate } = options
  return [
    wikilinkTheme,
    makeWikilinkPlugin(),
    autocompletion({
      override: [makeWikilinkCompletion(noteNamesFn)],
      activateOnTyping: true,
    }),
    keymap.of([
      {
        key: 'Mod-k',
        run: makeInsertWikilinkKeymap,
      },
    ]),
    makeClickHandler(onNavigate),
  ]
}
