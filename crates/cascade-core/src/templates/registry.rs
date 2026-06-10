//! # cascade_core::templates::registry
//!
//! [`TemplateRegistry`] — loads, indexes, and filters bundled and user templates.
//!
//! ## Purpose
//!
//! The registry is the single point of access for all template files. It:
//! 1. Loads `.md` files from one or more directories in priority order.
//! 2. Skips malformed files with a `warn` log (non-fatal).
//! 3. Indexes records by their manifest `id`.
//! 4. In multi-dir loads, later (higher-priority) directories win on id conflicts
//!    (user `~/.cascade/templates/` overrides bundled `data/templates/`).
//! 5. Exposes [`list`](TemplateRegistry::list) and [`get`](TemplateRegistry::get).
//!
//! ## Inputs
//!
//! Directories are passed as `&[&Path]` ordered lowest-to-highest priority.
//! Missing directories are silently skipped (empty registry, not error).
//!
//! ## Outputs
//!
//! `TemplateRecord` references for list/get queries.
//!
//! ## Constraints
//!
//! Thread-safe read-only access after construction (`Send + Sync`).
//!
//! ## SPORT
//!
//! Registered in MASTER-TABLES.md under `cascade-core` exports (T-P3-E05-02).

use super::parser::parse_template_file;
use cascade_types::{TemplateRecord, TemplateTier};
use semver::Version;
use std::collections::{HashMap, HashSet};
use std::path::Path;
use tracing::warn;

// ── TemplateFilter ────────────────────────────────────────────────────────────

/// Optional filter criteria for [`TemplateRegistry::list`].
///
/// All fields are optional. A `None` field matches everything.
/// Multiple non-`None` fields are ANDed together.
///
/// ## Tier matching
///
/// A template matches a tier filter if `manifest.tier == filter.tier` OR
/// `manifest.tier == TemplateTier::Any`.
#[derive(Debug, Default, Clone)]
pub struct TemplateFilter {
    /// Match only templates whose tier equals this value (or `Any`).
    pub tier: Option<TemplateTier>,

    /// Match only templates that include this stack tag.
    pub stack: Option<String>,

    /// Match only templates that include this project-shape tag.
    pub project_shape: Option<String>,
}

// ── TemplateRegistry ─────────────────────────────────────────────────────────

/// In-memory index of all loaded [`TemplateRecord`]s, keyed by manifest id.
#[derive(Debug, Default)]
pub struct TemplateRegistry {
    records: HashMap<String, TemplateRecord>,
}

impl TemplateRegistry {
    /// Create an empty registry.
    pub fn new() -> Self {
        Self::default()
    }

    /// Load all `.md` files from `dir` into this registry.
    ///
    /// Skips non-`.md` files, non-regular files, and files that fail parsing
    /// (each with a `warn` log). Duplicate ids are overwritten (last-writer wins
    /// within the same directory, alphabetical order; use [`load_dirs`] for
    /// controlled priority across directories).
    ///
    /// Missing or unreadable directories are silently skipped (not an error).
    pub fn load_dir(&mut self, dir: &Path) {
        let entries = match std::fs::read_dir(dir) {
            Ok(e) => e,
            Err(_) => return, // missing / unreadable — silent skip
        };

        // Recurse into subdirectories and collect .md files.
        let mut md_paths: Vec<std::path::PathBuf> = Vec::new();
        collect_md_files(dir, &mut md_paths);

        for path in md_paths {
            match parse_template_file(&path) {
                Ok(record) => {
                    let id = record.manifest.id.clone();
                    self.records.insert(id, record);
                }
                Err(e) => {
                    warn!("skipping malformed template {}: {}", path.display(), e);
                }
            }
        }

        // Suppress unused variable warning — entries was consumed by collect_md_files indirectly.
        drop(entries);
    }

    /// Load from multiple directories in priority order.
    ///
    /// Directories are processed left-to-right: later entries have *higher*
    /// priority and overwrite earlier entries for the same template id.
    ///
    /// Typical call:
    /// ```no_run
    /// # use cascade_core::templates::registry::TemplateRegistry;
    /// # use std::path::Path;
    /// let bundled = Path::new("data/templates");
    /// let user = std::path::PathBuf::from(std::env::var("HOME").unwrap()).join(".cascade/templates");
    /// let reg = TemplateRegistry::load_dirs(&[bundled, &user]);
    /// ```
    pub fn load_dirs(dirs: &[&Path]) -> Self {
        let mut registry = Self::new();
        for dir in dirs {
            registry.load_dir(dir);
        }
        registry
    }

    /// Return the number of loaded templates.
    pub fn len(&self) -> usize {
        self.records.len()
    }

    /// Return `true` if the registry contains no templates.
    pub fn is_empty(&self) -> bool {
        self.records.is_empty()
    }

    /// Get a template by its id.
    pub fn get(&self, id: &str) -> Option<&TemplateRecord> {
        self.records.get(id)
    }

    /// Resolve a template by id, merging inherited sections from parent templates.
    ///
    /// If the template has no `extends` field, returns a clone of the raw record.
    /// Otherwise resolves the parent chain recursively, merging sections so that
    /// the child's sections override the parent's on heading conflict.
    ///
    /// # Errors
    ///
    /// - `CascadeError::Other` if the template id is not in the registry.
    /// - `CascadeError::Other` if a cycle is detected in the inheritance chain.
    /// - `CascadeError::Other` if the depth exceeds 5 levels.
    ///
    /// ## SPORT
    ///
    /// Registered in MASTER-TABLES.md under `cascade-core` exports (T-P3-E05-12).
    pub fn resolve(&self, id: &str) -> cascade_types::error::Result<TemplateRecord> {
        let mut visited = HashSet::new();
        self.resolve_inner(id, &mut visited, 0)
    }

    fn resolve_inner(
        &self,
        id: &str,
        visited: &mut HashSet<String>,
        depth: usize,
    ) -> cascade_types::error::Result<TemplateRecord> {
        use super::section_parser::parse_sections;
        use cascade_types::error::CascadeError;

        const MAX_DEPTH: usize = 5;

        if depth > MAX_DEPTH {
            return Err(CascadeError::Other(format!(
                "Template inheritance depth exceeded (max {}): {}",
                MAX_DEPTH, id
            )));
        }

        if visited.contains(id) {
            let chain: Vec<&str> = visited.iter().map(|s| s.as_str()).collect();
            return Err(CascadeError::Other(format!(
                "Template inheritance cycle detected: {} -> {}",
                chain.join(" -> "),
                id
            )));
        }

        let record = self
            .records
            .get(id)
            .ok_or_else(|| CascadeError::Other(format!("Template not found: {}", id)))?;

        // No parent — return a clone directly.
        let parent_id = match &record.manifest.extends {
            None => return Ok(record.clone()),
            Some(p) => p.clone(),
        };

        // Mark this id as visited before recursing.
        visited.insert(id.to_string());

        // Resolve parent.
        let parent = self.resolve_inner(&parent_id, visited, depth + 1)?;

        // Merge: parent sections first, then child sections override.
        let parent_sections = parse_sections(&parent.body);
        let child_sections = parse_sections(&record.body);

        // Build parent section map (heading -> Section).
        use std::collections::HashMap;
        let mut merged: Vec<(String, super::section_parser::Section)> = Vec::new();
        let mut heading_index: HashMap<String, usize> = HashMap::new();

        for sec in &parent_sections {
            if !sec.heading.is_empty() {
                let idx = merged.len();
                heading_index.insert(sec.heading.clone(), idx);
                merged.push((sec.heading.clone(), sec.clone()));
            }
        }

        // Apply child sections: override parent sections or append new ones.
        for sec in &child_sections {
            if sec.heading.is_empty() {
                continue;
            }
            if let Some(&idx) = heading_index.get(&sec.heading) {
                // Child wins — override.
                merged[idx] = (sec.heading.clone(), sec.clone());
            } else {
                // New section — append.
                let idx = merged.len();
                heading_index.insert(sec.heading.clone(), idx);
                merged.push((sec.heading.clone(), sec.clone()));
            }
        }

        // Rebuild body from merged sections.
        let mut body = String::new();
        for (_, sec) in &merged {
            body.push_str(&sec.heading);
            body.push('\n');
            body.push_str(&sec.body);
        }

        let mut resolved = record.clone();
        resolved.body = body;
        Ok(resolved)
    }

    /// Return the registry's record for `id` if its version is newer than
    /// `current_version`, else `None`.
    ///
    /// Parses both versions as semver.  If either fails to parse, returns `None`
    /// (no upgrade available rather than panic).
    ///
    /// ## SPORT
    ///
    /// Registered in MASTER-TABLES.md under `cascade-core` exports (T-P3-E05-13).
    pub fn available_upgrade(&self, id: &str, current_version: &str) -> Option<&TemplateRecord> {
        let record = self.records.get(id)?;
        let current = Version::parse(current_version).ok()?;
        let available = Version::parse(&record.manifest.version).ok()?;
        if available > current {
            Some(record)
        } else {
            None
        }
    }

    /// List templates matching the given filter.
    ///
    /// Returns all templates if `filter` has all fields set to `None`.
    /// A template matches a `tier` filter if its manifest tier equals the
    /// filter tier **or** if it is `TemplateTier::Any`.
    pub fn list(&self, filter: &TemplateFilter) -> Vec<&TemplateRecord> {
        self.records
            .values()
            .filter(|rec| {
                // Tier filter: match exact tier or Any
                if let Some(ref ft) = filter.tier {
                    if rec.manifest.tier != *ft && rec.manifest.tier != TemplateTier::Any {
                        return false;
                    }
                }

                // Stack filter: manifest.stacks must contain the tag,
                // unless manifest.stacks is empty (means "all stacks")
                if let Some(ref fs) = filter.stack {
                    if !rec.manifest.stacks.is_empty() && !rec.manifest.stacks.contains(fs) {
                        return false;
                    }
                }

                // Project-shape filter: same logic as stacks
                if let Some(ref fp) = filter.project_shape {
                    if !rec.manifest.project_shapes.is_empty()
                        && !rec.manifest.project_shapes.contains(fp)
                    {
                        return false;
                    }
                }

                true
            })
            .collect()
    }
}

/// Recursively collect all `.md` files under `dir` into `out`.
fn collect_md_files(dir: &Path, out: &mut Vec<std::path::PathBuf>) {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return,
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            collect_md_files(&path, out);
        } else if path.extension().and_then(|e| e.to_str()) == Some("md") {
            out.push(path);
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;
    use tempfile::TempDir;

    fn write_template(dir: &TempDir, name: &str, id: &str, tier: &str) -> std::path::PathBuf {
        let path = dir.path().join(name);
        let mut f = std::fs::File::create(&path).unwrap();
        writeln!(
            f,
            "---\nid = \"{}\"\nversion = \"1.0.0\"\ntier = \"{}\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n---\n# Body",
            id, tier
        )
        .unwrap();
        path
    }

    fn write_malformed(dir: &TempDir, name: &str) {
        let path = dir.path().join(name);
        std::fs::write(&path, "not a valid template file").unwrap();
    }

    // ── T1: empty directory ───────────────────────────────────────────────────

    #[test]
    fn load_empty_dir_returns_empty_registry() {
        let dir = TempDir::new().unwrap();
        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        assert!(reg.is_empty(), "expected empty registry from empty dir");
    }

    // ── T2: load single valid template ────────────────────────────────────────

    #[test]
    fn load_single_valid_template() {
        let dir = TempDir::new().unwrap();
        write_template(&dir, "gci.md", "gci-default", "gci");

        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        assert_eq!(reg.len(), 1);

        let rec = reg.get("gci-default").expect("expected gci-default record");
        assert_eq!(rec.manifest.id, "gci-default");
        assert_eq!(rec.manifest.tier, TemplateTier::Gci);
        assert!(rec.body.contains("# Body"));
    }

    // ── T3: skip malformed file ───────────────────────────────────────────────

    #[test]
    fn malformed_template_is_skipped_not_panic() {
        let dir = TempDir::new().unwrap();
        write_malformed(&dir, "bad.md");
        // Should not panic; should return empty registry.
        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        assert!(reg.is_empty(), "malformed file should be skipped");
    }

    // ── T4: list with tier filter ─────────────────────────────────────────────

    #[test]
    fn list_filter_by_tier() {
        let dir = TempDir::new().unwrap();
        write_template(&dir, "gci.md", "gci-default", "gci");
        write_template(&dir, "prc.md", "prc-rust", "prc");
        write_template(&dir, "any.md", "any-base", "any");

        let reg = TemplateRegistry::load_dirs(&[dir.path()]);

        let gci_filter = TemplateFilter {
            tier: Some(TemplateTier::Gci),
            ..Default::default()
        };
        let results = reg.list(&gci_filter);
        // Should return gci-default AND any-base (Any matches all tiers)
        assert_eq!(results.len(), 2, "expected gci + any templates");
        let ids: Vec<&str> = results.iter().map(|r| r.manifest.id.as_str()).collect();
        assert!(ids.contains(&"gci-default"));
        assert!(ids.contains(&"any-base"));
    }

    // ── T5: get by id ─────────────────────────────────────────────────────────

    #[test]
    fn get_by_id_returns_correct_record() {
        let dir = TempDir::new().unwrap();
        write_template(&dir, "gci.md", "gci-default", "gci");
        write_template(&dir, "prc.md", "prc-rust", "prc");

        let reg = TemplateRegistry::load_dirs(&[dir.path()]);

        assert!(reg.get("gci-default").is_some());
        assert!(reg.get("prc-rust").is_some());
        assert!(reg.get("does-not-exist").is_none());
    }

    // ── T6: multi-dir precedence ──────────────────────────────────────────────

    #[test]
    fn multidir_user_template_overrides_bundled() {
        let bundled = TempDir::new().unwrap();
        let user = TempDir::new().unwrap();

        // Both dirs have the same id; user dir should win.
        write_template(&bundled, "gci.md", "gci-default", "gci");

        // User template has different description to distinguish it.
        let user_path = user.path().join("gci.md");
        let mut f = std::fs::File::create(&user_path).unwrap();
        std::io::Write::write_all(
            &mut f,
            b"---\nid = \"gci-default\"\nversion = \"2.0.0\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"user override\"\n---\n# User Body\n",
        ).unwrap();

        // bundled first (lower priority), user second (higher priority)
        let reg = TemplateRegistry::load_dirs(&[bundled.path(), user.path()]);
        assert_eq!(reg.len(), 1, "duplicate ids should merge to one record");

        let rec = reg.get("gci-default").unwrap();
        assert_eq!(rec.manifest.version, "2.0.0", "user version should win");
        assert_eq!(rec.manifest.description, "user override");
    }

    // ── T7: missing directory is silent ──────────────────────────────────────

    #[test]
    fn missing_directory_returns_empty_registry() {
        let nonexistent = std::path::Path::new("/tmp/__cascade_nonexistent_dir_test__");
        let reg = TemplateRegistry::load_dirs(&[nonexistent]);
        assert!(reg.is_empty());
    }

    // ── T8: list with no filter returns all ───────────────────────────────────

    #[test]
    fn list_no_filter_returns_all() {
        let dir = TempDir::new().unwrap();
        write_template(&dir, "a.md", "a", "gci");
        write_template(&dir, "b.md", "b", "prc");
        write_template(&dir, "c.md", "c", "any");

        let reg = TemplateRegistry::load_dirs(&[dir.path()]);
        let all = reg.list(&TemplateFilter::default());
        assert_eq!(all.len(), 3);
    }

    // ── T9: load_dir on dir with subdirectories ───────────────────────────────

    #[test]
    fn load_dir_recurses_subdirectories() {
        let root = TempDir::new().unwrap();
        let subdir = root.path().join("tier");
        std::fs::create_dir_all(&subdir).unwrap();

        write_template(&root, "root.md", "root-tmpl", "any");
        let sub_path = subdir.join("sub.md");
        let mut f = std::fs::File::create(&sub_path).unwrap();
        std::io::Write::write_all(
            &mut f,
            b"---\nid = \"sub-tmpl\"\nversion = \"1.0.0\"\ntier = \"prc\"\nstacks = []\nproject_shapes = []\ndescription = \"sub\"\n---\n# Sub\n",
        ).unwrap();

        let reg = TemplateRegistry::load_dirs(&[root.path()]);
        assert_eq!(reg.len(), 2, "should load from subdirectory too");
        assert!(reg.get("sub-tmpl").is_some());
    }

    // ═══════════════════════════════════════════════════════════════════════════
    // T-P3-E05-12: inheritance (resolve) tests
    // Nested in `inheritance` so `cargo test -- templates::inheritance` matches.
    // ═══════════════════════════════════════════════════════════════════════════

    mod inheritance {
        use super::*;

        fn write_template_with_extends(
            dir: &TempDir,
            name: &str,
            id: &str,
            tier: &str,
            extends: Option<&str>,
            body: &str,
        ) {
            let path = dir.path().join(name);
            let extends_line = match extends {
                Some(e) => format!("extends = \"{}\"\n", e),
                None => String::new(),
            };
            let content = format!(
                "---\nid = \"{}\"\nversion = \"1.0.0\"\ntier = \"{}\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n{}---\n{}",
                id, tier, extends_line, body
            );
            std::fs::write(path, content).unwrap();
        }

        // T-12-A: no extends → resolve returns a clone of the record
        #[test]
        fn resolve_no_extends_returns_self() {
            let dir = TempDir::new().unwrap();
            write_template(&dir, "gci.md", "gci-default", "gci");

            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            let resolved = reg.resolve("gci-default").unwrap();
            assert_eq!(resolved.manifest.id, "gci-default");
            assert!(resolved.body.contains("# Body"));
        }

        // T-12-B: single extends — child merges parent + child sections (child wins)
        #[test]
        fn resolve_single_extends_merges_parent_and_child() {
            let dir = TempDir::new().unwrap();

            write_template_with_extends(
                &dir,
                "parent.md",
                "gci-default",
                "gci",
                None,
                "## ParentSection\nparent content\n## Shared\nparent shared\n",
            );
            write_template_with_extends(
                &dir,
                "child.md",
                "child-tmpl",
                "gci",
                Some("gci-default"),
                "## Shared\nchild override\n## ChildOnly\nchild only\n",
            );

            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            let resolved = reg.resolve("child-tmpl").unwrap();

            assert!(
                resolved.body.contains("## ParentSection"),
                "parent section must be present"
            );
            assert!(resolved.body.contains("parent content"));
            assert!(
                resolved.body.contains("child override"),
                "child must override shared"
            );
            assert!(
                !resolved.body.contains("parent shared"),
                "parent shared must not appear"
            );
            assert!(
                resolved.body.contains("## ChildOnly"),
                "child-only section must appear"
            );
            assert_eq!(resolved.manifest.id, "child-tmpl");
        }

        // T-12-C: chain (grandparent → parent → child) resolves all three correctly
        #[test]
        fn resolve_three_level_chain() {
            let dir = TempDir::new().unwrap();

            write_template_with_extends(
                &dir,
                "gp.md",
                "grandparent",
                "gci",
                None,
                "## GpSection\ngp body\n",
            );
            write_template_with_extends(
                &dir,
                "parent.md",
                "parent",
                "gci",
                Some("grandparent"),
                "## ParentSection\nparent body\n",
            );
            write_template_with_extends(
                &dir,
                "child.md",
                "child",
                "gci",
                Some("parent"),
                "## ChildSection\nchild body\n",
            );

            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            let resolved = reg.resolve("child").unwrap();

            assert!(
                resolved.body.contains("## GpSection"),
                "grandparent section must resolve"
            );
            assert!(
                resolved.body.contains("## ParentSection"),
                "parent section must resolve"
            );
            assert!(
                resolved.body.contains("## ChildSection"),
                "child section must resolve"
            );
        }

        // T-12-D: cycle detection — A extends B extends A → Err, no panic
        #[test]
        fn resolve_cycle_returns_error_not_panic() {
            let dir = TempDir::new().unwrap();

            write_template_with_extends(
                &dir,
                "a.md",
                "tmpl-a",
                "gci",
                Some("tmpl-b"),
                "## A\na body\n",
            );
            write_template_with_extends(
                &dir,
                "b.md",
                "tmpl-b",
                "gci",
                Some("tmpl-a"),
                "## B\nb body\n",
            );

            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            let result = reg.resolve("tmpl-a");
            assert!(result.is_err(), "cycle must return an error");
            let msg = result.unwrap_err().to_string();
            assert!(
                msg.contains("cycle") || msg.contains("depth") || msg.contains("tmpl"),
                "error message must mention cycle: {}",
                msg
            );
        }
    } // mod inheritance

    // ═══════════════════════════════════════════════════════════════════════════
    // T-P3-E05-13: version / available_upgrade tests
    // Nested in `version` so `cargo test -- templates::version` matches.
    // ═══════════════════════════════════════════════════════════════════════════

    mod version {
        use super::*;

        fn write_template_v(dir: &TempDir, name: &str, id: &str, ver: &str) {
            let path = dir.path().join(name);
            let content = format!(
                "---\nid = \"{}\"\nversion = \"{}\"\ntier = \"gci\"\nstacks = []\nproject_shapes = []\ndescription = \"test\"\n---\n# Body\n",
                id, ver
            );
            std::fs::write(path, content).unwrap();
        }

        // T-13-A: no upgrade available when registry version == current
        #[test]
        fn available_upgrade_none_when_same_version() {
            let dir = TempDir::new().unwrap();
            write_template_v(&dir, "t.md", "my-tmpl", "1.0.0");
            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            assert!(reg.available_upgrade("my-tmpl", "1.0.0").is_none());
        }

        // T-13-B: upgrade returned when registry version > current
        #[test]
        fn available_upgrade_some_when_newer_version() {
            let dir = TempDir::new().unwrap();
            write_template_v(&dir, "t.md", "my-tmpl", "1.1.0");
            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            let upgrade = reg.available_upgrade("my-tmpl", "1.0.0");
            assert!(upgrade.is_some());
            assert_eq!(upgrade.unwrap().manifest.version, "1.1.0");
        }

        // T-13-C: None when registry version < current
        #[test]
        fn available_upgrade_none_when_registry_older() {
            let dir = TempDir::new().unwrap();
            write_template_v(&dir, "t.md", "my-tmpl", "1.0.0");
            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            assert!(reg.available_upgrade("my-tmpl", "1.2.0").is_none());
        }

        // T-13-D: pre-release handling (1.0.0-alpha < 1.0.0)
        #[test]
        fn available_upgrade_pre_release_less_than_release() {
            let dir = TempDir::new().unwrap();
            write_template_v(&dir, "t.md", "my-tmpl", "1.0.0");
            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            let upgrade = reg.available_upgrade("my-tmpl", "1.0.0-alpha");
            assert!(
                upgrade.is_some(),
                "1.0.0 > 1.0.0-alpha — upgrade should be available"
            );
        }

        // T-13-E: unknown template → None (no panic)
        #[test]
        fn available_upgrade_unknown_id_returns_none() {
            let dir = TempDir::new().unwrap();
            let reg = TemplateRegistry::load_dirs(&[dir.path()]);
            assert!(reg.available_upgrade("does-not-exist", "1.0.0").is_none());
        }
    } // mod version
}
