//! # memory::episode
//!
//! Purpose: Insert episodic memory entries into the `memory_episodes` table.
//!   Episodes are the raw, unprocessed observations that feed into the
//!   consolidation pipeline (see `memory::fact`).
//!
//! Inputs:  rusqlite Connection, namespace (validated), content string
//! Outputs: inserted episode UUID string
//! Constraints:
//!   - Namespace MUST be validated before calling — callers use namespace::validate()
//!     or namespace::validate_with_firewall().
//!   - No embedding is stored at insert time; embedding is populated lazily.
//!   - Thread safety: callers hold the Connection exclusively.

use rusqlite::{Connection, Result};

use super::namespace::ValidatedNamespace;

// ── Public API ────────────────────────────────────────────────────────────────

/// Insert a new episode into `memory_episodes` for the given namespace.
///
/// # Returns
/// The UUID of the newly inserted episode (TEXT PRIMARY KEY).
///
/// # Example
/// ```ignore
/// let ns = namespace::validate("dev-nself").unwrap();
/// let id = insert_episode(&conn, &ns, "User asked about Rust lifetimes")?;
/// ```
pub fn insert_episode(
    conn: &Connection,
    namespace: &ValidatedNamespace,
    content: &str,
) -> Result<String> {
    let id = new_uuid();
    conn.execute(
        "INSERT INTO memory_episodes (id, namespace, content) VALUES (?1, ?2, ?3)",
        rusqlite::params![id, namespace.as_str(), content],
    )?;
    Ok(id)
}

/// Count episodes in a given namespace. Used by consolidation threshold checks.
pub fn count_episodes(conn: &Connection, namespace: &ValidatedNamespace) -> Result<u64> {
    conn.query_row(
        "SELECT COUNT(*) FROM memory_episodes WHERE namespace = ?1",
        [namespace.as_str()],
        |row| row.get::<_, i64>(0),
    )
    .map(|n| n as u64)
}

/// Delete a single episode by id. Used by the `forget` MCP tool.
pub fn delete_episode(conn: &Connection, namespace: &ValidatedNamespace, id: &str) -> Result<usize> {
    conn.execute(
        "DELETE FROM memory_episodes WHERE id = ?1 AND namespace = ?2",
        rusqlite::params![id, namespace.as_str()],
    )
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Public alias used by `fact.rs` to generate UUIDs without an external dep.
pub(super) fn new_uuid_pub() -> String {
    new_uuid()
}

fn new_uuid() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    // Deterministic-enough for our purposes without pulling in a uuid crate.
    // Uses UNIX nanos + a simple counter encoded as hex.
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .subsec_nanos();
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    // RFC 4122 v4 UUID shape: 8-4-4-4-12 hex chars
    format!(
        "{:08x}-{:04x}-4{:03x}-{:04x}-{:012x}",
        secs & 0xFFFF_FFFF,
        (secs >> 32) & 0xFFFF,
        nanos & 0x0FFF,
        0x8000 | (nanos >> 12 & 0x3FFF),
        (secs as u128 * 1_000_000_000 + nanos as u128) & 0xFFFF_FFFF_FFFF,
    )
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use rusqlite::Connection;

    use super::*;
    use crate::db::run_migrations;
    use crate::memory::namespace::validate;

    fn test_db() -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();
        conn
    }

    #[test]
    fn insert_and_count() {
        let conn = test_db();
        let ns = validate("dev-test").unwrap();

        let id = insert_episode(&conn, &ns, "hello world").unwrap();
        assert!(!id.is_empty(), "uuid must be non-empty");

        let count = count_episodes(&conn, &ns).unwrap();
        assert_eq!(count, 1);
    }

    #[test]
    fn delete_removes_episode() {
        let conn = test_db();
        let ns = validate("dev-test").unwrap();

        let id = insert_episode(&conn, &ns, "to be deleted").unwrap();
        let affected = delete_episode(&conn, &ns, &id).unwrap();
        assert_eq!(affected, 1);
        assert_eq!(count_episodes(&conn, &ns).unwrap(), 0);
    }

    #[test]
    fn delete_wrong_namespace_is_noop() {
        let conn = test_db();
        let ns_a = validate("dev-alpha").unwrap();
        let ns_b = validate("dev-beta").unwrap();

        let id = insert_episode(&conn, &ns_a, "secret").unwrap();
        // Trying to delete from ns_b must not touch ns_a's row.
        let affected = delete_episode(&conn, &ns_b, &id).unwrap();
        assert_eq!(affected, 0, "cross-namespace delete must be a noop");
        assert_eq!(count_episodes(&conn, &ns_a).unwrap(), 1);
    }
}
