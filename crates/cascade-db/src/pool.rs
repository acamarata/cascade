//! Purpose: r2d2 connection pool for write-heavy databases (tasks.db, cascade.db).
//! Inputs: a database path + pool size.
//! Outputs: a `DbPool` handing out canonically-configured connections.
//! Constraints: compiled only with `features = ["pool"]`. Each checkout re-applies PRAGMAs.
//! SPORT: cascade-db / pool

use std::path::Path;

use r2d2_sqlite::SqliteConnectionManager;

use crate::conn::configure_connection;
use crate::error::{DbError, Result};

/// A pooled set of configured SQLite connections.
pub type DbPool = r2d2::Pool<SqliteConnectionManager>;

/// Build a connection pool for `path` with up to `max_size` connections. Every connection
/// handed out has the canonical PRAGMA set applied via an init hook.
pub fn build_pool(path: impl AsRef<Path>, max_size: u32) -> Result<DbPool> {
    let path = path.as_ref();
    if let Some(parent) = path.parent() {
        if !parent.as_os_str().is_empty() {
            std::fs::create_dir_all(parent).map_err(|source| DbError::Io {
                path: parent.to_path_buf(),
                source,
            })?;
        }
    }
    let manager = SqliteConnectionManager::file(path).with_init(|c| {
        configure_connection(c).map_err(|e| {
            rusqlite::Error::SqliteFailure(
                rusqlite::ffi::Error::new(rusqlite::ffi::SQLITE_ERROR),
                Some(e.to_string()),
            )
        })
    });
    r2d2::Pool::builder()
        .max_size(max_size)
        .build(manager)
        .map_err(|e| DbError::Io {
            path: path.to_path_buf(),
            source: std::io::Error::other(e.to_string()),
        })
}
