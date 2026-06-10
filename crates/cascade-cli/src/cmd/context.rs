//! `cascade context` — context fingerprint management subcommands.
//!
//! ## Subcommands
//!
//! | Subcommand | Description |
//! |---|---|
//! | `clear-session <id>` | Purge cross-session dedup fingerprints for a session ID |
//!
//! SPORT: MASTER-CLI.md → cascade context

use async_trait::async_trait;
use cascade_rag::context::ContextOptimizer;
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};
use std::path::PathBuf;

use super::Command;

// ── Args ──────────────────────────────────────────────────────────────────────

/// Context fingerprint management.
#[derive(Debug, Args)]
pub struct ContextArgs {
    #[command(subcommand)]
    pub subcommand: ContextSubcommand,
}

/// Subcommands under `cascade context`.
#[derive(Debug, Subcommand)]
pub enum ContextSubcommand {
    /// Purge cross-session dedup fingerprints for a harness session.
    ///
    /// After clearing, the same chunks will be returned again on the next
    /// `cascade.context_slice` call for this session ID.
    ClearSession(ClearSessionArgs),

    /// Run TTL cleanup: delete all fingerprint rows older than 2 hours.
    CleanupExpired(CleanupExpiredArgs),
}

/// Arguments for `cascade context clear-session`.
#[derive(Debug, Args)]
pub struct ClearSessionArgs {
    /// Harness session ID to purge (opaque string; matches the `session_id`
    /// passed to `cascade.context_slice`).
    pub session_id: String,

    /// Path to the cascade RAG database file.
    /// Defaults to `~/.cascade/rag.db`.
    #[arg(long, value_name = "PATH")]
    pub db: Option<PathBuf>,
}

/// Arguments for `cascade context cleanup-expired`.
#[derive(Debug, Args)]
pub struct CleanupExpiredArgs {
    /// Path to the cascade RAG database file.
    /// Defaults to `~/.cascade/rag.db`.
    #[arg(long, value_name = "PATH")]
    pub db: Option<PathBuf>,
}

// ── impl Command ──────────────────────────────────────────────────────────────

#[async_trait]
impl Command for ContextArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            ContextSubcommand::ClearSession(args) => args.run().await,
            ContextSubcommand::CleanupExpired(args) => args.run().await,
        }
    }
}

#[async_trait]
impl Command for ClearSessionArgs {
    async fn run(&self) -> Result<()> {
        let db_path = resolve_db_path(self.db.as_deref());
        let conn = open_conn(&db_path)?;

        // Ensure the table exists (no-op if already present).
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS context_fingerprints (
                session_id   TEXT NOT NULL,
                chunk_hash   TEXT NOT NULL,
                delivered_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
                PRIMARY KEY (session_id, chunk_hash)
            );",
        )
        .map_err(|e| CascadeError::Other(format!("DB error: {e}")))?;

        let deleted = conn
            .execute(
                "DELETE FROM context_fingerprints WHERE session_id = ?1",
                rusqlite::params![self.session_id],
            )
            .map_err(|e| CascadeError::Other(format!("DB error: {e}")))?;

        println!(
            "Cleared {deleted} fingerprint(s) for session '{}'.",
            self.session_id
        );
        Ok(())
    }
}

#[async_trait]
impl Command for CleanupExpiredArgs {
    async fn run(&self) -> Result<()> {
        let db_path = resolve_db_path(self.db.as_deref());
        let conn = open_conn(&db_path)?;

        let deleted = ContextOptimizer::cleanup_expired(&conn)
            .map_err(|e| CascadeError::Other(format!("DB error: {e}")))?;

        println!("Deleted {deleted} expired fingerprint row(s) (TTL > 2h).");
        Ok(())
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Resolve the RAG database path.
///
/// Falls back to `~/.cascade/rag.db` when no explicit path is given.
fn resolve_db_path(explicit: Option<&std::path::Path>) -> PathBuf {
    if let Some(p) = explicit {
        return p.to_path_buf();
    }
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("/tmp"))
        .join(".cascade")
        .join("rag.db")
}

/// Open (or create) the SQLite database at `path`.
fn open_conn(path: &PathBuf) -> Result<rusqlite::Connection> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create_dir_all",
            source: e,
        })?;
    }
    rusqlite::Connection::open(path)
        .map_err(|e| CascadeError::Other(format!("Failed to open DB at {}: {e}", path.display())))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    fn make_in_memory_conn() -> rusqlite::Connection {
        let conn = rusqlite::Connection::open_in_memory().unwrap();
        conn.execute_batch(
            "CREATE TABLE context_fingerprints (
                session_id   TEXT NOT NULL,
                chunk_hash   TEXT NOT NULL,
                delivered_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
                PRIMARY KEY (session_id, chunk_hash)
            );",
        )
        .unwrap();
        conn
    }

    #[test]
    fn clear_session_deletes_matching_rows() {
        let conn = make_in_memory_conn();
        conn.execute_batch(
            "INSERT INTO context_fingerprints (session_id, chunk_hash, delivered_at) VALUES ('s1', 'h1', 0);
             INSERT INTO context_fingerprints (session_id, chunk_hash, delivered_at) VALUES ('s1', 'h2', 0);
             INSERT INTO context_fingerprints (session_id, chunk_hash, delivered_at) VALUES ('s2', 'h3', 0);",
        )
        .unwrap();

        let deleted = conn
            .execute(
                "DELETE FROM context_fingerprints WHERE session_id = ?1",
                rusqlite::params!["s1"],
            )
            .unwrap();
        assert_eq!(deleted, 2, "should delete 2 rows for s1");

        let remaining: i64 = conn
            .query_row("SELECT COUNT(*) FROM context_fingerprints", [], |r| {
                r.get(0)
            })
            .unwrap();
        assert_eq!(remaining, 1, "s2 row should remain");
    }

    #[test]
    fn db_path_resolves_default() {
        let p = resolve_db_path(None);
        assert!(
            p.to_string_lossy().contains(".cascade"),
            "default path should contain .cascade"
        );
        assert!(p.ends_with("rag.db"), "default db file is rag.db");
    }

    #[test]
    fn db_path_uses_explicit() {
        let p = resolve_db_path(Some(std::path::Path::new("/tmp/test.db")));
        assert_eq!(p, std::path::PathBuf::from("/tmp/test.db"));
    }
}
