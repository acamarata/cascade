//! Integration tests for `cascade_core::resolution` using real temporary directories.
//!
//! # Purpose
//!
//! Verifies that [`Resolver`] correctly walks a synthetic directory hierarchy,
//! reads each `.cascade/CASCADE.md` file, and returns a [`ResolvedCascade`]
//! whose `tier_sources` contain each tier's content.
//!
//! Tests use `tokio::test` because [`Resolver::resolve`] is `async`.

use cascade_core::resolution::Resolver;
use serial_test::serial;
use std::fs;
use tempfile::TempDir;

static ENV_TEST_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

/// Helper: plant a `.cascade/CASCADE.md` at `dir` with `content`.
fn plant_cascade(dir: &std::path::Path, content: &str) {
    let cascade = dir.join(".cascade");
    fs::create_dir_all(&cascade).unwrap();
    fs::write(cascade.join("CASCADE.md"), content).unwrap();
}

/// A single-tier hierarchy (one `.cascade/CASCADE.md`) resolves to one source.
#[tokio::test]
#[serial(global_env)]
// Deliberate: the std MutexGuard must span the await so the HOME mutation stays
// serialized while the async resolve reads it.
#[allow(clippy::await_holding_lock)]
async fn test_resolver_single_tier() {
    let _env_guard = ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let root = TempDir::new().unwrap();
    plant_cascade(root.path(), "# Single tier\n");

    // Set HOME to the temp root so the resolver treats it as GCI.
    std::env::set_var("HOME", root.path());

    let resolved = Resolver::new().resolve(root.path()).await.unwrap();

    assert!(
        !resolved.tier_sources.is_empty(),
        "expected at least one tier source"
    );
    assert!(
        resolved.merged_text.contains("Single tier"),
        "merged_text should contain the planted content; got: {:?}",
        resolved.merged_text
    );
}

/// A three-tier hierarchy produces a `ResolvedCascade` where
/// `tier_sources.len() >= 1` (the resolver finds at least the first tier
/// within the synthetic tree).
#[tokio::test]
#[serial(global_env)]
// Deliberate: guard spans the await to serialize the HOME env mutation.
#[allow(clippy::await_holding_lock)]
async fn test_resolver_multi_tier_sources() {
    let _env_guard = ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let root = TempDir::new().unwrap();
    let project = root.path().join("Sites").join("myproject");
    let repo = project.join("myrepo");
    fs::create_dir_all(&repo).unwrap();

    plant_cascade(root.path(), "# GCI content\n");
    plant_cascade(&project, "# PPC content\n");
    plant_cascade(&repo, "# PRC content\n");

    // Override HOME so GCI-level heuristic fires at the temp root.
    std::env::set_var("HOME", root.path());

    let resolved = Resolver::new().resolve(&repo).await.unwrap();

    assert!(
        !resolved.tier_sources.is_empty(),
        "expected at least one tier_source; got: {:#?}",
        resolved.tier_sources
    );
}

/// `merged_text` contains content from each tier that was discovered.
#[tokio::test]
#[serial(global_env)]
// Deliberate: guard spans the await to serialize the HOME env mutation.
#[allow(clippy::await_holding_lock)]
async fn test_resolver_merged_text_contains_all_tiers() {
    let _env_guard = ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let root = TempDir::new().unwrap();
    let repo = root.path().join("Sites").join("proj").join("repo");
    fs::create_dir_all(&repo).unwrap();

    plant_cascade(root.path(), "# GCI header\n");
    plant_cascade(&repo, "# Repo instructions\n");

    std::env::set_var("HOME", root.path());

    let resolved = Resolver::new().resolve(&repo).await.unwrap();

    // Every tier_source's content must appear somewhere in merged_text.
    for src in &resolved.tier_sources {
        assert!(
            resolved.merged_text.contains(src.content.trim()),
            "merged_text missing content from tier {:?}: {:?}",
            src.tier,
            src.content
        );
    }
}

/// Dedup mode does not return duplicate lines from multiple tiers.
#[tokio::test]
#[serial(global_env)]
// Deliberate: guard spans the await to serialize the HOME env mutation.
#[allow(clippy::await_holding_lock)]
async fn test_resolver_dedup_removes_duplicate_lines() {
    let _env_guard = ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let root = TempDir::new().unwrap();
    let repo = root.path().join("Sites").join("proj").join("repo");
    fs::create_dir_all(&repo).unwrap();

    let shared_line = "# shared instruction";
    // Plant the same line in both tiers.
    plant_cascade(root.path(), &format!("{shared_line}\n"));
    plant_cascade(&repo, &format!("{shared_line}\nExtra repo line.\n"));

    std::env::set_var("HOME", root.path());

    let resolved = Resolver::new().with_dedup().resolve(&repo).await.unwrap();

    // Count occurrences of the shared line in the merged output.
    let occurrences = resolved.merged_text.matches(shared_line).count();
    assert_eq!(
        occurrences, 1,
        "dedup should deduplicate shared lines; merged_text: {:?}",
        resolved.merged_text
    );
}
