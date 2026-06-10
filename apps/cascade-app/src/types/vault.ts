/**
 * Purpose: TypeScript types for the Cascade vault — IPC wire types (vault_list /
 *   vault_read / vault_write / vault_watch) and memory aggregator layer types.
 * Inputs:  None — pure type definitions.
 * Outputs: VaultNode, VaultError, VaultChangedPayload (IPC);
 *          MemoryEntry, MemoryEntryKind (aggregator).
 * Constraints: No React, Tauri, or external imports. Strict TS (no any).
 * SPORT: MASTER-ENDPOINTS.md — vault IPC commands (T-P3-E06-01)
 *        MASTER-COMPONENTS.md — MemoryEntry type (T-P3-E06-11)
 */

// ── Vault IPC wire types (T-P3-E06-01) ───────────────────────────────────────
//
// Mirror the serde-serialised types in commands/vault.rs.
// All fields use camelCase (Tauri's rename_all = "camelCase").
//
// Usage:
//   import { invoke } from "@tauri-apps/api/core";
//   const tree = await invoke<VaultNode>("vault_list", { root: "~/.cascade" });
//   const text = await invoke<string>("vault_read", { root, path });
//   await invoke("vault_write", { root, path, content });
//   await invoke("vault_watch", { root });
//
// Event listening (vault_watch):
//   import { listen } from "@tauri-apps/api/event";
//   const unlisten = await listen<VaultChangedPayload>("vault://changed", (e) => {
//     console.log("changed:", e.payload.path);
//   });

/**
 * A single node in the vault directory tree returned by `vault_list`.
 *
 * Directories have `isDir = true` and a (possibly empty) `children` array.
 * Leaf `.md` files have `isDir = false` and an empty `children` array.
 */
export interface VaultNode {
  /** The file or directory name (last path component only). */
  name: string
  /**
   * Absolute path string — pass back to `vault_read`, `vault_write`, etc.
   * unchanged.
   */
  path: string
  /** `true` for directories, `false` for `.md` leaf files. */
  isDir: boolean
  /** Child nodes sorted name-ascending. Empty for file nodes. */
  children: VaultNode[]
}

/**
 * Discriminated union of errors returned by vault commands.
 *
 * Matches the `VaultError` enum in vault.rs (serialised with `#[serde(tag =
 * "kind", rename_all = "camelCase")]`).
 */
export type VaultError =
  | { kind: 'traversalDenied'; message: string }
  | { kind: 'io'; message: string }
  | { kind: 'watch'; message: string }
  | { kind: 'notFound'; message: string }

/**
 * Payload emitted on the `"vault://changed"` Tauri event.
 * Fired for every `.md` file creation, modification, or removal under `root`.
 */
export interface VaultChangedPayload {
  /** Absolute path of the changed file. */
  path: string
}

// ── vault_search types (T-P3-E06-09) ─────────────────────────────────────────

/**
 * A single ripgrep match returned by `vault_search`.
 *
 * Mirrors the `SearchMatch` struct in commands/vault.rs.
 * Fields use camelCase per Tauri's rename_all = "camelCase".
 *
 * Usage:
 *   const matches = await invoke<SearchMatch[]>("vault_search", { root, query });
 */
export interface SearchMatch {
  /** Absolute path to the `.md` file containing the match. */
  file: string
  /** 1-based line number of the match within `file`. */
  lineNo: number
  /** The matching line text (full, before UI truncation). */
  lineText: string
}

/**
 * Discriminated error kinds specific to vault_search.
 * A subset of VaultError — kept as a type alias for documentation clarity.
 */
export type VaultSearchError = VaultError

// ── Memory aggregator types (T-P3-E06-11) ────────────────────────────────────

/**
 * The kind of memory artifact a MemoryEntry represents.
 * - decisions: from {project}/.claude/memory/decisions.md
 * - lessons:   from {project}/.claude/memory/lessons.md
 * - patterns:  from {project}/.claude/memory/patterns.md
 * - ideas:     from {project}/.claude/ideas/{slug}.md
 * - inbox:     from {project}/.claude/inbox/{msg}.md
 */
export type MemoryEntryKind = 'decisions' | 'lessons' | 'patterns' | 'ideas' | 'inbox'

/**
 * A single normalised entry from the cross-project memory aggregator.
 * Aggregated from all registered project roots by buildMemoryIndex().
 *
 * SPORT: MASTER-COMPONENTS.md — MemoryEntry (T-P3-E06-11)
 */
export interface MemoryEntry {
  /** Absolute path to the project root that owns this entry. */
  project: string
  /** Classification of the source file. */
  type: MemoryEntryKind
  /** Section heading (## Title) or filename stem for idea/inbox files. */
  title: string
  /** First paragraph or first 3 lines of the section; "No excerpt" when empty. */
  excerpt: string
  /** Absolute path to the source file. */
  path: string
  /**
   * Last-modified time as a Unix epoch milliseconds integer.
   * Derived from filesystem mtime; falls back to 0 when unavailable.
   */
  mtime: number
}
