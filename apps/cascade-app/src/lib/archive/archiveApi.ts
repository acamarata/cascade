/**
 * Purpose: TypeScript bindings for archive/restore Tauri IPC commands.
 *   Provides typed wrappers for archive_legacy_tools, read_archive_manifest,
 *   list_archived_tools, archive_preflight, and restore_tool.
 *
 * Inputs:  Tauri invoke API, types from ./types.ts.
 * Outputs: Typed async functions; all IPC errors surfaced as thrown strings.
 *
 * Constraints:
 *   - No `any` — all invoke calls are fully typed via generics.
 *   - read_archive_manifest returns null (not undefined) when no manifest exists,
 *     matching the Rust `Option<ArchiveManifest>` → None → null JSON mapping.
 *
 * SPORT: MASTER-COMPONENTS.md — archiveApi module — archive/archiveApi.ts
 * Task: T-P3-E03-17
 */

import { invoke } from '@tauri-apps/api/core'

import type { ArchiveManifest, ArchiveValidation, RestoreResult, ToolArchive } from './types'

// ---------------------------------------------------------------------------
// T-P3-E03-16: archive_legacy_tools
// ---------------------------------------------------------------------------

/**
 * Archive a tool's legacy home directory.
 *
 * Moves `~/.{tool}` to `~/.cascade/legacy/{toolId}/` and writes the manifest.
 * Idempotent: returns the existing `ToolArchive` if the tool is already archived.
 *
 * @param toolId - Kebab-case tool identifier, e.g. `'claude-code'`.
 * @throws {string} If the move fails and rollback completes.
 */
export async function archiveLegacyTools(toolId: string): Promise<ToolArchive> {
  return invoke<ToolArchive>('archive_legacy_tools', { toolId })
}

// ---------------------------------------------------------------------------
// T-P3-E03-17: read_archive_manifest / list_archived_tools
// ---------------------------------------------------------------------------

/**
 * Read the archive manifest from `~/.cascade/legacy/manifest.json`.
 *
 * - Returns `null` when no manifest exists (no tools archived yet).
 * - Returns `ArchiveManifest` when the manifest is valid.
 * - Throws when the file exists but contains corrupt/invalid JSON.
 */
export async function readArchiveManifest(): Promise<ArchiveManifest | null> {
  return invoke<ArchiveManifest | null>('read_archive_manifest')
}

/**
 * Convenience wrapper: returns `manifest.tools` or `[]` if no manifest exists.
 *
 * Never throws for a missing manifest — only for corrupt JSON.
 */
export async function listArchivedTools(): Promise<ToolArchive[]> {
  return invoke<ToolArchive[]>('list_archived_tools')
}

// ---------------------------------------------------------------------------
// T-P3-E03-18: archive_preflight
// ---------------------------------------------------------------------------

/**
 * Run pre-flight validation before archiving a tool.
 *
 * Returns an `ArchiveValidation` with `ok: true` when all checks pass.
 * Fatal issues (InsufficientDisk, PermissionDenied) require resolution;
 * non-fatal issues are warnings the user may acknowledge and proceed through.
 *
 * @param toolId - Kebab-case tool identifier.
 */
export async function archivePreflight(toolId: string): Promise<ArchiveValidation> {
  return invoke<ArchiveValidation>('archive_preflight', { toolId })
}

// ---------------------------------------------------------------------------
// T-P3-E03-19: restore_tool
// ---------------------------------------------------------------------------

/**
 * Restore an archived tool's files to their original paths.
 *
 * @param toolId           - Kebab-case tool identifier.
 * @param overwriteExisting - If `true`, back up conflicting files before overwrite.
 *                           Defaults to `false` (skip conflicts, report them).
 * @returns `RestoreResult` with counts of restored, skipped, and errored files.
 */
export async function restoreTool(
  toolId: string,
  overwriteExisting = false
): Promise<RestoreResult> {
  return invoke<RestoreResult>('restore_tool', { toolId, overwriteExisting })
}
