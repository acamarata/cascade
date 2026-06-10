/**
 * Scanner types — shared type definitions for legacy tool home discovery.
 *
 * Purpose: Defines the TypeScript counterparts to the Rust scanner structs in
 *   src-tauri/src/scanner/types.rs. All fields use camelCase to match
 *   serde(rename_all = "camelCase") on the Rust side.
 *
 * Inputs:  None (type declarations only).
 * Outputs: Exported types consumed by scanner commands and the onboarding UI.
 *
 * Constraints:
 *   - ToolId variants must match the Rust ToolId enum exactly (same set of 7).
 *   - String values use kebab-case to match serde(rename_all = "kebab-case") on
 *     the Rust ToolId enum — JSON round-trip must be transparent.
 *   - Field names on interfaces must be camelCase to match Rust
 *     serde(rename_all = "camelCase").
 *   - No logic — types only.
 *
 * SPORT: MASTER-COMPONENTS.md — LegacyToolHome / ScanResult types — scanner/types.ts
 * Task: T-P3-E03-09
 */

// ---------------------------------------------------------------------------
// ToolId — the 7 supported AI coding tool identifiers
// ---------------------------------------------------------------------------

/**
 * The AI coding tools that Cascade knows how to scan and manage.
 * Values are kebab-case strings to match Rust `serde(rename_all = "kebab-case")`.
 */
export type ToolId =
  | 'claude-code'
  | 'opencode'
  | 'codex'
  | 'cursor'
  | 'aider'
  | 'windsurf'
  | 'antigravity'

/** All 7 ToolId values as a const array for runtime iteration. */
export const TOOL_IDS: readonly ToolId[] = [
  'claude-code',
  'opencode',
  'codex',
  'cursor',
  'aider',
  'windsurf',
  'antigravity',
] as const

// ---------------------------------------------------------------------------
// LegacyFile — a single file discovered inside a tool home
// ---------------------------------------------------------------------------

/**
 * Classification of a legacy file by its semantic role.
 * Matches the Rust FileKind enum serialized as a string.
 */
export type FileKind = 'instructions' | 'memory' | 'docs' | 'tasks' | 'ideas' | 'other'

/**
 * A single file found inside a legacy tool home.
 *
 * @param path       - Absolute filesystem path to the file.
 * @param sizeBytes  - File size in bytes.
 * @param kind       - Semantic classification of the file's role.
 */
export interface LegacyFile {
  path: string
  sizeBytes: number
  kind: FileKind
}

// ---------------------------------------------------------------------------
// LegacyToolHome — aggregated info for one tool's home directory/-ies
// ---------------------------------------------------------------------------

/**
 * All legacy files found for a single AI tool, across its global and/or
 * per-project locations.
 *
 * @param toolId          - Which tool this record belongs to.
 * @param globalPaths     - Global config directories that were scanned (may be empty).
 * @param perProjectPaths - Per-project directories containing tool files (may be empty).
 * @param totalFiles      - Total number of files discovered across all paths.
 * @param totalSizeBytes  - Aggregate size of all discovered files in bytes.
 */
export interface LegacyToolHome {
  toolId: ToolId
  globalPaths: string[]
  perProjectPaths: string[]
  totalFiles: number
  totalSizeBytes: number
}

// ---------------------------------------------------------------------------
// ScanResult — top-level result returned by scan commands
// ---------------------------------------------------------------------------

/**
 * The result of a full scanner pass (global or dev-tree).
 *
 * @param homes       - One LegacyToolHome per tool that had at least one file.
 * @param scannedAt   - ISO-8601 timestamp when the scan completed.
 * @param devTreeRoot - The dev-tree root that was scanned, or null for global scans.
 */
export interface ScanResult {
  homes: LegacyToolHome[]
  scannedAt: string
  devTreeRoot: string | null
}
