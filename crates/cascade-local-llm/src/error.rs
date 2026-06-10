//! Local LLM error types.
//!
//! # Purpose
//!
//! Defines `LocalModelError` — the single error enum for all failure modes
//! in the cascade-local-llm crate.  Converts into
//! [`cascade_providers::ProviderError::LocalModelError`] so callers only
//! need to match one type.
//!
//! # Inputs / Outputs
//!
//! `LocalModelError` implements `std::error::Error` via `thiserror`.
//! A `From<LocalModelError> for ProviderError` blanket conversion is provided
//! so `?` works across the crate boundary.
//!
//! # Constraints
//!
//! - Never embed raw weight bytes or credential values in error strings.
//! - `WeightsNotFound` carries only the *path* — never actual model data.

use std::path::PathBuf;

use thiserror::Error;

// ── LocalModelError ────────────────────────────────────────────────────────────

/// All failure modes in the local LLM crate.
#[derive(Debug, Error)]
pub enum LocalModelError {
    /// The model weights directory does not exist.
    ///
    /// The user needs to download the model first (see T-P3-E04-18).
    #[error(
        "model weights not found at '{path}'; run `cascade models download gemma-2-2b` \
         (T-P3-E04-18) to fetch them"
    )]
    WeightsNotFound {
        /// The path that was checked.
        path: PathBuf,
    },

    /// The tokenizer file could not be loaded.
    #[error("tokenizer load failed for '{path}': {reason}")]
    TokenizerLoad {
        /// Path of the tokenizer file that failed to load.
        path: PathBuf,
        /// Underlying tokenizer error message.
        reason: String,
    },

    /// A candle tensor or model operation failed.
    #[error("candle error: {0}")]
    Candle(String),

    /// The generation stream ended unexpectedly.
    #[error("generation stream interrupted: {0}")]
    StreamInterrupted(String),

    /// The requested model is not supported by this runner.
    #[error("unsupported model '{0}'; this runner only supports gemma-2-2b")]
    UnsupportedModel(String),

    /// A generic I/O error.
    #[error("I/O error: {0}")]
    Io(#[from] std::io::Error),

    // ── Downloader errors (T-P3-E04-18) ───────────────────────────────────────

    /// Insufficient disk space to download the model.
    ///
    /// The downloader requires ≥2× the model file size to be free on the
    /// target volume before starting the download.
    #[error(
        "insufficient disk space for model download: need {required} bytes, \
         only {available} bytes available"
    )]
    InsufficientDiskSpace {
        /// Bytes required (2× model file size).
        required: u64,
        /// Bytes available on the target volume.
        available: u64,
    },

    /// SHA-256 checksum of the downloaded file does not match the expected value.
    ///
    /// The `.tmp` file has been deleted before this error is returned.
    #[error(
        "SHA-256 mismatch for model '{model_id}': expected {expected}, got {actual}"
    )]
    ChecksumMismatch {
        /// Model identifier.
        model_id: String,
        /// Expected SHA-256 hex digest (from the model registry).
        expected: String,
        /// Actual SHA-256 hex digest of the downloaded bytes.
        actual: String,
    },
}

// ── From conversion ───────────────────────────────────────────────────────────

impl From<LocalModelError> for cascade_providers::ProviderError {
    fn from(e: LocalModelError) -> Self {
        cascade_providers::ProviderError::LocalModelError(e.to_string())
    }
}

// ── From candle_core::Error → LocalModelError ─────────────────────────────────

impl From<candle_core::Error> for LocalModelError {
    fn from(e: candle_core::Error) -> Self {
        LocalModelError::Candle(e.to_string())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn weights_not_found_message_contains_path() {
        let err = LocalModelError::WeightsNotFound {
            path: std::path::PathBuf::from("/tmp/no-model"),
        };
        let msg = err.to_string();
        assert!(msg.contains("/tmp/no-model"), "message: {msg}");
        assert!(msg.contains("cascade models download"), "message: {msg}");
    }

    #[test]
    fn tokenizer_load_message_contains_path_and_source() {
        let err = LocalModelError::TokenizerLoad {
            path: std::path::PathBuf::from("/tmp/tok.model"),
            reason: "file not found".into(),
        };
        let msg = err.to_string();
        assert!(msg.contains("/tmp/tok.model"), "message: {msg}");
        assert!(msg.contains("file not found"), "message: {msg}");
    }

    #[test]
    fn into_provider_error_is_local_model_variant() {
        let local = LocalModelError::WeightsNotFound {
            path: std::path::PathBuf::from("/x"),
        };
        let provider_err: cascade_providers::ProviderError = local.into();
        assert!(
            matches!(
                provider_err,
                cascade_providers::ProviderError::LocalModelError(_)
            ),
            "unexpected variant: {provider_err:?}"
        );
    }
}
