/**
 * Purpose: Settings tab for Context & Privacy — Personal-mode file scope
 *   (context.personalRoot), optional Sites root override (context.sitesRoot),
 *   and the Personal chat memory-sync consent gate (memory.personalChatSync).
 *   Closes the gap where ChatPage referenced "Enable personal chat sync in
 *   Settings" with no UI to actually do so.
 * Inputs:  draft { context: ContextSettings; memory: MemorySettings } built from
 *   two CascadeSettings sections; saveSection callback (invoked per-section by
 *   the parent, since context and memory are independent top-level sections).
 * Outputs: <section> with personalRoot path input, sitesRoot path input, and
 *   a personalChatSync toggle with an explicit on/off consent description.
 * Constraints:
 *   - personalChatSync defaults to false (opt-in, per the 2026-07-02 privacy
 *     review) — this tab must never auto-enable it.
 *   - Saving this tab persists both the context and memory sections via two
 *     independent partial update_settings calls (kept decoupled from other
 *     tabs' save cycles).
 *   - WCAG 2.1 AA: all inputs labelled; toggle uses role="switch" + aria-checked,
 *     matching the existing GeminiPoolTab enabled-toggle pattern.
 * SPORT: MASTER-COMPONENTS.md — ContextPrivacyTab — E1-S5
 */

import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import type { ContextSettings, MemorySettings } from '@/types/settings'

// ── ContextPrivacyTab ─────────────────────────────────────────────────────────

export interface ContextPrivacyTabProps {
  context: ContextSettings
  memory: MemorySettings
  isSaving: boolean
  error: string | null
  onUpdateContext: (val: ContextSettings) => void
  onUpdateMemory: (val: MemorySettings) => void
  onSave: () => void
  onDiscard: () => void
}

export function ContextPrivacyTab({
  context,
  memory,
  isSaving,
  error,
  onUpdateContext,
  onUpdateMemory,
  onSave,
  onDiscard,
}: ContextPrivacyTabProps) {
  function setContext<K extends keyof ContextSettings>(key: K, val: ContextSettings[K]) {
    onUpdateContext({ ...context, [key]: val })
  }

  function toggleChatSync() {
    onUpdateMemory({ ...memory, personalChatSync: !memory.personalChatSync })
  }

  return (
    <section aria-labelledby="context-privacy-tab-heading" className="space-y-6 max-w-xl">
      <h2 id="context-privacy-tab-heading" className="text-base font-semibold">
        Context &amp; Privacy
      </h2>

      {/* Personal root */}
      <div className="space-y-1.5">
        <label htmlFor="personal-root" className="block text-sm font-medium">
          Personal workspace root
        </label>
        <Input
          id="personal-root"
          type="text"
          value={context.personalRoot ?? ''}
          placeholder="~/Downloads"
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setContext('personalRoot', e.target.value || null)}
          className="font-mono text-xs"
        />
        <p className="text-xs text-muted-foreground">
          File scope used by Personal-mode chat and the personal vault. Leave blank to use the
          default (~/Downloads).
        </p>
      </div>

      {/* Sites root */}
      <div className="space-y-1.5">
        <label htmlFor="sites-root" className="block text-sm font-medium">
          Sites root
        </label>
        <Input
          id="sites-root"
          type="text"
          value={context.sitesRoot ?? ''}
          placeholder="~/Sites"
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setContext('sitesRoot', e.target.value || null)}
          className="font-mono text-xs"
        />
        <p className="text-xs text-muted-foreground">
          Override the default ~/Sites/ root used to resolve project context. Leave blank to use the
          system default.
        </p>
      </div>

      {/* Personal chat sync consent */}
      <div className="space-y-1.5">
        <div className="flex items-center gap-3">
          <label htmlFor="personal-chat-sync" className="text-sm font-medium">
            Enable personal chat sync
          </label>
          <button
            id="personal-chat-sync"
            type="button"
            role="switch"
            aria-checked={memory.personalChatSync}
            onClick={toggleChatSync}
            className={[
              'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors',
              'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2',
              memory.personalChatSync ? 'bg-primary' : 'bg-input',
            ].join(' ')}
          >
            <span
              className={[
                'pointer-events-none block h-4 w-4 rounded-full bg-background shadow-lg ring-0 transition-transform',
                memory.personalChatSync ? 'translate-x-4' : 'translate-x-0',
              ].join(' ')}
            />
            <span className="sr-only">{memory.personalChatSync ? 'Enabled' : 'Disabled'}</span>
          </button>
        </div>
        <p className="text-xs text-muted-foreground">
          Persist Personal-mode chat history to Cascade memory. Off: history stays on this device.
        </p>
      </div>

      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}

      <div className="flex gap-2 pt-2">
        <Button type="button" onClick={onSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save Context & Privacy'}
        </Button>
        <Button type="button" variant="outline" onClick={onDiscard} disabled={isSaving}>
          Discard
        </Button>
      </div>
    </section>
  )
}
