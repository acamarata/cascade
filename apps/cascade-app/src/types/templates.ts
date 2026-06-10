/**
 * Template IPC wire types — mirrors commands/templates.rs on the Rust side.
 *
 * All fields use camelCase (Tauri's automatic rename_all = "camelCase" applies
 * to the serialised JSON; serde on the Rust side handles the mapping).
 *
 * Usage:
 *   import { invoke } from "@tauri-apps/api/core";
 *   const entries = await invoke<TemplateEntryIpc[]>("template_list", { filter: {} });
 *
 * SPORT: MASTER-COMMANDS.md — template IPC types (T-P3-E05-15, T-P3-E05-17)
 */

// ── Tier ──────────────────────────────────────────────────────────────────────

/** The cascade tier a template targets. */
export type TemplateTier = 'gci' | 'pci' | 'apc' | 'ppc' | 'prc' | 'pac' | 'any'

// ── template_list ─────────────────────────────────────────────────────────────

/** Filter parameters for `template_list`. All fields optional. */
export interface TemplateFilterIpc {
  /** Match templates whose tier equals this value (e.g. `"gci"`, `"prc"`). */
  tier?: TemplateTier | string
  /** Match templates that include this stack tag (e.g. `"rust"`, `"tauri"`). */
  stack?: string
  /** Match templates that include this project-shape tag (e.g. `"cli"`, `"lib"`). */
  shape?: string
}

/**
 * One template entry returned by `template_list`.
 *
 * Mirrors `cascade_types::TemplateManifest` + body string.
 * The `body` field contains the raw Markdown suitable for preview rendering.
 */
export interface TemplateEntryIpc {
  id: string
  version: string
  tier: TemplateTier | string
  stacks: string[]
  projectShapes: string[]
  description: string
  extends?: string
  minCascadeVersion?: string
  /** Raw Markdown body — used for preview rendering on the frontend. */
  body: string
}

// ── template_apply ────────────────────────────────────────────────────────────

/** Outcome of `template_apply`. */
export interface ApplyResultIpc {
  /** Section headings added to the target. */
  applied: string[]
  /** Section headings that conflicted (not overwritten unless `force = true`). */
  conflicts: string[]
  /** Section headings already in the target with identical content (skipped). */
  skipped: string[]
}

// ── template_diff ─────────────────────────────────────────────────────────────

/** Outcome of `template_diff`. */
export interface DiffResultIpc {
  /** Sections present in the template but absent from the target. */
  added: string[]
  /** Sections present in both but with differing content. */
  conflicts: string[]
  /** Sections already matching the template exactly. */
  matching: string[]
}

// ── template_upgrade ──────────────────────────────────────────────────────────

/** Outcome of `template_upgrade`. */
export interface UpgradeResultIpc {
  /** Sections overwritten with the new template version. */
  upgraded: string[]
  /** Sections newly appended from the new template version. */
  added: string[]
  /**
   * Sections from the old version no longer in the new version.
   * Preserved in target with a deprecation notice comment.
   */
  deprecated: string[]
}
