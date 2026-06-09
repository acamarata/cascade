// Tauri IPC error type — propagated to JS as structured JSON.
//
// Why: Tauri commands return Result<T, E> where E must implement serde::Serialize.
// This enum translates daemon/socket errors into typed variants the frontend can
// pattern-match on. No raw error details are exposed (no stack traces, no socket
// internals) per the CR-A security constraint.
//
// Inputs: IpcClientError variants from cascade-cli::ipc_client.
// Outputs: serializable JSON with { "kind": "...", "message": "..." }.
// Constraints: never embed auth tokens, socket internals, or OS paths in the
//   message field. The frontend sees only a human-readable summary.
// SPORT: MASTER-COMPONENTS.md: CascadeError | src-tauri/src/error.rs | Tauri IPC error type

use serde::Serialize;

/// Structured error type for all cascade Tauri commands.
///
/// Serialises to `{ "kind": "<variant>", "message": "<msg>" }` so the
/// React frontend can distinguish error categories.
#[derive(Debug, thiserror::Error)]
pub enum CascadeError {
    /// The cascaded daemon is not running.
    #[error("cascade daemon is not running — start it from the app sidebar")]
    DaemonNotRunning,

    /// The daemon refused the IPC auth token.
    #[error("IPC authentication failed")]
    AuthFailed,

    /// The requested RPC method does not exist in this daemon version.
    #[error("method not found: {0}")]
    MethodNotFound(String),

    /// The params failed daemon-side validation.
    #[error("invalid params: {0}")]
    InvalidParams(String),

    /// A generic daemon-side RPC error (code + message).
    #[error("daemon error ({code}): {message}")]
    Rpc { code: i32, message: String },

    /// Low-level socket I/O error (no internal details exposed).
    #[error("IPC transport error — is the daemon running?")]
    Transport,

    /// The feature is not yet implemented in this phase.
    #[error("not yet implemented: {0}")]
    NotImplemented(String),

    /// A window-management error (open/close/focus failed).
    #[error("window error: {0}")]
    Daemon(String),

    /// A custom error message (used by wizard commands for filesystem errors).
    #[error("{0}")]
    Custom(String),
}

impl Serialize for CascadeError {
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        use serde::ser::SerializeMap;
        let mut map = serializer.serialize_map(Some(2))?;
        let kind = match self {
            CascadeError::DaemonNotRunning => "daemon_not_running",
            CascadeError::AuthFailed => "auth_failed",
            CascadeError::MethodNotFound(_) => "method_not_found",
            CascadeError::InvalidParams(_) => "invalid_params",
            CascadeError::Rpc { .. } => "rpc_error",
            CascadeError::Transport => "transport_error",
            CascadeError::NotImplemented(_) => "not_implemented",
            CascadeError::Daemon(_) => "daemon_error",
            CascadeError::Custom(_) => "custom_error",
        };
        map.serialize_entry("kind", kind)?;
        map.serialize_entry("message", &self.to_string())?;
        map.end()
    }
}

impl From<cascade_cli::ipc_client::IpcClientError> for CascadeError {
    fn from(e: cascade_cli::ipc_client::IpcClientError) -> Self {
        use cascade_cli::ipc_client::IpcClientError;
        match e {
            IpcClientError::DaemonNotRunning | IpcClientError::TokenNotFound => {
                CascadeError::DaemonNotRunning
            }
            IpcClientError::AuthFailed => CascadeError::AuthFailed,
            IpcClientError::MethodNotFound(m) => CascadeError::MethodNotFound(m),
            IpcClientError::InvalidParams(m) => CascadeError::InvalidParams(m),
            IpcClientError::Rpc { code, msg } => CascadeError::Rpc {
                code,
                message: msg,
            },
            IpcClientError::Io(_) | IpcClientError::Serde(_) => CascadeError::Transport,
        }
    }
}
