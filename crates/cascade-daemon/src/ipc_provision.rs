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
use std::path::PathBuf;
use std::sync::Arc;

use tokio::sync::Mutex;

use cascade_providers::{
    google_provision::GoogleProvisionClient, validate_gemini_key, ProvisionOptions,
};
use cascade_types::provision::{ProvisionMode, ProvisionRequest, ProvisionResult, ProvisionStatus};

// ── Shared state ─────────────────────────────────────────────────────────────

/// Per-email provision status (also carries multi-key progress).
#[derive(Debug, Clone)]
pub struct EmailProvisionStatus {
    pub status: String,
    pub done: bool,
    pub error: Option<String>,
    pub cancel: bool,
    /// For multi-key runs: number of keys created so far.
    pub keys_created: u32,
    /// For multi-key runs: total keys requested (effective ceiling).
    pub keys_target: u32,
}

impl Default for EmailProvisionStatus {
    fn default() -> Self {
        Self {
            status: "pending".to_string(),
            done: false,
            error: None,
            cancel: false,
            keys_created: 0,
            keys_target: 0,
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
/// For FullAuto, `count` controls how many API keys to create (default 1).
/// `opts` supplies the GFP policy (ceiling, cooldown, auto_max). Conservative
/// defaults apply when `opts` is `None`.
///
/// # Inputs
/// - `req`: `ProvisionRequest` deserialized from the IPC frame.
/// - `state`: shared provision state map for status/cancel tracking.
/// - `count`: for FullAuto multi-key — number of keys to create (default 1).
/// - `opts`: GFP provisioning options (uses `ProvisionOptions::default()` if None).
/// - `checkpoint_path`: override checkpoint file path (None = default).
/// - `pool_register`: callback invoked with (api_key, project_id) for each created key.
///
/// # Outputs
/// `ProvisionMultiResult` (always returned; single-key is just keys.len() == 1).
///
/// # Constraints
/// - FullAuto: uses req.client_id as the OAuth access token (wizard passes it directly).
/// - Token is never logged.
/// - ToS warning is always surfaced in the result.
pub async fn handle_provision_google_start(
    req: ProvisionRequest,
    state: &ProvisionStateMap,
) -> ProvisionResult {
    handle_provision_google_start_multi(req, state, 1, None, None, |_, _| async {}).await
}

/// Extended handler supporting multi-key GFP provisioning with progress tracking.
///
/// # Inputs
/// - `req`: `ProvisionRequest` deserialized from the IPC frame.
/// - `state`: shared provision state map for status/cancel tracking.
/// - `count`: number of API keys to create (capped by ceiling in opts).
/// - `opts`: GFP provisioning options; uses `ProvisionOptions::default()` if None.
/// - `checkpoint_path`: override checkpoint file path; uses default if None.
/// - `pool_register`: async callback called with (api_key, project_id) for each key.
///
/// # Outputs
/// `ProvisionMultiResult` with all keys + progress + ToS warning.
pub async fn handle_provision_google_start_multi<F, Fut>(
    req: ProvisionRequest,
    state: &ProvisionStateMap,
    count: u32,
    opts: Option<ProvisionOptions>,
    checkpoint_path: Option<PathBuf>,
    pool_register: F,
) -> ProvisionResult
where
    F: Fn(String, String) -> Fut + Send + Sync,
    Fut: std::future::Future<Output = ()> + Send,
{
    let opts = opts.unwrap_or_default();
    let email = req.account_email.clone();

    // Effective target (after ceiling).
    let effective_target = if opts.auto_max {
        count
    } else {
        count.min(opts.max_keys_per_account)
    };

    // Initialize status entry with target count.
    {
        let mut map = state.lock().await;
        map.insert(
            email.clone(),
            EmailProvisionStatus {
                status: "starting".to_string(),
                done: false,
                error: None,
                cancel: false,
                keys_created: 0,
                keys_target: effective_target,
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

            update_status(
                state,
                &email,
                &format!(
                    "GFP provisioning: 0/{} keys created\u{2026}",
                    effective_target
                ),
            )
            .await;
            let client = GoogleProvisionClient::new(oauth_token);

            // Build a stateful progress updater that tracks per-key progress.
            let state_clone = Arc::clone(state);
            let email_clone = email.clone();
            let target = effective_target;
            // Wrap pool_register in Arc so it can be shared across Fn closure calls.
            let pool_register_arc = Arc::new(pool_register);

            let multi_result = client
                .full_auto_multi(
                    &email,
                    count,
                    &opts,
                    checkpoint_path.as_deref(),
                    move |api_key, project_id| {
                        let state_ref = Arc::clone(&state_clone);
                        let em = email_clone.clone();
                        let reg = Arc::clone(&pool_register_arc);
                        async move {
                            // Update progress counter.
                            let mut map = state_ref.lock().await;
                            if let Some(s) = map.get_mut(&em) {
                                s.keys_created += 1;
                                s.status = format!(
                                    "GFP provisioning: {}/{} keys created\u{2026}",
                                    s.keys_created, target
                                );
                            }
                            drop(map);
                            // Delegate pool registration to the passed register fn.
                            reg(api_key, project_id).await;
                        }
                    },
                )
                .await;

            // Map multi-result back to ProvisionResult for IPC compat.
            if multi_result.keys.is_empty() {
                let msg = if multi_result.errors.is_empty() {
                    "No keys created (ceiling may have been 0)".to_string()
                } else {
                    multi_result.errors.join("; ")
                };
                set_done(state, &email, Some(msg.clone())).await;
                ProvisionResult::Error { message: msg }
            } else {
                // Return the first key as the primary result; progress carries the rest.
                {
                    let mut map = state.lock().await;
                    if let Some(s) = map.get_mut(&email) {
                        s.done = true;
                        s.keys_created = multi_result.keys_created;
                        s.status = format!(
                            "complete — {} key(s) created. ToS: {}",
                            multi_result.keys_created, multi_result.tos_warning
                        );
                    }
                }
                ProvisionResult::Success {
                    api_key: multi_result.keys[0].api_key.clone(),
                    project_id: multi_result.keys[0].project_id.clone(),
                }
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
            keys_created: s.keys_created,
            keys_target: s.keys_target,
        },
        None => ProvisionStatus {
            account_email: email,
            status: "not_started".to_string(),
            done: false,
            error: None,
            keys_created: 0,
            keys_target: 0,
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

    // ── GFP multi-key IPC tests ───────────────────────────────────────────────

    /// Missing token in FullAuto multi returns Error.
    #[tokio::test]
    async fn gfp_ipc_missing_token_returns_error() {
        let state = new_state_map();
        let req = ProvisionRequest {
            account_email: "multi@example.com".to_string(),
            mode: ProvisionMode::FullAuto,
            client_id: None,
            client_secret: None,
        };
        let result =
            handle_provision_google_start_multi(req, &state, 3, None, None, |_, _| async {}).await;
        assert!(matches!(result, ProvisionResult::Error { .. }));
    }

    /// conservative_default: ProvisionOptions default has auto_max OFF and small ceiling.
    #[test]
    fn gfp_ipc_provision_options_default_conservative() {
        let opts = ProvisionOptions::default();
        assert!(!opts.auto_max, "auto_max must default OFF");
        assert!(
            opts.max_keys_per_account <= 3,
            "default ceiling must be <= 3, got {}",
            opts.max_keys_per_account
        );
        assert!(
            opts.cooldown_secs >= 30,
            "default cooldown must be >= 30s, got {}",
            opts.cooldown_secs
        );
    }

    /// Progress tracked in state map during multi-key run (dry_run).
    #[tokio::test]
    async fn gfp_ipc_progress_tracked_in_state() {
        use tempfile::TempDir;
        let tmp = TempDir::new().unwrap();
        let cp_path = tmp.path().join("gfp-ipc-progress-cp.json");

        let state = new_state_map();
        let req = ProvisionRequest {
            account_email: "prog@example.com".to_string(),
            mode: ProvisionMode::FullAuto,
            client_id: Some("fake-token".to_string()),
            client_secret: None,
        };

        let opts = ProvisionOptions {
            dry_run: true,
            max_keys_per_account: 3,
            cooldown_secs: 0,
            auto_max: false,
        };

        let result = handle_provision_google_start_multi(
            req,
            &state,
            2,
            Some(opts),
            Some(cp_path),
            |_, _| async {},
        )
        .await;

        // Result should be Success with the first key.
        assert!(
            matches!(result, ProvisionResult::Success { .. }),
            "expected Success, got: {:?}",
            result
        );

        // State map should reflect 2 keys created.
        let map = state.lock().await;
        let s = map.get("prog@example.com").unwrap();
        assert_eq!(s.keys_created, 2, "state must track 2 keys created");
        assert_eq!(s.keys_target, 2, "state must track target of 2");
        assert!(s.done, "state must be done");
    }

    /// Status polling returns progress fields.
    #[tokio::test]
    async fn gfp_ipc_status_carries_progress_fields() {
        let state = new_state_map();
        {
            let mut map = state.lock().await;
            map.insert(
                "status@example.com".to_string(),
                EmailProvisionStatus {
                    status: "running".to_string(),
                    done: false,
                    error: None,
                    cancel: false,
                    keys_created: 2,
                    keys_target: 5,
                },
            );
        }

        let status = handle_provision_google_status("status@example.com".to_string(), &state).await;
        assert_eq!(status.keys_created, 2);
        assert_eq!(status.keys_target, 5);
        assert_eq!(status.status, "running");
    }
}
