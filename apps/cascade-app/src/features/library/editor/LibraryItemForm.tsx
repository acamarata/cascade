/**
 * Purpose: Author/editor modal form for creating and editing Cascade library items.
 *   Adapts its field set to the selected item kind (prompt / agent / persona /
 *   slash-command). Shared fields: title, description, slug, content, tags,
 *   harness_targets. Kind-specific: persona adds system_prompt; slash-command
 *   adds trigger + args_description.
 * Inputs:
 *   - open: boolean — controlled dialog open state
 *   - onOpenChange: (open: boolean) => void — called to open/close the dialog
 *   - initialItem: Partial<LibraryItem> — pre-fills fields (pass { kind: 'prompt' } for new)
 *   - onSaved: (item: LibraryItem) => void — called after successful upsert
 * Outputs: Radix Dialog wrapping a controlled form.
 * Constraints:
 *   - No external form library — uses React controlled state.
 *   - title + content required; slug auto-generated from title, editable.
 *   - Slug must match [a-z0-9-]+.
 *   - Dirty-state guard: cancel when dirty shows an AlertDialog confirmation.
 *   - WCAG 2.1 AA: focus rings, aria labels, escape to close (Radix default).
 *   - All IPC calls degrade gracefully: command-not-found → error in form footer.
 * SPORT: MASTER-COMPONENTS.md — LibraryItemForm — features/library/editor/ — T-P3-E07-06
 */

import { useState, useEffect, useCallback, useRef } from 'react'
import { X, Tag, Loader2, AlertCircle } from 'lucide-react'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
  DialogDescription,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { useUpsertLibraryItem } from '../useUpsertLibraryItem'
import type { LibraryItem, LibraryItemKind, HarnessTarget } from '../types'

// ── Constants ─────────────────────────────────────────────────────────────────

const ALL_KINDS: { value: LibraryItemKind; label: string }[] = [
  { value: 'prompt', label: 'Prompt' },
  { value: 'agent', label: 'Agent' },
  { value: 'persona', label: 'Persona' },
  { value: 'slash-command', label: 'Slash-Command' },
]

const ALL_HARNESS_TARGETS: HarnessTarget[] = ['CC', 'OC', 'Codex']

const SLUG_PATTERN = /^[a-z0-9]+(-[a-z0-9]+)*$/

// ── Slug utility ──────────────────────────────────────────────────────────────

/**
 * Auto-generates a kebab-case slug from a title string.
 * Matches the spec formula: lowercase → collapse non-alphanumeric → strip leading/trailing hyphens.
 */
export function generateSlug(title: string): string {
  return title
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/(^-|-$)/g, '')
}

// ── Validation ────────────────────────────────────────────────────────────────

export interface FormErrors {
  title?: string
  slug?: string
  content?: string
  trigger?: string
}

/**
 * Validates form fields and returns an error map.
 * Returns an empty object when all fields are valid.
 */
export function validateForm(
  fields: Pick<LibraryItem, 'title' | 'id' | 'content' | 'kind' | 'trigger'>,
): FormErrors {
  const errors: FormErrors = {}

  if (!fields.title.trim()) {
    errors.title = 'Title is required.'
  }

  if (!fields.id.trim()) {
    errors.slug = 'Slug is required.'
  } else if (!SLUG_PATTERN.test(fields.id)) {
    errors.slug = 'Slug must match pattern [a-z0-9-]+ (lowercase, digits, hyphens only).'
  }

  if (!fields.content.trim()) {
    errors.content = 'Content is required.'
  }

  if (fields.kind === 'slash-command' && !fields.trigger?.trim()) {
    errors.trigger = 'Trigger is required for slash-commands (e.g. /summarize).'
  }

  return errors
}

// ── TagInput ──────────────────────────────────────────────────────────────────

interface TagInputProps {
  tags: string[]
  onChange: (tags: string[]) => void
  'aria-label'?: string
}

/**
 * Chip-based tag input.
 * Enter or comma commits the current input as a tag.
 * Clicking the × on a chip removes it.
 */
function TagInput({ tags, onChange, 'aria-label': ariaLabel }: TagInputProps) {
  const [inputValue, setInputValue] = useState('')

  function commitTag(raw: string) {
    const trimmed = raw.trim().replace(/,/g, '').trim()
    if (!trimmed || tags.includes(trimmed)) return
    onChange([...tags, trimmed])
    setInputValue('')
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault()
      commitTag(inputValue)
    } else if (e.key === 'Backspace' && inputValue === '' && tags.length > 0) {
      onChange(tags.slice(0, -1))
    }
  }

  function removeTag(tag: string) {
    onChange(tags.filter((t) => t !== tag))
  }

  return (
    <div
      className={cn(
        'flex flex-wrap gap-1 min-h-9 w-full rounded-md border border-input bg-transparent px-3 py-1',
        'focus-within:ring-1 focus-within:ring-ring',
      )}
      aria-label={ariaLabel}
    >
      {tags.map((tag) => (
        <span
          key={tag}
          className="flex items-center gap-1 rounded-full bg-accent px-2 py-0.5 text-xs font-medium"
        >
          <Tag className="h-2.5 w-2.5" aria-hidden="true" />
          {tag}
          <button
            type="button"
            onClick={() => removeTag(tag)}
            className="ml-0.5 rounded-full hover:bg-muted focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
            aria-label={`Remove tag ${tag}`}
          >
            <X className="h-2.5 w-2.5" />
          </button>
        </span>
      ))}
      <input
        value={inputValue}
        onChange={(e) => setInputValue(e.target.value)}
        onKeyDown={handleKeyDown}
        onBlur={() => commitTag(inputValue)}
        placeholder={tags.length === 0 ? 'Add tags (Enter or comma)' : ''}
        className="flex-1 min-w-20 bg-transparent text-sm focus:outline-none placeholder:text-muted-foreground"
        aria-label="Tag input"
      />
    </div>
  )
}

// ── DirtyConfirmDialog ────────────────────────────────────────────────────────

interface DirtyConfirmDialogProps {
  open: boolean
  onConfirm: () => void
  onCancel: () => void
}

function DirtyConfirmDialog({ open, onConfirm, onCancel }: DirtyConfirmDialogProps) {
  if (!open) return null
  return (
    <div
      className="fixed inset-0 z-[60] flex items-center justify-center bg-black/50"
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="dirty-confirm-title"
      aria-describedby="dirty-confirm-desc"
    >
      <div className="w-full max-w-sm rounded-lg border border-border bg-background p-5 shadow-xl">
        <h2 id="dirty-confirm-title" className="text-base font-semibold">
          Discard unsaved changes?
        </h2>
        <p id="dirty-confirm-desc" className="mt-2 text-sm text-muted-foreground">
          You have unsaved changes. Closing the form will discard them permanently.
        </p>
        <div className="mt-4 flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onCancel}>
            Keep editing
          </Button>
          <Button variant="destructive" size="sm" onClick={onConfirm}>
            Discard changes
          </Button>
        </div>
      </div>
    </div>
  )
}

// ── FormField ─────────────────────────────────────────────────────────────────

interface FieldProps {
  id: string
  label: string
  error?: string
  required?: boolean
  children: React.ReactNode
}

function FormField({ id, label, error, required, children }: FieldProps) {
  return (
    <div className="space-y-1">
      <label
        htmlFor={id}
        className={cn('block text-sm font-medium', required && 'after:content-["*"] after:ml-0.5 after:text-destructive')}
      >
        {label}
      </label>
      {children}
      {error && (
        <p className="flex items-center gap-1 text-xs text-destructive" role="alert">
          <AlertCircle className="h-3 w-3" aria-hidden="true" />
          {error}
        </p>
      )}
    </div>
  )
}

// ── Props ─────────────────────────────────────────────────────────────────────

export interface LibraryItemFormProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Pre-fills fields. Pass { kind: 'prompt' } for a blank new-item form. */
  initialItem?: Partial<LibraryItem>
  /** Called after a successful upsert; the saved item is passed as argument. */
  onSaved?: (item: LibraryItem) => void
}

// ── Default state ─────────────────────────────────────────────────────────────

function buildInitialState(initial?: Partial<LibraryItem>): LibraryItem {
  return {
    id: initial?.id ?? '',
    title: initial?.title ?? '',
    description: initial?.description ?? '',
    kind: initial?.kind ?? 'prompt',
    content: initial?.content ?? '',
    tags: initial?.tags ?? [],
    harness_targets: initial?.harness_targets ?? [],
    system_prompt: initial?.system_prompt ?? '',
    trigger: initial?.trigger ?? '',
    args_description: initial?.args_description ?? '',
    created_at: initial?.created_at,
    updated_at: initial?.updated_at,
  }
}

// ── Component ─────────────────────────────────────────────────────────────────

/**
 * Modal form for creating and editing library items.
 *
 * The form is fully controlled via React state. On save it calls the
 * upsert_library_item IPC command and reports success via onSaved.
 * Cancel with unsaved changes shows a confirmation dialog.
 */
export function LibraryItemForm({ open, onOpenChange, initialItem, onSaved }: LibraryItemFormProps) {
  const [fields, setFields] = useState<LibraryItem>(() => buildInitialState(initialItem))
  const [errors, setErrors] = useState<FormErrors>({})
  const [dirtyConfirmOpen, setDirtyConfirmOpen] = useState(false)
  // Track whether the slug has been manually edited (prevents auto-gen from overwriting it).
  const slugEditedRef = useRef(false)

  const isEditing = Boolean(initialItem?.id)

  const { mutate, isPending, error: ipcError, reset: resetMutation } = useUpsertLibraryItem({
    onSuccess: (ack) => {
      const saved: LibraryItem = { ...fields, id: ack.id }
      onSaved?.(saved)
      onOpenChange(false)
    },
  })

  // Derived dirty state: compare current fields against initialItem.
  const isDirty = useCallback(() => {
    const orig = buildInitialState(initialItem)
    return JSON.stringify(fields) !== JSON.stringify(orig)
  }, [fields, initialItem])

  // Reset form when the dialog opens/closes or initialItem changes.
  useEffect(() => {
    if (open) {
      setFields(buildInitialState(initialItem))
      setErrors({})
      resetMutation()
      slugEditedRef.current = Boolean(initialItem?.id)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, initialItem?.id])

  // ── Field helpers ──────────────────────────────────────────────────────────

  function setField<K extends keyof LibraryItem>(key: K, value: LibraryItem[K]) {
    setFields((prev) => ({ ...prev, [key]: value }))
    // Clear per-field error on change.
    if (key in errors) {
      setErrors((prev) => {
        const next = { ...prev }
        delete (next as Record<string, string>)[key === 'id' ? 'slug' : String(key)]
        return next
      })
    }
  }

  function handleTitleChange(e: React.ChangeEvent<HTMLInputElement>) {
    const title = e.target.value
    setField('title', title)
    if (!slugEditedRef.current) {
      setField('id', generateSlug(title))
    }
    if (errors.title) setErrors((p) => ({ ...p, title: undefined }))
  }

  function handleSlugChange(e: React.ChangeEvent<HTMLInputElement>) {
    slugEditedRef.current = true
    setField('id', e.target.value)
    if (errors.slug) setErrors((p) => ({ ...p, slug: undefined }))
  }

  function handleKindChange(kind: LibraryItemKind) {
    setField('kind', kind)
  }

  function toggleHarnessTarget(target: HarnessTarget) {
    const current = fields.harness_targets
    const next = current.includes(target)
      ? current.filter((t) => t !== target)
      : [...current, target]
    setField('harness_targets', next)
  }

  // ── Dialog close / cancel ──────────────────────────────────────────────────

  function requestClose() {
    if (isDirty()) {
      setDirtyConfirmOpen(true)
    } else {
      onOpenChange(false)
    }
  }

  function handleConfirmDiscard() {
    setDirtyConfirmOpen(false)
    onOpenChange(false)
  }

  function handleCancelDiscard() {
    setDirtyConfirmOpen(false)
  }

  // Intercept Radix onOpenChange so escape/backdrop trigger our dirty guard.
  function handleDialogOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      requestClose()
    } else {
      onOpenChange(true)
    }
  }

  // ── Submit ─────────────────────────────────────────────────────────────────

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    const errs = validateForm(fields)
    if (Object.keys(errs).length > 0) {
      setErrors(errs)
      return
    }
    // Strip kind-specific fields that don't apply to the current kind.
    const payload: LibraryItem = {
      ...fields,
      system_prompt: fields.kind === 'persona' ? fields.system_prompt : undefined,
      trigger: fields.kind === 'slash-command' ? fields.trigger : undefined,
      args_description: fields.kind === 'slash-command' ? fields.args_description : undefined,
    }
    mutate(payload)
  }

  // ── Render ─────────────────────────────────────────────────────────────────

  return (
    <>
      <Dialog open={open} onOpenChange={handleDialogOpenChange}>
        <DialogContent className="max-w-2xl max-h-[90vh] overflow-y-auto" aria-describedby="library-form-description">
          <DialogHeader>
            <DialogTitle>
              {isEditing ? 'Edit library item' : 'New library item'}
            </DialogTitle>
            <DialogDescription id="library-form-description">
              {isEditing
                ? 'Update the fields below and save.'
                : 'Fill in the fields below to create a new library item.'}
            </DialogDescription>
          </DialogHeader>

          <form id="library-item-form" onSubmit={handleSubmit} noValidate>
            <div className="space-y-4 py-2">

              {/* Kind selector */}
              <FormField id="library-kind" label="Kind" required>
                <div
                  role="radiogroup"
                  aria-label="Item kind"
                  className="flex gap-2 flex-wrap"
                  id="library-kind"
                >
                  {ALL_KINDS.map(({ value, label }) => (
                    <button
                      key={value}
                      type="button"
                      role="radio"
                      aria-checked={fields.kind === value}
                      onClick={() => handleKindChange(value)}
                      className={cn(
                        'rounded-full border px-3 py-1 text-xs font-medium transition-colors',
                        'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
                        fields.kind === value
                          ? 'border-primary bg-primary text-primary-foreground'
                          : 'border-border bg-background text-muted-foreground hover:bg-accent',
                      )}
                    >
                      {label}
                    </button>
                  ))}
                </div>
              </FormField>

              {/* Title */}
              <FormField id="library-title" label="Title" required error={errors.title}>
                <Input
                  id="library-title"
                  value={fields.title}
                  onChange={handleTitleChange}
                  placeholder="e.g. Concise code reviewer"
                  aria-required="true"
                  aria-invalid={Boolean(errors.title)}
                  aria-describedby={errors.title ? 'library-title-error' : undefined}
                />
              </FormField>

              {/* Slug */}
              <FormField id="library-slug" label="ID / Slug" required error={errors.slug}>
                <Input
                  id="library-slug"
                  value={fields.id}
                  onChange={handleSlugChange}
                  placeholder="e.g. concise-code-reviewer"
                  aria-required="true"
                  aria-invalid={Boolean(errors.slug)}
                  aria-describedby={errors.slug ? 'library-slug-error' : undefined}
                />
                <p className="text-xs text-muted-foreground">
                  Lowercase letters, digits, and hyphens only. Auto-generated from title.
                </p>
              </FormField>

              {/* Description */}
              <FormField id="library-description" label="Description">
                <Input
                  id="library-description"
                  value={fields.description}
                  onChange={(e) => setField('description', e.target.value)}
                  placeholder="Short description shown in the library panel"
                />
              </FormField>

              {/* Content */}
              <FormField id="library-content" label="Content" required error={errors.content}>
                <textarea
                  id="library-content"
                  value={fields.content}
                  onChange={(e) => {
                    setField('content', e.target.value)
                    if (errors.content) setErrors((p) => ({ ...p, content: undefined }))
                  }}
                  placeholder={
                    fields.kind === 'agent'
                      ? 'Agent configuration (YAML or plain text)'
                      : fields.kind === 'persona'
                        ? 'Persona traits and background'
                        : fields.kind === 'slash-command'
                          ? 'Command template or instruction'
                          : 'Prompt body text'
                  }
                  rows={8}
                  className={cn(
                    'flex w-full rounded-md border border-input bg-transparent px-3 py-2',
                    'text-sm font-mono shadow-sm placeholder:text-muted-foreground',
                    'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
                    'disabled:cursor-not-allowed disabled:opacity-50 resize-y',
                    errors.content && 'border-destructive',
                  )}
                  aria-required="true"
                  aria-invalid={Boolean(errors.content)}
                  aria-label="Content"
                />
              </FormField>

              {/* Kind-specific: persona system_prompt */}
              {fields.kind === 'persona' && (
                <FormField id="library-system-prompt" label="System prompt">
                  <textarea
                    id="library-system-prompt"
                    value={fields.system_prompt ?? ''}
                    onChange={(e) => setField('system_prompt', e.target.value)}
                    placeholder="Top-level system prompt injected before persona traits"
                    rows={4}
                    className={cn(
                      'flex w-full rounded-md border border-input bg-transparent px-3 py-2',
                      'text-sm font-mono shadow-sm placeholder:text-muted-foreground',
                      'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
                      'resize-y',
                    )}
                    aria-label="System prompt"
                  />
                </FormField>
              )}

              {/* Kind-specific: slash-command trigger + args */}
              {fields.kind === 'slash-command' && (
                <>
                  <FormField id="library-trigger" label="Trigger" required error={errors.trigger}>
                    <Input
                      id="library-trigger"
                      value={fields.trigger ?? ''}
                      onChange={(e) => {
                        setField('trigger', e.target.value)
                        if (errors.trigger) setErrors((p) => ({ ...p, trigger: undefined }))
                      }}
                      placeholder="e.g. /summarize"
                      aria-required="true"
                      aria-invalid={Boolean(errors.trigger)}
                    />
                  </FormField>
                  <FormField id="library-args" label="Arguments description">
                    <Input
                      id="library-args"
                      value={fields.args_description ?? ''}
                      onChange={(e) => setField('args_description', e.target.value)}
                      placeholder="e.g. <file> [--verbose]"
                      aria-label="Arguments description"
                    />
                  </FormField>
                </>
              )}

              {/* Tags */}
              <FormField id="library-tags" label="Tags">
                <TagInput
                  tags={fields.tags}
                  onChange={(tags) => setField('tags', tags)}
                  aria-label="Tags"
                />
              </FormField>

              {/* Harness targets */}
              <FormField id="library-harness" label="Harness targets">
                <div
                  role="group"
                  aria-label="Harness targets"
                  className="flex gap-3"
                  id="library-harness"
                >
                  {ALL_HARNESS_TARGETS.map((target) => {
                    const checked = fields.harness_targets.includes(target)
                    return (
                      <label
                        key={target}
                        className={cn(
                          'flex items-center gap-1.5 rounded-md border px-3 py-1.5 text-xs font-medium cursor-pointer transition-colors',
                          'hover:bg-accent focus-within:ring-1 focus-within:ring-ring',
                          checked ? 'border-primary bg-primary/10 text-primary' : 'border-border',
                        )}
                      >
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={() => toggleHarnessTarget(target)}
                          className="sr-only"
                          aria-label={target}
                        />
                        {target}
                      </label>
                    )
                  })}
                </div>
              </FormField>

            </div>
          </form>

          {/* IPC error banner */}
          {ipcError && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-md border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive"
            >
              <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
              <span>{ipcError}</span>
            </div>
          )}

          <DialogFooter>
            <Button type="button" variant="outline" onClick={requestClose} disabled={isPending}>
              Cancel
            </Button>
            <Button
              type="submit"
              form="library-item-form"
              disabled={isPending}
              aria-disabled={isPending}
            >
              {isPending && <Loader2 className="mr-2 h-4 w-4 animate-spin" aria-hidden="true" />}
              {isPending ? 'Saving…' : isEditing ? 'Save changes' : 'Create item'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Dirty-state confirmation — rendered outside the main Dialog to avoid z-index conflict */}
      <DirtyConfirmDialog
        open={dirtyConfirmOpen}
        onConfirm={handleConfirmDiscard}
        onCancel={handleCancelDiscard}
      />
    </>
  )
}
