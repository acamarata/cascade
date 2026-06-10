//! # auto_auth
//!
//! Shared types for the auto-auth harness detection and import flow.
//!
//! ## Purpose
//! Defines `AuthSource`, `AuthType`, `DiscoveredAccount`, and `ImportResult`
//! as the canonical types shared between `cascade-providers`, `cascade-daemon`
//! IPC handlers, and the Tauri frontend.
//!
//! ## Constraints
//! - All types serialized as camelCase for Tauri IPC consumers.
//! - No business logic — pure data types only.
//! - API key / token values are never stored in these types (hints only).

use serde::{Deserialize, Serialize};

/// Which harness or source the discovered account came from.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub enum AuthSource {
    /// Claude Code (`~/.claude/`)
    ClaudeCode,
    /// OpenCode (`~/.config/opencode/`)
    OpenCode,
    /// Codex CLI (`~/.codex/` or `~/.config/codex/`)
    Codex,
    /// Cursor (`~/Library/Application Support/Cursor/`)
    Cursor,
    /// Environment variable (e.g. `ANTHROPIC_API_KEY`)
    EnvVar,
}

/// What type of authentication credential was detected.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub enum AuthType {
    /// An OAuth token belonging to another app (not directly importable).
    OAuthToken,
    /// A static API key stored on disk.
    ApiKey,
    /// A static API key in an environment variable (directly importable).
    EnvApiKey,
}

/// A single discovered harness account or credential entry.
///
/// Serialized as camelCase for Tauri IPC consumers.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct DiscoveredAccount {
    /// Which harness or source this account was found in.
    pub source: AuthSource,
    /// Email address or a human-readable hint (e.g. "env: ANTHROPIC_API_KEY").
    pub email_or_hint: String,
    /// Provider name (e.g. "anthropic", "openai", "google").
    pub provider: String,
    /// Type of the detected credential.
    pub auth_type: AuthType,
    /// True when Cascade can use this credential directly (env API keys).
    /// False when the credential belongs to another app (OAuth tokens, Cursor).
    pub importable: bool,
}

/// Result of an import action on selected `DiscoveredAccount` entries.
///
/// Serialized as camelCase for Tauri IPC consumers.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
#[serde(rename_all = "camelCase")]
pub struct ImportResult {
    /// Hints of providers successfully imported into cascade-keychain.
    pub imported: Vec<String>,
    /// Hints of providers skipped (not importable or deselected).
    pub skipped: Vec<String>,
    /// Error messages from any failed import attempts.
    pub errors: Vec<String>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn discovered_account_camel_case() {
        let account = DiscoveredAccount {
            source: AuthSource::ClaudeCode,
            email_or_hint: "test@example.com".to_string(),
            provider: "anthropic".to_string(),
            auth_type: AuthType::OAuthToken,
            importable: false,
        };
        let json = serde_json::to_string(&account).unwrap();
        assert!(
            json.contains("\"emailOrHint\""),
            "expected camelCase: {}",
            json
        );
        assert!(
            json.contains("\"authType\""),
            "expected camelCase: {}",
            json
        );
        assert!(
            json.contains("\"claudeCode\""),
            "expected claudeCode: {}",
            json
        );
        assert!(
            json.contains("\"oAuthToken\"")
                || json.contains("\"oauthToken\"")
                || json.contains("\"oAuthToken\""),
            "expected OAuthToken serialized: {}",
            json
        );
    }

    #[test]
    fn import_result_default_is_empty() {
        let r = ImportResult::default();
        assert!(r.imported.is_empty());
        assert!(r.skipped.is_empty());
        assert!(r.errors.is_empty());
    }
}
