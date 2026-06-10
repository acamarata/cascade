/**
 * Purpose: Shared vault context — provides tree data, current file selection,
 *   and content for the vault navigator and read-only note viewer.
 *   Calls vault_list IPC on mount / refresh; vault_read on file open.
 *   Falls back to a hard-coded fixture when window.__TAURI__ is undefined
 *   (browser-based Vitest / Storybook dev).
 * Inputs:  VaultProviderProps.defaultRoot — vault root path (default: ~/.cascade)
 * Outputs: VaultContextValue exposed via useVault() hook.
 * Constraints: No circular re-renders — setCurrentFile only triggers vault_read,
 *   not a full tree reload.  vault_watch integration is additive (T-P3-E06-01).
 * SPORT: MASTER-COMPONENTS.md — VaultContext (T-P3-E06-02)
 */

import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
} from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { VaultNode, MemoryEntry } from '../types/vault'
import { openDailyNote as _openDailyNote } from '../services/vault/dailyNotes'
import { buildMemoryIndex } from '../services/vault/memoryAggregator'

// ── Mock IPC fixture ─────────────────────────────────────────────────────────

/** Hard-coded tree returned when __TAURI__ is absent (dev/Vitest). */
export const VAULT_FIXTURE: VaultNode = {
  name: '.cascade',
  path: '~/.cascade',
  isDir: true,
  children: [
    {
      name: 'GCI',
      path: '~/.cascade/GCI',
      isDir: true,
      children: [
        {
          name: 'CLAUDE.md',
          path: '~/.cascade/GCI/CLAUDE.md',
          isDir: false,
          children: [],
        },
        {
          name: 'rules.md',
          path: '~/.cascade/GCI/rules.md',
          isDir: false,
          children: [],
        },
      ],
    },
    {
      name: 'PCI',
      path: '~/.cascade/PCI',
      isDir: true,
      children: [
        {
          name: 'project.md',
          path: '~/.cascade/PCI/project.md',
          isDir: false,
          children: [],
        },
      ],
    },
    {
      name: 'APC',
      path: '~/.cascade/APC',
      isDir: true,
      children: [],
    },
    {
      name: 'PPC',
      path: '~/.cascade/PPC',
      isDir: true,
      children: [],
    },
    {
      name: 'PRC',
      path: '~/.cascade/PRC',
      isDir: true,
      children: [
        {
          name: 'repo.md',
          path: '~/.cascade/PRC/repo.md',
          isDir: false,
          children: [],
        },
      ],
    },
    {
      name: 'PAC',
      path: '~/.cascade/PAC',
      isDir: true,
      children: [],
    },
  ],
}

// ── IPC helpers ──────────────────────────────────────────────────────────────

/** Returns true when running inside the Tauri webview. */
function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

async function ipcVaultList(root: string): Promise<VaultNode> {
  return invoke<VaultNode>('vault_list', { root })
}

async function ipcVaultRead(path: string): Promise<string> {
  return invoke<string>('vault_read', { path })
}

// ── Context shape ────────────────────────────────────────────────────────────

export interface VaultContextValue {
  /** Root VaultNode returned by vault_list (or null while loading). */
  treeData: VaultNode | null
  /** Absolute path of the currently open file, or null if none. */
  currentFile: string | null
  /** Markdown content for currentFile, or null while loading / no selection. */
  currentContent: string | null
  /** True while vault_list or vault_read is in flight. */
  loading: boolean
  /** Last error message, or null. */
  error: string | null
  /** Re-fetches the vault tree from IPC. */
  refreshTree: () => Promise<void>
  /**
   * Opens a file: sets currentFile and fires vault_read.
   * Seam for T-P3-E06-03 editor: caller can subscribe to currentContent.
   * onOpenNote is the public seam — the editor replaces the NoteView stub in T-03.
   */
  setCurrentFile: (path: string) => Promise<void>
  /** Called by parent to override the file open handler (T-P3-E06-03 seam). */
  onOpenNote: ((path: string, content: string) => void) | null
  setOnOpenNote: (handler: ((path: string, content: string) => void) | null) => void
  // ── Memory index (T-P3-E06-13 seam A) ─────────────────────────────────────
  /** Flat cross-project memory index built by buildMemoryIndex. */
  memoryIndex: MemoryEntry[]
  /** True while the memory index is being loaded. */
  memoryLoading: boolean
  /** Last error from memory index build, or null. */
  memoryError: string | null
  /** The project root paths used to build the memory index (passthrough for child components). */
  projectPaths: string[]
  // ── Daily notes (T-P3-E06-10) ─────────────────────────────────────────────
  /** Subdirectory within vault root for daily notes. Default: "daily". */
  dailyNotesFolder: string
  /** Subdirectory within vault root for note templates. Default: "templates". */
  templatesFolder: string
  /**
   * Create (or open if exists) today's daily note and navigate to it.
   * Bound to Cmd+Shift+D globally in AppLayout.
   */
  openTodayNote: () => Promise<void>
}

export const VaultContext = createContext<VaultContextValue | null>(null)

// ── Provider ─────────────────────────────────────────────────────────────────

interface VaultProviderProps {
  children: React.ReactNode
  /** Vault root directory. Defaults to ~/.cascade */
  defaultRoot?: string
  /** Daily notes subdirectory. Defaults to "daily". */
  dailyNotesFolder?: string
  /** Templates subdirectory. Defaults to "templates". */
  templatesFolder?: string
  /**
   * Absolute paths to registered project roots for the memory index.
   * Passed from daemon registry / AppState. Defaults to [] (empty index in dev).
   * T-P3-E06-13 seam A — provider wires buildMemoryIndex on mount + on change.
   */
  projectPaths?: string[]
}

export function VaultProvider({
  children,
  defaultRoot = '~/.cascade',
  dailyNotesFolder = 'daily',
  templatesFolder = 'templates',
  projectPaths = [],
}: VaultProviderProps) {
  const [treeData, setTreeData] = useState<VaultNode | null>(null)
  const [currentFile, setCurrentFileState] = useState<string | null>(null)
  const [currentContent, setCurrentContent] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [onOpenNote, setOnOpenNote] = useState<((path: string, content: string) => void) | null>(null)
  // ── Memory index state (T-P3-E06-13 seam A) ─────────────────────────────────
  const [memoryIndex, setMemoryIndex] = useState<MemoryEntry[]>([])
  const [memoryLoading, setMemoryLoading] = useState(false)
  const [memoryError, setMemoryError] = useState<string | null>(null)

  const refreshTree = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const node = isTauri() ? await ipcVaultList(defaultRoot) : VAULT_FIXTURE
      setTreeData(node)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [defaultRoot])

  const setCurrentFile = useCallback(
    async (path: string) => {
      setCurrentFileState(path)
      setCurrentContent(null)
      setLoading(true)
      setError(null)
      try {
        let content: string
        if (isTauri()) {
          content = await ipcVaultRead(path)
        } else {
          // Dev fixture: return a stub based on the path
          content = `# ${path.split('/').pop() ?? 'Note'}\n\n_Mock content for ${path}_`
        }
        setCurrentContent(content)
        if (onOpenNote) {
          onOpenNote(path, content)
        }
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setLoading(false)
      }
    },
    [onOpenNote],
  )

  // ── Daily notes (T-P3-E06-10) ───────────────────────────────────────────────

  const openTodayNote = useCallback(async () => {
    setError(null)
    try {
      const path = await _openDailyNote(defaultRoot, dailyNotesFolder)
      await setCurrentFile(path)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [defaultRoot, dailyNotesFolder, setCurrentFile])

  // Load tree on mount
  useEffect(() => {
    refreshTree()
  }, [refreshTree])

  // ── Memory index (T-P3-E06-13 seam A) ───────────────────────────────────────
  // Rebuild whenever projectPaths changes. In dev (empty array) → empty index.
  useEffect(() => {
    let cancelled = false
    setMemoryLoading(true)
    setMemoryError(null)
    buildMemoryIndex(projectPaths)
      .then((entries) => {
        if (!cancelled) setMemoryIndex(entries)
      })
      .catch((e) => {
        if (!cancelled) setMemoryError(e instanceof Error ? e.message : String(e))
      })
      .finally(() => {
        if (!cancelled) setMemoryLoading(false)
      })
    return () => {
      cancelled = true
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(projectPaths)])

  const value: VaultContextValue = {
    treeData,
    currentFile,
    currentContent,
    loading,
    error,
    refreshTree,
    setCurrentFile,
    onOpenNote,
    setOnOpenNote: (handler) => setOnOpenNote(() => handler),
    memoryIndex,
    memoryLoading,
    memoryError,
    projectPaths,
    dailyNotesFolder,
    templatesFolder,
    openTodayNote,
  }

  return <VaultContext.Provider value={value}>{children}</VaultContext.Provider>
}

// ── Hook ─────────────────────────────────────────────────────────────────────

export function useVault(): VaultContextValue {
  const ctx = useContext(VaultContext)
  if (!ctx) {
    throw new Error('useVault must be used inside <VaultProvider>')
  }
  return ctx
}
