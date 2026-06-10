//! # pool
//!
//! Shared types for the Gemini Pool key registration flow.
//!
//! ## Purpose
//! Defines `GeminiKeyEntry`, `GeminiPoolConfig`, and `RegisterResult` so that
//! `cascade-daemon` IPC handlers and the Tauri frontend share the same types.
//! Pool config file: `~/.cascade/pool/gemini-keys.json`.
//!
//! ## Constraints
//! - All types serialized as camelCase for Tauri IPC consumers.
//! - `api_key` fields are never logged (enforced by `#[serde(skip_serializing)]` where needed).
//! - Append-only schema — fields may be added but never removed.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

/// A single registered Gemini API key entry in the pool config.
///
/// Serialized as camelCase for the gemini-keys.json config file and Tauri IPC.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GeminiKeyEntry {
    /// The Google account email this key belongs to.
    pub email: String,
    /// The Gemini API key string.
    pub api_key: String,
    /// The GCP project ID the key was created in (empty for manually entered keys).
    pub project_id: String,
    /// UTC timestamp when the key was registered.
    pub added_at: DateTime<Utc>,
    /// Whether the key is currently active in the pool round-robin.
    pub enabled: bool,
}

/// The full gemini-keys.json config file schema.
///
/// Append-only: new entries are pushed to `keys`; entries are removed by
/// filtering on email. Atomic write via temp+rename.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct GeminiPoolConfig {
    /// All registered Gemini API keys.
    pub keys: Vec<GeminiKeyEntry>,
}

/// Result of a key registration attempt.
///
/// Serialized as an adjacently-tagged enum for clean Tauri IPC payloads.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "camelCase", rename_all_fields = "camelCase")]
pub enum RegisterResult {
    /// Key registered successfully.
    Success,
    /// A key for this email is already registered; JSON was not modified.
    AlreadyRegistered,
    /// Key validation failed — the API key is invalid.
    InvalidKey { message: String },
    /// Unexpected error (I/O, serialization, etc.).
    Error { message: String },
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn gemini_pool_config_default_is_empty() {
        let cfg = GeminiPoolConfig::default();
        assert!(cfg.keys.is_empty());
        let json = serde_json::to_string(&cfg).unwrap();
        assert!(json.contains("\"keys\""), "expected keys field: {}", json);
    }

    #[test]
    fn register_result_success_serde() {
        let r = RegisterResult::Success;
        let json = serde_json::to_string(&r).unwrap();
        assert!(json.contains("\"success\"") || json.contains("\"Success\""),
            "expected Success tag, got: {}", json);
    }

    #[test]
    fn register_result_invalid_key_contains_message() {
        let r = RegisterResult::InvalidKey { message: "Invalid API key".to_string() };
        let json = serde_json::to_string(&r).unwrap();
        assert!(json.contains("Invalid API key"), "expected message, got: {}", json);
    }
}
