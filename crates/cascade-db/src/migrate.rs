//! Purpose: Versioned, backup-protected SQLite migration runner. Home of mig-01.
//! Inputs: a configured `Connection` and a `MigrationRegistry` of ordered steps.
//! Outputs: the database advanced to the registry head, with a pre-migration backup written.
//! Constraints: `PRAGMA user_version` is the single source of truth; a future version is a
//!              typed error, never a silent proceed; every run backs up before mutating.
//! SPORT: cascade-db / migrate

use std::path::{Path, PathBuf};

use rusqlite::Connection;

use crate::error::{DbError, Result};

/// A single forward migration. Steps are applied in ascending `version` order; the runner
/// wraps each in a transaction and bumps `PRAGMA user_version` on success.
pub struct MigrationStep {
    /// Target schema version this step produces (1-based, contiguous).
    pub version: u32,
    /// Short human-readable name for logs and error messages.
    pub name: &'static str,
    /// The forward migration body. Runs inside a transaction.
    pub up: fn(&rusqlite::Transaction<'_>) -> rusqlite::Result<()>,
}

/// An ordered set of migration steps for one logical database.
pub struct MigrationRegistry {
    /// Logical name (e.g. "rag", "tasks") — used for backup filenames and logs.
    pub name: &'static str,
    steps: Vec<MigrationStep>,
}

impl MigrationRegistry {
    /// Create a registry. Steps are sorted by version; versions must be contiguous from 1.
    pub fn new(name: &'static str, mut steps: Vec<MigrationStep>) -> Self {
        steps.sort_by_key(|s| s.version);
        Self { name, steps }
    }

    /// Highest version known to this registry (0 if empty).
    pub fn head(&self) -> u32 {
        self.steps.last().map(|s| s.version).unwrap_or(0)
    }

    /// Run all pending migrations against `conn`, backing up `db_path` first if any are pending.
    ///
    /// `db_path` may be `None` for in-memory databases (no backup is taken).
    pub fn run(&self, conn: &mut Connection, db_path: Option<&Path>) -> Result<()> {
        let current: u32 = conn.pragma_query_value(None, "user_version", |r| r.get(0))?;
        let head = self.head();

        if current > head {
            return Err(DbError::FutureSchema {
                found: current,
                supported: head,
            });
        }
        if current == head {
            return Ok(()); // nothing to do
        }

        // Back up before mutating, only when there is real work and a real file.
        if let Some(path) = db_path {
            if path.exists() {
                let backup = backup_before_migrate(path, self.name, current)?;
                tracing::info!(
                    registry = self.name,
                    from = current,
                    to = head,
                    backup = %backup.display(),
                    "pre-migration backup written"
                );
            }
        }

        for step in self.steps.iter().filter(|s| s.version > current) {
            let tx = conn.transaction()?;
            (step.up)(&tx).map_err(|source| DbError::Migration {
                version: step.version,
                name: step.name.to_string(),
                source,
            })?;
            tx.pragma_update(None, "user_version", step.version)?;
            tx.commit()?;
            tracing::info!(
                registry = self.name,
                version = step.version,
                name = step.name,
                "migration applied"
            );
        }
        Ok(())
    }
}

/// Checkpoint the WAL and copy the database file to `.cascade/backups/`, returning the path.
/// The backup name encodes the registry, the pre-migration version, and a UTC timestamp.
pub fn backup_before_migrate(db_path: &Path, registry: &str, from_version: u32) -> Result<PathBuf> {
    // Best-effort WAL checkpoint so the copied file is self-contained.
    if let Ok(conn) = Connection::open(db_path) {
        let _ = conn.pragma_update(None, "wal_checkpoint", "TRUNCATE");
    }

    let backups_dir = backups_dir_for(db_path);
    std::fs::create_dir_all(&backups_dir).map_err(|source| DbError::Io {
        path: backups_dir.clone(),
        source,
    })?;

    let stem = db_path.file_stem().and_then(|s| s.to_str()).unwrap_or("db");
    let ts = chrono::Utc::now().format("%Y%m%dT%H%M%SZ");
    let dest = backups_dir.join(format!("{stem}-{registry}-pre-v{from_version}-{ts}.db"));
    std::fs::copy(db_path, &dest).map_err(|source| DbError::Io {
        path: dest.clone(),
        source,
    })?;
    Ok(dest)
}

/// Resolve the `.cascade/backups/` directory for a database path. We anchor it at the
/// database's own directory so each data root keeps its own backups.
fn backups_dir_for(db_path: &Path) -> PathBuf {
    db_path
        .parent()
        .map(|p| p.join("backups"))
        .unwrap_or_else(|| PathBuf::from("backups"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::conn::open_configured;

    fn reg() -> MigrationRegistry {
        MigrationRegistry::new(
            "test",
            vec![
                MigrationStep {
                    version: 1,
                    name: "create_t",
                    up: |tx| tx.execute_batch("CREATE TABLE t(id INTEGER PRIMARY KEY, v TEXT)"),
                },
                MigrationStep {
                    version: 2,
                    name: "add_col",
                    up: |tx| tx.execute_batch("ALTER TABLE t ADD COLUMN extra INTEGER"),
                },
            ],
        )
    }

    #[test]
    fn runs_pending_and_is_idempotent() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("m.db");
        let mut conn = open_configured(&path).unwrap();
        reg().run(&mut conn, Some(&path)).unwrap();
        let v: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap();
        assert_eq!(v, 2);
        // Second run is a no-op.
        reg().run(&mut conn, Some(&path)).unwrap();
        conn.execute("INSERT INTO t(v, extra) VALUES('a', 1)", [])
            .unwrap();
    }

    #[test]
    fn future_schema_is_rejected() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("f.db");
        let mut conn = open_configured(&path).unwrap();
        conn.pragma_update(None, "user_version", 99u32).unwrap();
        let err = reg().run(&mut conn, Some(&path)).unwrap_err();
        assert!(matches!(
            err,
            DbError::FutureSchema {
                found: 99,
                supported: 2
            }
        ));
    }

    #[test]
    fn backup_written_when_pending() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("b.db");
        {
            // Seed a v1 DB so a backup has something to copy.
            let mut conn = open_configured(&path).unwrap();
            MigrationRegistry::new(
                "test",
                vec![MigrationStep {
                    version: 1,
                    name: "create_t",
                    up: |tx| tx.execute_batch("CREATE TABLE t(id INTEGER PRIMARY KEY)"),
                }],
            )
            .run(&mut conn, Some(&path))
            .unwrap();
        }
        let mut conn = open_configured(&path).unwrap();
        reg().run(&mut conn, Some(&path)).unwrap();
        let backups = dir.path().join("backups");
        let count = std::fs::read_dir(&backups).unwrap().count();
        assert!(count >= 1, "expected a pre-migration backup file");
    }
}
