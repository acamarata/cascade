//! # coverage_ledger — THE LOSSLESS GATE
//!
//! ## Purpose
//! Track every source file and every atomic instruction unit (heading-or-bullet
//! level) through the migration. A ledger row is created for each atomic unit
//! with a status: `Mapped`, `Transformed`, `Dropped`, or `Unmapped`.
//! `ledger.is_lossless()` returns true when zero rows have `Dropped` or
//! `Unmapped` status (excluding an explicit allowlist).
//!
//! ## Inputs
//! Source files via `add_file()`, then their mapping status via
//! `mark_mapped()` / `mark_dropped()` / `mark_unmapped()`.
//!
//! ## Outputs
//! `CoverageSummary` with percentage, gap table, and lossless flag.
//!
//! ## Constraints
//! - Atomics are heading-level (##) or top-level bullet (-/*/1.) units.
//! - Allowlisted patterns (secrets, PII paths, binary files) are excluded from
//!   the lossless gate.
//! - All arithmetic is integer-safe (no overflow on usize).
//!
//! ## Errors
//! Infallible — the ledger never returns errors.
//!
//! ## Notes
//! The "atomic fact" abstraction is deliberately coarse-grained: a heading or
//! a top-level bullet point, not individual sentences. This avoids false
//! negatives from whitespace normalization while still catching genuinely lost
//! content sections.

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};

// ── Allowlist ─────────────────────────────────────────────────────────────────

/// Path prefixes that are intentionally excluded from the lossless gate.
///
/// These patterns cover files that should never be migrated (secrets, PII,
/// binary assets, OS metadata). The allowlist is checked against the relative
/// source path.
const EXCLUDED_PATTERNS: &[&str] = &[
    "vault.env",
    ".env",
    "*.key",
    "*.pem",
    "*.p12",
    "*.pfx",
    ".DS_Store",
    "Thumbs.db",
    "node_modules",
    ".git",
];

/// Returns true if a relative path matches any exclusion pattern.
pub fn is_excluded(relative: &Path) -> bool {
    let s = relative.to_string_lossy();
    for pat in EXCLUDED_PATTERNS {
        if let Some(ext) = pat.strip_prefix('*') {
            if s.ends_with(ext) {
                return true;
            }
        } else if s.contains(pat) {
            return true;
        }
    }
    false
}

// ── Ledger status ─────────────────────────────────────────────────────────────

/// The migration status of a single atomic unit.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub enum LedgerStatus {
    /// The unit was successfully mapped to a Cascade target.
    Mapped,
    /// The unit was transformed (e.g. restructured) but all content survives.
    Transformed,
    /// The unit was intentionally dropped (allowlisted exclusion).
    Dropped,
    /// The unit could not be mapped and is not in the allowlist — a gap.
    Unmapped,
}

// ── Ledger row ────────────────────────────────────────────────────────────────

/// A single row in the coverage ledger.
///
/// One row is created per atomic instruction unit (heading or top-level bullet).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LedgerRow {
    /// Source file relative path.
    pub source_path: PathBuf,
    /// Unique atomic identifier within the file (e.g. `H2:Operating Posture`).
    pub atomic_id: String,
    /// The Cascade target path this unit was mapped to (if mapped).
    pub mapped_to: Option<PathBuf>,
    /// Migration status.
    pub status: LedgerStatus,
}

// ── Coverage summary ──────────────────────────────────────────────────────────

/// Summary of coverage ledger accounting.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CoverageSummary {
    /// Total atomic units tracked (excluding allowlisted).
    pub total: usize,
    /// Number of units with Mapped or Transformed status.
    pub covered: usize,
    /// Number of units with Dropped status (allowlisted exclusions).
    pub dropped: usize,
    /// Number of units with Unmapped status (gaps that block lossless gate).
    pub unmapped: usize,
    /// Coverage percentage (covered / (total - dropped) * 100), 0-100.
    pub coverage_pct: f64,
    /// Whether zero rows are Unmapped (i.e. the migration is lossless).
    pub is_lossless: bool,
    /// All unmapped rows — printed to the user as the "gap list".
    pub gaps: Vec<LedgerRow>,
}

// ── Coverage ledger ───────────────────────────────────────────────────────────

/// The coverage ledger that tracks every atomic instruction unit.
#[derive(Debug, Default)]
pub struct CoverageLedger {
    rows: Vec<LedgerRow>,
}

impl CoverageLedger {
    /// Create a new empty ledger.
    pub fn new() -> Self {
        Self::default()
    }

    /// Add a file's atomic units to the ledger.
    ///
    /// Atomics are extracted from the file content: `##`-level headings and
    /// top-level bullet points (`-`, `*`, `1.`). Files on the exclusion
    /// allowlist are recorded as `Dropped` without extracting atomics.
    pub fn add_file(&mut self, source_path: &Path, content: &str) {
        if is_excluded(source_path) {
            // Record as a single dropped entry.
            self.rows.push(LedgerRow {
                source_path: source_path.to_path_buf(),
                atomic_id: "EXCLUDED".to_string(),
                mapped_to: None,
                status: LedgerStatus::Dropped,
            });
            return;
        }

        let atomics = extract_atomics(content);
        if atomics.is_empty() {
            // No extractable atomics (e.g. empty file or binary).
            self.rows.push(LedgerRow {
                source_path: source_path.to_path_buf(),
                atomic_id: "FILE".to_string(),
                mapped_to: None,
                status: LedgerStatus::Unmapped,
            });
        } else {
            for atomic_id in atomics {
                self.rows.push(LedgerRow {
                    source_path: source_path.to_path_buf(),
                    atomic_id,
                    mapped_to: None,
                    status: LedgerStatus::Unmapped,
                });
            }
        }
    }

    /// Mark all units from a source file as mapped to a Cascade target.
    pub fn mark_mapped(&mut self, source_path: &Path, target: &Path) {
        for row in &mut self.rows {
            if row.source_path == source_path && row.status == LedgerStatus::Unmapped {
                row.status = LedgerStatus::Mapped;
                row.mapped_to = Some(target.to_path_buf());
            }
        }
    }

    /// Mark all units from a source file as transformed (restructured but lossless).
    pub fn mark_transformed(&mut self, source_path: &Path, target: &Path) {
        for row in &mut self.rows {
            if row.source_path == source_path && row.status == LedgerStatus::Unmapped {
                row.status = LedgerStatus::Transformed;
                row.mapped_to = Some(target.to_path_buf());
            }
        }
    }

    /// Mark all units from a source file as explicitly dropped (allowed exclusion).
    pub fn mark_dropped(&mut self, source_path: &Path) {
        for row in &mut self.rows {
            if row.source_path == source_path {
                row.status = LedgerStatus::Dropped;
                row.mapped_to = None;
            }
        }
    }

    /// Compute and return the coverage summary.
    pub fn summarize(&self) -> CoverageSummary {
        let total = self
            .rows
            .iter()
            .filter(|r| r.status != LedgerStatus::Dropped)
            .count();
        let covered = self
            .rows
            .iter()
            .filter(|r| r.status == LedgerStatus::Mapped || r.status == LedgerStatus::Transformed)
            .count();
        let dropped = self
            .rows
            .iter()
            .filter(|r| r.status == LedgerStatus::Dropped)
            .count();
        let unmapped = self
            .rows
            .iter()
            .filter(|r| r.status == LedgerStatus::Unmapped)
            .count();

        let coverage_pct = if total == 0 {
            100.0
        } else {
            (covered as f64 / total as f64) * 100.0
        };

        let is_lossless = unmapped == 0;

        let gaps: Vec<LedgerRow> = self
            .rows
            .iter()
            .filter(|r| r.status == LedgerStatus::Unmapped)
            .cloned()
            .collect();

        CoverageSummary {
            total,
            covered,
            dropped,
            unmapped,
            coverage_pct,
            is_lossless,
            gaps,
        }
    }

    /// Returns all rows (for debugging / JSON export).
    pub fn rows(&self) -> &[LedgerRow] {
        &self.rows
    }
}

// ── Atomic extraction ─────────────────────────────────────────────────────────

/// Extract atomic instruction units from markdown content.
///
/// Atomics are:
/// - `##` or deeper headings (e.g. `## Operating Posture`)
/// - Top-level bullet points starting a new logical unit (`- ` / `* ` / `1. `)
///   that are not sub-bullets
///
/// Returns a list of atomic ID strings in the form `H2:Heading Text` or
/// `BULLET:first 60 chars`.
pub fn extract_atomics(content: &str) -> Vec<String> {
    let mut atomics = Vec::new();
    let mut seen = std::collections::HashSet::new();

    for line in content.lines() {
        let trimmed = line.trim_end();

        // Heading-level atomics (## or deeper).
        if let Some(text) = trimmed.strip_prefix("## ") {
            let text = text.trim();
            if !text.is_empty() {
                let id = format!("H2:{}", text);
                if seen.insert(id.clone()) {
                    atomics.push(id);
                }
            }
        } else if let Some(text) = trimmed.strip_prefix("### ") {
            let text = text.trim();
            if !text.is_empty() {
                let id = format!("H3:{}", text);
                if seen.insert(id.clone()) {
                    atomics.push(id);
                }
            }
        } else if let Some(text) = trimmed.strip_prefix("# ") {
            // Top-level heading = file-level anchor.
            let text = text.trim();
            if !text.is_empty() {
                let id = format!("H1:{}", text);
                if seen.insert(id.clone()) {
                    atomics.push(id);
                }
            }
        }
        // Top-level bullet points (non-indented).
        else if (trimmed.starts_with("- ")
            || trimmed.starts_with("* ")
            || trimmed.starts_with("+ "))
            && !line.starts_with(' ')
            && !line.starts_with('\t')
        {
            let text = &trimmed[2..];
            let snippet: String = text.chars().take(60).collect();
            if !snippet.trim().is_empty() {
                let id = format!("BULLET:{}", snippet.trim());
                if seen.insert(id.clone()) {
                    atomics.push(id);
                }
            }
        }
    }

    atomics
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extract_headings_and_bullets() {
        let content = "# Top\n## Section A\n### Subsection\n- bullet one\n- bullet two\n";
        let atomics = extract_atomics(content);
        assert!(atomics.iter().any(|a| a == "H1:Top"));
        assert!(atomics.iter().any(|a| a == "H2:Section A"));
        assert!(atomics.iter().any(|a| a == "H3:Subsection"));
        assert!(atomics.iter().any(|a| a.starts_with("BULLET:bullet one")));
    }

    #[test]
    fn sub_bullets_are_ignored() {
        let content = "- top level\n  - sub level should not appear\n";
        let atomics = extract_atomics(content);
        assert!(atomics.iter().any(|a| a.starts_with("BULLET:top level")));
        assert!(!atomics.iter().any(|a| a.contains("sub level")));
    }

    #[test]
    fn empty_content_yields_no_atomics() {
        let atomics = extract_atomics("");
        assert!(atomics.is_empty());
    }

    #[test]
    fn lossless_gate_passes_when_all_mapped() {
        let mut ledger = CoverageLedger::new();
        let src = PathBuf::from("rules/deny.md");
        let content = "## Deny patterns\n- No rm -rf\n";
        ledger.add_file(&src, content);
        ledger.mark_mapped(&src, Path::new("rules/deny.md"));
        let summary = ledger.summarize();
        assert!(summary.is_lossless, "all mapped → lossless");
        assert_eq!(summary.unmapped, 0);
    }

    #[test]
    fn lossless_gate_fails_when_unmapped_remain() {
        let mut ledger = CoverageLedger::new();
        let src = PathBuf::from("rules/deny.md");
        let content = "## Deny patterns\n- No rm -rf\n";
        ledger.add_file(&src, content);
        // Do NOT call mark_mapped — rows stay Unmapped.
        let summary = ledger.summarize();
        assert!(!summary.is_lossless, "unmapped rows → not lossless");
        assert!(summary.unmapped > 0);
    }

    #[test]
    fn excluded_files_are_dropped() {
        let mut ledger = CoverageLedger::new();
        let src = PathBuf::from("vault.env");
        ledger.add_file(&src, "SECRET=xyz");
        let summary = ledger.summarize();
        assert_eq!(summary.total, 0, "excluded files do not count toward total");
        assert_eq!(summary.dropped, 1);
        assert!(summary.is_lossless, "dropped-only → lossless");
    }

    #[test]
    fn coverage_pct_calculation() {
        let mut ledger = CoverageLedger::new();
        let src1 = PathBuf::from("a.md");
        let src2 = PathBuf::from("b.md");
        ledger.add_file(&src1, "## Section One\n");
        ledger.add_file(&src2, "## Section Two\n");
        ledger.mark_mapped(&src1, Path::new("a.md"));
        // src2 unmapped
        let summary = ledger.summarize();
        assert!(summary.coverage_pct < 100.0);
        assert!(summary.coverage_pct > 0.0);
    }

    #[test]
    fn dedup_prevents_duplicate_atomics() {
        let content = "## Same Section\n## Same Section\n";
        let atomics = extract_atomics(content);
        let count = atomics.iter().filter(|a| *a == "H2:Same Section").count();
        assert_eq!(count, 1, "duplicate headings must be deduplicated");
    }

    #[test]
    fn is_excluded_matches_vault_env() {
        assert!(is_excluded(Path::new("vault.env")));
        assert!(is_excluded(Path::new("secrets.key")));
        assert!(!is_excluded(Path::new("rules/deny.md")));
    }
}
