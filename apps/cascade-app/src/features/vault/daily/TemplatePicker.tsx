/**
 * Purpose: Modal dialog that lists vault note templates and creates a new note
 *   from the selected template after prompting for a title.
 *   Step 1 — pick a template from the list.
 *   Step 2 — enter a note title.
 *   Step 3 — vault_write the new note with variables substituted; setCurrentFile to open it.
 * Inputs:  isOpen (boolean), onClose callback, VaultContext (via useVault).
 * Outputs: A new vault note created at vaultRoot/<title>.md with template content.
 * Constraints: Focus is trapped inside the Radix Dialog (aria-modal).
 *   Escape closes the dialog at any step.
 *   Re-renders do NOT re-fetch the template list — it loads once on open.
 * SPORT: MASTER-COMPONENTS.md — TemplatePicker (T-P3-E06-10)
 */

import { useEffect, useState } from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { invoke } from '@tauri-apps/api/core'
import { useVault } from '../../../context/VaultContext'
import { listTemplates, applyTemplate, buildTemplateVars } from '../../../services/vault/templates'
import type { TemplateEntry } from '../../../services/vault/templates'

interface TemplatePickerProps {
  isOpen: boolean
  onClose: () => void
}

type Step = 'pick' | 'title'

function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

export function TemplatePicker({ isOpen, onClose }: TemplatePickerProps) {
  const { templatesFolder, setCurrentFile } = useVault()
  // Access vaultRoot from VaultContext — we read treeData.path as root
  const { treeData } = useVault()
  const vaultRoot = treeData?.path ?? '~/.cascade'

  const [templates, setTemplates] = useState<TemplateEntry[]>([])
  const [selected, setSelected] = useState<TemplateEntry | null>(null)
  const [title, setTitle] = useState('')
  const [step, setStep] = useState<Step>('pick')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Load templates when dialog opens
  useEffect(() => {
    if (!isOpen) return
    setStep('pick')
    setSelected(null)
    setTitle('')
    setError(null)

    listTemplates(vaultRoot, templatesFolder)
      .then(setTemplates)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
  }, [isOpen, vaultRoot, templatesFolder])

  function handlePickTemplate(entry: TemplateEntry) {
    setSelected(entry)
    setStep('title')
  }

  async function handleCreate() {
    if (!selected || !title.trim()) return
    setBusy(true)
    setError(null)
    try {
      let content = ''
      if (isTauri()) {
        const raw = await invoke<string>('vault_read', { path: selected.path })
        content = applyTemplate(raw, buildTemplateVars(title.trim()))
      } else {
        // Dev mode: use a stub
        content = applyTemplate('# {{title}}\ndate: {{date}}', buildTemplateVars(title.trim()))
      }

      const safeName = title.trim().replace(/[<>:"/\\|?*]/g, '-')
      const notePath = `${vaultRoot.replace(/\/$/, '')}/${safeName}.md`

      if (isTauri()) {
        await invoke('vault_write', { path: notePath, content })
      }

      await setCurrentFile(notePath)
      onClose()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Enter' && step === 'title') {
      e.preventDefault()
      void handleCreate()
    }
  }

  return (
    <Dialog.Root open={isOpen} onOpenChange={(v) => !v && onClose()}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 backdrop-blur-sm" />
        <Dialog.Content
          className="fixed left-1/2 top-[20%] z-50 w-full max-w-md -translate-x-1/2 rounded-xl border border-border bg-popover p-6 shadow-2xl outline-none"
          aria-label="New note from template"
          aria-modal
          role="dialog"
        >
          <Dialog.Title className="mb-4 text-sm font-semibold text-foreground">
            {step === 'pick' ? 'Choose a template' : 'Name your note'}
          </Dialog.Title>

          {error && (
            <p className="mb-3 text-xs text-destructive" role="alert">
              {error}
            </p>
          )}

          {step === 'pick' && (
            <div className="flex flex-col gap-1" role="listbox" aria-label="Templates">
              {templates.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No templates found in{' '}
                  <code>
                    {vaultRoot}/{templatesFolder}/
                  </code>
                  .
                </p>
              ) : (
                templates.map((t) => (
                  <button
                    key={t.path}
                    type="button"
                    role="option"
                    aria-selected={selected?.path === t.path}
                    onClick={() => handlePickTemplate(t)}
                    className="rounded-md px-3 py-2 text-left text-sm hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  >
                    {t.name}
                  </button>
                ))
              )}
            </div>
          )}

          {step === 'title' && (
            <div className="flex flex-col gap-3">
              <p className="text-xs text-muted-foreground">
                Template: <strong>{selected?.name}</strong>
              </p>
              <input
                // eslint-disable-next-line jsx-a11y/no-autofocus
                autoFocus
                type="text"
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                onKeyDown={handleKeyDown}
                placeholder="Note title…"
                className="w-full rounded-md border border-border bg-background px-3 py-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label="Note title"
              />
              <div className="flex gap-2 justify-end">
                <button
                  type="button"
                  onClick={() => setStep('pick')}
                  className="rounded-md border border-border px-3 py-1.5 text-xs hover:bg-accent"
                >
                  Back
                </button>
                <button
                  type="button"
                  onClick={() => void handleCreate()}
                  disabled={!title.trim() || busy}
                  className="rounded-md bg-primary px-3 py-1.5 text-xs text-primary-foreground hover:opacity-90 disabled:opacity-50"
                >
                  {busy ? 'Creating…' : 'Create note'}
                </button>
              </div>
            </div>
          )}

          <Dialog.Close asChild>
            <button
              type="button"
              aria-label="Close"
              className="absolute right-3 top-3 rounded-sm text-muted-foreground opacity-70 hover:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              ✕
            </button>
          </Dialog.Close>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
