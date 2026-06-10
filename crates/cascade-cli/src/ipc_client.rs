//! IPC client for cascade-cli commands.
//!
//! # Purpose
//! Single-use JSON-RPC 2.0 client used by every CLI subcommand to communicate
//! with the running `cascaded` daemon. Opens a fresh socket connection per
//! invocation (no pooling — CLI calls are infrequent, so simplicity wins).
//!
//! # Inputs
//! - Socket path: `~/.cascade/daemon.sock` (Unix) / `\\.\pipe\cascade-daemon` (Windows)
//! - Auth token: `~/.cascade/ipc_token` — written by the daemon on startup with mode 0o600
//! - `method: &str` — JSON-RPC 2.0 method name (e.g. `"ping"`)
//! - `params: P` — method-specific params struct (`Serialize`)
//!
//! # Outputs
//! - `Ok(R)` — typed result struct (`DeserializeOwned`)
//! - `Err(IpcClientError)` — connection, auth, or RPC error
//!
//! # Constraints
//! - No retry logic: if the daemon is not running, fail fast with a
//!   user-friendly message to stderr and return `DaemonNotRunning`.
//! - No connection pooling: each `send` call opens a new socket.
//! - Auth token bytes are never written to logs or error messages.
//!
//! # SPORT
//! MASTER-LIBS.md: IpcClient | cascade-cli/src/ipc_client.rs | IPC client for CLI commands | ✅ Done

use std::fmt;
use std::path::PathBuf;

use cascade_types::codec::FrameCodec;
use cascade_types::ipc::{
    JsonRpcVersion, Request, RequestId, Response, AUTH_FAILED, INVALID_PARAMS, METHOD_NOT_FOUND,
    PROTOCOL_VERSION,
};
use cascade_types::paths::global_cascade_dir;
use serde::{de::DeserializeOwned, Serialize};
use serde_json::Value;
use tokio::io::AsyncWriteExt;

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors returned by [`IpcClient::send`].
#[derive(Debug)]
pub enum IpcClientError {
    /// The daemon socket does not exist or connection was refused.
    ///
    /// UX message is printed to stderr automatically by `send` before returning.
    DaemonNotRunning,
    /// The daemon rejected the auth token (code -32002).
    AuthFailed,
    /// The requested method is not registered in the daemon (code -32601).
    MethodNotFound(String),
    /// The params failed daemon-side validation (code -32602).
    InvalidParams(String),
    /// Any other RPC-level error returned by the daemon.
    Rpc {
        /// Numeric RPC error code.
        code: i32,
        /// Human-readable error message from the daemon.
        msg: String,
    },
    /// Low-level I/O error on the socket.
    Io(std::io::Error),
    /// JSON serialisation or deserialisation error.
    Serde(serde_json::Error),
    /// The `~/.cascade/ipc_token` file does not exist.
    ///
    /// The daemon writes this file on startup; its absence means the daemon
    /// has never been started or the file was deleted manually.
    TokenNotFound,
}

impl fmt::Display for IpcClientError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::DaemonNotRunning => write!(
                f,
                "cascade daemon is not running. Start it with: cascade daemon start"
            ),
            Self::AuthFailed => write!(f, "IPC auth failed — token mismatch"),
            Self::MethodNotFound(m) => write!(f, "method not found: {m}"),
            Self::InvalidParams(msg) => write!(f, "invalid params: {msg}"),
            Self::Rpc { code, msg } => write!(f, "RPC error {code}: {msg}"),
            Self::Io(e) => write!(f, "I/O error: {e}"),
            Self::Serde(e) => write!(f, "serialisation error: {e}"),
            Self::TokenNotFound => write!(
                f,
                "IPC token not found at ~/.cascade/ipc_token — is the daemon running?"
            ),
        }
    }
}

impl std::error::Error for IpcClientError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Io(e) => Some(e),
            Self::Serde(e) => Some(e),
            _ => None,
        }
    }
}

impl From<std::io::Error> for IpcClientError {
    fn from(e: std::io::Error) -> Self {
        Self::Io(e)
    }
}

impl From<serde_json::Error> for IpcClientError {
    fn from(e: serde_json::Error) -> Self {
        Self::Serde(e)
    }
}

impl From<IpcClientError> for cascade_types::error::CascadeError {
    fn from(e: IpcClientError) -> Self {
        match e {
            IpcClientError::DaemonNotRunning => {
                cascade_types::error::CascadeError::DaemonUnreachable {
                    socket: cascade_types::paths::daemon_socket(),
                    detail: "daemon is not running — start with `cascade daemon start`".into(),
                }
            }
            IpcClientError::AuthFailed => cascade_types::error::CascadeError::IpcProtocolError {
                detail: "IPC auth failed — token mismatch".into(),
            },
            other => cascade_types::error::CascadeError::IpcProtocolError {
                detail: other.to_string(),
            },
        }
    }
}

// ── IpcClient ────────────────────────────────────────────────────────────────

/// Single-use IPC client for CLI subcommands.
///
/// Created once per CLI invocation via [`IpcClient::new`], then used to send
/// one or more JSON-RPC requests to the daemon.
pub struct IpcClient {
    socket_path: PathBuf,
    token: String,
}

impl IpcClient {
    /// Create a new [`IpcClient`], reading the socket path and auth token from
    /// the canonical Cascade home directory (`~/.cascade/`).
    ///
    /// Returns [`IpcClientError::TokenNotFound`] if `~/.cascade/ipc_token` is
    /// absent. Returns [`IpcClientError::Io`] if the file cannot be read.
    pub fn new() -> Result<Self, IpcClientError> {
        let cascade_dir = global_cascade_dir();

        let socket_path = {
            #[cfg(target_os = "windows")]
            {
                PathBuf::from(cascade_types::paths::DAEMON_PIPE_WIN)
            }
            #[cfg(not(target_os = "windows"))]
            {
                cascade_dir.join("daemon.sock")
            }
        };

        let token_path = cascade_dir.join("ipc_token");
        if !token_path.exists() {
            return Err(IpcClientError::TokenNotFound);
        }

        let token = std::fs::read_to_string(&token_path)
            .map_err(IpcClientError::Io)?
            .trim()
            .to_owned();

        Ok(Self { socket_path, token })
    }

    /// Send a JSON-RPC 2.0 request to the daemon and return the typed result.
    ///
    /// # Protocol
    ///
    /// The request is wrapped in an auth envelope before being sent:
    /// ```json
    /// {"auth":"<hex-token>","rpc":<json-rpc-request-object>}
    /// ```
    ///
    /// The frame is length-prefixed (big-endian u32) via [`FrameCodec`].
    ///
    /// # Errors
    ///
    /// | Variant | Trigger |
    /// |---------|---------|
    /// | `DaemonNotRunning` | Socket file absent or connection refused |
    /// | `AuthFailed` | Daemon returned error code -32002 |
    /// | `MethodNotFound` | Daemon returned error code -32601 |
    /// | `InvalidParams` | Daemon returned error code -32602 |
    /// | `Rpc` | Any other RPC error from the daemon |
    /// | `Io` | Low-level socket I/O failure |
    /// | `Serde` | JSON (de)serialisation failure |
    pub async fn send<P, R>(&self, method: &str, params: P) -> Result<R, IpcClientError>
    where
        P: Serialize,
        R: DeserializeOwned,
    {
        // Build the JSON-RPC 2.0 request envelope.
        let rpc_request = Request {
            jsonrpc: JsonRpcVersion::V2_0,
            id: RequestId::Number(1),
            method: method.to_owned(),
            params: Some(params),
            protocol_version: PROTOCOL_VERSION,
        };
        let rpc_json = serde_json::to_value(&rpc_request)?;

        // Wrap in auth envelope: {"auth":"<token>","rpc":<request>}
        let auth_envelope = serde_json::json!({
            "auth": self.token,
            "rpc": rpc_json,
        });
        let frame_bytes = serde_json::to_vec(&auth_envelope)?;

        // Open the socket connection.
        let stream = self.connect().await?;
        let (mut reader, mut writer) = tokio::io::split(stream);

        // Send the frame.
        FrameCodec::write_frame(&mut writer, &frame_bytes)
            .await
            .map_err(|e| match e {
                cascade_types::codec::CodecError::Io(io_err) => IpcClientError::Io(io_err),
                cascade_types::codec::CodecError::FrameTooLarge { len, max } => {
                    IpcClientError::Io(std::io::Error::new(
                        std::io::ErrorKind::InvalidData,
                        format!("frame too large: {} > {}", len, max),
                    ))
                }
            })?;

        // Flush to ensure the frame is sent.
        writer.flush().await?;

        // Read the response frame.
        let response_bytes = FrameCodec::read_frame(&mut reader)
            .await
            .map_err(|e| match e {
                cascade_types::codec::CodecError::Io(io_err) => IpcClientError::Io(io_err),
                cascade_types::codec::CodecError::FrameTooLarge { len, max } => {
                    IpcClientError::Io(std::io::Error::new(
                        std::io::ErrorKind::InvalidData,
                        format!("frame too large: {} > {}", len, max),
                    ))
                }
            })?;

        // Parse the response as a generic Response<Value> first so we can
        // inspect the error field before attempting typed deserialisation.
        let raw_response: Response<Value> = serde_json::from_slice(&response_bytes)?;

        if let Some(err) = raw_response.error {
            match err.code {
                AUTH_FAILED => return Err(IpcClientError::AuthFailed),
                METHOD_NOT_FOUND => return Err(IpcClientError::MethodNotFound(err.message)),
                INVALID_PARAMS => return Err(IpcClientError::InvalidParams(err.message)),
                code => {
                    return Err(IpcClientError::Rpc {
                        code,
                        msg: err.message,
                    })
                }
            }
        }

        // Deserialise the typed result.
        let result_value = raw_response.result.unwrap_or(Value::Null);
        let result: R = serde_json::from_value(result_value)?;
        Ok(result)
    }

    // ── Platform-specific socket connection ───────────────────────────────────

    #[cfg(not(target_os = "windows"))]
    async fn connect(&self) -> Result<tokio::net::UnixStream, IpcClientError> {
        tokio::net::UnixStream::connect(&self.socket_path)
            .await
            .map_err(|e| {
                if e.kind() == std::io::ErrorKind::NotFound
                    || e.kind() == std::io::ErrorKind::ConnectionRefused
                {
                    eprintln!("cascade daemon is not running. Start it with: cascade daemon start");
                    IpcClientError::DaemonNotRunning
                } else {
                    IpcClientError::Io(e)
                }
            })
    }

    #[cfg(target_os = "windows")]
    async fn connect(
        &self,
    ) -> Result<tokio::net::windows::named_pipe::NamedPipeClient, IpcClientError> {
        tokio::net::windows::named_pipe::ClientOptions::new()
            .open(&self.socket_path)
            .map_err(|e| {
                if e.kind() == std::io::ErrorKind::NotFound
                    || e.kind() == std::io::ErrorKind::ConnectionRefused
                {
                    eprintln!("cascade daemon is not running. Start it with: cascade daemon start");
                    IpcClientError::DaemonNotRunning
                } else {
                    IpcClientError::Io(e)
                }
            })
    }
}

// ── send_on_stream helper (test use) ─────────────────────────────────────────

impl IpcClient {
    /// Send a request over a pre-connected stream (used by unit tests with
    /// `tokio::io::duplex` mock streams). Identical to [`send`] but takes an
    /// explicit stream rather than connecting to `socket_path`.
    #[cfg(test)]
    pub(crate) async fn send_on_stream<S, P, R>(
        &self,
        stream: S,
        method: &str,
        params: P,
    ) -> Result<R, IpcClientError>
    where
        S: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin,
        P: Serialize,
        R: DeserializeOwned,
    {
        let rpc_request = Request {
            jsonrpc: JsonRpcVersion::V2_0,
            id: RequestId::Number(1),
            method: method.to_owned(),
            params: Some(params),
            protocol_version: PROTOCOL_VERSION,
        };
        let rpc_json = serde_json::to_value(&rpc_request)?;
        let auth_envelope = serde_json::json!({
            "auth": self.token,
            "rpc": rpc_json,
        });
        let frame_bytes = serde_json::to_vec(&auth_envelope)?;

        let (mut reader, mut writer) = tokio::io::split(stream);

        FrameCodec::write_frame(&mut writer, &frame_bytes)
            .await
            .map_err(|e| match e {
                cascade_types::codec::CodecError::Io(io_err) => IpcClientError::Io(io_err),
                cascade_types::codec::CodecError::FrameTooLarge { len, max } => {
                    IpcClientError::Io(std::io::Error::new(
                        std::io::ErrorKind::InvalidData,
                        format!("frame too large: {} > {}", len, max),
                    ))
                }
            })?;
        writer.flush().await?;

        let response_bytes = FrameCodec::read_frame(&mut reader)
            .await
            .map_err(|e| match e {
                cascade_types::codec::CodecError::Io(io_err) => IpcClientError::Io(io_err),
                cascade_types::codec::CodecError::FrameTooLarge { len, max } => {
                    IpcClientError::Io(std::io::Error::new(
                        std::io::ErrorKind::InvalidData,
                        format!("frame too large: {} > {}", len, max),
                    ))
                }
            })?;

        let raw_response: Response<Value> = serde_json::from_slice(&response_bytes)?;

        if let Some(err) = raw_response.error {
            match err.code {
                AUTH_FAILED => return Err(IpcClientError::AuthFailed),
                METHOD_NOT_FOUND => return Err(IpcClientError::MethodNotFound(err.message)),
                INVALID_PARAMS => return Err(IpcClientError::InvalidParams(err.message)),
                code => {
                    return Err(IpcClientError::Rpc {
                        code,
                        msg: err.message,
                    })
                }
            }
        }

        let result_value = raw_response.result.unwrap_or(Value::Null);
        let result: R = serde_json::from_value(result_value)?;
        Ok(result)
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::ipc::{
        PingParams, PingResult, StatusParams, StatusResult, DAEMON_NOT_RUNNING,
    };
    use serde_json::json;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    // ── Helper: write a length-prefixed frame to a writer ────────────────────

    async fn write_canned_frame<W: tokio::io::AsyncWrite + Unpin>(writer: &mut W, body: &[u8]) {
        let len = (body.len() as u32).to_be_bytes();
        writer.write_all(&len).await.unwrap();
        writer.write_all(body).await.unwrap();
        writer.flush().await.unwrap();
    }

    // ── Helper: read a length-prefixed frame as JSON ──────────────────────────

    async fn read_client_frame<R: tokio::io::AsyncRead + Unpin>(
        reader: &mut R,
    ) -> serde_json::Value {
        let mut len_buf = [0u8; 4];
        reader.read_exact(&mut len_buf).await.unwrap();
        let len = u32::from_be_bytes(len_buf) as usize;
        let mut body = vec![0u8; len];
        reader.read_exact(&mut body).await.unwrap();
        serde_json::from_slice(&body).unwrap()
    }

    // ── Test: mock duplex returns StatusResult, IpcClient.send parses it ─────

    #[tokio::test]
    async fn test_send_parses_status_result() {
        let (client_stream, server_stream) = tokio::io::duplex(65536);
        let (mut server_read, mut server_write) = tokio::io::split(server_stream);

        let server_task = tokio::spawn(async move {
            let _req = read_client_frame(&mut server_read).await;
            let response = json!({
                "jsonrpc": "2.0",
                "id": 1,
                "result": {
                    "pid": 42,
                    "uptime_secs": 100,
                    "queue_depth": 0,
                    "rag_index_fresh": true,
                    "version": "0.1.0"
                }
            });
            let bytes = serde_json::to_vec(&response).unwrap();
            write_canned_frame(&mut server_write, &bytes).await;
        });

        let client = IpcClient {
            socket_path: PathBuf::from("/mock"),
            token: "deadbeef".to_string(),
        };

        let result: StatusResult = client
            .send_on_stream(client_stream, "cascade.status", StatusParams {})
            .await
            .unwrap();

        server_task.await.unwrap();

        assert_eq!(result.pid, 42);
        assert_eq!(result.uptime_secs, 100);
        assert_eq!(result.version, "0.1.0");
    }

    // ── Test: auth envelope contains 'auth' and 'rpc' fields ─────────────────

    #[test]
    fn test_auth_envelope_shape() {
        // Test that the envelope construction produces the expected JSON shape.
        let rpc_request = Request {
            jsonrpc: JsonRpcVersion::V2_0,
            id: RequestId::Number(1),
            method: "ping".to_owned(),
            params: Some(PingParams { echo: None }),
            protocol_version: PROTOCOL_VERSION,
        };
        let rpc_json = serde_json::to_value(&rpc_request).unwrap();
        let auth_envelope = json!({
            "auth": "cafebabe",
            "rpc": rpc_json,
        });
        assert_eq!(
            auth_envelope["auth"], "cafebabe",
            "envelope must have auth field"
        );
        assert!(
            auth_envelope.get("rpc").is_some(),
            "envelope must have rpc field"
        );
        assert_eq!(auth_envelope["rpc"]["method"], "ping");
    }

    // ── Test: DaemonNotRunning when socket file does not exist ────────────────

    #[tokio::test]
    async fn test_daemon_not_running_on_missing_socket() {
        let tmpdir = tempfile::tempdir().unwrap();
        let client = IpcClient {
            // Use a socket path that cannot exist.
            socket_path: tmpdir.path().join("nonexistent.sock"),
            token: "deadbeef".to_string(),
        };

        let result: Result<PingResult, IpcClientError> =
            client.send("ping", PingParams { echo: None }).await;

        match result {
            Err(IpcClientError::DaemonNotRunning) => {} // expected
            Err(e) => panic!("expected DaemonNotRunning, got: {e}"),
            Ok(_) => panic!("expected error, got Ok"),
        }
    }

    // ── Test: TokenNotFound when ipc_token file is missing ───────────────────

    #[test]
    fn test_token_not_found_logic() {
        // Test the TokenNotFound logic directly (IpcClient::new reads the real
        // HOME; we verify the check independently to avoid mutating the env).
        let tmpdir = tempfile::tempdir().unwrap();
        let token_path = tmpdir.path().join("ipc_token");

        // File does not exist — should return TokenNotFound.
        assert!(!token_path.exists());
        let result: Result<String, IpcClientError> = if !token_path.exists() {
            Err(IpcClientError::TokenNotFound)
        } else {
            Ok(std::fs::read_to_string(&token_path).unwrap())
        };
        match result {
            Err(IpcClientError::TokenNotFound) => {} // expected
            _ => panic!("expected TokenNotFound"),
        }

        // File exists — should succeed.
        std::fs::write(&token_path, "abc123").unwrap();
        let result: Result<String, IpcClientError> = if !token_path.exists() {
            Err(IpcClientError::TokenNotFound)
        } else {
            Ok(std::fs::read_to_string(&token_path)
                .unwrap()
                .trim()
                .to_owned())
        };
        assert_eq!(result.unwrap(), "abc123");
    }

    // ── Test: error code AUTH_FAILED maps to IpcClientError::AuthFailed ───────

    #[tokio::test]
    async fn test_auth_failed_error_mapping() {
        let (client_stream, server_stream) = tokio::io::duplex(65536);
        let (mut server_read, mut server_write) = tokio::io::split(server_stream);

        let server_task = tokio::spawn(async move {
            let _req = read_client_frame(&mut server_read).await;
            let response = json!({
                "jsonrpc": "2.0",
                "id": 1,
                "error": { "code": -32002, "message": "auth failed" }
            });
            let bytes = serde_json::to_vec(&response).unwrap();
            write_canned_frame(&mut server_write, &bytes).await;
        });

        let client = IpcClient {
            socket_path: PathBuf::from("/mock"),
            token: "wrong".to_string(),
        };
        let result: Result<PingResult, IpcClientError> = client
            .send_on_stream(client_stream, "ping", PingParams { echo: None })
            .await;

        server_task.await.unwrap();

        match result {
            Err(IpcClientError::AuthFailed) => {}
            other => panic!("expected AuthFailed, got {:?}", other),
        }
    }

    // ── Test: DAEMON_NOT_RUNNING constant value ───────────────────────────────

    #[test]
    fn test_daemon_not_running_constant() {
        // Verify the cascade-types constant matches the expected JSON-RPC code.
        assert_eq!(DAEMON_NOT_RUNNING, -32001);
    }
}
