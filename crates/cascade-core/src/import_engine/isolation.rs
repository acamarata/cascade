//! # isolation — scope-isolation verifier for the import engine
//!
//! ## Purpose
//! Verify that no file classified as private, protected, or narrow-scope leaks
//! into a broader-scope tier during migration. A memory file destined for PRC
//! must never be mapped to GCI; an inbox message for a project must not merge
//! into the global tier.
//!
//! ## Inputs
//! `&[(DiscoveredFile, TierMapping)]` — the combined plan entries.
//!
//! ## Outputs
//! `IsolationReport` — list of violations found (empty = no violations).
//!
//! ## Constraints
//! - Only checks scope *widening* (more-global tier than source implies).
//! - Scripts and templates are exempt (they are intentionally global assets).
//! - Library-mapped files (LoadMode::Library) at any tier are exempt.
//!
//! ## Errors
//! Infallible — reports violations rather than returning errors.
//!
//! ## Notes
//! The scope hierarchy for isolation checking: PAC < PRC < PPC < APC < PCI < GCI.
//! A file whose source implies PRC must not be mapped to GCI or PCI.

use std::path::PathBuf;

use cascade_types::cascade_tier::CascadeTier;
use serde::{Deserialize, Serialize};

use super::discover::{DiscoveredFile, SourceKind};
use super::tier_map::{LoadMode, TierMapping};

// ── Public types ──────────────────────────────────────────────────────────────

/// A single scope-isolation violation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IsolationViolation {
    /// Source file relative path.
    pub source_path: PathBuf,
    /// Source kind (for context in error messages).
    pub source_kind: SourceKind,
    /// The tier the file was mapped to.
    pub mapped_to: CascadeTier,
    /// The maximum allowed tier (most-global scope allowed for this file kind).
    pub max_allowed: CascadeTier,
    /// Human-readable explanation.
    pub reason: String,
}

/// Result of the isolation check over a full import plan.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct IsolationReport {
    /// All scope violations found.
    pub violations: Vec<IsolationViolation>,
    /// Whether the plan is clean (no violations).
    pub clean: bool,
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Check all plan entries for scope-isolation violations.
///
/// Returns an [`IsolationReport`] with the full list of violations.
/// An empty violations list means the plan is clean.
pub fn check_isolation(plan: &[(DiscoveredFile, TierMapping)]) -> IsolationReport {
    let mut violations = Vec::new();

    for (file, mapping) in plan {
        // Library-mode files are global by design — skip.
        if mapping.load_mode == LoadMode::Library {
            continue;
        }

        let max_allowed = max_allowed_tier(&file.kind);

        // Check for scope widening: mapped tier is "higher" (broader) than allowed.
        // CascadeTier Ord: Gci < Pci < Apc < Ppc < Prc < Pac (higher enum = narrower scope).
        // Scope widening = mapped tier is LESS THAN (higher priority, broader) the max.
        if tier_ordinal(mapping.tier) < tier_ordinal(max_allowed) {
            violations.push(IsolationViolation {
                source_path: file.relative_path.clone(),
                source_kind: file.kind.clone(),
                mapped_to: mapping.tier,
                max_allowed,
                reason: format!(
                    "{:?} file (max scope {:?}) mapped to broader tier {:?} — \
                     private content would leak to a shared scope",
                    file.kind, max_allowed, mapping.tier
                ),
            });
        }
    }

    let clean = violations.is_empty();
    IsolationReport { violations, clean }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Returns the most-global (broadest) tier a source kind is allowed to be
/// mapped to.
///
/// Kinds that are inherently narrow-scope (memory, inbox, ideas, tasks) must
/// stay within the project-level tiers. Global instruction files can go to GCI.
fn max_allowed_tier(kind: &SourceKind) -> CascadeTier {
    match kind {
        // Global instruction files and behavioral rules are GCI-native.
        SourceKind::InstructionRoot => CascadeTier::Gci,
        SourceKind::AlwaysLoadedRule => CascadeTier::Gci,
        SourceKind::OnDemandReference => CascadeTier::Gci,
        SourceKind::Standard => CascadeTier::Gci,
        // Templates are GCI-safe (shared across all projects).
        SourceKind::Template => CascadeTier::Gci,
        // Scripts are GCI-safe (shared tools).
        SourceKind::Script => CascadeTier::Gci,
        // Site-tier files are at most PCI (project-group).
        SourceKind::SiteTier => CascadeTier::Pci,
        // Memory, inbox, ideas, tasks are project-scoped — max PPC.
        SourceKind::Memory => CascadeTier::Ppc,
        SourceKind::Inbox => CascadeTier::Ppc,
        SourceKind::Idea => CascadeTier::Ppc,
        SourceKind::Task => CascadeTier::Ppc,
        // Unknown files: allow GCI (they land in library/unclassified).
        SourceKind::Unknown => CascadeTier::Gci,
    }
}

// ── Tier numeric order helper ─────────────────────────────────────────────────

// CascadeTier Ord: Gci < Pci < Apc < Ppc < Prc < Pac (smaller = broader scope).
// The isolation check catches scope widening: mapped_tier has a smaller ordinal
// than max_allowed_tier.

/// Returns the ordinal for a tier (GCI=0 broadest, PAC=5 narrowest).
fn tier_ordinal(tier: CascadeTier) -> u8 {
    match tier {
        CascadeTier::Gci => 0,
        CascadeTier::Pci => 1,
        CascadeTier::Apc => 2,
        CascadeTier::Ppc => 3,
        CascadeTier::Prc => 4,
        CascadeTier::Pac => 5,
    }
}

/// Returns true when `mapped_tier` is broader (lower ordinal) than `max_allowed`.
///
/// Used in tests and by the isolation checker.
pub fn has_scope_widening(mapped_tier: CascadeTier, max_allowed: CascadeTier) -> bool {
    tier_ordinal(mapped_tier) < tier_ordinal(max_allowed)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use super::*;
    use crate::import_engine::tier_map::LoadMode;

    fn make_entry(
        rel: &str,
        kind: SourceKind,
        tier: CascadeTier,
        load_mode: LoadMode,
    ) -> (DiscoveredFile, TierMapping) {
        let file = DiscoveredFile {
            path: PathBuf::from(rel),
            relative_path: PathBuf::from(rel),
            kind,
            imports: vec![],
            pointers: vec![],
            readable: true,
            content: String::new(),
        };
        let mapping = TierMapping {
            tier,
            cascade_relative_path: PathBuf::from(rel),
            load_mode,
            reason: "test".to_string(),
        };
        (file, mapping)
    }

    #[test]
    fn no_violation_for_rule_at_gci() {
        let entry = make_entry(
            "rules/deny.md",
            SourceKind::AlwaysLoadedRule,
            CascadeTier::Gci,
            LoadMode::AlwaysLoaded,
        );
        let report = check_isolation(&[entry]);
        assert!(report.clean, "rule at GCI is valid");
    }

    #[test]
    fn violation_when_memory_mapped_to_gci() {
        let entry = make_entry(
            "memory/decisions.md",
            SourceKind::Memory,
            CascadeTier::Gci, // too broad for memory
            LoadMode::OnDemand,
        );
        let report = check_isolation(&[entry]);
        assert!(!report.clean, "memory mapped to GCI is a violation");
        assert_eq!(report.violations.len(), 1);
    }

    #[test]
    fn library_mode_skips_isolation_check() {
        // A script mapped to GCI with Library mode is fine.
        let entry = make_entry(
            "scripts/claude-recall",
            SourceKind::Script,
            CascadeTier::Gci,
            LoadMode::Library, // library mode → skip check
        );
        let report = check_isolation(&[entry]);
        assert!(report.clean, "library-mode files are exempt");
    }

    #[test]
    fn site_tier_at_pci_is_valid() {
        let entry = make_entry(
            "sites/ASI-CLAUDE.md",
            SourceKind::SiteTier,
            CascadeTier::Pci,
            LoadMode::OnDemand,
        );
        let report = check_isolation(&[entry]);
        assert!(report.clean, "site-tier file at PCI is valid");
    }

    #[test]
    fn site_tier_at_gci_is_violation() {
        let entry = make_entry(
            "sites/ASI-CLAUDE.md",
            SourceKind::SiteTier,
            CascadeTier::Gci, // broader than PCI max
            LoadMode::OnDemand,
        );
        let report = check_isolation(&[entry]);
        assert!(!report.clean, "site-tier at GCI is scope widening");
    }

    #[test]
    fn inbox_at_ppc_is_valid() {
        let entry = make_entry(
            "inbox/message.md",
            SourceKind::Inbox,
            CascadeTier::Ppc,
            LoadMode::Library,
        );
        let report = check_isolation(&[entry]);
        assert!(report.clean);
    }

    #[test]
    fn has_scope_widening_logic() {
        // GCI (0) is broader than PPC (3).
        assert!(has_scope_widening(CascadeTier::Gci, CascadeTier::Ppc));
        // PRC (4) is not broader than PPC (3).
        assert!(!has_scope_widening(CascadeTier::Prc, CascadeTier::Ppc));
    }
}
