//! # import_expand — `@import` expansion for cascade instruction text
//!
//! Scans instruction text for `@path` import lines and replaces them with the
//! referenced file's contents, enabling modular instruction authoring that
//! mirrors the local CLAUDE.md `@RTK.md` convention.
//!
//! ## Syntax
//!
//! A line is treated as an `@import` directive when ALL of the following hold:
//!
//! 1. The first non-whitespace character on the line is `@`.
//! 2. The token immediately following `@` (up to the first whitespace or
//!    end-of-line) forms a non-empty path string.
//! 3. The resulting path, resolved relative to [`base_dir`], refers to an
//!    existing, readable file.
//!
//! Lines that contain `@` anywhere other than the leading position are **not**
//! treated as imports — email addresses (`a@b.com`), mid-line references, and
//! inline code containing `@` are left untouched.
//!
//! ## Resolution base directory
//!
//! Every `@path` is resolved relative to the `base_dir` passed to
//! [`expand_imports`]. The caller should supply the `.cascade/` directory of
//! the tier that owns the instruction text, so tier-local references work
//! correctly regardless of the working directory.
//!
//! ## Nesting and cycle detection
//!
//! Imported files are themselves scanned for `@import` lines (recursive
//! expansion). A [`HashSet`] of canonical paths tracks already-visited files.
//! When a cycle is detected the offending `@path` line is left verbatim and a
//! `tracing::warn!` is emitted — the expansion does **not** infinite-loop.
//!
//! A hard `MAX_DEPTH` of 16 prevents pathological nesting; imports at or beyond
//! that depth are left verbatim with a warning.
//!
//! ## Missing / unreadable files
//!
//! When a referenced file cannot be read (missing, permission denied, etc.) the
//! `@path` line is left verbatim and a `tracing::warn!` is emitted. The overall
//! resolution is never aborted — `expand_imports` is infallible by design.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: import_expand module (P12-import-expansion)

use std::collections::HashSet;
use std::path::{Path, PathBuf};

use tracing::warn;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Maximum recursion depth for nested `@import` expansion.
///
/// Imports at this depth or deeper are left verbatim rather than expanded.
/// A depth of 16 is generous for any realistic instruction layout; it exists
/// solely to prevent runaway nesting bugs.
const MAX_DEPTH: usize = 16;

// ── Public API ─────────────────────────────────────────────────────────────────

/// Expand all `@path` import directives in `text`.
///
/// Scans every line of `text`; lines whose first non-whitespace token starts
/// with `@` and whose remainder resolves to a readable file under `base_dir`
/// are replaced by the contents of that file (trimmed).  Nested imports are
/// expanded recursively with cycle detection and a depth cap.
///
/// # Arguments
///
/// * `text`     — the instruction text to scan.
/// * `base_dir` — directory used to resolve relative `@path` references.
///   Callers should pass the `.cascade/` directory of the contributing tier.
///
/// # Returns
///
/// The expanded text with all resolvable `@import` lines replaced.
/// The function is infallible: unresolvable references are left verbatim.
pub fn expand_imports(text: &str, base_dir: &Path) -> String {
    let mut visited: HashSet<PathBuf> = HashSet::new();
    expand_inner(text, base_dir, &mut visited, 0)
}

// ── Implementation ─────────────────────────────────────────────────────────────

/// Recursive expansion worker.
///
/// # Arguments
///
/// * `text`    — text to scan for `@import` lines.
/// * `base`    — directory against which relative paths are resolved.
/// * `visited` — set of canonical paths already on the current expansion stack
///   (cycle guard — NOT a global "seen" cache; identical imports in distinct
///   branches are each expanded independently).
/// * `depth`   — current recursion depth (starts at 0).
fn expand_inner(text: &str, base: &Path, visited: &mut HashSet<PathBuf>, depth: usize) -> String {
    let mut out = String::with_capacity(text.len());
    let mut first = true;

    for line in text.lines() {
        if !first {
            out.push('\n');
        }
        first = false;

        // Detect a leading `@path` token.
        let trimmed = line.trim_start();
        if let Some(path_token) = leading_import_token(trimmed) {
            // Attempt to expand this directive.
            match try_expand_line(path_token, base, visited, depth) {
                ExpandResult::Expanded(content) => {
                    out.push_str(&content);
                }
                ExpandResult::Verbatim(reason) => {
                    // Log why we are leaving the line verbatim.
                    warn!(line = line, reason = reason, "leaving @import verbatim");
                    out.push_str(line);
                }
            }
        } else {
            out.push_str(line);
        }
    }

    out
}

/// Result of attempting to expand a single `@import` directive.
enum ExpandResult {
    /// Successful expansion — the string is the inlined file content.
    Expanded(String),
    /// Could not expand; leave the original line and emit a warning.
    Verbatim(&'static str),
}

/// Try to expand a resolved import path.
///
/// Returns `ExpandResult::Expanded` with the (recursively expanded) file
/// contents on success, or `ExpandResult::Verbatim` with an explanation on
/// any failure.
fn try_expand_line(
    path_token: &str,
    base: &Path,
    visited: &mut HashSet<PathBuf>,
    depth: usize,
) -> ExpandResult {
    if depth >= MAX_DEPTH {
        return ExpandResult::Verbatim("max nesting depth reached");
    }

    // Resolve the path relative to base.
    let raw_path = Path::new(path_token);
    let candidate = if raw_path.is_absolute() {
        raw_path.to_path_buf()
    } else {
        base.join(raw_path)
    };

    // Canonicalise to detect cycles and normalise `..` components.
    let canonical = match candidate.canonicalize() {
        Ok(c) => c,
        Err(_) => {
            // File does not exist or cannot be canonicalised.
            return ExpandResult::Verbatim("file not found or not readable");
        }
    };

    // Cycle guard.
    if visited.contains(&canonical) {
        return ExpandResult::Verbatim("cycle detected");
    }

    // Read the file.
    let content = match std::fs::read_to_string(&canonical) {
        Ok(c) => c,
        Err(_) => {
            return ExpandResult::Verbatim("file read failed");
        }
    };

    // Descend — mark as visited before recursing.
    visited.insert(canonical.clone());

    // Determine the new base_dir for nested imports: the directory of the
    // imported file, so relative paths inside it resolve from its location.
    let nested_base = canonical
        .parent()
        .map(|p| p.to_path_buf())
        .unwrap_or_else(|| base.to_path_buf());

    let expanded = expand_inner(content.trim(), &nested_base, visited, depth + 1);

    // Remove from visited after recursion so sibling imports of the same file
    // are allowed (cycle = A imports A, not A imports B then B imports A
    // within the *same stack*).
    visited.remove(&canonical);

    ExpandResult::Expanded(expanded)
}

/// Extract the import path token from a leading `@` line.
///
/// Returns `Some(path)` when `line` (already left-trimmed) starts with `@`
/// followed by at least one non-whitespace character that does not look like
/// an email address or mention.
///
/// Rules:
/// - Must begin with exactly `@`.
/// - The token after `@` (up to first whitespace or end of string) must be
///   non-empty.
/// - The token must not contain `@` itself (rejects email-like tokens such as
///   `user@host` even if the line starts with `@`).
/// - The token must contain at least one path character (letter, digit, `/`,
///   `.`, `-`, `_`) — pure symbols like `@@` are rejected.
///
/// This is deliberately conservative: a line like `@see also …` where the
/// token `see` has no path separator will resolve against `base_dir` and, if
/// no file named `see` exists there, fall through to verbatim — which is
/// harmless.  The real guard against false positives is the file-existence
/// check in [`try_expand_line`].
fn leading_import_token(line: &str) -> Option<&str> {
    // Must start with `@`.
    let after_at = line.strip_prefix('@')?;

    // Token = everything up to the first whitespace.
    let token = after_at.split_whitespace().next().unwrap_or("");

    if token.is_empty() {
        return None;
    }

    // Reject tokens that themselves contain `@` (email addresses, mentions).
    if token.contains('@') {
        return None;
    }

    // Token must contain at least one path-like character.
    if !token
        .chars()
        .any(|c| c.is_alphanumeric() || matches!(c, '/' | '.' | '-' | '_'))
    {
        return None;
    }

    Some(token)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    // ── helpers ───────────────────────────────────────────────────────────────

    fn write(dir: &Path, name: &str, content: &str) -> PathBuf {
        let p = dir.join(name);
        fs::write(&p, content).unwrap();
        p
    }

    // ── test 1: simple import expands ─────────────────────────────────────────

    /// A single `@filename` line is replaced with the file's contents.
    #[test]
    fn simple_import_expands() {
        let tmp = TempDir::new().unwrap();
        write(
            tmp.path(),
            "rules.md",
            "# Imported rules\n\nDo the right thing.",
        );

        let text = "@rules.md\n\nOther text.";
        let result = expand_imports(text, tmp.path());
        assert!(
            result.contains("# Imported rules"),
            "imported content must appear: {result}"
        );
        assert!(
            result.contains("Other text."),
            "non-import line must be preserved: {result}"
        );
        assert!(
            !result.starts_with("@rules.md"),
            "original @import line must be replaced: {result}"
        );
    }

    // ── test 2: nested import expands ─────────────────────────────────────────

    /// A file that itself contains an `@import` line is recursively expanded.
    #[test]
    fn nested_import_expands() {
        let tmp = TempDir::new().unwrap();
        write(tmp.path(), "leaf.md", "Leaf content.");
        write(tmp.path(), "mid.md", "@leaf.md\nMid content.");

        let text = "@mid.md";
        let result = expand_imports(text, tmp.path());
        assert!(
            result.contains("Leaf content."),
            "deeply nested content must appear: {result}"
        );
        assert!(
            result.contains("Mid content."),
            "intermediate content must appear: {result}"
        );
    }

    // ── test 3: cycle is detected, left verbatim, no hang ────────────────────

    /// File A imports file B which imports file A → cycle; both `@` lines are
    /// left verbatim and the function returns without infinite-looping.
    #[test]
    fn cycle_detected_left_verbatim_no_hang() {
        let tmp = TempDir::new().unwrap();
        // a.md imports b.md; b.md imports a.md — direct cycle.
        write(tmp.path(), "a.md", "A start\n@b.md\nA end");
        write(tmp.path(), "b.md", "B start\n@a.md\nB end");

        let text = "@a.md";
        // Must complete without hanging.
        let result = expand_imports(text, tmp.path());

        // a.md's body must be inlined.
        assert!(
            result.contains("A start"),
            "A content must appear: {result}"
        );
        // b.md's body must be inlined (first expansion of b succeeds).
        assert!(
            result.contains("B start"),
            "B content must appear: {result}"
        );
        // The cyclic `@a.md` inside b.md must be left verbatim.
        assert!(
            result.contains("@a.md"),
            "cyclic @import must be left verbatim: {result}"
        );
    }

    // ── test 4: missing file left verbatim ───────────────────────────────────

    /// A `@path` that refers to a non-existent file is left verbatim.
    #[test]
    fn missing_file_left_verbatim() {
        let tmp = TempDir::new().unwrap();
        let text = "Before\n@nonexistent.md\nAfter";
        let result = expand_imports(text, tmp.path());
        assert!(
            result.contains("@nonexistent.md"),
            "missing @import must be left verbatim: {result}"
        );
        assert!(result.contains("Before"), "before text preserved: {result}");
        assert!(result.contains("After"), "after text preserved: {result}");
    }

    // ── test 5: email addresses are NOT treated as imports ───────────────────

    /// Lines like `contact: a@b.com` are not import directives.
    #[test]
    fn email_address_not_an_import() {
        let tmp = TempDir::new().unwrap();
        // Create a file named "b.com" so the check cannot pass due to missing file.
        // The token-validation step must reject it before file lookup.
        write(tmp.path(), "b.com", "should not appear");

        let text = "Contact: a@b.com\nSend mail to user@example.org";
        let result = expand_imports(text, tmp.path());
        // No expansion should occur; both lines preserved literally.
        assert_eq!(result, text, "email lines must be unchanged: {result}");
        assert!(
            !result.contains("should not appear"),
            "file content must not be inlined: {result}"
        );
    }

    // ── test 6: mid-line @ is NOT an import ──────────────────────────────────

    /// A line that contains `@` but not as the very first non-whitespace
    /// character is not treated as an import.
    #[test]
    fn mid_line_at_not_an_import() {
        let tmp = TempDir::new().unwrap();
        write(tmp.path(), "rules.md", "should not appear");

        // `@` appears mid-line (after "See ").
        let text = "See @rules.md for details.";
        let result = expand_imports(text, tmp.path());
        assert_eq!(result, text, "mid-line @ must not trigger import: {result}");
    }

    // ── test 7: no @ lines → text unchanged ──────────────────────────────────

    /// Instructions without any `@` lines pass through unchanged.
    #[test]
    fn no_import_lines_unchanged() {
        let tmp = TempDir::new().unwrap();
        let text = "# Instructions\n\nDo the right thing.\n\nBe helpful.";
        let result = expand_imports(text, tmp.path());
        assert_eq!(result, text);
    }

    // ── test 8: max depth guard ───────────────────────────────────────────────

    /// Imports that would exceed MAX_DEPTH are left verbatim.
    ///
    /// We build a 3-level chain and artificially inject a simulated depth
    /// overflow by testing `leading_import_token` then `try_expand_line` at
    /// MAX_DEPTH directly.
    #[test]
    fn max_depth_guard_verbatim() {
        let tmp = TempDir::new().unwrap();
        write(tmp.path(), "deep.md", "deep content");

        let mut visited = HashSet::new();
        // Simulate calling at exactly MAX_DEPTH — must return Verbatim.
        let result = try_expand_line("deep.md", tmp.path(), &mut visited, MAX_DEPTH);
        assert!(
            matches!(result, ExpandResult::Verbatim("max nesting depth reached")),
            "at MAX_DEPTH must return Verbatim"
        );
    }

    // ── test 9: leading_import_token rejects edge cases ───────────────────────

    /// Unit tests for the token parser directly.
    #[test]
    fn leading_import_token_edge_cases() {
        // Valid
        assert_eq!(leading_import_token("@rules.md"), Some("rules.md"));
        assert_eq!(
            leading_import_token("@sub/dir/file.md"),
            Some("sub/dir/file.md")
        );
        assert_eq!(leading_import_token("@RTK.md rest"), Some("RTK.md"));

        // Invalid — email
        assert_eq!(leading_import_token("@user@host.com"), None);

        // Invalid — bare @ with no token
        assert_eq!(leading_import_token("@ "), None);
        assert_eq!(leading_import_token("@"), None);

        // Invalid — only symbols after @
        assert_eq!(leading_import_token("@!#$"), None);
    }

    // ── test 10: indented @import line expands ────────────────────────────────

    /// A leading `@` with whitespace before it (e.g. inside a block) still
    /// expands, because we trim_start before checking.
    #[test]
    fn indented_import_expands() {
        let tmp = TempDir::new().unwrap();
        write(tmp.path(), "extra.md", "Extra rules.");

        // Leading spaces before @
        let text = "  @extra.md";
        let result = expand_imports(text, tmp.path());
        assert!(
            result.contains("Extra rules."),
            "indented @import must expand: {result}"
        );
    }
}
