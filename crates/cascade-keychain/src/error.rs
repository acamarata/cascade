//! KeychainError — error variants for all keychain operations.
//!
//! # Purpose
//! Unified error enum covering OS-keychain and in-memory backend failure modes.
//!
//! # Security invariant
//! No variant may carry a secret value. Only service/account names or non-secret
//! OS error descriptions may appear in Display output.
//!
//! # SPORT
//! MASTER-SECURITY.md: os-keychain-crate / KeychainError

use thiserror::Error;

/// Errors returned by [`crate::Keychain`] implementations.
///
/// # Security invariant
/// Secret *values* MUST NOT appear in any variant's message or fields.
/// Only service names, account names, or OS error codes may appear.
#[derive(Debug, Error)]
pub enum KeychainError {
    /// The requested item does not exist in the keychain.
    #[error("keychain item not found")]
    NotFound,

    /// The caller lacks permission to access the requested item.
    #[error("permission denied accessing keychain item")]
    PermissionDenied,

    /// The keychain backend is unavailable (D-Bus not running, daemon missing, etc.).
    ///
    /// The inner string describes the backend failure — MUST NOT contain secret values.
    #[error("keychain unavailable: {0}")]
    Unavailable(String),

    /// Any other OS or implementation error.
    ///
    /// The inner string MUST NOT contain secret values.
    #[error("keychain error: {0}")]
    Other(String),
}
