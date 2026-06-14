//! Error types for cascade-ccapi.
//!
//! # Purpose
//! Single crate-local error enum so all bridge/driver/quota errors
//! unify into one `CcApiError` that the CLI layer can format.
//!
//! # Constraints
//! Does not depend on `cascade_types::error` to keep this crate lightweight.

use thiserror::Error;

/// All errors produced by `cascade-ccapi`.
#[derive(Debug, Error)]
pub enum CcApiError {
    /// The feature is disabled in config (`[experimental] cc_api_proxy = false`).
    #[error(
        "CC API proxy is disabled (experimental — set `[experimental] cc_api_proxy = true` \
         in config.toml after reading .github/docs/cc-api-proxy-beta.md)"
    )]
    Disabled,

    /// Claude Code is not installed on the PATH.
    #[error("Claude Code CLI (`claude`) is not installed or not found on PATH")]
    CcNotInstalled,

    /// Claude Code is installed but has no authenticated session.
    #[error("Claude Code is installed but not authenticated; run `claude` interactively first")]
    CcNotAuthenticated,

    /// The token-bucket quota was exceeded.
    #[error("rate limit exceeded: {0}")]
    QuotaExceeded(String),

    /// The driver reported an I/O error driving the CC process.
    #[error("process driver error: {0}")]
    DriverError(String),

    /// The bridge HTTP server failed to bind or run.
    #[error("bridge server error: {0}")]
    ServerError(String),

    /// A session was not found by its ID.
    #[error("session not found: {0}")]
    SessionNotFound(String),

    /// Generic internal error.
    #[error("internal error: {0}")]
    Internal(String),
}

impl From<std::io::Error> for CcApiError {
    fn from(e: std::io::Error) -> Self {
        CcApiError::DriverError(e.to_string())
    }
}
