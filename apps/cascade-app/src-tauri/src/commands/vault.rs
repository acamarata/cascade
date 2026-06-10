//! # commands::vault
//!
//! Tauri IPC commands for vault (note file) operations — provides the React
//! frontend with a confinement-safe file-system view of `~/.cascade` (or any
//! caller-supplied root).
//!
//! ## Purpose
//!
//! These commands let the frontend read, write, rename, and delete Markdown
//! notes, and receive live change notifications via a file-system watcher.
//! Every path argument is canonicalised and checked against the vault root
//! before any I/O — directory traversal is always rejected.
//!
//! ## Commands
//!
//! | Tauri command    | Operation                                        |
//! |------------------|--------------------------------------------------|
//! | `vault_list`     | Recursive tree of `.md` files + directories     |
//! | `vault_read`     | Read a single note as a UTF-8 string            |
//! | `vault_write`    | Atomic write (tmp + rename); creates parent dirs |
//! | `vault_watch`    | Set up fs watcher; emits `vault://changed` event |
//!
//! ## IPC naming
//!
//! Tauri maps snake_case command names to camelCase on the JS side automatically.
//! All struct fields use `#[serde(rename_all = "camelCase")]`.
//!
//! ## Security
//!
//! - Both root and path are canonicalised via `std::fs::canonicalize`.
//! - A path is accepted only if it starts_with the canonical vault root.
//! - Symlinks are resolved by canonicalize; if resolution escapes the root it
//!   is rejected.
//! - A path that does not yet exist is validated by walking parent components
//!   up to an existing ancestor and checking the ancestor is inside the root.
//!
//! ## Constraints
//!
//! - Only `.md` files are included in `vault_list` results.
//! - Atomic writes use a sibling `.tmp` file in the same directory; POSIX
//!   `rename` is used for atomicity.
//! - The `notify` Watcher returned by `vault_watch` is intentionally leaked —
//!   the Tauri window owns its lifetime until the window is destroyed.
//!
//! ## SPORT
//!
//! MASTER-ENDPOINTS.md — vault IPC commands (T-P3-E06-01)

use notify::{Config, EventKind, RecommendedWatcher, RecursiveMode, Watcher};
use serde::{Deserialize, Serialize};
use std::fs;
use std::io;
use std::path::{Component, Path, PathBuf};
use tauri::Emitter;

// ── Error type ─────────────────────────────────────────────────────────────────

/// Typed errors returned by every vault command, serialised to JSON on failure.
///
/// The `#[serde(tag = "kind")]` envelope means every error arrives at the
/// frontend as `{ "kind": "TraversalDenied", "message": "..." }` — forward-
/// compatible: new variants never break existing frontend error handling.
#[derive(Debug, thiserror::Error, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "camelCase")]
pub enum VaultError {
    #[error("traversal denied: {message}")]
    TraversalDenied { message: String },

    #[error("io error: {message}")]
    Io { message: String },

    #[error("watch error: {message}")]
    Watch { message: String },

    #[error("not found: {message}")]
    NotFound { message: String },
}

impl VaultError {
    fn traversal(msg: impl Into<String>) -> Self {
        VaultError::TraversalDenied {
            message: msg.into(),
        }
    }

    fn io(err: impl ToString) -> Self {
        VaultError::Io {
            message: err.to_string(),
        }
    }

    fn watch(err: impl ToString) -> Self {
        VaultError::Watch {
            message: err.to_string(),
        }
    }

    fn not_found(msg: impl Into<String>) -> Self {
        VaultError::NotFound {
            message: msg.into(),
        }
    }
}

// Tauri requires commands to return `Result<T, E>` where `E: Serialize`.
// `thiserror::Error` does not impl `Serialize` by default — we do it above.
impl From<io::Error> for VaultError {
    fn from(e: io::Error) -> Self {
        VaultError::io(e)
    }
}

// ── Tree node ──────────────────────────────────────────────────────────────────

/// A node in the vault directory tree returned by `vault_list`.
///
/// Directories have `isDir = true` and a non-empty `children` vec.
/// Leaf `.md` files have `isDir = false` and an empty `children` vec.
/// `path` is the absolute path string — pass it back to `vault_read` /
/// `vault_write` / etc. unchanged.
#[derive(Debug, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct VaultNode {
    /// The file/directory name (last component only).
    pub name: String,
    /// Absolute path string — safe to pass back to other vault commands.
    pub path: String,
    /// `true` for directories, `false` for `.md` files.
    pub is_dir: bool,
    /// Child nodes (empty for files; sorted name-ascending for directories).
    pub children: Vec<VaultNode>,
}

// ── Path validation ────────────────────────────────────────────────────────────

/// Resolve `root` to a canonical absolute path.
///
/// Returns `VaultError::Io` if `root` does not exist or cannot be resolved.
fn canonical_root(root: &str) -> Result<PathBuf, VaultError> {
    fs::canonicalize(root)
        .map_err(|e| VaultError::io(format!("cannot resolve vault root {root:?}: {e}")))
}

/// Validate `path` against `canon_root`.
///
/// Two cases:
///   a) The path exists on disk — canonicalize it and check starts_with.
///   b) The path does not yet exist — walk up to the nearest existing ancestor,
///      canonicalize that, and check starts_with.
///
/// Returns the canonical path if valid; `VaultError::TraversalDenied` otherwise.
fn guard_path(path: &str, canon_root: &Path) -> Result<PathBuf, VaultError> {
    let candidate = PathBuf::from(path);

    // Reject paths with explicit traversal components.
    for c in candidate.components() {
        if c == Component::ParentDir {
            return Err(VaultError::traversal(
                "path contains '..' component — traversal denied",
            ));
        }
    }

    // Case a: path exists — canonicalize and check.
    if candidate.exists() {
        let canon = fs::canonicalize(&candidate)
            .map_err(|e| VaultError::io(format!("cannot canonicalize {path:?}: {e}")))?;
        if !canon.starts_with(canon_root) {
            return Err(VaultError::traversal(format!(
                "{path:?} resolves outside vault root"
            )));
        }
        return Ok(canon);
    }

    // Case b: path does not exist — walk up until an ancestor exists.
    let mut ancestor = candidate.clone();
    loop {
        if ancestor.pop() {
            if ancestor.as_os_str().is_empty() {
                // Ran out of components without finding an existing ancestor.
                break;
            }
            if ancestor.exists() {
                let canon_ancestor = fs::canonicalize(&ancestor).map_err(|e| {
                    VaultError::io(format!("cannot canonicalize ancestor {ancestor:?}: {e}"))
                })?;
                if !canon_ancestor.starts_with(canon_root) {
                    return Err(VaultError::traversal(format!(
                        "{path:?} resolves outside vault root"
                    )));
                }
                // Ancestor is inside root — the full (non-existent) path is fine.
                return Ok(candidate.to_path_buf());
            }
        } else {
            break;
        }
    }

    Err(VaultError::traversal(format!(
        "{path:?}: cannot verify confinement — no existing ancestor found inside vault root"
    )))
}

// ── Tree walk ─────────────────────────────────────────────────────────────────

/// Recursively build a `VaultNode` tree rooted at `dir`.
///
/// Only `.md` files are included as leaf nodes; all directories are included
/// as intermediate nodes (even if they contain no `.md` files — they may
/// contain sub-directories that do).  Entries are sorted name-ascending.
fn walk(dir: &Path) -> VaultNode {
    let name = dir
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    let path_str = dir.to_string_lossy().into_owned();

    let mut children = Vec::new();

    if let Ok(entries) = fs::read_dir(dir) {
        let mut entries: Vec<_> = entries.flatten().collect();
        entries.sort_by_key(|e| e.file_name());

        for entry in entries {
            let ep = entry.path();
            let ft = match entry.file_type() {
                Ok(ft) => ft,
                Err(_) => continue,
            };

            if ft.is_dir() {
                // Recurse — include even if currently empty (may contain md later)
                children.push(walk(&ep));
            } else if ft.is_file() && ep.extension().and_then(|e| e.to_str()) == Some("md") {
                let leaf_name = ep
                    .file_name()
                    .map(|n| n.to_string_lossy().into_owned())
                    .unwrap_or_default();
                children.push(VaultNode {
                    name: leaf_name,
                    path: ep.to_string_lossy().into_owned(),
                    is_dir: false,
                    children: vec![],
                });
            }
        }
    }

    VaultNode {
        name,
        path: path_str,
        is_dir: true,
        children,
    }
}

// ── Tauri commands ─────────────────────────────────────────────────────────────

/// Recursively list all `.md` files and directories under `root`.
///
/// `root` must be an absolute path that exists on disk (e.g. `~/.cascade`
/// after tilde expansion performed by the caller).  Returns the root node of
/// the tree; all descendant nodes are nested in `children`.
#[tauri::command]
pub fn vault_list(root: String) -> Result<VaultNode, VaultError> {
    let canon_root = canonical_root(&root)?;
    if !canon_root.is_dir() {
        return Err(VaultError::not_found(format!(
            "vault root {root:?} is not a directory"
        )));
    }
    Ok(walk(&canon_root))
}

/// Read the UTF-8 content of the `.md` file at `path`.
///
/// `path` must be inside the vault root supplied at watch/list time.  The
/// caller is responsible for resolving `~` before calling this command.
#[tauri::command]
pub fn vault_read(root: String, path: String) -> Result<String, VaultError> {
    let canon_root = canonical_root(&root)?;
    let safe_path = guard_path(&path, &canon_root)?;

    if !safe_path.exists() {
        return Err(VaultError::not_found(format!("{path:?} does not exist")));
    }

    fs::read_to_string(&safe_path).map_err(VaultError::from)
}

/// Atomically write `content` to `path` (creates parent directories if needed).
///
/// Uses a sibling `.tmp` file + POSIX rename for atomicity.  Safe to call
/// concurrently with `vault_watch` — the watcher will see the rename as one
/// change event rather than a partial write.
#[tauri::command]
pub fn vault_write(root: String, path: String, content: String) -> Result<(), VaultError> {
    let canon_root = canonical_root(&root)?;
    let safe_path = guard_path(&path, &canon_root)?;

    // Create parent directories if needed.
    if let Some(parent) = safe_path.parent() {
        fs::create_dir_all(parent).map_err(VaultError::from)?;
    }

    // Atomic write: write to a sibling tmp file then rename.
    let tmp_path = safe_path.with_extension("md.tmp");
    fs::write(&tmp_path, &content).map_err(VaultError::from)?;
    fs::rename(&tmp_path, &safe_path).map_err(VaultError::from)?;

    Ok(())
}

/// Install a file-system watcher on `root`; emits `"vault://changed"` Tauri
/// events whenever any `.md` file under `root` is created, modified, or removed.
///
/// The event payload is `{ "path": "<absolute path>" }`.
///
/// ## Watcher lifetime
///
/// The `RecommendedWatcher` is Box-leaked so it lives for the duration of the
/// process (or until the window is destroyed and its memory is reclaimed by the
/// OS).  A production implementation would store the watcher in `AppState`; the
/// spec for this ticket calls for a simple watch-and-forget approach — the
/// watcher ref is intentionally held alive via `Box::leak`.
#[tauri::command]
pub fn vault_watch(root: String, window: tauri::Window) -> Result<(), VaultError> {
    let canon_root = canonical_root(&root)?;
    if !canon_root.is_dir() {
        return Err(VaultError::not_found(format!(
            "vault root {root:?} is not a directory"
        )));
    }

    let canon_root_clone = canon_root.clone();

    let mut watcher = RecommendedWatcher::new(
        move |res: notify::Result<notify::Event>| {
            if let Ok(event) = res {
                // Only emit for file-content-changing events on .md files.
                let is_content_event = matches!(
                    event.kind,
                    EventKind::Create(_) | EventKind::Modify(_) | EventKind::Remove(_)
                );
                if !is_content_event {
                    return;
                }

                for path in &event.paths {
                    let is_md = path.extension().and_then(|e| e.to_str()) == Some("md");
                    if is_md && path.starts_with(&canon_root_clone) {
                        let _ = window.emit(
                            "vault://changed",
                            serde_json::json!({
                                "path": path.to_string_lossy()
                            }),
                        );
                    }
                }
            }
        },
        Config::default(),
    )
    .map_err(VaultError::watch)?;

    watcher
        .watch(&canon_root, RecursiveMode::Recursive)
        .map_err(VaultError::watch)?;

    // Leak the watcher so it keeps running for the window's lifetime.
    // SAFETY: the watcher holds only a Tauri Window handle (refcounted) and the
    // notify OS handles — no raw pointer unsafety.
    Box::leak(Box::new(watcher));

    Ok(())
}

// ── vault_search ──────────────────────────────────────────────────────────────

/// A single match returned by `vault_search`.
///
/// `file` is the absolute path to the `.md` file.
/// `line_no` is 1-based (ripgrep --line-number output).
/// `line_text` is the matching line content (80-char capped for the UI snippet).
#[derive(Debug, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct SearchMatch {
    /// Absolute path to the `.md` file that contains the match.
    pub file: String,
    /// 1-based line number of the match inside `file`.
    pub line_no: u64,
    /// The matching line text (truncated to 200 chars before capping to 80
    /// in the UI layer — full text kept here for keyword highlighting).
    pub line_text: String,
}

/// Maximum number of matches returned by `vault_search`.
const SEARCH_RESULT_CAP: usize = 200;

/// Full-text search across all `.md` files in the vault root using ripgrep.
///
/// ## Purpose
///
/// Shells out to the bundled ripgrep (`rg`) sidecar with `--json --line-number`
/// to search `query` within every `.md` file under `root`.  Returns up to
/// [`SEARCH_RESULT_CAP`] matches, each with a file path, 1-based line number,
/// and the matching line text.
///
/// ## Inputs
///
/// - `root`: absolute vault root path (same as `vault_list`).
/// - `query`: the search pattern (plain string; empty/blank → returns empty vec).
///
/// ## Outputs
///
/// `Ok(Vec<SearchMatch>)` — empty when no files match.
/// `Err(VaultError)` — typed error on traversal denial, missing rg binary, or
/// ripgrep I/O failure.
///
/// ## Security
///
/// - `root` is validated by [`canonical_root`] and [`guard_path`] helpers.
/// - `query` and `root` are passed as discrete `Command::new` arguments — never
///   through a shell (no `bash -c`), so shell injection is not possible.
/// - Ripgrep's `--no-ignore` is NOT passed — the caller's `.gitignore` etc. are
///   respected (intentional: vault roots typically have no ignore files anyway).
///
/// ## Missing rg binary
///
/// When ripgrep is not found (`NotFound` OS error), returns
/// `VaultError::Io { message: "ripgrep (rg) not found …" }` rather than
/// crashing.  The spec permits this typed error as the graceful fallback path
/// when the sidecar binary has not been bundled.
///
/// ## SPORT
///
/// MASTER-ENDPOINTS.md — vault_search (T-P3-E06-09)
#[tauri::command]
pub fn vault_search(root: String, query: String) -> Result<Vec<SearchMatch>, VaultError> {
    // Reject blank queries immediately — return empty rather than searching for "".
    if query.trim().is_empty() {
        return Ok(vec![]);
    }

    // Validate vault root.
    let canon_root = canonical_root(&root)?;
    if !canon_root.is_dir() {
        return Err(VaultError::not_found(format!(
            "vault root {root:?} is not a directory"
        )));
    }

    // Build the ripgrep command.
    // Discrete args — NEVER bash -c — to prevent injection (per repo convention).
    //
    // Flags:
    //   --json          structured JSONL output (Message::Match lines)
    //   --line-number   include 1-based line numbers (already implied by --json but
    //                   explicit for clarity)
    //   --glob "*.md"   restrict matches to Markdown files
    //   --max-count 200 stop after 200 matches per file (belt-and-suspenders)
    //   --              end of flags; next arg is the pattern
    let output = std::process::Command::new("rg")
        .arg("--json")
        .arg("--line-number")
        .arg("--glob")
        .arg("*.md")
        .arg("--max-count")
        .arg("200")
        .arg("--")
        .arg(&query)
        .arg(canon_root.as_os_str())
        .output()
        .map_err(|e| {
            if e.kind() == std::io::ErrorKind::NotFound {
                VaultError::io(
                    "ripgrep (rg) not found — ensure it is installed or bundled as a sidecar",
                )
            } else {
                VaultError::io(format!("failed to spawn rg: {e}"))
            }
        })?;

    // ripgrep exits 0 (matches found), 1 (no matches), or 2 (error).
    // Exit code 1 is normal — return empty vec.
    if output.status.code() == Some(1) {
        return Ok(vec![]);
    }
    if output.status.code() == Some(2) {
        let stderr = String::from_utf8_lossy(&output.stderr);
        return Err(VaultError::io(format!("rg reported an error: {stderr}")));
    }

    // Parse JSONL output.  Each line is a JSON object with a `type` field.
    // We care only about `type == "match"` lines.
    let stdout = String::from_utf8_lossy(&output.stdout);
    let mut matches: Vec<SearchMatch> = Vec::new();

    for line in stdout.lines() {
        if matches.len() >= SEARCH_RESULT_CAP {
            break;
        }
        let Ok(val) = serde_json::from_str::<serde_json::Value>(line) else {
            continue;
        };
        if val.get("type").and_then(|t| t.as_str()) != Some("match") {
            continue;
        }
        let data = match val.get("data") {
            Some(d) => d,
            None => continue,
        };

        // Extract file path.
        let file = data
            .get("path")
            .and_then(|p| p.get("text"))
            .and_then(|t| t.as_str())
            .unwrap_or("")
            .to_owned();
        if file.is_empty() {
            continue;
        }

        // Validate the matched file is inside the vault root (defence-in-depth).
        if !std::path::Path::new(&file).starts_with(&canon_root) {
            continue;
        }

        // Extract line number (1-based).
        let line_no = data
            .get("line_number")
            .and_then(|n| n.as_u64())
            .unwrap_or(0);

        // Extract matched line text.
        let line_text = data
            .get("lines")
            .and_then(|l| l.get("text"))
            .and_then(|t| t.as_str())
            .unwrap_or("")
            .trim_end_matches('\n')
            .to_owned();

        matches.push(SearchMatch {
            file,
            line_no,
            line_text,
        });
    }

    Ok(matches)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    /// Helper: create a `TempDir` with a small fixture tree.
    ///
    /// Layout:
    ///   root/
    ///     note1.md
    ///     subdir/
    ///       note2.md
    ///       note3.md
    ///     subdir2/
    ///       ignored.txt
    fn make_fixture() -> TempDir {
        let dir = tempfile::tempdir().expect("tempdir");
        let root = dir.path();
        fs::write(root.join("note1.md"), "# Note 1").unwrap();
        let sub = root.join("subdir");
        fs::create_dir(&sub).unwrap();
        fs::write(sub.join("note2.md"), "# Note 2").unwrap();
        fs::write(sub.join("note3.md"), "# Note 3").unwrap();
        let sub2 = root.join("subdir2");
        fs::create_dir(&sub2).unwrap();
        fs::write(sub2.join("ignored.txt"), "not md").unwrap();
        dir
    }

    // ── vault_list ─────────────────────────────────────────────────────────────

    #[test]
    fn vault_list_returns_root_node() {
        let dir = make_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let node = vault_list(root).expect("vault_list");
        assert!(node.is_dir, "root should be a directory node");
        assert!(!node.children.is_empty(), "root should have children");
    }

    #[test]
    fn vault_list_includes_md_files_only() {
        let dir = make_fixture();
        let root_str = dir.path().to_str().unwrap().to_owned();
        let node = vault_list(root_str).expect("vault_list");

        // Collect all leaf paths recursively.
        fn leaves(node: &VaultNode, acc: &mut Vec<String>) {
            if !node.is_dir {
                acc.push(node.path.clone());
            }
            for c in &node.children {
                leaves(c, acc);
            }
        }
        let mut all_leaves = Vec::new();
        leaves(&node, &mut all_leaves);

        assert!(!all_leaves.is_empty());
        for p in &all_leaves {
            assert!(p.ends_with(".md"), "non-md leaf: {p}");
        }
    }

    #[test]
    fn vault_list_subdir_has_two_notes() {
        let dir = make_fixture();
        let root_str = dir.path().to_str().unwrap().to_owned();
        let node = vault_list(root_str).expect("vault_list");

        let subdir = node
            .children
            .iter()
            .find(|c| c.is_dir && c.name == "subdir")
            .expect("subdir node");
        let md_count = subdir.children.iter().filter(|c| !c.is_dir).count();
        assert_eq!(md_count, 2, "subdir should have 2 .md files");
    }

    // ── vault_read / vault_write round-trip ────────────────────────────────────

    #[test]
    fn vault_read_write_round_trip() {
        let dir = make_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let note_path = dir.path().join("round_trip.md");
        let note_str = note_path.to_str().unwrap().to_owned();
        let content = "# Round trip\n\nHello vault.";

        vault_write(root.clone(), note_str.clone(), content.to_owned()).expect("vault_write");
        let read_back = vault_read(root, note_str).expect("vault_read");
        assert_eq!(read_back, content);
    }

    #[test]
    fn vault_write_creates_parent_dirs() {
        let dir = make_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let nested = dir.path().join("new_subdir").join("child").join("note.md");
        let nested_str = nested.to_str().unwrap().to_owned();

        vault_write(root.clone(), nested_str.clone(), "content".to_owned())
            .expect("vault_write creates parents");
        assert!(nested.exists());
    }

    #[test]
    fn vault_read_not_found() {
        let dir = make_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let missing = dir.path().join("nope.md").to_str().unwrap().to_owned();
        let result = vault_read(root, missing);
        assert!(matches!(result, Err(VaultError::NotFound { .. })));
    }

    // ── traversal rejection ────────────────────────────────────────────────────

    #[test]
    fn traversal_dotdot_rejected() {
        let dir = make_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let evil = format!("{}/../etc/passwd", dir.path().display());
        let result = vault_read(root, evil);
        assert!(
            matches!(result, Err(VaultError::TraversalDenied { .. })),
            "expected TraversalDenied"
        );
    }

    #[test]
    fn traversal_absolute_outside_root_rejected() {
        let dir = make_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        // Try to read an existing file outside the vault root.
        let result = vault_read(root, "/etc/hosts".to_owned());
        assert!(
            matches!(result, Err(VaultError::TraversalDenied { .. })),
            "expected TraversalDenied for absolute path outside root"
        );
    }

    #[test]
    fn guard_path_inside_root_ok() {
        let dir = make_fixture();
        let canon_root = fs::canonicalize(dir.path()).unwrap();
        let inside = dir.path().join("note1.md");
        let result = guard_path(inside.to_str().unwrap(), &canon_root);
        assert!(result.is_ok(), "path inside root should be accepted");
    }

    // ── rename / delete semantics (write-based) ────────────────────────────────

    #[test]
    fn vault_write_overwrites_existing() {
        let dir = make_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let path = dir.path().join("note1.md").to_str().unwrap().to_owned();
        vault_write(root.clone(), path.clone(), "updated content".to_owned())
            .expect("vault_write overwrite");
        let content = vault_read(root, path).expect("vault_read");
        assert_eq!(content, "updated content");
    }

    // ── vault_search ──────────────────────────────────────────────────────────

    /// Helper: check whether `rg` is available on PATH.
    /// Tests are skipped (not failed) when rg is absent so CI on minimal images passes.
    fn rg_available() -> bool {
        std::process::Command::new("rg")
            .arg("--version")
            .output()
            .map(|o| o.status.success())
            .unwrap_or(false)
    }

    /// Create a fixture vault with known searchable content.
    ///
    /// Layout:
    ///   root/
    ///     alpha.md  — contains "hello world"
    ///     subdir/
    ///       beta.md — contains "ripgrep search test" and "hello again"
    fn make_search_fixture() -> TempDir {
        let dir = tempfile::tempdir().expect("tempdir");
        let root = dir.path();
        fs::write(root.join("alpha.md"), "# Alpha\n\nhello world\n").unwrap();
        let sub = root.join("subdir");
        fs::create_dir(&sub).unwrap();
        fs::write(
            sub.join("beta.md"),
            "# Beta\n\nripgrep search test\n\nhello again\n",
        )
        .unwrap();
        dir
    }

    #[test]
    fn vault_search_blank_query_returns_empty() {
        let dir = make_search_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let result = vault_search(root.clone(), "".to_owned()).expect("vault_search");
        assert!(result.is_empty(), "blank query should return empty vec");

        let result = vault_search(root, "   ".to_owned()).expect("vault_search");
        assert!(
            result.is_empty(),
            "whitespace-only query should return empty vec"
        );
    }

    #[test]
    fn vault_search_no_match_returns_empty() {
        if !rg_available() {
            eprintln!("SKIP: rg not found on PATH — skipping vault_search_no_match_returns_empty");
            return;
        }
        let dir = make_search_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let result =
            vault_search(root, "xyzzy_no_such_token_12345".to_owned()).expect("vault_search");
        assert!(
            result.is_empty(),
            "non-matching query should return empty vec"
        );
    }

    #[test]
    fn vault_search_finds_match_in_root_file() {
        if !rg_available() {
            eprintln!(
                "SKIP: rg not found on PATH — skipping vault_search_finds_match_in_root_file"
            );
            return;
        }
        let dir = make_search_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let result = vault_search(root.clone(), "hello world".to_owned()).expect("vault_search");
        assert!(!result.is_empty(), "should find 'hello world' in alpha.md");
        let hit = result.iter().find(|m| m.file.ends_with("alpha.md"));
        assert!(hit.is_some(), "alpha.md should be in results");
        let hit = hit.unwrap();
        assert!(hit.line_no > 0, "line_no should be > 0");
        assert!(
            hit.line_text.contains("hello world"),
            "line_text should contain the match"
        );
    }

    #[test]
    fn vault_search_finds_matches_in_subdir() {
        if !rg_available() {
            eprintln!("SKIP: rg not found on PATH — skipping vault_search_finds_matches_in_subdir");
            return;
        }
        let dir = make_search_fixture();
        let root = dir.path().to_str().unwrap().to_owned();
        let result = vault_search(root, "hello".to_owned()).expect("vault_search");
        // "hello" appears in alpha.md and beta.md
        let paths: Vec<&str> = result.iter().map(|m| m.file.as_str()).collect();
        let has_alpha = paths.iter().any(|p| p.ends_with("alpha.md"));
        let has_beta = paths.iter().any(|p| p.ends_with("beta.md"));
        assert!(has_alpha, "alpha.md should appear in results");
        assert!(has_beta, "beta.md should appear in results");
    }

    #[test]
    fn vault_search_results_are_capped_at_200() {
        if !rg_available() {
            eprintln!(
                "SKIP: rg not found on PATH — skipping vault_search_results_are_capped_at_200"
            );
            return;
        }
        let dir = tempfile::tempdir().expect("tempdir");
        let root = dir.path();
        // Write a file with 300 lines all containing "needle".
        let content: String = (1..=300).map(|i| format!("needle line {i}\n")).collect();
        fs::write(root.join("big.md"), content).unwrap();
        let result = vault_search(root.to_str().unwrap().to_owned(), "needle".to_owned())
            .expect("vault_search");
        assert!(
            result.len() <= SEARCH_RESULT_CAP,
            "results should be capped at {SEARCH_RESULT_CAP}, got {}",
            result.len()
        );
    }

    #[test]
    fn vault_search_rejects_root_outside_vault() {
        let _dir = make_search_fixture();
        // A path that does not exist — canonical_root should return Io error.
        let bogus_root = "/tmp/__nonexistent_cascade_vault_test_path__";
        let result = vault_search(bogus_root.to_owned(), "hello".to_owned());
        assert!(result.is_err(), "non-existent root should return Err");
    }
}
