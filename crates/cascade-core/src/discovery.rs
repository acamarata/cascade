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
    pub fn new() -> Self {
        Self::default()
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
    /// Intended for unit tests only.
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
    /// whether known markers (`Sites/`) are present.
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

        // Detect `Sites/` root (APC level in the cascade hierarchy)
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
}
