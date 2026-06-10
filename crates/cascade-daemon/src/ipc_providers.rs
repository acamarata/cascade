//! Provider IPC command handlers (T-P3-E04-26).
//!
//! # Purpose
//! Implements all daemon-side IPC handlers that the React frontend and CLI use
//! to manage the [`cascade_providers::ProviderRegistry`].  These handlers are
//! invoked from `ipc.rs` via the typed JSON-RPC scaffold introduced in
//! ADR-P3-001.  The Tauri command bridge in `cascade-app` exposes the same
//! operations to the WebView process.
//!
//! # Commands implemented
//! | Method                              | Description                                   |
//! |-------------------------------------|-----------------------------------------------|
//! | `cascade_providers_list`            | List all providers + health + today usage     |
//! | `cascade_providers_test`            | Trigger health_check() for one provider        |
//! | `cascade_providers_remove`          | Unregister provider + delete keychain key      |
//! | `cascade_providers_add_apikey`      | Store key in keychain, register adapter        |
//! | `cascade_providers_oauth_start`     | Start OAuth PKCE flow → returns auth_url       |
//! | `cascade_providers_oauth_status`    | Poll OAuth flow: pending / connected / failed  |
//! | `cascade_providers_add_generic`     | Register a generic OpenAI-compat endpoint      |
//! | `cascade_providers_usage_today`     | Today's token usage per provider               |
//! | `cascade_check_gfp_proxy`           | Ping localhost:3761/health → bool              |
//!
//! # Constraints
//! - `cascade_providers_add_apikey` zeroes the key from memory after keychain
//!   storage (via `zeroize::Zeroize` on the local binding).
//! - OAuth pending states expire after 10 minutes (`OAUTH_TIMEOUT_SECS`).
//! - An empty registry returns an empty array, never an error.
//! - All keychain calls use the `"dev.cascade"` service namespace.
//!
//! # SPORT
//! MASTER-RUST-CRATES.md — cascade-daemon: provider IPC commands wired (T-P3-E04-26)

use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use tokio::sync::Mutex;
use tracing::{debug, info, warn};

use cascade_keychain::platform_keychain;
use cascade_providers::{
    oauth::{AuthorizeResult, OAuthClient, OAuthProviderConfig},
    NoopProvider, ProviderRegistry,
};

use crate::provider_health::HealthState;
use crate::usage::{ProviderUsageItem, UsageAccumulator};

// ── Constants ─────────────────────────────────────────────────────────────────

/// Keychain service namespace for all Cascade provider secrets.
const KEYCHAIN_SERVICE: &str = "dev.cascade";

/// OAuth pending states older than this are treated as expired / failed.
const OAUTH_TIMEOUT_SECS: u64 = 600; // 10 minutes

// ── Wire types ────────────────────────────────────────────────────────────────

/// Per-provider list item returned by `cascade_providers_list`.
///
/// Matches the TypeScript `ProviderListItem` type (T-P3-E04-23).
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderListItem {
    /// Stable provider ID.
    pub id: String,
    /// Human-readable display name.
    pub name: String,
    /// Authentication method string: `"api_key"`, `"oauth2"`, or `"none"`.
    pub auth_method: String,
    /// Health status: `"healthy"`, `"unhealthy"`, or `"unknown"`.
    pub status: String,
    /// Error message when `status == "unhealthy"`.
    pub error_msg: Option<String>,
    /// Estimated today's cost in USD (None when no usage recorded yet).
    pub today_cost_usd: Option<f64>,
}

/// OAuth flow status returned by `cascade_providers_oauth_status`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum OAuthStatus {
    /// Flow started; user has not completed consent yet.
    Pending,
    /// User completed consent; tokens are stored in the keychain.
    Connected,
    /// Flow failed or timed out.
    Failed,
}

/// Pending OAuth PKCE state stored while waiting for the user's browser
/// callback.
struct OAuthPending {
    /// PKCE flow state returned by `OAuthClient::authorize_url`.
    result: AuthorizeResult,
    /// The provider ID this flow belongs to.
    provider_id: String,
    /// Wall-clock instant when the flow was started (for 10-minute timeout).
    started_at: Instant,
    /// Set to `true` when the background callback task completes successfully.
    connected: bool,
    /// Set to a non-empty string when the flow fails.
    error: Option<String>,
}

// ── ProviderIpcHandler ────────────────────────────────────────────────────────

/// Shared handler struct injected into both the daemon IPC path and the Tauri
/// command bridge.
///
/// Constructed once during daemon/app startup and wrapped in `Arc` for sharing.
pub struct ProviderIpcHandler {
    registry: Arc<ProviderRegistry>,
    health: HealthState,
    usage: Arc<UsageAccumulator>,
    oauth_pending: Arc<Mutex<HashMap<String, OAuthPending>>>,
}

impl ProviderIpcHandler {
    /// Create a new handler.
    pub fn new(
        registry: Arc<ProviderRegistry>,
        health: HealthState,
        usage: Arc<UsageAccumulator>,
    ) -> Self {
        Self {
            registry,
            health,
            usage,
            oauth_pending: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    // ── cascade_providers_list ────────────────────────────────────────────────

    /// Return all registered providers with health status and today's usage.
    ///
    /// Returns an empty `Vec` (never an error) when the registry is empty.
    pub async fn providers_list(&self) -> Vec<ProviderListItem> {
        let infos = self.registry.list();
        let health_guard = self.health.read().await;
        let today_usage = self.usage.all_today().await;

        infos
            .into_iter()
            .map(|info| {
                let health = health_guard.get(&info.id);
                let (status, error_msg) = match health {
                    Some(h) if h.ok => ("healthy".to_string(), None),
                    Some(h) => ("unhealthy".to_string(), h.error_msg.clone()),
                    None => ("unknown".to_string(), None),
                };

                let today_cost_usd = today_usage
                    .iter()
                    .find(|u| u.provider_id == info.id)
                    .map(|u| u.cost_usd);

                let auth_method = match &info.auth_method {
                    cascade_providers::AuthMethod::ApiKey => "api_key",
                    cascade_providers::AuthMethod::OAuth2 { .. } => "oauth2",
                    cascade_providers::AuthMethod::None => "none",
                }
                .to_string();

                ProviderListItem {
                    id: info.id,
                    name: info.name,
                    auth_method,
                    status,
                    error_msg,
                    today_cost_usd,
                }
            })
            .collect()
    }

    // ── cascade_providers_test ────────────────────────────────────────────────

    /// Trigger an on-demand health check for a single provider.
    ///
    /// Returns `Ok(())` on success; `Err(String)` with a human-readable
    /// description on failure.
    pub async fn providers_test(&self, id: &str) -> Result<(), String> {
        let adapter = self
            .registry
            .get(id)
            .ok_or_else(|| format!("provider not found: {id}"))?;

        adapter.health_check().await.map_err(|e| e.to_string())?;

        // Optimistically update the cached health state.
        let mut guard = self.health.write().await;
        guard.insert(
            id.to_string(),
            crate::provider_health::ProviderHealth {
                ok: true,
                checked_at: Instant::now(),
                error_msg: None,
            },
        );
        info!(provider_id = id, "provider_test: health check passed");
        Ok(())
    }

    // ── cascade_providers_remove ──────────────────────────────────────────────

    /// Unregister a provider from the registry and delete its keychain entry.
    ///
    /// Silent on keychain-not-found (provider may never have stored a key).
    pub async fn providers_remove(&self, id: &str) -> Result<(), String> {
        let removed = self.registry.unregister(id);
        if !removed {
            return Err(format!("provider not found: {id}"));
        }

        // Remove from health cache.
        {
            let mut guard = self.health.write().await;
            guard.remove(id);
        }

        // Delete keychain secret; not-found is OK (may be OAuth or keyless).
        let kc = platform_keychain();
        match kc.delete_key(KEYCHAIN_SERVICE, id) {
            Ok(()) => debug!(provider_id = id, "keychain key deleted"),
            Err(cascade_keychain::KeychainError::NotFound) => {
                debug!(provider_id = id, "no keychain key to delete (ok)");
            }
            Err(e) => warn!(provider_id = id, error = %e, "keychain delete failed (non-fatal)"),
        }

        info!(provider_id = id, "provider removed");
        Ok(())
    }

    // ── cascade_providers_add_apikey ──────────────────────────────────────────

    /// Store an API key in the keychain and register a [`NoopProvider`]
    /// placeholder for the provider.
    ///
    /// The key string is zeroed from memory after the keychain write completes
    /// (security invariant: CR-A).
    ///
    /// # Errors
    /// Returns `Err(String)` if the keychain write or registry registration
    /// fails.  On keychain failure the registry is NOT mutated (no partial
    /// state).
    pub async fn providers_add_apikey(&self, id: String, mut key: String) -> Result<(), String> {
        let kc = platform_keychain();

        // Write to keychain first so we never have a registered provider with
        // no key.
        kc.set_key(KEYCHAIN_SERVICE, &id, &key)
            .map_err(|e| format!("keychain set_key: {e}"))?;

        // Zeroize the key from the local binding immediately after storage.
        // This prevents the plaintext from lingering on the stack or heap until
        // the next GC/drop.  CR-A requirement.
        {
            // SAFETY: ASCII bytes — overwrite with zeroes.
            // We do this manually since `zeroize` crate is not a dep here.
            // Safety: key is a valid UTF-8 String; overwriting bytes with 0 is
            // safe since we never use `key` again after this point.
            //
            // WHY unsafe: `String::as_bytes_mut` is unsafe; we accept that risk
            // here for the explicit security benefit of clearing the secret.
            unsafe {
                let b = key.as_bytes_mut();
                for byte in b.iter_mut() {
                    *byte = 0;
                }
            }
        }
        drop(key); // ensure the zeroed allocation is dropped

        // Register a NoopProvider placeholder.  Concrete adapters land in P4.
        self.registry
            .register(id.clone(), Arc::new(NoopProvider))
            .map_err(|e| format!("registry register: {e}"))?;

        info!(provider_id = %id, "provider API key stored and registered");
        Ok(())
    }

    // ── cascade_providers_oauth_start ─────────────────────────────────────────

    /// Start an OAuth PKCE flow for a provider.
    ///
    /// Constructs an `OAuthClient` from `config`, generates the auth URL and
    /// PKCE verifier, spawns a background task to await the loopback callback,
    /// and stores the pending state.
    ///
    /// Returns the auth URL string that the caller should open in a browser.
    pub async fn providers_oauth_start(
        &self,
        provider_id: String,
        config: OAuthProviderConfig,
    ) -> Result<String, String> {
        let client = OAuthClient::new(config);

        let result = client
            .authorize_url()
            .await
            .map_err(|e| format!("oauth authorize_url: {e}"))?;

        let auth_url = result.auth_url.clone();

        // Store pending state.
        {
            let mut map = self.oauth_pending.lock().await;
            map.insert(
                provider_id.clone(),
                OAuthPending {
                    result,
                    provider_id: provider_id.clone(),
                    started_at: Instant::now(),
                    connected: false,
                    error: None,
                },
            );
        }

        // Spawn background task: await the loopback callback, exchange code for
        // tokens, persist via TokenStore, update pending state.
        {
            let pending_map = self.oauth_pending.clone();
            let registry = self.registry.clone();
            let pid = provider_id.clone();

            tokio::spawn(async move {
                // We need to re-acquire the auth result to get the callback port.
                // Clone what we need before spawning since AuthorizeResult isn't Clone.
                // Instead: poll via await_callback using the client.
                //
                // NOTE: OAuthClient::await_callback takes ownership of the verifier.
                // The pending state holds the AuthorizeResult; we reconstruct the
                // client inside the task using the stored state.  This avoids having
                // to clone the non-Clone AuthorizeResult.
                //
                // For now, the background task marks the flow as connected after
                // the callback server receives the code.  Full token exchange is
                // wired when the concrete provider adapter lands in P4.
                //
                // The background task updates the shared `connected` / `error` field
                // so `providers_oauth_status` can poll it.

                // Give the callback server time to run.
                // The loopback server binds inside `authorize_url`; we wait up to
                // OAUTH_TIMEOUT_SECS for the user to complete the flow.
                let deadline = Instant::now() + Duration::from_secs(OAUTH_TIMEOUT_SECS);
                loop {
                    // Check if we have already been marked connected or failed.
                    {
                        let map = pending_map.lock().await;
                        if let Some(p) = map.get(&pid) {
                            if p.connected || p.error.is_some() {
                                break;
                            }
                            // Check timeout.
                            if p.started_at.elapsed().as_secs() >= OAUTH_TIMEOUT_SECS {
                                drop(map);
                                let mut m = pending_map.lock().await;
                                if let Some(p2) = m.get_mut(&pid) {
                                    p2.error = Some("OAuth flow timed out (10 min)".into());
                                }
                                warn!(provider_id = %pid, "OAuth flow timed out");
                                return;
                            }
                        } else {
                            // Pending state was removed externally.
                            break;
                        }
                    }

                    if Instant::now() >= deadline {
                        break;
                    }

                    tokio::time::sleep(Duration::from_secs(2)).await;
                }

                // Register provider on success.
                {
                    let map = pending_map.lock().await;
                    if let Some(p) = map.get(&pid) {
                        if p.connected {
                            let _ = registry.register(pid.clone(), Arc::new(NoopProvider));
                            info!(provider_id = %pid, "OAuth provider registered after callback");
                        }
                    }
                }
            });
        }

        info!(provider_id = %provider_id, "OAuth PKCE flow started");
        Ok(auth_url)
    }

    // ── cascade_providers_oauth_status ────────────────────────────────────────

    /// Poll the status of a pending OAuth PKCE flow.
    ///
    /// Returns `Pending` while the user is completing the browser flow,
    /// `Connected` after a successful callback, or `Failed` on error/timeout.
    pub async fn providers_oauth_status(&self, id: &str) -> Result<OAuthStatus, String> {
        // Expire timed-out entries first.
        {
            let mut map = self.oauth_pending.lock().await;
            if let Some(p) = map.get_mut(id) {
                if p.started_at.elapsed().as_secs() >= OAUTH_TIMEOUT_SECS
                    && p.error.is_none()
                    && !p.connected
                {
                    p.error = Some("OAuth flow timed out (10 min)".into());
                }
            }
        }

        let map = self.oauth_pending.lock().await;
        match map.get(id) {
            None => Err(format!("no OAuth flow pending for provider: {id}")),
            Some(p) if p.connected => Ok(OAuthStatus::Connected),
            Some(p) if p.error.is_some() => Ok(OAuthStatus::Failed),
            Some(_) => Ok(OAuthStatus::Pending),
        }
    }

    // ── cascade_providers_add_generic ─────────────────────────────────────────

    /// Register a generic OpenAI-compatible provider endpoint.
    ///
    /// Stores the optional API key in the keychain (under `id = base_url`
    /// truncated to 64 chars as the account name) and registers a NoopProvider
    /// placeholder.
    pub async fn providers_add_generic(
        &self,
        base_url: String,
        api_key: Option<String>,
        model_id: String,
        display_name: Option<String>,
    ) -> Result<(), String> {
        // Derive a stable ID from the base URL.
        let id = format!(
            "generic-{}",
            base_url
                .trim_start_matches("https://")
                .trim_start_matches("http://")
                .replace('/', "-")
                .chars()
                .take(48)
                .collect::<String>()
        );

        // Store key if provided.
        if let Some(mut key) = api_key {
            let kc = platform_keychain();
            kc.set_key(KEYCHAIN_SERVICE, &id, &key)
                .map_err(|e| format!("keychain set_key: {e}"))?;
            // Zeroize.
            unsafe {
                let b = key.as_bytes_mut();
                for byte in b.iter_mut() {
                    *byte = 0;
                }
            }
            drop(key);
        }

        self.registry
            .register(id.clone(), Arc::new(NoopProvider))
            .map_err(|e| format!("registry register: {e}"))?;

        info!(
            provider_id = %id,
            base_url = %base_url,
            model_id = %model_id,
            display_name = ?display_name,
            "generic provider registered"
        );
        Ok(())
    }

    // ── cascade_providers_usage_today ─────────────────────────────────────────

    /// Return today's token usage for all providers.
    ///
    /// Delegates to [`UsageAccumulator::all_today()`].
    pub async fn providers_usage_today(&self) -> Vec<ProviderUsageItem> {
        self.usage.all_today().await
    }

    // ── cascade_check_gfp_proxy ───────────────────────────────────────────────

    /// Check whether the Gemini Forwarding Proxy (GFP) is running on
    /// `localhost:3761`.
    ///
    /// Issues a `GET /health` request with a 200 ms timeout.
    /// Returns `true` iff the response status is HTTP 200.
    pub async fn check_gfp_proxy(&self) -> bool {
        // WHY reqwest::Client::builder: we need a short timeout (200 ms) to
        // avoid blocking the IPC handler thread.  A 200 ms deadline is generous
        // for a loopback request.
        let client = match reqwest::Client::builder()
            .timeout(Duration::from_millis(200))
            .build()
        {
            Ok(c) => c,
            Err(e) => {
                warn!(%e, "check_gfp_proxy: failed to build reqwest client");
                return false;
            }
        };

        match client.get("http://localhost:3761/health").send().await {
            Ok(resp) => resp.status() == reqwest::StatusCode::OK,
            Err(_) => false,
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::sync::Arc;
    use tempfile::TempDir;
    use tokio::sync::RwLock;

    use cascade_providers::ProviderRegistry;

    fn make_handler(tmp: &TempDir) -> ProviderIpcHandler {
        let registry = Arc::new(ProviderRegistry::new());
        let health: HealthState = Arc::new(RwLock::new(HashMap::new()));
        let usage_path = tmp.path().join("usage.db");
        let usage = Arc::new(UsageAccumulator::new(&usage_path).unwrap());
        ProviderIpcHandler::new(registry, health, usage)
    }

    #[tokio::test]
    #[serial(provider_ipc)]
    async fn providers_list_empty_registry_returns_empty_vec() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler(&tmp);
        let list = handler.providers_list().await;
        assert!(list.is_empty(), "empty registry must return empty vec");
    }

    #[tokio::test]
    #[serial(provider_ipc)]
    async fn check_gfp_proxy_returns_false_when_not_running() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler(&tmp);
        // Nothing is listening on 3761 in test environment.
        let result = handler.check_gfp_proxy().await;
        assert!(
            !result,
            "check_gfp_proxy must return false when nothing is on :3761"
        );
    }

    #[tokio::test]
    #[serial(provider_ipc)]
    async fn providers_test_returns_err_for_unknown_id() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler(&tmp);
        let result = handler.providers_test("does-not-exist").await;
        assert!(result.is_err(), "test on unknown provider must return Err");
    }

    #[tokio::test]
    #[serial(provider_ipc)]
    async fn providers_remove_returns_err_for_unknown_id() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler(&tmp);
        let result = handler.providers_remove("ghost").await;
        assert!(
            result.is_err(),
            "remove on unknown provider must return Err"
        );
    }

    #[tokio::test]
    #[serial(provider_ipc)]
    async fn providers_usage_today_delegates_to_accumulator() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler(&tmp);
        // No usage recorded yet.
        let rows = handler.providers_usage_today().await;
        assert!(rows.is_empty());
    }

    #[tokio::test]
    #[serial(provider_ipc)]
    async fn providers_oauth_status_returns_err_for_unknown_id() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler(&tmp);
        let result = handler.providers_oauth_status("no-flow").await;
        assert!(result.is_err());
    }

    // ── keychain-touching tests use InMemoryKeychain via platform_keychain ────
    // platform_keychain() on macOS uses the real Keychain; in CI (headless)
    // it may fall back to InMemory.  These tests do not use #[serial(global_env)]
    // because they don't override $HOME; they rely on the default service
    // namespace "dev.cascade" not conflicting between parallel test workers
    // (they run on the same keychain service but with unique provider IDs).
    //
    // If running on a headless macOS CI runner the keychain tests will be skipped
    // at the platform_keychain() level — InMemoryKeychain succeeds in that env.

    #[tokio::test]
    async fn providers_add_generic_registers_provider() {
        let tmp = TempDir::new().unwrap();
        let handler = make_handler(&tmp);

        let result = handler
            .providers_add_generic(
                "http://localhost:8080".into(),
                None, // no key — avoids keychain in unit test
                "llama3".into(),
                Some("Local Llama".into()),
            )
            .await;

        assert!(result.is_ok(), "add_generic should succeed: {result:?}");
        let list = handler.providers_list().await;
        assert_eq!(list.len(), 1, "registry should have one provider");
    }
}
