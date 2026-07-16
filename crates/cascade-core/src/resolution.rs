//! Cascade tier resolution — merges discovered tiers in priority order.
//!
//! # Tier priority
//!
//! GCI (highest) > PCI > APC > PPC > PRC > PAC (lowest).
//! Higher-tier content prepends in the merged output.
//!
//! # Usage
//!
//! ```rust,no_run
//! use cascade_core::resolution::Resolver;
//! use std::path::Path;
//!
//! # async fn run() -> cascade_types::error::Result<()> {
//! let resolved = Resolver::new().resolve(Path::new(".")).await?;
//! println!("{}", resolved.merged_text);
//! # Ok(())
//! # }
//! ```

use std::path::{Path, PathBuf};

use cascade_types::{cascade_tier::CascadeTier, error::Result};
use serde::{Deserialize, Serialize};

use crate::security::validate_cascade_path;

/// A single tier that contributed to the resolved cascade.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct TierSource {
    /// The cascade tier level.
    pub tier: CascadeTier,
    /// Absolute path to the CASCADE.md that was read.
    pub path: PathBuf,
    /// Raw text content read from `path`.
    pub content: String,
}

/// The fully resolved cascade for a working directory.
///
/// Contains the merged instruction text and provenance metadata so callers
/// can audit which tiers contributed to the output.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResolvedCascade {
    /// Merged text with higher-tier content first.
    pub merged_text: String,
    /// Ordered list of tiers that were found and read (highest tier first).
    pub tier_sources: Vec<TierSource>,
    /// Tiers that were searched but had no `.cascade/CASCADE.md` present.
    pub missing_tiers: Vec<CascadeTier>,
    /// Working directory the resolution started from.
    pub cwd: PathBuf,
}

/// Resolver walks a directory tree upward, locates `.cascade/CASCADE.md`
/// files at each tier, and merges their contents.
#[derive(Debug, Default)]
pub struct Resolver {
    /// When true, deduplicate identical instruction lines across tiers.
    pub dedup: bool,
}

impl Resolver {
    /// Create a new resolver with default options.
    pub fn new() -> Self {
        Self::default()
    }

    /// Build a resolver that deduplicates instruction lines.
    pub fn with_dedup(mut self) -> Self {
        self.dedup = true;
        self
    }

    /// Resolve the cascade for the given working directory.
    ///
    /// Walks `cwd` upward through the filesystem, locating `.cascade/CASCADE.md`
    /// at each recognised tier level. Returns a [`ResolvedCascade`] with the
    /// merged text and full provenance.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::Io`] if a discovered file cannot be read. Missing
    /// tiers are silently recorded in `missing_tiers` and do not cause errors.
    pub async fn resolve(&self, cwd: &Path) -> Result<ResolvedCascade> {
        use cascade_types::error::CascadeError;

        // Guard: reject null bytes and path traversal in the caller-supplied cwd.
        // Absolute paths are permitted here — cwd is always a filesystem absolute path.
        // The AbsolutePath check in validate_cascade_path applies to user-supplied
        // relative file arguments (e.g. CLI tier paths), not the working directory.
        // STRIDE: Tampering (T).
        let cwd_str = cwd.to_string_lossy();
        if cwd_str.contains('\0') {
            tracing::warn!(
                path = %cwd.display(),
                "null byte in cwd — aborting resolve"
            );
            return Err(CascadeError::Io {
                path: cwd.to_path_buf(),
                operation: "validate cwd null byte",
                source: std::io::Error::new(std::io::ErrorKind::InvalidInput, "null byte in path"),
            });
        }
        if cwd
            .components()
            .any(|c| c == std::path::Component::ParentDir)
        {
            tracing::warn!(
                path = %cwd.display(),
                "path traversal in cwd — aborting resolve"
            );
            return Err(CascadeError::Io {
                path: cwd.to_path_buf(),
                operation: "validate cwd traversal",
                source: std::io::Error::new(
                    std::io::ErrorKind::InvalidInput,
                    "path traversal detected",
                ),
            });
        }

        let cwd = cwd.canonicalize().map_err(|e| CascadeError::Io {
            path: cwd.to_path_buf(),
            operation: "canonicalize cwd",
            source: e,
        })?;

        let mut tier_sources: Vec<TierSource> = Vec::new();
        let mut missing_tiers: Vec<CascadeTier> = Vec::new();

        // Walk upward from CWD, collecting .cascade/CASCADE.md at each tier.
        let candidates = self.discover_tier_paths(&cwd);

        for (tier, path) in candidates {
            let cascade_file = path
                .join(cascade_types::paths::CASCADE_DIR_NAME)
                .join(cascade_types::paths::CASCADE_MD_NAME);

            // Follow symlinks: CLAUDE.md / AGENTS.md may point here. The only
            // legitimate pattern is a RELATIVE sibling link (e.g. CLAUDE.md ->
            // CASCADE.md in the same `.cascade/` dir). A symlink target is
            // attacker-influenced if an adversary controls a `.cascade/` dir in
            // the cwd ancestry, so:
            //   - absolute targets are rejected outright (an absolute link could
            //     point anywhere, e.g. `/etc/passwd`, and its contents would be
            //     read as instruction text) — the tier is skipped, not read;
            //   - relative targets are guarded against `..` traversal / null
            //     bytes before being resolved against the link's directory.
            // STRIDE: Tampering / Information disclosure.
            let resolved_path = if cascade_file.is_symlink() {
                let target =
                    std::fs::read_link(&cascade_file).unwrap_or_else(|_| cascade_file.clone());
                if target.is_absolute() {
                    tracing::warn!(
                        path = %target.display(),
                        "rejected absolute symlink target for CASCADE.md; skipping tier"
                    );
                    missing_tiers.push(tier);
                    continue;
                }
                if let Err(e) = validate_cascade_path(&target) {
                    tracing::warn!(
                        path = %target.display(),
                        error = %e,
                        "rejected unsafe relative symlink target; skipping tier"
                    );
                    missing_tiers.push(tier);
                    continue;
                }
                // Resolve the validated relative target against the link's directory.
                cascade_file
                    .parent()
                    .map(|dir| dir.join(&target))
                    .unwrap_or(target)
            } else {
                cascade_file.clone()
            };

            if resolved_path.exists() {
                match std::fs::read_to_string(&resolved_path) {
                    Ok(content) => {
                        tier_sources.push(TierSource {
                            tier,
                            path: resolved_path,
                            content,
                        });
                    }
                    Err(e) => {
                        return Err(CascadeError::Io {
                            path: resolved_path,
                            operation: "read tier CASCADE.md",
                            source: e,
                        });
                    }
                }
            } else {
                missing_tiers.push(tier);
            }
        }

        // Merge: higher tier (earlier in list) content first.
        let merged_text = self.merge(&tier_sources);

        Ok(ResolvedCascade {
            merged_text,
            tier_sources,
            missing_tiers,
            cwd,
        })
    }

    /// Discover the filesystem paths to check for each cascade tier.
    ///
    /// Returns an ordered list from highest-priority tier to lowest. Each entry
    /// is `(tier, directory_path)` where `directory_path/.cascade/CASCADE.md`
    /// is the expected location for that tier's instructions.
    fn discover_tier_paths(&self, cwd: &Path) -> Vec<(CascadeTier, PathBuf)> {
        let mut results: Vec<(CascadeTier, PathBuf)> = Vec::new();

        // GCI lives at $HOME. Canonicalize so the comparison below (which
        // excludes HOME from the intermediate walk) is robust against symlinks.
        let home = dirs_home().map(|h| std::fs::canonicalize(&h).unwrap_or(h));
        if let Some(ref h) = home {
            results.push((CascadeTier::Gci, h.clone()));
        }

        // PCI lives one level above project directories (e.g. ~/Sites/).
        // Heuristic: grandparent of a git repo root that contains project dirs.
        // For now, walk upward from CWD and record each ancestor that has a
        // .cascade/ directory; assign tiers in reverse-depth order.
        let mut ancestors: Vec<PathBuf> = cwd.ancestors().map(|p| p.to_path_buf()).collect();
        ancestors.reverse(); // root → cwd

        // Assign the intermediate tiers (PCI through PAC) to ancestor dirs
        // that contain a .cascade/ directory.
        let tier_sequence = [
            CascadeTier::Pci,
            CascadeTier::Apc,
            CascadeTier::Ppc,
            CascadeTier::Prc,
            CascadeTier::Pac,
        ];
        let mut tier_idx = 0;

        for ancestor in &ancestors {
            if tier_idx >= tier_sequence.len() {
                break;
            }
            // Skip $HOME itself: `~/.cascade/` is already claimed as the GCI
            // tier above. Without this guard it was ALSO assigned to the first
            // intermediate slot (PCI), so the GCI global file was loaded twice —
            // once as GCI, once as PCI — making every GCI block look like it was
            // duplicated into PCI. That produced phantom "Cross-tier duplication
            // (Pci → Gci)" findings for content that never existed in the real
            // personal tier, and shifted the true Downloads/Sites tiers down by
            // one slot (Downloads mislabelled APC, etc.).
            if home.as_deref() == Some(ancestor.as_path()) {
                continue;
            }
            if ancestor
                .join(cascade_types::paths::CASCADE_DIR_NAME)
                .is_dir()
            {
                results.push((tier_sequence[tier_idx], ancestor.clone()));
                tier_idx += 1;
            }
        }

        results
    }

    /// Merge tier sources into a single instruction string.
    fn merge(&self, sources: &[TierSource]) -> String {
        if self.dedup {
            let mut seen = std::collections::HashSet::new();
            sources
                .iter()
                .flat_map(|s| s.content.lines())
                .filter(|line| seen.insert(line.to_string()))
                .collect::<Vec<_>>()
                .join("\n")
        } else {
            sources
                .iter()
                .map(|s| s.content.as_str())
                .collect::<Vec<_>>()
                .join("\n\n---\n\n")
        }
    }
}

/// Returns the user home directory, or `None` if it cannot be determined.
fn dirs_home() -> Option<PathBuf> {
    std::env::var("HOME").ok().map(PathBuf::from)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn mk_cascade(dir: &Path) {
        let c = dir.join(".cascade");
        fs::create_dir_all(&c).unwrap();
        fs::write(c.join("CASCADE.md"), "# tier\n").unwrap();
    }

    /// Regression (Bug 3): `$HOME` must be assigned ONLY the GCI tier. It must
    /// NOT also be consumed as the first intermediate (PCI) slot — otherwise the
    /// GCI global file is loaded twice and every GCI block looks duplicated into
    /// PCI, producing phantom "Cross-tier duplication (Pci → Gci)" findings.
    #[test]
    #[serial_test::serial(global_env)]
    fn home_is_gci_only_not_also_pci() {
        let tmp = TempDir::new().unwrap();
        // Canonicalize so it matches the ancestor walk (which canonicalizes cwd).
        let home = std::fs::canonicalize(tmp.path()).unwrap();
        let downloads = home.join("Downloads");
        let proj = downloads.join("proj");
        fs::create_dir_all(&proj).unwrap();
        mk_cascade(&home); // ~/.cascade      → GCI
        mk_cascade(&downloads); // ~/Downloads/.cascade → PCI (personal)
        mk_cascade(&proj); // project             → APC slot (next)

        let prev = std::env::var("HOME").ok();
        std::env::set_var("HOME", &home);

        let resolver = Resolver::new();
        let candidates = resolver.discover_tier_paths(&proj);

        match prev {
            Some(v) => std::env::set_var("HOME", v),
            None => std::env::remove_var("HOME"),
        }

        // HOME appears exactly once, and only as GCI.
        let home_entries: Vec<_> = candidates.iter().filter(|(_, p)| *p == home).collect();
        assert_eq!(
            home_entries.len(),
            1,
            "HOME must appear exactly once (as GCI), got: {candidates:#?}"
        );
        assert_eq!(
            home_entries[0].0,
            CascadeTier::Gci,
            "HOME must be classified GCI only"
        );

        // The real personal dir (~/Downloads) takes the PCI slot, not HOME.
        let pci = candidates.iter().find(|(t, _)| *t == CascadeTier::Pci);
        assert_eq!(
            pci.map(|(_, p)| p.clone()),
            Some(downloads.clone()),
            "PCI slot must be ~/Downloads, not ~/ (the GCI global)"
        );
        // No intermediate tier may point back at HOME.
        assert!(
            !candidates
                .iter()
                .any(|(t, p)| *t != CascadeTier::Gci && *p == home),
            "no intermediate tier may reuse the HOME dir: {candidates:#?}"
        );
    }
}
