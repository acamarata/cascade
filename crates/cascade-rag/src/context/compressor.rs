//! # context::compressor
//!
//! `ContextOptimizer` — stateless compression and token-budgeting pipeline for
//! RAG context slices served to AI harnesses.
//!
//! ## Purpose
//! Reduce token cost of context before it is forwarded to a downstream LLM by
//! running three passes in sequence:
//!   1. **within-window dedup** (SHA-256 of normalised chunk text)
//!   2. **sliding token window** (greedy top-K by score within `budget`)
//!   3. **shell-output compression** (ANSI strip + line-fold + head/tail trim)
//!
//! Cross-session dedup (T-P4-E04-21) is layered on top via
//! [`ContextOptimizer::cross_session_dedup`] and
//! [`ContextOptimizer::record_delivered`], which require a live SQLite
//! connection.
//!
//! ## Inputs
//! - `chunks: Vec<RetrievalHit>` — ordered by `score` descending (RRF output).
//! - `shell_snippets: &[String]` — raw stdout/stderr blocks to compress.
//! - `budget: usize` — maximum estimated tokens to include.
//!
//! ## Outputs
//! [`ContextResult`] containing the selected chunks, compressed shell snippets,
//! and `tokens_used`.
//!
//! ## Constraints
//! - Fully synchronous; no async state.
//! - Does NOT own a DB connection — cross-session logic is an opt-in layer.
//! - Token estimate: `words * 1.33` (cheap approximation, ±15%).
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-rag/context/compressor

use std::collections::HashSet;

use cascade_types::RetrievalHit;
use sha2::{Digest, Sha256};

// ── ContextResult ─────────────────────────────────────────────────────────────

/// Result of a full context-optimization pass.
///
/// `tokens_used` reflects the estimated token count for the included chunks
/// only (shell snippets are compressed but their token cost is not charged
/// against the budget to avoid over-constraining short prompts).
#[derive(Debug, Clone)]
pub struct ContextResult {
    /// Chunks selected after dedup + windowing.
    pub chunks: Vec<RetrievalHit>,

    /// Shell snippets after ANSI-strip + fold + head/tail truncation.
    pub shell_snippets: Vec<String>,

    /// Estimated token count for the included chunks (not shell snippets).
    pub tokens_used: usize,
}

// ── ContextOptimizer ─────────────────────────────────────────────────────────

/// Stateless context compression and budgeting pipeline.
///
/// ## Example
/// ```rust
/// use cascade_rag::context::ContextOptimizer;
/// let opt = ContextOptimizer::new(4096);
/// let result = opt.optimize(vec![], &[]);
/// assert_eq!(result.tokens_used, 0);
/// ```
pub struct ContextOptimizer {
    /// Maximum estimated tokens to include in the output window.
    pub budget: usize,
}

impl ContextOptimizer {
    /// Create a new optimizer with the given token budget.
    ///
    /// # Panics
    /// Panics if `budget == 0` (nonsensical value).
    pub fn new(budget: usize) -> Self {
        assert!(budget > 0, "ContextOptimizer budget must be > 0");
        Self { budget }
    }

    // ── Shell compression ─────────────────────────────────────────────────────

    /// Compress a raw shell-output string.
    ///
    /// Passes applied in order:
    /// 1. Strip ANSI escape sequences (`ESC[…m` and cursor-move variants).
    /// 2. Fold consecutive identical lines: replace N copies with `… (×N)`.
    /// 3. If the result exceeds 80 lines, truncate to head(20) + tail(20) with
    ///    a middle-omission notice.
    ///
    /// # Purpose
    /// A typical `cargo build` output is 200+ lines with repeated `Compiling …`
    /// entries and ANSI colour codes. This reduces it to ≤80 clean lines,
    /// cutting token cost by ≥60%.
    pub fn compress_shell(&self, raw: &str) -> String {
        // Pass 1: strip ANSI escape codes.
        let stripped = strip_ansi(raw);

        // Pass 2: fold consecutive duplicate lines.
        let folded = fold_duplicate_lines(&stripped);

        // Pass 3: truncate if > 80 lines.
        let lines: Vec<&str> = folded.lines().collect();
        if lines.len() <= 80 {
            return folded;
        }

        // Keep head(20) + tail(20); insert omission notice in between.
        let head: Vec<&str> = lines[..20].to_vec();
        let tail: Vec<&str> = lines[lines.len() - 20..].to_vec();
        let omitted = lines.len() - 40;
        let mut out = head.join("\n");
        out.push_str(&format!("\n… [{omitted} lines omitted] …\n"));
        out.push_str(&tail.join("\n"));
        out
    }

    // ── Sliding token window ─────────────────────────────────────────────────

    /// Select the highest-scoring chunks that fit within `self.budget` tokens.
    ///
    /// Chunks must already be sorted by `score` descending (RRF output order).
    /// Accumulates greedily: includes a chunk if adding its estimated tokens
    /// would not exceed the budget, then stops at the first chunk that does not
    /// fit.
    ///
    /// Token estimation: `text.split_whitespace().count() as f64 * 1.33` rounded
    /// up.
    pub fn window(&self, chunks: &[RetrievalHit]) -> Vec<RetrievalHit> {
        let mut selected = Vec::new();
        let mut used: usize = 0;

        for chunk in chunks {
            let chunk_tokens = estimate_tokens(&chunk.text);
            if used + chunk_tokens > self.budget {
                break;
            }
            used += chunk_tokens;
            selected.push(chunk.clone());
        }

        selected
    }

    // ── Within-window dedup ──────────────────────────────────────────────────

    /// Remove duplicate chunks based on normalised content fingerprint.
    ///
    /// Normalisation: lowercase → collapse whitespace → strip punctuation.
    /// Fingerprint: SHA-256 hex of normalised text.
    ///
    /// The first occurrence of each fingerprint is kept; subsequent copies are
    /// dropped. Order is preserved.
    pub fn dedup(&self, chunks: Vec<RetrievalHit>) -> Vec<RetrievalHit> {
        let mut seen: HashSet<String> = HashSet::new();
        chunks
            .into_iter()
            .filter(|c| {
                let fp = content_fingerprint(&c.text);
                seen.insert(fp)
            })
            .collect()
    }

    // ── Cross-session dedup (T-P4-E04-21) ────────────────────────────────────

    /// Filter out chunks whose hash was already delivered to `session_id`
    /// within the last 30 minutes, using the `context_fingerprints` SQLite
    /// table.
    ///
    /// Requires a live `rusqlite::Connection`. Returns only chunks not yet
    /// delivered (or whose TTL has expired).
    pub fn cross_session_dedup(
        &self,
        chunks: Vec<RetrievalHit>,
        session_id: &str,
        conn: &rusqlite::Connection,
    ) -> Vec<RetrievalHit> {
        // Fetch all hashes delivered to this session in the last 30 min.
        let cutoff = now_unix() - 1800;
        let mut stmt = match conn.prepare(
            "SELECT chunk_hash FROM context_fingerprints \
             WHERE session_id = ?1 AND delivered_at >= ?2",
        ) {
            Ok(s) => s,
            Err(_) => return chunks, // table not yet created — fall through
        };

        let seen: HashSet<String> = match stmt.query_map(rusqlite::params![session_id, cutoff], |row| {
            row.get::<_, String>(0)
        }) {
            Ok(rows) => rows.flatten().collect(),
            Err(_) => HashSet::new(),
        };

        chunks
            .into_iter()
            .filter(|c| {
                let fp = content_fingerprint(&c.text);
                !seen.contains(&fp)
            })
            .collect()
    }

    /// Persist delivered chunk fingerprints to the `context_fingerprints` table.
    ///
    /// Called AFTER serialising the response to avoid marking chunks as
    /// delivered when a network error drops the reply.
    pub fn record_delivered(
        &self,
        session_id: &str,
        chunks: &[RetrievalHit],
        conn: &rusqlite::Connection,
    ) -> rusqlite::Result<()> {
        let now = now_unix();
        let mut stmt = conn.prepare(
            "INSERT OR REPLACE INTO context_fingerprints \
             (session_id, chunk_hash, delivered_at) VALUES (?1, ?2, ?3)",
        )?;
        for chunk in chunks {
            let fp = content_fingerprint(&chunk.text);
            stmt.execute(rusqlite::params![session_id, fp, now])?;
        }
        Ok(())
    }

    // ── TTL cleanup ──────────────────────────────────────────────────────────

    /// Delete fingerprint rows older than 2 hours.
    ///
    /// Should be called on daemon startup and hourly.
    pub fn cleanup_expired(conn: &rusqlite::Connection) -> rusqlite::Result<usize> {
        let cutoff = now_unix() - 7200;
        conn.execute(
            "DELETE FROM context_fingerprints WHERE delivered_at < ?1",
            rusqlite::params![cutoff],
        )
    }

    // ── Full pipeline ─────────────────────────────────────────────────────────

    /// Run the full optimization pipeline: dedup → window → compress shell.
    ///
    /// Returns a [`ContextResult`] with the selected chunks, compressed shell
    /// snippets, and estimated token count.
    pub fn optimize(&self, chunks: Vec<RetrievalHit>, shell_snippets: &[String]) -> ContextResult {
        // 1. Within-window dedup.
        let deduped = self.dedup(chunks);

        // 2. Greedy token window.
        let selected = self.window(&deduped);

        // 3. Token accounting.
        let tokens_used: usize = selected.iter().map(|c| estimate_tokens(&c.text)).sum();

        // 4. Compress shell snippets.
        let compressed_shell: Vec<String> =
            shell_snippets.iter().map(|s| self.compress_shell(s)).collect();

        ContextResult {
            chunks: selected,
            shell_snippets: compressed_shell,
            tokens_used,
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Estimate token count for a text string.
///
/// Uses `words * 1.33` rounded up, which stays within ±15% of tiktoken for
/// typical prose and code without requiring any tokeniser library.
pub fn estimate_tokens(text: &str) -> usize {
    let words = text.split_whitespace().count();
    ((words as f64) * 1.33).ceil() as usize
}

/// Compute a content fingerprint (SHA-256 hex) of normalised text.
///
/// Normalisation: lowercase → collapse whitespace → strip non-alphanumeric.
pub fn content_fingerprint(text: &str) -> String {
    let normalised = normalise_text(text);
    let mut hasher = Sha256::new();
    hasher.update(normalised.as_bytes());
    format!("{:x}", hasher.finalize())
}

/// Normalise text for fingerprinting: lowercase, collapse whitespace, strip
/// ASCII punctuation.
fn normalise_text(text: &str) -> String {
    text.to_lowercase()
        .chars()
        .map(|c| if c.is_alphanumeric() || c == ' ' { c } else { ' ' })
        .collect::<String>()
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ")
}

/// Strip ANSI escape sequences from `raw`.
///
/// Matches: `ESC [ <params> <final-byte>` covering colour, bold, dim, cursor
/// movement, and erase sequences.
fn strip_ansi(raw: &str) -> String {
    // State machine: outside escape vs. inside `ESC[…`.
    let mut out = String::with_capacity(raw.len());
    let mut chars = raw.chars().peekable();

    while let Some(ch) = chars.next() {
        if ch == '\x1b' {
            // Consume until a letter in `@A-Z[\]^_`a-z` terminates the seq.
            if chars.peek() == Some(&'[') {
                chars.next(); // consume '['
                // Consume parameter/intermediate bytes (0x20-0x3F) and final byte.
                for c in chars.by_ref() {
                    if ('\x40'..='\x7e').contains(&c) {
                        break; // final byte — sequence complete
                    }
                }
            }
            // Other ESC sequences: just skip the ESC.
        } else {
            out.push(ch);
        }
    }
    out
}

/// Fold consecutive identical lines, replacing N copies with `… (×N)`.
fn fold_duplicate_lines(text: &str) -> String {
    let mut result = Vec::<String>::new();
    let mut last_line: Option<String> = None;
    let mut count: usize = 0;

    for line in text.lines() {
        if Some(line) == last_line.as_deref() {
            count += 1;
        } else {
            // Flush previous group.
            if let Some(ref prev) = last_line {
                if count > 1 {
                    result.push(format!("… (×{count})"));
                } else {
                    result.push(prev.clone());
                }
            }
            last_line = Some(line.to_string());
            count = 1;
        }
    }

    // Flush last group.
    if let Some(ref prev) = last_line {
        if count > 1 {
            result.push(format!("… (×{count})"));
        } else {
            result.push(prev.clone());
        }
    }

    result.join("\n")
}

/// Unix timestamp (seconds since epoch).
fn now_unix() -> i64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs() as i64
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::{RetrievalHit};

    /// Build a minimal RetrievalHit for tests.
    fn make_hit(id: &str, text: &str, score: f32) -> RetrievalHit {
        RetrievalHit {
            chunk_id: id.to_string(),
            text: text.to_string(),
            file_path: None,
            start_line: None,
            end_line: None,
            score,
            rank: 0,
            tier: None,
        }
    }

    // ── compress_shell ────────────────────────────────────────────────────────

    #[test]
    fn ansi_codes_stripped() {
        let opt = ContextOptimizer::new(4096);
        let raw = "\x1b[32mCompiling\x1b[0m cascade v0.1.0";
        let out = opt.compress_shell(raw);
        assert!(!out.contains('\x1b'), "ANSI codes should be removed");
        assert!(out.contains("Compiling cascade v0.1.0"));
    }

    #[test]
    fn consecutive_duplicate_lines_folded() {
        let opt = ContextOptimizer::new(4096);
        let raw = "warning: unused import\n".repeat(5);
        let out = opt.compress_shell(&raw);
        assert!(
            out.contains("… (×5)") || out.contains("(×5)"),
            "5 duplicate lines should be folded: {out:?}"
        );
        // After folding N identical lines into "… (×N)", the original text should
        // not appear separately — it is entirely replaced by the fold marker.
        assert!(
            !out.contains("warning: unused import"),
            "original text should be replaced by fold marker, got: {out:?}"
        );
    }

    #[test]
    fn shell_output_over_80_lines_truncated() {
        let opt = ContextOptimizer::new(4096);
        // 200 unique lines — no folding, so truncation must apply.
        let raw = (0..200).map(|i| format!("line {i}")).collect::<Vec<_>>().join("\n");
        let out = opt.compress_shell(&raw);
        let line_count = out.lines().count();
        // head(20) + 1 omission notice + tail(20) = 41 lines
        assert!(
            line_count <= 41,
            "truncated output should be ≤41 lines, got {line_count}"
        );
        assert!(out.contains("omitted"), "should contain omission notice");
        assert!(out.contains("line 0"), "head should be present");
        assert!(out.contains("line 199"), "tail should be present");
    }

    #[test]
    fn cargo_build_fixture_60pct_reduction() {
        let opt = ContextOptimizer::new(4096);
        // Simulate a 200-line cargo build with ANSI codes and repeated lines.
        let mut lines = Vec::new();
        for i in 0..10 {
            lines.push(format!("\x1b[32m   Compiling\x1b[0m crate-{i} v1.0.0"));
        }
        for _ in 0..150 {
            lines.push("warning: unused variable `x`".to_string());
        }
        for i in 0..40 {
            lines.push(format!("   Compiling unique-{i} v0.0.1"));
        }
        let raw = lines.join("\n");
        let out = opt.compress_shell(&raw);
        let out_lines = out.lines().count();
        let orig_lines = lines.len();
        assert!(
            out_lines <= orig_lines * 40 / 100,
            "should reduce lines by ≥60%: {orig_lines} → {out_lines}"
        );
    }

    // ── window ────────────────────────────────────────────────────────────────

    #[test]
    fn window_stops_at_budget() {
        let opt = ContextOptimizer::new(50); // tiny budget
        // Each chunk: "word " * 20 ≈ 20 words * 1.33 ≈ 27 tokens.
        let chunks: Vec<RetrievalHit> = (0..10)
            .map(|i| make_hit(&i.to_string(), &"word ".repeat(20), 1.0 - i as f32 * 0.1))
            .collect();
        let selected = opt.window(&chunks);
        let used: usize = selected.iter().map(|c| estimate_tokens(&c.text)).sum();
        assert!(
            used <= 50,
            "window must respect budget: used={used} > budget=50"
        );
        // At least one chunk should fit.
        assert!(!selected.is_empty(), "at least one chunk should fit in budget=50");
    }

    #[test]
    fn window_budget_math_exact() {
        // Single-word chunks: estimate_tokens("hello") = ceil(1 * 1.33) = 2.
        let opt = ContextOptimizer::new(4); // fits exactly 2 single-word chunks
        let chunks: Vec<RetrievalHit> = (0..5)
            .map(|i| make_hit(&i.to_string(), "hello", 1.0 - i as f32 * 0.1))
            .collect();
        let selected = opt.window(&chunks);
        assert_eq!(selected.len(), 2, "exactly 2 chunks should fit in budget=4");
    }

    // ── dedup ─────────────────────────────────────────────────────────────────

    #[test]
    fn dedup_removes_exact_duplicate() {
        let opt = ContextOptimizer::new(4096);
        let chunks = vec![
            make_hit("a", "The quick brown fox", 1.0),
            make_hit("b", "The quick brown fox", 0.9), // exact duplicate
            make_hit("c", "Completely different text here", 0.8),
        ];
        let result = opt.dedup(chunks);
        assert_eq!(result.len(), 2, "one duplicate should be removed");
        assert_eq!(result[0].chunk_id, "a");
        assert_eq!(result[1].chunk_id, "c");
    }

    #[test]
    fn dedup_normalises_before_fingerprint() {
        let opt = ContextOptimizer::new(4096);
        let chunks = vec![
            make_hit("a", "Hello, World!", 1.0),
            make_hit("b", "hello world", 0.9), // same after normalisation
        ];
        let result = opt.dedup(chunks);
        assert_eq!(result.len(), 1, "normalised duplicates should be removed");
    }

    // ── optimize (full pipeline) ──────────────────────────────────────────────

    #[test]
    fn optimize_tokens_used_accurate() {
        let budget = 100;
        let opt = ContextOptimizer::new(budget);
        // Each chunk: 10 words → ceil(10*1.33) = 14 tokens.
        let chunks: Vec<RetrievalHit> = (0..20)
            .map(|i| make_hit(&i.to_string(), &"word ".repeat(10), 1.0 - i as f32 * 0.01))
            .collect();
        let result = opt.optimize(chunks, &[]);
        assert!(
            result.tokens_used <= budget,
            "tokens_used={} exceeds budget={budget}",
            result.tokens_used
        );
        // Verify the sum matches.
        let expected: usize = result.chunks.iter().map(|c| estimate_tokens(&c.text)).sum();
        assert_eq!(result.tokens_used, expected);
    }

    #[test]
    fn optimize_empty_input() {
        let opt = ContextOptimizer::new(2048);
        let result = opt.optimize(vec![], &[]);
        assert!(result.chunks.is_empty());
        assert_eq!(result.tokens_used, 0);
    }

    // ── cross-session dedup (T-P4-E04-21) ────────────────────────────────────

    fn make_conn_with_fingerprints_table() -> rusqlite::Connection {
        let conn = rusqlite::Connection::open_in_memory().unwrap();
        conn.execute_batch(
            "CREATE TABLE context_fingerprints (
                session_id  TEXT NOT NULL,
                chunk_hash  TEXT NOT NULL,
                delivered_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
                PRIMARY KEY (session_id, chunk_hash)
            );
            CREATE INDEX IF NOT EXISTS idx_cfp_delivered ON context_fingerprints (delivered_at);",
        )
        .unwrap();
        conn
    }

    #[test]
    fn cross_session_dedup_filters_recent() {
        let conn = make_conn_with_fingerprints_table();
        let opt = ContextOptimizer::new(4096);

        let chunk = make_hit("x", "some unique chunk text for session test", 1.0);
        let fp = content_fingerprint(&chunk.text);

        // Manually insert as recently delivered.
        conn.execute(
            "INSERT INTO context_fingerprints (session_id, chunk_hash, delivered_at) VALUES (?1, ?2, ?3)",
            rusqlite::params!["sess-1", fp, now_unix()],
        )
        .unwrap();

        let result = opt.cross_session_dedup(vec![chunk], "sess-1", &conn);
        assert!(result.is_empty(), "chunk should be filtered as already delivered");
    }

    #[test]
    fn cross_session_dedup_allows_different_session() {
        let conn = make_conn_with_fingerprints_table();
        let opt = ContextOptimizer::new(4096);

        let chunk = make_hit("y", "another unique text block here", 1.0);
        let fp = content_fingerprint(&chunk.text);

        // Delivered to session A — should not affect session B.
        conn.execute(
            "INSERT INTO context_fingerprints (session_id, chunk_hash, delivered_at) VALUES (?1, ?2, ?3)",
            rusqlite::params!["sess-A", fp, now_unix()],
        )
        .unwrap();

        let result = opt.cross_session_dedup(vec![chunk], "sess-B", &conn);
        assert_eq!(result.len(), 1, "different session should not be filtered");
    }

    #[test]
    fn cross_session_dedup_allows_expired_30min() {
        let conn = make_conn_with_fingerprints_table();
        let opt = ContextOptimizer::new(4096);

        let chunk = make_hit("z", "expired delivery text content here", 1.0);
        let fp = content_fingerprint(&chunk.text);

        // Delivered 31 minutes ago — outside the 30-min window.
        let old = now_unix() - 1860;
        conn.execute(
            "INSERT INTO context_fingerprints (session_id, chunk_hash, delivered_at) VALUES (?1, ?2, ?3)",
            rusqlite::params!["sess-1", fp, old],
        )
        .unwrap();

        let result = opt.cross_session_dedup(vec![chunk], "sess-1", &conn);
        assert_eq!(result.len(), 1, "chunk with expired TTL should not be filtered");
    }

    #[test]
    fn record_delivered_persists_fingerprints() {
        let conn = make_conn_with_fingerprints_table();
        let opt = ContextOptimizer::new(4096);

        let chunks = vec![
            make_hit("r1", "record delivery chunk one text", 1.0),
            make_hit("r2", "record delivery chunk two text", 0.9),
        ];
        opt.record_delivered("sess-2", &chunks, &conn).unwrap();

        let count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM context_fingerprints WHERE session_id = 'sess-2'",
                [],
                |row| row.get(0),
            )
            .unwrap();
        assert_eq!(count, 2, "two fingerprints should be recorded");
    }

    #[test]
    fn ttl_cleanup_removes_old_rows() {
        let conn = make_conn_with_fingerprints_table();

        let old = now_unix() - 7201; // just over 2h
        let recent = now_unix();

        conn.execute_batch(&format!(
            "INSERT INTO context_fingerprints (session_id, chunk_hash, delivered_at) VALUES ('s', 'old-hash', {old});
             INSERT INTO context_fingerprints (session_id, chunk_hash, delivered_at) VALUES ('s', 'new-hash', {recent});"
        ))
        .unwrap();

        let deleted = ContextOptimizer::cleanup_expired(&conn).unwrap();
        assert_eq!(deleted, 1, "one expired row should be deleted");

        let remaining: i64 = conn
            .query_row("SELECT COUNT(*) FROM context_fingerprints", [], |row| {
                row.get(0)
            })
            .unwrap();
        assert_eq!(remaining, 1, "one recent row should remain");
    }
}
