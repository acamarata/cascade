/**
 * Purpose: Personal (PCI) vault context — surfaces the PCI root (~Downloads/.cascade
 *   or CASCADE_PCI_PATH override) as a first-class vault alongside the project/global vault.
 *   Provides tree data, file selection, and daily-notes for the personal vault at /vault/personal.
 * Inputs:  PersonalVaultProviderProps.pciPath — PCI vault root (default: ~/Downloads/.cascade).
 *          Can be overridden via CASCADE_PCI_PATH env / window.__CASCADE_PCI_PATH, an explicit
 *          localStorage override (setPciRoot), or the `context.personalRoot` setting
 *          (~/.cascade/settings.json, edited via get_settings/update_settings IPC).
 * Outputs: PersonalVaultContextValue exposed via usePersonalVault() hook.
 * Constraints:
 *   - Mirrors the shape of VaultContextValue for nav/editor reuse.
 *   - Falls back to a fixture when __TAURI__ is absent (Vitest/browser dev).
 *   - PCI path is stored in localStorage under "cascade.pciPath" for settings persistence.
 *   - Resolution priority: window override > localStorage explicit override >
 *     settings.json personalRoot > DEFAULT_PCI_PATH. The settings.json value is
 *     fetched async on mount (Tauri only) and applied only when no explicit
 *     localStorage override already exists, so a user's manual override always wins.
 * SPORT: MASTER-COMPONENTS.md — PersonalVaultContext (E-P9-01)
 */

import React, { createContext, useCallback, useContext, useEffect, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import type { VaultNode, MemoryEntry } from '../types/vault'
import type { CascadeSettings } from '../types/settings'
import { buildMemoryIndex } from '../services/vault/memoryAggregator'

// ── Default PCI path resolution ───────────────────────────────────────────────

const PCI_STORAGE_KEY = 'cascade.pciPath'
const DEFAULT_PCI_PATH = '~/Downloads/.cascade'

/** Resolve the PCI root: env override > localStorage > default. */
function resolvePciRoot(): string {
  // Check window-injected override (set by Tauri or build).
  if (typeof window !== 'undefined') {
    const w = window as typeof window & { __CASCADE_PCI_PATH?: string }
    if (w.__CASCADE_PCI_PATH) return w.__CASCADE_PCI_PATH
  }
  // Check localStorage persistence.
  if (typeof localStorage !== 'undefined') {
    const stored = localStorage.getItem(PCI_STORAGE_KEY)
    if (stored) return stored
  }
  return DEFAULT_PCI_PATH
}

/** True when the current pciRoot came from an explicit, user-set localStorage override. */
function hasExplicitLocalOverride(): boolean {
  if (typeof localStorage === 'undefined') return false
  return localStorage.getItem(PCI_STORAGE_KEY) !== null
}

// ── Dev fixture ───────────────────────────────────────────────────────────────

export const PERSONAL_VAULT_FIXTURE: VaultNode = {
  name: '.cascade',
  path: '~/Downloads/.cascade',
  isDir: true,
  children: [
    {
      name: 'memory',
      path: '~/Downloads/.cascade/memory',
      isDir: true,
      children: [
        {
          name: 'decisions.md',
          path: '~/Downloads/.cascade/memory/decisions.md',
          isDir: false,
          children: [],
        },
      ],
    },
    {
      name: 'inbox',
      path: '~/Downloads/.cascade/inbox',
      isDir: true,
      children: [],
    },
    {
      name: 'ideas',
      path: '~/Downloads/.cascade/ideas',
      isDir: true,
      children: [],
    },
    {
      name: 'daily',
      path: '~/Downloads/.cascade/daily',
      isDir: true,
      children: [],
    },
  ],
}

// ── IPC helpers ───────────────────────────────────────────────────────────────

function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

async function ipcVaultList(root: string): Promise<VaultNode> {
  return invoke<VaultNode>('vault_list', { root })
}

async function ipcVaultRead(root: string, path: string): Promise<string> {
  return invoke<string>('vault_read', { root, path })
}

// ── Context shape ─────────────────────────────────────────────────────────────

export interface PersonalVaultContextValue {
  /** Current PCI vault root path. */
  pciRoot: string
  /** Root VaultNode for the personal vault (null while loading). */
  treeData: VaultNode | null
  /** Absolute path of the currently open file, or null. */
  currentFile: string | null
  /** Markdown content for currentFile, or null. */
  currentContent: string | null
  /** True while vault_list or vault_read is in flight. */
  loading: boolean
  /** Last error message, or null. */
  error: string | null
  /** Re-fetches the PCI vault tree. */
  refreshTree: () => Promise<void>
  /** Opens a file in the personal vault. */
  setCurrentFile: (path: string) => Promise<void>
  /** Override the PCI root path (persisted to localStorage). */
  setPciRoot: (newRoot: string) => void
  /** Memory entries from the personal root. */
  memoryIndex: MemoryEntry[]
  memoryLoading: boolean
  memoryError: string | null
}

export const PersonalVaultContext = createContext<PersonalVaultContextValue | null>(null)

// ── Provider ─────────────────────────────────────────────────────────────────

interface PersonalVaultProviderProps {
  children: React.ReactNode
  /** PCI vault root. Defaults to resolvePciRoot(). */
  initialPciPath?: string
}

export function PersonalVaultProvider({ children, initialPciPath }: PersonalVaultProviderProps) {
  const [pciRoot, setPciRootState] = useState<string>(initialPciPath ?? resolvePciRoot())
  const [treeData, setTreeData] = useState<VaultNode | null>(null)
  const [currentFile, setCurrentFileState] = useState<string | null>(null)
  const [currentContent, setCurrentContent] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [memoryIndex, setMemoryIndex] = useState<MemoryEntry[]>([])
  const [memoryLoading, setMemoryLoading] = useState(false)
  const [memoryError, setMemoryError] = useState<string | null>(null)

  const setPciRoot = useCallback((newRoot: string) => {
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem(PCI_STORAGE_KEY, newRoot)
    }
    setPciRootState(newRoot)
  }, [])

  // Apply the `context.personalRoot` setting on mount when no explicit
  // localStorage override exists and no initialPciPath prop was passed. This
  // makes the personal_root config actually take effect once set, while never
  // clobbering a user's manual override.
  useEffect(() => {
    if (initialPciPath || hasExplicitLocalOverride() || !isTauri()) return
    let cancelled = false
    invoke<CascadeSettings>('get_settings')
      .then((settings) => {
        const configured = settings.context?.personalRoot
        if (!cancelled && configured) {
          setPciRootState(configured)
        }
      })
      .catch(() => {
        // Settings unavailable — keep the env/default resolution already applied.
      })
    return () => {
      cancelled = true
    }
    // Runs once on mount only — initialPciPath/localStorage are captured at call time.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const refreshTree = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const node = isTauri() ? await ipcVaultList(pciRoot) : PERSONAL_VAULT_FIXTURE
      setTreeData(node)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [pciRoot])

  const setCurrentFile = useCallback(
    async (path: string) => {
      setCurrentFileState(path)
      setCurrentContent(null)
      setLoading(true)
      setError(null)
      try {
        let content: string
        if (isTauri()) {
          content = await ipcVaultRead(pciRoot, path)
        } else {
          content = `# ${path.split('/').pop() ?? 'Note'}\n\n_Mock PCI content for ${path}_`
        }
        setCurrentContent(content)
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setLoading(false)
      }
    },
    [pciRoot]
  )

  // Load tree on mount and whenever pciRoot changes.
  useEffect(() => {
    refreshTree()
  }, [refreshTree])

  // Build memory index from PCI root.
  useEffect(() => {
    if (!pciRoot) return
    let cancelled = false
    setMemoryLoading(true)
    setMemoryError(null)
    buildMemoryIndex([pciRoot])
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
  }, [pciRoot])

  const value: PersonalVaultContextValue = {
    pciRoot,
    treeData,
    currentFile,
    currentContent,
    loading,
    error,
    refreshTree,
    setCurrentFile,
    setPciRoot,
    memoryIndex,
    memoryLoading,
    memoryError,
  }

  return <PersonalVaultContext.Provider value={value}>{children}</PersonalVaultContext.Provider>
}

// ── Hook ─────────────────────────────────────────────────────────────────────

export function usePersonalVault(): PersonalVaultContextValue {
  const ctx = useContext(PersonalVaultContext)
  if (!ctx) {
    throw new Error('usePersonalVault must be used inside <PersonalVaultProvider>')
  }
  return ctx
}
