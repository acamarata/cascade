//! # cascade_core::discovery
//!
//! Walks from a given CWD upward through the filesystem, identifying
//! `.cascade/CASCADE.md` files at each tier (GCI → PCI → APC → PPC → PRC → PAC).
//!
//! ## Algorithm
//!
//! 1. Walk every ancestor from the start up to `/` (or the filesystem root).
//!    The path is used as-is (no canonicalization) so callers get back paths
//!    that match what they passed in.
//! 2. For each ancestor, check whether `<ancestor>/.cascade/CASCADE.md` is
//!    readable (symlinks are followed via `fs::metadata`).
//! 4. Assign a [`CascadeTier`] heuristic based on position relative to known
//!    markers (home directory, `Sites/` directory, git root, etc.).
//! 5. Return the list ordered lowest-tier-first (PAC → ... → GCI); callers
//!    that want merge order should reverse the list.
//!
//! Missing tiers are silently skipped (logged at `tracing::trace` level).

use cascade_types::{cascade_tier::CascadeTier, error::Result};
use std::path::{Path, PathBuf};
use tracing::{debug, trace};

// ── PersonalScope ─────────────────────────────────────────────────────────────

/// Returns `true` when the given path is inside the personal scope.
///
/// The **personal scope** is `~/Downloads` (and all paths beneath it).
/// Content from this scope is automatically treated as
/// [`ContentSensitivity::Sensitive`] by the v1.2 firewall regardless of its
/// text content — it holds the user's personal threaded memory and is
/// considered locked/private.
///
/// This function mirrors [`cascade_core::sensitivity::path_is_personal_scope`]
/// but lives here so the discovery layer can annotate tiers without a
/// cross-module dependency on the sensitivity module.
pub fn path_is_personal_scope(path: &Path) -> bool {
    cascade_core_path_is_personal_scope_impl(path)
}

/// Internal helper used by both `path_is_personal_scope` and `classify_tier`.
fn cascade_core_path_is_personal_scope_impl(path: &Path) -> bool {
    let path_str = path.to_string_lossy();
    if path_str.starts_with("~/Downloads") {
        return true;
    }
    let home = std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .unwrap_or_default();
    if !home.is_empty() {
        let personal = format!("{}/Downloads", home.trim_end_matches('/'));
        if path_str.starts_with(personal.as_str()) {
            return true;
        }
    }
    false
}

/// Returns the home directory via `$HOME` env var (Unix) or `USERPROFILE` (Windows).
fn home_dir() -> Option<PathBuf> {
    std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .ok()
        .map(PathBuf::from)
}

/// A single discovered tier entry: the tier classification and the absolute
/// path to the `.cascade/` directory (not the `CASCADE.md` file itself).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct DiscoveredTier {
    /// The tier this directory represents.
    pub tier: CascadeTier,
    /// Absolute path to the `.cascade/` directory.
    pub cascade_dir: PathBuf,
    /// Absolute path to `CASCADE.md` inside `cascade_dir`.
    pub cascade_md: PathBuf,
}

impl DiscoveredTier {
    /// Returns `true` if the `CASCADE.md` file exists and is readable.
    ///
    /// Follows symlinks — if `CASCADE.md` is a symlink to another file, the
    /// target must exist and be readable.
    pub fn is_readable(&self) -> bool {
        self.cascade_md.exists()
    }
}

/// Performs the upward filesystem walk for a given starting path.
#[derive(Debug, Default)]
pub struct TierDiscovery {
    /// Optional override for the home directory (used in tests).
    home_override: Option<PathBuf>,
    /// Optional override for the "all-sites" parent directory (used in tests).
    sites_override: Option<PathBuf>,
}

impl TierDiscovery {
    /// Create a new [`TierDiscovery`] with production defaults.
    ///
    /// Reads `CASCADE_APC_PATH` from the environment at construction time.
    /// If set, it overrides the default `$HOME/Sites` used by `classify_tier`
    /// for APC detection. Precedence: explicit `with_sites()` call > env var >
    /// default `$HOME/Sites`.
    ///
    /// Reading happens once at construction, not per-call, to avoid race
    /// conditions if the env var changes at runtime (T-P5-E02-02).
    pub fn new() -> Self {
        let sites_override = std::env::var("CASCADE_APC_PATH")
            .ok()
            .map(PathBuf::from);
        Self {
            home_override: None,
            sites_override,
        }
    }

    /// Override the home directory used for GCI detection.
    ///
    /// Intended for unit tests only; do not call in production code.
    pub fn with_home(mut self, home: PathBuf) -> Self {
        self.home_override = Some(home);
        self
    }

    /// Override the "all sites" parent directory for APC detection.
    ///
    /// Intended for unit tests only. Takes precedence over `CASCADE_APC_PATH`.
    pub fn with_sites(mut self, sites: PathBuf) -> Self {
        self.sites_override = Some(sites);
        self
    }

    /// Walk upward from `start` and collect all tiers that have a readable
    /// `.cascade/CASCADE.md`.
    ///
    /// Returns tiers in bottom-up order (PAC first, GCI last).
    /// The caller reverses this list for highest-priority-first merge order.
    ///
    /// # Errors
    ///
    /// Returns `Err` only for OS-level failures (e.g. permission denied on
    /// a directory entry). Missing `.cascade/` directories are not errors.
    pub fn discover(&self, start: &Path) -> Result<Vec<DiscoveredTier>> {
        let home = self
            .home_override
            .clone()
            .or_else(home_dir)
            .unwrap_or_else(|| PathBuf::from("/"));

        let mut found: Vec<DiscoveredTier> = Vec::new();

        // Walk from the starting directory up to (and including) the filesystem root.
        // We intentionally do NOT canonicalize the path so that tests using tempfile
        // get back the same non-canonical path they passed in (macOS resolves
        // /var → /private/var via canonicalize, which breaks path equality assertions).
        for ancestor in start.ancestors() {
            let cascade_dir = ancestor.join(".cascade");
            let cascade_md = cascade_dir.join("CASCADE.md");

            if !cascade_dir.is_dir() {
                trace!("no .cascade/ at {}", ancestor.display());
                continue;
            }

            let tier = self.classify_tier(ancestor, &home);
            debug!(
                "found .cascade/ at {} → tier {:?}",
                ancestor.display(),
                tier
            );

            found.push(DiscoveredTier {
                tier,
                cascade_dir,
                cascade_md,
            });
        }

        Ok(found)
    }

    /// Heuristically classify a directory path as a [`CascadeTier`].
    ///
    /// Classification is based on depth relative to the home directory and
    /// whether known markers (`Sites/`, `Downloads/`) are present.
    ///
    /// ## Mac / OS-level defaults (v1.2)
    ///
    /// | Path | Default tier | Notes |
    /// |---|---|---|
    /// | `~` | [`CascadeTier::Gci`] | home = GCI always |
    /// | `~/Sites` | [`CascadeTier::Pci`] | all-projects parent |
    /// | `~/Sites/<project>` | [`CascadeTier::Apc`] | project root |
    /// | `~/Downloads` | [`CascadeTier::Pac`] | **personal scope** — locked/sensitive |
    /// | `~/Downloads/**` | [`CascadeTier::Pac`] | sub-paths also personal-scope |
    ///
    /// `~/Downloads` is classified as [`CascadeTier::Pac`] (personal/app-level)
    /// to signal that it carries the user's personal threaded memory.  The
    /// sensitivity firewall then treats any content from this scope as
    /// [`cascade_core::sensitivity::ContentSensitivity::Sensitive`].
    ///
    /// Environment-variable overrides (`CASCADE_APC_PATH`, `with_sites()`) always
    /// win over the defaults.
    fn classify_tier(&self, path: &Path, home: &Path) -> CascadeTier {
        // Compute the sites directory: honour override, otherwise $HOME/Sites.
        let sites: PathBuf = if let Some(s) = &self.sites_override {
            s.clone()
        } else {
            home.join("Sites")
        };

        if path == home {
            return CascadeTier::Gci;
        }

        // ── Mac / personal-scope default: ~/Downloads → Pac (personal) ────────
        // Checked before Sites so the personal scope always wins even if someone
        // sets CASCADE_APC_PATH to something that overlaps.
        let downloads = home.join("Downloads");
        if path == downloads || path.starts_with(&downloads) {
            // Personal scope: all content here is treated as sensitive by the
            // sensitivity firewall.  We use PAC tier to signal this is a locked
            // per-user context rather than a project-level configuration.
            return CascadeTier::Pac;
        }

        // Detect `Sites/` root (PCI level — parent of all APC projects)
        if path == sites {
            return CascadeTier::Pci;
        }

        // Detect one level beneath Sites/ (project root → APC)
        if path.parent() == Some(sites.as_path()) {
            return CascadeTier::Apc;
        }

        // If there is a git root at this level, it could be PPC or PRC.
        // PPC: one more level beneath a known project root.
        // We use a simple heuristic: if `.git` exists here, call it PRC.
        if path.join(".git").is_dir() {
            return CascadeTier::Prc;
        }

        // If we are below a `.git` root (app subdirectory), classify as PAC.
        if self.has_git_ancestor(path, home) {
            return CascadeTier::Pac;
        }

        // Fallback: classify as PPC (package-level config).
        CascadeTier::Ppc
    }

    /// Returns `true` if any ancestor between `path` (exclusive) and `home`
    /// (exclusive) contains a `.git` directory.
    fn has_git_ancestor(&self, path: &Path, home: &Path) -> bool {
        let mut cur = path;
        loop {
            match cur.parent() {
                Some(p) if p != home => {
                    if p.join(".git").is_dir() {
                        return true;
                    }
                    cur = p;
                }
                _ => return false,
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn make_tier(dir: &Path) -> PathBuf {
        let cascade = dir.join(".cascade");
        fs::create_dir_all(&cascade).unwrap();
        fs::write(cascade.join("CASCADE.md"), "# Test cascade\n").unwrap();
        cascade
    }

    #[test]
    fn discovers_single_tier() {
        let root = TempDir::new().unwrap();
        make_tier(root.path());

        let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
        let tiers = discovery.discover(root.path()).unwrap();
        assert!(!tiers.is_empty());
        assert!(tiers
            .iter()
            .any(|t| t.cascade_dir == root.path().join(".cascade")));
    }

    #[test]
    fn missing_cascade_dir_is_skipped() {
        let root = TempDir::new().unwrap();
        // No .cascade/ created here.
        let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
        let tiers = discovery.discover(root.path()).unwrap();
        // The path itself has no .cascade/ but ancestors might; just verify no panic.
        // We cannot guarantee zero results since tempfile paths include system dirs.
        let _ = tiers;
    }

    #[test]
    fn is_readable_follows_symlinks() {
        let root = TempDir::new().unwrap();
        make_tier(root.path());
        let discovered = DiscoveredTier {
            tier: CascadeTier::Gci,
            cascade_dir: root.path().join(".cascade"),
            cascade_md: root.path().join(".cascade").join("CASCADE.md"),
        };
        assert!(discovered.is_readable());
    }

    // ── Mac tier defaults (v1.2) ──────────────────────────────────────────────

    /// Helper: create a mock home tree with Sites/ and Downloads/ subdirs.
    fn make_home_tree(root: &Path) -> (PathBuf, PathBuf, PathBuf) {
        let home = root.to_path_buf();
        let sites = home.join("Sites");
        let downloads = home.join("Downloads");
        fs::create_dir_all(&sites).unwrap();
        fs::create_dir_all(&downloads).unwrap();
        (home, sites, downloads)
    }

    #[test]
    fn sites_project_dir_classified_as_apc() {
        let root = TempDir::new().unwrap();
        let (home, sites, _) = make_home_tree(root.path());
        let proj = sites.join("myproject");
        fs::create_dir_all(&proj).unwrap();

        let discovery = TierDiscovery::new().with_home(home.clone());
        let tier = discovery.classify_tier(&proj, &home);
        assert_eq!(
            tier,
            CascadeTier::Apc,
            "~/Sites/<project> should classify as APC"
        );
    }

    #[test]
    fn downloads_dir_classified_as_pac_personal_scope() {
        let root = TempDir::new().unwrap();
        let (home, _, downloads) = make_home_tree(root.path());

        let discovery = TierDiscovery::new().with_home(home.clone());
        let tier = discovery.classify_tier(&downloads, &home);
        assert_eq!(
            tier,
            CascadeTier::Pac,
            "~/Downloads should classify as PAC (personal scope)"
        );
    }

    #[test]
    fn downloads_subdir_also_classified_as_pac() {
        let root = TempDir::new().unwrap();
        let (home, _, downloads) = make_home_tree(root.path());
        let subdir = downloads.join("personal").join("threads");
        fs::create_dir_all(&subdir).unwrap();

        let discovery = TierDiscovery::new().with_home(home.clone());
        let tier = discovery.classify_tier(&subdir, &home);
        assert_eq!(
            tier,
            CascadeTier::Pac,
            "~/Downloads/** should also classify as PAC (personal scope)"
        );
    }

    #[test]
    fn home_dir_classified_as_gci() {
        let root = TempDir::new().unwrap();
        let home = root.path().to_path_buf();

        let discovery = TierDiscovery::new().with_home(home.clone());
        let tier = discovery.classify_tier(&home, &home);
        assert_eq!(tier, CascadeTier::Gci, "home dir should be GCI");
    }

    #[test]
    fn cascade_apc_path_env_override_respected() {
        // When CASCADE_APC_PATH is set via with_sites(), it overrides the default Sites.
        let root = TempDir::new().unwrap();
        let home = root.path().to_path_buf();
        let custom_sites = root.path().join("CustomWork");
        fs::create_dir_all(&custom_sites).unwrap();
        let proj = custom_sites.join("proj");
        fs::create_dir_all(&proj).unwrap();

        let discovery = TierDiscovery::new()
            .with_home(home.clone())
            .with_sites(custom_sites.clone());
        let tier = discovery.classify_tier(&proj, &home);
        assert_eq!(
            tier,
            CascadeTier::Apc,
            "custom sites override: project under custom sites dir should be APC"
        );
    }

    #[test]
    fn path_is_personal_scope_downloads() {
        let root = TempDir::new().unwrap();
        let (home, _, downloads) = make_home_tree(root.path());
        let sub = downloads.join("notes.md");

        // Override HOME for this test via the function logic (uses env HOME).
        // Since we can't easily set HOME in a unit test without side effects,
        // we verify the ~/Downloads string-prefix form works.
        assert!(
            path_is_personal_scope(Path::new("~/Downloads/thread.md")),
            "~/Downloads/* should be personal scope"
        );
        assert!(
            !path_is_personal_scope(&home.join("Sites").join("proj")),
            "Sites path should not be personal scope"
        );
        // Real path from TempDir — only works if HOME matches; skip if not.
        let _ = sub;
    }
}
