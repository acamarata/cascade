// merge/reader.rs — Legacy content reader for the AI merge engine.
//
// Purpose: Reads the actual UTF-8 content of legacy instruction files from disk
//   for a given tool, preparing them as MergeSourceFileRaw objects for the AI merge.
//
// Inputs:  tool_id (String), paths (Vec<String>) — from a prior ScanResult.
// Outputs: Vec<MergeSourceFileRaw> or Err(String) if any path is stale/missing.
//
// Constraints:
//   - Files >1 MB are skipped (content: None), not errors.
//   - Binary files (null byte in first 512 bytes) are skipped (content: None).
//   - Total content per tool is capped at 50 000 characters (smallest-first inclusion).
//   - Missing paths (stale scan result) return Err with the missing path.
//   - Traversal outside $HOME is rejected with Err.
//   - Permission-denied → skip entry, do not abort.
//   - Read-only — no writes.
//
// SPORT: MASTER-COMPONENTS.md — read_legacy_content Tauri command — merge/reader.rs
// Task: T-P3-E03-24

use std::fs;
use std::path::{Path, PathBuf};

use crate::merge::types::MergeSourceFileRaw;
use crate::scanner::types::FileKind;

/// Maximum file size included in the AI context (1 MiB).
const MAX_FILE_BYTES: u64 = 1_048_576;

/// Maximum total characters per tool sent to the AI.
const MAX_TOTAL_CHARS: usize = 50_000;

/// Number of bytes to sniff for binary detection.
const BINARY_SNIFF_BYTES: usize = 512;

/// Derive the canonical FileKind from a file path.
///
/// Classifies based on path components and filename:
/// - `memory/` subtree or `MEMORY.md` → Memory
/// - `docs/` or `.github/wiki/` or `.github/docs/` → Docs
/// - `tasks/` or `.todo` → Tasks
/// - `ideas/` → Ideas
/// - `CLAUDE.md`, `AGENTS.md`, `rules/`, `settings.json` → Instructions
/// - Anything else → Other
fn classify_path(path: &Path) -> FileKind {
    let path_str = path.to_string_lossy().to_lowercase();
    let file_name = path
        .file_name()
        .map(|n| n.to_string_lossy().to_lowercase())
        .unwrap_or_default();

    if path_str.contains("/memory/") || file_name == "memory.md" {
        return FileKind::Memory;
    }
    if path_str.contains("/docs/")
        || path_str.contains("/.github/wiki/")
        || path_str.contains("/.github/docs/")
    {
        return FileKind::Docs;
    }
    if path_str.contains("/tasks/") || file_name.ends_with(".todo") {
        return FileKind::Tasks;
    }
    if path_str.contains("/ideas/") {
        return FileKind::Ideas;
    }
    if file_name == "claude.md"
        || file_name == "agents.md"
        || file_name == "settings.json"
        || path_str.contains("/rules/")
    {
        return FileKind::Instructions;
    }
    FileKind::Other
}

/// Return the user's HOME directory as an absolute canonical PathBuf.
///
/// Canonicalizes the HOME path so symlinks (e.g. `/var` → `/private/var` on macOS)
/// are resolved before the confinement check. Falls back to `/` when HOME is unset.
fn home_dir() -> PathBuf {
    let raw = std::env::var("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|_| PathBuf::from("/"));
    // Canonicalize so macOS `/var/folders/…` resolves to `/private/var/folders/…`
    // and the starts_with check works correctly against canonicalized file paths.
    fs::canonicalize(&raw).unwrap_or(raw)
}

/// Canonicalize `path_str` and verify it is confined under `home_root`.
///
/// Returns `Err` with a descriptive message if the path escapes HOME.
/// Returns `Err` if canonicalization fails (i.e. path does not exist).
fn safe_canonicalize(path_str: &str, home_root: &Path) -> Result<PathBuf, String> {
    let canonical = fs::canonicalize(path_str)
        .map_err(|_| format!("path not found (stale scan result): {path_str}"))?;
    if !canonical.starts_with(home_root) {
        return Err(format!(
            "traversal rejected: {path_str} is outside HOME ({})",
            home_root.display()
        ));
    }
    Ok(canonical)
}

/// Detect whether a file is binary by sniffing its first `BINARY_SNIFF_BYTES` bytes.
///
/// Returns `true` if any byte is 0x00 (null), indicating binary content.
fn is_binary(bytes: &[u8]) -> bool {
    let sniff_len = bytes.len().min(BINARY_SNIFF_BYTES);
    bytes[..sniff_len].contains(&0u8)
}

/// Read legacy file content from disk for one tool.
///
/// # Arguments
/// * `tool_id` — kebab-case tool identifier (e.g. `"claude-code"`).
/// * `paths`   — absolute file paths discovered by a prior scan.
///
/// # Returns
/// `Ok(Vec<MergeSourceFileRaw>)` where each entry carries its content (or `None`
/// if the file was skipped due to size, binary content, or permission denial).
/// Returns `Err` if any path no longer exists (stale scan result) or if a path
/// is outside the user's HOME directory.
///
/// # Algorithm
/// 1. Verify every path exists (stale check) — fail fast on first missing path.
/// 2. Read metadata + raw bytes for each path, classify, mark binary/oversize.
/// 3. Sort eligible (non-skipped) entries by size ascending to maximise variety
///    within the 50 000-character total budget.
/// 4. Fill in `content` for entries that fit within the budget; the rest get
///    `content: None`.
pub fn read_legacy_content(
    tool_id: String,
    paths: Vec<String>,
) -> Result<Vec<MergeSourceFileRaw>, String> {
    let home = home_dir();

    // -----------------------------------------------------------------------
    // Phase 1: validate existence (stale-path hard check) + gather metadata.
    // We do this before reading content so we fail fast on the first stale path.
    // -----------------------------------------------------------------------
    struct FileEntry {
        original_path: String,
        canonical: PathBuf,
        size_bytes: u64,
        kind: FileKind,
    }

    let mut entries: Vec<FileEntry> = Vec::with_capacity(paths.len());
    for path_str in &paths {
        // canonicalize checks existence; returns Err on missing path.
        let canonical = safe_canonicalize(path_str, &home)?;
        let metadata =
            fs::metadata(&canonical).map_err(|e| format!("cannot stat {path_str}: {e}"))?;
        let size_bytes = metadata.len();
        let kind = classify_path(&canonical);
        entries.push(FileEntry {
            original_path: path_str.clone(),
            canonical,
            size_bytes,
            kind,
        });
    }

    // -----------------------------------------------------------------------
    // Phase 2: read raw bytes, detect binary / oversize.
    // We accumulate all results first (with content = None for skipped entries)
    // before applying the total-char cap, because the cap orders by size.
    // -----------------------------------------------------------------------
    struct ReadResult {
        original_path: String,
        size_bytes: u64,
        kind: FileKind,
        /// Raw UTF-8 content if readable and within per-file limits.
        text: Option<String>,
    }

    let mut read_results: Vec<ReadResult> = Vec::with_capacity(entries.len());

    for entry in entries {
        // Permission-denied or other IO errors → skip this file, not abort.
        let raw_bytes = match fs::read(&entry.canonical) {
            Ok(b) => b,
            Err(_) => {
                read_results.push(ReadResult {
                    original_path: entry.original_path,
                    size_bytes: entry.size_bytes,
                    kind: entry.kind,
                    text: None,
                });
                continue;
            }
        };

        // Oversize check (uses the metadata size for efficiency).
        if entry.size_bytes > MAX_FILE_BYTES {
            read_results.push(ReadResult {
                original_path: entry.original_path,
                size_bytes: entry.size_bytes,
                kind: entry.kind,
                text: None,
            });
            continue;
        }

        // Binary detection.
        if is_binary(&raw_bytes) {
            read_results.push(ReadResult {
                original_path: entry.original_path,
                size_bytes: entry.size_bytes,
                kind: entry.kind,
                text: None,
            });
            continue;
        }

        // UTF-8 decode — invalid UTF-8 treated as binary.
        let text = match String::from_utf8(raw_bytes) {
            Ok(s) => s,
            Err(_) => {
                read_results.push(ReadResult {
                    original_path: entry.original_path,
                    size_bytes: entry.size_bytes,
                    kind: entry.kind,
                    text: None,
                });
                continue;
            }
        };

        read_results.push(ReadResult {
            original_path: entry.original_path,
            size_bytes: entry.size_bytes,
            kind: entry.kind,
            text: Some(text),
        });
    }

    // -----------------------------------------------------------------------
    // Phase 3: apply the 50 000-character total cap.
    // Strategy: include smallest readable files first to maximise variety.
    // Build an index sorted by size (ascending) over eligible entries.
    // -----------------------------------------------------------------------
    let mut eligible_indices: Vec<usize> = read_results
        .iter()
        .enumerate()
        .filter_map(|(i, r)| if r.text.is_some() { Some(i) } else { None })
        .collect();
    eligible_indices.sort_by_key(|&i| read_results[i].size_bytes);

    // Determine which indices survive the cap.
    let mut included: std::collections::HashSet<usize> =
        std::collections::HashSet::with_capacity(eligible_indices.len());
    let mut total_chars: usize = 0;
    for i in eligible_indices {
        let chars = read_results[i]
            .text
            .as_ref()
            .map(|s| s.chars().count())
            .unwrap_or(0);
        if total_chars + chars <= MAX_TOTAL_CHARS {
            included.insert(i);
            total_chars += chars;
        }
        // Files that push over the cap are silently skipped (content: None).
    }

    // -----------------------------------------------------------------------
    // Phase 4: assemble MergeSourceFileRaw, clearing content for excluded entries.
    // -----------------------------------------------------------------------
    let result = read_results
        .into_iter()
        .enumerate()
        .map(|(i, r)| {
            let content = if included.contains(&i) { r.text } else { None };
            MergeSourceFileRaw {
                tool_id: tool_id.clone(),
                path: r.original_path,
                content,
                kind: r.kind,
                size_bytes: r.size_bytes,
            }
        })
        .collect();

    Ok(result)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::io::Write;
    use tempfile::TempDir;

    /// Create a temp dir, set HOME to it, run `f`, then restore HOME.
    /// Uses a mutex to serialise env mutation across test threads.
    fn with_home<F: FnOnce(&TempDir)>(f: F) {
        use std::sync::Mutex;
        static ENV_LOCK: Mutex<()> = Mutex::new(());
        // Recover poisoned lock (another test panicked while holding it).
        let _guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());

        let dir = TempDir::new().unwrap();
        let old_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", dir.path());
        f(&dir);
        match old_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }
    }

    // -----------------------------------------------------------------------
    // Happy path: readable UTF-8 text file returned with content.
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn happy_path_reads_text_file() {
        with_home(|dir| {
            let file_path = dir.path().join("CLAUDE.md");
            std::fs::write(&file_path, "# My instructions\nNo any types.").unwrap();

            let paths = vec![file_path.to_str().unwrap().to_string()];
            let result = read_legacy_content("claude-code".to_string(), paths).unwrap();

            assert_eq!(result.len(), 1);
            let entry = &result[0];
            assert_eq!(entry.tool_id, "claude-code");
            assert!(entry.content.is_some(), "content should be populated");
            let content = entry.content.as_ref().unwrap();
            assert!(
                content.contains("No any types."),
                "content should match file"
            );
            assert_eq!(entry.kind, FileKind::Instructions);
            assert!(entry.size_bytes > 0);
        });
    }

    // -----------------------------------------------------------------------
    // Binary file: null byte in first 512 bytes → content: None.
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn binary_file_skipped() {
        with_home(|dir| {
            let file_path = dir.path().join("binary.bin");
            let mut f = std::fs::File::create(&file_path).unwrap();
            f.write_all(b"Some text\x00more text after null").unwrap();

            let paths = vec![file_path.to_str().unwrap().to_string()];
            let result = read_legacy_content("claude-code".to_string(), paths).unwrap();

            assert_eq!(result.len(), 1);
            assert!(
                result[0].content.is_none(),
                "binary file should have content: None"
            );
            assert!(
                result[0].size_bytes > 0,
                "size_bytes should still be populated"
            );
        });
    }

    // -----------------------------------------------------------------------
    // Oversize file (>1 MB) → content: None, no error.
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn oversize_file_skipped() {
        with_home(|dir| {
            let file_path = dir.path().join("huge.md");
            // Write 1 MiB + 1 byte of ASCII text (no null bytes).
            let chunk = "a".repeat(1024);
            let mut f = std::fs::File::create(&file_path).unwrap();
            for _ in 0..1025 {
                f.write_all(chunk.as_bytes()).unwrap();
            }
            drop(f);

            let paths = vec![file_path.to_str().unwrap().to_string()];
            let result = read_legacy_content("claude-code".to_string(), paths).unwrap();

            assert_eq!(result.len(), 1);
            assert!(
                result[0].content.is_none(),
                "oversize file should have content: None"
            );
            // size_bytes should reflect the actual file size (>1MB).
            assert!(result[0].size_bytes > MAX_FILE_BYTES);
        });
    }

    // -----------------------------------------------------------------------
    // Traversal attack: path outside HOME → Err.
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn traversal_outside_home_rejected() {
        with_home(|_dir| {
            // /etc/hosts exists on macOS/Linux; it is definitely outside a temp HOME.
            let paths = vec!["/etc/hosts".to_string()];
            let result = read_legacy_content("claude-code".to_string(), paths);
            assert!(result.is_err(), "path outside HOME should return Err");
            let msg = result.unwrap_err();
            assert!(
                msg.contains("traversal rejected") || msg.contains("outside HOME"),
                "error message should mention traversal: {msg}"
            );
        });
    }

    // -----------------------------------------------------------------------
    // Stale path: file does not exist → Err containing the path.
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn stale_path_returns_err() {
        with_home(|dir| {
            let missing = dir
                .path()
                .join("nonexistent_file_xyz.md")
                .to_str()
                .unwrap()
                .to_string();
            let result = read_legacy_content("claude-code".to_string(), vec![missing.clone()]);
            assert!(result.is_err(), "missing path should return Err");
            let msg = result.unwrap_err();
            assert!(
                msg.contains(&missing) || msg.contains("stale"),
                "error should reference the missing path: {msg}"
            );
        });
    }

    // -----------------------------------------------------------------------
    // Total character cap: files beyond 50 K chars get content: None.
    // Uses 3 small files that together exceed 50 K.
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn total_char_cap_applied() {
        with_home(|dir| {
            // Three files, each ~20 K chars (total ~60 K > 50 K cap).
            // Sorted ascending by size → first two included (40 K), third excluded.
            let text_20k = "x".repeat(20_000);
            let files: Vec<_> = (0..3)
                .map(|i| {
                    let p = dir.path().join(format!("file_{i}.md"));
                    std::fs::write(&p, &text_20k).unwrap();
                    p.to_str().unwrap().to_string()
                })
                .collect();

            let result = read_legacy_content("claude-code".to_string(), files).unwrap();

            assert_eq!(result.len(), 3);
            let with_content: Vec<_> = result.iter().filter(|r| r.content.is_some()).collect();
            let without_content: Vec<_> = result.iter().filter(|r| r.content.is_none()).collect();

            // Exactly 2 should have content (2 × 20 K = 40 K ≤ 50 K).
            assert_eq!(
                with_content.len(),
                2,
                "exactly 2 files should fit in the 50 K cap"
            );
            assert_eq!(
                without_content.len(),
                1,
                "exactly 1 file should be excluded"
            );
        });
    }

    // -----------------------------------------------------------------------
    // classify_path: spot-check known patterns.
    // -----------------------------------------------------------------------
    #[test]
    #[serial(global_env)]
    fn classify_path_instructions() {
        assert_eq!(
            classify_path(Path::new("/home/user/.claude/CLAUDE.md")),
            FileKind::Instructions
        );
        assert_eq!(
            classify_path(Path::new("/home/user/.claude/rules/no-todo.md")),
            FileKind::Instructions
        );
        assert_eq!(
            classify_path(Path::new("/home/user/.opencode/settings.json")),
            FileKind::Instructions
        );
    }

    #[test]
    #[serial(global_env)]
    fn classify_path_memory() {
        assert_eq!(
            classify_path(Path::new("/home/user/.claude/memory/decisions.md")),
            FileKind::Memory
        );
    }

    #[test]
    #[serial(global_env)]
    fn classify_path_other() {
        assert_eq!(
            classify_path(Path::new("/home/user/.claude/inbox/task.yaml")),
            FileKind::Other
        );
    }
}
