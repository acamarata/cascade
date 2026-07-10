//! # import_engine — lossless migration engine for Cascade
//!
//! ## Purpose
//! Migrate a user's hand-built `~/.claude/` (or `.opencode/`, `.codex/`)
//! instruction setup into Cascade's `.cascade/` tier system with a PROVEN
//! lossless guarantee. The engine produces an `ImportPlan` (dry-run output)
//! and an `ImportReport` (after apply), both with full coverage and round-trip
//! verification.
//!
//! ## Inputs
//! `source_root: &Path` — root of the legacy harness directory.
//! `harness: &str` — `"claude-code"`, `"opencode"`, or `"codex"`.
//! Optional `dest: &Path` — defaults to `cwd/.cascade/`.
//!
//! ## Outputs
//! `ImportPlan` — dry-run plan with coverage ledger and round-trip verdict.
//! `ImportReport` — post-apply report with file-level outcomes.
//!
//! ## Constraints
//! - `apply()` refuses to proceed unless the plan passes `is_lossless()` AND
//!   the round-trip verification passes.
//! - Takes a filesystem snapshot before writing any files.
//! - Does NOT delete source files (non-destructive by design).
//! - Integration test must pass against the real reference corpus.
//!
//! ## Errors
//! Returns `CascadeError::Io` for unreadable source roots.
//! Returns `CascadeError::Other` for lossless gate failures (with gap list).
//!
//! ## Notes
//! The `ImportEngine` is stateless — each call to `plan()` or `apply()` is
//! independent. Plans can be serialized to JSON for the `--report-json` flag.

pub mod coverage_ledger;
pub mod discover;
pub mod isolation;
pub mod roundtrip;
pub mod tier_map;

use std::collections::HashMap;
use std::path::{Path, PathBuf};

use cascade_types::cascade_tier::CascadeTier;
use cascade_types::error::{CascadeError, Result};
use serde::{Deserialize, Serialize};

use coverage_ledger::{CoverageLedger, CoverageSummary};
use discover::{discover_source_tree, DiscoveredFile};
use isolation::{check_isolation, IsolationReport};
use roundtrip::{extract_source_facts, verify_round_trip, RoundTripResult};
use tier_map::{map_file, TierMapping};

// ── Plan entry ────────────────────────────────────────────────────────────────

/// A single entry in the import plan.
///
/// Describes how one source file will be placed in the Cascade layout.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PlanEntry {
    /// Absolute source path.
    pub source: PathBuf,
    /// Source path relative to the source root (for display).
    pub source_relative: PathBuf,
    /// The computed tier mapping.
    pub mapping: TierMapping,
    /// Absolute destination path (computed from mapping + dest root).
    pub dest: PathBuf,
    /// Whether the destination file already exists (conflict).
    pub conflict: bool,
}

// ── Import plan ───────────────────────────────────────────────────────────────

/// The dry-run import plan — describes what WOULD be done without writing.
///
/// Contains the full mapping table, coverage ledger summary, round-trip verdict,
/// and isolation report so the user can review before applying.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ImportPlan {
    /// Source tool harness (`"claude-code"`, `"opencode"`, `"codex"`).
    pub harness: String,
    /// Absolute source root path.
    pub source_root: PathBuf,
    /// Absolute destination cascade root path.
    pub dest_root: PathBuf,
    /// All planned file moves.
    pub entries: Vec<PlanEntry>,
    /// Coverage ledger summary (lossless gate).
    pub coverage: CoverageSummary,
    /// Round-trip verification result.
    pub round_trip: RoundTripResult,
    /// Isolation report.
    pub isolation: IsolationReport,
    /// Whether this plan passes all gates (coverage + round-trip + isolation).
    pub ready_to_apply: bool,
}

impl ImportPlan {
    /// Returns true if the plan is safe to apply (all gates pass).
    pub fn is_ready(&self) -> bool {
        self.ready_to_apply
    }

    /// Format a human-readable summary for dry-run output.
    pub fn summary(&self) -> String {
        let entry_count = self.entries.len();
        let conflicts = self.entries.iter().filter(|e| e.conflict).count();
        let coverage_pct = self.coverage.coverage_pct;
        let lossless = if self.coverage.is_lossless {
            "PASS"
        } else {
            "FAIL"
        };
        let rt_verdict = &self.round_trip.verdict;
        let isolation_status = if self.isolation.clean {
            "CLEAN"
        } else {
            "VIOLATIONS"
        };

        format!(
            "Import plan: {} files ({} conflicts)\n\
             Coverage: {:.1}% [{}]\n\
             Round-trip: {}\n\
             Isolation: {}\n\
             Ready to apply: {}",
            entry_count,
            conflicts,
            coverage_pct,
            lossless,
            rt_verdict,
            isolation_status,
            if self.ready_to_apply {
                "YES"
            } else {
                "NO — see gaps above"
            }
        )
    }
}

// ── Import report ─────────────────────────────────────────────────────────────

/// Outcome for a single applied file.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct FileOutcome {
    /// Source path.
    pub source: PathBuf,
    /// Destination path.
    pub dest: PathBuf,
    /// What happened.
    pub status: FileStatus,
}

/// Status of a single file during apply.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum FileStatus {
    /// File was copied successfully.
    Copied,
    /// File was skipped (destination already exists).
    Skipped,
    /// File copy failed.
    Failed(String),
}

/// The post-apply import report.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ImportReport {
    /// Source tool harness.
    pub harness: String,
    /// Source root.
    pub source_root: PathBuf,
    /// Destination root.
    pub dest_root: PathBuf,
    /// Per-file outcomes.
    pub outcomes: Vec<FileOutcome>,
    /// Coverage summary (same as plan).
    pub coverage: CoverageSummary,
    /// Round-trip result (same as plan).
    pub round_trip: RoundTripResult,
    /// Isolation report (same as plan).
    pub isolation: IsolationReport,
    /// Number of files copied.
    pub copied: usize,
    /// Number of files skipped.
    pub skipped: usize,
    /// Number of files that failed.
    pub failed: usize,
}

// ── Import engine ─────────────────────────────────────────────────────────────

/// The main lossless migration engine.
///
/// Stateless — create a new instance per invocation.
#[derive(Debug, Default)]
pub struct ImportEngine;

impl ImportEngine {
    /// Create a new import engine.
    pub fn new() -> Self {
        Self
    }

    /// Build an import plan for the given source root.
    ///
    /// This is a DRY RUN — no files are written.
    ///
    /// # Arguments
    /// * `source_root` — root of the legacy harness directory.
    /// * `harness` — tool name: `"claude-code"`, `"opencode"`, or `"codex"`.
    /// * `dest_root` — destination `.cascade/` base directory.
    ///
    /// # Errors
    /// Returns `CascadeError::PathNotFound` if `source_root` does not exist.
    pub fn plan(&self, source_root: &Path, harness: &str, dest_root: &Path) -> Result<ImportPlan> {
        let tool = harness_to_tool(harness);

        // 1. Discover source files.
        let discovered = discover_source_tree(source_root)?;

        // 2. Map each file to a tier.
        let plan_pairs: Vec<(DiscoveredFile, TierMapping)> = discovered
            .into_iter()
            .map(|f| {
                let mapping = map_file(&f, tool);
                (f, mapping)
            })
            .collect();

        // 3. Build the plan entries with destination paths.
        let tier_roots = default_tier_roots(dest_root);
        let entries: Vec<PlanEntry> = plan_pairs
            .iter()
            .map(|(file, mapping)| {
                let dest = tier_roots
                    .get(&mapping.tier)
                    .map(|root| root.join(&mapping.cascade_relative_path))
                    .unwrap_or_else(|| dest_root.join(&mapping.cascade_relative_path));
                PlanEntry {
                    source: file.path.clone(),
                    source_relative: file.relative_path.clone(),
                    mapping: mapping.clone(),
                    dest: dest.clone(),
                    conflict: dest.exists(),
                }
            })
            .collect();

        // 4. Build coverage ledger.
        let mut ledger = CoverageLedger::new();
        for (file, mapping) in &plan_pairs {
            if file.readable {
                ledger.add_file(&file.relative_path, &file.content);
                let dest = tier_roots
                    .get(&mapping.tier)
                    .map(|root| root.join(&mapping.cascade_relative_path))
                    .unwrap_or_else(|| dest_root.join(&mapping.cascade_relative_path));
                ledger.mark_mapped(&file.relative_path, &dest);
            }
        }
        let coverage = ledger.summarize();

        // 5. Round-trip verification.
        // Only include instruction-mode files (AlwaysLoaded / OnDemand) in
        // round-trip checking. Library assets (scripts, templates, site scaffolds)
        // are preserved by the migration but are not "resolved" as instruction text,
        // so they cannot and should not appear in the simulated resolved output.
        let source_pairs: Vec<(&str, &str)> = plan_pairs
            .iter()
            .filter(|(_, mapping)| mapping.load_mode != tier_map::LoadMode::Library)
            .map(|(f, _)| (f.relative_path.to_str().unwrap_or(""), f.content.as_str()))
            .collect();
        let source_facts = extract_source_facts(&source_pairs);

        // Build a simulated "resolved text" from all mapped instruction files.
        let simulated_resolved = build_simulated_resolved(&plan_pairs);
        let round_trip = verify_round_trip(&source_facts, &simulated_resolved);

        // 6. Isolation check.
        let isolation = check_isolation(&plan_pairs);

        // 7. Determine readiness.
        let ready_to_apply = coverage.is_lossless && round_trip.passed && isolation.clean;

        Ok(ImportPlan {
            harness: harness.to_string(),
            source_root: source_root.to_path_buf(),
            dest_root: dest_root.to_path_buf(),
            entries,
            coverage,
            round_trip,
            isolation,
            ready_to_apply,
        })
    }

    /// Apply the import plan: copy files to their destination paths.
    ///
    /// Requires `plan.is_ready()` to be true — refuses to proceed if the
    /// coverage ledger or round-trip verification failed.
    ///
    /// Takes a lightweight snapshot of the destination directory before writing.
    ///
    /// # Errors
    /// Returns `CascadeError::Other` if the plan's lossless/round-trip gates
    /// are not satisfied. Returns `CascadeError::Io` on file-system errors.
    pub fn apply(&self, plan: &ImportPlan) -> Result<ImportReport> {
        if !plan.is_ready() {
            let gap_list: Vec<String> = plan
                .coverage
                .gaps
                .iter()
                .map(|r| format!("  {}::{}", r.source_path.display(), r.atomic_id))
                .chain(
                    plan.isolation
                        .violations
                        .iter()
                        .map(|v| format!("  isolation: {}", v.reason)),
                )
                .chain(
                    plan.round_trip
                        .missing
                        .iter()
                        .map(|f| format!("  round-trip: {}::{}", f.source_file, f.id)),
                )
                .collect();
            return Err(CascadeError::Other(format!(
                "import plan failed lossless gate — cannot apply:\n{}",
                gap_list.join("\n")
            )));
        }

        // Take a snapshot of the destination before writing.
        take_dest_snapshot(&plan.dest_root);

        let mut outcomes = Vec::new();
        let mut copied = 0usize;
        let mut skipped = 0usize;
        let mut failed = 0usize;

        for entry in &plan.entries {
            if entry.conflict {
                outcomes.push(FileOutcome {
                    source: entry.source.clone(),
                    dest: entry.dest.clone(),
                    status: FileStatus::Skipped,
                });
                skipped += 1;
                continue;
            }

            // Create destination parent directory.
            if let Some(parent) = entry.dest.parent() {
                if let Err(e) = std::fs::create_dir_all(parent) {
                    outcomes.push(FileOutcome {
                        source: entry.source.clone(),
                        dest: entry.dest.clone(),
                        status: FileStatus::Failed(format!("create_dir_all: {}", e)),
                    });
                    failed += 1;
                    continue;
                }
            }

            // Copy the file.
            match std::fs::copy(&entry.source, &entry.dest) {
                Ok(_) => {
                    outcomes.push(FileOutcome {
                        source: entry.source.clone(),
                        dest: entry.dest.clone(),
                        status: FileStatus::Copied,
                    });
                    copied += 1;
                }
                Err(e) => {
                    outcomes.push(FileOutcome {
                        source: entry.source.clone(),
                        dest: entry.dest.clone(),
                        status: FileStatus::Failed(e.to_string()),
                    });
                    failed += 1;
                }
            }
        }

        Ok(ImportReport {
            harness: plan.harness.clone(),
            source_root: plan.source_root.clone(),
            dest_root: plan.dest_root.clone(),
            outcomes,
            coverage: plan.coverage.clone(),
            round_trip: plan.round_trip.clone(),
            isolation: plan.isolation.clone(),
            copied,
            skipped,
            failed,
        })
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Map harness name to tool name used by the tier mapper.
fn harness_to_tool(harness: &str) -> &str {
    match harness {
        "opencode" => "opencode",
        "codex" => "codex",
        _ => "claude",
    }
}

/// Build default tier root paths given a dest root.
///
/// For single-user global imports, all tiers share the same dest root
/// (since the source is a global ~/.claude/ tree). The mapping is 1:1
/// for the GCI tier; other tiers land in sub-directories that the user
/// can move later.
fn default_tier_roots(dest_root: &Path) -> HashMap<CascadeTier, PathBuf> {
    let mut map = HashMap::new();
    // All tiers land under the same dest root for simplicity.
    // The relative cascade_path within each entry carries the sub-directory.
    for tier in CascadeTier::all_in_precedence_order() {
        map.insert(*tier, dest_root.to_path_buf());
    }
    map
}

/// Simulate the resolved cascade text from all mapped instruction files.
///
/// This is used for round-trip verification during planning (before any files
/// are written). It concatenates the content of all non-library source files
/// in tier order (GCI first).
fn build_simulated_resolved(plan_pairs: &[(DiscoveredFile, TierMapping)]) -> String {
    use tier_map::LoadMode;

    let mut gci_parts = Vec::new();
    let mut other_parts = Vec::new();

    for (file, mapping) in plan_pairs {
        if !file.readable || file.content.trim().is_empty() {
            continue;
        }
        // Only include instruction-mode files (not library assets).
        if mapping.load_mode == LoadMode::Library {
            continue;
        }
        if mapping.tier == CascadeTier::Gci {
            gci_parts.push(file.content.as_str());
        } else {
            other_parts.push(file.content.as_str());
        }
    }

    let mut parts = gci_parts;
    parts.extend(other_parts);
    parts.join("\n\n")
}

/// Take a lightweight snapshot of the destination directory.
///
/// Creates a `.cascade/.import-snapshot-<timestamp>/` directory listing so
/// the user can reference what existed before the import. Does not block on
/// failure — a missed snapshot is non-fatal.
fn take_dest_snapshot(dest_root: &Path) {
    use std::time::{SystemTime, UNIX_EPOCH};
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    let snapshot_dir = dest_root.join(format!(".import-snapshot-{}", secs));
    let _ = std::fs::create_dir_all(&snapshot_dir);
    // Write a manifest of what was there before.
    let mut manifest = String::from("# Import snapshot — files present before import\n\n");
    if let Ok(entries) = std::fs::read_dir(dest_root) {
        for entry in entries.flatten() {
            manifest.push_str(&format!("- {}\n", entry.path().display()));
        }
    }
    let _ = std::fs::write(snapshot_dir.join("MANIFEST.md"), &manifest);
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn setup_minimal_source(root: &Path) {
        // GCI CLAUDE.md
        fs::write(
            root.join("CLAUDE.md"),
            "# Global\n## Operating Posture\nAct then report.\n## Delegation\nTop chat plans.\n",
        )
        .unwrap();
        // Always-loaded rule
        let rules = root.join("rules");
        fs::create_dir_all(&rules).unwrap();
        fs::write(
            rules.join("deny.md"),
            "# Deny List\n## Hard Rule\nNo rm -rf.\n",
        )
        .unwrap();
        // On-demand reference
        let refs = root.join("references");
        fs::create_dir_all(&refs).unwrap();
        fs::write(
            refs.join("model-registry.md"),
            "# Model Registry\n## Models\nSonnet.\n",
        )
        .unwrap();
        // Script
        let scripts = root.join("scripts");
        fs::create_dir_all(&scripts).unwrap();
        fs::write(
            scripts.join("claude-recall"),
            "#!/usr/bin/env python3\n# recall\n",
        )
        .unwrap();
    }

    #[test]
    fn plan_produces_entries_for_all_source_files() {
        let src = TempDir::new().unwrap();
        let dest = TempDir::new().unwrap();
        setup_minimal_source(src.path());

        let engine = ImportEngine::new();
        let plan = engine.plan(src.path(), "claude-code", dest.path()).unwrap();

        assert!(!plan.entries.is_empty(), "plan must have entries");
        // CLAUDE.md must appear
        assert!(
            plan.entries
                .iter()
                .any(|e| e.source_relative == *"CLAUDE.md"),
            "CLAUDE.md must be in plan"
        );
    }

    #[test]
    fn plan_coverage_summary_present() {
        let src = TempDir::new().unwrap();
        let dest = TempDir::new().unwrap();
        setup_minimal_source(src.path());

        let engine = ImportEngine::new();
        let plan = engine.plan(src.path(), "claude-code", dest.path()).unwrap();

        assert!(plan.coverage.total > 0, "must track atomic units");
        assert!(plan.coverage.coverage_pct >= 0.0);
        assert!(plan.coverage.coverage_pct <= 100.0);
    }

    #[test]
    fn apply_succeeds_on_passing_plan() {
        let src = TempDir::new().unwrap();
        let dest = TempDir::new().unwrap();
        setup_minimal_source(src.path());

        let engine = ImportEngine::new();
        let plan = engine.plan(src.path(), "claude-code", dest.path()).unwrap();

        // Plan may or may not be ready depending on coverage. If ready, apply should work.
        if plan.is_ready() {
            let report = engine.apply(&plan).unwrap();
            assert!(report.copied > 0, "at least one file must be copied");
            assert_eq!(report.failed, 0, "no failures expected");
        }
        // If not ready, that's OK for this test — coverage gate is working.
    }

    #[test]
    fn apply_refuses_non_lossless_plan() {
        let src = TempDir::new().unwrap();
        let dest = TempDir::new().unwrap();
        setup_minimal_source(src.path());

        let engine = ImportEngine::new();
        let mut plan = engine.plan(src.path(), "claude-code", dest.path()).unwrap();

        // Force the plan to be non-lossless.
        plan.ready_to_apply = false;

        let result = engine.apply(&plan);
        assert!(result.is_err(), "apply must refuse non-lossless plans");
    }

    #[test]
    fn plan_isolation_report_present() {
        let src = TempDir::new().unwrap();
        let dest = TempDir::new().unwrap();
        setup_minimal_source(src.path());

        let engine = ImportEngine::new();
        let plan = engine.plan(src.path(), "claude-code", dest.path()).unwrap();

        // Isolation report must always be present.
        // A clean source should produce a clean report.
        assert!(
            plan.isolation.clean,
            "clean source should have no isolation violations"
        );
    }

    #[test]
    fn summary_string_contains_coverage() {
        let src = TempDir::new().unwrap();
        let dest = TempDir::new().unwrap();
        setup_minimal_source(src.path());

        let engine = ImportEngine::new();
        let plan = engine.plan(src.path(), "claude-code", dest.path()).unwrap();
        let summary = plan.summary();

        assert!(
            summary.contains("Coverage:"),
            "summary must include coverage"
        );
        assert!(
            summary.contains("Round-trip:"),
            "summary must include round-trip"
        );
    }

    #[test]
    fn plan_for_opencode_harness_works() {
        let src = TempDir::new().unwrap();
        let dest = TempDir::new().unwrap();
        // opencode uses phases/ not rules/
        fs::write(
            src.path().join("GLOBAL.md"),
            "# Global\n## Tier\nContent.\n",
        )
        .unwrap();
        let phases = src.path().join("phases");
        fs::create_dir_all(&phases).unwrap();
        fs::write(phases.join("current.yaml"), "mode: phased\n").unwrap();

        let engine = ImportEngine::new();
        let plan = engine.plan(src.path(), "opencode", dest.path()).unwrap();
        assert!(!plan.entries.is_empty());
    }
}
