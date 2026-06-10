/**
 * Purpose: TypeScript types for the Cascade Context Sessions feature.
 *   Mirrors the Rust ContextSession schema used by list_contexts / upsert_context IPC.
 * Inputs:  None — pure type definitions.
 * Outputs: Exported types consumed by ContextPanel, useContextSessions, ItemList.
 * Constraints: Must stay in sync with crates/cascade-types/src/context.rs (schema v1).
 *   Add fields by appending only. Never rename or remove without a versioning ticket.
 * SPORT: MASTER-COMPONENTS.md — ContextSession types — features/context/ — T-P3-E07-10
 */

// ── ContextItemType ───────────────────────────────────────────────────────────

/** The three supported kinds of context items. */
export type ContextItemType = 'file' | 'note' | 'url'

// ── ContextItem ───────────────────────────────────────────────────────────────

/**
 * A single pinned context item within a session.
 */
export interface ContextItem {
  /** Stable unique id for this item within the session. */
  id: string
  /** Item type discriminant. */
  item_type: ContextItemType
  /** For file items: absolute or repo-relative path. */
  path?: string
  /** For note items: full text content. */
  content?: string
  /** For URL items: the full URL string. */
  url?: string
  /** ISO 8601 creation timestamp. */
  created_at?: string
}

// ── ContextSession ────────────────────────────────────────────────────────────

/**
 * A named collection of pinned context items.
 * Stored as a YAML file under .cascade/contexts/<id>.yaml.
 */
export interface ContextSession {
  /** Kebab-case slug — used as filename and primary key. */
  id: string
  /** Human-readable session title. */
  title: string
  /** Ordered list of pinned items. */
  items: ContextItem[]
  /** ISO 8601 creation timestamp. */
  created_at?: string
  /** ISO 8601 last-modified timestamp. */
  updated_at?: string
}

// ── ContextMeta — lightweight list result ─────────────────────────────────────

/**
 * Lightweight session metadata returned by context_list IPC.
 * Does NOT include items — call context_get to load items for a session.
 * Mirrors crates/cascade-core/src/context_pack/types.rs ContextMeta.
 */
export interface ContextMeta {
  /** Session identifier (kebab-case slug). */
  id: string
  /** Human-readable title. */
  title: string
  /** Number of items currently in this session. */
  itemCount: number
}

// ── IPC arg shapes ────────────────────────────────────────────────────────────

/** context_list returns Vec<ContextMeta> directly (not wrapped). */
export type ListContextsResult = ContextMeta[]

/** context_get returns ContextSession directly (not wrapped). */
export type GetContextResult = ContextSession

/** Args for context_upsert IPC. */
export interface UpsertContextArgs {
  session: ContextSession
}

/** context_upsert returns unit () — no structured ack. */
export type UpsertContextAck = null | undefined

/** Args for context_delete IPC. */
export interface DeleteContextArgs {
  id: string
}

/** context_delete returns unit () — no structured ack. */
export type DeleteContextAck = null | undefined

// ── PackCodebase IPC ──────────────────────────────────────────────────────────

/** Args for pack_codebase IPC. */
export interface PackCodebaseArgs {
  /** Absolute path to the root directory to pack. */
  root: string
  /** Glob patterns to exclude (one per entry). */
  excludes?: string[]
}

/** Success result from pack_codebase IPC. */
export interface PackCodebaseResult {
  /** Absolute path to the generated bundle file. */
  output_path: string
  /** Byte size of the bundle. */
  size_bytes: number
  /** Number of files included. */
  file_count: number
}
