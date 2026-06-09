// Archive types — TypeScript counterparts to src-tauri/src/archive/types.rs.
//
// Purpose: Defines the manifest schema that records every file moved during
//   archive of a legacy tool home. The manifest is the sole source of truth
//   needed to fully restore a tool's original directory tree.
//
// Inputs:  None — type declarations only.
// Outputs: Types consumed by archiveApi.ts and Phase 7 wizard UI components.
//
// Constraints:
//   - All path fields are absolute strings (no `~/` expansion — resolved paths
//     only). Rust stores them as PathBuf; round-trip via String.
//   - Parity with src-tauri/src/archive/types.rs is MANDATORY. Field names are
//     camelCase on both sides (Rust uses `serde(rename_all = "camelCase")`).
//   - ArchiveManifest.version is a semver string ('1.0') for future migration.
//
// SPORT: MASTER-COMPONENTS.md — ArchiveManifest / ToolArchive types — archive/types.ts
// Task: T-P3-E03-15

// ---------------------------------------------------------------------------
// ArchivedFile — single file/dir entry moved during archive
// ---------------------------------------------------------------------------

/**
 * Records one file or directory that was moved during an archive operation.
 *
 * Both paths are absolute, fully resolved strings (no `~/` shorthand).
 */
export interface ArchivedFile {
  /** Absolute path where the file lived before archiving. */
  originalPath: string;
  /** Absolute path where the file now lives inside ~/.cascade/legacy/{toolId}/. */
  archivedPath: string;
  /** ISO 8601 timestamp of when the file was moved. */
  movedAt: string;
  /** File size in bytes at the time of archiving. */
  sizeBytes: number;
}

// ---------------------------------------------------------------------------
// ToolArchive — all files archived for one tool
// ---------------------------------------------------------------------------

/**
 * Records the complete archive of one AI coding tool's legacy home directory.
 */
export interface ToolArchive {
  /** Kebab-case tool identifier, e.g. 'claude-code', 'opencode'. */
  toolId: string;
  /** Absolute original root directory that was archived (e.g. ~/.claude). */
  originalRoot: string;
  /** Absolute destination root inside ~/.cascade/legacy/ (e.g. ~/.cascade/legacy/claude-code). */
  archiveRoot: string;
  /** ISO 8601 timestamp of when the archive operation completed. */
  archivedAt: string;
  /** Every file and directory moved during this archive operation. */
  files: ArchivedFile[];
}

// ---------------------------------------------------------------------------
// ArchiveManifest — the root manifest.json document
// ---------------------------------------------------------------------------

/**
 * Root document written to ~/.cascade/legacy/manifest.json.
 *
 * `version` is a semver string ('1.0') enabling forward-compatible schema
 * migration if the manifest format changes in a future release.
 */
export interface ArchiveManifest {
  /** Manifest schema version — currently '1.0'. */
  version: string;
  /** ISO 8601 timestamp of when this manifest was first created. */
  createdAt: string;
  /** All tool archives recorded in this manifest. One entry per archived tool. */
  tools: ToolArchive[];
}

// ---------------------------------------------------------------------------
// ArchiveValidation — pre-flight check result (T-P3-E03-18)
// ---------------------------------------------------------------------------

/** Fatal or non-fatal issue detected during pre-flight validation. */
export type ArchiveIssue =
  | { type: 'InsufficientDisk'; requiredBytes: number; availableBytes: number }
  | { type: 'DestinationExists'; path: string }
  | { type: 'PermissionDenied'; path: string }
  | { type: 'SourceNotFound'; path: string }
  | { type: 'ActiveProcessUsing'; toolName: string };

/**
 * Result of `validate_archive` pre-flight check.
 *
 * `ok` is true only when there are zero issues. Non-fatal issues
 * (DestinationExists, SourceNotFound, ActiveProcessUsing) allow the user
 * to proceed; fatal issues (InsufficientDisk, PermissionDenied) must be
 * resolved first.
 */
export interface ArchiveValidation {
  ok: boolean;
  issues: ArchiveIssue[];
}

// ---------------------------------------------------------------------------
// RestoreResult — outcome of a restore_tool operation (T-P3-E03-19)
// ---------------------------------------------------------------------------

/**
 * Summary result returned by `restore_tool`.
 */
export interface RestoreResult {
  /** Number of files successfully moved back to their original paths. */
  restoredFiles: number;
  /** Number of files skipped due to conflicts at the original path. */
  skippedConflicts: number;
  /** Error messages for any files that could not be moved or skipped cleanly. */
  errors: string[];
}
