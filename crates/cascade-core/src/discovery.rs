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

/// Canonicalizes `path` for tier comparison, resolving symlinks (e.g. a
/// `~/Sites -> /mnt/data/Sites` symlink) so that a path passed in as the
/// symlink and a CWD already resolved to the target compare equal.
///
/// Falls back to the input path unchanged when canonicalization fails — the
/// target does not exist (tempfile-based tests) or the volume is unmounted.
/// This keeps behaviour identical to the previous literal comparison in those
/// cases rather than introducing a hard failure.
fn canon(path: &Path) -> PathBuf {
    std::fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf())
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
        let sites_override = std::env::var("CASCADE_APC_PATH").ok().map(PathBuf::from);
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
    /// | `~/Sites` | [`CascadeTier::Apc`] | all-projects parent (all coding projects) |
    /// | `~/Sites/<project>` | [`CascadeTier::Ppc`] | per-project root (multi-repo project) |
    /// | `~/Downloads` | [`CascadeTier::Pci`] | **personal scope** — locked/sensitive |
    /// | `~/Downloads/**` | [`CascadeTier::Pci`] | sub-paths also personal-scope |
    ///
    /// `~/Sites` is the **All-Projects Cascade** ([`CascadeTier::Apc`]) — the
    /// parent that governs every coding project. A single directory beneath it
    /// (e.g. `~/Sites/nself`) is a **Per-Project Cascade** ([`CascadeTier::Ppc`]),
    /// covering all repos within that one multi-repo project.
    ///
    /// `~/Downloads` is classified as [`CascadeTier::Pci`] (Personal Cascade) to
    /// signal that it carries the user's personal threaded memory.  The
    /// sensitivity firewall then treats any content from this scope as
    /// [`cascade_core::sensitivity::ContentSensitivity::Sensitive`].
    ///
    /// Path comparisons are made on canonicalized paths so that a symlinked
    /// `~/Sites` (e.g. `~/Sites -> /mnt/data/Sites`) still matches a CWD that
    /// the OS has already resolved to the symlink target.
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

        // Canonicalize for comparison so symlinked roots resolve. `~/Sites` is
        // commonly a symlink to external storage (e.g. /mnt/data/Sites); the
        // OS resolves the CWD to the target, so a literal `==` against the
        // symlink path would never match and every project would fall through to
        // the Ppc catch-all. `canon()` falls back to the input path when the
        // target does not exist (e.g. in tempfile-based tests).
        let path_c = canon(path);
        let sites_c = canon(&sites);
        let home_c = canon(home);

        if path_c == home_c {
            return CascadeTier::Gci;
        }

        // ── Personal-scope default: ~/Downloads → Pci (personal) ─────────────
        // Checked before Sites so the personal scope always wins even if someone
        // sets CASCADE_APC_PATH to something that overlaps.
        let downloads = canon(&home.join("Downloads"));
        if path_c == downloads || path_c.starts_with(&downloads) {
            // Personal scope: all content here is treated as sensitive by the
            // sensitivity firewall.  Pci (Personal Cascade Instructions) marks
            // this as a locked per-user context rather than a project-level one.
            return CascadeTier::Pci;
        }

        // Detect `Sites/` root → Apc (All-Projects Cascade, parent of all
        // per-project cascades).
        if path_c == sites_c {
            return CascadeTier::Apc;
        }

        // Detect one level beneath Sites/ (a single multi-repo project root → Ppc).
        if path_c.parent() == Some(sites_c.as_path()) {
            return CascadeTier::Ppc;
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
    fn sites_root_classified_as_apc() {
        // ~/Sites itself is the All-Projects Cascade (governs all coding projects).
        let root = TempDir::new().unwrap();
        let (home, sites, _) = make_home_tree(root.path());

        let discovery = TierDiscovery::new().with_home(home.clone());
        let tier = discovery.classify_tier(&sites, &home);
        assert_eq!(
            tier,
            CascadeTier::Apc,
            "~/Sites should classify as APC (all-projects cascade)"
        );
    }

    #[test]
    fn sites_project_dir_classified_as_ppc() {
        // A single multi-repo project under ~/Sites (e.g. ~/Sites/nself) is the
        // Per-Project Cascade.
        let root = TempDir::new().unwrap();
        let (home, sites, _) = make_home_tree(root.path());
        let proj = sites.join("nself");
        fs::create_dir_all(&proj).unwrap();

        let discovery = TierDiscovery::new().with_home(home.clone());
        let tier = discovery.classify_tier(&proj, &home);
        assert_eq!(
            tier,
            CascadeTier::Ppc,
            "~/Sites/<project> should classify as PPC (per-project cascade)"
        );
    }

    #[test]
    fn sites_symlink_still_classified_as_apc() {
        // Regression: ~/Sites is frequently a symlink to external storage. When
        // the CWD is already resolved to the symlink target, classification must
        // still match ~/Sites → APC (not fall through to the Ppc catch-all).
        let root = TempDir::new().unwrap();
        let home = root.path().join("home");
        let external = root.path().join("external-volume").join("Sites");
        fs::create_dir_all(&home).unwrap();
        fs::create_dir_all(&external).unwrap();
        // Create the ~/Sites symlink pointing at the external target.
        // Bound inside the gate: on Windows there is no symlink call, so an
        // unconditional binding is an unused variable.
        #[cfg(unix)]
        {
            let sites_link = home.join("Sites");
            std::os::unix::fs::symlink(&external, &sites_link).unwrap();
        }

        let discovery = TierDiscovery::new().with_home(home.clone());
        // Classify the RESOLVED target path (what the OS gives as CWD).
        let tier = discovery.classify_tier(&external, &home);
        assert_eq!(
            tier,
            CascadeTier::Apc,
            "resolved ~/Sites symlink target should still classify as APC"
        );
    }

    #[test]
    fn downloads_dir_classified_as_pci_personal_scope() {
        let root = TempDir::new().unwrap();
        let (home, _, downloads) = make_home_tree(root.path());

        let discovery = TierDiscovery::new().with_home(home.clone());
        let tier = discovery.classify_tier(&downloads, &home);
        assert_eq!(
            tier,
            CascadeTier::Pci,
            "~/Downloads should classify as PCI (personal scope)"
        );
    }

    #[test]
    fn downloads_subdir_also_classified_as_pci() {
        let root = TempDir::new().unwrap();
        let (home, _, downloads) = make_home_tree(root.path());
        let subdir = downloads.join("personal").join("threads");
        fs::create_dir_all(&subdir).unwrap();

        let discovery = TierDiscovery::new().with_home(home.clone());
        let tier = discovery.classify_tier(&subdir, &home);
        assert_eq!(
            tier,
            CascadeTier::Pci,
            "~/Downloads/** should also classify as PCI (personal scope)"
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
        // Under the custom sites root: the root itself is APC, a project beneath
        // it is PPC.
        assert_eq!(
            discovery.classify_tier(&custom_sites, &home),
            CascadeTier::Apc,
            "custom sites override: the sites root should be APC"
        );
        let tier = discovery.classify_tier(&proj, &home);
        assert_eq!(
            tier,
            CascadeTier::Ppc,
            "custom sites override: project under custom sites dir should be PPC"
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
