//! # lint_dangling — dangling / broken reference detector
//!
//! Scans the resolved tier instruction texts for file-system references that no
//! longer resolve to an existing path on disk.
//!
//! ## Purpose
//!
//! Cascade instruction files regularly embed pointers to companion files using
//! one of three syntactic forms:
//!
//! 1. **Arrow pointer** — `-> some/path.md` or `-> ./some/path.md`
//! 2. **`@import` directive** — `@some/path.md` (leading `@` token)
//! 3. **`load_when` file reference** — `load_when: some/path.md`
//!
//! Over time those files may be renamed, moved, or deleted, leaving stale
//! pointers in the instruction text.  This module surfaces them without
//! performing any writes or repairs.
//!
//! ## Inputs / Outputs
//!
//! - **Input:** `&ResolvedCascade` — the fully resolved cascade; each
//!   [`TierResult`] supplies the instruction text AND the `path_searched`
//!   directory that acts as the base for relative-path resolution.
//! - **Output:** `Vec<DanglingFinding>` — one entry per broken reference;
//!   empty when all referenced paths exist.
//!
//! ## Constraints
//!
//! - Absolute paths are checked as-is.
//! - Relative paths are resolved against the tier's `.cascade/` directory
//!   (`path_searched`).
//! - The check is read-only and pure w.r.t. the resolver.
//! - No `unwrap()` outside `#[cfg(test)]` blocks.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: lint_dangling module (P12-doctor-drift)

use std::path::{Path, PathBuf};

use cascade_types::cascade_tier::CascadeTier;
use regex::Regex;

use crate::cascade_resolution::ResolvedCascade;

// ── Public types ──────────────────────────────────────────────────────────────

/// A single dangling-reference finding.
///
/// Carries the tier that owns the broken reference, the referenced path string
/// exactly as it appeared in the instruction text, and the resolved (absolute)
/// path that was checked.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DanglingFinding {
    /// The tier whose instruction text contains the broken reference.
    pub tier: CascadeTier,
    /// The path token as it appeared in the instruction text.
    pub raw_ref: String,
    /// The absolute path that was checked and does not exist.
    pub resolved_path: PathBuf,
}

// ── Entry point ───────────────────────────────────────────────────────────────

/// Scan a resolved cascade for dangling file-system references.
///
/// For every found tier, extract path-like tokens from the instruction text
/// using the three recognised pointer syntaxes, resolve each relative to the
/// tier's base directory, and record those that do not exist on disk.
///
/// # Arguments
///
/// * `resolved` — the fully resolved cascade, as returned by
///   [`crate::cascade_resolution::resolve_cascade_full`].
///
/// # Returns
///
/// `Vec<DanglingFinding>` — empty when all referenced paths exist (or when no
/// path references are found).
pub fn lint_dangling(resolved: &ResolvedCascade) -> Vec<DanglingFinding> {
    let mut findings = Vec::new();

    for tier in &resolved.tiers_found {
        if !tier.found || tier.instructions.trim().is_empty() {
            continue;
        }

        // Determine the base directory for relative-path resolution.
        // `path_searched` is the `.cascade/` directory for found tiers.
        let base_dir: PathBuf = if tier.path_searched.is_file() {
            tier.path_searched
                .parent()
                .map(|p| p.to_path_buf())
                .unwrap_or_else(|| tier.path_searched.clone())
        } else {
            tier.path_searched.clone()
        };

        for raw_ref in extract_path_refs(&tier.instructions) {
            let resolved_path = resolve_ref(&raw_ref, &base_dir);
            if !resolved_path.exists() {
                findings.push(DanglingFinding {
                    tier: tier.tier,
                    raw_ref,
                    resolved_path,
                });
            }
        }
    }

    findings
}

// ── Reference extraction ──────────────────────────────────────────────────────

/// Extract all path-like references from `text` using the three recognised
/// pointer syntaxes.
///
/// Returns a deduplicated list of raw path strings exactly as they appear in
/// the text (before resolution).
fn extract_path_refs(text: &str) -> Vec<String> {
    // Regex patterns — compiled once per call (files are small; no perf issue).
    // Pattern 1: arrow pointer  `-> path/to/file` (optional leading `./`)
    // Pattern 2: @import        `@path/to/file`   (leading @, first token)
    // Pattern 3: load_when ref  `load_when: path/to/file`
    //
    // We match only tokens that look like relative or absolute file paths:
    // contain a `.` or `/` and end before whitespace or end-of-line.
    // Bare words (e.g. `-> detail`) that contain no slash or dot are skipped
    // because they are prose pointers, not filesystem references.

    let arrow_re = Regex::new(r"(?m)^[[:blank:]]*->[[:blank:]]+([^\s]+\.[^\s]+|/[^\s]+)").unwrap();
    let import_re = Regex::new(r"(?m)^[[:blank:]]*@([^\s@][^\s]*)").unwrap();
    let load_when_re = Regex::new(r"(?i)load_when:[[:blank:]]*([^\s]+\.[^\s]+|/[^\s]+)").unwrap();

    let mut refs: Vec<String> = Vec::new();

    for cap in arrow_re.captures_iter(text) {
        refs.push(cap[1].to_owned());
    }
    for cap in import_re.captures_iter(text) {
        refs.push(cap[1].to_owned());
    }
    for cap in load_when_re.captures_iter(text) {
        refs.push(cap[1].to_owned());
    }

    // Deduplicate while preserving first-seen order.
    let mut seen = std::collections::HashSet::new();
    refs.retain(|r| seen.insert(r.clone()));
    refs
}

/// Resolve a raw path reference to an absolute [`PathBuf`].
///
/// Resolution rules, in order:
///
/// 1. A `~/`-prefixed reference (or a bare `~`) is expanded against `$HOME`.
///    These are already absolute-ish paths — they must NOT be joined onto the
///    tier's `.cascade/` directory. (Without this, a reference such as
///    `~/.claude/references/foo.md` was concatenated verbatim onto the cascade
///    root, producing a nonsense path like `<cascade_root>/~/.claude/...` that
///    can never exist, so every `~/`-prefixed reference was wrongly reported as
///    dangling.)
/// 2. An already-absolute path (leading `/`) is returned as-is.
/// 3. Anything else is treated as relative and joined onto `base_dir`.
fn resolve_ref(raw: &str, base_dir: &Path) -> PathBuf {
    // Strip markdown code-span backticks and surrounding quotes that commonly
    // wrap references in instruction text (e.g. `-> `~/.claude/foo.md``). Also
    // strip a single trailing sentence punctuation mark. Without this the token
    // still carries a leading backtick, so tilde/absolute detection below fails.
    let raw = raw
        .trim_matches(['`', '"', '\''])
        .trim_end_matches(['.', ',', ';', ':', ')'])
        .trim_matches(['`', '"', '\'']);

    // ── Rule 1: tilde expansion ──────────────────────────────────────────────
    if raw == "~" {
        if let Some(home) = home_dir() {
            return home;
        }
    }
    if let Some(rest) = raw.strip_prefix("~/") {
        if let Some(home) = home_dir() {
            return home.join(rest);
        }
        // No $HOME available: fall through and treat the remainder as a path
        // rooted at "/" so it is still checked as an absolute-ish reference
        // rather than being joined onto the cascade root.
        return PathBuf::from("/").join(rest);
    }

    // ── Rules 2 & 3: absolute vs relative ────────────────────────────────────
    let p = Path::new(raw);
    if p.is_absolute() {
        p.to_path_buf()
    } else {
        base_dir.join(p)
    }
}

/// Returns the user's home directory via `$HOME` (Unix) or `USERPROFILE` (Windows).
fn home_dir() -> Option<PathBuf> {
    std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .ok()
        .map(PathBuf::from)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cascade_resolution::TierResult;
    use std::fs;
    use tempfile::TempDir;

    /// Build a minimal `ResolvedCascade` with one GCI tier sourced from `dir`.
    fn make_resolved_with_dir(instructions: &str, cascade_dir: &Path) -> ResolvedCascade {
        let all_tiers = cascade_types::cascade_tier::CascadeTier::all_in_precedence_order();
        let tiers_found: Vec<TierResult> = all_tiers
            .iter()
            .map(|t| {
                if *t == CascadeTier::Gci {
                    TierResult {
                        tier: CascadeTier::Gci,
                        found: true,
                        path_searched: cascade_dir.to_path_buf(),
                        instructions: instructions.to_string(),
                    }
                } else {
                    TierResult {
                        tier: *t,
                        found: false,
                        path_searched: PathBuf::from("/fake"),
                        instructions: String::new(),
                    }
                }
            })
            .collect();

        ResolvedCascade {
            merged_instructions: instructions.to_string(),
            on_demand_rules: Vec::new(),
            vars: Default::default(),
            mcp_server_url: String::new(),
            tiers_found,
            working_dir: cascade_dir.to_path_buf(),
        }
    }

    // ── Test 1: existing file → no finding ───────────────────────────────────

    /// An arrow pointer to a file that exists on disk produces no finding.
    ///
    /// WHY: the happy-path contract; must not generate false positives.
    #[test]
    fn existing_ref_not_flagged() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        // Create the referenced file.
        let rules_dir = cascade_dir.join("rules");
        fs::create_dir_all(&rules_dir).unwrap();
        fs::write(rules_dir.join("my-rule.md"), "content").unwrap();

        let instructions = "Some text.\n-> rules/my-rule.md\nMore text.";
        let resolved = make_resolved_with_dir(instructions, &cascade_dir);
        let findings = lint_dangling(&resolved);
        assert!(
            findings.is_empty(),
            "existing ref must not be flagged: {findings:#?}"
        );
    }

    // ── Test 2: missing file → one finding ───────────────────────────────────

    /// An arrow pointer to a non-existent file produces exactly one finding.
    ///
    /// WHY: core contract — broken pointers must be detected.
    #[test]
    fn missing_ref_produces_finding() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let instructions = "-> rules/ghost.md\nSome other text.";
        let resolved = make_resolved_with_dir(instructions, &cascade_dir);
        let findings = lint_dangling(&resolved);
        assert_eq!(findings.len(), 1, "expected exactly one finding");
        assert_eq!(findings[0].raw_ref, "rules/ghost.md");
        assert_eq!(findings[0].tier, CascadeTier::Gci);
    }

    // ── Test 3: @import to missing file ───────────────────────────────────────

    /// An `@import` directive pointing to a missing file produces a finding.
    ///
    /// WHY: import directives must be covered by the same check.
    #[test]
    fn missing_import_produces_finding() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let instructions = "@missing/file.md\nOther text.";
        let resolved = make_resolved_with_dir(instructions, &cascade_dir);
        let findings = lint_dangling(&resolved);
        assert_eq!(findings.len(), 1, "expected one finding for @import");
        assert_eq!(findings[0].raw_ref, "missing/file.md");
    }

    // ── Test 4: prose arrow pointer without extension is ignored ─────────────

    /// A bare-word arrow pointer (`-> detail`) without a file extension or slash
    /// must NOT be flagged — it is a prose reference, not a filesystem ref.
    ///
    /// WHY: prose pointers like `-> detail` are common in instructions and
    /// must not generate false positives.
    #[test]
    fn prose_arrow_without_extension_ignored() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        // This line looks like a prose reference — no extension, no slash.
        let instructions = "-> detail\n-> more context\nSome text.";
        let resolved = make_resolved_with_dir(instructions, &cascade_dir);
        let findings = lint_dangling(&resolved);
        assert!(
            findings.is_empty(),
            "prose arrow pointers must not be flagged: {findings:#?}"
        );
    }

    // ── Test 5: load_when reference ───────────────────────────────────────────

    /// A `load_when:` reference to a missing path is flagged.
    ///
    /// WHY: load_when references are the third supported pointer syntax.
    #[test]
    fn load_when_missing_path_flagged() {
        let tmp = TempDir::new().unwrap();
        let cascade_dir = tmp.path().join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();

        let instructions = "load_when: references/missing.md\nOther text.";
        let resolved = make_resolved_with_dir(instructions, &cascade_dir);
        let findings = lint_dangling(&resolved);
        assert_eq!(findings.len(), 1, "load_when missing path must be flagged");
        assert_eq!(findings[0].raw_ref, "references/missing.md");
    }

    // ── Test: tilde-prefixed reference expands against $HOME ─────────────────

    /// A `~/`-prefixed reference must be expanded to `$HOME/...` and checked
    /// there — NOT joined onto the tier's `.cascade/` directory.
    ///
    /// WHY (regression): previously `~/.claude/refs/x.md` fell into the relative
    /// branch and became `<cascade_dir>/~/.claude/refs/x.md`, which never exists,
    /// so every `~/`-prefixed pointer was reported as dangling regardless of the
    /// real target's existence.
    #[test]
    #[serial_test::serial(global_env)]
    fn tilde_ref_expands_against_home_and_is_not_flagged_when_present() {
        let tmp = TempDir::new().unwrap();
        // Point HOME at the tempdir so the expanded path lands somewhere we
        // control. Serialised against other env-mutating tests is unnecessary
        // here because we read HOME immediately after setting it.
        let home = tmp.path().to_path_buf();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", &home);

        // Create ~/.claude/references/exists.md under the fake HOME.
        let target_dir = home.join(".claude").join("references");
        fs::create_dir_all(&target_dir).unwrap();
        fs::write(target_dir.join("exists.md"), "content").unwrap();

        let cascade_dir = home.join("proj").join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        let instructions = "See detail.\n-> `~/.claude/references/exists.md`";
        let resolved = make_resolved_with_dir(instructions, &cascade_dir);
        let findings = lint_dangling(&resolved);

        // Restore HOME before asserting (panic safety).
        match prev_home {
            Some(v) => std::env::set_var("HOME", v),
            None => std::env::remove_var("HOME"),
        }

        assert!(
            findings.is_empty(),
            "existing ~/-prefixed ref must not be flagged: {findings:#?}"
        );
    }

    /// A missing `~/`-prefixed reference is flagged, and the resolved path is the
    /// $HOME-expanded path (never the `<cascade_dir>/~/...` concatenation).
    #[test]
    #[serial_test::serial(global_env)]
    fn tilde_ref_missing_is_flagged_with_home_expanded_path() {
        let tmp = TempDir::new().unwrap();
        let home = tmp.path().to_path_buf();
        let prev_home = std::env::var("HOME").ok();
        std::env::set_var("HOME", &home);

        let cascade_dir = home.join("proj").join(".cascade");
        fs::create_dir_all(&cascade_dir).unwrap();
        let instructions = "-> ~/.claude/references/ghost.md";
        let resolved = make_resolved_with_dir(instructions, &cascade_dir);
        let findings = lint_dangling(&resolved);

        let expected = home.join(".claude").join("references").join("ghost.md");

        match prev_home {
            Some(v) => std::env::set_var("HOME", v),
            None => std::env::remove_var("HOME"),
        }

        assert_eq!(findings.len(), 1, "missing ~/-ref must be flagged");
        assert_eq!(
            findings[0].resolved_path, expected,
            "resolved path must be $HOME-expanded, not joined onto the cascade dir"
        );
    }

    // ── Test 6: extract_path_refs deduplication ───────────────────────────────

    /// The same path appearing twice yields only one entry.
    ///
    /// WHY: duplicate findings for the same reference would clutter output.
    #[test]
    fn duplicate_refs_deduplicated() {
        let text = "-> rules/missing.md\n-> rules/missing.md\nOther text.";
        let refs = extract_path_refs(text);
        assert_eq!(refs.len(), 1, "duplicate refs must be deduplicated");
    }
}
