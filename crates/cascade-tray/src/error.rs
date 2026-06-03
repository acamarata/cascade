//! TrayError — typed error variants for the cascade-tray abstraction layer.
//!
//! Purpose: Provide a single error type that covers all failure modes that
//!   a TrayHandle implementation can raise, without leaking platform-specific
//!   details into the public API.
//! Inputs: constructed by platform backend impls or serialization helpers.
//! Outputs: returned via `Result<_, TrayError>` from TrayHandle methods.
//! Constraints: must implement `std::error::Error` + `Send + Sync` so callers
//!   can propagate it through async contexts (e.g. `tokio::spawn`).
//! SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray

use thiserror::Error;

/// All error variants that a [`crate::TrayHandle`] implementation can produce.
#[derive(Debug, Error)]
pub enum TrayError {
    /// A platform-native tray operation failed.
    /// The inner string carries the OS error message.
    #[error("tray platform error: {0}")]
    Platform(String),

    /// An I/O operation underlying the tray (e.g. icon file read) failed.
    #[error("tray I/O error: {0}")]
    Io(#[from] std::io::Error),

    /// Serializing or deserializing tray state failed.
    /// Used when IPC payloads are converted to/from JSON.
    #[error("tray serialize error: {0}")]
    Serialize(String),
}
