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
//! ## Dispatch routing (ADR-P3-001)
//!
//! Two-path routing keeps the frozen legacy enum working while enabling
//! the typed JSON-RPC scaffold for future methods (E-07+):
//!
//!   1. **Legacy path** — `serde_json::from_slice::<Request>(&body)` handles
//!      the 8 frozen P2 methods. If it succeeds, dispatch proceeds unchanged.
//!   2. **Typed scaffold** — on legacy-parse failure, `try_typed_dispatch` is
//!      called. It validates the JSON-RPC envelope via
//!      `cascade_types::ipc::deserialize_request`, checks `protocol_version`,
//!      and returns `METHOD_NOT_FOUND` for any method not yet implemented.
//!      Future epics (E-07 settings, etc.) add handlers here without touching
//!      the frozen `Request` enum.
//!
//! IPC schema is FROZEN after S06-FREEZE. Schema changes require a versioning
//! ticket before any widget (S10-S13) or MCP layer (E7) is updated.

use std::path::PathBuf;
use std::sync::Arc;

use cascade_types::ipc::{self as typed_ipc, PROTOCOL_VERSION};
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
    InboxSummary {
        limit: Option<usize>,
    },
    HotwordLookup {
        word: String,
    },
    ProviderQuota,
    /// Read the persisted quota store (~/.cascade/quota-store.json). Wired per
    /// T-P2-E02-31; handler lives in ipc_handlers::handle_read_quota_store.
    ReadQuotaStore,
    DaemonStop,
    Ping {
        echo: Option<String>,
    },
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

        // Two-path dispatch (ADR-P3-001):
        // 1. Legacy path — try the frozen 8-method enum first.
        // 2. Typed scaffold — on legacy-parse failure, validate the JSON-RPC
        //    envelope via cascade_types and return METHOD_NOT_FOUND (or a
        //    structured IpcError) until E-07+ handlers are wired.
        let response = match serde_json::from_slice::<Request>(&body) {
            Ok(req) => dispatch(&server, req).await,
            Err(_legacy_err) => try_typed_dispatch(&server, &body),
        };
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
        Request::ReadQuotaStore => match crate::ipc_handlers::handle_read_quota_store().await {
            Ok(store) => Response::ok(store),
            Err(e) => Response::err(-32004, e.to_string()),
        },
        Request::DaemonStop => {
            // Actual cancellation is handled by the signal handler; here we
            // just acknowledge and let the supervisor wind down naturally.
            info!("stop requested via IPC");
            Response::ok(serde_json::json!({ "status": "stopping" }))
        }
    }
}

/// Typed JSON-RPC dispatch scaffold (ADR-P3-001).
///
/// Purpose: Validate and route requests that don't match the frozen legacy
///   `Request` enum. Checks the JSON-RPC envelope for unknown fields and
///   validates `protocol_version`. Returns `METHOD_NOT_FOUND` for any method
///   since no typed handlers exist yet (they arrive in E-07+).
/// Inputs: `server` — IPC server handle; `body` — raw UTF-8 JSON frame bytes
/// Outputs: `Response::Error` with appropriate JSON-RPC error code
/// Constraints: body must already be bounds-checked (MAX_FRAME_LEN enforced upstream)
/// SPORT: MASTER-ENDPOINTS.md — IPC routing note updated to "typed JSON-RPC (ADR-P3-001)"
pub(crate) fn try_typed_dispatch(_server: &IpcServer, body: &[u8]) -> Response {
    let raw: serde_json::Value = match serde_json::from_slice(body) {
        Ok(v) => v,
        Err(e) => {
            debug!(%e, "typed dispatch: not valid JSON");
            return Response::err(-32700, format!("parse error: {e}"));
        }
    };

    let version = raw
        .get("protocol_version")
        .and_then(|v| v.as_u64())
        .unwrap_or(PROTOCOL_VERSION as u64) as u8;
    if version != PROTOCOL_VERSION {
        warn!(got = version, expected = PROTOCOL_VERSION, "protocol_version mismatch");
        return Response::err(
            -32600,
            format!(
                "protocol_version mismatch: got {version}, expected {PROTOCOL_VERSION}"
            ),
        );
    }

    match typed_ipc::deserialize_request::<serde_json::Value>(body) {
        Err(e) => {
            use cascade_types::error::IpcError;
            debug!(?e, "typed dispatch: envelope error");
            let (code, msg) = match e {
                IpcError::UnknownField(m) => (-32600, format!("unknown field: {m}")),
                IpcError::MissingField(m) => (-32600, format!("missing field: {m}")),
                IpcError::MalformedFrame(m) => (-32700, format!("malformed frame: {m}")),
                IpcError::InvalidFieldValue { field, reason } => (
                    -32602,
                    format!("invalid field '{field}': {reason}"),
                ),
            };
            Response::err(code, msg)
        }
        Ok(typed_req) => {
            // Audit hook — fires for all five privileged typed methods before their
            // implementations land (E-07+).  The hook records a dispatch attempt so
            // the audit trail captures the actor and target even while the method
            // still returns METHOD_NOT_FOUND.  Write-then-audit ordering is N/A here
            // (no operation succeeds yet); the hook records the *attempt*.
            //
            // When a real handler is added for one of these methods in a future ticket,
            // move the audit::record() call to AFTER the operation succeeds (write-then-
            // audit) and remove the hook entry below for that method.
            //
            // SPORT: MASTER-ENDPOINTS.md — audit=hook annotation for these five methods.
            // Resolves P2 residue E07-17 (hook wired; full instrumentation in E-07+).
            use cascade_audit::AuditOp;
            match typed_req.method.as_str() {
                "gci_write" => {
                    crate::audit::record(
                        AuditOp::GciWrite,
                        "cascaded",
                        &typed_req.method,
                        "typed-dispatch-hook: METHOD_NOT_FOUND (handler pending E-07+)",
                    );
                }
                "symlink_create" => {
                    crate::audit::record(
                        AuditOp::SymlinkCreate,
                        "cascaded",
                        &typed_req.method,
                        "typed-dispatch-hook: METHOD_NOT_FOUND (handler pending E-07+)",
                    );
                }
                "symlink_delete" => {
                    crate::audit::record(
                        AuditOp::SymlinkDelete,
                        "cascaded",
                        &typed_req.method,
                        "typed-dispatch-hook: METHOD_NOT_FOUND (handler pending E-07+)",
                    );
                }
                "cascade_resolve" => {
                    crate::audit::record(
                        AuditOp::CascadeResolve,
                        "cascaded",
                        &typed_req.method,
                        "typed-dispatch-hook: METHOD_NOT_FOUND (handler pending E-07+)",
                    );
                }
                "key_rotation" => {
                    crate::audit::record(
                        AuditOp::KeyRotation,
                        "cascaded",
                        &typed_req.method,
                        "typed-dispatch-hook: METHOD_NOT_FOUND (handler pending E-07+)",
                    );
                }
                _ => {}
            }
            debug!(method = %typed_req.method, "typed dispatch: METHOD_NOT_FOUND (scaffold)");
            Response::err(
                -32601,
                format!("method not found: {}", typed_req.method),
            )
        }
    }
}
