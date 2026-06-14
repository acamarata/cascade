//! EIE check implementations: FileSizeCheck and DuplicationCheck.
//!
//! # Purpose
//! Defines the [`Check`] trait and v1 implementations. New checks slot in by
//! implementing `Check` and registering in [`crate::eie::runner::all_checks`].
//!
//! # Inputs
//! Each check receives a slice of `(relative_path, line_count, lines)` tuples
//! produced by the file walker in the runner.
//!
//! # Constraints
//! - No external crate dependencies beyond `std`.
//! - DuplicationCheck uses a simple `DefaultHasher`-based rolling window hash
//!   (fast heuristic; not cryptographic).

use std::collections::{HashMap, HashSet};
use std::hash::{DefaultHasher, Hash, Hasher};

use super::report::{DuplicationIssue, FileSizeIssue, HealthReport};

/// A single EIE health check.
///
/// Implement this trait to add a new check. Register it in
/// [`crate::eie::runner::all_checks`].
pub trait Check: Send + Sync {
    /// Human-readable name for this check.
    fn name(&self) -> &'static str;

    /// Run the check against the collected file data.
    ///
    /// # Inputs
    /// - `files`: slice of `(relative_path, line_count, lines)`.
    /// - `report`: mutable report to append issues into.
    fn run(&self, files: &[FileEntry], report: &mut HealthReport);
}

/// A single source file entry passed to each check.
pub struct FileEntry {
    /// Path relative to the scanned root.
    pub rel_path: String,
    /// Total line count.
    pub line_count: usize,
    /// Raw lines (without trailing newline).
    pub lines: Vec<String>,
}

// ── FileSizeCheck ────────────────────────────────────────────────────────────

/// Flags source files exceeding a configurable line cap.
///
/// # Purpose
/// Enforces the repo standard (default 300 lines/file per engineering-standard.md).
/// Reports the worst offenders sorted descending by overage.
///
/// # Inputs
/// - `cap`: maximum allowed lines per file.
///
/// # Outputs
/// Appends [`FileSizeIssue`] entries to `report.file_size_issues`.
pub struct FileSizeCheck {
    /// Line cap — default 300.
    pub cap: usize,
}

impl Default for FileSizeCheck {
    fn default() -> Self {
        Self { cap: 300 }
    }
}

impl Check for FileSizeCheck {
    fn name(&self) -> &'static str {
        "file_size"
    }

    fn run(&self, files: &[FileEntry], report: &mut HealthReport) {
        let mut issues: Vec<FileSizeIssue> = files
            .iter()
            .filter(|f| f.line_count > self.cap)
            .map(|f| FileSizeIssue {
                path: f.rel_path.clone(),
                lines: f.line_count,
                cap: self.cap,
            })
            .collect();
        // Worst offenders first.
        issues.sort_by_key(|b| std::cmp::Reverse(b.lines));
        report.file_size_issues.extend(issues);
    }
}

// ── DuplicationCheck ─────────────────────────────────────────────────────────

/// Detects near-duplicate blocks via rolling-hash over N-line windows.
///
/// # Purpose
/// A cheap heuristic clone-detection pass. For each file it computes hashes of
/// every consecutive `window_size`-line block, then flags any two distinct
/// files that share `min_shared` or more identical window hashes.
///
/// # Inputs
/// - `window_size`: number of consecutive lines per window (default 5).
/// - `min_shared`: minimum shared windows to report (default 3).
///
/// # Outputs
/// Appends [`DuplicationIssue`] entries to `report.duplication_issues`.
///
/// # Constraints
/// Uses `std::hash::DefaultHasher` — fast, not collision-resistant. Suitable
/// for heuristic detection only.
pub struct DuplicationCheck {
    /// Sliding window size in lines.
    pub window_size: usize,
    /// Minimum shared windows to flag as a duplication issue.
    pub min_shared: usize,
}

impl Default for DuplicationCheck {
    fn default() -> Self {
        Self {
            window_size: 5,
            min_shared: 3,
        }
    }
}

impl Check for DuplicationCheck {
    fn name(&self) -> &'static str {
        "duplication"
    }

    fn run(&self, files: &[FileEntry], report: &mut HealthReport) {
        // Build a map: window_hash -> set of file indices that contain it.
        let mut hash_to_files: HashMap<u64, Vec<usize>> = HashMap::new();

        for (idx, entry) in files.iter().enumerate() {
            // Skip files shorter than one full window.
            if entry.lines.len() < self.window_size {
                continue;
            }
            let hashes = window_hashes(&entry.lines, self.window_size);
            // Deduplicate within the same file (avoid counting repeated blocks
            // in one file multiple times).
            let unique: HashSet<u64> = hashes.into_iter().collect();
            for h in unique {
                hash_to_files.entry(h).or_default().push(idx);
            }
        }

        // For each pair of files, count shared hashes.
        let mut pair_counts: HashMap<(usize, usize), usize> = HashMap::new();
        for file_list in hash_to_files.values() {
            if file_list.len() < 2 {
                continue;
            }
            // Only consider pairs (no triple counting), keep order canonical.
            for i in 0..file_list.len() {
                for j in (i + 1)..file_list.len() {
                    let a = file_list[i].min(file_list[j]);
                    let b = file_list[i].max(file_list[j]);
                    *pair_counts.entry((a, b)).or_insert(0) += 1;
                }
            }
        }

        let mut issues: Vec<DuplicationIssue> = pair_counts
            .into_iter()
            .filter(|(_, count)| *count >= self.min_shared)
            .map(|((a, b), shared_windows)| DuplicationIssue {
                file_a: files[a].rel_path.clone(),
                file_b: files[b].rel_path.clone(),
                shared_windows,
            })
            .collect();
        // Most-shared first.
        issues.sort_by_key(|b| std::cmp::Reverse(b.shared_windows));
        report.duplication_issues.extend(issues);
    }
}

// ── helpers ──────────────────────────────────────────────────────────────────

/// Compute rolling hashes for all `window_size`-line windows in `lines`.
fn window_hashes(lines: &[String], window_size: usize) -> Vec<u64> {
    lines
        .windows(window_size)
        .map(|window| {
            let mut h = DefaultHasher::new();
            for line in window {
                // Trim whitespace so indentation differences don't hide duplicates.
                line.trim().hash(&mut h);
            }
            h.finish()
        })
        .collect()
}

// ── tests ────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn make_entry(path: &str, lines: Vec<&str>) -> FileEntry {
        FileEntry {
            rel_path: path.to_string(),
            line_count: lines.len(),
            lines: lines.into_iter().map(|s| s.to_string()).collect(),
        }
    }

    fn empty_report() -> HealthReport {
        HealthReport {
            score: 100,
            file_size_issues: vec![],
            duplication_issues: vec![],
            summary: String::new(),
        }
    }

    // ── FileSizeCheck ────────────────────────────────────────────────────────

    #[test]
    fn file_size_check_flags_over_cap() {
        let check = FileSizeCheck { cap: 5 };
        let over = make_entry("big.rs", vec!["a"; 10]);
        let under = make_entry("small.rs", vec!["a"; 3]);
        let mut report = empty_report();
        check.run(&[over, under], &mut report);
        assert_eq!(report.file_size_issues.len(), 1);
        assert_eq!(report.file_size_issues[0].path, "big.rs");
        assert_eq!(report.file_size_issues[0].lines, 10);
        assert_eq!(report.file_size_issues[0].cap, 5);
    }

    #[test]
    fn file_size_check_clean_tree_no_issues() {
        let check = FileSizeCheck { cap: 300 };
        let files: Vec<FileEntry> = (0..5)
            .map(|i| make_entry(&format!("file{i}.rs"), vec!["x"; 50]))
            .collect();
        let mut report = empty_report();
        check.run(&files, &mut report);
        assert!(report.file_size_issues.is_empty());
    }

    #[test]
    fn file_size_check_worst_offenders_sorted() {
        let check = FileSizeCheck { cap: 2 };
        let files = vec![
            make_entry("medium.rs", vec!["x"; 10]),
            make_entry("huge.rs", vec!["x"; 50]),
            make_entry("large.rs", vec!["x"; 30]),
        ];
        let mut report = empty_report();
        check.run(&files, &mut report);
        assert_eq!(report.file_size_issues[0].path, "huge.rs");
        assert_eq!(report.file_size_issues[1].path, "large.rs");
        assert_eq!(report.file_size_issues[2].path, "medium.rs");
    }

    // ── DuplicationCheck ─────────────────────────────────────────────────────

    #[test]
    fn duplication_check_flags_shared_windows() {
        let shared_block: Vec<&str> = vec!["fn foo() {", "let x = 1;", "let y = 2;", "x + y", "}"];
        let check = DuplicationCheck {
            window_size: 5,
            min_shared: 1,
        };
        let file_a = make_entry("a.rs", shared_block.clone());
        let file_b = make_entry("b.rs", shared_block.clone());
        let mut report = empty_report();
        check.run(&[file_a, file_b], &mut report);
        assert_eq!(report.duplication_issues.len(), 1);
        assert!(report.duplication_issues[0].shared_windows >= 1);
    }

    #[test]
    fn duplication_check_clean_tree_no_issues() {
        let check = DuplicationCheck {
            window_size: 5,
            min_shared: 1,
        };
        let file_a = make_entry(
            "a.rs",
            vec!["line_a1", "line_a2", "line_a3", "line_a4", "line_a5"],
        );
        let file_b = make_entry(
            "b.rs",
            vec!["line_b1", "line_b2", "line_b3", "line_b4", "line_b5"],
        );
        let mut report = empty_report();
        check.run(&[file_a, file_b], &mut report);
        assert!(report.duplication_issues.is_empty());
    }

    // ── Check trait ──────────────────────────────────────────────────────────

    #[test]
    fn check_trait_name_returns_correct_strings() {
        let fs_check = FileSizeCheck::default();
        let dup_check = DuplicationCheck::default();
        assert_eq!(fs_check.name(), "file_size");
        assert_eq!(dup_check.name(), "duplication");
    }

    // ── score rubric ─────────────────────────────────────────────────────────

    #[test]
    fn health_report_score_zero_issues_is_100() {
        assert_eq!(HealthReport::compute_score(0, 0), 100);
    }

    #[test]
    fn health_report_score_saturates_at_zero() {
        // Many issues should not underflow.
        assert_eq!(HealthReport::compute_score(100, 100), 0);
    }
}
