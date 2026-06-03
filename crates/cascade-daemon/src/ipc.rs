//! Unix domain socket IPC server for cascaded.
//!
//! Purpose: Accept JSON-framed requests from cascade-app, widgets, and CLI
//! tools over `~/.cascade/daemon.sock`. Each connection is handled in its own
//! tokio task. Windows uses a Named Pipe (`\\.\pipe\cascade-daemon`) instead.
//!
//! Protocol (documented in cascade/docs/ipc-protocol.md):
//!   Request  — JSON object with a `method` field + optional `params`
//!   Response — JSON object with `result` (success) or `error` (failure)
//!   Framing  — length-prefixed: 4-byte LE u32 length followed by UTF-8 JSON
//!
//! IPC schema is FROZEN after S06-FREEZE. Schema changes require a versioning
//! ticket before any widget (S10-S13) or MCP layer (E7) is updated.

use std::path::PathBuf;
use std::sync::Arc;

use serde::{Deserialize, Serialize};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio_util::sync::CancellationToken;
use tracing::{debug, error, info, warn};

use crate::event_bus::EventBus;
use crate::healthcheck::HealthState;
use crate::supervisor::DaemonError;

const SOCKET_NAME: &str = "daemon.sock";
const MAX_FRAME_LEN: usize = 1024 * 256; // 256 KiB — prevents runaway allocations

// ── Request / Response (frozen schema) ───────────────────────────────────

/// All IPC request types understood by cascaded.
/// FROZEN — do not add variants without creating a versioning ticket.
#[derive(Debug, Deserialize)]
#[serde(tag = "method", rename_all = "snake_case")]
pub enum Request {
    Health,
    Status,
    InboxSummary { limit: Option<usize> },
    HotwordLookup { word: String },
    ProviderQuota,
    DaemonStop,
    Ping { echo: Option<String> },
}

/// All IPC response shapes.
#[derive(Debug, Serialize)]
#[serde(untagged)]
pub enum Response {
    Ok(serde_json::Value),
    Error { code: i32, message: String },
}

impl Response {
    pub fn ok(v: impl Serialize) -> Self {
        Response::Ok(serde_json::to_value(v).unwrap_or(serde_json::Value::Null))
    }
    pub fn err(code: i32, msg: impl Into<String>) -> Self {
        Response::Error {
            code,
            message: msg.into(),
        }
    }
}

// ── Server ────────────────────────────────────────────────────────────────

pub struct IpcServer {
    socket_path: PathBuf,
    health: Arc<HealthState>,
    bus: Arc<EventBus>,
}

impl IpcServer {
    pub async fn new(
        config_dir: PathBuf,
        health: Arc<HealthState>,
        bus: Arc<EventBus>,
    ) -> Result<Self, DaemonError> {
        let socket_path = config_dir.join(SOCKET_NAME);
        Ok(Self {
            socket_path,
            health,
            bus,
        })
    }

    /// Bind the Unix socket and serve connections until `shutdown` fires.
    pub async fn serve(self, shutdown: CancellationToken) -> Result<(), DaemonError> {
        #[cfg(unix)]
        return self.serve_unix(shutdown).await;
        #[cfg(windows)]
        return self.serve_named_pipe(shutdown).await;
        #[allow(unreachable_code)]
        Err(DaemonError::UnsupportedPlatform)
    }

    #[cfg(unix)]
    async fn serve_unix(self, shutdown: CancellationToken) -> Result<(), DaemonError> {
        use tokio::net::UnixListener;

        // Remove stale socket from a previous run.
        let _ = tokio::fs::remove_file(&self.socket_path).await;

        let listener = UnixListener::bind(&self.socket_path).map_err(DaemonError::Io)?;
        info!(path = %self.socket_path.display(), "IPC socket listening");

        let server = Arc::new(self);
        loop {
            tokio::select! {
                accepted = listener.accept() => {
                    match accepted {
                        Ok((stream, _)) => {
                            let srv = Arc::clone(&server);
                            tokio::spawn(async move {
                                let (reader, writer) = stream.into_split();
                                if let Err(e) = handle_connection(srv, reader, writer).await {
                                    warn!(%e, "IPC connection error");
                                }
                            });
                        }
                        Err(e) => error!(%e, "accept error"),
                    }
                }
                _ = shutdown.cancelled() => {
                    info!("IPC server shutting down");
                    break;
                }
            }
        }
        let _ = tokio::fs::remove_file(&server.socket_path).await;
        Ok(())
    }

    #[cfg(windows)]
    async fn serve_named_pipe(self, shutdown: CancellationToken) -> Result<(), DaemonError> {
        use tokio::net::windows::named_pipe::{PipeMode, ServerOptions};
        const PIPE_NAME: &str = r"\\.\pipe\cascade-daemon";

        let server = Arc::new(self);
        loop {
            let mut pipe = ServerOptions::new()
                .pipe_mode(PipeMode::Message)
                .create(PIPE_NAME)
                .map_err(DaemonError::Io)?;

            tokio::select! {
                result = pipe.connect() => {
                    result.map_err(DaemonError::Io)?;
                    let srv = Arc::clone(&server);
                    tokio::spawn(async move {
                        let (reader, writer) = tokio::io::split(pipe);
                        if let Err(e) = handle_connection(srv, reader, writer).await {
                            warn!(%e, "IPC connection error");
                        }
                    });
                }
                _ = shutdown.cancelled() => { break; }
            }
        }
        Ok(())
    }
}

// ── Connection handler ────────────────────────────────────────────────────

async fn handle_connection<R, W>(
    server: Arc<IpcServer>,
    mut reader: R,
    mut writer: W,
) -> Result<(), DaemonError>
where
    R: AsyncReadExt + Unpin,
    W: AsyncWriteExt + Unpin,
{
    loop {
        // Read 4-byte LE length prefix.
        let mut len_buf = [0u8; 4];
        match reader.read_exact(&mut len_buf).await {
            Ok(_) => {}
            Err(e) if e.kind() == std::io::ErrorKind::UnexpectedEof => break,
            Err(e) => return Err(DaemonError::Io(e)),
        }
        let len = u32::from_le_bytes(len_buf) as usize;
        if len > MAX_FRAME_LEN {
            let resp = Response::err(-32600, "frame too large");
            write_response(&mut writer, &resp).await?;
            break;
        }

        let mut body = vec![0u8; len];
        reader
            .read_exact(&mut body)
            .await
            .map_err(DaemonError::Io)?;

        let request: Request = match serde_json::from_slice(&body) {
            Ok(r) => r,
            Err(e) => {
                debug!(%e, "malformed IPC request");
                let resp = Response::err(-32700, format!("parse error: {e}"));
                write_response(&mut writer, &resp).await?;
                continue;
            }
        };

        let response = dispatch(&server, request).await;
        write_response(&mut writer, &response).await?;
    }
    Ok(())
}

async fn write_response<W: AsyncWriteExt + Unpin>(
    writer: &mut W,
    resp: &Response,
) -> Result<(), DaemonError> {
    let bytes = serde_json::to_vec(resp).unwrap_or_default();
    let len = (bytes.len() as u32).to_le_bytes();
    writer.write_all(&len).await.map_err(DaemonError::Io)?;
    writer.write_all(&bytes).await.map_err(DaemonError::Io)?;
    Ok(())
}

// ── Request dispatch ──────────────────────────────────────────────────────

async fn dispatch(server: &IpcServer, req: Request) -> Response {
    match req {
        Request::Health => {
            let snap = server.health.snapshot();
            Response::ok(snap)
        }
        Request::Status => {
            let snap = server.health.snapshot();
            Response::ok(snap)
        }
        Request::Ping { echo } => {
            Response::ok(serde_json::json!({ "pong": echo.unwrap_or_default() }))
        }
        Request::InboxSummary { limit } => {
            match server.bus.inbox_summary(limit.unwrap_or(5)).await {
                Ok(items) => Response::ok(items),
                Err(e) => Response::err(-32001, e.to_string()),
            }
        }
        Request::HotwordLookup { word } => match server.bus.hotword_lookup(&word).await {
            Ok(Some(block)) => Response::ok(serde_json::json!({ "block": block })),
            Ok(None) => Response::ok(serde_json::json!({ "block": null })),
            Err(e) => Response::err(-32002, e.to_string()),
        },
        Request::ProviderQuota => match server.bus.provider_quota().await {
            Ok(quota) => Response::ok(quota),
            Err(e) => Response::err(-32003, e.to_string()),
        },
        Request::DaemonStop => {
            // Actual cancellation is handled by the signal handler; here we
            // just acknowledge and let the supervisor wind down naturally.
            info!("stop requested via IPC");
            Response::ok(serde_json::json!({ "status": "stopping" }))
        }
    }
}
