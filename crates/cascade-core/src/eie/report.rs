//! Health report types for EIE checks.
//!
//! # Purpose
//! Defines the shared output structures for all EIE checks.
//! `HealthReport` is the top-level result returned by [`crate::eie::runner::run_health_check`].
//!
//! # Outputs
//! - `score`: 0–100 simple rubric (100 = clean, deducted per issue category).
//! - `file_size_issues`: files exceeding the line cap, sorted worst-first.
//! - `duplication_issues`: file pairs sharing identical line-window hashes.
//! - `summary`: human-readable one-liner.

use serde::{Deserialize, Serialize};

/// A single file that exceeds the configured line-count threshold.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct FileSizeIssue {
    /// Path relative to the scanned root.
    pub path: String,
    /// Actual line count.
    pub lines: usize,
    /// Configured cap that was exceeded.
    pub cap: usize,
}

/// A pair of files that share identical rolling-hash windows (near-duplicate blocks).
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct DuplicationIssue {
    /// First file (relative path).
    pub file_a: String,
    /// Second file (relative path).
    pub file_b: String,
    /// Number of shared window hashes detected.
    pub shared_windows: usize,
}

/// Top-level report produced by the EIE health runner.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HealthReport {
    /// Overall score 0–100. 100 = no issues found.
    pub score: u8,
    /// Files exceeding the line-count cap.
    pub file_size_issues: Vec<FileSizeIssue>,
    /// File pairs with near-duplicate blocks.
    pub duplication_issues: Vec<DuplicationIssue>,
    /// Short human-readable summary.
    pub summary: String,
}

impl HealthReport {
    /// Compute a simple score from issue counts.
    ///
    /// Rubric (capped at 0):
    /// - Each file-size issue deducts 5 points.
    /// - Each duplication issue deducts 8 points.
    pub fn compute_score(file_size_count: usize, duplication_count: usize) -> u8 {
        let deduction = ((file_size_count * 5) + (duplication_count * 8)) as u32;
        100u32.saturating_sub(deduction) as u8
    }
}
