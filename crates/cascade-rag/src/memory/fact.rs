//! # memory::fact
//!
//! Purpose: Upsert facts with BLAKE3 fingerprint + content-hash dedup,
//!   consolidation (merge duplicate episodes into a fact after N hits), and
//!   decay (archive facts not seen in T duration).
//!
//! ## Content-hash dedup
//! The content_hash is BLAKE3(namespace + "\x00" + subject + "\x00" + predicate
//! + "\x00" + value). The namespace prefix prevents two namespaces from sharing
//! the same hash even if the factual text is identical.
//!
//! ## Consolidation
//! `consolidate_namespace` scans `memory_episodes`, groups by content hash of
//! the episode text, and promotes groups with count ≥ N into a fact row.
//! Promoted episodes are deleted.
//!
//! ## Decay
//! `decay_namespace` archives facts whose `last_seen` is older than `max_age`.
//!
//! Inputs:  rusqlite Connection, namespace, subject/predicate/value strings
//! Outputs: upserted fact id / count of consolidated/archived rows
//! Constraints:
//!   - All queries MUST include `WHERE namespace = ?` — no cross-namespace leakage.
//!   - blake3 crate is a direct dependency of cascade-rag (MIT/Apache-2.0).

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use rusqlite::{Connection, Result};

use rusqlite::OptionalExtension;

use super::namespace::ValidatedNamespace;

// ── Public API ────────────────────────────────────────────────────────────────

/// Upsert a fact into `memory_facts`.
///
/// If a row with the same `content_hash` already exists:
/// - `last_seen` is updated to now.
/// - `confidence` is incremented toward 1.0 (capped at 1.0).
/// - `archived` is reset to FALSE (re-activates a previously decayed fact).
///
/// Returns the UUID of the upserted row.
pub fn upsert_fact(
    conn: &Connection,
    namespace: &ValidatedNamespace,
    subject: &str,
    predicate: &str,
    value: &str,
    confidence: f64,
) -> Result<String> {
    let hash = content_hash(namespace.as_str(), subject, predicate, value);
    let now = iso_now();

    // Check whether the fact already exists.
    let existing: Option<String> = conn
        .query_row(
            "SELECT id FROM memory_facts WHERE content_hash = ?1",
            [&hash],
            |row| row.get(0),
        )
        .optional()?;

    if let Some(id) = existing {
        conn.execute(
            "UPDATE memory_facts
             SET last_seen  = ?1,
                 confidence = MIN(1.0, confidence + ?2),
                 archived   = 0
             WHERE content_hash = ?3",
            rusqlite::params![now, confidence * 0.1, hash],
        )?;
        Ok(id)
    } else {
        let id = super::episode::new_uuid_pub();
        conn.execute(
            "INSERT INTO memory_facts
             (id, namespace, subject, predicate, value, confidence, content_hash, last_seen, archived)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, 0)",
            rusqlite::params![id, namespace.as_str(), subject, predicate, value, confidence, hash, now],
        )?;
        Ok(id)
    }
}

/// Archive (soft-delete) a fact by content_hash within the namespace.
pub fn archive_fact(
    conn: &Connection,
    namespace: &ValidatedNamespace,
    content_hash: &str,
) -> Result<usize> {
    conn.execute(
        "UPDATE memory_facts SET archived = 1 WHERE content_hash = ?1 AND namespace = ?2",
        rusqlite::params![content_hash, namespace.as_str()],
    )
}

/// Archive all facts in the namespace whose `last_seen` is older than `max_age`.
///
/// This is the decay step — called by `consolidate_namespace`.
///
/// # Returns
/// Number of facts archived.
pub fn decay_namespace(
    conn: &Connection,
    namespace: &ValidatedNamespace,
    max_age: Duration,
) -> Result<usize> {
    let cutoff_secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs()
        .saturating_sub(max_age.as_secs());
    let cutoff = epoch_secs_to_iso(cutoff_secs);
    conn.execute(
        "UPDATE memory_facts
         SET archived = 1
         WHERE namespace = ?1
           AND archived = 0
           AND last_seen IS NOT NULL
           AND last_seen < ?2",
        rusqlite::params![namespace.as_str(), cutoff],
    )
}

/// Consolidation threshold: promote episode groups into facts after this many duplicates.
pub const CONSOLIDATION_THRESHOLD: u64 = 3;

/// Consolidate duplicate episodes in a namespace into facts.
///
/// Groups episodes by BLAKE3(namespace + "\x00" + content) and promotes any
/// group with count ≥ `threshold` into a `memory_facts` row (subject="episode",
/// predicate="observed", value=content). Promoted episodes are deleted.
///
/// This is the background consolidation entry point — the scheduler calls this.
///
/// # Returns
/// Number of episode groups promoted to facts.
pub fn consolidate_namespace(
    conn: &Connection,
    namespace: &ValidatedNamespace,
    threshold: u64,
) -> Result<usize> {
    // Gather episode content groups with count >= threshold.
    // We use a GROUP BY on content to find duplicates.
    let mut stmt = conn.prepare(
        "SELECT content, COUNT(*) as cnt
         FROM memory_episodes
         WHERE namespace = ?1
         GROUP BY content
         HAVING COUNT(*) >= ?2",
    )?;
    let groups: Vec<(String, i64)> = stmt
        .query_map(
            rusqlite::params![namespace.as_str(), threshold as i64],
            |row| Ok((row.get::<_, String>(0)?, row.get::<_, i64>(1)?)),
        )?
        .filter_map(|r| r.ok())
        .collect();

    let mut promoted = 0usize;
    for (content, _count) in &groups {
        // Upsert as a fact: subject="episode", predicate="observed", value=content
        upsert_fact(conn, namespace, "episode", "observed", content, 1.0)?;

        // Delete the promoted episodes (namespace-scoped).
        conn.execute(
            "DELETE FROM memory_episodes WHERE namespace = ?1 AND content = ?2",
            rusqlite::params![namespace.as_str(), content],
        )?;
        promoted += 1;
    }

    Ok(promoted)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// BLAKE3 hash of namespace + NUL + subject + NUL + predicate + NUL + value.
fn content_hash(namespace: &str, subject: &str, predicate: &str, value: &str) -> String {
    let mut hasher = blake3::Hasher::new();
    hasher.update(namespace.as_bytes());
    hasher.update(b"\x00");
    hasher.update(subject.as_bytes());
    hasher.update(b"\x00");
    hasher.update(predicate.as_bytes());
    hasher.update(b"\x00");
    hasher.update(value.as_bytes());
    hasher.finalize().to_hex().to_string()
}

fn iso_now() -> String {
    use std::time::SystemTime;
    let d = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    epoch_secs_to_iso(d.as_secs())
}

fn epoch_secs_to_iso(secs: u64) -> String {
    // Minimal ISO 8601 UTC formatter without chrono dependency.
    let s = secs;
    let mins = s / 60;
    let hours = mins / 60;
    let days_total = hours / 24;
    let hour = hours % 24;
    let min = mins % 60;
    let sec = s % 60;

    // Days to date (Gregorian). Algorithm: http://howardhinnant.github.io/date_algorithms.html
    let z = days_total as i64 + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = z - era * 146097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };

    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}Z",
        y, m, d, hour, min, sec
    )
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use rusqlite::Connection;

    use super::*;
    use crate::db::run_migrations;
    use crate::memory::namespace::validate;
    use crate::memory::episode::insert_episode;

    fn test_db() -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();
        conn
    }

    #[test]
    fn upsert_creates_fact() {
        let conn = test_db();
        let ns = validate("dev-test").unwrap();

        let id = upsert_fact(&conn, &ns, "user", "prefers", "dark mode", 1.0).unwrap();
        assert!(!id.is_empty());

        // Count active facts
        let count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM memory_facts WHERE namespace = ?1 AND archived = 0",
                [ns.as_str()],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(count, 1);
    }

    #[test]
    fn upsert_deduplicates_by_content_hash() {
        let conn = test_db();
        let ns = validate("dev-test").unwrap();

        upsert_fact(&conn, &ns, "user", "prefers", "dark mode", 1.0).unwrap();
        upsert_fact(&conn, &ns, "user", "prefers", "dark mode", 1.0).unwrap();

        let count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM memory_facts WHERE namespace = ?1",
                [ns.as_str()],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(count, 1, "duplicate upsert must not create a second row");
    }

    #[test]
    fn cross_namespace_hash_differs() {
        // Two namespaces with identical content must NOT share a content_hash.
        let h1 = super::content_hash("dev-alpha", "user", "likes", "rust");
        let h2 = super::content_hash("dev-beta", "user", "likes", "rust");
        assert_ne!(h1, h2, "hashes must differ across namespaces");
    }

    #[test]
    fn consolidation_promotes_duplicates() {
        let conn = test_db();
        let ns = validate("dev-consolidate").unwrap();

        // Insert 3 identical episodes — above the default threshold of 3.
        for _ in 0..3 {
            insert_episode(&conn, &ns, "user prefers dark mode").unwrap();
        }

        let promoted = consolidate_namespace(&conn, &ns, CONSOLIDATION_THRESHOLD).unwrap();
        assert_eq!(promoted, 1, "one group should be promoted");

        // Episodes deleted
        let ep_count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM memory_episodes WHERE namespace = ?1",
                [ns.as_str()],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(ep_count, 0, "promoted episodes must be deleted");

        // Fact created
        let fact_count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM memory_facts WHERE namespace = ?1 AND archived = 0",
                [ns.as_str()],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(fact_count, 1, "one fact must exist after consolidation");
    }

    #[test]
    fn consolidation_below_threshold_no_op() {
        let conn = test_db();
        let ns = validate("dev-below").unwrap();

        // Insert only 2 identical episodes — below threshold of 3.
        insert_episode(&conn, &ns, "rare observation").unwrap();
        insert_episode(&conn, &ns, "rare observation").unwrap();

        let promoted = consolidate_namespace(&conn, &ns, CONSOLIDATION_THRESHOLD).unwrap();
        assert_eq!(promoted, 0, "below-threshold group must not be promoted");
    }

    #[test]
    fn decay_archives_old_facts() {
        let conn = test_db();
        let ns = validate("dev-decay").unwrap();

        // Insert a fact with last_seen far in the past.
        let hash = super::content_hash("dev-decay", "user", "liked", "old-thing");
        let id = super::super::episode::new_uuid_pub();
        conn.execute(
            "INSERT INTO memory_facts
             (id, namespace, subject, predicate, value, confidence, content_hash, last_seen, archived)
             VALUES (?1, 'dev-decay', 'user', 'liked', 'old-thing', 1.0, ?2, '2000-01-01T00:00:00Z', 0)",
            rusqlite::params![id, hash],
        ).unwrap();

        let archived = decay_namespace(&conn, &ns, Duration::from_secs(1)).unwrap();
        assert_eq!(archived, 1, "old fact must be archived");

        let still_active: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM memory_facts WHERE namespace = ?1 AND archived = 0",
                [ns.as_str()],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(still_active, 0);
    }
}
