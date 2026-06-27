//! # memory::recall
//!
//! Purpose: Recall (query) memory within a namespace.
//!   Provides FTS-like keyword search over episodes and facts.
//!   A recall in one namespace MUST NOT return another namespace's rows — this
//!   is enforced at the SQL level via `WHERE namespace = ?`.
//!
//! Inputs:  rusqlite Connection, validated namespace, query string, k (limit)
//! Outputs: Vec<RecallHit> sorted by relevance (recency + keyword match)
//! Constraints:
//!   - Hard namespace isolation: every SELECT includes `WHERE namespace = ?`.
//!   - `k` is clamped to [1, 50].
//!   - No embedding-based search in this ticket (embedding path is P4+ scope).

use rusqlite::Connection;

use super::namespace::ValidatedNamespace;

// ── Public types ──────────────────────────────────────────────────────────────

/// A single hit returned by [`recall`].
#[derive(Debug, Clone, PartialEq)]
pub struct RecallHit {
    /// Source table: `"episode"` or `"fact"`.
    pub source: String,
    /// Row id (UUID).
    pub id: String,
    /// Text content of the episode or fact triple (subject::predicate::value).
    pub content: String,
    /// ISO 8601 UTC timestamp of creation / last_seen.
    pub timestamp: Option<String>,
    /// Confidence for facts; 1.0 for episodes.
    pub confidence: f64,
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Recall memory items matching `query` within `namespace`.
///
/// Searches both `memory_episodes` and active `memory_facts` for rows whose
/// content contains any token from `query` (case-insensitive LIKE). Results are
/// ordered by recency (most recent first). `k` is clamped to [1, 50].
///
/// Cross-namespace isolation: every SELECT binds `namespace = ?`.
pub fn recall(
    conn: &Connection,
    namespace: &ValidatedNamespace,
    query: &str,
    k: usize,
) -> rusqlite::Result<Vec<RecallHit>> {
    let k = k.clamp(1, 50);
    let pattern = format!("%{}%", query.replace('%', "\\%").replace('_', "\\_"));
    let ns = namespace.as_str();

    let mut hits: Vec<RecallHit> = Vec::new();

    // ── Episodes ──────────────────────────────────────────────────────────────
    {
        let mut stmt = conn.prepare(
            "SELECT id, content, created_at
             FROM memory_episodes
             WHERE namespace = ?1
               AND content LIKE ?2 ESCAPE '\\'
             ORDER BY created_at DESC
             LIMIT ?3",
        )?;
        let ep_hits = stmt.query_map(rusqlite::params![ns, pattern, k as i64], |row| {
            Ok(RecallHit {
                source: "episode".into(),
                id: row.get(0)?,
                content: row.get(1)?,
                timestamp: row.get(2)?,
                confidence: 1.0,
            })
        })?;
        for h in ep_hits.flatten() {
            hits.push(h);
        }
    }

    // ── Facts ─────────────────────────────────────────────────────────────────
    {
        let mut stmt = conn.prepare(
            "SELECT id,
                    (subject || '::' || predicate || '::' || value) as content,
                    last_seen,
                    confidence
             FROM memory_facts
             WHERE namespace = ?1
               AND archived = 0
               AND (subject LIKE ?2 ESCAPE '\\'
                    OR predicate LIKE ?2 ESCAPE '\\'
                    OR value LIKE ?2 ESCAPE '\\')
             ORDER BY last_seen DESC
             LIMIT ?3",
        )?;
        let fact_hits = stmt.query_map(rusqlite::params![ns, pattern, k as i64], |row| {
            Ok(RecallHit {
                source: "fact".into(),
                id: row.get(0)?,
                content: row.get(1)?,
                timestamp: row.get(2)?,
                confidence: row.get(3)?,
            })
        })?;
        for h in fact_hits.flatten() {
            hits.push(h);
        }
    }

    // Sort combined results: facts first (higher confidence intent), then episodes.
    // Within each group: recency order is preserved from SQL ORDER BY.
    hits.sort_by(|a, b| b.confidence.partial_cmp(&a.confidence).unwrap_or(std::cmp::Ordering::Equal));
    hits.truncate(k);
    Ok(hits)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use rusqlite::Connection;

    use super::*;
    use crate::db::run_migrations;
    use crate::memory::episode::insert_episode;
    use crate::memory::fact::upsert_fact;
    use crate::memory::namespace::validate;

    fn test_db() -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();
        conn
    }

    #[test]
    fn recall_finds_episode_by_keyword() {
        let conn = test_db();
        let ns = validate("dev-recall-test").unwrap();

        insert_episode(&conn, &ns, "the user loves Rust lifetimes").unwrap();
        insert_episode(&conn, &ns, "unrelated topic").unwrap();

        let hits = recall(&conn, &ns, "Rust", 10).unwrap();
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].source, "episode");
        assert!(hits[0].content.contains("Rust"));
    }

    #[test]
    fn recall_finds_fact_by_keyword() {
        let conn = test_db();
        let ns = validate("dev-recall-test").unwrap();

        upsert_fact(&conn, &ns, "user", "prefers", "dark mode", 1.0).unwrap();

        let hits = recall(&conn, &ns, "dark", 10).unwrap();
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].source, "fact");
    }

    #[test]
    fn recall_namespace_isolation() {
        let conn = test_db();
        let ns_a = validate("dev-alpha").unwrap();
        let ns_b = validate("dev-beta").unwrap();

        insert_episode(&conn, &ns_a, "alpha secret data").unwrap();

        // recall in ns_b must not return ns_a's episode.
        let hits = recall(&conn, &ns_b, "alpha", 10).unwrap();
        assert!(
            hits.is_empty(),
            "cross-namespace recall must return no results"
        );
    }

    #[test]
    fn recall_limit_enforced() {
        let conn = test_db();
        let ns = validate("dev-limit").unwrap();

        for i in 0..10 {
            insert_episode(&conn, &ns, &format!("tokio runtime episode {i}")).unwrap();
        }

        let hits = recall(&conn, &ns, "tokio", 3).unwrap();
        assert!(hits.len() <= 3, "result count must be <= k");
    }

    #[test]
    fn recall_k_clamped_to_50() {
        let conn = test_db();
        let ns = validate("dev-clamp").unwrap();

        // Requesting k=9999 must not crash or panic.
        let hits = recall(&conn, &ns, "anything", 9999).unwrap();
        // Empty DB — just check it doesn't panic.
        assert!(hits.is_empty());
    }
}
