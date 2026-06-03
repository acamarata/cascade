//! Error types for the Cascade AI context framework.
//!
//! All fallible operations return `Result<T, CascadeError>`. The variants are
//! grouped by domain so callers can match at a coarse or fine level.

use std::path::PathBuf;
use thiserror::Error;

/// The unified error type for all Cascade operations.
///
/// Variants are designed to carry enough context for `cascade doctor` to
/// surface actionable diagnostics without requiring callers to attach extra
/// strings manually.
#[derive(Debug, Error)]
pub enum CascadeError {
    // ── I/O ───────────────────────────────────────────────────────────────
    /// A file-system operation failed. Includes the path and the operation
    /// attempted so error messages are self-contained.
    #[error("IO error on {path} during {operation}: {source}")]
    Io {
        path: PathBuf,
        operation: &'static str,
        #[source]
        source: std::io::Error,
    },

    /// A symlink was expected but the path is either missing or not a symlink.
    #[error("Symlink not found or invalid at {path}")]
    InvalidSymlink { path: PathBuf },

    /// A required path does not exist.
    #[error("Path not found: {path}")]
    PathNotFound { path: PathBuf },

    // ── Cascade tier / resolution ─────────────────────────────────────────
    /// No `.cascade/CASCADE.md` was found at any tier during resolution.
    /// Returned only when the caller requires at least one tier to be present.
    #[error("No cascade tiers found starting from {cwd}")]
    NoTiersFound { cwd: PathBuf },

    /// A tier file exists but cannot be read (permissions, encoding, etc.).
    #[error("Failed to read tier {tier} at {path}: {source}")]
    TierReadFailed {
        tier: String,
        path: PathBuf,
        #[source]
        source: std::io::Error,
    },

    /// The resolved cascade text exceeded the configured size limit.
    #[error("Resolved cascade text exceeds limit: {size} bytes > {limit} bytes")]
    CascadeTooLarge { size: usize, limit: usize },

    // ── Config ────────────────────────────────────────────────────────────
    /// Configuration file could not be parsed (TOML/YAML/JSON).
    #[error("Config parse error in {path}: {detail}")]
    ConfigParse { path: PathBuf, detail: String },

    /// A required config key is missing.
    #[error("Missing required config key: {key}")]
    ConfigMissingKey { key: String },

    /// A config value has the wrong type or is out of range.
    #[error("Invalid config value for {key}: {detail}")]
    ConfigInvalidValue { key: String, detail: String },

    // ── Embedding / provider ──────────────────────────────────────────────
    /// The embedding provider returned an error response.
    #[error("Embedding provider error ({provider}): {detail}")]
    EmbeddingFailed { provider: String, detail: String },

    /// The embedding dimension returned by the provider does not match the
    /// expected dimension for the configured index.
    #[error("Embedding dimension mismatch: expected {expected}, got {actual}")]
    EmbeddingDimensionMismatch { expected: usize, actual: usize },

    // ── Retrieval / RAG ───────────────────────────────────────────────────
    /// The vector index is not yet built or is stale.
    #[error("Index not ready: {detail}")]
    IndexNotReady { detail: String },

    /// A retrieval query returned an error from the underlying store.
    #[error("Retrieval error: {detail}")]
    RetrievalFailed { detail: String },

    // ── Chunking / parsing ────────────────────────────────────────────────
    /// A parser does not support the given file type.
    #[error("Unsupported file type for parsing: {extension}")]
    UnsupportedFileType { extension: String },

    /// Parsing the document produced an error.
    #[error("Parse error in {path}: {detail}")]
    ParseFailed { path: PathBuf, detail: String },

    // ── Key storage ───────────────────────────────────────────────────────
    /// The requested key was not found in the key store.
    #[error("Key not found: {key_id}")]
    KeyNotFound { key_id: String },

    /// The key store backend returned an error.
    #[error("Key storage error ({backend}): {detail}")]
    KeyStorageFailed { backend: String, detail: String },

    // ── Daemon / IPC ──────────────────────────────────────────────────────
    /// The daemon socket is not reachable. The daemon may not be running.
    #[error("Daemon unreachable at {socket}: {detail}")]
    DaemonUnreachable { socket: PathBuf, detail: String },

    /// A protocol framing or serialization error in the daemon IPC channel.
    #[error("IPC protocol error: {detail}")]
    IpcProtocolError { detail: String },

    // ── Agent ─────────────────────────────────────────────────────────────
    /// An agent returned an error or could not be dispatched.
    #[error("Agent error (tier {tier:?}): {detail}")]
    AgentFailed {
        tier: crate::agent::Tier,
        detail: String,
    },

    // ── Schema ────────────────────────────────────────────────────────────
    /// A store's persisted schema version does not match the compiled schema
    /// version. Returned when a store reads an existing database table with
    /// a different schema_version field than it expects.
    #[error("Schema mismatch: expected schema version {expected}, found {found}")]
    SchemaMismatch { expected: u32, found: u32 },

    // ── Generic ───────────────────────────────────────────────────────────
    /// A catch-all for errors that do not fit a more specific variant.
    /// Prefer a specific variant; use this only during initial prototyping.
    #[error("{0}")]
    Other(String),

    /// Wraps an arbitrary boxed error. Useful for trait implementations that
    /// do not know the concrete error type.
    #[error(transparent)]
    Boxed(#[from] Box<dyn std::error::Error + Send + Sync>),
}

/// Convenience alias used throughout the crate.
pub type Result<T> = std::result::Result<T, CascadeError>;

impl CascadeError {
    /// Construct an [`Io`](CascadeError::Io) error with context.
    ///
    /// # Example
    /// ```rust
    /// # use cascade_types::error::CascadeError;
    /// # use std::path::PathBuf;
    /// let err = CascadeError::io(PathBuf::from("/tmp/x"), "read", std::io::Error::last_os_error());
    /// ```
    pub fn io(path: impl Into<PathBuf>, operation: &'static str, source: std::io::Error) -> Self {
        CascadeError::Io {
            path: path.into(),
            operation,
            source,
        }
    }

    /// Returns `true` if this error is recoverable by running `cascade doctor --fix`.
    pub fn is_auto_fixable(&self) -> bool {
        matches!(
            self,
            CascadeError::InvalidSymlink { .. } | CascadeError::IndexNotReady { .. }
        )
    }
}
