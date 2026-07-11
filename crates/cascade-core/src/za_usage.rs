//! Za (z.ai GLM Coding Plan) local usage tracking.
//!
//! Purpose: Since the Za account exposes no usage API, track dispatched prompts
//! locally in a rolling JSONL log (`~/.cascade/za-usage.jsonl`). The daemon
//! reads this file to build a synthetic five-hour utilization for the widget.
//!
//! Inputs:  `~/.cascade/za-usage.jsonl` — one JSON record per line.
//! Outputs: `append_record` appends; `read_window` returns a rolling summary.
//!
//! Constraints:
//!   - All I/O is best-effort: errors are silently ignored so Za usage tracking
//!     never interrupts a dispatch.
//!   - `read_window` prunes records older than 24 h to keep the file bounded.
//!   - No `unwrap()` outside `#[cfg(test)]` blocks.

use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

use serde::{Deserialize, Serialize};

// ── Types ─────────────────────────────────────────────────────────────────────

/// One dispatched-prompt record written to `za-usage.jsonl`.
///
/// Purpose: append-only audit log; one line per Za dispatch so the daemon can
/// reconstruct a rolling window without any shared state.
/// Inputs:  filled by the CLI conductor after each successful Za dispatch.
/// Outputs: serialised as a single JSON line.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ZaRecord {
    /// Unix timestamp (seconds) of the dispatch.
    pub ts: u64,
    /// Account id (e.g. `"zai-acc1"`).
    pub account: String,
    /// Number of prompts in this record (always 1 per dispatch).
    pub prompts: u32,
    /// Estimated input tokens: `prompt_text.len() / 4`.
    pub est_input_tokens: u64,
    /// Estimated output tokens: `reply_text.len() / 4`.
    pub est_output_tokens: u64,
}

/// Summary of Za activity inside a rolling window returned by `read_window`.
///
/// Purpose: let the daemon compute utilization without re-parsing the log file
/// again after `read_window` has already done the work.
/// Outputs: fields are ready to write directly into a `five_hour` usage slot.
#[derive(Debug, Clone)]
pub struct ZaWindowSummary {
    /// Total prompts dispatched within the window.
    pub used_prompts: u32,
    /// Estimated input tokens summed across window records.
    pub est_input_tokens: u64,
    /// Estimated output tokens summed across window records.
    pub est_output_tokens: u64,
    /// Timestamp of the oldest record inside the window; `None` when empty.
    pub oldest_ts_in_window: Option<u64>,
}

// ── Writer ────────────────────────────────────────────────────────────────────

/// Append one `ZaRecord` to `path` as a JSON line.
///
/// Purpose: called by the CLI conductor after a successful Za dispatch.
/// Inputs:  `path` — absolute path to `~/.cascade/za-usage.jsonl`;
///          `record` — the record to append.
/// Outputs: best-effort append; silently ignores any I/O error.
/// Constraints: opens in append mode; never truncates the file.
pub fn append_record(path: &Path, record: &ZaRecord) {
    use std::io::Write;

    let line = match serde_json::to_string(record) {
        Ok(s) => s,
        Err(_) => return,
    };

    let Ok(mut file) = std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(path)
    else {
        return;
    };

    // Best-effort: ignore write errors.
    let _ = writeln!(file, "{line}");
}

// ── Reader ────────────────────────────────────────────────────────────────────

/// Read `path`, filter records within `window_secs` from now, and return a
/// summary. Prunes records older than 24 h as a side-effect (best-effort).
///
/// Purpose: called by the daemon fleet poller to synthesise a five-hour
/// utilization window for Za accounts from the local audit log.
/// Inputs:  `path` — absolute path to `~/.cascade/za-usage.jsonl`;
///          `window_secs` — rolling window length in seconds (e.g. 5 * 3600).
/// Outputs: `Some(ZaWindowSummary)` when the file is readable and non-empty;
///          `None` when the file is absent, empty, or entirely unreadable.
/// Constraints: best-effort pruning — failure to rewrite leaves the file intact.
pub fn read_window(path: &Path, window_secs: u64) -> Option<ZaWindowSummary> {
    let content = std::fs::read_to_string(path).ok()?;
    if content.trim().is_empty() {
        return None;
    }

    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);

    let cutoff_24h = now.saturating_sub(24 * 3600);

    let mut all_records: Vec<ZaRecord> = Vec::new();
    for line in content.lines() {
        let trimmed = line.trim();
        if trimmed.is_empty() {
            continue;
        }
        if let Ok(rec) = serde_json::from_str::<ZaRecord>(trimmed) {
            all_records.push(rec);
        }
    }

    if all_records.is_empty() {
        return None;
    }

    // Prune: rewrite file keeping only records from the last 24 h (best-effort).
    let keep: Vec<&ZaRecord> = all_records.iter().filter(|r| r.ts >= cutoff_24h).collect();
    if keep.len() < all_records.len() {
        // Some records are stale — rewrite keeping only recent ones.
        try_rewrite(path, &keep);
    }

    // Filter to the requested window.
    let window_cutoff = now.saturating_sub(window_secs);
    let in_window: Vec<&ZaRecord> = all_records
        .iter()
        .filter(|r| r.ts >= window_cutoff)
        .collect();

    if in_window.is_empty() {
        return None;
    }

    let used_prompts: u32 = in_window.iter().map(|r| r.prompts).sum();
    let est_input_tokens: u64 = in_window.iter().map(|r| r.est_input_tokens).sum();
    let est_output_tokens: u64 = in_window.iter().map(|r| r.est_output_tokens).sum();
    let oldest_ts_in_window = in_window.iter().map(|r| r.ts).min();

    Some(ZaWindowSummary {
        used_prompts,
        est_input_tokens,
        est_output_tokens,
        oldest_ts_in_window,
    })
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Rewrite `path` with only `records`, using an atomic tmp-rename.
/// Best-effort — silently ignores any I/O error.
fn try_rewrite(path: &Path, records: &[&ZaRecord]) {
    use std::io::Write;

    let tmp = path.with_extension("tmp");
    let Ok(mut file) = std::fs::File::create(&tmp) else {
        return;
    };
    for rec in records {
        if let Ok(line) = serde_json::to_string(rec) {
            let _ = writeln!(file, "{line}");
        }
    }
    drop(file);
    let _ = std::fs::rename(&tmp, path);
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn now_secs() -> u64 {
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs()
    }

    #[test]
    fn test_count_in_window() {
        // 3 records inside 5h window + 1 record 6h old.
        let now = now_secs();
        let records = vec![
            ZaRecord {
                ts: now - 100,
                account: "za1".into(),
                prompts: 1,
                est_input_tokens: 10,
                est_output_tokens: 5,
            },
            ZaRecord {
                ts: now - 200,
                account: "za1".into(),
                prompts: 1,
                est_input_tokens: 10,
                est_output_tokens: 5,
            },
            ZaRecord {
                ts: now - 300,
                account: "za1".into(),
                prompts: 1,
                est_input_tokens: 10,
                est_output_tokens: 5,
            },
            ZaRecord {
                ts: now - 6 * 3600,
                account: "za1".into(),
                prompts: 1,
                est_input_tokens: 10,
                est_output_tokens: 5,
            },
        ];
        let window_secs = 5 * 3600u64;
        let in_window: Vec<_> = records
            .iter()
            .filter(|r| now.saturating_sub(r.ts) < window_secs)
            .collect();
        assert_eq!(in_window.len(), 3);
    }

    #[test]
    fn test_utilization() {
        let used = 60u32;
        let cap = 120u32;
        let utilization = (100.0 * used as f64 / cap as f64).min(100.0);
        assert_eq!(utilization, 50.0);
    }

    #[test]
    fn test_absent_file_returns_none() {
        let path = std::path::PathBuf::from("/tmp/nonexistent-za-usage-test-xyz.jsonl");
        let result = read_window(&path, 5 * 3600);
        assert!(result.is_none());
    }

    #[test]
    fn test_append_and_read_round_trip() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("za-usage.jsonl");
        let now = now_secs();

        let rec = ZaRecord {
            ts: now,
            account: "zai-acc1".into(),
            prompts: 1,
            est_input_tokens: 25,
            est_output_tokens: 10,
        };
        append_record(&path, &rec);

        let summary = read_window(&path, 5 * 3600).expect("should have summary");
        assert_eq!(summary.used_prompts, 1);
        assert_eq!(summary.est_input_tokens, 25);
        assert_eq!(summary.est_output_tokens, 10);
        assert_eq!(summary.oldest_ts_in_window, Some(now));
    }

    #[test]
    fn test_old_records_excluded_from_window() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("za-usage.jsonl");
        let now = now_secs();

        // One record inside 5h window, one 6h old.
        append_record(
            &path,
            &ZaRecord {
                ts: now - 100,
                account: "za1".into(),
                prompts: 1,
                est_input_tokens: 10,
                est_output_tokens: 5,
            },
        );
        append_record(
            &path,
            &ZaRecord {
                ts: now - 6 * 3600,
                account: "za1".into(),
                prompts: 1,
                est_input_tokens: 10,
                est_output_tokens: 5,
            },
        );

        let summary = read_window(&path, 5 * 3600).expect("should have summary");
        assert_eq!(
            summary.used_prompts, 1,
            "only the recent record should count"
        );
    }
}
