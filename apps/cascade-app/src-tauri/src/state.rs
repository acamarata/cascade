// Tauri managed application state.
//
// Why: Tauri's .manage() injects a shared state struct into every command.
// This file holds the app-level state that commands need at call time.
// Replacing the AppState unit-placeholder (P3-E01 wave-1 scaffold) with a
// real socket-path container so the daemon client can connect without relying
// on process-env reads inside every command handler.
//
// Inputs: $HOME (resolved at startup); CASCADE_SOCKET env var override.
// Outputs: AppState { socket_path, provision_state, provider_health } — Send + Sync.
// Constraints: no mutex on socket_path (read-only after construction).
// SPORT: MASTER-COMPONENTS.md: AppState | src-tauri/src/state.rs | Tauri managed state

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Instant;
use tokio::sync::{Mutex, RwLock};

use cascade_types::paths::daemon_socket;

/// Per-email provision status (local to Tauri app; mirrors daemon state).
#[derive(Debug, Clone, Default)]
pub struct EmailProvStatus {
    pub status: String,
    pub done: bool,
    pub error: Option<String>,
    pub cancel: bool,
}

/// Shared provision state map type alias.
pub type ProvisionStateMap = Arc<Mutex<HashMap<String, EmailProvStatus>>>;

/// Per-provider health snapshot cached in Tauri app state.
///
/// Mirrors `cascade_daemon::provider_health::ProviderHealth`.  Defined here to
/// avoid a cascade-daemon dev-dependency in the Tauri app crate.
#[derive(Debug, Clone)]
pub struct AppProviderHealth {
    pub ok: bool,
    pub checked_at: Instant,
    pub error_msg: Option<String>,
}

/// Shared provider health cache type alias.
///
/// Keyed by stable provider ID (matches `ProviderEntry.id` in providers.json).
pub type ProviderHealthMap = Arc<RwLock<HashMap<String, AppProviderHealth>>>;

/// Managed Tauri application state.
///
/// Holds configuration that every command handler needs at call time.
/// Constructed once in [`crate::run()`] and shared read-only.
pub struct AppState {
    /// Absolute path to the cascaded Unix domain socket.
    ///
    /// Defaults to `$HOME/.cascade/daemon.sock`.
    /// Override with the `CASCADE_SOCKET` environment variable at launch.
    pub socket_path: String,

    /// Per-email provision operation status (T-P3-E03-39b).
    ///
    /// Keyed by account_email; updated by cascade_provision_google_start and
    /// polled by cascade_provision_google_status.
    pub provision_state: ProvisionStateMap,

    /// Cached provider health results (T-P3-E04-15).
    ///
    /// Populated by the background health-check task spawned in
    /// [`crate::run()`].  Keyed by provider ID; read by
    /// [`crate::commands::provision::cascade_providers_health`].
    pub provider_health: ProviderHealthMap,
}

impl AppState {
    /// Build [`AppState`] from the environment.
    ///
    /// Reads `CASCADE_SOCKET` if set; otherwise uses the canonical path from
    /// [`daemon_socket()`].
    pub fn from_env() -> Self {
        let socket_path = std::env::var("CASCADE_SOCKET")
            .unwrap_or_else(|_| daemon_socket().to_string_lossy().into_owned());
        Self {
            socket_path,
            provision_state: Arc::new(Mutex::new(HashMap::new())),
            provider_health: Arc::new(RwLock::new(HashMap::new())),
        }
    }
}
