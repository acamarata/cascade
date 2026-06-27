//! # cascade-core::tasks::migrations — idempotent schema setup
//!
//! Purpose: Open (or create) the kanban tasks SQLite database and apply all
//!   migrations in order. Every migration is a CREATE TABLE IF NOT EXISTS or
//!   safe ALTER TABLE — safe to run on an already-migrated DB.
//!
//! Inputs:  `db_path` — absolute path to `tasks.db`
//! Outputs: `Arc<Mutex<Connection>>` ready for `KanbanTaskStore`
//! Constraints: no async; migrations run synchronously at startup.
//! SPORT: cascade-core migrations — tasks_db schema v1

use std::path::Path;
use std::sync::{Arc, Mutex};

use cascade_types::{CascadeError, Result};
use rusqlite::Connection;

/// Open the tasks database at `db_path`, running all idempotent migrations.
///
/// Creates the file (and parent directories) if absent.
/// Safe to call on an existing database — migrations are idempotent.
pub fn open_tasks_db(db_path: &Path) -> Result<Arc<Mutex<Connection>>> {
    let conn = cascade_db::open_configured(db_path)
        .map_err(|e| CascadeError::Other(format!("failed to open tasks db: {e}")))?;

    run_migrations(&conn)?;

    Ok(Arc::new(Mutex::new(conn)))
}

/// Apply all schema migrations. Each is idempotent.
fn run_migrations(conn: &Connection) -> Result<()> {
    // Migration v1 — initial kanban tasks table
    conn.execute_batch(
        "
        CREATE TABLE IF NOT EXISTS kanban_tasks (
            id          TEXT    NOT NULL PRIMARY KEY,   -- UUID v4
            title       TEXT    NOT NULL,
            description TEXT,                           -- nullable
            status      TEXT    NOT NULL DEFAULT 'backlog',
            project     TEXT    NOT NULL DEFAULT '',
            tags_json   TEXT    NOT NULL DEFAULT '[]',  -- JSON array of strings
            assignee    TEXT,                           -- nullable
            priority    TEXT    NOT NULL DEFAULT 'med',
            blockers_json TEXT  NOT NULL DEFAULT '[]',  -- JSON array of UUID strings
            created_at  TEXT    NOT NULL,               -- RFC 3339 UTC
            updated_at  TEXT    NOT NULL,               -- RFC 3339 UTC
            due         TEXT,                           -- RFC 3339 UTC, nullable
            ord         INTEGER NOT NULL DEFAULT 9223372036854775807
        );

        -- Index for project-scoped listing (most common query)
        CREATE INDEX IF NOT EXISTS idx_kanban_tasks_project
            ON kanban_tasks (project, status, ord, created_at);

        -- Index for status-only listing (cross-project dashboard)
        CREATE INDEX IF NOT EXISTS idx_kanban_tasks_status
            ON kanban_tasks (status, ord, created_at);

        -- Index for assignee filtering
        CREATE INDEX IF NOT EXISTS idx_kanban_tasks_assignee
            ON kanban_tasks (assignee, project, status);
        ",
    )
    .map_err(|e| CascadeError::Other(format!("migration v1 failed: {e}")))?;

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;

    #[test]
    fn migration_creates_table() {
        let dir = tempdir().unwrap();
        let db_path = dir.path().join("tasks.db");
        let conn_arc = open_tasks_db(&db_path).expect("open_tasks_db failed");
        let conn = conn_arc.lock().unwrap();
        // Table must exist
        let count: i64 = conn
            .query_row("SELECT COUNT(*) FROM kanban_tasks", [], |r| r.get(0))
            .expect("table not created");
        assert_eq!(count, 0);
    }

    #[test]
    fn migration_is_idempotent() {
        let dir = tempdir().unwrap();
        let db_path = dir.path().join("tasks.db");
        // Run migrations twice on the same file
        open_tasks_db(&db_path).expect("first open failed");
        open_tasks_db(&db_path).expect("second open (idempotent) failed");
    }
}
