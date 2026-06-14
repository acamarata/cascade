//! EIE health-check runner: file walker + check dispatcher.
//!
//! # Purpose
//! Walks a source tree (respecting `.gitignore` and skip-dirs), collects
//! per-file metadata, runs all registered checks, and builds a [`HealthReport`].
//!
//! # Inputs
//! - `root: &Path` — directory to scan.
//! - `config: &HealthConfig` — thresholds and options.
//!
//! # Outputs
//! [`HealthReport`] with score, issues, and summary.
//!
//! # Constraints
//! - Uses the `ignore` crate for `.gitignore` awareness (already in cascade-core deps).
//! - Skips: `target/`, `node_modules/`, `.git/`, `dist/` at any depth.
//! - Only processes source-code extensions by language cap table.

use std::path::Path;

use ignore::WalkBuilder;

use super::checks::{Check, DuplicationCheck, FileEntry, FileSizeCheck};
use super::report::HealthReport;

/// Tunable thresholds for the EIE health runner.
#[derive(Debug, Clone)]
pub struct HealthConfig {
    /// Maximum allowed lines per source file. Default 300.
    pub max_file_lines: usize,
    /// Sliding window size (lines) for duplication hashing. Default 5.
    pub dup_window_size: usize,
    /// Minimum shared windows to flag as duplication. Default 3.
    pub dup_min_shared: usize,
}

impl Default for HealthConfig {
    fn default() -> Self {
        Self {
            max_file_lines: 300,
            dup_window_size: 5,
            dup_min_shared: 3,
        }
    }
}

/// Source-file extensions we analyse (by language).
static SOURCE_EXTENSIONS: &[&str] = &[
    "rs", "ts", "tsx", "js", "jsx", "py", "go", "java", "kt", "swift", "dart",
    "cpp", "cc", "c", "h", "hpp", "cs", "rb", "php",
];

/// Directory names always skipped regardless of `.gitignore`.
static SKIP_DIRS: &[&str] = &["target", "node_modules", ".git", "dist", ".cache", "build"];

/// Build and return the list of checks enabled for v1.
///
/// # Purpose
/// Central registry — add new checks here as they are implemented.
/// The runner calls each check in order.
pub fn all_checks(config: &HealthConfig) -> Vec<Box<dyn Check>> {
    vec![
        Box::new(FileSizeCheck {
            cap: config.max_file_lines,
        }),
        Box::new(DuplicationCheck {
            window_size: config.dup_window_size,
            min_shared: config.dup_min_shared,
        }),
    ]
}

/// Walk `root`, collect source files, run all checks, return a [`HealthReport`].
///
/// # Inputs
/// - `root`: path to the project root.
/// - `config`: thresholds and options.
///
/// # Outputs
/// [`HealthReport`] — never returns `Err`; individual unreadable files are skipped.
pub fn run_health_check(root: &Path, config: &HealthConfig) -> HealthReport {
    let entries = collect_files(root);
    let checks = all_checks(config);

    let mut report = HealthReport {
        score: 100,
        file_size_issues: vec![],
        duplication_issues: vec![],
        summary: String::new(),
    };

    for check in &checks {
        check.run(&entries, &mut report);
    }

    let score = HealthReport::compute_score(
        report.file_size_issues.len(),
        report.duplication_issues.len(),
    );
    report.score = score;
    report.summary = build_summary(&report);
    report
}

// ── file walker ──────────────────────────────────────────────────────────────

fn collect_files(root: &Path) -> Vec<FileEntry> {
    let mut entries = Vec::new();

    let mut builder = WalkBuilder::new(root);
    builder
        .hidden(false)
        .follow_links(false)
        .git_ignore(true)
        .git_global(false)
        .git_exclude(false);

    // Explicitly add the root .gitignore so it is honoured even when
    // the root is not inside a git repository (e.g. temp dirs in tests).
    let root_gitignore = root.join(".gitignore");
    if root_gitignore.exists() {
        builder.add_ignore(&root_gitignore);
    }

    let walker = builder.build();

    for result in walker {
        let dir_entry = match result {
            Ok(e) => e,
            Err(_) => continue,
        };

        // Files only.
        let ft = match dir_entry.file_type() {
            Some(ft) => ft,
            None => continue,
        };
        if !ft.is_file() {
            continue;
        }

        let path = dir_entry.path();

        // Skip-dir gate: any path component in SKIP_DIRS is excluded.
        let rel_for_skip = path.strip_prefix(root).unwrap_or(path);
        let in_skip_dir = rel_for_skip.components().any(|c| {
            let name = c.as_os_str().to_string_lossy();
            SKIP_DIRS.contains(&name.as_ref())
        });
        if in_skip_dir {
            continue;
        }

        // Extension gate.
        let ext = match path.extension().and_then(|e| e.to_str()) {
            Some(e) => e,
            None => continue,
        };
        if !SOURCE_EXTENSIONS.contains(&ext) {
            continue;
        }

        // Read lines; skip unreadable files silently.
        let content = match std::fs::read_to_string(path) {
            Ok(c) => c,
            Err(_) => continue,
        };

        let lines: Vec<String> = content.lines().map(|l| l.to_string()).collect();
        let line_count = lines.len();

        let rel_path = path
            .strip_prefix(root)
            .unwrap_or(path)
            .to_string_lossy()
            .to_string();

        entries.push(FileEntry {
            rel_path,
            line_count,
            lines,
        });
    }

    entries
}

// ── summary ───────────────────────────────────────────────────────────────────

fn build_summary(report: &HealthReport) -> String {
    let fs = report.file_size_issues.len();
    let dup = report.duplication_issues.len();

    if fs == 0 && dup == 0 {
        format!("Score {}/100 — no issues found", report.score)
    } else {
        let mut parts = Vec::new();
        if fs > 0 {
            parts.push(format!("{fs} oversized file{}", if fs == 1 { "" } else { "s" }));
        }
        if dup > 0 {
            parts.push(format!(
                "{dup} duplication pair{}",
                if dup == 1 { "" } else { "s" }
            ));
        }
        format!("Score {}/100 — {}", report.score, parts.join(", "))
    }
}

// ── tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    fn write_file(dir: &Path, name: &str, content: &str) {
        let path = dir.join(name);
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).unwrap();
        }
        fs::write(path, content).unwrap();
    }

    fn lines(n: usize) -> String {
        (0..n).map(|i| format!("line_{i}\n")).collect()
    }

    // ── file-size ─────────────────────────────────────────────────────────────

    #[test]
    fn runner_file_size_flags_over_cap() {
        let tmp = TempDir::new().unwrap();
        write_file(tmp.path(), "big.rs", &lines(400));
        write_file(tmp.path(), "small.rs", &lines(10));

        let config = HealthConfig {
            max_file_lines: 300,
            ..Default::default()
        };
        let report = run_health_check(tmp.path(), &config);
        assert_eq!(report.file_size_issues.len(), 1);
        assert_eq!(report.file_size_issues[0].path, "big.rs");
    }

    // ── duplication ───────────────────────────────────────────────────────────

    #[test]
    fn runner_duplication_flags_shared_windows() {
        let tmp = TempDir::new().unwrap();
        // 10-line shared block repeated in two files.
        let shared = "fn foo() {\nlet x = 1;\nlet y = 2;\nlet z = 3;\nreturn x + y + z;\n}\nfn bar() {\nprintln!(\"hello\");\nlet a = 42;\na\n";
        write_file(tmp.path(), "a.rs", shared);
        write_file(tmp.path(), "b.rs", shared);

        let config = HealthConfig {
            dup_window_size: 5,
            dup_min_shared: 1,
            ..Default::default()
        };
        let report = run_health_check(tmp.path(), &config);
        assert!(!report.duplication_issues.is_empty());
    }

    // ── clean tree ────────────────────────────────────────────────────────────

    #[test]
    fn runner_clean_tree_high_score_no_issues() {
        let tmp = TempDir::new().unwrap();
        // Use file-unique prefixes so rolling windows never match between files.
        let main_content: String = (0..50).map(|i| format!("main_unique_line_{i}\n")).collect();
        let lib_content: String = (0..30).map(|i| format!("lib_unique_line_{i}\n")).collect();
        write_file(tmp.path(), "main.rs", &main_content);
        write_file(tmp.path(), "lib.rs", &lib_content);

        let config = HealthConfig::default();
        let report = run_health_check(tmp.path(), &config);
        assert!(report.file_size_issues.is_empty());
        assert!(report.duplication_issues.is_empty());
        assert_eq!(report.score, 100);
    }

    // ── skip dirs ─────────────────────────────────────────────────────────────

    #[test]
    fn runner_skips_target_and_node_modules() {
        let tmp = TempDir::new().unwrap();
        // A huge file in a skipped dir should not appear in issues.
        let big: String = (0..1000).map(|i| format!("target_line_{i}\n")).collect();
        let big2: String = (0..1000).map(|i| format!("node_line_{i}\n")).collect();
        write_file(tmp.path(), "target/debug/big.rs", &big);
        write_file(tmp.path(), "node_modules/lodash/index.js", &big2);
        // Real source is clean.
        let small: String = (0..20).map(|i| format!("src_unique_{i}\n")).collect();
        write_file(tmp.path(), "src/main.rs", &small);

        let config = HealthConfig::default();
        let report = run_health_check(tmp.path(), &config);
        assert!(report.file_size_issues.is_empty());
    }

    // ── gitignore ─────────────────────────────────────────────────────────────

    #[test]
    fn runner_respects_gitignore() {
        let tmp = TempDir::new().unwrap();
        // Write a .gitignore that ignores generated/.
        write_file(tmp.path(), ".gitignore", "generated/\n");
        write_file(tmp.path(), "generated/huge.rs", &lines(500));
        write_file(tmp.path(), "src/real.rs", &lines(20));

        let config = HealthConfig::default();
        let report = run_health_check(tmp.path(), &config);
        assert!(report.file_size_issues.is_empty());
    }

    // ── JSON shape ────────────────────────────────────────────────────────────

    #[test]
    fn runner_report_serialises_to_expected_json_shape() {
        let tmp = TempDir::new().unwrap();
        write_file(tmp.path(), "big.rs", &lines(400));

        let config = HealthConfig {
            max_file_lines: 300,
            ..Default::default()
        };
        let report = run_health_check(tmp.path(), &config);
        let json = serde_json::to_string(&report).unwrap();
        let val: serde_json::Value = serde_json::from_str(&json).unwrap();

        // Must have the required top-level keys.
        assert!(val.get("score").is_some());
        assert!(val.get("file_size_issues").is_some());
        assert!(val.get("duplication_issues").is_some());
        assert!(val.get("summary").is_some());
    }
}
