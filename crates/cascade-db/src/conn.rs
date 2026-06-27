//! Purpose: The single, canonical SQLite connection configuration for all of Cascade.
//! Inputs: a `rusqlite::Connection` (freshly opened) or a path to open.
//! Outputs: a connection with WAL, busy_timeout, synchronous=NORMAL, foreign_keys, and a
//!          64 MiB page cache applied — replacing the ad-hoc per-callsite PRAGMA soup.
//! Constraints: every database opened in Cascade MUST pass through here. No bespoke PRAGMA blocks.
//! SPORT: cascade-db / conn

use std::path::Path;

use rusqlite::Connection;

use crate::error::{DbError, Result};

/// Busy-timeout in milliseconds. Without this, concurrent writers fail instantly with
/// `SQLITE_BUSY`; WAL mode alone does not serialise writers. 5 s matches our daemon's
/// worst-case checkpoint window.
pub const BUSY_TIMEOUT_MS: u32 = 5_000;

/// Page-cache size in KiB, expressed as the negative form SQLite expects (`-N` => N KiB).
/// 65_536 KiB = 64 MiB shared read cache per connection.
const CACHE_SIZE_KIB: i64 = -65_536;

/// Apply Cascade's canonical PRAGMA set to an already-open connection.
///
/// This is idempotent and cheap; call it immediately after `Connection::open`.
/// WAL persists in the database header, so re-applying on every open is harmless.
pub fn configure_connection(conn: &Connection) -> Result<()> {
    // `journal_mode=WAL` is a no-op after the first time (it is stored in the header),
    // but `busy_timeout`, `synchronous`, `foreign_keys`, and `cache_size` are
    // per-connection and MUST be set every open.
    conn.busy_timeout(std::time::Duration::from_millis(BUSY_TIMEOUT_MS as u64))?;
    conn.pragma_update(None, "journal_mode", "WAL")?;
    conn.pragma_update(None, "synchronous", "NORMAL")?;
    conn.pragma_update(None, "foreign_keys", "ON")?;
    conn.pragma_update(None, "cache_size", CACHE_SIZE_KIB)?;
    // `temp_store=MEMORY` keeps transient indices/sorts off disk — measurable win for
    // the chunking + ANN write path.
    conn.pragma_update(None, "temp_store", "MEMORY")?;
    Ok(())
}

/// Open a SQLite database at `path`, creating parent directories as needed, and apply the
/// canonical configuration. This is the preferred entry point for every Cascade database.
pub fn open_configured(path: impl AsRef<Path>) -> Result<Connection> {
    let path = path.as_ref();
    if let Some(parent) = path.parent() {
        if !parent.as_os_str().is_empty() {
            std::fs::create_dir_all(parent).map_err(|source| DbError::Io {
                path: parent.to_path_buf(),
                source,
            })?;
        }
    }
    let conn = Connection::open(path)?;
    configure_connection(&conn)?;
    Ok(conn)
}

/// Open an in-memory database with the canonical configuration applied. Used by tests and
/// ephemeral caches. (WAL is silently ignored for `:memory:`, which is expected.)
pub fn open_in_memory() -> Result<Connection> {
    let conn = Connection::open_in_memory()?;
    // In-memory DBs reject WAL; apply only the relevant pragmas.
    conn.busy_timeout(std::time::Duration::from_millis(BUSY_TIMEOUT_MS as u64))?;
    conn.pragma_update(None, "foreign_keys", "ON")?;
    Ok(conn)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn configure_sets_busy_timeout_and_wal() {
        let dir = tempfile::tempdir().unwrap();
        let conn = open_configured(dir.path().join("t.db")).unwrap();
        let mode: String = conn
            .query_row("PRAGMA journal_mode", [], |r| r.get(0))
            .unwrap();
        assert_eq!(mode.to_uppercase(), "WAL");
        let fk: i64 = conn
            .query_row("PRAGMA foreign_keys", [], |r| r.get(0))
            .unwrap();
        assert_eq!(fk, 1);
    }

    #[test]
    fn open_creates_parent_dirs() {
        let dir = tempfile::tempdir().unwrap();
        let nested = dir.path().join("a/b/c/t.db");
        assert!(open_configured(&nested).is_ok());
        assert!(nested.exists());
    }
}
