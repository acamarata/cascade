//! # context_pack::packer
//!
//! Purpose: Produce a single UTF-8 text bundle from a directory tree —
//!   repomix-style output containing an ASCII file-tree header followed by
//!   per-file sections fenced with `=== {rel_path} ===` delimiters.
//!
//! Inputs:
//!   - `root: &Path` — directory to pack; MUST be absolute and inside HOME.
//!   - `extra_excludes: &[String]` — additional gitignore-compatible patterns
//!     supplied by the caller (beyond the built-in exclude list).
//!
//! Outputs:
//!   - `Ok(PathBuf)` — absolute path of the temp file containing the bundle.
//!     Caller is responsible for deleting the temp file when done.
//!   - `Err(PackError)` — see [`PackError`] for variants.
//!
//! Constraints:
//!   - Output size limit: 2 MB (2_097_152 bytes). Exceeded → `PackError::TooLarge`.
//!   - Traversal is confined to `root`; symlinks are not followed.
//!   - Paths outside HOME are rejected immediately.
//!   - Binary files (null byte in first 512 bytes) are skipped silently.
//!   - Built-in excludes: `node_modules/`, `.git/`, `dist/`, `target/`, `.cache/`.
//!   - `.gitignore` files inside `root` are honoured via the `ignore` crate.
//!   - Caller is responsible for deleting the returned temp file.
//!
//! SPORT: MASTER-COMPONENTS.md — packer module (T-P3-E07-09)

use std::cmp::Reverse;
use std::fmt::Write as FmtWrite;
use std::fs;
use std::io::{Read, Write as IoWrite};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use ignore::WalkBuilder;
use thiserror::Error;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Output size cap: 2 MB.
pub const MAX_OUTPUT_BYTES: usize = 2 * 1024 * 1024;

/// First N bytes read from each file to detect binary content.
const BINARY_PROBE_BYTES: usize = 512;

/// Directories always excluded regardless of caller-supplied patterns.
const BUILT_IN_EXCLUDES: &[&str] = &["node_modules/", ".git/", "dist/", "target/", ".cache/"];

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by [`pack_codebase`].
#[derive(Debug, Error)]
pub enum PackError {
    /// The root path is outside the user's HOME directory.
    #[error("path traversal rejected: root must be inside HOME")]
    TraversalRejected,

    /// The root path does not exist or is not a directory.
    #[error("root is not a directory: {0}")]
    NotADirectory(PathBuf),

    /// The generated bundle would exceed the 2 MB output cap.
    ///
    /// The payload lists the top-10 largest contributors as
    /// `"path: N bytes"` strings so the user knows what to exclude.
    #[error("bundle exceeds 2 MB limit; largest contributors:\n{contributors}")]
    TooLarge { contributors: String },

    /// An I/O error occurred while walking the tree or writing the bundle.
    #[error("i/o error: {0}")]
    Io(#[from] std::io::Error),

    /// A fmt::write error occurred while building the in-memory bundle.
    #[error("format error: {0}")]
    Fmt(#[from] std::fmt::Error),
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Pack a directory tree into a single UTF-8 text bundle (repomix-style).
///
/// Returns the path to a temp file containing the bundle. The caller is
/// responsible for deleting the file when it is no longer needed.
///
/// # Arguments
///
/// * `root` — absolute path to the directory to pack; must be inside HOME.
/// * `extra_excludes` — additional gitignore-compatible glob patterns; merged
///   with the built-in exclude list.
///
/// # Errors
///
/// See [`PackError`] for all error variants.
pub fn pack_codebase(root: &Path, extra_excludes: &[String]) -> Result<PathBuf, PackError> {
    // ── Guard: must be inside HOME ─────────────────────────────────────────────
    let home = home_dir()?;
    if !root.starts_with(&home) {
        return Err(PackError::TraversalRejected);
    }

    // ── Guard: must be a directory ─────────────────────────────────────────────
    if !root.is_dir() {
        return Err(PackError::NotADirectory(root.to_owned()));
    }

    // ── Walk: collect all text file paths ─────────────────────────────────────
    let entries = collect_entries(root, extra_excludes)?;

    // ── Size check: track per-file sizes for the error payload ────────────────
    let mut file_sizes: Vec<(PathBuf, usize)> = Vec::with_capacity(entries.len());
    let mut total_size: usize = 0;

    for path in &entries {
        let size = fs::metadata(path).map(|m| m.len() as usize).unwrap_or(0);
        total_size += size;
        file_sizes.push((path.clone(), size));
    }

    // Add a rough overhead for the tree header and fences (~4KB per 100 files).
    let overhead = entries.len() * 64;
    if total_size + overhead > MAX_OUTPUT_BYTES {
        // Sort descending by size; take top-10.
        file_sizes.sort_by_key(|b| std::cmp::Reverse(b.1));
        let contributors = file_sizes
            .iter()
            .take(10)
            .map(|(p, s)| {
                let rel = p.strip_prefix(root).unwrap_or(p);
                format!("  {}: {} bytes", rel.display(), s)
            })
            .collect::<Vec<_>>()
            .join("\n");
        return Err(PackError::TooLarge { contributors });
    }

    // ── Build bundle in memory ─────────────────────────────────────────────────
    let bundle = build_bundle(root, &entries)?;

    // Final size check on the actual rendered output.
    if bundle.len() > MAX_OUTPUT_BYTES {
        let mut sorted = file_sizes;
        sorted.sort_by_key(|b| Reverse(b.1));
        let contributors = sorted
            .iter()
            .take(10)
            .map(|(p, s)| {
                let rel = p.strip_prefix(root).unwrap_or(p);
                format!("  {}: {} bytes", rel.display(), s)
            })
            .collect::<Vec<_>>()
            .join("\n");
        return Err(PackError::TooLarge { contributors });
    }

    // ── Write to temp file ─────────────────────────────────────────────────────
    let ts = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0);
    let tmp_path = std::env::temp_dir().join(format!("cascade-pack-{ts}.txt"));
    let mut file = fs::File::create(&tmp_path)?;
    file.write_all(bundle.as_bytes())?;

    Ok(tmp_path)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Return the user's HOME directory as a `PathBuf`.
fn home_dir() -> Result<PathBuf, PackError> {
    std::env::var_os("HOME").map(PathBuf::from).ok_or_else(|| {
        PackError::Io(std::io::Error::new(
            std::io::ErrorKind::NotFound,
            "HOME environment variable not set",
        ))
    })
}

/// Walk `root` using the `ignore` crate (honours `.gitignore`) and return a
/// sorted `Vec<PathBuf>` of all non-binary, non-excluded text files.
///
/// Exclusions are checked by inspecting each entry's path components against
/// the built-in and caller-supplied exclude patterns.
fn collect_entries(root: &Path, extra_excludes: &[String]) -> Result<Vec<PathBuf>, PackError> {
    // Compile extra excludes into glob::Pattern for fast matching.
    let extra_globs: Vec<glob::Pattern> = extra_excludes
        .iter()
        .filter_map(|p| glob::Pattern::new(p).ok())
        .collect();

    let mut builder = WalkBuilder::new(root);
    builder
        .hidden(false) // include dot-files unless gitignored
        .follow_links(false) // never follow symlinks
        .git_ignore(true) // honour .gitignore files inside root
        .git_global(false) // skip ~/.gitignore_global (user may have broad rules)
        .git_exclude(false) // skip .git/info/exclude
        .parents(false); // only read .gitignore files at/below root, not ancestors

    let mut paths: Vec<PathBuf> = Vec::new();

    for result in builder.build() {
        let entry = match result {
            Ok(e) => e,
            Err(_) => continue,
        };
        let path = entry.into_path();

        // Skip non-files.
        if !path.is_file() {
            continue;
        }

        // Compute path relative to root for exclusion checks.
        let rel = match path.strip_prefix(root) {
            Ok(r) => r,
            Err(_) => continue,
        };

        // Check built-in excludes: any component named in the built-in list
        // (the list uses trailing `/` to indicate directories, strip it).
        let excluded_by_builtin = rel.components().any(|c| {
            let name = c.as_os_str().to_string_lossy();
            BUILT_IN_EXCLUDES
                .iter()
                .any(|pat| name == pat.trim_end_matches('/'))
        });
        if excluded_by_builtin {
            continue;
        }

        // Check caller-supplied glob excludes against relative path string.
        let rel_str = rel.to_string_lossy();
        if extra_globs.iter().any(|g| {
            g.matches(&rel_str)
                || g.matches(rel.file_name().unwrap_or_default().to_str().unwrap_or(""))
        }) {
            continue;
        }

        // Skip binary files.
        if is_binary(&path) {
            continue;
        }

        paths.push(path);
    }

    // Deterministic order.
    paths.sort();
    Ok(paths)
}

/// Return `true` if the file appears to be binary (null byte in first 512 bytes).
///
/// UTF-16 files (which contain null bytes) are also classified as binary —
/// this is intentional per scope.
fn is_binary(path: &Path) -> bool {
    let mut buf = [0u8; BINARY_PROBE_BYTES];
    match fs::File::open(path) {
        Ok(mut f) => {
            let n = f.read(&mut buf).unwrap_or(0);
            buf[..n].contains(&0u8)
        }
        Err(_) => true, // unreadable → treat as binary / skip
    }
}

/// Build an in-memory bundle string from the collected file list.
///
/// Format:
/// ```text
/// ================================================================
/// CASCADE CODEBASE PACK
/// root: /abs/path/to/root
/// files: N
/// ================================================================
///
/// FILE TREE
/// ─────────
/// root/
///   subdir/
///     file.rs
///   top.rs
///
/// ================================================================
///
/// === subdir/file.rs ===
/// <contents>
///
/// === top.rs ===
/// <contents>
/// ```
fn build_bundle(root: &Path, entries: &[PathBuf]) -> Result<String, PackError> {
    let mut out = String::with_capacity(entries.len() * 512);
    let sep = "=".repeat(64);

    // ── Header ────────────────────────────────────────────────────────────────
    writeln!(out, "{sep}")?;
    writeln!(out, "CASCADE CODEBASE PACK")?;
    writeln!(out, "root: {}", root.display())?;
    writeln!(out, "files: {}", entries.len())?;
    writeln!(out, "{sep}")?;
    writeln!(out)?;

    // ── ASCII tree ────────────────────────────────────────────────────────────
    writeln!(out, "FILE TREE")?;
    writeln!(out, "─────────")?;
    write_tree(&mut out, root, entries)?;
    writeln!(out)?;
    writeln!(out, "{sep}")?;
    writeln!(out)?;

    // ── File sections ─────────────────────────────────────────────────────────
    for path in entries {
        let rel = path.strip_prefix(root).unwrap_or(path);
        writeln!(out, "=== {} ===", rel.display())?;
        match fs::read_to_string(path) {
            Ok(content) => {
                out.push_str(&content);
                if !content.ends_with('\n') {
                    out.push('\n');
                }
            }
            Err(_) => {
                writeln!(out, "[unreadable — skipped]")?;
            }
        }
        writeln!(out)?;
    }

    Ok(out)
}

/// Write a simple indented file tree.
///
/// Each directory appears as `dirname/` and each file as its name, indented
/// two spaces per depth level relative to root.
fn write_tree(out: &mut String, root: &Path, entries: &[PathBuf]) -> Result<(), PackError> {
    // Collect all unique directory prefixes so we can print the tree.
    use std::collections::BTreeSet;

    let mut dirs: BTreeSet<PathBuf> = BTreeSet::new();
    for path in entries {
        if let Ok(rel) = path.strip_prefix(root) {
            // Walk ancestor components.
            let mut ancestor = PathBuf::new();
            for component in rel.components() {
                ancestor.push(component);
                if ancestor != *rel {
                    dirs.insert(ancestor.clone());
                }
            }
        }
    }

    // Print root label.
    writeln!(
        out,
        "{}/",
        root.file_name().unwrap_or_default().to_string_lossy()
    )?;

    // Print dirs.
    for dir in &dirs {
        let depth = dir.components().count();
        let indent = "  ".repeat(depth);
        writeln!(
            out,
            "{}{}/",
            indent,
            dir.file_name().unwrap_or_default().to_string_lossy()
        )?;
    }

    // Print files.
    for path in entries {
        if let Ok(rel) = path.strip_prefix(root) {
            let depth = rel.components().count(); // file itself counts as one component
            let indent = "  ".repeat(depth.saturating_sub(1) + 1);
            writeln!(
                out,
                "{}{}",
                indent,
                rel.file_name().unwrap_or_default().to_string_lossy()
            )?;
        }
    }

    Ok(())
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    /// Create a small fixture repo and verify the bundle output.
    #[test]
    #[serial(global_env)]
    fn test_pack_small_fixture() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let root = tmp.path();

        // Create a few text files.
        let sub = root.join("subdir");
        fs::create_dir_all(&sub).unwrap();
        fs::write(sub.join("hello.rs"), "fn hello() {}\n").unwrap();
        fs::write(root.join("main.rs"), "fn main() {}\n").unwrap();
        fs::write(root.join("README.md"), "# test\n").unwrap();

        // Override HOME so the traversal guard passes.
        unsafe { std::env::set_var("HOME", root) };

        let out_path = pack_codebase(root, &[]).expect("pack should succeed");
        let content = fs::read_to_string(&out_path).unwrap();

        // Verify tree header present.
        assert!(content.contains("CASCADE CODEBASE PACK"), "missing header");
        // Verify file sections present.
        assert!(
            content.contains("=== main.rs ==="),
            "missing main.rs section"
        );
        assert!(
            content.contains("=== README.md ==="),
            "missing README.md section"
        );
        assert!(
            content.contains("=== subdir/hello.rs ==="),
            "missing subdir/hello.rs section"
        );
        // Verify content included.
        assert!(content.contains("fn main()"), "missing file content");

        fs::remove_file(&out_path).unwrap();
        unsafe { std::env::remove_var("HOME") };
    }

    /// Binary files must be excluded from the bundle.
    #[test]
    #[serial(global_env)]
    fn test_binary_file_excluded() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let root = tmp.path();

        fs::write(root.join("text.txt"), "hello world\n").unwrap();
        // Write a file with a null byte → detected as binary.
        fs::write(root.join("image.png"), b"\x89PNG\x00\x00\x00\x00data").unwrap();

        unsafe { std::env::set_var("HOME", root) };

        let out_path = pack_codebase(root, &[]).expect("pack should succeed");
        let content = fs::read_to_string(&out_path).unwrap();

        assert!(
            content.contains("=== text.txt ==="),
            "text file should be present"
        );
        assert!(
            !content.contains("=== image.png ==="),
            "binary file should be absent"
        );

        fs::remove_file(&out_path).unwrap();
        unsafe { std::env::remove_var("HOME") };
    }

    /// Files in node_modules/ and .git/ must be excluded.
    #[test]
    #[serial(global_env)]
    fn test_built_in_excludes() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let root = tmp.path();

        fs::create_dir_all(root.join("node_modules/pkg")).unwrap();
        fs::write(root.join("node_modules/pkg/index.js"), "module.exports=1\n").unwrap();
        fs::create_dir_all(root.join(".git")).unwrap();
        fs::write(root.join(".git/HEAD"), "ref: refs/heads/main\n").unwrap();
        fs::write(root.join("app.ts"), "export {}\n").unwrap();

        unsafe { std::env::set_var("HOME", root) };

        let out_path = pack_codebase(root, &[]).expect("pack should succeed");
        let content = fs::read_to_string(&out_path).unwrap();

        assert!(
            !content.contains("node_modules"),
            "node_modules should be excluded"
        );
        assert!(!content.contains("HEAD"), "git HEAD should be excluded");
        assert!(
            content.contains("=== app.ts ==="),
            "app.ts should be present"
        );

        fs::remove_file(&out_path).unwrap();
        unsafe { std::env::remove_var("HOME") };
    }

    /// A directory producing > 2 MB output must return TooLarge with file list.
    #[test]
    #[serial(global_env)]
    fn test_size_cap_returns_error_with_contributors() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let root = tmp.path();

        // Write 3 files each ~800 KB → total > 2 MB.
        let big = "x".repeat(820_000);
        fs::write(root.join("a.txt"), &big).unwrap();
        fs::write(root.join("b.txt"), &big).unwrap();
        fs::write(root.join("c.txt"), &big).unwrap();

        unsafe { std::env::set_var("HOME", root) };

        let result = pack_codebase(root, &[]);
        match result {
            Err(PackError::TooLarge { contributors }) => {
                assert!(
                    !contributors.is_empty(),
                    "contributors list must not be empty"
                );
                // At least one file name should appear.
                let has_file = contributors.contains("a.txt")
                    || contributors.contains("b.txt")
                    || contributors.contains("c.txt");
                assert!(
                    has_file,
                    "contributors should name at least one file: {contributors}"
                );
            }
            Ok(_) => panic!("expected TooLarge error"),
            Err(e) => panic!("unexpected error: {e}"),
        }

        unsafe { std::env::remove_var("HOME") };
    }

    /// Extra excludes supplied by the caller must be respected.
    #[test]
    #[serial(global_env)]
    fn test_extra_excludes_respected() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let root = tmp.path();

        fs::write(root.join("keep.rs"), "fn keep() {}\n").unwrap();
        fs::write(root.join("skip.log"), "log output\n").unwrap();

        unsafe { std::env::set_var("HOME", root) };

        let out_path = pack_codebase(root, &["*.log".to_string()]).expect("pack should succeed");
        let content = fs::read_to_string(&out_path).unwrap();

        assert!(
            content.contains("=== keep.rs ==="),
            "keep.rs should be present"
        );
        assert!(
            !content.contains("skip.log"),
            "skip.log should be excluded by extra pattern"
        );

        fs::remove_file(&out_path).unwrap();
        unsafe { std::env::remove_var("HOME") };
    }

    /// Paths outside HOME must be rejected.
    #[test]
    #[serial(global_env)]
    fn test_traversal_rejected_outside_home() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let outside = tmp.path();

        // Set HOME to a different directory.
        let home_tmp = TempDir::new().unwrap();
        unsafe { std::env::set_var("HOME", home_tmp.path()) };

        let result = pack_codebase(outside, &[]);
        assert!(
            matches!(result, Err(PackError::TraversalRejected)),
            "expected TraversalRejected, got: {result:?}"
        );

        unsafe { std::env::remove_var("HOME") };
    }
}
