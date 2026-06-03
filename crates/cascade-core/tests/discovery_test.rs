//! Integration tests for `cascade_core::discovery`.
//!
//! These tests create real temporary directories to verify the upward-walk
//! algorithm without mocking the filesystem. Each test is self-contained and
//! cleans up via `TempDir::drop`.

use cascade_core::discovery::{DiscoveredTier, TierDiscovery};
use cascade_types::cascade_tier::CascadeTier;
use std::fs;
use tempfile::TempDir;

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Create a `.cascade/CASCADE.md` under `dir`, returning the cascade dir path.
fn plant_cascade(dir: &std::path::Path, content: &str) -> std::path::PathBuf {
    let cascade = dir.join(".cascade");
    fs::create_dir_all(&cascade).unwrap();
    fs::write(cascade.join("CASCADE.md"), content).unwrap();
    cascade
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[test]
fn no_cascade_dirs_returns_empty() {
    let root = TempDir::new().unwrap();
    // No .cascade/ anywhere beneath root.
    let child = root.path().join("a").join("b");
    fs::create_dir_all(&child).unwrap();

    let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
    let tiers = discovery.discover(&child).unwrap();

    // Only tiers under root.path() are relevant; no .cascade/ was planted.
    let relevant: Vec<&DiscoveredTier> = tiers
        .iter()
        .filter(|t| t.cascade_dir.starts_with(root.path()))
        .collect();
    assert!(
        relevant.is_empty(),
        "expected no tiers under root: {:?}",
        relevant
    );
}

#[test]
fn discovers_home_level_as_gci() {
    let root = TempDir::new().unwrap();
    plant_cascade(root.path(), "# GCI level\n");

    let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
    let tiers = discovery.discover(root.path()).unwrap();

    let gci = tiers.iter().find(|t| t.tier == CascadeTier::Gci);
    assert!(gci.is_some(), "expected GCI tier; got: {:?}", tiers);
    assert!(gci.unwrap().is_readable());
}

#[test]
fn multi_tier_tree_discovers_multiple_levels() {
    let root = TempDir::new().unwrap();
    let sites = root.path().join("Sites");
    let project = sites.join("myproject");
    let repo = project.join("myrepo");
    fs::create_dir_all(&repo).unwrap();

    // Plant .cascade/ at multiple levels.
    plant_cascade(root.path(), "# GCI\n");
    plant_cascade(&sites, "# PCI\n");
    plant_cascade(&project, "# APC\n");
    plant_cascade(&repo, "# PRC\n");

    let discovery = TierDiscovery::new()
        .with_home(root.path().to_path_buf())
        .with_sites(sites.clone());
    let tiers = discovery.discover(&repo).unwrap();

    // We should find at least 3 tiers within our temp tree.
    let under_root: Vec<&DiscoveredTier> = tiers
        .iter()
        .filter(|t| t.cascade_dir.starts_with(root.path()))
        .collect();
    assert!(
        under_root.len() >= 3,
        "expected ≥3 tiers under temp root; got: {:#?}",
        under_root
    );
}

#[test]
fn discovered_tier_cascade_md_path_is_correct() {
    let root = TempDir::new().unwrap();
    plant_cascade(root.path(), "# test\n");

    let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
    let tiers = discovery.discover(root.path()).unwrap();

    let gci = tiers.iter().find(|t| t.tier == CascadeTier::Gci).unwrap();
    assert_eq!(
        gci.cascade_md,
        root.path().join(".cascade").join("CASCADE.md")
    );
}

#[test]
fn is_readable_returns_false_for_deleted_file() {
    let root = TempDir::new().unwrap();
    plant_cascade(root.path(), "# ephemeral\n");

    let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
    let tiers = discovery.discover(root.path()).unwrap();
    let gci = tiers
        .iter()
        .find(|t| t.tier == CascadeTier::Gci)
        .unwrap()
        .clone();

    // Delete the file after discovery.
    fs::remove_file(&gci.cascade_md).unwrap();
    assert!(
        !gci.is_readable(),
        "is_readable should be false after deletion"
    );
}

#[test]
fn discovery_is_deterministic_on_repeated_calls() {
    let root = TempDir::new().unwrap();
    plant_cascade(root.path(), "# stable\n");

    let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
    let first = discovery.discover(root.path()).unwrap();
    let second = discovery.discover(root.path()).unwrap();

    let first_paths: Vec<_> = first.iter().map(|t| t.cascade_dir.clone()).collect();
    let second_paths: Vec<_> = second.iter().map(|t| t.cascade_dir.clone()).collect();
    assert_eq!(first_paths, second_paths, "discovery must be deterministic");
}
