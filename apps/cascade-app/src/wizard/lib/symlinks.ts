/**
 * Purpose: Symlink generation for Cascade-managed tools, with Windows fallback.
 * Inputs: List of scoped tool configs (tier, tool_id, cascade_md_path).
 * Outputs: Created symlinks or file-copy fallback records.
 * Constraints: Relative symlinks only. Windows: attempt symlink first;
 *   on ACCESS_DENIED, offer Developer Mode prompt or file-copy fallback.
 *   Never overwrites user files without explicit confirmation.
 *   Respects dry_run flag — logs would-be operations without writing.
 * SPORT: E5-S16-01, S16-08 — symlink engine + Windows fallback
 */

import { invoke } from "@tauri-apps/api/core";
import { platform } from "@tauri-apps/plugin-os";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Maps tool_id to the sibling filename it expects (relative to CASCADE.md). */
const TOOL_SIBLING_FILES: Record<string, string[]> = {
  "claude-code": ["CLAUDE.md", "AGENTS.md"],
  opencode: ["AGENTS.md"],
  cursor: [".cursorrules"],
  aider: [".aider.conf.yml"],
  windsurf: [".windsurf"],
  antigravity: [".antigravity"],
  codex: ["CODEX.md"],
};

export type SymlinkMode = "symlink" | "copy";

export interface SymlinkPlan {
  tool_id: string;
  cascade_md_path: string; // absolute path to CASCADE.md
  target_filename: string; // sibling filename to create
  target_path: string; // absolute path of symlink/copy target
  mode: SymlinkMode;
}

export interface SymlinkResult {
  plan: SymlinkPlan;
  success: boolean;
  error?: string;
  /** On Windows file-copy fallback, true = daemon will re-copy on CASCADE.md changes */
  daemon_watch_registered?: boolean;
}

export interface WindowsSymlinkFallbackInfo {
  requires_developer_mode: boolean;
  settings_url: string;
  copy_fallback_available: boolean;
}

// ---------------------------------------------------------------------------
// Plan generation
// ---------------------------------------------------------------------------

/**
 * Purpose: Build the list of symlink operations for a given tool at a given scope.
 * Returns one SymlinkPlan per sibling file required by the tool.
 */
export function buildSymlinkPlans(
  toolId: string,
  cascadeMdPath: string,
  symlinkMode: SymlinkMode = "symlink"
): SymlinkPlan[] {
  const siblings = TOOL_SIBLING_FILES[toolId];
  if (!siblings) return [];

  const dir = cascadeMdPath.substring(0, cascadeMdPath.lastIndexOf("/"));

  return siblings.map((filename) => ({
    tool_id: toolId,
    cascade_md_path: cascadeMdPath,
    target_filename: filename,
    target_path: `${dir}/${filename}`,
    mode: symlinkMode,
  }));
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

/**
 * Purpose: Execute a symlink plan (or file copy on Windows fallback).
 * Handles ACCESS_DENIED on Windows by returning structured fallback info.
 */
export async function executeSymlinkPlan(
  plan: SymlinkPlan,
  dryRun: boolean
): Promise<SymlinkResult> {
  if (dryRun) {
    await invoke("wizard_dry_run_log", {
      operation: "SYMLINK",
      source: plan.cascade_md_path,
      destination: plan.target_path,
      payload: JSON.stringify(plan),
    });
    return { plan, success: true };
  }

  if (plan.mode === "copy") {
    return executeFileCopy(plan);
  }

  try {
    await invoke("symlink_create", {
      source: plan.cascade_md_path,
      target: plan.target_path,
    });
    return { plan, success: true };
  } catch (err) {
    const errStr = String(err);
    const isWindowsPrivilege = errStr.includes("ACCESS_DENIED") || errStr.includes("privilege");

    if (isWindowsPrivilege && (await platform()) === "windows") {
      // Return without throwing — caller shows Windows fallback dialog
      return { plan, success: false, error: "windows_privilege" };
    }

    return { plan, success: false, error: errStr };
  }
}

/**
 * Purpose: Copy CASCADE.md to target path (Windows file-copy fallback).
 * Registers target with daemon file-watcher for subsequent re-copies.
 */
async function executeFileCopy(plan: SymlinkPlan): Promise<SymlinkResult> {
  try {
    await invoke("file_copy_create", {
      source: plan.cascade_md_path,
      target: plan.target_path,
    });
    // Register with daemon so it re-copies on CASCADE.md changes
    await invoke("daemon_watch_register", {
      source: plan.cascade_md_path,
      target: plan.target_path,
    });
    return { plan, success: true, daemon_watch_registered: true };
  } catch (err) {
    return { plan, success: false, error: String(err) };
  }
}

// ---------------------------------------------------------------------------
// Windows helpers
// ---------------------------------------------------------------------------

/**
 * Purpose: Return info needed to show the Windows Developer Mode prompt.
 */
export function windowsFallbackInfo(): WindowsSymlinkFallbackInfo {
  return {
    requires_developer_mode: true,
    settings_url: "ms-settings:developers",
    copy_fallback_available: true,
  };
}

/**
 * Purpose: Open Windows Developer Mode settings page via shell.
 */
export async function openWindowsDeveloperSettings(): Promise<void> {
  await invoke("shell_open", { url: "ms-settings:developers" });
}

// ---------------------------------------------------------------------------
// Derived-file generation (non-symlink tools: Cursor, Aider)
// ---------------------------------------------------------------------------

/**
 * Purpose: Generate derived config files from CASCADE.md content.
 * Cursor: strip top-level markdown decorators, output plain text.
 * Aider: extract as YAML fragment.
 * Why not symlinks: these tools require specific non-markdown formats.
 */
export async function generateDerivedFile(
  toolId: "cursor" | "aider",
  cascadeMdContent: string,
  outputPath: string,
  dryRun: boolean
): Promise<void> {
  let derived: string;

  if (toolId === "cursor") {
    // Strip top-level markdown: headers (#/##/###), bold (**text**), tables
    // Preserve: inline code, bullets, fenced code blocks
    derived = cascadeMdContent
      .replace(/^#{1,3}\s+.+$/gm, "")
      .replace(/\*\*([^*]+)\*\*/g, "$1")
      .replace(/^\|.+\|$/gm, "")
      .replace(/\n{3,}/g, "\n\n")
      .trim();
  } else if (toolId === "aider") {
    // Minimal YAML fragment — Cascade-managed, points back to source
    derived = `# Generated by Cascade from CASCADE.md — do not edit manually\ncascade_managed: true\n`;
  } else {
    derived = cascadeMdContent;
  }

  if (dryRun) {
    await invoke("wizard_dry_run_log", {
      operation: "WRITE",
      source: "CASCADE.md",
      destination: outputPath,
      payload: derived.slice(0, 200) + (derived.length > 200 ? "..." : ""),
    });
    return;
  }

  await invoke("file_write", { path: outputPath, content: derived });
}

// ---------------------------------------------------------------------------
// Integrity check (called by daemon)
// ---------------------------------------------------------------------------

export interface IntegrityReport {
  path: string;
  is_symlink: boolean;
  target_exists: boolean;
  drift_detected: boolean;
}

/**
 * Purpose: Verify that a sibling file is still a symlink pointing to CASCADE.md.
 * Daemon calls this on startup and every 5 minutes.
 * Drift = file was replaced with a regular file (no longer a symlink).
 */
export async function checkSymlinkIntegrity(symlinkPath: string): Promise<IntegrityReport> {
  return invoke("symlink_integrity_check", { path: symlinkPath });
}
