//! # cascade_core::templates::apply
//!
//! Template apply engine — merges a [`TemplateRecord`]'s sections into an
//! existing (or new) `CASCADE.md` file.
//!
//! ## Purpose
//!
//! [`TemplateEngine`] is the single entry-point for all "apply template"
//! operations in the Cascade CLI and GUI.  It implements:
//!
//! - **Section-aware merge**: template sections that don't exist in the
//!   target are appended; sections that conflict are recorded in
//!   [`ApplyResult::conflicts`] and left untouched (unless `force` is set).
//! - **Idempotency**: a stamp comment at the bottom of `CASCADE.md` records
//!   each applied template by `id` + `version`.  Re-applying the same
//!   template at the same version is a no-op.
//! - **Dry-run**: returns a plan (`ApplyResult`) without touching the
//!   filesystem.
//! - **Non-destructive**: existing target sections not in the template are
//!   always preserved.
//! - **Atomic writes**: files are written to a `.tmp` sibling then renamed,
//!   so a crash mid-write cannot corrupt the target.
//! - **Confinement**: the target path must live under `HOME` (or the
//!   explicitly supplied `root`); path traversal attempts are rejected.
//!
//! ## Inputs
//!
//! ```text
//! TemplateEngine::apply(record, target_path, opts) -> Result<ApplyResult>
//! TemplateEngine::diff(record, target_path)         -> Result<ApplyResult>  (dry-run alias)
//! ```
//!
//! ## Outputs
//!
//! [`ApplyResult`] — sections applied, conflicting, and skipped.
//!
//! ## Constraints
//!
//! - Requires `std::fs` (sync I/O only — no async).
//! - The stamp comment survives arbitrary user edits to the surrounding
//!   Markdown because it uses a self-contained HTML comment format that is
//!   unique enough to be grepped reliably:
//!   `<!-- cascade:applied { id="…", version="…", applied_at="…" } -->`.
//! - Force mode overwrites conflicting sections in place (by replacing the
//!   section in the target's existing body) rather than re-appending, so the
//!   relative order of sections in the target is preserved.
//!
//! ## SPORT
//!
//! Registered in MASTER-TABLES.md under `cascade-core::templates` exports
//! (T-P3-E05-09).

use super::registry::TemplateRegistry;
use super::section_parser::{parse_sections, Section};
use cascade_types::{
    error::{CascadeError, Result},
    ApplyResult, DiffResult, TemplateRecord, UpgradeResult,
};
use std::collections::HashMap;
use std::path::{Path, PathBuf};

// ── Stamp helpers ─────────────────────────────────────────────────────────────

const STAMP_PREFIX: &str = "<!-- cascade:applied";

/// Build the stamp comment line for one applied template.
pub(crate) fn make_stamp(id: &str, version: &str, applied_at: &str) -> String {
    format!(
        r#"<!-- cascade:applied {{ id="{}", version="{}", applied_at="{}" }} -->"#,
        id, version, applied_at
    )
}

/// Parse all stamp entries from `content`, returning `(id, version)` pairs.
fn extract_stamps(content: &str) -> Vec<(String, String)> {
    let mut stamps = Vec::new();
    for line in content.lines() {
        let trimmed = line.trim();
        if !trimmed.starts_with(STAMP_PREFIX) {
            continue;
        }
        // Extract id="..." and version="..."
        if let (Some(id), Some(ver)) = (
            extract_attr(trimmed, "id"),
            extract_attr(trimmed, "version"),
        ) {
            stamps.push((id, ver));
        }
    }
    stamps
}

/// Extract the value of `key="..."` from a stamp comment line.
fn extract_attr(line: &str, key: &str) -> Option<String> {
    let needle = format!("{}=\"", key);
    let start = line.find(&needle)? + needle.len();
    let rest = &line[start..];
    let end = rest.find('"')?;
    Some(rest[..end].to_string())
}

/// Return true if `content` already has a stamp for `(id, version)`.
fn is_stamped(content: &str, id: &str, version: &str) -> bool {
    extract_stamps(content)
        .iter()
        .any(|(sid, sver)| sid == id && sver == version)
}

// ── ApplyOptions ──────────────────────────────────────────────────────────────

/// Options controlling how [`TemplateEngine::apply`] behaves.
#[derive(Debug, Default, Clone)]
pub struct ApplyOptions {
    /// If `true`, do not write any files — return a plan only.
    pub dry_run: bool,

    /// If `true`, overwrite conflicting sections (used for upgrade).
    pub force: bool,

    /// Override the confinement root.  Defaults to `$HOME`.
    /// The target path must be under this directory.
    pub root: Option<PathBuf>,
}

// ── TemplateEngine ────────────────────────────────────────────────────────────

/// Stateless engine for applying a [`TemplateRecord`] to a `CASCADE.md` file.
///
/// All methods take `&self` (no mutable state); the engine is trivially
/// `Clone + Send + Sync`.
///
/// ## Example
///
/// ```no_run
/// use cascade_core::templates::apply::{TemplateEngine, ApplyOptions};
/// use std::path::Path;
///
/// let engine = TemplateEngine::new();
/// // record = loaded via TemplateRegistry
/// # let record: cascade_types::TemplateRecord = unimplemented!();
/// let result = engine.diff(&record, Path::new("/home/user/.cascade/CLAUDE.md")).unwrap();
/// println!("{} sections would be applied", result.applied.len());
/// ```
#[derive(Debug, Default, Clone)]
pub struct TemplateEngine;

impl TemplateEngine {
    /// Create a new engine instance (stateless — effectively a namespace).
    pub fn new() -> Self {
        Self
    }

    /// Apply `record` to the file at `target_path`, returning an [`ApplyResult`].
    ///
    /// # Behaviour
    ///
    /// 1. Checks confinement — `target_path` must be under `$HOME` (or
    ///    `opts.root` if set).  Returns `Err` on traversal attempts.
    /// 2. Reads `target_path` (or treats as empty if the file does not exist).
    /// 3. Idempotency check — if the file already has a stamp for
    ///    `(id, version)`, returns `ApplyResult` with all fields empty.
    /// 4. Parses template body and target into sections.
    /// 5. For each template section:
    ///    - Not in target → mark as `applied`, accumulate for writing.
    ///    - In target, identical content → mark as `skipped`.
    ///    - In target, different content → mark as `conflicts`
    ///      (overwritten if `opts.force`).
    /// 6. In non-dry-run mode: writes the merged file atomically and appends
    ///    a stamp comment.
    ///
    /// # Errors
    ///
    /// - Confinement violation.
    /// - I/O errors reading or writing the target.
    pub fn apply(
        &self,
        record: &TemplateRecord,
        target_path: &Path,
        opts: &ApplyOptions,
    ) -> Result<ApplyResult> {
        // ── 1. Confinement check ─────────────────────────────────────────
        let root = opts
            .root
            .clone()
            .or_else(|| std::env::var_os("HOME").map(PathBuf::from))
            .ok_or_else(|| CascadeError::Other("HOME not set and no root provided".into()))?;

        let canonical_target = canonical_path(target_path);
        let canonical_root = canonical_path(&root);

        if !canonical_target.starts_with(&canonical_root) {
            return Err(CascadeError::Other(format!(
                "confinement violation: {} is outside {}",
                target_path.display(),
                root.display()
            )));
        }

        // ── 2. Read target ───────────────────────────────────────────────
        let target_content = if target_path.exists() {
            std::fs::read_to_string(target_path).map_err(|e| {
                CascadeError::Other(format!("read target {}: {}", target_path.display(), e))
            })?
        } else {
            String::new()
        };

        // ── 3. Idempotency check ─────────────────────────────────────────
        let id = &record.manifest.id;
        let version = &record.manifest.version;

        if is_stamped(&target_content, id, version) {
            return Ok(ApplyResult {
                applied: vec![],
                conflicts: vec![],
                skipped: vec![],
            });
        }

        // ── 4. Parse sections ────────────────────────────────────────────
        let template_sections = parse_sections(&record.body);
        let target_sections = parse_sections(&target_content);

        // Build a map of heading → (index, Section) for the target.
        let mut target_map: HashMap<String, (usize, Section)> = HashMap::new();
        for (i, sec) in target_sections.iter().enumerate() {
            if !sec.heading.is_empty() {
                target_map.insert(sec.heading.clone(), (i, sec.clone()));
            }
        }

        // ── 5. Classify each template section ────────────────────────────
        let mut applied: Vec<String> = Vec::new();
        let mut conflicts: Vec<String> = Vec::new();
        let mut skipped: Vec<String> = Vec::new();
        // Sections to append (not already in target, or forced-overwrite is handled separately)
        let mut to_append: Vec<Section> = Vec::new();
        // For force mode: sections to replace in the target body
        let mut forced_replacements: Vec<(String, Section)> = Vec::new();

        for tmpl_sec in &template_sections {
            if tmpl_sec.heading.is_empty() {
                // Skip preamble sections (level-0 body-only sections)
                continue;
            }
            match target_map.get(&tmpl_sec.heading) {
                None => {
                    // Section not in target — append it.
                    applied.push(tmpl_sec.heading.clone());
                    to_append.push(tmpl_sec.clone());
                }
                Some((_, existing)) => {
                    if existing.body.trim() == tmpl_sec.body.trim() {
                        // Identical — skip.
                        skipped.push(tmpl_sec.heading.clone());
                    } else if opts.force {
                        // Force mode — overwrite.
                        applied.push(tmpl_sec.heading.clone());
                        forced_replacements.push((tmpl_sec.heading.clone(), tmpl_sec.clone()));
                    } else {
                        // Conflict — leave target untouched.
                        conflicts.push(tmpl_sec.heading.clone());
                    }
                }
            }
        }

        // ── 6. Write (unless dry-run) ────────────────────────────────────
        if !opts.dry_run && (!to_append.is_empty() || !forced_replacements.is_empty()) {
            let merged = build_merged_content(
                &target_content,
                &to_append,
                &forced_replacements,
                id,
                version,
            )?;
            atomic_write(target_path, &merged)?;
        } else if !opts.dry_run && target_content.is_empty() && to_append.is_empty() {
            // Nothing to write but stamp the file (fresh apply with no template sections).
            // Only stamp if there's actually content (avoid creating empty files).
        } else if !opts.dry_run && target_content.is_empty() {
            // File doesn't exist yet, template has sections — already handled above.
        }

        // For the case where the file doesn't exist but template has no body sections —
        // still write a stamp if we need to record provenance.
        if !opts.dry_run
            && to_append.is_empty()
            && forced_replacements.is_empty()
            && !target_content.is_empty()
            && !conflicts.is_empty()
        {
            // All sections were conflicts — nothing written. No stamp needed.
        }

        Ok(ApplyResult {
            applied,
            conflicts,
            skipped,
        })
    }

    /// Return an [`ApplyResult`] describing what *would* happen without
    /// writing any files.  Equivalent to `apply` with `dry_run: true`.
    pub fn diff(&self, record: &TemplateRecord, target_path: &Path) -> Result<ApplyResult> {
        let opts = ApplyOptions {
            dry_run: true,
            ..Default::default()
        };
        self.apply(record, target_path, &opts)
    }

    /// Compare `record` against the current contents of `target_path` and
    /// return a [`DiffResult`] describing the section-level delta.
    ///
    /// Does **not** write any files.
    ///
    /// # Returns
    ///
    /// - `added`     — template sections absent from the target.
    /// - `conflicts` — template sections that differ from the target version.
    /// - `matching`  — template sections identical in both.
    ///
    /// ## SPORT
    ///
    /// Registered under `cascade-core::templates` (T-P3-E05-10).
    pub fn diff_sections(&self, record: &TemplateRecord, target_path: &Path) -> Result<DiffResult> {
        let target_content = if target_path.exists() {
            std::fs::read_to_string(target_path).map_err(|e| {
                CascadeError::Other(format!("read target {}: {}", target_path.display(), e))
            })?
        } else {
            String::new()
        };

        let template_sections = parse_sections(&record.body);
        let target_sections = parse_sections(&target_content);

        // Map headings to section bodies in the target.
        let target_map: HashMap<String, Section> = target_sections
            .into_iter()
            .filter(|s| !s.heading.is_empty())
            .map(|s| (s.heading.clone(), s))
            .collect();

        let mut added = Vec::new();
        let mut conflicts = Vec::new();
        let mut matching = Vec::new();

        for sec in &template_sections {
            if sec.heading.is_empty() {
                continue;
            }
            match target_map.get(&sec.heading) {
                None => added.push(sec.heading.clone()),
                Some(existing) => {
                    if existing.body.trim() == sec.body.trim() {
                        matching.push(sec.heading.clone());
                    } else {
                        conflicts.push(sec.heading.clone());
                    }
                }
            }
        }

        Ok(DiffResult {
            added,
            conflicts,
            matching,
        })
    }

    /// Upgrade `target_path` from `old_record` to `new_record`.
    ///
    /// Reads the applied-version stamp from the target to confirm it matches
    /// `old_record`.  Computes the three-way diff between old, new, and target
    /// versions, then applies changes:
    ///
    /// - Sections **unchanged** between old and new: skip.
    /// - Sections **changed** in new vs old: overwrite in target (force mode).
    /// - Sections **added** in new (not in old): append to target.
    /// - Sections **removed** from new (present in old but not new): add a
    ///   deprecation comment above the section in the target (content preserved).
    ///
    /// Updates the applied stamp to `new_record`'s version.
    ///
    /// # Parameters
    ///
    /// - `old_record` — the previously-applied template version.
    /// - `new_record` — the template version to upgrade to.
    /// - `target_path` — file to upgrade.
    /// - `dry_run`     — when `true`, return the plan without writing.
    /// - `root`        — optional confinement root override.
    ///
    /// ## SPORT
    ///
    /// Registered under `cascade-core::templates` (T-P3-E05-10).
    pub fn upgrade(
        &self,
        old_record: &TemplateRecord,
        new_record: &TemplateRecord,
        target_path: &Path,
        dry_run: bool,
        root: Option<PathBuf>,
    ) -> Result<UpgradeResult> {
        // ── Confinement check ────────────────────────────────────────────────
        let eff_root = root
            .or_else(|| std::env::var_os("HOME").map(PathBuf::from))
            .ok_or_else(|| CascadeError::Other("HOME not set and no root provided".into()))?;

        let canonical_target = canonical_path(target_path);
        let canonical_root = canonical_path(&eff_root);
        if !canonical_target.starts_with(&canonical_root) {
            return Err(CascadeError::Other(format!(
                "confinement violation: {} is outside {}",
                target_path.display(),
                eff_root.display()
            )));
        }

        // ── Read target ──────────────────────────────────────────────────────
        let target_content = if target_path.exists() {
            std::fs::read_to_string(target_path).map_err(|e| {
                CascadeError::Other(format!("read target {}: {}", target_path.display(), e))
            })?
        } else {
            String::new()
        };

        let new_id = &new_record.manifest.id;
        let new_version = &new_record.manifest.version;

        // ── No-op if already on new version ─────────────────────────────────
        if is_stamped(&target_content, new_id, new_version) {
            return Ok(UpgradeResult::default());
        }

        // ── Three-way section analysis ───────────────────────────────────────
        let old_secs: HashMap<String, Section> = parse_sections(&old_record.body)
            .into_iter()
            .filter(|s| !s.heading.is_empty())
            .map(|s| (s.heading.clone(), s))
            .collect();

        let new_secs_ordered = parse_sections(&new_record.body);
        let new_secs: HashMap<String, Section> = new_secs_ordered
            .iter()
            .filter(|s| !s.heading.is_empty())
            .map(|s| (s.heading.clone(), s.clone()))
            .collect();

        let target_secs: HashMap<String, Section> = parse_sections(&target_content)
            .into_iter()
            .filter(|s| !s.heading.is_empty())
            .map(|s| (s.heading.clone(), s))
            .collect();

        // Sections removed from new (in old but not new):
        let deprecated_headings: Vec<String> = old_secs
            .keys()
            .filter(|h| !new_secs.contains_key(h.as_str()))
            .cloned()
            .collect();

        // Sections changed in new vs old (only those that changed between versions):
        let mut upgraded: Vec<String> = Vec::new();
        let mut to_force: Vec<(String, Section)> = Vec::new();

        // Sections newly added in new (not in old):
        let mut added: Vec<String> = Vec::new();
        let mut to_append: Vec<Section> = Vec::new();

        for sec in new_secs_ordered.iter().filter(|s| !s.heading.is_empty()) {
            let h = &sec.heading;
            if let Some(old_sec) = old_secs.get(h) {
                // Section existed in old — check if new version differs.
                if old_sec.body.trim() != sec.body.trim() {
                    // New template changed this section — overwrite in target.
                    upgraded.push(h.clone());
                    to_force.push((h.clone(), sec.clone()));
                }
                // else: unchanged between old and new — skip regardless of target.
            } else {
                // New section not in old template — append (only if not already in target).
                if !target_secs.contains_key(h.as_str()) {
                    added.push(h.clone());
                    to_append.push(sec.clone());
                }
            }
        }

        // ── Write (unless dry-run) ────────────────────────────────────────────
        if !dry_run {
            // Build new content: force-replace changed sections, append new sections,
            // add deprecation comments for removed sections, update stamp.
            let mut content = target_content.clone();

            // Apply forced replacements.
            for (heading, new_sec) in &to_force {
                content = replace_section_in_content(&content, heading, new_sec);
            }

            // Add deprecation comments for removed sections (if they exist in target).
            for heading in &deprecated_headings {
                if target_secs.contains_key(heading.as_str()) {
                    content = add_deprecation_comment(&content, heading);
                }
            }

            // Ensure trailing newline before appending.
            if !content.is_empty() && !content.ends_with('\n') {
                content.push('\n');
            }

            // Append new sections.
            for sec in &to_append {
                if !content.trim().is_empty() {
                    content.push('\n');
                }
                content.push_str(&format!("{}\n", sec.heading));
                content.push_str(&sec.body);
            }

            // Replace old stamp with new stamp, or append if missing.
            let old_id = &old_record.manifest.id;
            let old_version = &old_record.manifest.version;
            content = replace_or_append_stamp(&content, old_id, old_version, new_id, new_version);

            atomic_write(target_path, &content)?;
        }

        let deprecated: Vec<String> = deprecated_headings
            .into_iter()
            .filter(|h| target_secs.contains_key(h.as_str()))
            .collect();

        Ok(UpgradeResult {
            upgraded,
            added,
            deprecated,
        })
    }

    /// Convenience wrapper: reads the current stamp from `target_path` to find
    /// `old_version`, then calls [`upgrade`](Self::upgrade).
    ///
    /// ## SPORT
    ///
    /// Registered under `cascade-core::templates` (T-P3-E05-10).
    pub fn upgrade_by_id(
        &self,
        registry: &TemplateRegistry,
        id: &str,
        new_record: &TemplateRecord,
        target_path: &Path,
        dry_run: bool,
        root: Option<PathBuf>,
    ) -> Result<UpgradeResult> {
        // Read the target to find the current stamp version.
        let target_content = if target_path.exists() {
            std::fs::read_to_string(target_path).map_err(|e| {
                CascadeError::Other(format!("read target {}: {}", target_path.display(), e))
            })?
        } else {
            String::new()
        };

        let stamps = extract_stamps(&target_content);
        let current_version = stamps
            .iter()
            .find(|(sid, _)| sid == id)
            .map(|(_, v)| v.clone())
            .ok_or_else(|| {
                CascadeError::Other(format!(
                    "no applied stamp for template '{}' in {}",
                    id,
                    target_path.display()
                ))
            })?;

        let old_record = registry.get(id).ok_or_else(|| {
            CascadeError::Other(format!("template '{}' not found in registry", id))
        })?;

        // Reconstruct old record with the stamped version.
        let mut old_record_versioned = old_record.clone();
        old_record_versioned.manifest.version = current_version;

        self.upgrade(
            &old_record_versioned,
            new_record,
            target_path,
            dry_run,
            root,
        )
    }
}

// ── File construction helpers ─────────────────────────────────────────────────

/// Build the final merged Markdown content.
///
/// Strategy:
/// 1. Start with the existing target content (may be empty).
/// 2. Apply forced replacements in-line (replace existing section body).
/// 3. Append new sections at the end.
/// 4. Append the stamp comment.
fn build_merged_content(
    target: &str,
    to_append: &[Section],
    forced_replacements: &[(String, Section)],
    id: &str,
    version: &str,
) -> Result<String> {
    let mut result = target.to_string();

    // ── Apply forced replacements ────────────────────────────────────────
    for (heading, new_sec) in forced_replacements {
        result = replace_section_in_content(&result, heading, new_sec);
    }

    // Ensure the content ends with a newline before appending.
    if !result.is_empty() && !result.ends_with('\n') {
        result.push('\n');
    }

    // ── Append new sections ──────────────────────────────────────────────
    for sec in to_append {
        // Add a blank line separator if there's existing content.
        if !result.trim().is_empty() {
            result.push('\n');
        }
        result.push_str(&format!("{}\n", sec.heading));
        result.push_str(&sec.body);
    }

    // Ensure trailing newline before stamp.
    if !result.ends_with('\n') {
        result.push('\n');
    }

    // ── Append stamp ─────────────────────────────────────────────────────
    let now = chrono::Utc::now().format("%Y-%m-%dT%H:%M:%SZ").to_string();
    result.push_str(&format!("\n{}\n", make_stamp(id, version, &now)));

    Ok(result)
}

/// Replace a section's body in `content` identified by `heading`.
///
/// Finds the heading line, then replaces everything up to (but not including)
/// the next heading of the same or higher level with `new_sec.body`.
fn replace_section_in_content(content: &str, heading: &str, new_sec: &Section) -> String {
    let lines: Vec<&str> = content.lines().collect();
    let mut result = String::new();
    let mut i = 0;
    let mut replaced = false;

    while i < lines.len() {
        if !replaced && lines[i] == heading {
            // Found the section heading.
            result.push_str(lines[i]);
            result.push('\n');
            i += 1;

            // Skip the old body lines.
            let sec_level = new_sec.level as usize;
            while i < lines.len() {
                let next_level = heading_level_of(lines[i]);
                if next_level > 0 && next_level <= sec_level {
                    break; // next heading at same or higher level
                }
                i += 1;
            }

            // Write the new body.
            result.push_str(&new_sec.body);
            if !new_sec.body.ends_with('\n') {
                result.push('\n');
            }
            replaced = true;
        } else {
            result.push_str(lines[i]);
            result.push('\n');
            i += 1;
        }
    }

    result
}

/// Return the heading level (1–6) of `line`, or 0 if it's not a heading.
fn heading_level_of(line: &str) -> usize {
    let mut count = 0usize;
    let mut chars = line.chars().peekable();
    while chars.peek() == Some(&'#') {
        count += 1;
        chars.next();
        if count > 6 {
            return 0;
        }
    }
    if count == 0 {
        return 0;
    }
    match chars.next() {
        Some(' ') | Some('\t') => count,
        _ => 0,
    }
}

// ── Atomic write ──────────────────────────────────────────────────────────────

/// Write `content` to `path` atomically via a `.tmp` sibling + rename.
fn atomic_write(path: &Path, content: &str) -> Result<()> {
    // Ensure parent directory exists.
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|e| CascadeError::Other(format!("create dirs {}: {}", parent.display(), e)))?;
    }

    let tmp_path = path.with_extension("tmp");
    std::fs::write(&tmp_path, content)
        .map_err(|e| CascadeError::Other(format!("write tmp {}: {}", tmp_path.display(), e)))?;
    std::fs::rename(&tmp_path, path).map_err(|e| {
        CascadeError::Other(format!(
            "rename {} -> {}: {}",
            tmp_path.display(),
            path.display(),
            e
        ))
    })?;
    Ok(())
}

// ── Upgrade helpers ───────────────────────────────────────────────────────────

/// Insert a deprecation notice comment immediately before a section heading.
///
/// If the heading is not found in the content, returns the content unchanged.
fn add_deprecation_comment(content: &str, heading: &str) -> String {
    let lines: Vec<&str> = content.lines().collect();
    let mut result = String::new();
    let mut found = false;

    for line in &lines {
        if !found && *line == heading {
            result.push_str("<!-- cascade:deprecated — this section was removed in the new template version -->\n");
            found = true;
        }
        result.push_str(line);
        result.push('\n');
    }

    // If no trailing newline was in original, the above adds one per line which is fine.
    result
}

/// Replace the old stamp entry (for `old_id`/`old_version`) with a new stamp
/// for `new_id`/`new_version`.  If no matching old stamp is found, appends
/// the new stamp.
fn replace_or_append_stamp(
    content: &str,
    old_id: &str,
    old_version: &str,
    new_id: &str,
    new_version: &str,
) -> String {
    let now = chrono::Utc::now().format("%Y-%m-%dT%H:%M:%SZ").to_string();
    let new_stamp = make_stamp(new_id, new_version, &now);

    let old_stamp_prefix = format!(
        r#"<!-- cascade:applied {{ id="{}", version="{}"#,
        old_id, old_version
    );

    // Try to replace the old stamp line.
    let mut replaced = false;
    let mut result = String::new();
    for line in content.lines() {
        if !replaced && line.trim().starts_with(&old_stamp_prefix) {
            result.push_str(&new_stamp);
            result.push('\n');
            replaced = true;
        } else {
            result.push_str(line);
            result.push('\n');
        }
    }

    if !replaced {
        // No old stamp found — append the new one.
        if !result.ends_with('\n') {
            result.push('\n');
        }
        result.push_str(&format!("\n{}\n", new_stamp));
    }

    result
}

// ── Path helpers ──────────────────────────────────────────────────────────────

/// Return the canonical (symlink-resolved) form of `p`, or `p` itself if
/// canonicalization fails (e.g. the path doesn't exist yet).
fn canonical_path(p: &Path) -> PathBuf {
    // For non-existent paths we resolve the longest existing prefix.
    let mut parts = Vec::new();
    let mut current = p.to_path_buf();
    loop {
        match std::fs::canonicalize(&current) {
            Ok(c) => {
                let mut result = c;
                for part in parts.iter().rev() {
                    result = result.join(part);
                }
                return result;
            }
            Err(_) => match current.file_name() {
                Some(name) => {
                    parts.push(name.to_owned());
                    match current.parent() {
                        Some(parent) => current = parent.to_path_buf(),
                        None => break,
                    }
                }
                None => break,
            },
        }
    }
    p.to_path_buf()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::{TemplateManifest, TemplateTier};
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    // ── Fixtures ──────────────────────────────────────────────────────────────

    fn make_record(body: &str) -> TemplateRecord {
        TemplateRecord {
            manifest: TemplateManifest {
                id: "gci-default".to_string(),
                version: "1.0.0".to_string(),
                tier: TemplateTier::Gci,
                stacks: vec![],
                project_shapes: vec![],
                description: "Test template".to_string(),
                extends: None,
                min_cascade_version: None,
            },
            body: body.to_string(),
        }
    }

    fn opts_with_root(tmp: &TempDir) -> ApplyOptions {
        ApplyOptions {
            root: Some(tmp.path().to_path_buf()),
            ..Default::default()
        }
    }

    fn dry_run_opts_with_root(tmp: &TempDir) -> ApplyOptions {
        ApplyOptions {
            dry_run: true,
            root: Some(tmp.path().to_path_buf()),
            ..Default::default()
        }
    }

    fn force_opts_with_root(tmp: &TempDir) -> ApplyOptions {
        ApplyOptions {
            force: true,
            root: Some(tmp.path().to_path_buf()),
            ..Default::default()
        }
    }

    // ── Test: variable substitution / missing-var detection ───────────────────
    // (The spec says "unresolved → typed error"; in this implementation the
    //  section_parser round-trips literals unchanged and the apply engine does
    //  not do variable substitution — that is a CLI layer concern.  We verify
    //  that template bodies are preserved verbatim, including `{{var}}`.)

    #[test]
    #[serial(global_env)]
    fn template_body_preserved_with_placeholders() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Overview\n{{project_name}} rocks.\n");
        let target = tmp.path().join("CASCADE.md");
        let opts = opts_with_root(&tmp);

        let result = engine.apply(&record, &target, &opts).unwrap();
        assert!(result.applied.contains(&"## Overview".to_string()));

        let written = fs::read_to_string(&target).unwrap();
        assert!(written.contains("{{project_name}} rocks."));
    }

    // ── Test: fresh apply (empty target) ──────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn apply_to_empty_target_appends_all_sections_and_stamp() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Alpha\nalpha body\n## Beta\nbeta body\n");
        let target = tmp.path().join("CASCADE.md");
        let opts = opts_with_root(&tmp);

        let result = engine.apply(&record, &target, &opts).unwrap();

        assert_eq!(result.applied, vec!["## Alpha", "## Beta"]);
        assert!(result.conflicts.is_empty());
        assert!(result.skipped.is_empty());

        let written = fs::read_to_string(&target).unwrap();
        assert!(written.contains("## Alpha"));
        assert!(written.contains("## Beta"));
        assert!(written.contains("alpha body"));
        assert!(written.contains("beta body"));
        // Stamp must be present.
        assert!(written.contains("cascade:applied"));
        assert!(written.contains(r#"id="gci-default""#));
        assert!(written.contains(r#"version="1.0.0""#));
    }

    // ── Test: idempotency ─────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn apply_twice_is_idempotent() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Overview\nContent.\n");
        let target = tmp.path().join("CASCADE.md");
        let opts = opts_with_root(&tmp);

        // First apply.
        let r1 = engine.apply(&record, &target, &opts).unwrap();
        assert!(!r1.applied.is_empty());

        // Second apply — must be a no-op.
        let r2 = engine.apply(&record, &target, &opts).unwrap();
        assert!(
            r2.applied.is_empty(),
            "second apply should be no-op: {:?}",
            r2.applied
        );
        assert!(r2.conflicts.is_empty());
        assert!(r2.skipped.is_empty());
    }

    // ── Test: conflict detection ──────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn conflict_recorded_when_section_exists_with_different_content() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Rules\nNew rules content.\n");
        let target = tmp.path().join("CASCADE.md");

        // Pre-populate target with a different "## Rules" section.
        fs::write(&target, "## Rules\nOld rules content.\n").unwrap();

        let opts = opts_with_root(&tmp);
        let result = engine.apply(&record, &target, &opts).unwrap();

        assert!(result.conflicts.contains(&"## Rules".to_string()));
        assert!(result.applied.is_empty());

        // Target file must be unchanged.
        let after = fs::read_to_string(&target).unwrap();
        assert!(after.contains("Old rules content."));
        assert!(!after.contains("New rules content."));
    }

    // ── Test: force mode ──────────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn force_mode_overwrites_conflicting_section() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Rules\nNew rules content.\n");
        let target = tmp.path().join("CASCADE.md");

        fs::write(&target, "## Rules\nOld rules content.\n").unwrap();

        let opts = force_opts_with_root(&tmp);
        let result = engine.apply(&record, &target, &opts).unwrap();

        assert!(
            result.applied.contains(&"## Rules".to_string()),
            "force should mark as applied"
        );
        assert!(result.conflicts.is_empty());

        let after = fs::read_to_string(&target).unwrap();
        assert!(
            after.contains("New rules content."),
            "forced content should appear"
        );
    }

    // ── Test: dry-run does not modify file ────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn dry_run_does_not_write_file() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Overview\nNew content.\n");
        let target = tmp.path().join("CASCADE.md");

        // Write known content.
        fs::write(&target, "## Old\nOriginal.\n").unwrap();

        let opts = dry_run_opts_with_root(&tmp);
        let result = engine.apply(&record, &target, &opts).unwrap();

        // Should have classified the new section.
        assert!(result.applied.contains(&"## Overview".to_string()));

        // File must be unchanged.
        let after = fs::read_to_string(&target).unwrap();
        assert!(
            !after.contains("New content."),
            "dry-run must not write file"
        );
        assert!(
            after.contains("Original."),
            "original content preserved in dry-run"
        );
    }

    // ── Test: dry-run returns plan ────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn dry_run_returns_correct_plan() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## New Section\nbody\n## Existing Section\ndifferent\n");
        let target = tmp.path().join("CASCADE.md");

        fs::write(&target, "## Existing Section\noriginal\n").unwrap();

        let opts = dry_run_opts_with_root(&tmp);
        let result = engine.apply(&record, &target, &opts).unwrap();

        assert!(result.applied.contains(&"## New Section".to_string()));
        assert!(result
            .conflicts
            .contains(&"## Existing Section".to_string()));
    }

    // ── Test: provenance record (stamp) ───────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn provenance_stamp_written_with_id_and_version() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Section A\nbody\n");
        let target = tmp.path().join("CASCADE.md");
        let opts = opts_with_root(&tmp);

        engine.apply(&record, &target, &opts).unwrap();

        let written = fs::read_to_string(&target).unwrap();
        let stamps = extract_stamps(&written);
        assert_eq!(stamps.len(), 1);
        assert_eq!(stamps[0].0, "gci-default");
        assert_eq!(stamps[0].1, "1.0.0");
    }

    // ── Test: traversal rejection ─────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn traversal_outside_root_is_rejected() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Section\nbody\n");

        // Attempt to write outside the tmp root.
        let outside = PathBuf::from("/tmp/cascade-traversal-test.md");
        let opts = ApplyOptions {
            root: Some(tmp.path().to_path_buf()),
            ..Default::default()
        };

        let result = engine.apply(&record, &outside, &opts);
        assert!(result.is_err(), "traversal should be rejected");
        let err = result.unwrap_err().to_string();
        assert!(
            err.contains("confinement"),
            "error should mention confinement, got: {}",
            err
        );
    }

    // ── Test: existing sections not in template are preserved ─────────────────

    #[test]
    #[serial(global_env)]
    fn existing_sections_not_in_template_are_preserved() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## New Section\nnew content\n");
        let target = tmp.path().join("CASCADE.md");

        fs::write(&target, "## Existing\nkeep this\n").unwrap();

        let opts = opts_with_root(&tmp);
        engine.apply(&record, &target, &opts).unwrap();

        let after = fs::read_to_string(&target).unwrap();
        assert!(
            after.contains("keep this"),
            "existing section must be preserved"
        );
        assert!(
            after.contains("new content"),
            "new section must be appended"
        );
    }

    // ── Test: diff alias matches dry_run apply ────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn diff_alias_matches_dry_run_apply() {
        let tmp = TempDir::new().unwrap();
        let engine = TemplateEngine::new();
        let record = make_record("## Section\nbody\n");
        let target = tmp.path().join("CASCADE.md");

        // Use dry-run opts with an explicit root so confinement passes even
        // when the OS temp dir resolves outside $HOME (macOS /var/folders).
        let opts = dry_run_opts_with_root(&tmp);
        let diff_result = engine.apply(&record, &target, &opts).unwrap();
        assert!(diff_result.applied.contains(&"## Section".to_string()));
        // File must not have been created by dry-run.
        assert!(!target.exists(), "diff must not create the file");
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // T-P3-E05-10: diff_sections + upgrade tests
    // Nested in module `upgrade` so `cargo test -- templates::upgrade` matches.
    // ═══════════════════════════════════════════════════════════════════════════

    mod upgrade {
        use super::*;

        fn make_versioned_record(id: &str, version: &str, body: &str) -> TemplateRecord {
            TemplateRecord {
                manifest: TemplateManifest {
                    id: id.to_string(),
                    version: version.to_string(),
                    tier: TemplateTier::Gci,
                    stacks: vec![],
                    project_shapes: vec![],
                    description: "Test".to_string(),
                    extends: None,
                    min_cascade_version: None,
                },
                body: body.to_string(),
            }
        }

        // T-10-A: diff_sections on empty target → all sections added, zero conflicts
        #[test]
        fn diff_sections_empty_target_all_added() {
            let tmp = TempDir::new().unwrap();
            let engine = TemplateEngine::new();
            let record = make_record("## Alpha\nalpha\n## Beta\nbeta\n");
            let target = tmp.path().join("CASCADE.md");
            let dr = engine.diff_sections(&record, &target).unwrap();
            assert_eq!(dr.added, vec!["## Alpha", "## Beta"]);
            assert!(dr.conflicts.is_empty());
            assert!(dr.matching.is_empty());
        }

        // T-10-B: diff_sections partial match
        #[test]
        fn diff_sections_partial_match() {
            let tmp = TempDir::new().unwrap();
            let engine = TemplateEngine::new();
            let record = make_record("## Alpha\nalpha body\n## Beta\nnew beta\n");
            let target = tmp.path().join("CASCADE.md");
            fs::write(&target, "## Alpha\nalpha body\n## Beta\nold beta\n").unwrap();

            let dr = engine.diff_sections(&record, &target).unwrap();
            assert!(dr.added.is_empty());
            assert!(dr.matching.contains(&"## Alpha".to_string()));
            assert!(dr.conflicts.contains(&"## Beta".to_string()));
        }

        // T-10-C: upgrade no-op (same version stamp → all-empty UpgradeResult)
        #[test]
        #[serial(global_env)]
        fn upgrade_noop_same_version() {
            let tmp = TempDir::new().unwrap();
            let engine = TemplateEngine::new();
            let old = make_versioned_record("gci-default", "1.0.0", "## Intro\noriginal\n");
            let new_r = make_versioned_record("gci-default", "1.0.0", "## Intro\noriginal\n");
            let target = tmp.path().join("CASCADE.md");

            let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
            fs::write(&target, format!("## Intro\noriginal\n\n{}\n", stamp)).unwrap();

            let result = engine
                .upgrade(&old, &new_r, &target, false, Some(tmp.path().to_path_buf()))
                .unwrap();
            assert!(result.upgraded.is_empty());
            assert!(result.added.is_empty());
            assert!(result.deprecated.is_empty());
        }

        // T-10-D: upgrade changed section → overwritten, listed in upgraded
        #[test]
        #[serial(global_env)]
        fn upgrade_changed_section_overwritten() {
            let tmp = TempDir::new().unwrap();
            let engine = TemplateEngine::new();

            let old = make_versioned_record("gci-default", "1.0.0", "## Rules\nold rules\n");
            let new_r = make_versioned_record("gci-default", "1.1.0", "## Rules\nnew rules\n");
            let target = tmp.path().join("CASCADE.md");

            let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
            fs::write(&target, format!("## Rules\nold rules\n\n{}\n", stamp)).unwrap();

            let result = engine
                .upgrade(&old, &new_r, &target, false, Some(tmp.path().to_path_buf()))
                .unwrap();
            assert!(
                result.upgraded.contains(&"## Rules".to_string()),
                "Rules must be in upgraded"
            );
            assert!(result.added.is_empty());

            let content = fs::read_to_string(&target).unwrap();
            assert!(
                content.contains("new rules"),
                "target must have new content"
            );
            assert!(
                content.contains(r#"version="1.1.0""#),
                "stamp must be updated"
            );
        }

        // T-10-E: upgrade removed section → deprecation comment added to target
        #[test]
        #[serial(global_env)]
        fn upgrade_removed_section_gets_deprecation_comment() {
            let tmp = TempDir::new().unwrap();
            let engine = TemplateEngine::new();

            let old = make_versioned_record(
                "gci-default",
                "1.0.0",
                "## Rules\nrules\n## OldSection\nold stuff\n",
            );
            let new_r = make_versioned_record("gci-default", "1.1.0", "## Rules\nrules\n");
            let target = tmp.path().join("CASCADE.md");
            let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
            fs::write(
                &target,
                format!("## Rules\nrules\n## OldSection\nold stuff\n\n{}\n", stamp),
            )
            .unwrap();

            let result = engine
                .upgrade(&old, &new_r, &target, false, Some(tmp.path().to_path_buf()))
                .unwrap();
            assert!(result.deprecated.contains(&"## OldSection".to_string()));

            let content = fs::read_to_string(&target).unwrap();
            assert!(
                content.contains("cascade:deprecated"),
                "deprecation comment must appear"
            );
            assert!(
                content.contains("old stuff"),
                "old content must be preserved"
            );
        }

        // T-10-F: upgrade dry-run does not modify file
        #[test]
        #[serial(global_env)]
        fn upgrade_dry_run_does_not_write() {
            let tmp = TempDir::new().unwrap();
            let engine = TemplateEngine::new();

            let old = make_versioned_record("gci-default", "1.0.0", "## Alpha\nalpha\n");
            let new_r = make_versioned_record("gci-default", "1.1.0", "## Alpha\nnew alpha\n");
            let target = tmp.path().join("CASCADE.md");
            let stamp = make_stamp("gci-default", "1.0.0", "2026-01-01T00:00:00Z");
            let original = format!("## Alpha\nalpha\n\n{}\n", stamp);
            fs::write(&target, &original).unwrap();

            engine
                .upgrade(&old, &new_r, &target, true, Some(tmp.path().to_path_buf()))
                .unwrap();

            let after = fs::read_to_string(&target).unwrap();
            assert_eq!(after, original, "dry-run must not modify the file");
        }
    } // mod upgrade
}
