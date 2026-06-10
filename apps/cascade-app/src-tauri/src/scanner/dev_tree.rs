// Scanner — per-project dev-tree discovery.
//
// Purpose: Walks a user-chosen dev tree root (default ~/Sites/) up to depth 3
//   to discover per-project legacy tool files (.claude/CLAUDE.md, .cursorrules,
//   .aider.md, etc.) and returns a Vec<LegacyToolHome> aggregated per tool.
//
// Inputs:  root: &str — absolute path to the dev tree root to scan.
// Outputs: Vec<LegacyToolHome> — one entry per ToolId with ≥1 per_project_path.
//
// Constraints:
//   - max_depth(3): root = depth 0; project dirs at depth 1–3.
//   - Skip heavy dirs: node_modules, target, .git, dist, build, .next, __pycache__.
//   - Skip hidden dirs except: .claude, .opencode, .cursor, .aider, .windsurf.
//   - follow_links(false): symlink loops cannot occur.
//   - Short-circuit after 500 project dirs found.
//   - Returns empty Vec (not error) if root does not exist.
//   - No writes to disk, no side effects.
//   - Synchronous; must complete in <10s for ~/Sites/ with ≤500 subdirs.
//
// Strategy:
//   Walk the tree. For each *directory* entry, check whether it contains any
//   of the known per-project legacy file/dir patterns. If it does, record the
//   project dir path in the corresponding ToolId bucket.  Aggregate at the end
//   into one LegacyToolHome per tool that had ≥1 project hit.
//
// SPORT: MASTER-COMPONENTS.md — scan_dev_tree Tauri command — scanner/dev_tree.rs
// Task: T-P3-E03-11

use std::collections::{HashMap, HashSet};
use std::path::Path;

use walkdir::WalkDir;

use crate::scanner::types::{LegacyToolHome, ToolId};

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/// Maximum directory depth to walk. Root itself is depth 0, so project dirs
/// at depth 1, 2, and 3 are all examined.
const MAX_DEPTH: usize = 3;

/// Stop accumulating after this many distinct project directories.
const MAX_PROJECT_DIRS: usize = 500;

/// Directory names that are always skipped (never descended into).
const SKIP_DIRS: &[&str] = &[
    "node_modules",
    ".git",
    "target",
    "dist",
    "build",
    ".next",
    "__pycache__",
];

/// Hidden directory names that ARE allowed (not skipped despite leading dot).
const ALLOWED_HIDDEN_DIRS: &[&str] = &[".claude", ".opencode", ".cursor", ".aider", ".windsurf"];

// ---------------------------------------------------------------------------
// Per-tool file/dir patterns (checked inside a candidate project directory)
// ---------------------------------------------------------------------------
//
// Each variant holds a list of relative paths (files or directories) that, if
// present under a project root, indicate that tool is used there.  The check
// is existence-only — we record the *project directory*, not the individual
// file, in per_project_paths.

struct ToolPattern {
    tool_id: ToolId,
    /// Relative path fragments to test (any match → project is recorded).
    signals: &'static [&'static str],
}

/// Returns the static detection patterns for all 7 tools.
fn tool_patterns() -> &'static [ToolPattern] {
    // Using a static slice of ToolPattern structs defined inline.  The slice
    // is &'static because the data is encoded in the binary.
    static PATTERNS: std::sync::OnceLock<Vec<ToolPattern>> = std::sync::OnceLock::new();
    // Safety: OnceLock<Vec<_>> cannot be &'static without boxing. Use a helper.
    // Instead, define as a plain fn returning a temporary vec and let callers
    // handle it — but that conflicts with the intended design.
    //
    // Compromise: return a &'static [ToolPattern] via a const-friendly approach.
    // Since ToolPattern contains &'static slices, we can use a static array if
    // ToolId implements Copy.  ToolId is Clone but not Copy — use OnceLock.
    PATTERNS.get_or_init(|| {
        vec![
            ToolPattern {
                tool_id: ToolId::ClaudeCode,
                signals: &[
                    ".claude/CLAUDE.md",
                    ".claude/AGENTS.md",
                    ".claude/memory",
                    ".claude/docs",
                    ".claude",
                ],
            },
            ToolPattern {
                tool_id: ToolId::Opencode,
                signals: &[".opencode", "AGENTS.md", "opencode.json"],
            },
            ToolPattern {
                tool_id: ToolId::Cursor,
                signals: &[".cursorrules", ".cursor/rules"],
            },
            ToolPattern {
                tool_id: ToolId::Aider,
                signals: &[".aider.md", ".aider.conf.yml"],
            },
            ToolPattern {
                tool_id: ToolId::Windsurf,
                signals: &[".windsurf/rules", ".windsurf"],
            },
            ToolPattern {
                tool_id: ToolId::Codex,
                signals: &[".codex"],
            },
            ToolPattern {
                tool_id: ToolId::Antigravity,
                signals: &[".antigravity"],
            },
        ]
    })
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/// Walk `root` up to depth 3 and return per-project legacy tool homes.
///
/// Each returned [`LegacyToolHome`] has `per_project_paths` populated with the
/// distinct project directory paths where that tool's config was found.
/// `global_paths` and `total_files`/`total_size_bytes` are left at defaults
/// (empty / 0) — global scanning is handled by the separate `scan_global` fn.
///
/// Returns an empty [`Vec`] (not an error) if `root` does not exist.
///
/// # Errors
/// Returns `Err(String)` only on unexpected OS-level failures unrelated to
/// missing or permission-denied directories.
pub fn scan_dev_tree(root: &str) -> Result<Vec<LegacyToolHome>, String> {
    let root_path = Path::new(root);

    // Non-existent root → empty result (per spec).
    if !root_path.exists() {
        return Ok(Vec::new());
    }

    // tool_id → set of project dir paths that have a signal for that tool.
    let mut hits: HashMap<ToolId, HashSet<String>> = HashMap::new();
    let patterns = tool_patterns();

    let mut project_dirs_seen = 0usize;

    let walker = WalkDir::new(root_path)
        .max_depth(MAX_DEPTH)
        .follow_links(false)
        .into_iter()
        .filter_entry(should_descend);

    for entry in walker {
        let entry = match entry {
            Ok(e) => e,
            // Permission-denied or other IO error on a single entry: skip it.
            Err(_) => continue,
        };

        // We only inspect directories as candidate project roots.
        if !entry.file_type().is_dir() {
            continue;
        }

        // The root itself is not a project dir.
        if entry.depth() == 0 {
            continue;
        }

        // Short-circuit guard.
        if project_dirs_seen >= MAX_PROJECT_DIRS {
            break;
        }
        project_dirs_seen += 1;

        let dir_path = entry.path();

        // Check each tool's signals against this directory.
        for pattern in patterns.iter() {
            for signal in pattern.signals.iter() {
                if dir_path.join(signal).exists() {
                    hits.entry(pattern.tool_id.clone())
                        .or_default()
                        .insert(dir_path.to_string_lossy().into_owned());
                    break; // One signal match is enough for this tool+dir.
                }
            }
        }
    }

    // Convert hits map into Vec<LegacyToolHome>, one per tool with ≥1 hit.
    let mut result: Vec<LegacyToolHome> = hits
        .into_iter()
        .map(|(tool_id, paths)| {
            let mut sorted: Vec<String> = paths.into_iter().collect();
            sorted.sort();
            LegacyToolHome {
                tool_id,
                global_paths: Vec::new(),
                per_project_paths: sorted,
                total_files: 0,
                total_size_bytes: 0,
            }
        })
        .collect();

    // Stable sort by tool_id debug string for deterministic output.
    result.sort_by_key(|h| format!("{:?}", h.tool_id));

    Ok(result)
}

// ---------------------------------------------------------------------------
// Walkdir filter predicate
// ---------------------------------------------------------------------------

/// Returns `true` if walkdir should descend into / yield this entry.
///
/// Rules (applied in order):
/// 1. The root itself (depth 0) is always allowed — its name may be a hidden
///    temp dir (e.g., `.tmpXXX` on macOS) that we must still traverse.
/// 2. Files are always yielded (the outer loop only acts on dirs, but walkdir
///    needs to yield them for correct operation).
/// 3. Directories on the explicit skip list are never descended.
/// 4. Directories whose name starts with '.' are skipped UNLESS they are one
///    of the five allowed hidden dirs.
fn should_descend(entry: &walkdir::DirEntry) -> bool {
    // Rule 1: Always allow the root (depth 0).
    if entry.depth() == 0 {
        return true;
    }

    // Rule 2: Files are never filtered by this predicate.
    if !entry.file_type().is_dir() {
        return true;
    }

    let name = entry.file_name().to_string_lossy();

    // Rule 3: Explicit skip list (case-sensitive, exact match).
    if SKIP_DIRS.contains(&name.as_ref()) {
        return false;
    }

    // Rule 4: Hidden dir filter: skip hidden dirs not in the allowed list.
    if name.starts_with('.') && !ALLOWED_HIDDEN_DIRS.contains(&name.as_ref()) {
        return false;
    }

    true
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    /// Helper: create an empty file (and all parent dirs).
    fn touch(base: &Path, rel: &str) {
        let full = base.join(rel);
        if let Some(parent) = full.parent() {
            fs::create_dir_all(parent).unwrap();
        }
        fs::write(&full, b"").unwrap();
    }

    /// Helper: create a directory (all parents included).
    fn mkdir(base: &Path, rel: &str) {
        fs::create_dir_all(base.join(rel)).unwrap();
    }

    // -----------------------------------------------------------------------
    // T1: non-existent root returns empty vec (not error).
    // -----------------------------------------------------------------------
    #[test]
    fn non_existent_root_returns_empty() {
        let result = scan_dev_tree("/this/path/cannot/possibly/exist/9f3a2b1c");
        assert!(result.is_ok());
        assert!(result.unwrap().is_empty());
    }

    // -----------------------------------------------------------------------
    // T2: empty root (no tool signals) returns empty vec.
    // -----------------------------------------------------------------------
    #[test]
    fn empty_root_returns_empty() {
        let tmp = TempDir::new().unwrap();
        mkdir(tmp.path(), "proj-a");
        mkdir(tmp.path(), "proj-b");
        let result = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();
        assert!(result.is_empty(), "No tool signals → empty result");
    }

    // -----------------------------------------------------------------------
    // T3: depth anchoring — project at depth 3 IS found; depth 4 is NOT.
    // -----------------------------------------------------------------------
    #[test]
    fn depth_anchoring_depth_3_found_depth_4_not() {
        let tmp = TempDir::new().unwrap();

        // depth 3 project: root / a / b / proj3 — should be found.
        touch(tmp.path(), "a/b/proj3/.claude/CLAUDE.md");

        // depth 4 project: root / a / b / c / proj4 — must NOT be found.
        touch(tmp.path(), "a/b/c/proj4/.claude/CLAUDE.md");

        let result = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();

        let claude_home = result.iter().find(|h| h.tool_id == ToolId::ClaudeCode);
        assert!(claude_home.is_some(), "ClaudeCode must appear in result");
        let paths = &claude_home.unwrap().per_project_paths;

        let proj3_path = tmp.path().join("a/b/proj3").to_string_lossy().into_owned();
        let proj4_path = tmp
            .path()
            .join("a/b/c/proj4")
            .to_string_lossy()
            .into_owned();

        assert!(
            paths.contains(&proj3_path),
            "depth-3 project must be found; paths={paths:?}"
        );
        assert!(
            !paths.contains(&proj4_path),
            "depth-4 project must NOT be found; paths={paths:?}"
        );
    }

    // -----------------------------------------------------------------------
    // T4: node_modules decoy is never entered.
    // -----------------------------------------------------------------------
    #[test]
    fn node_modules_decoy_excluded() {
        let tmp = TempDir::new().unwrap();

        // Real project at depth 1.
        touch(tmp.path(), "real-proj/.claude/CLAUDE.md");

        // Decoy inside node_modules — must be ignored.
        touch(
            tmp.path(),
            "real-proj/node_modules/some-lib/.claude/CLAUDE.md",
        );

        let result = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();
        let claude_home = result
            .iter()
            .find(|h| h.tool_id == ToolId::ClaudeCode)
            .unwrap();

        // Only real-proj should appear; node_modules sub-path must not.
        for p in &claude_home.per_project_paths {
            assert!(
                !p.contains("node_modules"),
                "node_modules path leaked into results: {p}"
            );
        }
        let real_path = tmp.path().join("real-proj").to_string_lossy().into_owned();
        assert!(
            claude_home.per_project_paths.contains(&real_path),
            "real project must be present"
        );
    }

    // -----------------------------------------------------------------------
    // T5: multi-tool aggregation — one project with both .claude and .cursorrules.
    // -----------------------------------------------------------------------
    #[test]
    fn multi_tool_aggregation() {
        let tmp = TempDir::new().unwrap();

        // Project with both ClaudeCode and Cursor signals.
        touch(tmp.path(), "poly-proj/.claude/CLAUDE.md");
        touch(tmp.path(), "poly-proj/.cursorrules");

        let result = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();

        let claude = result.iter().find(|h| h.tool_id == ToolId::ClaudeCode);
        let cursor = result.iter().find(|h| h.tool_id == ToolId::Cursor);

        assert!(claude.is_some(), "ClaudeCode must be detected");
        assert!(cursor.is_some(), "Cursor must be detected");

        let poly_path = tmp.path().join("poly-proj").to_string_lossy().into_owned();
        assert!(claude.unwrap().per_project_paths.contains(&poly_path));
        assert!(cursor.unwrap().per_project_paths.contains(&poly_path));
    }

    // -----------------------------------------------------------------------
    // T6: .git and other skip dirs are not entered.
    // -----------------------------------------------------------------------
    #[test]
    fn skip_dirs_not_entered() {
        let tmp = TempDir::new().unwrap();

        // Legitimate project at depth 1.
        touch(tmp.path(), "good-proj/.aider.md");

        // Files inside skip dirs — must not appear.
        for skip in &["target", "dist", "build", ".next"] {
            touch(tmp.path(), &format!("good-proj/{skip}/.claude/CLAUDE.md"));
        }
        touch(tmp.path(), "good-proj/.git/.aider.md");

        let result = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();

        for home in &result {
            for p in &home.per_project_paths {
                for skip in &["target", "dist", "build", ".next", ".git"] {
                    assert!(
                        !p.contains(skip),
                        "skip dir '{skip}' leaked into per_project_paths: {p}"
                    );
                }
            }
        }

        // good-proj itself must appear under Aider.
        let aider = result.iter().find(|h| h.tool_id == ToolId::Aider);
        assert!(aider.is_some(), "Aider must be detected");
        let good_path = tmp.path().join("good-proj").to_string_lossy().into_owned();
        assert!(aider.unwrap().per_project_paths.contains(&good_path));
    }

    // -----------------------------------------------------------------------
    // T7: Opencode AGENTS.md signal detected.
    // -----------------------------------------------------------------------
    #[test]
    fn opencode_agents_md_detected() {
        let tmp = TempDir::new().unwrap();
        touch(tmp.path(), "oc-proj/AGENTS.md");
        let result = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();
        let opencode = result.iter().find(|h| h.tool_id == ToolId::Opencode);
        assert!(
            opencode.is_some(),
            "Opencode must be detected via AGENTS.md"
        );
        let oc_path = tmp.path().join("oc-proj").to_string_lossy().into_owned();
        assert!(opencode.unwrap().per_project_paths.contains(&oc_path));
    }

    // -----------------------------------------------------------------------
    // T8: Windsurf .windsurf dir signal detected.
    // -----------------------------------------------------------------------
    #[test]
    fn windsurf_dir_detected() {
        let tmp = TempDir::new().unwrap();
        mkdir(tmp.path(), "ws-proj/.windsurf");
        let result = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();
        let windsurf = result.iter().find(|h| h.tool_id == ToolId::Windsurf);
        assert!(windsurf.is_some(), "Windsurf must be detected");
        let ws_path = tmp.path().join("ws-proj").to_string_lossy().into_owned();
        assert!(windsurf.unwrap().per_project_paths.contains(&ws_path));
    }

    // -----------------------------------------------------------------------
    // T9: result is deterministic (sorted).
    // -----------------------------------------------------------------------
    #[test]
    fn result_is_deterministic() {
        let tmp = TempDir::new().unwrap();
        touch(tmp.path(), "alpha/.claude/CLAUDE.md");
        touch(tmp.path(), "beta/.claude/CLAUDE.md");
        touch(tmp.path(), "gamma/.cursorrules");

        let r1 = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();
        let r2 = scan_dev_tree(tmp.path().to_str().unwrap()).unwrap();

        let paths1: Vec<_> = r1.iter().flat_map(|h| h.per_project_paths.iter()).collect();
        let paths2: Vec<_> = r2.iter().flat_map(|h| h.per_project_paths.iter()).collect();
        assert_eq!(
            paths1, paths2,
            "scan_dev_tree must produce deterministic output"
        );
    }
}
