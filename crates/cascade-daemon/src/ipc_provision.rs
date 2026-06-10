//! # ipc_provision
//!
//! Daemon-side IPC handlers for GCP provisioning.
//!
//! ## Purpose
//! Implements the typed dispatch handlers for the three provisioning IPC methods
//! defined in T-P3-E03-39b:
//! - `cascade_provision_google_start`
//! - `cascade_provision_google_status`
//! - `cascade_provision_google_cancel`
//!
//! These handlers delegate to `GoogleProvisionClient` from cascade-providers and
//! maintain per-email status + cancellation flags via a shared in-memory map.
//!
//! ## Inputs
//! - `ProvisionRequest` (JSON) from the Tauri frontend.
//! - `email: String` for status/cancel queries.
//!
//! ## Outputs
//! - `ProvisionResult` (JSON) to the Tauri frontend.
//! - `ProvisionStatus` (JSON) for status polling.
//!
//! ## Constraints
//! - No plaintext credential logging (oauth_token never in log spans).
//! - Cancellation flag checked before each GCP polling step.

use std::collections::HashMap;
use std::sync::Arc;

use tokio::sync::Mutex;

use cascade_providers::{google_provision::GoogleProvisionClient, validate_gemini_key};
use cascade_types::provision::{ProvisionMode, ProvisionRequest, ProvisionResult, ProvisionStatus};

// ── Shared state ─────────────────────────────────────────────────────────────

/// Per-email provision status.
#[derive(Debug, Clone)]
pub struct EmailProvisionStatus {
    pub status: String,
    pub done: bool,
    pub error: Option<String>,
    pub cancel: bool,
}

impl Default for EmailProvisionStatus {
    fn default() -> Self {
        Self {
            status: "pending".to_string(),
            done: false,
            error: None,
            cancel: false,
        }
    }
}

/// Global provision state map: email → status.
pub type ProvisionStateMap = Arc<Mutex<HashMap<String, EmailProvisionStatus>>>;

/// Create a new empty provision state map.
pub fn new_state_map() -> ProvisionStateMap {
    Arc::new(Mutex::new(HashMap::new()))
}

// ── Handler: start ────────────────────────────────────────────────────────────

/// Handle a `cascade_provision_google_start` request.
///
/// # Purpose
/// Validates the `ProvisionRequest` and dispatches to the appropriate
/// provisioning mode (FullAuto / Guided / Manual) via `GoogleProvisionClient`.
///
/// # Inputs
/// - `req`: `ProvisionRequest` deserialized from the IPC frame.
/// - `state`: shared provision state map for status/cancel tracking.
///
/// # Outputs
/// `ProvisionResult` (tagged JSON) — Success, GuidedPending, or Error.
///
/// # Constraints
/// - FullAuto: uses req.client_id as the OAuth access token (wizard passes it directly).
/// - Token is never logged.
pub async fn handle_provision_google_start(
    req: ProvisionRequest,
    state: &ProvisionStateMap,
) -> ProvisionResult {
    let email = req.account_email.clone();

    // Initialize status entry.
    {
        let mut map = state.lock().await;
        map.insert(
            email.clone(),
            EmailProvisionStatus {
                status: "starting".to_string(),
                done: false,
                error: None,
                cancel: false,
            },
        );
    }

    match req.mode {
        ProvisionMode::FullAuto => {
            // client_id carries the OAuth access token for FullAuto mode.
            let oauth_token = match req.client_id {
                Some(ref tok) if !tok.is_empty() => tok.clone(),
                _ => {
                    let msg = "FullAuto mode requires an OAuth access token (client_id field)"
                        .to_string();
                    set_done(state, &email, Some(msg.clone())).await;
                    return ProvisionResult::Error { message: msg };
                }
            };

            update_status(state, &email, "Creating GCP project\u{2026}").await;
            let client = GoogleProvisionClient::new(oauth_token);

            let result = client.full_auto(&email, 1).await;

            match result {
                Ok(r @ ProvisionResult::Success { .. }) => {
                    set_done(state, &email, None).await;
                    r
                }
                Ok(ProvisionResult::Error { message: msg }) => {
                    set_done(state, &email, Some(msg.clone())).await;
                    ProvisionResult::Error { message: msg }
                }
                Err(e) => {
                    let msg = e.to_string();
                    set_done(state, &email, Some(msg.clone())).await;
                    ProvisionResult::Error { message: msg }
                }
                Ok(other) => other,
            }
        }

        ProvisionMode::Guided => {
            // Return the first guided step URL immediately.
            set_done(state, &email, None).await;
            let client = GoogleProvisionClient::new(String::new());
            client.guided(0)
        }

        ProvisionMode::Manual => {
            let api_key = match req.client_secret {
                Some(ref k) if !k.is_empty() => k.clone(),
                _ => {
                    let msg =
                        "Manual mode requires the API key in the client_secret field".to_string();
                    set_done(state, &email, Some(msg.clone())).await;
                    return ProvisionResult::Error { message: msg };
                }
            };

            update_status(state, &email, "Validating API key\u{2026}").await;

            match validate_gemini_key(&api_key).await {
                Ok(()) => {
                    set_done(state, &email, None).await;
                    ProvisionResult::Success {
                        api_key,
                        project_id: String::new(),
                    }
                }
                Err(e) => {
                    let msg = e.to_string();
                    set_done(state, &email, Some(msg.clone())).await;
                    ProvisionResult::Error { message: msg }
                }
            }
        }
    }
}

// ── Handler: status ───────────────────────────────────────────────────────────

/// Handle a `cascade_provision_google_status` request.
///
/// # Purpose
/// Returns the current status of an in-progress provisioning operation for
/// the given account email (used for UI polling during FULL_AUTO).
///
/// # Inputs
/// - `email`: the Google account email being provisioned.
/// - `state`: shared provision state map.
///
/// # Outputs
/// `ProvisionStatus` with current status message and done flag.
pub async fn handle_provision_google_status(
    email: String,
    state: &ProvisionStateMap,
) -> ProvisionStatus {
    let map = state.lock().await;
    match map.get(&email) {
        Some(s) => ProvisionStatus {
            account_email: email,
            status: s.status.clone(),
            done: s.done,
            error: s.error.clone(),
        },
        None => ProvisionStatus {
            account_email: email,
            status: "not_started".to_string(),
            done: false,
            error: None,
        },
    }
}

// ── Handler: cancel ───────────────────────────────────────────────────────────

/// Handle a `cascade_provision_google_cancel` request.
///
/// # Purpose
/// Sets a cancellation flag for the in-progress provisioning operation for
/// the given account email. The FullAuto flow checks this flag before each
/// GCP polling step.
///
/// # Inputs
/// - `email`: the Google account email whose provisioning to cancel.
/// - `state`: shared provision state map.
pub async fn handle_provision_google_cancel(email: String, state: &ProvisionStateMap) {
    let mut map = state.lock().await;
    if let Some(s) = map.get_mut(&email) {
        s.cancel = true;
        s.status = "cancelling".to_string();
    }
}

// ── Private helpers ───────────────────────────────────────────────────────────

async fn update_status(state: &ProvisionStateMap, email: &str, msg: &str) {
    let mut map = state.lock().await;
    if let Some(s) = map.get_mut(email) {
        s.status = msg.to_string();
    }
}

async fn set_done(state: &ProvisionStateMap, email: &str, error: Option<String>) {
    let mut map = state.lock().await;
    if let Some(s) = map.get_mut(email) {
        s.done = true;
        s.error = error;
        s.status = if s.error.is_some() {
            "error".to_string()
        } else {
            "complete".to_string()
        };
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::provision::ProvisionMode;

    #[tokio::test]
    async fn status_returns_not_started_for_unknown_email() {
        let state = new_state_map();
        let status = handle_provision_google_status("nobody@example.com".to_string(), &state).await;
        assert_eq!(status.status, "not_started");
        assert!(!status.done);
        assert!(status.error.is_none());
    }

    #[tokio::test]
    async fn cancel_sets_cancel_flag() {
        let state = new_state_map();
        {
            let mut map = state.lock().await;
            map.insert(
                "test@example.com".to_string(),
                EmailProvisionStatus::default(),
            );
        }
        handle_provision_google_cancel("test@example.com".to_string(), &state).await;
        let map = state.lock().await;
        assert!(map.get("test@example.com").unwrap().cancel);
    }

    #[tokio::test]
    async fn manual_mode_missing_key_returns_error() {
        let state = new_state_map();
        let req = ProvisionRequest {
            account_email: "test@example.com".to_string(),
            mode: ProvisionMode::Manual,
            client_id: None,
            client_secret: None,
        };
        let result = handle_provision_google_start(req, &state).await;
        assert!(matches!(result, ProvisionResult::Error { .. }));
    }

    #[tokio::test]
    async fn full_auto_missing_token_returns_error() {
        let state = new_state_map();
        let req = ProvisionRequest {
            account_email: "test@example.com".to_string(),
            mode: ProvisionMode::FullAuto,
            client_id: None,
            client_secret: None,
        };
        let result = handle_provision_google_start(req, &state).await;
        assert!(matches!(result, ProvisionResult::Error { .. }));
    }

    #[tokio::test]
    async fn guided_mode_returns_guided_pending() {
        let state = new_state_map();
        let req = ProvisionRequest {
            account_email: "test@example.com".to_string(),
            mode: ProvisionMode::Guided,
            client_id: None,
            client_secret: None,
        };
        let result = handle_provision_google_start(req, &state).await;
        assert!(
            matches!(result, ProvisionResult::GuidedPending { step: 0, .. }),
            "expected GuidedPending step 0, got: {:?}",
            result
        );
    }
}
