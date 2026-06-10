// Scanner — global legacy home discovery.
//
// Purpose: Discovers legacy AI tool home directories in standard global paths
//   (~/.claude/, ~/.opencode/, ~/.codex/, etc.) and returns a structured
//   LegacyToolHome for each tool that has files present.
//
// Inputs:  tool_ids — slice of ToolId variants to scan; respects filter.
// Outputs: Vec<LegacyToolHome> — one entry per tool with ≥1 file discovered.
//
// Constraints:
//   - No async: synchronous scan must complete in <2s for typical ~/.claude/.
//   - 50 MB total cap per tool to prevent runaway scans of large binary dirs.
//   - Missing paths are silently skipped — never an error.
//   - Permission-denied or unreadable entries are silently skipped.
//   - Symlinks are not followed (follow_links = false equivalent).
//   - No writes to disk, no side effects.
//
// SPORT: MASTER-COMPONENTS.md — scan_global_homes Tauri command — scanner/global.rs
// Task: T-P3-E03-10

use std::fs;
use std::path::{Path, PathBuf};

use crate::scanner::types::{FileKind, LegacyFile, LegacyToolHome, ToolId};

/// Maximum total bytes scanned per tool before we stop collecting files.
const CAP_BYTES: u64 = 50 * 1024 * 1024; // 50 MB

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/// Scan the standard global home directories for each `ToolId` in `tool_ids`.
///
/// Returns one [`LegacyToolHome`] per tool that has at least one file present.
/// Tools whose paths do not exist, or whose paths are unreadable, are silently
/// omitted from the result.
///
/// # Errors
/// Returns `Err(String)` only on unexpected OS-level failures that are not
/// simply a missing or unreadable directory.
pub fn scan_global(tool_ids: &[ToolId]) -> Result<Vec<LegacyToolHome>, String> {
    let home = home_dir().ok_or_else(|| "could not determine home directory".to_string())?;

    let mut results: Vec<LegacyToolHome> = Vec::new();

    for tool_id in tool_ids {
        let paths = global_paths_for(tool_id, &home);

        // Collect files across all candidate paths for this tool.
        let mut all_files: Vec<LegacyFile> = Vec::new();
        let mut scanned_paths: Vec<String> = Vec::new();
        let mut total_bytes: u64 = 0;

        for path in &paths {
            if !path.exists() {
                continue;
            }

            // Resolve symlinks to detect loops: if the canonical path differs
            // from the given path, we could follow a symlink — skip.
            // We simply use metadata without following symlinks for the dir itself.
            match fs::symlink_metadata(path) {
                Err(_) => continue, // unreadable — skip silently
                Ok(meta) if meta.file_type().is_symlink() => continue, // skip symlinked homes
                Ok(_) => {}
            }

            let path_str = path.to_string_lossy().to_string();
            scanned_paths.push(path_str);

            // Shallow walk: read_dir gives one level only (non-recursive per spec).
            let entries = match fs::read_dir(path) {
                Ok(e) => e,
                Err(_) => continue, // permission denied or gone — skip
            };

            for entry in entries {
                let entry = match entry {
                    Ok(e) => e,
                    Err(_) => continue, // unreadable entry — skip
                };

                let entry_path = entry.path();

                // Do not follow symlinks into entries.
                let meta = match fs::symlink_metadata(&entry_path) {
                    Ok(m) => m,
                    Err(_) => continue,
                };

                if !meta.is_file() {
                    continue; // only files at this depth
                }

                let size = meta.len();

                // Enforce 50 MB cap.
                if total_bytes + size > CAP_BYTES {
                    break;
                }

                total_bytes += size;

                let kind = classify(&entry_path);
                all_files.push(LegacyFile {
                    path: entry_path.to_string_lossy().to_string(),
                    size_bytes: size,
                    kind,
                });
            }
        }

        if all_files.is_empty() {
            continue; // tool not present — omit from results
        }

        let total_files = all_files.len() as u32;
        let total_size_bytes: u64 = all_files.iter().map(|f| f.size_bytes).sum();

        results.push(LegacyToolHome {
            tool_id: tool_id.clone(),
            global_paths: scanned_paths,
            per_project_paths: Vec::new(), // per-project scan is T-P3-E03-11
            total_files,
            total_size_bytes,
        });
    }

    Ok(results)
}

// ---------------------------------------------------------------------------
// Path resolution
// ---------------------------------------------------------------------------

/// Returns the candidate global paths for a given tool under `home`.
fn global_paths_for(tool_id: &ToolId, home: &Path) -> Vec<PathBuf> {
    match tool_id {
        ToolId::ClaudeCode => vec![home.join(".claude")],
        ToolId::Opencode => vec![
            home.join(".opencode"),
            home.join(".config").join("opencode"),
        ],
        ToolId::Codex => vec![home.join(".codex")],
        ToolId::Cursor => vec![home.join(".cursor")],
        ToolId::Aider => vec![home.join(".aider")],
        ToolId::Windsurf => vec![home.join(".windsurf")],
        ToolId::Antigravity => vec![home.join(".antigravity")],
    }
}

// ---------------------------------------------------------------------------
// File classification
// ---------------------------------------------------------------------------

/// Classify a file by its name into a [`FileKind`].
///
/// Heuristics (first match wins):
/// - `CLAUDE.md`, `AGENTS.md`, `CASCADE.md`, `.cascade` → `Instructions`
/// - Parent directory named `memory`                    → `Memory`
/// - Parent directory named `docs`                      → `Docs`
/// - Parent directory named `tasks`                     → `Tasks`
/// - Parent directory named `ideas`                     → `Ideas`
/// - Everything else                                    → `Other`
fn classify(path: &Path) -> FileKind {
    let file_name = path.file_name().and_then(|n| n.to_str()).unwrap_or("");

    let parent_name = path
        .parent()
        .and_then(|p| p.file_name())
        .and_then(|n| n.to_str())
        .unwrap_or("");

    // Instruction files by name.
    if matches!(
        file_name,
        "CLAUDE.md" | "AGENTS.md" | "CASCADE.md" | ".cascade"
    ) {
        return FileKind::Instructions;
    }

    // Directory-based classification.
    match parent_name {
        "memory" => FileKind::Memory,
        "docs" => FileKind::Docs,
        "tasks" => FileKind::Tasks,
        "ideas" => FileKind::Ideas,
        _ => FileKind::Other,
    }
}

// ---------------------------------------------------------------------------
// Home directory resolution (std only — no `dirs` crate required at test time)
// ---------------------------------------------------------------------------

fn home_dir() -> Option<PathBuf> {
    // Use HOME env var first (allows tests to override), then std fallback.
    if let Ok(h) = std::env::var("HOME") {
        if !h.is_empty() {
            return Some(PathBuf::from(h));
        }
    }
    #[allow(deprecated)] // std::env::home_dir is deprecated but available
    std::env::home_dir()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs::{self, File};
    use std::io::Write;

    #[cfg(unix)]
    use std::os::unix::fs::PermissionsExt;

    use serial_test::serial;

    // -----------------------------------------------------------------------
    // Helper: build a fake tool home under `base`
    // -----------------------------------------------------------------------

    fn make_tool_home(base: &Path, dot_dir: &str) -> PathBuf {
        let dir = base.join(dot_dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn write_file(dir: &Path, name: &str, content: &[u8]) -> PathBuf {
        let p = dir.join(name);
        let mut f = File::create(&p).unwrap();
        f.write_all(content).unwrap();
        p
    }

    // -----------------------------------------------------------------------
    // Test: detection + file count for 3 tools
    // -----------------------------------------------------------------------

    #[test]
    #[serial(global_env)]
    fn detects_three_tools_and_counts_files() {
        let tmp = tempfile::tempdir().unwrap();
        let home = tmp.path();

        // Claude Code: 2 files
        let cc_dir = make_tool_home(home, ".claude");
        write_file(&cc_dir, "CLAUDE.md", b"instructions");
        write_file(&cc_dir, "settings.json", b"{}");

        // Opencode: 1 file
        let oc_dir = make_tool_home(home, ".opencode");
        write_file(&oc_dir, "config.json", b"{}");

        // Codex: 3 files
        let cx_dir = make_tool_home(home, ".codex");
        write_file(&cx_dir, "AGENTS.md", b"agents");
        write_file(&cx_dir, "foo.md", b"foo");
        write_file(&cx_dir, "bar.md", b"bar");

        std::env::set_var("HOME", home.to_str().unwrap());

        let all_tools = vec![
            ToolId::ClaudeCode,
            ToolId::Opencode,
            ToolId::Codex,
            ToolId::Cursor, // not present — should be absent from results
            ToolId::Aider,
            ToolId::Windsurf,
            ToolId::Antigravity,
        ];

        let result = scan_global(&all_tools).expect("scan should succeed");

        // Only 3 tools present
        assert_eq!(result.len(), 3, "expected 3 tools, got {}", result.len());

        let cc = result
            .iter()
            .find(|h| h.tool_id == ToolId::ClaudeCode)
            .unwrap();
        assert_eq!(cc.total_files, 2, "ClaudeCode: expected 2 files");

        let oc = result
            .iter()
            .find(|h| h.tool_id == ToolId::Opencode)
            .unwrap();
        assert_eq!(oc.total_files, 1, "Opencode: expected 1 file");

        let cx = result.iter().find(|h| h.tool_id == ToolId::Codex).unwrap();
        assert_eq!(cx.total_files, 3, "Codex: expected 3 files");

        std::env::remove_var("HOME");
    }

    // -----------------------------------------------------------------------
    // Test: missing tool is absent from results
    // -----------------------------------------------------------------------

    #[test]
    #[serial(global_env)]
    fn missing_tool_is_absent() {
        let tmp = tempfile::tempdir().unwrap();
        let home = tmp.path();

        // Only ClaudeCode exists
        let cc_dir = make_tool_home(home, ".claude");
        write_file(&cc_dir, "CLAUDE.md", b"hi");

        std::env::set_var("HOME", home.to_str().unwrap());

        let result = scan_global(&[
            ToolId::ClaudeCode,
            ToolId::Cursor, // does not exist
        ])
        .expect("scan should succeed");

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].tool_id, ToolId::ClaudeCode);
        assert!(
            result.iter().all(|h| h.tool_id != ToolId::Cursor),
            "Cursor should not appear when its dir is absent"
        );

        std::env::remove_var("HOME");
    }

    // -----------------------------------------------------------------------
    // Test: unreadable directory is silently skipped (Unix only)
    // -----------------------------------------------------------------------

    #[test]
    #[serial(global_env)]
    #[cfg(unix)]
    fn unreadable_dir_is_skipped() {
        // Skip this test if running as root (chmod 000 doesn't restrict root).
        if unsafe { libc::getuid() } == 0 {
            return;
        }

        let tmp = tempfile::tempdir().unwrap();
        let home = tmp.path();

        // Create a readable tool dir (ClaudeCode) so we have a baseline result.
        let cc_dir = make_tool_home(home, ".claude");
        write_file(&cc_dir, "CLAUDE.md", b"hi");

        // Create an unreadable dir for Aider.
        let aider_dir = make_tool_home(home, ".aider");
        write_file(&aider_dir, "config.yaml", b"key: val");
        let perms = fs::Permissions::from_mode(0o000);
        fs::set_permissions(&aider_dir, perms).unwrap();

        std::env::set_var("HOME", home.to_str().unwrap());

        let result = scan_global(&[ToolId::ClaudeCode, ToolId::Aider])
            .expect("scan should not error on unreadable dir");

        // Restore permissions so tempdir cleanup succeeds.
        let perms = fs::Permissions::from_mode(0o755);
        let _ = fs::set_permissions(&aider_dir, perms);

        std::env::remove_var("HOME");

        // ClaudeCode readable → present; Aider unreadable → absent.
        assert!(
            result.iter().any(|h| h.tool_id == ToolId::ClaudeCode),
            "ClaudeCode should be detected"
        );
        assert!(
            result.iter().all(|h| h.tool_id != ToolId::Aider),
            "Aider unreadable dir should be silently skipped"
        );
    }

    // -----------------------------------------------------------------------
    // Test: file classification heuristics
    // -----------------------------------------------------------------------

    #[test]
    fn classify_instruction_files() {
        let base = PathBuf::from("/fake/.claude");
        assert_eq!(classify(&base.join("CLAUDE.md")), FileKind::Instructions);
        assert_eq!(classify(&base.join("AGENTS.md")), FileKind::Instructions);
        assert_eq!(classify(&base.join("CASCADE.md")), FileKind::Instructions);
    }

    #[test]
    fn classify_by_parent_dir() {
        let base = PathBuf::from("/fake/.claude");
        assert_eq!(
            classify(&base.join("memory").join("foo.md")),
            FileKind::Memory
        );
        assert_eq!(classify(&base.join("docs").join("arch.md")), FileKind::Docs);
        assert_eq!(
            classify(&base.join("tasks").join("queue.md")),
            FileKind::Tasks
        );
        assert_eq!(
            classify(&base.join("ideas").join("idea.md")),
            FileKind::Ideas
        );
        assert_eq!(classify(&base.join("settings.json")), FileKind::Other);
    }

    // -----------------------------------------------------------------------
    // Test: tool_ids filter is respected
    // -----------------------------------------------------------------------

    #[test]
    #[serial(global_env)]
    fn filter_respects_tool_ids() {
        let tmp = tempfile::tempdir().unwrap();
        let home = tmp.path();

        // Both exist, but we only ask for ClaudeCode.
        let cc_dir = make_tool_home(home, ".claude");
        write_file(&cc_dir, "CLAUDE.md", b"cc");
        let aider_dir = make_tool_home(home, ".aider");
        write_file(&aider_dir, "settings.json", b"{}");

        std::env::set_var("HOME", home.to_str().unwrap());

        let result = scan_global(&[ToolId::ClaudeCode]).expect("scan should succeed");

        std::env::remove_var("HOME");

        assert_eq!(result.len(), 1);
        assert_eq!(result[0].tool_id, ToolId::ClaudeCode);
    }

    // -----------------------------------------------------------------------
    // Test: scan completes within 2 seconds on a populated dir
    // -----------------------------------------------------------------------

    #[test]
    #[serial(global_env)]
    fn scan_completes_under_2s() {
        let tmp = tempfile::tempdir().unwrap();
        let home = tmp.path();

        // Create a moderately populated dir (50 files).
        let cc_dir = make_tool_home(home, ".claude");
        for i in 0..50 {
            write_file(&cc_dir, &format!("file_{i}.md"), b"content");
        }

        std::env::set_var("HOME", home.to_str().unwrap());

        let start = std::time::Instant::now();
        let _ = scan_global(&[ToolId::ClaudeCode]).unwrap();
        let elapsed = start.elapsed();

        std::env::remove_var("HOME");

        assert!(
            elapsed.as_secs() < 2,
            "scan took {}ms, expected <2s",
            elapsed.as_millis()
        );
    }
}
