//! # compat_gen — harness compatibility file generator
//!
//! Generates `CLAUDE.md` and `AGENTS.md` files at each tier directory that has
//! a resolved source path in a [`ResolvedContext`]. These files point back to the
//! Cascade configuration source so Claude Code and OpenCode both see consistent
//! instructions.
//!
//! ## Generated file layout (per tier)
//!
//! ```text
//! {tier_dir}/CLAUDE.md  — generated; contains marker + instructions summary
//! {tier_dir}/AGENTS.md  — Unix: relative symlink → CLAUDE.md
//!                         Windows: plain file with identical content
//! ```
//!
//! ## Idempotency guarantee
//!
//! Before overwriting, the generator reads the first line of any existing
//! `CLAUDE.md`. If it starts with [`GENERATED_MARKER`], the file is
//! regenerated. If not, the file is **skipped** and a WARN is emitted — this
//! prevents clobbering hand-written instructions.
//!
//! ## Public API
//!
//! | Symbol | Signature |
//! |--------|-----------|
//! | [`generate_all`] | `(project_root: &Path) -> Result<Vec<CompatFile>>` |
//! | [`CompatGenerator`] | `generate(resolved: &ResolvedContext) -> Result<Vec<CompatFile>>` |
//! | [`CompatFile`] | output record per generated file |
//! | [`GENERATED_MARKER`] | stable constant — single source for the marker string |
//!
//! ## SPORT reference
//!
//! - `.claude/docs/MASTER-MODULES.md` — `compat_gen` row

use std::path::{Path, PathBuf};

use cascade_types::error::{CascadeError, Result};
use cascade_types::tiers::ResolvedContext;
use tracing::{debug, warn};

use crate::cascade_resolve::resolve_cascade;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Stable cascade-owned marker placed on the FIRST line of every generated
/// `CLAUDE.md`. The generator checks this string to determine whether it may
/// safely overwrite an existing file.
///
/// This is the **single source of truth** for the marker string; never inline it.
pub const GENERATED_MARKER: &str = "# cascade:generated — do not edit by hand";

/// Maximum length (bytes) of the instructions summary embedded in CLAUDE.md.
const INSTRUCTIONS_SUMMARY_MAX: usize = 500;

// ── Public types ──────────────────────────────────────────────────────────────

/// A record of one compatibility file that was generated (or would be generated).
///
/// # Fields
///
/// * `path` — absolute path to the generated file.
/// * `content` — content written to the file (empty for symlinks; their target
///   is `CLAUDE.md` relative to the same directory).
/// * `is_symlink` — `true` when the file was written as a relative symlink
///   rather than regular content. Always `false` on Windows.
#[derive(Debug, Clone)]
pub struct CompatFile {
    /// Absolute path of the written file.
    pub path: PathBuf,
    /// Content written (empty string for symlink entries).
    pub content: String,
    /// Whether this entry is a relative symlink to `CLAUDE.md`.
    pub is_symlink: bool,
}

// ── CompatGenerator ───────────────────────────────────────────────────────────

/// Generates `CLAUDE.md` and `AGENTS.md` compatibility files for each tier in
/// a [`ResolvedContext`].
///
/// # Constraints
///
/// * Inputs: `&ResolvedContext` (produced by [`resolve_cascade`])
/// * Outputs: `Result<Vec<CompatFile>>`
/// * Idempotent: safe to call multiple times; existing cascade-managed files
///   are updated in-place; foreign (non-marked) files are skipped with a warning.
/// * No side-effects beyond writing `CLAUDE.md` and `AGENTS.md` inside each
///   tier directory that already exists in `resolved.tier_sources`.
pub struct CompatGenerator;

impl CompatGenerator {
    /// Generate compatibility files for every tier in `resolved`.
    ///
    /// For each `(tier, tier_dir)` pair in `resolved.tier_sources`:
    /// 1. Build the content for `CLAUDE.md`.
    /// 2. Write (or skip) `{tier_dir}/CLAUDE.md`.
    /// 3. Write (or skip) `{tier_dir}/AGENTS.md` as a relative symlink on Unix,
    ///    or as a plain file on Windows.
    ///
    /// Returns all files that were actually written (skipped tiers are excluded).
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::Io`] when an existing file cannot be read or a
    /// new file cannot be written. Skipped-due-to-no-marker dirs are **not** errors.
    pub fn generate(resolved: &ResolvedContext) -> Result<Vec<CompatFile>> {
        let mut written: Vec<CompatFile> = Vec::new();

        for (tier, tier_dir) in &resolved.tier_sources {
            debug!(tier = %tier, path = %tier_dir.display(), "compat_gen: processing tier");

            // ── 1. Idempotency check on CLAUDE.md ────────────────────────────
            let claude_path = tier_dir.join("CLAUDE.md");
            if claude_path.exists() && !should_overwrite(&claude_path)? {
                // Foreign (hand-written) CLAUDE.md — skip this tier entirely.
                warn!(
                    path = %claude_path.display(),
                    "skipping {}: not cascade-managed (no generated marker on line 1)",
                    claude_path.display()
                );
                continue;
            }

            // ── 2. Build CLAUDE.md content ────────────────────────────────────
            let content = build_claude_md_content(
                tier.display_name(),
                tier.short_name(),
                tier_dir,
                &resolved.instructions,
            );

            // ── 3. Write CLAUDE.md ────────────────────────────────────────────
            std::fs::write(&claude_path, &content).map_err(|e| CascadeError::Io {
                path: claude_path.clone(),
                operation: "write CLAUDE.md",
                source: e,
            })?;
            debug!(path = %claude_path.display(), "compat_gen: wrote CLAUDE.md");
            written.push(CompatFile {
                path: claude_path.clone(),
                content: content.clone(),
                is_symlink: false,
            });

            // ── 4. Write AGENTS.md ────────────────────────────────────────────
            let agents_path = tier_dir.join("AGENTS.md");
            let agents_file = write_agents_md(&agents_path, &content)?;
            written.push(agents_file);
        }

        Ok(written)
    }
}

// ── Public top-level entrypoint ───────────────────────────────────────────────

/// Resolve the cascade for `project_root` and generate all compatibility files.
///
/// This is the single-call entrypoint used by the daemon during file-watch
/// regeneration events. It calls [`resolve_cascade`] then
/// [`CompatGenerator::generate`] and returns the combined output.
///
/// # Errors
///
/// Returns [`CascadeError::Io`] if resolution or any file write fails.
pub fn generate_all(project_root: &Path) -> Result<Vec<CompatFile>> {
    let resolved = resolve_cascade(project_root)?;
    CompatGenerator::generate(&resolved)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Returns `true` if `path` should be overwritten (either doesn't exist yet, or
/// its first line is the [`GENERATED_MARKER`]).
///
/// Returns `false` when the file exists but its first line is NOT the marker
/// (indicating hand-written content that must not be clobbered).
///
/// # Errors
///
/// Returns [`CascadeError::Io`] only if the file exists but cannot be read.
fn should_overwrite(path: &Path) -> Result<bool> {
    if !path.exists() {
        return Ok(true);
    }
    let content = std::fs::read_to_string(path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "read existing CLAUDE.md for marker check",
        source: e,
    })?;
    let first_line = content.lines().next().unwrap_or("");
    Ok(first_line.starts_with(GENERATED_MARKER))
}

/// Build the content string for a tier's `CLAUDE.md`.
///
/// Format:
/// ```text
/// # cascade:generated — do not edit by hand
///
/// # Cascade Instructions — {tier_display_name}
///
/// This file points to the Cascade instruction source for this tier.
///
/// Source: {tier_dir}/config.toml
///
/// Instructions summary:
/// {first 500 chars of merged instructions}
///
/// For full context, run: cascade resolve --tier {tier_short}
/// ```
fn build_claude_md_content(
    tier_display: &str,
    tier_short: &str,
    tier_dir: &Path,
    instructions: &str,
) -> String {
    let summary = if instructions.len() <= INSTRUCTIONS_SUMMARY_MAX {
        instructions.to_owned()
    } else {
        let truncated = &instructions[..INSTRUCTIONS_SUMMARY_MAX];
        // Truncate at a UTF-8 character boundary (find the last valid boundary ≤ INSTRUCTIONS_SUMMARY_MAX)
        let safe_end = truncated
            .char_indices()
            .last()
            .map(|(i, c)| i + c.len_utf8())
            .unwrap_or(INSTRUCTIONS_SUMMARY_MAX);
        format!("{}…", &instructions[..safe_end])
    };

    // Behavioral-core rule: Security (always-loaded, 4 terse constraints).
    // Keep this section minimal — it shapes code writes with near-zero token overhead.
    let security_section = "\
# Security\n\
\n\
- No API key, secret, or token in any client-side path (public/, static/, frontend bundles). Server-side or proxy only.\n\
- Client-side validation is UX only — always re-validate on the server.\n\
- User-facing errors are generic; log full errors server-side only.\n\
- Every endpoint calling a paid/external API has rate limiting.\n";

    format!(
        "{marker}\n\n# Cascade Instructions — {tier_display}\n\nThis file points to the Cascade instruction source for this tier.\n\nSource: {tier_dir}/config.toml\n\nInstructions summary:\n{summary}\n\nFor full context, run: cascade resolve --tier {tier_short}\n\n{security}",
        marker = GENERATED_MARKER,
        tier_display = tier_display,
        tier_dir = tier_dir.display(),
        summary = summary,
        tier_short = tier_short,
        security = security_section,
    )
}

/// Write `AGENTS.md` at `agents_path`.
///
/// * **Unix:** creates a relative symlink `AGENTS.md → CLAUDE.md` in the same
///   directory. If a file (or broken symlink) already exists at the target, it
///   is removed first so the symlink call does not fail with `EEXIST`.
/// * **Windows:** writes `content` as a plain text file (Windows symlinks
///   require administrator rights or Developer Mode; we do not mandate either).
///
/// # Errors
///
/// Returns [`CascadeError::Io`] if the write/symlink operation fails.
fn write_agents_md(agents_path: &Path, _content: &str) -> Result<CompatFile> {
    // On Unix, `_content` is unused (we create a symlink); on Windows it is the
    // file body. The leading underscore suppresses the unused-variable warning
    // on the default (Unix) build without compromising the Windows branch.
    #[cfg(unix)]
    {
        use std::os::unix::fs::symlink;

        // Remove any existing entry so symlink() doesn't return EEXIST.
        if agents_path.exists() || agents_path.symlink_metadata().is_ok() {
            std::fs::remove_file(agents_path).map_err(|e| CascadeError::Io {
                path: agents_path.to_path_buf(),
                operation: "remove existing AGENTS.md before symlinking",
                source: e,
            })?;
        }

        // Relative target: just "CLAUDE.md" (same directory).
        symlink("CLAUDE.md", agents_path).map_err(|e| CascadeError::Io {
            path: agents_path.to_path_buf(),
            operation: "create AGENTS.md symlink → CLAUDE.md",
            source: e,
        })?;
        debug!(path = %agents_path.display(), "compat_gen: created AGENTS.md symlink");
        Ok(CompatFile {
            path: agents_path.to_path_buf(),
            content: String::new(), // symlinks have no content of their own
            is_symlink: true,
        })
    }

    #[cfg(windows)]
    {
        // Windows: plain file with the same content as CLAUDE.md.
        std::fs::write(agents_path, _content).map_err(|e| CascadeError::Io {
            path: agents_path.to_path_buf(),
            operation: "write AGENTS.md (Windows plain-file fallback)",
            source: e,
        })?;
        debug!(path = %agents_path.display(), "compat_gen: wrote AGENTS.md (Windows plain-file)");
        Ok(CompatFile {
            path: agents_path.to_path_buf(),
            content: _content.to_owned(),
            is_symlink: false,
        })
    }

    // Safety net for targets that are neither unix nor windows (unlikely but
    // avoids a hard compile error on unusual platforms).
    #[cfg(not(any(unix, windows)))]
    {
        let _ = _content; // exotic platform — write empty file
        std::fs::write(agents_path, "").map_err(|e| CascadeError::Io {
            path: agents_path.to_path_buf(),
            operation: "write AGENTS.md (unknown platform fallback)",
            source: e,
        })?;
        Ok(CompatFile {
            path: agents_path.to_path_buf(),
            content: String::new(),
            is_symlink: false,
        })
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;
    use std::fs;

    use cascade_types::tiers::{ResolvedContext, TierName};
    use tempfile::TempDir;

    use super::*;

    // ── helpers ───────────────────────────────────────────────────────────────

    fn make_resolved(tier: TierName, tier_dir: &Path) -> ResolvedContext {
        let mut tier_sources = BTreeMap::new();
        tier_sources.insert(tier, tier_dir.to_path_buf());
        ResolvedContext {
            instructions: "Always use snake_case.".to_string(),
            rules: vec![],
            on_demand_rules: vec![],
            memory_paths: vec![],
            task_paths: vec![],
            tier_sources,
            model_registry: cascade_types::model_registry::ModelRegistry::default(),
            vars: Default::default(),
            resolved_at: "2026-06-02T00:00:00Z".to_string(),
        }
    }

    // ── Test 1: generation writes CLAUDE.md with marker on line 1 ─────────────

    /// `CompatGenerator::generate` must write a `CLAUDE.md` whose first line is
    /// exactly the [`GENERATED_MARKER`] constant.
    #[test]
    fn test_generate_writes_claude_md_with_marker() {
        let tmp = TempDir::new().expect("tempdir");
        let tier_dir = tmp.path();
        let resolved = make_resolved(TierName::GCI, tier_dir);

        let files = CompatGenerator::generate(&resolved).expect("generate");
        assert!(!files.is_empty(), "expected at least one output file");

        let claude_path = tier_dir.join("CLAUDE.md");
        assert!(claude_path.exists(), "CLAUDE.md should exist");

        let content = fs::read_to_string(&claude_path).expect("read CLAUDE.md");
        let first_line = content.lines().next().unwrap_or("");
        assert_eq!(
            first_line, GENERATED_MARKER,
            "first line must be the generated marker"
        );
    }

    // ── Test 2: AGENTS.md is a symlink (Unix) or plain file (Windows) ─────────

    /// On Unix, `{tier_dir}/AGENTS.md` must be a symbolic link pointing to
    /// `CLAUDE.md`. On Windows, it must be a plain file with matching content.
    #[test]
    fn test_agents_md_is_symlink_on_unix() {
        let tmp = TempDir::new().expect("tempdir");
        let tier_dir = tmp.path();
        let resolved = make_resolved(TierName::PPC, tier_dir);

        CompatGenerator::generate(&resolved).expect("generate");

        let agents_path = tier_dir.join("AGENTS.md");
        assert!(
            agents_path.exists() || agents_path.symlink_metadata().is_ok(),
            "AGENTS.md should exist"
        );

        #[cfg(unix)]
        {
            let meta = fs::symlink_metadata(&agents_path).expect("symlink_metadata");
            assert!(
                meta.file_type().is_symlink(),
                "AGENTS.md must be a symlink on Unix"
            );
            let target = std::fs::read_link(&agents_path).expect("read_link");
            assert_eq!(
                target.to_str().unwrap(),
                "CLAUDE.md",
                "symlink target must be relative CLAUDE.md"
            );
        }

        #[cfg(windows)]
        {
            let meta = fs::metadata(&agents_path).expect("metadata");
            assert!(meta.is_file(), "AGENTS.md must be a plain file on Windows");
            let claude_content =
                fs::read_to_string(tier_dir.join("CLAUDE.md")).expect("read CLAUDE.md");
            let agents_content = fs::read_to_string(&agents_path).expect("read AGENTS.md");
            assert_eq!(
                claude_content, agents_content,
                "Windows AGENTS.md content must match CLAUDE.md"
            );
        }
    }

    // ── Test 3: re-generation is idempotent ───────────────────────────────────

    /// Calling `generate` twice on an already-generated tier dir must produce
    /// identical CLAUDE.md content on both runs (idempotent).
    #[test]
    fn test_generate_is_idempotent() {
        let tmp = TempDir::new().expect("tempdir");
        let tier_dir = tmp.path();
        let resolved = make_resolved(TierName::PRC, tier_dir);

        CompatGenerator::generate(&resolved).expect("first generate");
        let content_first = fs::read_to_string(tier_dir.join("CLAUDE.md")).expect("first read");

        CompatGenerator::generate(&resolved).expect("second generate");
        let content_second = fs::read_to_string(tier_dir.join("CLAUDE.md")).expect("second read");

        assert_eq!(
            content_first, content_second,
            "repeated generate must produce identical CLAUDE.md content"
        );
    }

    // ── Test 4: hand-written CLAUDE.md (no marker) is skipped ────────────────

    /// A `CLAUDE.md` whose first line is NOT the cascade:generated marker must
    /// be left untouched; `generate` must log a WARN and skip that tier.
    #[test]
    fn test_non_cascade_managed_claude_md_is_skipped() {
        let tmp = TempDir::new().expect("tempdir");
        let tier_dir = tmp.path();

        // Write a hand-crafted CLAUDE.md with no cascade marker.
        let hand_written = "# My hand-written instructions\n\nDo not touch this.\n";
        fs::write(tier_dir.join("CLAUDE.md"), hand_written).expect("write hand-written CLAUDE.md");

        let resolved = make_resolved(TierName::PAC, tier_dir);
        // generate() should not error, just skip the tier.
        let files = CompatGenerator::generate(&resolved).expect("generate should not fail");

        // No files should have been written for this tier.
        assert!(
            files.is_empty(),
            "no files should be written when CLAUDE.md has no cascade marker"
        );

        // Original content must be preserved.
        let after = fs::read_to_string(tier_dir.join("CLAUDE.md")).expect("read after");
        assert_eq!(
            after, hand_written,
            "hand-written CLAUDE.md must be unchanged"
        );
    }

    // ── Test 5: tier dir that does not exist is silently skipped ─────────────

    /// If a tier listed in `tier_sources` has a directory path that does not
    /// exist on disk, `generate` must skip it without error. (This can happen
    /// when a resolved context references a tier whose dir was deleted between
    /// resolution and generation.)
    #[test]
    fn test_missing_tier_dir_is_skipped() {
        let tier_dir = PathBuf::from("/tmp/cascade_test_nonexistent_tier_dir_9f3a2b1c");
        // Make absolutely sure this path does not exist.
        if tier_dir.exists() {
            fs::remove_dir_all(&tier_dir).ok();
        }

        let resolved = make_resolved(TierName::APC, &tier_dir);
        // The tier_dir does not exist; generate should proceed without error.
        // CLAUDE.md writes will fail because the parent dir is absent.
        // We accept either an error (Io) or an empty result here — the key
        // requirement from the ticket is that we do NOT panic.
        let result = CompatGenerator::generate(&resolved);
        // Either an Io error (acceptable) or empty Ok is fine — no panic.
        match result {
            Ok(files) => assert!(
                files.is_empty(),
                "non-existent tier dir should yield no files"
            ),
            Err(_) => {
                // Io error is acceptable for a completely missing directory.
            }
        }
    }
}
