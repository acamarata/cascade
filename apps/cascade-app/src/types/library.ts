/**
 * Purpose: TypeScript types for the Cascade Library IPC layer.
 *   Mirrors the Rust LibraryItem schema from cascade-core::library::types exactly.
 *   Wire format is snake_case (Rust serde(rename_all = "snake_case") over Tauri IPC).
 * Inputs:  Derived from crates/cascade-core/src/library/types.rs (schema v1).
 * Outputs: Exported types consumed by all library feature components and hooks.
 * Constraints:
 *   - Field names match Rust snake_case wire format.
 *   - Add fields by appending only. Never rename or remove without a versioning ticket.
 *   - HarnessTarget values match the Rust harness_targets Vec<String> convention.
 * SPORT: MASTER-LIBS.md — library types, src/types/library.ts
 */

/** The four content kinds in the library. */
export type LibraryItemKind = 'prompt' | 'agent' | 'persona' | 'slash-command'

/** AI harness targets that a library item can be associated with. */
export type HarnessTarget = 'CC' | 'OC' | 'Codex'

/**
 * A single library item — prompt, agent, persona, or slash-command.
 *
 * Matches the Rust LibraryItem struct (cascade-core::library::types).
 * Field names are snake_case to match the Tauri IPC wire format.
 */
export interface LibraryItem {
  /** URL-safe slug — unique within the item's type directory. */
  id: string
  /** Human-readable display title. Required. */
  title: string
  /** Short description shown in cards. Optional. */
  description: string
  /** Content kind (corresponds to Rust item_type, serialised as "type" on disk). */
  kind: LibraryItemKind
  /** Primary instruction / prompt text. Required. */
  content: string
  /** Taxonomy tags (empty list allowed). */
  tags: string[]
  /** Harness targets — subset of ["CC", "OC", "Codex"]. */
  harness_targets: HarnessTarget[]
  /** Semantic version string, e.g. "1.0.0". Optional. */
  version?: string
  /** Persona: top-level system prompt injected before the persona traits. */
  system_prompt?: string
  /** Slash-command: the trigger string (e.g. "/summarize"). */
  trigger?: string
  /** Slash-command: argument description (e.g. "<file> [--verbose]"). */
  args_description?: string
  /** RFC 3339 creation timestamp (set by daemon on first upsert). */
  created_at?: string
  /** RFC 3339 last-modified timestamp (set by daemon on every upsert). */
  updated_at?: string
}

/** Result returned by list_library_items. */
export interface ListLibraryItemsResult {
  items: LibraryItem[]
}

/** Result returned by get_library_item. */
export interface GetLibraryItemResult {
  item: LibraryItem
}

/** Args for upsert_library_item. */
export interface UpsertLibraryItemArgs {
  item: LibraryItem
}

/** Acknowledgement returned by upsert_library_item. */
export interface UpsertLibraryItemAck {
  /** Slug of the upserted item. */
  id: string
  /** Absolute path to the written YAML file. */
  path: string
}

/** Args for delete_library_item. */
export interface DeleteLibraryItemArgs {
  id: string
  kind: LibraryItemKind
}
