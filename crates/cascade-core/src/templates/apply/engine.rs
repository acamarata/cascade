//! [`TemplateEngine`] — stateless engine for applying templates to `CASCADE.md`.

use super::merge::{atomic_write, build_merged_content, replace_section_in_content};
use super::options::ApplyOptions;
use super::path::canonical_path;
use super::stamp::{extract_stamps, is_stamped};
use super::upgrade_helpers::{add_deprecation_comment, replace_or_append_stamp};
use super::super::registry::TemplateRegistry;
use super::super::section_parser::{parse_sections, Section};
use cascade_types::{
    error::{CascadeError, Result},
    ApplyResult, DiffResult, TemplateRecord, UpgradeResult,
};
use std::collections::HashMap;
use std::path::{Path, PathBuf};

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
        let mut to_append: Vec<Section> = Vec::new();
        let mut forced_replacements: Vec<(String, Section)> = Vec::new();

        for tmpl_sec in &template_sections {
            if tmpl_sec.heading.is_empty() {
                continue;
            }
            match target_map.get(&tmpl_sec.heading) {
                None => {
                    applied.push(tmpl_sec.heading.clone());
                    to_append.push(tmpl_sec.clone());
                }
                Some((_, existing)) => {
                    if existing.body.trim() == tmpl_sec.body.trim() {
                        skipped.push(tmpl_sec.heading.clone());
                    } else if opts.force {
                        applied.push(tmpl_sec.heading.clone());
                        forced_replacements.push((tmpl_sec.heading.clone(), tmpl_sec.clone()));
                    } else {
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
    /// versions, then applies changes.
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

        let mut upgraded: Vec<String> = Vec::new();
        let mut to_force: Vec<(String, Section)> = Vec::new();
        let mut added: Vec<String> = Vec::new();
        let mut to_append: Vec<Section> = Vec::new();

        for sec in new_secs_ordered.iter().filter(|s| !s.heading.is_empty()) {
            let h = &sec.heading;
            if let Some(old_sec) = old_secs.get(h) {
                if old_sec.body.trim() != sec.body.trim() {
                    upgraded.push(h.clone());
                    to_force.push((h.clone(), sec.clone()));
                }
            } else {
                if !target_secs.contains_key(h.as_str()) {
                    added.push(h.clone());
                    to_append.push(sec.clone());
                }
            }
        }

        // ── Write (unless dry-run) ────────────────────────────────────────────
        if !dry_run {
            let mut content = target_content.clone();

            for (heading, new_sec) in &to_force {
                content = replace_section_in_content(&content, heading, new_sec);
            }

            for heading in &deprecated_headings {
                if target_secs.contains_key(heading.as_str()) {
                    content = add_deprecation_comment(&content, heading);
                }
            }

            if !content.is_empty() && !content.ends_with('\n') {
                content.push('\n');
            }

            for sec in &to_append {
                if !content.trim().is_empty() {
                    content.push('\n');
                }
                content.push_str(&format!("{}\n", sec.heading));
                content.push_str(&sec.body);
            }

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
    pub fn upgrade_by_id(
        &self,
        registry: &TemplateRegistry,
        id: &str,
        new_record: &TemplateRecord,
        target_path: &Path,
        dry_run: bool,
        root: Option<PathBuf>,
    ) -> Result<UpgradeResult> {
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
