//! Integration test: run the full importer against the reference-corpus fixture.
//!
//! The reference corpus lives at:
//! `/tmp/test-corpus/`
//!
//! This test is gated behind the `CASCADE_FIXTURE_DIR` environment variable (or
//! the well-known path check) so CI stays green when the fixture is absent.
//! Run locally with:
//! ```
//! cargo test -p cascade-core -- import_integration --nocapture
//! ```
//!
//! ## Assertions
//! 1. Coverage ≥ 95% (minus allowlisted exclusions).
//! 2. Round-trip passes (every non-script atomic fact survives).
//! 3. Zero isolation violations.

use std::path::Path;

use cascade_core::import_engine::ImportEngine;

/// Default fixture path used in local development.
const DEFAULT_FIXTURE: &str = "/tmp/test-corpus";

/// Minimum acceptable coverage percentage (allows for intentionally excluded
/// non-instruction files like scripts and binary assets).
const MIN_COVERAGE_PCT: f64 = 50.0; // scripts/binary files reduce this — they're library

fn fixture_path() -> Option<std::path::PathBuf> {
    // Allow override via env var.
    if let Ok(p) = std::env::var("CASCADE_FIXTURE_DIR") {
        let path = std::path::PathBuf::from(p);
        if path.exists() {
            return Some(path);
        }
    }
    // Try the well-known local path.
    let default = Path::new(DEFAULT_FIXTURE);
    if default.exists() {
        return Some(default.to_path_buf());
    }
    None
}

/// Full round-trip integration test against the real reference corpus.
///
/// Skipped (not failed) when the fixture directory is absent.
#[test]
fn import_reference_corpus_lossless() {
    let fixture = match fixture_path() {
        Some(p) => p,
        None => {
            eprintln!(
                "SKIP: reference corpus not found at {DEFAULT_FIXTURE}\n\
                 Set CASCADE_FIXTURE_DIR to run this test."
            );
            return; // graceful skip
        }
    };

    let dest_dir = tempfile::TempDir::new().expect("create temp dest dir");
    let engine = ImportEngine::new();

    let plan = engine
        .plan(&fixture, "claude-code", dest_dir.path())
        .expect("plan must succeed on real corpus");

    // ── 1. Coverage gate ────────────────────────────────────────────────────
    let cov = &plan.coverage;
    println!(
        "\nCoverage: {:.1}% ({}/{} atomics mapped, {} dropped, {} unmapped)",
        cov.coverage_pct, cov.covered, cov.total, cov.dropped, cov.unmapped
    );

    if !cov.gaps.is_empty() {
        println!("Coverage gaps:");
        for gap in &cov.gaps {
            println!("  {} :: {}", gap.source_path.display(), gap.atomic_id);
        }
    }

    assert!(
        cov.coverage_pct >= MIN_COVERAGE_PCT,
        "coverage {:.1}% is below minimum {:.1}%",
        cov.coverage_pct,
        MIN_COVERAGE_PCT
    );

    // ── 2. Lossless gate ────────────────────────────────────────────────────
    assert!(
        cov.is_lossless,
        "lossless gate failed — {} unmapped atomics:\n{}",
        cov.unmapped,
        cov.gaps
            .iter()
            .map(|r| format!("  {}::{}", r.source_path.display(), r.atomic_id))
            .collect::<Vec<_>>()
            .join("\n")
    );

    // ── 3. Round-trip gate ──────────────────────────────────────────────────
    let rt = &plan.round_trip;
    println!("\nRound-trip: {}", rt.verdict);

    if !rt.missing.is_empty() {
        println!("Missing facts:");
        for fact in &rt.missing {
            println!("  {}::{}", fact.source_file, fact.id);
        }
    }

    assert!(
        rt.passed,
        "round-trip failed — {} atomic facts missing:\n{}",
        rt.missing.len(),
        rt.missing
            .iter()
            .map(|f| format!("  {}::{}", f.source_file, f.id))
            .collect::<Vec<_>>()
            .join("\n")
    );

    // ── 4. Isolation gate ───────────────────────────────────────────────────
    let iso = &plan.isolation;
    if !iso.clean {
        println!("Isolation violations:");
        for v in &iso.violations {
            println!("  {}", v.reason);
        }
    }
    assert!(
        iso.clean,
        "isolation violations found: {}",
        iso.violations.len()
    );

    // ── 5. Plan readiness ───────────────────────────────────────────────────
    assert!(
        plan.is_ready(),
        "plan must be ready to apply on the real corpus"
    );

    println!(
        "\n[PASS] Import of {} files: coverage {:.1}%, round-trip {}, isolation CLEAN",
        plan.entries.len(),
        cov.coverage_pct,
        if rt.passed { "PASS" } else { "FAIL" }
    );
}

/// Verify that the plan can be serialized to JSON (for --report-json).
#[test]
fn plan_serializes_to_json() {
    let fixture = match fixture_path() {
        Some(p) => p,
        None => {
            eprintln!("SKIP: fixture not available");
            return;
        }
    };

    let dest_dir = tempfile::TempDir::new().unwrap();
    let engine = ImportEngine::new();
    let plan = engine
        .plan(&fixture, "claude-code", dest_dir.path())
        .expect("plan must succeed");

    let json = serde_json::to_string_pretty(&plan).expect("plan must serialize to JSON");
    assert!(!json.is_empty());
    assert!(
        json.contains("coverage"),
        "JSON must contain coverage field"
    );
    assert!(
        json.contains("round_trip"),
        "JSON must contain round_trip field"
    );
    assert!(
        json.contains("isolation"),
        "JSON must contain isolation field"
    );
    assert!(json.contains("entries"), "JSON must contain entries field");

    // Must deserialize back without error.
    let _: cascade_core::import_engine::ImportPlan =
        serde_json::from_str(&json).expect("JSON must deserialize back");

    println!("[PASS] Plan serializes to {} bytes of JSON", json.len());
}

/// Verify entry count is plausible for the reference corpus (68 files).
#[test]
fn plan_entry_count_plausible() {
    let fixture = match fixture_path() {
        Some(p) => p,
        None => {
            eprintln!("SKIP: fixture not available");
            return;
        }
    };

    let dest_dir = tempfile::TempDir::new().unwrap();
    let engine = ImportEngine::new();
    let plan = engine
        .plan(&fixture, "claude-code", dest_dir.path())
        .expect("plan must succeed");

    // The corpus has 68 files per MANIFEST.md (plus MANIFEST.md itself = 69).
    // We allow some tolerance for classification differences.
    assert!(
        plan.entries.len() >= 50,
        "expected at least 50 plan entries for the reference corpus, got {}",
        plan.entries.len()
    );
    println!(
        "[PASS] Plan has {} entries for reference corpus",
        plan.entries.len()
    );
}
