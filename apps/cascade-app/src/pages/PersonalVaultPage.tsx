/**
 * Purpose: /vault/personal route — surfaces the PCI vault (~/Downloads/.cascade)
 *   as a first-class root with its own navigator, editor, and memory view.
 *   A settings field at the top lets the user override the PCI path.
 * Inputs:  None — reads PersonalVaultContext via usePersonalVault().
 * Outputs: Three-panel personal vault page at /vault/personal.
 * Constraints:
 *   - Wraps in PersonalVaultProvider so tree + note share the same context.
 *   - PCI path override persisted to localStorage (cascade.pciPath).
 *   - Loading/empty/error states; a11y landmarks.
 * SPORT: MASTER-ROUTES.md — /vault/personal (E-P9-01)
 *        MASTER-COMPONENTS.md — PersonalVaultPage (E-P9-01)
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { Brain, FolderOpen, RefreshCw, Settings2, UserCircle2, FileText } from 'lucide-react'
import { PersonalVaultProvider, usePersonalVault } from '../context/PersonalVaultContext'
import { VaultNavigator } from '../components/vault/VaultNavigator'
import { MarkdownEditor } from '../features/vault/editor/MarkdownEditor'
import { MarkdownPreview } from '../components/vault/MarkdownPreview'
import { MemoryViewer } from '../components/vault/MemoryViewer'
import { Button } from '../components/ui/button'

// ── isTauri helper ────────────────────────────────────────────────────────────

function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

async function ipcVaultWrite(root: string, path: string, content: string): Promise<void> {
  return invoke('vault_write', { root, path, content })
}

// ── PCI Path Settings strip ───────────────────────────────────────────────────

function PciPathStrip() {
  const { pciRoot, setPciRoot, refreshTree, loading } = usePersonalVault()
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(pciRoot)
  const inputRef = useRef<HTMLInputElement>(null)

  function startEdit() {
    setDraft(pciRoot)
    setEditing(true)
    setTimeout(() => inputRef.current?.focus(), 50)
  }

  function commitEdit() {
    const trimmed = draft.trim()
    if (trimmed && trimmed !== pciRoot) {
      setPciRoot(trimmed)
    }
    setEditing(false)
  }

  function cancelEdit() {
    setDraft(pciRoot)
    setEditing(false)
  }

  return (
    <div className="flex shrink-0 items-center gap-2 border-b border-border bg-muted/30 px-3 py-1.5">
      <UserCircle2 className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      <span className="text-xs font-medium text-muted-foreground">Personal vault root:</span>

      {editing ? (
        <>
          <input
            ref={inputRef}
            type="text"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') commitEdit()
              if (e.key === 'Escape') cancelEdit()
            }}
            className="flex-1 rounded border border-border bg-background px-2 py-0.5 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-primary"
            aria-label="PCI vault root path"
          />
          <Button size="sm" variant="default" onClick={commitEdit} className="h-6 px-2 text-xs">
            Set
          </Button>
          <Button size="sm" variant="ghost" onClick={cancelEdit} className="h-6 px-2 text-xs">
            Cancel
          </Button>
        </>
      ) : (
        <>
          <code className="flex-1 truncate text-xs font-mono text-foreground" title={pciRoot}>
            {pciRoot}
          </code>
          <Button
            size="sm"
            variant="ghost"
            onClick={startEdit}
            aria-label="Change PCI vault root"
            className="h-6 px-1.5"
          >
            <Settings2 className="h-3 w-3" aria-hidden="true" />
          </Button>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => void refreshTree()}
            disabled={loading}
            aria-label="Refresh personal vault"
            className="h-6 px-1.5"
          >
            <RefreshCw
              className={['h-3 w-3', loading ? 'animate-spin' : ''].join(' ')}
              aria-hidden="true"
            />
          </Button>
        </>
      )}
    </div>
  )
}

// ── Tab types ─────────────────────────────────────────────────────────────────

type PanelTab = 'files' | 'memory'

// ── Editor pane ───────────────────────────────────────────────────────────────

function PersonalEditorView() {
  const { pciRoot, currentFile, currentContent } = usePersonalVault()
  const [editorContent, setEditorContent] = useState('')
  const [showPreview, setShowPreview] = useState(true)

  useEffect(() => {
    if (currentContent !== null) {
      setEditorContent(currentContent)
    }
  }, [currentContent])

  const handleSave = useCallback(
    async (path: string, content: string) => {
      if (!isTauri()) return
      try {
        await ipcVaultWrite(pciRoot, path, content)
      } catch (e) {
        console.error('[PersonalVaultPage] vault_write failed:', e)
      }
    },
    [pciRoot]
  )

  if (!currentFile) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 text-muted-foreground">
        <FileText className="h-8 w-8" aria-hidden="true" />
        <p className="text-sm">Select a note from your personal vault</p>
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <div className="flex h-9 shrink-0 items-center gap-2 border-b border-border px-3">
        <FileText className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="truncate text-xs font-medium" title={currentFile}>
          {currentFile.split('/').pop() ?? currentFile}
        </span>
        <button
          type="button"
          onClick={() => setShowPreview((v) => !v)}
          className="ml-auto rounded px-2 py-0.5 text-[11px] text-muted-foreground hover:bg-accent hover:text-foreground"
          aria-label={showPreview ? 'Hide preview' : 'Show preview'}
        >
          {showPreview ? 'Hide preview' : 'Preview'}
        </button>
      </div>
      <div className="flex min-h-0 flex-1 overflow-hidden">
        <div
          className={`flex flex-col overflow-hidden ${showPreview ? 'w-1/2 border-r border-border' : 'w-full'}`}
        >
          <div className="min-h-0 flex-1 overflow-hidden">
            <MarkdownEditor
              path={currentFile}
              value={editorContent}
              onChange={setEditorContent}
              onSave={handleSave}
            />
          </div>
        </div>
        {showPreview && (
          <div className="w-1/2 overflow-hidden">
            <MarkdownPreview content={editorContent} className="h-full" />
          </div>
        )}
      </div>
    </div>
  )
}

// ── Page inner ────────────────────────────────────────────────────────────────

function PersonalVaultPageInner() {
  const [activeTab, setActiveTab] = useState<PanelTab>('files')
  const { error, loading, memoryIndex, memoryLoading, memoryError, setCurrentFile, pciRoot } =
    usePersonalVault()

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {/* PCI path settings strip */}
      <PciPathStrip />

      <div className="flex min-h-0 flex-1 overflow-hidden">
        {/* Left sidebar */}
        <aside
          aria-label="Personal vault sidebar"
          className="flex w-64 shrink-0 flex-col border-r border-border bg-card overflow-hidden"
        >
          {/* Tab bar */}
          <div
            role="tablist"
            aria-label="Personal vault panels"
            className="flex h-9 shrink-0 border-b border-border"
          >
            <button
              type="button"
              role="tab"
              id="pv-tab-files"
              aria-controls="pv-panel-files"
              aria-selected={activeTab === 'files'}
              onClick={() => setActiveTab('files')}
              className={[
                'flex flex-1 items-center justify-center gap-1 text-xs font-medium transition-colors',
                activeTab === 'files'
                  ? 'border-b-2 border-primary text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              ].join(' ')}
            >
              <FolderOpen className="h-3.5 w-3.5" aria-hidden="true" />
              Files
            </button>
            <button
              type="button"
              role="tab"
              id="pv-tab-memory"
              aria-controls="pv-panel-memory"
              aria-selected={activeTab === 'memory'}
              onClick={() => setActiveTab('memory')}
              className={[
                'flex flex-1 items-center justify-center gap-1 text-xs font-medium transition-colors',
                activeTab === 'memory'
                  ? 'border-b-2 border-primary text-foreground'
                  : 'text-muted-foreground hover:text-foreground',
              ].join(' ')}
            >
              <Brain className="h-3.5 w-3.5" aria-hidden="true" />
              Memory
            </button>
          </div>

          {/* Files panel */}
          <div
            role="tabpanel"
            id="pv-panel-files"
            aria-labelledby="pv-tab-files"
            className={[
              'overflow-hidden',
              activeTab === 'files' ? 'flex flex-1 flex-col' : 'hidden',
            ].join(' ')}
          >
            {error ? (
              <div className="p-4 text-xs text-destructive" role="alert">
                {error}
              </div>
            ) : loading && !activeTab ? (
              <div className="p-4 text-xs text-muted-foreground">Loading…</div>
            ) : (
              <VaultNavigator />
            )}
          </div>

          {/* Memory panel */}
          <div
            role="tabpanel"
            id="pv-panel-memory"
            aria-labelledby="pv-tab-memory"
            className={[
              'overflow-hidden',
              activeTab === 'memory' ? 'flex flex-1 flex-col' : 'hidden',
            ].join(' ')}
          >
            <MemoryViewer
              memoryIndex={memoryIndex}
              onOpenFile={(path) => void setCurrentFile(path)}
              loading={memoryLoading}
              error={memoryError}
              projectPaths={[pciRoot]}
            />
          </div>
        </aside>

        {/* Right: editor */}
        <main className="flex-1 overflow-hidden bg-background">
          <PersonalEditorView />
        </main>
      </div>
    </div>
  )
}

// ── Public export ─────────────────────────────────────────────────────────────

export function PersonalVaultPage() {
  return (
    <PersonalVaultProvider>
      <PersonalVaultPageInner />
    </PersonalVaultProvider>
  )
}
