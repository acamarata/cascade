/**
 * Purpose: Top-level /vault route page.
 *   Left panel: VaultNavigator tree (collapsible dirs + md leaves).
 *   Right panel (editor mode): MarkdownEditor (left) + MarkdownPreview (right) split.
 *   Right panel (no file): Empty state.
 *   FrontmatterPanel appears below the editor when frontmatter is present.
 *   BacklinksPanel appears below the editor when a note is open (T-P3-E06-06).
 *   Wrap in VaultProvider so tree + note share the same VaultContext.
 * Inputs:  None — self-contained; reads VaultContext via hooks.
 * Outputs: Three-panel vault page at /vault route.
 * Constraints:
 *   - Preview toggle controlled by local showPreview state (default: true).
 *   - Editor onChange feeds MarkdownPreview via local editorContent state.
 *   - onSave calls vault_write IPC via invoke (Tauri) or no-ops in dev mode.
 *   - vault_read → editor content on file open via setOnOpenNote seam.
 *   - Link index rebuilt on vault://changed event (T-P3-E06-06).
 * SPORT: MASTER-COMPONENTS.md — VaultPage (T-P3-E06-02, T-P3-E06-04, T-P3-E06-06)
 */

import { useCallback, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { invoke } from '@tauri-apps/api/core'
import { FileText, Eye, EyeOff, GitGraph } from 'lucide-react'
import { VaultProvider, useVault } from '../context/VaultContext'
import { VaultNavigator } from '../components/vault/VaultNavigator'
import { TagBrowser } from '../components/vault/TagBrowser'
import { BacklinksPanel } from '../components/vault/BacklinksPanel'
import { MarkdownEditor } from '../features/vault/editor/MarkdownEditor'
import { MarkdownPreview } from '../components/vault/MarkdownPreview'
import { FrontmatterPanel } from '../components/vault/FrontmatterPanel'
import { TemplatePicker } from '../features/vault/daily/TemplatePicker'
import { useDailyNoteShortcut } from '../features/vault/daily/useDailyNoteShortcut'
import { useLinkIndex } from '../hooks/useLinkIndex'

// ── Sidebar tab type ──────────────────────────────────────────────────────────
type SidebarTab = 'files' | 'tags'

// ---------------------------------------------------------------------------
// IPC helper
// ---------------------------------------------------------------------------

/** Returns true when running inside the Tauri webview. */
function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

async function ipcVaultWrite(path: string, content: string): Promise<void> {
  return invoke('vault_write', { path, content })
}

// ── Editor + Preview pane ────────────────────────────────────────────────────

/**
 * Active editor view with live preview and frontmatter panel.
 * Replaces the read-only NoteView stub (T-P3-E06-03 seam).
 */
function EditorView() {
  const { currentFile, currentContent, setOnOpenNote, setCurrentFile } = useVault()
  // Link index — rebuilt on vault://changed; provides backlinks for current note.
  const linkIndex = useLinkIndex()

  // Local editor content — updated on every keystroke for live preview.
  const [editorContent, setEditorContent] = useState<string>('')
  // Toggle: show/hide the preview pane.
  const [showPreview, setShowPreview] = useState(true)

  // When a new file is opened, seed the editor with the vault_read content.
  const handleOpenNote = useCallback((_path: string, content: string) => {
    setEditorContent(content)
  }, [])

  useEffect(() => {
    setOnOpenNote(handleOpenNote)
    return () => setOnOpenNote(null)
  }, [setOnOpenNote, handleOpenNote])

  // Seed editor when currentContent arrives (initial load).
  useEffect(() => {
    if (currentContent !== null) {
      setEditorContent(currentContent)
    }
  }, [currentContent])

  const handleSave = useCallback(
    async (path: string, content: string) => {
      if (!isTauri()) return
      try {
        await ipcVaultWrite(path, content)
      } catch (e) {
        console.error('[VaultPage] vault_write failed:', e)
      }
    },
    [],
  )

  if (!currentFile) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground">
        <FileText className="h-8 w-8" aria-hidden="true" />
        <p className="text-sm">Select a note from the vault</p>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* ── Toolbar ─────────────────────────────────────────────────────── */}
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
        <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate text-xs font-medium text-foreground" title={currentFile}>
          {currentFile.split('/').pop() ?? currentFile}
        </span>

        {/* Preview toggle */}
        <button
          type="button"
          onClick={() => setShowPreview((v) => !v)}
          className="ml-auto flex items-center gap-1 rounded px-2 py-0.5 text-[11px] text-muted-foreground hover:bg-accent hover:text-foreground"
          aria-label={showPreview ? 'Hide preview' : 'Show preview'}
          data-testid="preview-toggle"
        >
          {showPreview ? (
            <EyeOff className="h-3 w-3" aria-hidden="true" />
          ) : (
            <Eye className="h-3 w-3" aria-hidden="true" />
          )}
          <span>{showPreview ? 'Hide preview' : 'Preview'}</span>
        </button>
      </div>

      {/* ── Editor / Preview split ───────────────────────────────────────── */}
      <div className="flex min-h-0 flex-1 overflow-hidden">
        {/* Editor column */}
        <div
          className={`flex flex-col overflow-hidden ${showPreview ? 'w-1/2 border-r border-border' : 'w-full'}`}
        >
          {/* MarkdownEditor fills available height */}
          <div className="min-h-0 flex-1 overflow-hidden">
            <MarkdownEditor
              path={currentFile}
              value={editorContent}
              onChange={setEditorContent}
              onSave={handleSave}
            />
          </div>

          {/* FrontmatterPanel — zero height when no frontmatter */}
          <FrontmatterPanel content={editorContent} />

          {/* BacklinksPanel — additive mount (T-P3-E06-06) */}
          {currentFile && (
            <div className="shrink-0 border-t border-border">
              <BacklinksPanel
                currentFile={currentFile}
                linkIndex={linkIndex}
                setCurrentFile={setCurrentFile}
              />
            </div>
          )}
        </div>

        {/* Preview column */}
        {showPreview && (
          <div className="w-1/2 overflow-hidden">
            <MarkdownPreview content={editorContent} className="h-full" />
          </div>
        )}
      </div>
    </div>
  )
}

// ── Page shell ────────────────────────────────────────────────────────────────

function VaultPageInner() {
  // Sidebar tab selection (Files vs Tags)
  const [activeTab, setActiveTab] = useState<SidebarTab>('files')
  // Tag filter — lifted here so TagBrowser and VaultNavigator share it.
  const [selectedTags, setSelectedTags] = useState<ReadonlySet<string>>(new Set())
  // Template picker dialog state (T-P3-E06-10)
  const [templatePickerOpen, setTemplatePickerOpen] = useState(false)

  const { openTodayNote, setCurrentFile } = useVault()

  // Seam B (T-P3-E06-13): open a file passed via ?file=<encoded-path> from
  // SearchPanel / MemoryCard navigation. Runs once on mount (or when param changes).
  const [searchParams, setSearchParams] = useSearchParams()
  useEffect(() => {
    const filePath = searchParams.get('file')
    if (filePath) {
      void setCurrentFile(decodeURIComponent(filePath))
      // Remove the param so the URL stays clean after opening.
      setSearchParams((prev) => {
        const next = new URLSearchParams(prev)
        next.delete('file')
        return next
      }, { replace: true })
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams.get('file')])

  // Register Cmd+Shift+D and Cmd+Shift+T shortcuts + command palette entries
  useDailyNoteShortcut({
    openTodayNote,
    onOpenTemplatePicker: () => setTemplatePickerOpen(true),
  })

  return (
    <div className="flex h-full overflow-hidden">
      {/* Left: tabbed sidebar */}
      <aside
        aria-label="Vault sidebar"
        className="flex w-64 shrink-0 flex-col border-r border-border bg-card overflow-hidden"
      >
        {/* Tab bar */}
        <div
          role="tablist"
          aria-label="Sidebar panels"
          className="flex h-9 shrink-0 border-b border-border"
        >
          <button
            type="button"
            role="tab"
            id="tab-files"
            aria-controls="panel-files"
            aria-selected={activeTab === 'files'}
            onClick={() => setActiveTab('files')}
            className={[
              'flex-1 text-xs font-medium transition-colors',
              activeTab === 'files'
                ? 'border-b-2 border-primary text-foreground'
                : 'text-muted-foreground hover:text-foreground',
            ].join(' ')}
          >
            Files
          </button>
          <button
            type="button"
            role="tab"
            id="tab-tags"
            aria-controls="panel-tags"
            aria-selected={activeTab === 'tags'}
            onClick={() => setActiveTab('tags')}
            className={[
              'flex-1 text-xs font-medium transition-colors',
              activeTab === 'tags'
                ? 'border-b-2 border-primary text-foreground'
                : 'text-muted-foreground hover:text-foreground',
            ].join(' ')}
          >
            Tags
            {selectedTags.size > 0 && (
              <span className="ml-1 rounded-full bg-primary px-1 text-[9px] font-bold text-primary-foreground">
                {selectedTags.size}
              </span>
            )}
          </button>
          {/* T-P3-E06-07: Graph view link */}
          <Link
            to="/vault/graph"
            aria-label="Open graph view"
            title="Graph view"
            className="flex flex-1 items-center justify-center text-muted-foreground hover:text-foreground transition-colors"
          >
            <GitGraph className="h-3.5 w-3.5" aria-hidden="true" />
          </Link>
        </div>

        {/* Panel: Files */}
        <div
          role="tabpanel"
          id="panel-files"
          aria-labelledby="tab-files"
          className={[
            'overflow-hidden',
            activeTab === 'files' ? 'flex flex-1 flex-col' : 'hidden',
          ].join(' ')}
        >
          <VaultNavigator tagFilter={selectedTags} />
        </div>

        {/* Panel: Tags */}
        <div
          role="tabpanel"
          id="panel-tags"
          aria-labelledby="tab-tags"
          className={[
            'overflow-hidden',
            activeTab === 'tags' ? 'flex flex-1 flex-col' : 'hidden',
          ].join(' ')}
        >
          <TagBrowser selectedTags={selectedTags} onTagsChange={setSelectedTags} />
        </div>
      </aside>

      {/* Right: editor + preview */}
      <main className="flex-1 overflow-hidden bg-background">
        <EditorView />
      </main>

      {/* Template picker dialog — additive mount (T-P3-E06-10) */}
      <TemplatePicker
        isOpen={templatePickerOpen}
        onClose={() => setTemplatePickerOpen(false)}
      />
    </div>
  )
}

export function VaultPage() {
  return (
    <VaultProvider>
      <VaultPageInner />
    </VaultProvider>
  )
}
