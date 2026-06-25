//! # google_provision::types
//!
//! ## Purpose
//! Data types for the GCP provisioning flow: errors, options, checkpoint, and
//! multi-key result structs. Kept separate from the client to allow independent
//! use/import without pulling in the HTTP client.

use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::google_oauth::OAuthError;

// ── Errors ────────────────────────────────────────────────────────────────────

/// Errors from the GCP provisioning flow.
#[derive(Debug, Error)]
pub enum ProvisionError {
    /// HTTP transport error from a GCP API call.
    #[error("GCP API error: {0}")]
    GcpApi(String),
    /// Project did not reach ACTIVE state within the polling deadline.
    #[error("Project activation timed out after 30s")]
    ActivationTimeout,
    /// Key validation failed (wraps OAuthError).
    #[error("Key validation failed: {0}")]
    KeyValidation(#[from] OAuthError),
    /// HTTP client error.
    #[error("HTTP error: {0}")]
    Http(#[from] reqwest::Error),
    /// I/O error reading/writing the provisioning checkpoint.
    #[error("Checkpoint I/O error: {0}")]
    Checkpoint(String),
}

// ── ProvisionOptions ──────────────────────────────────────────────────────────

/// Options controlling the GFP multi-key provisioning loop.
///
/// ## Purpose
/// Provides per-call policy knobs to `full_auto_multi`. All defaults are
/// **conservative** to protect Google accounts from ToS / abuse-detection bans.
///
/// ## Conservative defaults
/// - `max_keys_per_account`: 3 — never create more than 3 keys by default.
/// - `cooldown_secs`: 30 — 30 s between key creations.
/// - `auto_max`: false — the ceiling can never be exceeded without opt-in.
/// - `dry_run`: false — real provisioning unless explicitly simulated.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProvisionOptions {
    /// Hard ceiling on the number of API keys created per Google account in one
    /// `full_auto_multi` call. The effective limit is `min(count, max_keys_per_account)`.
    pub max_keys_per_account: u32,

    /// Seconds to wait between successive key creations for the same account.
    /// Zero disables the cooldown (not recommended for production use).
    pub cooldown_secs: u64,

    /// When `false` (safe default), `count` is always capped at
    /// `max_keys_per_account`. When `true`, `count` may exceed the ceiling.
    ///
    /// **Never set to `true` without explicit user opt-in.**
    pub auto_max: bool,

    /// When `true`, simulate the provisioning loop without calling GCP APIs.
    /// Useful for integration tests and UI smoke tests.
    pub dry_run: bool,
}

impl Default for ProvisionOptions {
    fn default() -> Self {
        Self {
            max_keys_per_account: 3,
            cooldown_secs: 30,
            auto_max: false,
            dry_run: false,
        }
    }
}

// ── Provisioning checkpoint ───────────────────────────────────────────────────

/// Persisted state written after each successful key creation.
///
/// Stored at `~/.cascade/provisioning-state.json`. A re-run reads this and
/// skips already-created keys (idempotent by project_id presence in the pool).
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct ProvisioningCheckpoint {
    /// The Google account email being provisioned.
    pub account_email: String,
    /// Number of keys successfully created so far.
    pub keys_created: u32,
    /// Unix timestamp (seconds) of the last successful key creation.
    pub last_ts: u64,
    /// Project IDs already created (used to skip on resume).
    pub created_project_ids: Vec<String>,
}

impl ProvisioningCheckpoint {
    /// Default path: `~/.cascade/provisioning-state.json`.
    pub fn default_path() -> PathBuf {
        dirs::home_dir()
            .unwrap_or_else(|| PathBuf::from("/tmp"))
            .join(".cascade")
            .join("provisioning-state.json")
    }

    /// Load checkpoint from disk; returns a fresh default if absent or unreadable.
    pub fn load(path: &Path) -> Self {
        std::fs::read_to_string(path)
            .ok()
            .and_then(|s| serde_json::from_str(&s).ok())
            .unwrap_or_default()
    }

    /// Write checkpoint atomically (tmp + rename).
    pub fn save(&self, path: &Path) -> Result<(), ProvisionError> {
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)
                .map_err(|e| ProvisionError::Checkpoint(e.to_string()))?;
        }
        let tmp_path = path.with_extension("json.tmp");
        let json = serde_json::to_string_pretty(self)
            .map_err(|e| ProvisionError::Checkpoint(e.to_string()))?;
        std::fs::write(&tmp_path, &json).map_err(|e| ProvisionError::Checkpoint(e.to_string()))?;
        std::fs::rename(&tmp_path, path).map_err(|e| ProvisionError::Checkpoint(e.to_string()))?;
        Ok(())
    }
}

// ── ProvisionMultiResult ──────────────────────────────────────────────────────

/// Result of a `full_auto_multi` provisioning loop run.
///
/// Partial success is valid: if the loop hit a rate-limit or the ceiling, it
/// stops gracefully and reports how many keys were created.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProvisionMultiResult {
    /// Keys successfully provisioned in this run (api_key + project_id pairs).
    pub keys: Vec<ProvisionedKey>,
    /// How many keys were created (may be less than `count` if capped or failed).
    pub keys_created: u32,
    /// The effective ceiling that was applied.
    pub effective_ceiling: u32,
    /// True if the loop was stopped by a rate-limit or quota error (partial success).
    pub rate_limited: bool,
    /// User-visible ToS/safety warning — always present to surface the risk.
    pub tos_warning: String,
    /// Any per-key errors encountered (non-fatal if some keys succeeded).
    pub errors: Vec<String>,
}

/// A single successfully provisioned key.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProvisionedKey {
    /// The Gemini API key string.
    pub api_key: String,
    /// The GCP project ID created.
    pub project_id: String,
}

/// ToS warning surfaced on every `full_auto_multi` result.
pub(crate) const TOS_WARNING: &str =
    "WARNING: Creating multiple GCP API keys on a real Google account may violate \
     Google Cloud Terms of Service or trigger abuse detection. Use conservative \
     defaults (max 3 keys, 30s cooldown). You are responsible for compliance.";
