/**
 * Purpose: useLinkIndex — React hook that builds and maintains a vault-wide
 *   wikilink reverse index. Reads all .md files via vault_read IPC on mount
 *   (when treeData is available) and rebuilds on every vault://changed event.
 *   Returns the current BacklinkEntry map (null while the initial build runs).
 * Inputs:  VaultContext.treeData (tree of all .md paths).
 * Outputs: Map<normalisedTargetName, BacklinkEntry[]> | null
 * Constraints: No-op in non-Tauri (dev/Vitest) — returns empty Map immediately.
 *   Debounced (300 ms) to guard against rapid vault://changed bursts.
 *   Mirrors the TagBrowser pattern for consistency (T-P3-E06-08).
 * SPORT: MASTER-COMPONENTS.md — useLinkIndex hook (T-P3-E06-06)
 */

import { useCallback, useEffect, useRef, useState } from 'react'
import { invoke } from '@tauri-apps/api/core'
import { useVault } from '../context/VaultContext'
import type { VaultNode, VaultChangedPayload } from '../types/vault'
import { buildLinkIndex, type BacklinkEntry, type LinkIndexFile } from '../services/vault/linkIndex'

// ── Helpers ───────────────────────────────────────────────────────────────────

/** Returns true when running inside the Tauri webview. */
function isTauri(): boolean {
  return typeof window !== 'undefined' && '__TAURI__' in window
}

/** Recursively collects all leaf .md file paths from a VaultNode tree. */
function collectLeafPaths(node: VaultNode): string[] {
  if (!node.isDir) return [node.path]
  return node.children.flatMap(collectLeafPaths)
}

/** Reads content for all leaf paths via vault_read IPC in parallel. */
async function readAllFiles(paths: string[]): Promise<LinkIndexFile[]> {
  const results = await Promise.allSettled(
    paths.map((path) =>
      invoke<string>('vault_read', { path }).then((content) => ({ path, content })),
    ),
  )
  return results
    .filter((r): r is PromiseFulfilledResult<LinkIndexFile> => r.status === 'fulfilled')
    .map((r) => r.value)
}

// ── Hook ──────────────────────────────────────────────────────────────────────

/**
 * useLinkIndex
 *
 * Builds a vault-wide link index on mount (after treeData loads) and rebuilds
 * it on every `vault://changed` Tauri event. The index is debounced so rapid
 * file-system events don't trigger redundant full-vault reads.
 *
 * In non-Tauri environments (dev/Vitest), returns an empty Map immediately
 * (no IPC available to read file contents).
 *
 * @returns Map<normalisedTargetName, BacklinkEntry[]> | null
 *   null = initial build has not completed yet.
 *   Map  = current index (may be empty if vault has no wikilinks).
 */
export function useLinkIndex(): Map<string, BacklinkEntry[]> | null {
  const { treeData } = useVault()
  const [index, setIndex] = useState<Map<string, BacklinkEntry[]> | null>(null)

  // Debounce timer ref — cleared on each vault://changed event burst.
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const build = useCallback(async (tree: VaultNode | null) => {
    if (!tree) {
      setIndex(new Map())
      return
    }

    if (!isTauri()) {
      // No IPC available in dev mode — return empty index.
      setIndex(new Map())
      return
    }

    const paths = collectLeafPaths(tree)
    if (paths.length === 0) {
      setIndex(new Map())
      return
    }

    const files = await readAllFiles(paths)
    setIndex(buildLinkIndex(files))
  }, [])

  // Build on mount / when treeData changes.
  useEffect(() => {
    void build(treeData)
  }, [treeData, build])

  // Rebuild (debounced) on vault://changed event.
  useEffect(() => {
    if (!isTauri()) return

    let unlisten: (() => void) | null = null

    async function subscribe() {
      const { listen } = await import('@tauri-apps/api/event')
      unlisten = await listen<VaultChangedPayload>('vault://changed', () => {
        // Debounce: cancel any pending rebuild and schedule a fresh one.
        if (debounceRef.current !== null) {
          clearTimeout(debounceRef.current)
        }
        debounceRef.current = setTimeout(() => {
          void build(treeData)
        }, 300)
      })
    }

    void subscribe()

    return () => {
      if (unlisten) unlisten()
      if (debounceRef.current !== null) {
        clearTimeout(debounceRef.current)
      }
    }
  }, [treeData, build])

  return index
}
