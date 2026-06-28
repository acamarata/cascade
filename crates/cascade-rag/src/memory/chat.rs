//! # memory::chat
//!
//! Purpose: chat_history table API — insert and list chat turns.
//!   Used by the daemon HTTP endpoint and future app integration.
//!
//! Inputs:  rusqlite Connection, scope, namespace, role, content
//! Outputs: inserted id / Vec<ChatMessage>
//! Constraints:
//!   - All queries include `WHERE scope = ? AND namespace = ?`.
//!   - Role must be "user" or "assistant" — validated by the caller / HTTP layer.
//!   - `limit` is clamped to [1, 500].
//!
//! // TODO app-01: swap app's localStorage to use this endpoint

use rusqlite::Connection;

use super::episode::new_uuid_pub;
use super::namespace::ValidatedNamespace;

// ── Public types ──────────────────────────────────────────────────────────────

/// A single chat turn retrieved from `chat_history`.
#[derive(Debug, Clone, PartialEq)]
pub struct ChatMessage {
    pub id: String,
    pub scope: String,
    pub namespace: String,
    pub role: String,
    pub content: String,
    pub created_at: String,
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Insert a chat message into `chat_history`.
///
/// # Returns
/// The UUID of the newly inserted row.
pub fn insert_chat(
    conn: &Connection,
    scope: &str,
    namespace: &ValidatedNamespace,
    role: &str,
    content: &str,
) -> rusqlite::Result<String> {
    let id = new_uuid_pub();
    conn.execute(
        "INSERT INTO chat_history (id, scope, namespace, role, content)
         VALUES (?1, ?2, ?3, ?4, ?5)",
        rusqlite::params![id, scope, namespace.as_str(), role, content],
    )?;
    Ok(id)
}

/// List the most recent `limit` chat messages for `scope` + `namespace`,
/// ordered oldest-first (chronological).
///
/// `limit` is clamped to [1, 500].
pub fn list_chat(
    conn: &Connection,
    scope: &str,
    namespace: &ValidatedNamespace,
    limit: usize,
) -> rusqlite::Result<Vec<ChatMessage>> {
    let limit = limit.clamp(1, 500);
    let mut stmt = conn.prepare(
        "SELECT id, scope, namespace, role, content, created_at
         FROM chat_history
         WHERE scope = ?1 AND namespace = ?2
         ORDER BY created_at ASC, rowid ASC
         LIMIT ?3",
    )?;
    let msgs = stmt
        .query_map(
            rusqlite::params![scope, namespace.as_str(), limit as i64],
            |row| {
                Ok(ChatMessage {
                    id: row.get(0)?,
                    scope: row.get(1)?,
                    namespace: row.get(2)?,
                    role: row.get(3)?,
                    content: row.get(4)?,
                    created_at: row.get(5)?,
                })
            },
        )?
        .filter_map(|r| r.ok())
        .collect();
    Ok(msgs)
}

/// Delete all chat history for a scope + namespace. Used by the `forget` tool.
pub fn clear_chat(
    conn: &Connection,
    scope: &str,
    namespace: &ValidatedNamespace,
) -> rusqlite::Result<usize> {
    conn.execute(
        "DELETE FROM chat_history WHERE scope = ?1 AND namespace = ?2",
        rusqlite::params![scope, namespace.as_str()],
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
    fn insert_and_list() {
        let conn = test_db();
        let ns = validate("dev-chat-test").unwrap();

        insert_chat(&conn, "cascade-app", &ns, "user", "hello").unwrap();
        insert_chat(&conn, "cascade-app", &ns, "assistant", "world").unwrap();

        let msgs = list_chat(&conn, "cascade-app", &ns, 10).unwrap();
        assert_eq!(msgs.len(), 2);
        assert_eq!(msgs[0].role, "user");
        assert_eq!(msgs[1].role, "assistant");
    }

    #[test]
    fn scope_isolation() {
        let conn = test_db();
        let ns = validate("dev-chat-test").unwrap();

        insert_chat(&conn, "scope-a", &ns, "user", "in scope a").unwrap();

        let msgs = list_chat(&conn, "scope-b", &ns, 10).unwrap();
        assert!(msgs.is_empty(), "scope-b must not see scope-a messages");
    }

    #[test]
    fn namespace_isolation() {
        let conn = test_db();
        let ns_a = validate("dev-alpha").unwrap();
        let ns_b = validate("dev-beta").unwrap();

        insert_chat(&conn, "cascade-app", &ns_a, "user", "alpha message").unwrap();

        let msgs = list_chat(&conn, "cascade-app", &ns_b, 10).unwrap();
        assert!(msgs.is_empty(), "ns-beta must not see ns-alpha messages");
    }

    #[test]
    fn clear_removes_messages() {
        let conn = test_db();
        let ns = validate("dev-clear").unwrap();

        insert_chat(&conn, "cascade-app", &ns, "user", "to be cleared").unwrap();
        let removed = clear_chat(&conn, "cascade-app", &ns).unwrap();
        assert_eq!(removed, 1);

        let msgs = list_chat(&conn, "cascade-app", &ns, 10).unwrap();
        assert!(msgs.is_empty());
    }

    #[test]
    fn limit_clamped() {
        let conn = test_db();
        let ns = validate("dev-limit").unwrap();

        for i in 0..5 {
            insert_chat(&conn, "cascade-app", &ns, "user", &format!("msg {i}")).unwrap();
        }

        let msgs = list_chat(&conn, "cascade-app", &ns, 3).unwrap();
        assert!(msgs.len() <= 3);
    }
}
