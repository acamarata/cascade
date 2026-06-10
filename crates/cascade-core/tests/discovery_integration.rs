//! Integration tests for `cascade_core::discovery` using real temporary directories.
//!
//! # Purpose
//!
//! These tests verify that [`TierDiscovery`] correctly finds `.cascade/CASCADE.md`
//! files in a synthetic filesystem hierarchy without any mocking. The `with_home`
//! override pins discovery to the temp directory so real `$HOME` contents never
//! interfere with test results.
//!
//! Kept separate from `discovery_test.rs` (which lives in the same `tests/` dir) to
//! make it easy to run the integration-only suite via
//! `cargo nextest run --test discovery_integration`.

use cascade_core::discovery::TierDiscovery;
use std::fs;
use tempfile::TempDir;

/// Helper: plant a `.cascade/CASCADE.md` file under `dir` with `content`.
fn plant_cascade(dir: &std::path::Path, content: &str) {
    let cascade = dir.join(".cascade");
    fs::create_dir_all(&cascade).unwrap();
    fs::write(cascade.join("CASCADE.md"), content).unwrap();
}

/// Smoke test: a single `.cascade/CASCADE.md` at the home level is found as GCI.
#[test]
fn test_discovery_finds_cascade_md() {
    let dir = TempDir::new().unwrap();

    // Plant a GCI-level CASCADE.md at the temp root (used as $HOME override).
    plant_cascade(dir.path(), "# Test GCI\nHello.");

    let result = TierDiscovery::new()
        .with_home(dir.path().to_path_buf())
        .discover(dir.path())
        .unwrap();

    // with_home pins the GCI lookup to dir.path(), so at least one tier should
    // be discovered (the GCI tier planted above).
    assert!(
        !result.is_empty(),
        "should find at least one tier; got: {result:#?}"
    );
}

/// A nested directory with no `.cascade/` directories at the home level has
/// no GCI tier discoverable under the synthetic root.
#[test]
fn test_discovery_empty_tree_returns_no_tiers() {
    let root = TempDir::new().unwrap();
    let nested = root.path().join("a").join("b").join("c");
    fs::create_dir_all(&nested).unwrap();

    // with_home pins GCI to root.path(), which has no .cascade/ planted.
    let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
    let result = discovery.discover(&nested).unwrap();

    // Only tiers that would be discovered FROM our temp root — no .cascade/ planted,
    // so the GCI tier should be absent.
    let gci_found = result
        .iter()
        .any(|t| t.cascade_dir.starts_with(root.path()));

    assert!(
        !gci_found,
        "expected no GCI tier in empty temp root; got: {result:#?}"
    );
}

/// A two-level hierarchy (GCI at root + an intermediate level) discovers
/// at least the GCI tier.
#[test]
fn test_discovery_two_level_hierarchy() {
    let root = TempDir::new().unwrap();
    let repo = root.path().join("myproject").join("myrepo");
    fs::create_dir_all(&repo).unwrap();

    plant_cascade(root.path(), "# GCI\n");
    plant_cascade(&repo, "# PRC\n");

    let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
    let result = discovery.discover(&repo).unwrap();

    // At minimum the GCI tier (planted at root) must be discovered.
    assert!(
        !result.is_empty(),
        "expected at least one tier; got: {result:#?}"
    );
}

/// Discovered tiers are readable immediately after discovery (no stale paths).
#[test]
fn test_discovered_tier_is_readable() {
    let root = TempDir::new().unwrap();
    plant_cascade(root.path(), "# readable\n");

    let discovery = TierDiscovery::new().with_home(root.path().to_path_buf());
    let result = discovery.discover(root.path()).unwrap();

    assert!(
        !result.is_empty(),
        "need at least one tier to validate readability"
    );

    for tier in &result {
        assert!(
            tier.is_readable(),
            "discovered tier should be readable: {tier:#?}"
        );
    }
}
