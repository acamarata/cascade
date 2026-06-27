//! Project scanner — walks project roots and builds a `Vec<ProjectRecord>`.
//!
//! Purpose: given the list of roots from `CascadeConfig::effective_projects_dirs()`,
//! walk each root's immediate children (and selected sub-directories of monorepos),
//! classify each directory, and persist the results via `ProjectRegistry`.
//!
//! Inputs:  `Vec<PathBuf>` (effective project roots), `ProjectRegistry` (for persistence)
//! Outputs: `Vec<ProjectRecord>` (all discovered projects, parent_id set for sub-projects)
//! Constraints:
//!   - Skips: `node_modules`, `target`, `.git`, hidden dirs (name starts with `.`)
//!   - Monorepo sub-apps: scan one level under `apps/` and `packages/` subdirs
//!   - NESTED-ROOT DEDUP: if root B is an ancestor of (or equal to) root A, root A's
//!     contents are registered under the inner root; the outer root must not double-register
//!     directories that would also appear under the inner root. The inner root "wins".
//!   - Does NOT recurse arbitrarily — only two levels: root → project → (apps|packages → sub).
//!   - Sync scan (no async): called from tokio::spawn(blocking) context.
//!
//! SPORT: cascade-daemon / discovery / scanner (rag-13)

use super::classifier::classify;
use super::registry::ProjectRegistry;
use cascade_types::project::{IndexStatus, ProjectRecord, ProjectType};
use std::path::{Path, PathBuf};
use tracing::{debug, warn};

// ── Skip-list ─────────────────────────────────────────────────────────────────

const SKIP_DIRS: &[&str] = &["node_modules", "target", ".git"];

fn should_skip(name: &str) -> bool {
    SKIP_DIRS.contains(&name) || name.starts_with('.')
}

// ── ProjectScanner ────────────────────────────────────────────────────────────

/// Walks configured project roots and classifies sub-directories.
pub struct ProjectScanner {
    roots: Vec<PathBuf>,
}

impl ProjectScanner {
    /// Construct a scanner for the given root directories.
    pub fn new(roots: Vec<PathBuf>) -> Self {
        Self { roots }
    }

    /// Scan all roots and persist results through `registry`.
    ///
    /// Returns the full list of `ProjectRecord`s that were upserted.
    pub fn scan(&self, registry: &mut ProjectRegistry) -> Vec<ProjectRecord> {
        // ── Nested-root dedup ─────────────────────────────────────────────────
        // Sort roots so shorter (outer) paths come first, then remove any root
        // that is an ancestor of a later root in the same list. That way, if
        // both `~/Sites` and `~/Sites/nself` are configured, `~/Sites/nself`
        // becomes the canonical root; the scanner will see it as a project entry
        // when walking `~/Sites`, and must not separately walk it as a root AND
        // register it as a child of `~/Sites`.
        //
        // Strategy: for each root, collect the "effective" root set: keep a root
        // only if no other root in the list is strictly contained within it AND
        // is more specific. In practice: inner wins means we keep the most
        // specific root when two roots are nested.
        //
        // Concretely: deduplicate by removing roots that are proper prefixes of
        // another root in the set — the more-specific (longer) root wins.
        let deduped_roots = dedup_nested_roots(&self.roots);

        // Build a fast lookup set of the deduped roots for skip-inner-root logic.
        let root_set: std::collections::HashSet<&Path> =
            deduped_roots.iter().map(|p| p.as_path()).collect();

        let mut records: Vec<ProjectRecord> = Vec::new();

        for root in &deduped_roots {
            if !root.is_dir() {
                warn!(path = %root.display(), "configured projects root does not exist, skipping");
                continue;
            }
            self.walk_root(root, &root_set, registry, &mut records);
        }

        records
    }

    /// Walk one root directory and register any immediate project children.
    ///
    /// Also registers the root itself as a project if it carries recognisable
    /// markers (e.g. when `~/Sites/nself` is a configured root AND itself a
    /// Rust workspace or Node monorepo).
    fn walk_root(
        &self,
        root: &Path,
        root_set: &std::collections::HashSet<&Path>,
        registry: &mut ProjectRegistry,
        records: &mut Vec<ProjectRecord>,
    ) {
        // ── Register the root itself if it is a recognisable project ──────────
        // WHY: a configured root like `~/Sites/myapp` may itself be a Rust
        // crate, Next app, etc. We classify it and register it before walking
        // its children. Children that are also configured roots are skipped
        // (handled below), so there is no double-registration.
        let (root_pt, root_lang, root_fw, root_pm) = classify(root);
        if root_pt != ProjectType::Unknown {
            let root_name = root
                .file_name()
                .and_then(|n| n.to_str())
                .unwrap_or("")
                .to_owned();
            if !root_name.is_empty() {
                let is_monorepo =
                    root_pt != ProjectType::Unknown
                        && (root.join("apps").is_dir() || root.join("packages").is_dir());
                let effective_type = if is_monorepo {
                    ProjectType::Monorepo
                } else {
                    root_pt
                };
                let rec = make_record(root, &root_name, effective_type.clone(), root_lang, root_fw, root_pm, None);
                if let Err(e) = registry.upsert(&rec) {
                    warn!(path = %root.display(), error = %e, "failed to upsert root-as-project record");
                } else {
                    records.push(rec.clone());
                }
                // If the root itself is a monorepo, scan its apps/packages sub-dirs.
                if is_monorepo || effective_type == ProjectType::Monorepo {
                    for sub_dir in ["apps", "packages"] {
                        let sub_root = root.join(sub_dir);
                        if sub_root.is_dir() {
                            self.walk_sub_apps(&sub_root, &rec.id, registry, records);
                        }
                    }
                }
                // Don't walk children further — the root itself is the project.
                return;
            }
        }

        let Ok(entries) = std::fs::read_dir(root) else {
            warn!(path = %root.display(), "cannot read projects root");
            return;
        };

        for entry in entries.flatten() {
            let path = entry.path();
            if !path.is_dir() {
                continue;
            }
            let name = match entry.file_name().to_str() {
                Some(n) => n.to_owned(),
                None => continue,
            };
            if should_skip(&name) {
                continue;
            }

            // If this directory is itself a configured root (inner), skip it here —
            // it will be (or was already) processed as its own root entry.
            if root_set.contains(path.as_path()) {
                debug!(path = %path.display(), "skipping inner configured root during outer walk");
                continue;
            }

            let (pt, lang, fw, pm) = classify(&path);

            // Check for monorepo markers: if apps/ or packages/ exists, treat as
            // Monorepo and walk sub-directories.
            let is_monorepo = path.join("apps").is_dir() || path.join("packages").is_dir();
            let effective_type = if is_monorepo {
                ProjectType::Monorepo
            } else {
                pt.clone()
            };

            let rec = make_record(
                &path,
                &name,
                effective_type.clone(),
                lang.clone(),
                fw.clone(),
                pm.clone(),
                None,
            );

            // Register the top-level record.
            if let Err(e) = registry.upsert(&rec) {
                warn!(path = %path.display(), error = %e, "failed to upsert project record");
            } else {
                records.push(rec.clone());
            }

            // ── Monorepo sub-apps ─────────────────────────────────────────────
            if is_monorepo {
                for sub_dir in ["apps", "packages"] {
                    let sub_root = path.join(sub_dir);
                    if sub_root.is_dir() {
                        self.walk_sub_apps(&sub_root, &rec.id, registry, records);
                    }
                }
            }
        }
    }

    /// Walk one level under a monorepo sub-directory (`apps/` or `packages/`).
    fn walk_sub_apps(
        &self,
        sub_root: &Path,
        parent_id: &str,
        registry: &mut ProjectRegistry,
        records: &mut Vec<ProjectRecord>,
    ) {
        let Ok(entries) = std::fs::read_dir(sub_root) else {
            return;
        };
        for entry in entries.flatten() {
            let path = entry.path();
            if !path.is_dir() {
                continue;
            }
            let name = match entry.file_name().to_str() {
                Some(n) => n.to_owned(),
                None => continue,
            };
            if should_skip(&name) {
                continue;
            }

            let (pt, lang, fw, pm) = classify(&path);
            let rec = make_record(
                &path,
                &name,
                pt,
                lang,
                fw,
                pm,
                Some(parent_id.to_owned()),
            );

            if let Err(e) = registry.upsert(&rec) {
                warn!(path = %path.display(), error = %e, "failed to upsert sub-app record");
            } else {
                records.push(rec);
            }
        }
    }
}

// ── Nested-root dedup ─────────────────────────────────────────────────────────

/// Remove any root that is a proper prefix of another root in the list.
///
/// Inner (longer/more-specific) roots win. Example:
///   `[~/Sites, ~/Sites/nself]` → `[~/Sites, ~/Sites/nself]` with the
///   understanding that when walking `~/Sites`, we skip `~/Sites/nself` because
///   it is itself a root.
///
/// WHY: this approach keeps both roots in the deduped list but makes the walker
/// skip the inner when encountered as a child of the outer, preventing double
/// registration. The inner is walked as its own root entry separately.
pub fn dedup_nested_roots(roots: &[PathBuf]) -> Vec<PathBuf> {
    // Filter: keep a root only if it is NOT a proper prefix of any other root.
    // i.e. if root A is a prefix of root B, A is the outer — but we KEEP A as
    // a configured root (to walk its other children). We just skip B when
    // encountered as a child of A.
    //
    // Result: all roots are kept; the skip-inner logic in walk_root handles dedup.
    // The only roots we remove are exact duplicates.
    let mut seen = std::collections::HashSet::new();
    let mut out = Vec::new();
    for root in roots {
        let canonical = root.canonicalize().unwrap_or_else(|_| root.clone());
        if seen.insert(canonical.clone()) {
            out.push(root.clone());
        }
    }
    out
}

// ── Record factory ────────────────────────────────────────────────────────────

fn make_record(
    path: &Path,
    name: &str,
    project_type: ProjectType,
    language: Option<String>,
    framework: Option<String>,
    package_manager: Option<String>,
    parent_id: Option<String>,
) -> ProjectRecord {
    ProjectRecord {
        id: ProjectRecord::id_for(path),
        root: path.to_path_buf(),
        name: name.to_owned(),
        project_type,
        language,
        framework,
        package_manager,
        parent_id,
        index_status: IndexStatus::Pending,
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn tempdir() -> TempDir {
        tempfile::tempdir().expect("create tempdir")
    }

    fn mkdir(base: &Path, name: &str) -> PathBuf {
        let p = base.join(name);
        fs::create_dir_all(&p).unwrap();
        p
    }

    fn touch(dir: &Path, name: &str) {
        fs::write(dir.join(name), b"").unwrap();
    }

    fn open_registry(dir: &Path) -> ProjectRegistry {
        ProjectRegistry::open(dir).expect("open registry")
    }

    // ── nested-root dedup ─────────────────────────────────────────────────────

    #[test]
    fn nested_root_inner_wins_no_double_register() {
        // Layout:
        //   outer/
        //     inner/               ← also a configured root
        //       Cargo.toml         ← inner is a RustCrate
        //     other_project/
        //       package.json
        //
        // Both outer/ and outer/inner/ are configured roots.
        // Expected: inner/ is registered once (as its own root's top-level scan),
        //           NOT twice (not also as a child of outer/).
        let d = tempdir();
        let outer = d.path().to_path_buf();
        let inner = mkdir(&outer, "inner");
        touch(&inner, "Cargo.toml");
        let other = mkdir(&outer, "other_project");
        touch(&other, "package.json");

        let reg_dir = tempdir();
        let mut registry = open_registry(reg_dir.path());

        let scanner = ProjectScanner::new(vec![outer.clone(), inner.clone()]);
        let records = scanner.scan(&mut registry);

        // Count how many times inner/ appears.
        let inner_count = records.iter().filter(|r| r.root == inner).count();
        assert_eq!(
            inner_count, 1,
            "inner root must appear exactly once, got {inner_count}"
        );

        // other_project should appear once (as child of outer).
        let other_count = records.iter().filter(|r| r.root == other).count();
        assert_eq!(other_count, 1, "other_project must appear exactly once");
    }

    #[test]
    fn rust_crate_registered() {
        let d = tempdir();
        let proj = mkdir(d.path(), "mylib");
        touch(&proj, "Cargo.toml");

        let reg_dir = tempdir();
        let mut registry = open_registry(reg_dir.path());
        let scanner = ProjectScanner::new(vec![d.path().to_path_buf()]);
        let records = scanner.scan(&mut registry);

        assert_eq!(records.len(), 1);
        assert_eq!(records[0].project_type, ProjectType::RustCrate);
        assert_eq!(records[0].name, "mylib");
    }

    #[test]
    fn monorepo_sub_apps_registered_with_parent_id() {
        let d = tempdir();
        let monorepo = mkdir(d.path(), "monorepo");
        touch(&monorepo, "package.json");
        let apps_dir = mkdir(&monorepo, "apps");
        let web = mkdir(&apps_dir, "web");
        touch(&web, "package.json");
        touch(&web, "vite.config.ts");
        let api = mkdir(&apps_dir, "api");
        touch(&api, "Cargo.toml");

        let reg_dir = tempdir();
        let mut registry = open_registry(reg_dir.path());
        let scanner = ProjectScanner::new(vec![d.path().to_path_buf()]);
        let records = scanner.scan(&mut registry);

        // monorepo root + web + api = 3 records.
        assert_eq!(records.len(), 3, "expected 3 records: monorepo + web + api");

        let monorepo_rec = records.iter().find(|r| r.name == "monorepo").unwrap();
        assert_eq!(monorepo_rec.project_type, ProjectType::Monorepo);
        assert!(monorepo_rec.parent_id.is_none());

        let web_rec = records.iter().find(|r| r.name == "web").unwrap();
        assert_eq!(web_rec.project_type, ProjectType::ViteApp);
        assert_eq!(web_rec.parent_id.as_deref(), Some(monorepo_rec.id.as_str()));

        let api_rec = records.iter().find(|r| r.name == "api").unwrap();
        assert_eq!(api_rec.project_type, ProjectType::RustCrate);
        assert_eq!(api_rec.parent_id.as_deref(), Some(monorepo_rec.id.as_str()));
    }

    #[test]
    fn skip_dirs_excluded() {
        let d = tempdir();
        mkdir(d.path(), "node_modules");
        mkdir(d.path(), "target");
        mkdir(d.path(), ".git");
        let real = mkdir(d.path(), "real_project");
        touch(&real, "Cargo.toml");

        let reg_dir = tempdir();
        let mut registry = open_registry(reg_dir.path());
        let scanner = ProjectScanner::new(vec![d.path().to_path_buf()]);
        let records = scanner.scan(&mut registry);

        assert_eq!(records.len(), 1, "only real_project should be registered");
        assert_eq!(records[0].name, "real_project");
    }

    #[test]
    fn duplicate_roots_deduplicated() {
        let d = tempdir();
        let proj = mkdir(d.path(), "proj");
        touch(&proj, "package.json");

        let reg_dir = tempdir();
        let mut registry = open_registry(reg_dir.path());
        // Pass the same root twice.
        let root = d.path().to_path_buf();
        let scanner = ProjectScanner::new(vec![root.clone(), root]);
        let records = scanner.scan(&mut registry);

        // proj should appear only once even though the root was listed twice.
        let count = records.iter().filter(|r| r.name == "proj").count();
        assert_eq!(count, 1, "duplicate root must not produce duplicate records");
    }
}
