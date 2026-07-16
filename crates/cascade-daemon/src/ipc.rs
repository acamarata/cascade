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

use cascade_rag::index_manager::IndexRegistry;
use cascade_types::ipc::{self as typed_ipc, RequestId, PROTOCOL_VERSION};
use serde::{Deserialize, Serialize};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio_util::sync::CancellationToken;
use tracing::{debug, error, info, warn};

use cascade_core::tasks::{open_tasks_db, KanbanTaskStore};

use crate::chunk_cache::ChunkCache;
use crate::event_bus::EventBus;
use crate::healthcheck::HealthState;
use crate::ipc_providers::ProviderIpcHandler;
use crate::ipc_tasks::{
    handle_task_create, handle_task_delete, handle_task_get, handle_task_list, handle_task_move,
    handle_task_update,
};
use crate::ipc_usage_analytics::{
    handle_usage_history, handle_usage_ledger, handle_usage_summary, new_summary_cache,
    SummaryCache,
};
use crate::search_handler::{dispatch_rag, RagSearchHandler};
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
    auth_token: String,
    health: Arc<HealthState>,
    bus: Arc<EventBus>,
    /// Cancellation token for the whole daemon.  Cancelled when
    /// `Request::DaemonStop` is received so the supervisor exits cleanly
    /// (D10 — DaemonStop previously acknowledged the request but did nothing).
    daemon_shutdown: Option<CancellationToken>,
    /// In-memory 60-second summary cache for cascade_usage_summary (T-P3-E04-29).
    usage_summary_cache: SummaryCache,
    /// RAG IPC method handlers (rag.search / rag.ingest_file / rag.list_sources /
    /// rag.index_stats). Wired per T-P4-E01-29.  Uses MockEmbedModel until a
    /// real BGE-M3 embedder is injected in a later ticket.
    rag_handler: Arc<RagSearchHandler>,
    /// In-memory mtime-validated chunk cache (T-P4-E04-10).
    /// Surfaced via `cache.stats` and cleared via `cache.clear --chunk`.
    file_chunk_cache: Arc<ChunkCache>,
    /// RAG query result LRU cache (T-P4-E04-07 / T-P4-E04-11).
    /// Shared with the RAG pipeline; surfaced via `cache.stats`.
    query_cache: Arc<cascade_rag::cache::QueryCache>,
    /// RAG embedding SQLite cache (T-P4-E04-09 / T-P4-E04-11).
    /// Shared with the RAG pipeline; surfaced via `cache.stats`.
    embed_cache: Arc<cascade_rag::cache::EmbedCache>,
    /// CEO orchestrator runtime (E-P6-04).
    /// Shared via Arc so connection tasks can call CEO IPC methods concurrently.
    ceo_runtime: Arc<crate::ipc_ceo::CeoRuntime>,
    /// Kanban Task store (E-P8-01).
    /// SQLite-backed; opened at daemon start from config_dir/tasks.db.
    task_store: Arc<KanbanTaskStore>,
    /// Provider IPC handler (D2): routes cascade_providers_* commands.
    /// Shared via Arc so connection tasks can call provider commands concurrently.
    provider_handler: Arc<ProviderIpcHandler>,
}

impl IpcServer {
    #[allow(dead_code)]
    pub async fn new(
        config_dir: PathBuf,
        health: Arc<HealthState>,
        bus: Arc<EventBus>,
        provider_registry: Arc<cascade_providers::ProviderRegistry>,
    ) -> Result<Self, DaemonError> {
        Self::new_with_socket(config_dir, health, bus, provider_registry, None).await
    }

    /// Construct an IpcServer with an optional socket path override.
    ///
    /// When `socket_override` is `Some(path)` and non-empty, that path is used
    /// instead of `config_dir/daemon.sock`.  Called from supervisor.rs to honour
    /// `[daemon] socket_path` from config.toml.
    pub async fn new_with_socket(
        config_dir: PathBuf,
        health: Arc<HealthState>,
        bus: Arc<EventBus>,
        provider_registry: Arc<cascade_providers::ProviderRegistry>,
        socket_override: Option<PathBuf>,
    ) -> Result<Self, DaemonError> {
        let socket_path = match socket_override {
            Some(p) if p.as_os_str().is_empty() => config_dir.join(SOCKET_NAME),
            Some(p) => p,
            None => config_dir.join(SOCKET_NAME),
        };

        // Provision the IPC auth token the CLI reads from ~/.cascade/ipc_token to
        // sign requests. Previously this file was never written, so every IPC
        // client failed with "IPC token not found — is the daemon running?".
        let _ = std::fs::create_dir_all(&config_dir);
        let token_path = config_dir.join("ipc_token");
        let token = format!(
            "{}{}",
            uuid::Uuid::new_v4().simple(),
            uuid::Uuid::new_v4().simple()
        );
        if std::fs::write(&token_path, &token).is_ok() {
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                let _ =
                    std::fs::set_permissions(&token_path, std::fs::Permissions::from_mode(0o600));
            }
        }

        // RAG handler: built INSTANTLY so daemon startup is never blocked.
        // The embedder is a LazyEmbedModel (mock now, real ONNX swapped in by a
        // background task); the BGE reranker is loaded by a separate background
        // task. Both loads are offline-graceful (Err -> mock / skip, no panic).
        //
        // CASCADE_OFFLINE_MODELS=1 skips the background download tasks entirely.
        // Used in CI (daemon-ci.yml) to prevent ~4 GB ONNX model downloads from
        // filling the self-hosted runner disk during integration test runs.
        let offline_models = std::env::var("CASCADE_OFFLINE_MODELS").is_ok();
        // Shared process-global embedder: the same instance backs the indexer
        // WorkerPool (supervisor.rs). spawn_load() is idempotent, so the real
        // ONNX model is downloaded/warmed exactly once per process.
        let lazy_embed = cascade_rag::embed::LazyEmbedModel::shared();
        if !offline_models {
            lazy_embed.spawn_load();
        }
        let embed: Arc<dyn cascade_rag::embed::EmbedModel> = lazy_embed;
        let rag_handler = RagSearchHandler::new(IndexRegistry::new(), embed);
        if !offline_models {
            rag_handler.spawn_load_reranker();
        }
        // Caches (T-P4-E04-10/11): chunk cache with default capacity; RAG caches
        // with default capacities. These are shared via Arc so future tickets can
        // pass them into the RAG pipeline without cloning.
        let file_chunk_cache = Arc::new(ChunkCache::new(256));
        let query_cache = Arc::new(cascade_rag::cache::QueryCache::default());
        // EmbedCache: enabled now that a real BGE-M3 model is wired in.
        // Falls back to disabled() if the SQLite open fails.
        let embed_cache_dir = config_dir.join("embed-cache");
        let _ = std::fs::create_dir_all(&embed_cache_dir);
        let embed_cache = Arc::new(
            cascade_rag::cache::EmbedCache::open(&embed_cache_dir, "bge-m3", "1.0", true)
                .unwrap_or_else(|_| cascade_rag::cache::EmbedCache::disabled()),
        );
        // CEO runtime: session files under ~/.cascade/agents/sessions/ (E-P6-04).
        // Wired to the real ProviderRegistry so directives use live LLM completions
        // via RegistryRouter + SafeToolInvoker (same pair as AutomationRunner).
        let ceo_session_dir = config_dir.join("agents").join("sessions");
        // Clone before moving into CeoRuntime so provider_handler below can
        // still take a clone of the same Arc.
        let registry_for_provider_handler = Arc::clone(&provider_registry);
        let ceo_runtime = Arc::new(crate::ipc_ceo::CeoRuntime::with_registry(
            Some(ceo_session_dir),
            provider_registry,
        ));

        // Kanban task store (E-P8-01): tasks.db in the config dir.
        let tasks_db_path = config_dir.join("tasks.db");
        let task_conn = open_tasks_db(&tasks_db_path).unwrap_or_else(|e| {
            tracing::warn!(%e, "failed to open tasks.db; task IPC will return errors");
            // Fallback: in-memory db so the daemon still starts.
            let conn = rusqlite::Connection::open_in_memory().unwrap();
            std::sync::Arc::new(std::sync::Mutex::new(conn))
        });
        let task_store = Arc::new(KanbanTaskStore::new(task_conn));

        // Provider IPC handler (D2): opened once and shared via Arc.
        // Usage DB lives next to the tasks DB (provider-usage.db).
        let provider_usage_path = config_dir.join("provider-usage.db");
        let usage_acc = Arc::new(
            crate::usage::UsageAccumulator::new(&provider_usage_path).unwrap_or_else(|e| {
                tracing::warn!(%e, "provider usage DB open failed; usage tracking disabled");
                // Fallback to in-memory accumulator so the daemon still starts.
                crate::usage::UsageAccumulator::new(":memory:")
                    .expect("in-memory accumulator must open")
            }),
        );
        // WHY fresh empty health state here: the daemon's live provider health
        // state lives in main.rs and is populated by the background health-check
        // task (spawn_health_check_task). Passing it through the IPC constructor
        // chain is a larger refactor than D2 scope.  An empty health state means
        // cascade_providers_list returns status="unknown" for all providers until
        // a full health-state injection is wired (tracked separately).
        let provider_health_state: crate::provider_health::HealthState =
            Arc::new(tokio::sync::RwLock::new(std::collections::HashMap::new()));
        let provider_handler = Arc::new(ProviderIpcHandler::new(
            registry_for_provider_handler,
            provider_health_state,
            usage_acc,
        ));

        Ok(Self {
            socket_path,
            auth_token: token,
            health,
            bus,
            daemon_shutdown: None,
            usage_summary_cache: new_summary_cache(),
            rag_handler,
            file_chunk_cache,
            query_cache,
            embed_cache,
            ceo_runtime,
            task_store,
            provider_handler,
        })
    }

    /// Attach the daemon-level shutdown token so that `DaemonStop` IPC requests
    /// actually cancel the running daemon (D10).
    ///
    /// Call this immediately after `IpcServer::new_with_socket` and before `serve`.
    pub fn set_daemon_shutdown(&mut self, token: CancellationToken) {
        self.daemon_shutdown = Some(token);
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
        // Big-endian length prefix to match the canonical cascade-types FrameCodec
        // (and the CLI IpcClient). A prior little-endian mismatch here made every
        // CLI<->daemon frame unreadable (cascade ping/status/harness failed).
        let len = u32::from_be_bytes(len_buf) as usize;
        if len > MAX_FRAME_LEN {
            let resp = Response::err(-32600, "frame too large");
            write_response(&mut writer, &resp, RequestId::Number(1)).await?;
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
        // The CLI IpcClient wraps requests as {"auth": <token>, "rpc": <jsonrpc>}.
        // Unwrap to the inner rpc for dispatch; fall back to the raw body for
        // direct/legacy callers that send the request unwrapped.
        //
        // Extract the JSON-RPC `id` from the envelope so we can echo it back in
        // the response (JSON-RPC 2.0 requires the response `id` to match the
        // request `id`). Default to Number(1) for legacy callers that omit it.
        let parsed_envelope: serde_json::Value =
            serde_json::from_slice(&body).unwrap_or(serde_json::Value::Null);
        let request_id: RequestId = parsed_envelope
            .get("rpc")
            .and_then(|rpc| rpc.get("id"))
            .and_then(|id| serde_json::from_value(id.clone()).ok())
            .unwrap_or(RequestId::Number(1));

        let auth_ok = parsed_envelope
            .get("auth")
            .and_then(|auth| auth.as_str())
            .is_some_and(|auth| constant_time_token_eq(&server.auth_token, auth));
        if !auth_ok {
            let resp = Response::err(typed_ipc::AUTH_FAILED, "auth failed");
            write_response(&mut writer, &resp, request_id).await?;
            break;
        }

        let dispatch_body: Vec<u8> = if parsed_envelope.get("rpc").is_some() {
            serde_json::to_vec(&parsed_envelope["rpc"]).unwrap_or(body)
        } else {
            body
        };
        // A typed JSON-RPC frame carries `jsonrpc`/`protocol_version` and puts its
        // arguments under `params`. The legacy `Request` enum ignores unknown fields,
        // so such a frame ALSO deserializes into it — with every optional field set to
        // None (e.g. `Ping { echo: None }`). Dispatching legacy-first therefore
        // shadowed the typed handlers and silently dropped typed callers' params.
        // Route typed frames to the typed dispatcher first, falling back to legacy
        // only when the method is not implemented there.
        let looks_typed = parsed_envelope
            .get("rpc")
            .map(|rpc| rpc.get("jsonrpc").is_some() || rpc.get("protocol_version").is_some())
            .unwrap_or(false);

        let response = if looks_typed {
            let typed = try_typed_dispatch(&server, &dispatch_body).await;
            if is_method_not_found(&typed) {
                match serde_json::from_slice::<Request>(&dispatch_body) {
                    Ok(req) => dispatch(&server, req).await,
                    Err(_) => typed,
                }
            } else {
                typed
            }
        } else {
            match serde_json::from_slice::<Request>(&dispatch_body) {
                Ok(req) => dispatch(&server, req).await,
                Err(_legacy_err) => try_typed_dispatch(&server, &dispatch_body).await,
            }
        };
        write_response(&mut writer, &response, request_id).await?;
    }
    Ok(())
}

/// True when `resp` is a JSON-RPC `METHOD_NOT_FOUND` (-32601) error.
///
/// Used to decide whether a typed-dispatch miss should fall back to the legacy
/// `Request` enum dispatcher, which implements a wider set of methods.
fn is_method_not_found(resp: &Response) -> bool {
    matches!(resp, Response::Error { code, .. } if *code == typed_ipc::METHOD_NOT_FOUND)
}

fn constant_time_token_eq(expected: &str, supplied: &str) -> bool {
    let expected = expected.as_bytes();
    let supplied = supplied.as_bytes();
    let mut diff = expected.len() ^ supplied.len();
    let max_len = expected.len().max(supplied.len());

    for i in 0..max_len {
        let lhs = expected.get(i).copied().unwrap_or(0);
        let rhs = supplied.get(i).copied().unwrap_or(0);
        diff |= (lhs ^ rhs) as usize;
    }

    diff == 0
}

async fn write_response<W: AsyncWriteExt + Unpin>(
    writer: &mut W,
    resp: &Response,
    id: RequestId,
) -> Result<(), DaemonError> {
    // Wrap the daemon's internal Response in a JSON-RPC 2.0 envelope so the
    // CLI's IpcClient (which deserialises cascade_types::ipc::Response<Value>
    // with fields jsonrpc/id/result/error) can parse it.
    let envelope = match resp {
        Response::Ok(value) => serde_json::json!({
            "jsonrpc": "2.0",
            "id": id,
            "result": value,
        }),
        Response::Error { code, message } => serde_json::json!({
            "jsonrpc": "2.0",
            "id": id,
            "error": { "code": code, "message": message },
        }),
    };
    let bytes = serde_json::to_vec(&envelope).unwrap_or_default();
    let len = (bytes.len() as u32).to_be_bytes(); // big-endian: match FrameCodec / CLI
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
            info!("stop requested via IPC");
            // Cancel the daemon-level token so the supervisor exits cleanly (D10).
            if let Some(token) = &server.daemon_shutdown {
                token.cancel();
                info!("daemon shutdown token cancelled");
            } else {
                warn!("DaemonStop: no shutdown token wired — daemon will not exit via IPC");
            }
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
pub(crate) async fn try_typed_dispatch(server: &IpcServer, body: &[u8]) -> Response {
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
        warn!(
            got = version,
            expected = PROTOCOL_VERSION,
            "protocol_version mismatch"
        );
        return Response::err(
            -32600,
            format!("protocol_version mismatch: got {version}, expected {PROTOCOL_VERSION}"),
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
                IpcError::InvalidFieldValue { field, reason } => {
                    (-32602, format!("invalid field '{field}': {reason}"))
                }
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
            // ── Core liveness methods ────────────────────────────────────
            // The CLI IpcClient sends these via the typed JSON-RPC envelope, so
            // they must be handled here (the legacy `dispatch` only sees the old
            // enum form). Bridge them to the same health/ping logic. Without this,
            // `cascade ping` got METHOD_NOT_FOUND from a running daemon.
            match typed_req.method.as_str() {
                "ping" => {
                    let echo = typed_req
                        .params
                        .as_ref()
                        .and_then(|p| p.get("echo"))
                        .and_then(|v| v.as_str())
                        .unwrap_or_default()
                        .to_string();
                    return Response::ok(serde_json::json!({ "pong": echo }));
                }
                "status" | "health" => {
                    return Response::ok(server.health.snapshot());
                }
                _ => {}
            }

            // ── Usage analytics methods (T-P3-E04-29) ────────────────────
            // These are fully implemented handlers; wire them before the
            // audit-hook scaffold so they return real data, not NOT_FOUND.
            match typed_req.method.as_str() {
                "cascade_usage_summary" => {
                    use cascade_types::usage_analytics::UsagePeriod;
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    match serde_json::from_value::<UsagePeriod>(params_val) {
                        Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
                        Ok(period) => {
                            return match handle_usage_summary(period, &server.usage_summary_cache) {
                                Ok(summary) => Response::ok(summary),
                                Err(e) => Response::err(-32001, e),
                            };
                        }
                    }
                }
                "cascade_usage_history" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    let months_back = params_val
                        .get("monthsBack")
                        .or_else(|| params_val.get("months_back"))
                        .and_then(|v| v.as_u64())
                        .unwrap_or(6) as u32;
                    return match handle_usage_history(months_back) {
                        Ok(history) => Response::ok(history),
                        Err(e) => Response::err(-32001, e),
                    };
                }
                "cascade_usage_ledger" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    let account_email = params_val
                        .get("accountEmail")
                        .or_else(|| params_val.get("account_email"))
                        .and_then(|v| v.as_str())
                        .unwrap_or("")
                        .to_string();
                    use cascade_types::usage_analytics::UsagePeriod;
                    let period_val = params_val
                        .get("period")
                        .cloned()
                        .unwrap_or(serde_json::Value::Null);
                    let period: UsagePeriod = match serde_json::from_value(period_val) {
                        Ok(p) => p,
                        Err(e) => return Response::err(-32602, format!("invalid period: {e}")),
                    };
                    return match handle_usage_ledger(account_email, period) {
                        Ok(ledger) => Response::ok(ledger),
                        Err(e) => Response::err(-32001, e),
                    };
                }
                _ => {}
            }

            // ── CEO methods (E-P6-04) ────────────────────────────────────
            match typed_req.method.as_str() {
                "ceo_directive" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    let params: crate::ipc_ceo::CeoDirectiveParams =
                        match serde_json::from_value(params_val) {
                            Ok(p) => p,
                            Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
                        };
                    return match crate::ipc_ceo::handle_ceo_directive(&server.ceo_runtime, params)
                        .await
                    {
                        Ok(v) => Response::ok(v),
                        Err(e) => Response::err(-32001, e),
                    };
                }
                "ceo_status" => {
                    return match crate::ipc_ceo::handle_ceo_status(&server.ceo_runtime) {
                        Ok(v) => Response::ok(v),
                        Err(e) => Response::err(-32001, e),
                    };
                }
                "ceo_approvals" => {
                    return match crate::ipc_ceo::handle_ceo_approvals(&server.ceo_runtime) {
                        Ok(v) => Response::ok(v),
                        Err(e) => Response::err(-32001, e),
                    };
                }
                "ceo_approve" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    let params: crate::ipc_ceo::CeoApproveParams =
                        match serde_json::from_value(params_val) {
                            Ok(p) => p,
                            Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
                        };
                    return match crate::ipc_ceo::handle_ceo_approve(&server.ceo_runtime, params) {
                        Ok(v) => Response::ok(v),
                        Err(e) => Response::err(-32001, e),
                    };
                }
                "ceo_deny" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    let params: crate::ipc_ceo::CeoApproveParams =
                        match serde_json::from_value(params_val) {
                            Ok(p) => p,
                            Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
                        };
                    return match crate::ipc_ceo::handle_ceo_deny(&server.ceo_runtime, params) {
                        Ok(v) => Response::ok(v),
                        Err(e) => Response::err(-32001, e),
                    };
                }
                _ => {}
            }

            // ── Kanban Task methods (E-P8-01) ─────────────────────────────
            if typed_req.method.starts_with("task_") {
                let ts = Arc::clone(&server.task_store);
                let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                return match typed_req.method.as_str() {
                    "task_create" => handle_task_create(ts, params_val).await,
                    "task_get" => handle_task_get(ts, params_val).await,
                    "task_update" => handle_task_update(ts, params_val).await,
                    "task_list" => handle_task_list(ts, params_val).await,
                    "task_delete" => handle_task_delete(ts, params_val).await,
                    "task_move" => handle_task_move(ts, params_val).await,
                    other => Response::err(-32601, format!("method not found: {other}")),
                };
            }

            // ── Cache methods (T-P4-E04-11) ───────────────────────────────
            if typed_req.method.starts_with("cache.") {
                return dispatch_cache(server, &typed_req.method, typed_req.params).await;
            }

            // ── RAG methods (T-P4-E01-29) ─────────────────────────────────
            // Dispatched to RagSearchHandler; must be done before the audit-hook
            // scaffold so they return real data rather than METHOD_NOT_FOUND.
            if typed_req.method.starts_with("rag.") {
                return match dispatch_rag(
                    &server.rag_handler,
                    &typed_req.method,
                    typed_req.params.clone(),
                )
                .await
                {
                    Ok(value) => Response::ok(value),
                    Err((code, msg)) => Response::err(code, msg),
                };
            }

            // ── cascade_resolve (E-P5-02) ─────────────────────────────────
            if typed_req.method == "cascade_resolve" {
                let params_val = typed_req.params.clone().unwrap_or(serde_json::Value::Null);
                let params: cascade_types::ipc::ResolveParams =
                    match serde_json::from_value(params_val) {
                        Ok(p) => p,
                        Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
                    };

                // Validate tier slug if provided.
                if let Err(e) = cascade_types::ipc::validate_resolve_params(&params) {
                    return Response::err(-32602, format!("{e}"));
                }

                // Validate format if provided.
                let fmt = params.format.as_deref().unwrap_or("markdown");
                if fmt != "markdown" && fmt != "json" {
                    return Response::err(
                        -32602,
                        format!("invalid format `{fmt}`; must be `markdown` or `json`"),
                    );
                }

                // Resolve cwd: use params.cwd if provided, else daemon's own cwd.
                let cwd: std::path::PathBuf = match &params.cwd {
                    Some(p) => {
                        if !p.is_dir() {
                            return Response::err(
                                -32602,
                                format!("cwd `{}` is not an existing directory", p.display()),
                            );
                        }
                        p.clone()
                    }
                    None => match std::env::current_dir() {
                        Ok(d) => d,
                        Err(e) => {
                            return Response::err(-32603, format!("cannot determine cwd: {e}"))
                        }
                    },
                };

                // Run the resolver.
                let resolved = match cascade_core::resolution::Resolver::new()
                    .resolve(&cwd)
                    .await
                {
                    Ok(r) => r,
                    Err(e) => return Response::err(-32603, format!("resolve failed: {e}")),
                };

                // Audit after successful resolve.
                crate::audit::record(
                    cascade_audit::AuditOp::CascadeResolve,
                    "cascaded",
                    "cascade_resolve",
                    &format!("resolved cwd={}", cwd.display()),
                );

                // Filter to a specific tier if requested.
                let tier_label = params.tier.as_deref().unwrap_or("full").to_string();
                let content = if let Some(tier_slug) = &params.tier {
                    // Return only the content for the requested tier.
                    // CascadeTier uses serde rename_all = "lowercase" and has
                    // an `acronym()` method that returns the lowercase slug.
                    let matched = resolved
                        .tier_sources
                        .iter()
                        .find(|ts| ts.tier.acronym() == tier_slug.as_str());
                    match matched {
                        Some(ts) => ts.content.clone(),
                        None => String::new(),
                    }
                } else {
                    // Full merge across all tiers.
                    resolved.merged_text.clone()
                };

                let result_value = if fmt == "json" {
                    // Serialize the full ResolvedCascade as JSON inside the content field.
                    let json_content = serde_json::to_string(&resolved)
                        .unwrap_or_else(|e| format!("{{\"error\":\"{e}\"}}"));
                    serde_json::json!({
                        "content": json_content,
                        "format": "json",
                        "tier": tier_label,
                    })
                } else {
                    serde_json::json!({
                        "content": content,
                        "format": "markdown",
                        "tier": tier_label,
                    })
                };

                return Response::ok(result_value);
            }

            // ── E-P6-02 T-06: Multi-provider quota IPC methods ────────────
            match typed_req.method.as_str() {
                "update_quota_state" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    // Home dir for config_dir resolution.
                    let home_dir = std::env::var_os("HOME")
                        .map(std::path::PathBuf::from)
                        .or_else(dirs::home_dir)
                        .unwrap_or_else(|| std::path::PathBuf::from("/tmp"));
                    let config_dir = home_dir.join(".cascade");
                    let max_snapshots = 1000;
                    // We don't have DaemonState here in IpcServer; use a stub path.
                    // In a full wire this would pull from server.daemon_state.
                    // For now write directly so the IPC path is exercised.
                    // Full daemon_state wiring deferred to E-P6-03 (supervisor wire).
                    use cascade_types::quota_store::QuotaState as TypedQuotaState;
                    match serde_json::from_value::<TypedQuotaState>(params_val) {
                        Err(e) => return Response::err(-32602, format!("invalid QuotaState: {e}")),
                        Ok(snap) => {
                            // Build a minimal single-snapshot store and write it.
                            let store = cascade_core::quota_aggregator::aggregate_quota(
                                &[snap],
                                max_snapshots,
                            );
                            let path = config_dir.join("quota-store.json");
                            match cascade_core::quota_store::write_quota_store(&path, &store) {
                                Ok(()) => {
                                    return Response::ok(
                                        serde_json::json!({ "status": "updated" }),
                                    );
                                }
                                Err(e) => {
                                    return Response::err(
                                        -32603,
                                        format!("failed to write quota-store: {e:?}"),
                                    );
                                }
                            }
                        }
                    }
                }
                "get_rotation_advice" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    let provider = params_val
                        .get("provider")
                        .and_then(|v| v.as_str())
                        .unwrap_or("")
                        .to_string();
                    if provider.is_empty() {
                        return Response::err(
                            -32602,
                            "missing required param: provider".to_string(),
                        );
                    }
                    // Read the current quota store.
                    let home_dir = std::env::var_os("HOME")
                        .map(std::path::PathBuf::from)
                        .or_else(dirs::home_dir)
                        .unwrap_or_else(|| std::path::PathBuf::from("/tmp"));
                    let path = home_dir.join(".cascade/quota-store.json");
                    match cascade_core::quota_store::read_quota_store(&path) {
                        Err(_) => {
                            // No store yet — return "no accounts" response.
                            return Response::ok(serde_json::json!({
                                "account_id": null,
                                "exhausted": true,
                            }));
                        }
                        Ok(store) => {
                            let advice =
                                crate::ipc_handlers::handle_get_rotation_advice(&provider, &store);
                            return Response::ok(advice);
                        }
                    }
                }
                "budget_check" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    let params: crate::ipc_handlers::BudgetCheckParams =
                        match serde_json::from_value(params_val) {
                            Ok(p) => p,
                            Err(e) => {
                                return Response::err(-32602, format!("invalid params: {e}"));
                            }
                        };
                    // Read quota store.
                    let home_dir = std::env::var_os("HOME")
                        .map(std::path::PathBuf::from)
                        .or_else(dirs::home_dir)
                        .unwrap_or_else(|| std::path::PathBuf::from("/tmp"));
                    let path = home_dir.join(".cascade/quota-store.json");
                    let store =
                        cascade_core::quota_store::read_quota_store(&path).unwrap_or_else(|_| {
                            cascade_core::quota_store::QuotaStore {
                                schema_version:
                                    cascade_core::quota_store::QUOTA_STORE_SCHEMA_VERSION,
                                updated_at: String::new(),
                                accounts: vec![],
                                week_totals: std::collections::HashMap::new(),
                                month_totals: std::collections::HashMap::new(),
                                rolling_history: vec![],
                            }
                        });
                    // Use default BudgetConfig (limits disabled) since we don't have
                    // the config in IpcServer currently. Full config wiring in E-P6-03.
                    let config = crate::config::BudgetConfig::default();
                    let result = crate::ipc_handlers::handle_budget_check(params, &config, &store);
                    return Response::ok(result);
                }
                _ => {}
            }

            // ── update_check / update_apply / update_auto (T-P4-E04-14/16) ──
            // Backs `cascade update check|apply|auto`. Handlers live in
            // updates::ipc_handlers to keep this dispatch slim; see that
            // module for the delta-then-full-bundle apply flow.
            match typed_req.method.as_str() {
                "update_check" => {
                    return match crate::updates::check_for_update().await {
                        Ok(result) => Response::ok(result),
                        Err(e) => Response::err(-32005, format!("update check failed: {e}")),
                    };
                }
                "update_apply" => {
                    let result = crate::updates::apply_update().await;
                    return Response::ok(result);
                }
                "update_auto" => {
                    let params_val = typed_req.params.unwrap_or(serde_json::Value::Null);
                    let params: cascade_types::ipc::UpdateAutoParams =
                        match serde_json::from_value(params_val) {
                            Ok(p) => p,
                            Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
                        };
                    return match crate::updates::set_auto_update(params.enable) {
                        Ok(result) => Response::ok(result),
                        Err(e) => {
                            Response::err(-32603, format!("failed to write config.toml: {e}"))
                        }
                    };
                }
                _ => {}
            }

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
            // ── cascade_providers_* commands (D2 — ipc_providers.rs wired) ───
            // Dispatch all provider management commands to ProviderIpcHandler.
            if typed_req.method.starts_with("cascade_providers_")
                || typed_req.method == "cascade_check_gfp_proxy"
            {
                let handler = Arc::clone(&server.provider_handler);
                let params = typed_req.params.unwrap_or(serde_json::Value::Null);
                return match typed_req.method.as_str() {
                    "cascade_providers_list" => {
                        let items = handler.providers_list().await;
                        Response::ok(items)
                    }
                    "cascade_providers_test" => {
                        let id = params
                            .get("id")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        if id.is_empty() {
                            Response::err(-32602, "missing required param: id")
                        } else {
                            match handler.providers_test(&id).await {
                                Ok(()) => Response::ok(serde_json::json!({ "ok": true })),
                                Err(e) => Response::err(-32001, e),
                            }
                        }
                    }
                    "cascade_providers_remove" => {
                        let id = params
                            .get("id")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        if id.is_empty() {
                            Response::err(-32602, "missing required param: id")
                        } else {
                            match handler.providers_remove(&id).await {
                                Ok(()) => Response::ok(serde_json::json!({ "ok": true })),
                                Err(e) => Response::err(-32001, e),
                            }
                        }
                    }
                    "cascade_providers_add_apikey" => {
                        let id = params
                            .get("id")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        let key = params
                            .get("key")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        if id.is_empty() || key.is_empty() {
                            Response::err(-32602, "missing required params: id, key")
                        } else {
                            match handler.providers_add_apikey(id, key).await {
                                Ok(()) => Response::ok(serde_json::json!({ "ok": true })),
                                Err(e) => Response::err(-32001, e),
                            }
                        }
                    }
                    "cascade_providers_add_generic" => {
                        let base_url = params
                            .get("base_url")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        if base_url.is_empty() {
                            return Response::err(-32602, "missing required param: base_url");
                        }
                        let api_key = params
                            .get("api_key")
                            .and_then(|v| v.as_str())
                            .map(|s| s.to_string());
                        let model_id = params
                            .get("model_id")
                            .and_then(|v| v.as_str())
                            .unwrap_or("unknown")
                            .to_string();
                        let display_name = params
                            .get("display_name")
                            .and_then(|v| v.as_str())
                            .map(|s| s.to_string());
                        match handler
                            .providers_add_generic(base_url, api_key, model_id, display_name)
                            .await
                        {
                            Ok(()) => Response::ok(serde_json::json!({ "ok": true })),
                            Err(e) => Response::err(-32001, e),
                        }
                    }
                    "cascade_providers_usage_today" => {
                        let items = handler.providers_usage_today().await;
                        Response::ok(items)
                    }
                    "cascade_check_gfp_proxy" => {
                        let ok = handler.check_gfp_proxy().await;
                        Response::ok(serde_json::json!({ "running": ok }))
                    }
                    other => Response::err(-32601, format!("method not found: {other}")),
                };
            }

            // ── turn.complete (auto-01) ───────────────────────────────────
            if typed_req.method == "turn.complete" {
                let session_id = typed_req
                    .params
                    .as_ref()
                    .and_then(|p| p.get("session_id"))
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                if let Err(e) = server
                    .bus
                    .publish(
                        "turn.complete",
                        serde_json::json!({ "session_id": session_id }),
                    )
                    .await
                {
                    warn!(%e, "turn.complete: failed to publish event");
                }
                info!(session_id = %session_id, "turn.complete: event published");
                return Response::ok(serde_json::json!({ "ok": true }));
            }

            debug!(method = %typed_req.method, "typed dispatch: METHOD_NOT_FOUND (scaffold)");
            Response::err(-32601, format!("method not found: {}", typed_req.method))
        }
    }
}

// ── Cache IPC (T-P4-E04-11) ───────────────────────────────────────────────────

/// Stats snapshot for a single cache (returned by `cache.stats`).
#[derive(Debug, Serialize)]
pub struct CacheStat {
    pub name: String,
    pub entries: usize,
    pub hits: Option<u64>,
    pub misses: Option<u64>,
}

/// Response body for `cache.stats`.
#[derive(Debug, Serialize)]
pub struct CacheStatsResponse {
    pub caches: Vec<CacheStat>,
}

/// Params for `cache.clear`.
#[derive(Debug, Deserialize)]
pub struct CacheClearParams {
    /// Clear the query LRU cache.
    #[serde(default)]
    pub query: bool,
    /// Clear the embedding cache (in-memory scaffold; disk deletion handled
    /// by the CLI before sending this message).
    #[serde(default)]
    pub embed: bool,
    /// Clear the chunk cache.
    #[serde(default)]
    pub chunk: bool,
    /// Clear all three caches.
    #[serde(default)]
    pub all: bool,
}

/// Dispatch `cache.*` IPC methods.
///
/// Purpose: implement `cascade cache stats` and `cascade cache clear`
///   without touching the frozen legacy Request enum.
/// Constraints: `cache.clear --embed` only clears the in-memory embed cache
///   here; the CLI is responsible for deleting the on-disk embed-cache database
///   before sending this message.
///
/// SPORT: MASTER-ENDPOINTS.md → cache.stats + cache.clear (T-P4-E04-11)
async fn dispatch_cache(
    server: &IpcServer,
    method: &str,
    params: Option<serde_json::Value>,
) -> Response {
    match method {
        "cache.stats" => {
            // QueryCache: uses `stats()` → (hits, misses) and `len()`.
            let (q_hits, q_misses) = server.query_cache.stats();
            let q_entries = server.query_cache.len();

            // EmbedCache: count() gives entries; hits/misses not tracked.
            let e_entries = server.embed_cache.count();

            // File ChunkCache (T-P4-E04-10): mtime-keyed, daemon-level.
            let fc = server.file_chunk_cache.counters();

            let resp = CacheStatsResponse {
                caches: vec![
                    CacheStat {
                        name: "query".into(),
                        entries: q_entries,
                        hits: Some(q_hits),
                        misses: Some(q_misses),
                    },
                    CacheStat {
                        name: "embedding".into(),
                        entries: e_entries,
                        hits: None, // EmbedCache is SQLite-backed; hits/misses not tracked
                        misses: None,
                    },
                    CacheStat {
                        name: "chunk".into(),
                        entries: fc.entries,
                        hits: Some(fc.hits),
                        misses: Some(fc.misses),
                    },
                ],
            };
            Response::ok(resp)
        }
        "cache.clear" => {
            let p: CacheClearParams = match params.map(serde_json::from_value).transpose() {
                Ok(Some(p)) => p,
                Ok(None) => CacheClearParams {
                    query: false,
                    embed: false,
                    chunk: false,
                    all: false,
                },
                Err(e) => return Response::err(-32602, format!("invalid params: {e}")),
            };

            let clear_all = p.all;
            if p.query || clear_all {
                server.query_cache.clear();
            }
            if p.embed || clear_all {
                // In-memory portion; CLI deletes the on-disk .db before sending this.
                server.embed_cache.clear();
            }
            if p.chunk || clear_all {
                server.file_chunk_cache.clear();
            }

            let cleared: Vec<&str> = {
                let mut v = Vec::new();
                if p.query || clear_all {
                    v.push("query");
                }
                if p.embed || clear_all {
                    v.push("embedding");
                }
                if p.chunk || clear_all {
                    v.push("chunk");
                }
                v
            };

            info!(cleared = ?cleared, "cache.clear: caches evicted");
            Response::ok(serde_json::json!({ "cleared": cleared }))
        }
        other => Response::err(-32601, format!("method not found: {other}")),
    }
}

// ── Tests ───────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::Instant;
    use tokio::io::{duplex, AsyncReadExt, AsyncWriteExt};

    async fn test_server(tmp: &tempfile::TempDir) -> Arc<IpcServer> {
        let health = HealthState::new(Instant::now());
        let bus = EventBus::new(tmp.path().to_path_buf()).await.unwrap();
        let registry = Arc::new(cascade_providers::ProviderRegistry::new());

        Arc::new(
            IpcServer::new(tmp.path().to_path_buf(), health, bus, registry)
                .await
                .unwrap(),
        )
    }

    async fn write_json_frame<W: AsyncWriteExt + Unpin>(writer: &mut W, value: serde_json::Value) {
        let bytes = serde_json::to_vec(&value).unwrap();
        writer
            .write_all(&(bytes.len() as u32).to_be_bytes())
            .await
            .unwrap();
        writer.write_all(&bytes).await.unwrap();
    }

    async fn read_json_frame<R: AsyncReadExt + Unpin>(reader: &mut R) -> serde_json::Value {
        let mut len_buf = [0u8; 4];
        reader.read_exact(&mut len_buf).await.unwrap();
        let len = u32::from_be_bytes(len_buf) as usize;
        let mut body = vec![0u8; len];
        reader.read_exact(&mut body).await.unwrap();
        serde_json::from_slice(&body).unwrap()
    }

    #[tokio::test]
    async fn handle_connection_rejects_bad_auth_and_closes() {
        let tmp = tempfile::TempDir::new().unwrap();
        let server = test_server(&tmp).await;
        let (mut client, server_io) = duplex(4096);
        let (server_read, server_write) = tokio::io::split(server_io);

        let task = tokio::spawn(handle_connection(server, server_read, server_write));
        write_json_frame(
            &mut client,
            serde_json::json!({
                "auth": "wrong-token",
                "rpc": {
                    "jsonrpc": "2.0",
                    "id": 7,
                    "method": "ping",
                    "protocol_version": PROTOCOL_VERSION,
                    "params": { "echo": "hi" }
                }
            }),
        )
        .await;

        let response = read_json_frame(&mut client).await;

        assert_eq!(response["id"].as_i64(), Some(7));
        assert_eq!(
            response["error"]["code"].as_i64(),
            Some(typed_ipc::AUTH_FAILED as i64)
        );
        assert_eq!(response["error"]["message"], "auth failed");

        task.await.unwrap().unwrap();
    }

    // Multi-thread flavor required: an accepted request dispatches into the real
    // handler, which reaches the RAG reranker's `block_in_place` — that panics on
    // the single-threaded current_thread runtime.
    #[tokio::test(flavor = "multi_thread")]
    async fn handle_connection_accepts_matching_auth() {
        let tmp = tempfile::TempDir::new().unwrap();
        let server = test_server(&tmp).await;
        let token = server.auth_token.clone();
        let (mut client, server_io) = duplex(4096);
        let (server_read, server_write) = tokio::io::split(server_io);

        let task = tokio::spawn(handle_connection(server, server_read, server_write));
        write_json_frame(
            &mut client,
            serde_json::json!({
                "auth": token,
                "rpc": {
                    "jsonrpc": "2.0",
                    "id": 8,
                    "method": "ping",
                    "protocol_version": PROTOCOL_VERSION,
                    "params": { "echo": "hi" }
                }
            }),
        )
        .await;

        let response = read_json_frame(&mut client).await;
        assert_eq!(response["id"].as_i64(), Some(8));
        assert_eq!(response["result"]["pong"], "hi");

        drop(client);
        task.await.unwrap().unwrap();
    }

    #[test]
    fn token_compare_rejects_prefix_match() {
        assert!(constant_time_token_eq("abcdef", "abcdef"));
        assert!(!constant_time_token_eq("abcdef", "abc"));
        assert!(!constant_time_token_eq("abcdef", "abcdeg"));
    }

    /// D2: providers_list round-trip through the IPC stack.
    ///
    /// Sends a `cascade_providers_list` typed request via the real IPC frame
    /// protocol and asserts the response contains a JSON array (empty when
    /// the registry has no providers registered at startup).
    // Multi-thread flavor: the RAG reranker uses block_in_place which panics
    // on single-thread runtime.
    #[tokio::test(flavor = "multi_thread")]
    async fn providers_list_round_trip_via_ipc() {
        let tmp = tempfile::TempDir::new().unwrap();
        let server = test_server(&tmp).await;
        let token = server.auth_token.clone();
        let (mut client, server_io) = duplex(4096);
        let (server_read, server_write) = tokio::io::split(server_io);

        let task = tokio::spawn(handle_connection(server, server_read, server_write));
        write_json_frame(
            &mut client,
            serde_json::json!({
                "auth": token,
                "rpc": {
                    "jsonrpc": "2.0",
                    "id": 42,
                    "method": "cascade_providers_list",
                    "protocol_version": PROTOCOL_VERSION,
                    "params": {}
                }
            }),
        )
        .await;

        let response = read_json_frame(&mut client).await;
        assert_eq!(response["id"].as_i64(), Some(42), "id echo must match");
        assert!(
            response.get("error").is_none(),
            "providers_list must not return an error; got: {response}"
        );
        // Empty registry → empty array.
        let result = &response["result"];
        assert!(
            result.is_array(),
            "providers_list result must be a JSON array; got: {result}"
        );
        assert_eq!(
            result.as_array().map(|a| a.len()),
            Some(0),
            "empty registry must return empty array via IPC"
        );

        drop(client);
        task.await.unwrap().unwrap();
    }
}
