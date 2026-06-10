/**
 * Purpose: Register Cmd+Shift+D (today's daily note) and Cmd+Shift+T (new note
 *   from template) keyboard shortcuts and their command-palette entries.
 *   Hooks into VaultContext.openTodayNote for the daily note action.
 *   Template picker is handled by the TemplatePicker component (T-P3-E06-10).
 * Inputs:  openTodayNote callback from VaultContext; onOpenTemplatePicker callback.
 * Outputs: Side-effects — registers window keydown listener and globalRegistry entries.
 * Constraints: Listener is on document (fires from any focused element).
 *   Cleanup runs on unmount to prevent duplicate listeners.
 *   Commands registered into globalRegistry are idempotent (replace by id).
 * SPORT: MASTER-HOOKS.md — useDailyNoteShortcut (T-P3-E06-10)
 */

import { useEffect } from 'react'
import { globalRegistry } from '../../../lib/commands/registry'

interface UseDailyNoteShortcutOptions {
  openTodayNote: () => Promise<void>
  onOpenTemplatePicker: () => void
}

/**
 * Register Cmd+Shift+D and Cmd+Shift+T shortcuts + command palette entries.
 * Call this hook once inside a component that has access to VaultContext.
 *
 * @param options.openTodayNote       - Callback from VaultContext.openTodayNote.
 * @param options.onOpenTemplatePicker - Callback to open the template picker dialog.
 */
export function useDailyNoteShortcut({
  openTodayNote,
  onOpenTemplatePicker,
}: UseDailyNoteShortcutOptions): void {
  useEffect(() => {
    // Register command palette entries (idempotent)
    globalRegistry.register({
      id: 'vault:today-note',
      label: "Open today's daily note",
      group: 'Vault',
      keywords: ['daily', 'journal', 'today', 'note'],
      shortcut: '⌘⇧D',
      onExecute: () => void openTodayNote(),
    })

    globalRegistry.register({
      id: 'vault:new-from-template',
      label: 'New note from template…',
      group: 'Vault',
      keywords: ['template', 'create', 'new', 'note'],
      shortcut: '⌘⇧T',
      onExecute: onOpenTemplatePicker,
    })

    // Keyboard shortcut handler
    function handleKeyDown(e: KeyboardEvent) {
      const isMac = navigator.platform.toLowerCase().includes('mac')
      const meta = isMac ? e.metaKey : e.ctrlKey

      // Cmd+Shift+D — today's daily note
      if (meta && e.shiftKey && e.key === 'd') {
        e.preventDefault()
        void openTodayNote()
        return
      }

      // Cmd+Shift+T — template picker
      if (meta && e.shiftKey && e.key === 't') {
        e.preventDefault()
        onOpenTemplatePicker()
      }
    }

    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('keydown', handleKeyDown)
      globalRegistry.unregister('vault:today-note')
      globalRegistry.unregister('vault:new-from-template')
    }
  }, [openTodayNote, onOpenTemplatePicker])
}
