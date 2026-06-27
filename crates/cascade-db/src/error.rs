//! Purpose: Single error type for the embedded data layer.
//! Inputs: rusqlite + io failures bubbling up from connection/migration/cache code.
//! Outputs: `DbError` with a `Result<T>` alias used across the crate.
//! Constraints: no panics on recoverable failures; migration future-version is a typed error.
//! SPORT: cascade-db / error

use std::path::PathBuf;

/// Errors surfaced by the embedded data layer.
#[derive(Debug, thiserror::Error)]
pub enum DbError {
    /// Underlying SQLite failure.
    #[error("sqlite error: {0}")]
    Sqlite(#[from] rusqlite::Error),

    /// Filesystem failure (open, backup copy, directory creation).
    #[error("io error at {path:?}: {source}")]
    Io {
        /// Path involved in the failure, if known.
        path: PathBuf,
        /// Underlying io error.
        source: std::io::Error,
    },

    /// The database is at a schema version newer than this binary understands.
    /// Refusing to proceed protects user data from a downgrade corrupting it.
    #[error("database schema v{found} is newer than supported v{supported}; upgrade Cascade or restore a backup")]
    FutureSchema {
        /// `PRAGMA user_version` read from the database.
        found: u32,
        /// Highest version this binary's migration registry knows.
        supported: u32,
    },

    /// A migration step failed; the pre-migration backup (if any) is preserved.
    #[error("migration to v{version} ('{name}') failed: {source}")]
    Migration {
        /// Target version of the failed step.
        version: u32,
        /// Human-readable step name.
        name: String,
        /// Underlying cause.
        source: rusqlite::Error,
    },

    /// A backend (Redis) was requested but the feature is not compiled in.
    #[error("backend '{0}' is not available in this build (enable the matching cargo feature)")]
    BackendUnavailable(&'static str),
}

/// Crate-wide result alias.
pub type Result<T> = std::result::Result<T, DbError>;
