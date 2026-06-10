//! # provision
//!
//! Shared types for the GCP project + API key provisioning flow.
//!
//! ## Purpose
//! Defines `ProvisionMode`, `ProvisionRequest`, and `ProvisionResult` so that
//! `cascade-providers`, `cascade-daemon` IPC handlers, and the Tauri frontend
//! all share the same canonical type definitions.
//!
//! ## Constraints
//! - All types serialized as camelCase for Tauri IPC consumers.
//! - No business logic — pure data types only.

use serde::{Deserialize, Serialize};

/// Which provisioning mode the wizard step is using.
///
/// Serialized as camelCase strings for Tauri IPC consumers.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "camelCase")]
pub enum ProvisionMode {
    /// Fully automated GCP project + API key creation via OAuth access token.
    FullAuto,
    /// User follows Cloud Console links; wizard polls for pasted key.
    Guided,
    /// User pastes a pre-existing API key; wizard validates it.
    Manual,
}

/// Input parameters for a GCP provisioning request.
///
/// Serialized as camelCase for Tauri IPC consumers.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProvisionRequest {
    /// The Google account email being provisioned.
    pub account_email: String,
    /// Which provisioning mode to use.
    pub mode: ProvisionMode,
    /// OAuth client_id — required for FullAuto mode.
    pub client_id: Option<String>,
    /// OAuth client_secret — required for FullAuto mode.
    pub client_secret: Option<String>,
}

/// Status of an in-progress or completed provisioning operation.
///
/// Serialized as camelCase for polling calls.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ProvisionStatus {
    /// The account email being provisioned.
    pub account_email: String,
    /// Human-readable status message (e.g. "Creating project…", "Complete").
    pub status: String,
    /// True when provisioning has finished (success or error).
    pub done: bool,
    /// Non-empty if provisioning ended with an error.
    pub error: Option<String>,
}

/// Result of a provisioning attempt.
///
/// Serialized as an adjacently-tagged enum for clean Tauri IPC payloads.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "camelCase", rename_all_fields = "camelCase")]
pub enum ProvisionResult {
    /// Provisioning completed — api_key and project_id are ready.
    Success {
        /// The provisioned Gemini API key string.
        api_key: String,
        /// The GCP project ID created or used.
        project_id: String,
    },
    /// Guided mode — user should navigate to the Cloud Console URL for this step.
    GuidedPending {
        /// The Cloud Console deep-link URL for the current step.
        next_url: String,
        /// Which step (0 = create project, 1 = enable API, 2 = create key).
        step: u8,
    },
    /// Provisioning failed with a user-readable message.
    Error {
        /// User-readable failure message.
        message: String,
    },
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn provision_mode_round_trips() {
        for mode in [ProvisionMode::FullAuto, ProvisionMode::Guided, ProvisionMode::Manual] {
            let json = serde_json::to_string(&mode).unwrap();
            let back: ProvisionMode = serde_json::from_str(&json).unwrap();
            assert_eq!(mode, back);
        }
    }

    #[test]
    fn provision_request_camel_case() {
        let req = ProvisionRequest {
            account_email: "test@example.com".to_string(),
            mode: ProvisionMode::Manual,
            client_id: None,
            client_secret: None,
        };
        let json = serde_json::to_string(&req).unwrap();
        assert!(json.contains("\"accountEmail\""), "expected camelCase: {}", json);
        assert!(json.contains("\"clientId\""), "expected camelCase: {}", json);
    }
}
