//! # cascade_core::maps::cascade_tiers
//!
//! Resolves the six-tier cascade chain for a given project root and returns
//! one [`TierEntry`] per tier, indicating whether the tier file exists.
//!
//! ## Tier resolution order (highest priority first)
//!
//! | Tier | Abbr | Standard path |
//! |------|------|---------------|
//! | Global Config Instructions  | GCI | `$HOME/.cascade/CASCADE.md` |
//! | Project Config Instructions | PCI | `$HOME/Sites/.cascade/CASCADE.md` |
//! | App-level Config            | APC | `$HOME/Sites/{project}/.cascade/CASCADE.md` (= given root's parent if depth=project) |
//! | Package-level Config        | PPC | `{root}/.cascade/CASCADE.md` |
//! | Repo Config                 | PRC | `{root}/../.cascade/CASCADE.md` (one level up from root inside a project dir) |
//! | Per-App Config              | PAC | `{root}/apps/**/.cascade/CASCADE.md` (first match) |
//!
//! In practice `get_cascade_tier_tree(root)` treats `root` as the project/repo
//! directory passed in by the frontend. The six tiers are computed relative to
//! that root and to `$HOME`.
//!
//! ## SPORT
//! MASTER-COMPONENTS.md — cascade tier tree provider

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use tracing::trace;

/// Returns the home directory via `$HOME` (Unix) or `USERPROFILE` (Windows).
fn home_dir() -> Option<PathBuf> {
    std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .ok()
        .map(PathBuf::from)
}

/// One tier entry in the resolved cascade chain.
///
/// # Purpose
/// Describes whether a given tier's CASCADE.md exists at its canonical path.
/// The frontend uses this to render the "Cascade Tier Tree" view.
///
/// # Constraints
/// Fields are camelCase so TypeScript needs no mapping layer.
/// `exists` reflects the real filesystem at call time (not cached).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct TierEntry {
    /// Tier abbreviation: "GCI" | "PCI" | "APC" | "PPC" | "PRC" | "PAC".
    pub tier: String,
    /// Human-readable tier name.
    pub name: String,
    /// Absolute path to the expected `.cascade/CASCADE.md` file.
    pub path: String,
    /// Whether the file exists and is a regular file (symlinks followed).
    pub exists: bool,
}

impl TierEntry {
    fn new(tier: &str, name: &str, path: PathBuf) -> Self {
        let exists = path.is_file();
        TierEntry {
            tier: tier.to_string(),
            name: name.to_string(),
            path: path.to_string_lossy().to_string(),
            exists,
        }
    }
}

/// Resolve the six-tier cascade chain for the given project root.
///
/// # Purpose
/// Returns a list of [`TierEntry`] values in resolution order (GCI first)
/// so the Maps panel can render which tiers are populated for a project.
///
/// # Inputs
/// - `root`: Absolute path to the project/repo directory to inspect.
///   Typically the directory containing `.cascade/`.
///
/// # Outputs
/// `Vec<TierEntry>` with exactly six entries — one per tier, in GCI→PAC order.
///
/// # Constraints
/// - Uses `$HOME` to compute absolute tier paths (HOME-confined by construction).
/// - `exists` is evaluated at call time; stale results possible if files change.
/// - Returns six entries even when `home_dir()` fails — tier paths will be empty
///   strings and `exists` will be false.
pub fn get_cascade_tier_tree(root: &str) -> Vec<TierEntry> {
    let root_path = PathBuf::from(root);
    let home = home_dir().unwrap_or_else(|| PathBuf::from("/nonexistent"));

    // GCI — ~/.cascade/CASCADE.md
    let gci = TierEntry::new(
        "GCI",
        "Global Config Instructions",
        home.join(".cascade").join("CASCADE.md"),
    );

    // PCI — ~/Sites/.cascade/CASCADE.md
    let pci = TierEntry::new(
        "PCI",
        "Project Config Instructions",
        home.join("Sites").join(".cascade").join("CASCADE.md"),
    );

    // APC — root's parent .cascade (the sites-project tier above the repo)
    let apc_parent = root_path
        .parent()
        .map(|p| p.to_path_buf())
        .unwrap_or_else(|| root_path.clone());
    let apc = TierEntry::new(
        "APC",
        "App-level Config",
        apc_parent.join(".cascade").join("CASCADE.md"),
    );

    // PPC — root/.cascade/CASCADE.md  (the project/package root itself)
    let ppc = TierEntry::new(
        "PPC",
        "Package-level Config",
        root_path.join(".cascade").join("CASCADE.md"),
    );

    // PRC — one level up from root's parent (grandparent) .cascade
    // Represents the "repo config" when root is an app inside a repo.
    let prc_base = apc_parent
        .parent()
        .map(|p| p.to_path_buf())
        .unwrap_or_else(|| apc_parent.clone());
    let prc = TierEntry::new(
        "PRC",
        "Repo Config",
        prc_base.join(".cascade").join("CASCADE.md"),
    );

    // PAC — first app subdirectory under root that contains .cascade/CASCADE.md
    let pac_path = find_first_app_cascade(&root_path);
    let pac = TierEntry::new(
        "PAC",
        "Per-App Config",
        pac_path.unwrap_or_else(|| root_path.join("apps").join(".cascade").join("CASCADE.md")),
    );

    trace!(
        "get_cascade_tier_tree({}): GCI={}, PCI={}, APC={}, PPC={}, PRC={}, PAC={}",
        root,
        gci.exists,
        pci.exists,
        apc.exists,
        ppc.exists,
        prc.exists,
        pac.exists,
    );

    vec![gci, pci, apc, ppc, prc, pac]
}

/// Scan for the first `apps/<name>/.cascade/CASCADE.md` under `root`.
fn find_first_app_cascade(root: &Path) -> Option<PathBuf> {
    let apps_dir = root.join("apps");
    let entries = std::fs::read_dir(&apps_dir).ok()?;
    for entry in entries.flatten() {
        let candidate = entry.path().join(".cascade").join("CASCADE.md");
        if candidate.is_file() {
            return Some(candidate);
        }
    }
    None
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    /// Build a fixture simulating a complete six-tier tree:
    /// ```
    /// tmp/                            ← HOME
    ///   .cascade/CASCADE.md           ← GCI
    ///   Sites/
    ///     .cascade/CASCADE.md         ← PCI
    ///     myproject/
    ///       .cascade/CASCADE.md       ← APC
    ///       myrepo/
    ///         .cascade/CASCADE.md     ← PPC
    ///         apps/
    ///           myapp/
    ///             .cascade/CASCADE.md ← PAC
    /// ```
    /// PRC is the grandparent of myrepo, which is `Sites/`, already covered.
    fn build_six_tier_fixture(home: &Path) {
        let files = [
            home.join(".cascade").join("CASCADE.md"),
            home.join("Sites").join(".cascade").join("CASCADE.md"),
            home.join("Sites")
                .join("myproject")
                .join(".cascade")
                .join("CASCADE.md"),
            home.join("Sites")
                .join("myproject")
                .join("myrepo")
                .join(".cascade")
                .join("CASCADE.md"),
            home.join("Sites")
                .join("myproject")
                .join("myrepo")
                .join("apps")
                .join("myapp")
                .join(".cascade")
                .join("CASCADE.md"),
        ];
        for f in &files {
            fs::create_dir_all(f.parent().unwrap()).unwrap();
            fs::write(f, "# cascade").unwrap();
        }
    }

    #[test]
    #[serial(global_env)]
    fn tier_tree_returns_six_entries() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());
        build_six_tier_fixture(tmp.path());

        let root = tmp.path().join("Sites").join("myproject").join("myrepo");
        let entries = get_cascade_tier_tree(root.to_str().unwrap());

        std::env::remove_var("HOME");

        assert_eq!(entries.len(), 6, "must have exactly 6 tier entries");

        let tiers: Vec<&str> = entries.iter().map(|e| e.tier.as_str()).collect();
        assert_eq!(tiers, ["GCI", "PCI", "APC", "PPC", "PRC", "PAC"]);
    }

    #[test]
    #[serial(global_env)]
    fn tier_tree_gci_ppc_exist() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());
        build_six_tier_fixture(tmp.path());

        let root = tmp.path().join("Sites").join("myproject").join("myrepo");
        let entries = get_cascade_tier_tree(root.to_str().unwrap());

        std::env::remove_var("HOME");

        let by_tier = |tier: &str| -> bool {
            entries
                .iter()
                .find(|e| e.tier == tier)
                .map(|e| e.exists)
                .unwrap_or(false)
        };

        assert!(by_tier("GCI"), "GCI should exist");
        assert!(by_tier("PCI"), "PCI should exist");
        assert!(by_tier("PPC"), "PPC should exist");
        assert!(by_tier("PAC"), "PAC should exist");
    }

    #[test]
    #[serial(global_env)]
    fn tier_tree_missing_tiers_show_exists_false() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());
        // Only create PPC tier — everything else missing.
        let root = tmp.path().join("myproject");
        fs::create_dir_all(root.join(".cascade")).unwrap();
        fs::write(root.join(".cascade").join("CASCADE.md"), "# ppc").unwrap();

        let entries = get_cascade_tier_tree(root.to_str().unwrap());

        std::env::remove_var("HOME");

        let gci = entries.iter().find(|e| e.tier == "GCI").unwrap();
        assert!(!gci.exists, "GCI should not exist in this fixture");

        let ppc = entries.iter().find(|e| e.tier == "PPC").unwrap();
        assert!(ppc.exists, "PPC should exist");
    }
}
