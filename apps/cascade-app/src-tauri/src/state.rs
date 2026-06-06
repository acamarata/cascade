// Tauri managed application state.
//
// Why: Tauri's .manage() injects a shared state struct into every command.
// This file holds the app-level state that commands need at call time.
// Replacing the AppState unit-placeholder (P3-E01 wave-1 scaffold) with a
// real socket-path container so the daemon client can connect without relying
// on process-env reads inside every command handler.
//
// Inputs: $HOME (resolved at startup); CASCADE_SOCKET env var override.
// Outputs: AppState { socket_path } — Send + Sync so Tauri can share it.
// Constraints: no mutex required; socket_path is read-only after construction.
// SPORT: MASTER-COMPONENTS.md: AppState | src-tauri/src/state.rs | Tauri managed state

use cascade_types::paths::daemon_socket;

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
}

impl AppState {
    /// Build [`AppState`] from the environment.
    ///
    /// Reads `CASCADE_SOCKET` if set; otherwise uses the canonical path from
    /// [`daemon_socket()`].
    pub fn from_env() -> Self {
        let socket_path = std::env::var("CASCADE_SOCKET")
            .unwrap_or_else(|_| daemon_socket().to_string_lossy().into_owned());
        Self { socket_path }
    }
}
