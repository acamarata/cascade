//! # cascade-db — Cascade's embedded data layer
//!
//! Purpose: one place that owns how Cascade talks to SQLite. Provides the canonical
//! connection configuration, a versioned + backup-protected migration runner, an embedded
//! cache and job queue, canonical BLAKE3 hashing, and (default-on) an ANN vector store.
//!
//! Locked decisions this crate enforces:
//! - Redis is **never** required. The default runtime is fully embedded (SQLite + in-process).
//!   Redis is available only behind `features = ["redis-backend"]`.
//! - The migration runner lives **here** (not a separate crate) — mig-01.
//! - BLAKE3 is the single content hash — locked decision #2.
//!
//! Every Cascade database should be opened via [`open_configured`] (or a pool built on it),
//! never with a bare `Connection::open` followed by hand-rolled PRAGMA blocks.
//!
//! SPORT: cascade-db / lib

#![forbid(unsafe_op_in_unsafe_fn)]

pub mod cache;
pub mod conn;
pub mod error;
pub mod hash;
pub mod migrate;
pub mod queue;

#[cfg(feature = "vec")]
pub mod vector;

#[cfg(feature = "pool")]
pub mod pool;

pub use conn::{configure_connection, open_configured, open_in_memory, BUSY_TIMEOUT_MS};
pub use error::{DbError, Result};
pub use hash::{content_hash, content_hash_str};
pub use migrate::{backup_before_migrate, MigrationRegistry, MigrationStep};

use std::path::{Path, PathBuf};

/// Standard filenames for the databases that make up a Cascade data root. Centralised so
/// callers stop hard-coding these strings at seven different sites.
pub mod db_files {
    /// Event bus + task/job storage.
    pub const TASKS: &str = "tasks.db";
    /// Account usage / quota accounting.
    pub const USAGE: &str = "usage.db";
    /// Daemon/runtime state.
    pub const STATE: &str = "cascade_state.db";
    /// RAG index + embeddings.
    pub const RAG: &str = "cascade.db";
    /// Project registry (rag-13).
    pub const REGISTRY: &str = "registry.db";
}

/// A handle to a Cascade data root (typically `~/.cascade/`). The single entry point for
/// opening the layer's databases with consistent configuration.
#[derive(Debug, Clone)]
pub struct CascadeDb {
    root: PathBuf,
}

impl CascadeDb {
    /// Bind to a data root, creating it if necessary.
    pub fn open(data_dir: impl AsRef<Path>) -> Result<Self> {
        let root = data_dir.as_ref().to_path_buf();
        std::fs::create_dir_all(&root).map_err(|source| DbError::Io {
            path: root.clone(),
            source,
        })?;
        Ok(Self { root })
    }

    /// The data root directory.
    pub fn root(&self) -> &Path {
        &self.root
    }

    /// Full path to a named database within this root.
    pub fn path(&self, file: &str) -> PathBuf {
        self.root.join(file)
    }

    /// Open a configured connection to a named database within this root.
    pub fn connect(&self, file: &str) -> Result<rusqlite::Connection> {
        open_configured(self.path(file))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn opens_root_and_connects() {
        let dir = tempfile::tempdir().unwrap();
        let db = CascadeDb::open(dir.path().join(".cascade")).unwrap();
        let conn = db.connect(db_files::TASKS).unwrap();
        conn.execute_batch("CREATE TABLE t(x INTEGER)").unwrap();
        assert!(db.path(db_files::TASKS).exists());
    }
}
