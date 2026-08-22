//! Transport abstraction for MCP 2025-03.
//!
//! Two layers:
//!
//! ## 1. `Transport` — per-connection message I/O (P2)
//!
//! Low-level framed I/O: `send` / `recv` / `close` one JSON-RPC message at a time.
//! Used by `McpServer::run()` which owns a single `Box<dyn Transport>` per connection.
//!
//! ## 2. `McpTransport` — server-side listener (P4, T-P4-E02-13)
//!
//! Higher-level trait that manages the accept loop, auth handshake, connection
//! limiting, and graceful shutdown. Each accepted connection spawns a `connection_loop`.
//!
//! ## Available transports
//!
//! | Module | Use case |
//! |--------|----------|
//! | [`stdio`] | Claude Code / OpenCode — line-delimited JSON on stdin/stdout |
//! | [`unix`] | Same-machine daemon ↔ client (high-throughput IPC, 0600 socket) |
//! | [`http`] | HTTP POST /mcp with Bearer auth (loopback only, port 7722) |
//! | [`sse`] | GET /mcp/events SSE stream for notifications |
//! | [`tcp`] | TCP loopback 127.0.0.1:7723 with Bearer auth handshake |
//!
//! ## Implementing a custom transport
//!
//! ```rust,no_run
//! use async_trait::async_trait;
//! use cascade_mcp::transport::Transport;
//! use cascade_types::error::Result;
//!
//! struct MyTransport;
//!
//! #[async_trait]
//! impl Transport for MyTransport {
//!     async fn send(&mut self, message: &str) -> Result<()> { Ok(()) }
//!     async fn recv(&mut self) -> Result<Option<String>> { Ok(None) }
//!     async fn close(&mut self) -> Result<()> { Ok(()) }
//!     fn name(&self) -> &str { "my-transport" }
//! }
//! ```

pub mod connection;
pub mod http;
pub mod sse;
pub mod stdio;
pub mod tcp;

/// Unix-domain-socket transport.
///
/// `#[cfg(unix)]` because it is built on `tokio::net::{UnixListener,
/// UnixStream}`, which do not exist on Windows. The module was declared
/// unconditionally, so cascade-mcp simply did not compile for
/// x86_64-pc-windows-msvc — invisible until hosted CI was switched on. The
/// Windows equivalent is a named pipe (T-P7-E25-10).
#[cfg(unix)]
pub mod unix;

use async_trait::async_trait;
use cascade_types::error::Result;

// ── Transport (per-connection I/O, P2) ───────────────────────────────────────

/// Low-level message framing for MCP.
///
/// Each `send`/`recv` call handles exactly one complete JSON-RPC message
/// (as a UTF-8 string). Framing (newline delimiting, HTTP chunking, etc.)
/// is fully encapsulated by the implementing type.
///
/// # Send + Sync
///
/// Implementations must be `Send` so the server can hold the transport
/// inside a `tokio::spawn`-ed task.
#[async_trait]
pub trait Transport: Send {
    /// Write one JSON-RPC message to the transport.
    ///
    /// The `message` string is a complete, valid JSON object. The transport
    /// is responsible for adding any required framing (newline, length prefix,
    /// SSE `data:` prefix, etc.).
    async fn send(&mut self, message: &str) -> Result<()>;

    /// Read one JSON-RPC message from the transport.
    ///
    /// Returns:
    /// - `Ok(Some(msg))` — a complete message is available.
    /// - `Ok(None)` — the connection was cleanly closed by the peer.
    /// - `Err(e)` — an I/O error occurred; the server should stop.
    async fn recv(&mut self) -> Result<Option<String>>;

    /// Close the transport gracefully.
    ///
    /// After this call, `send` and `recv` may return errors. The server calls
    /// this on shutdown.
    async fn close(&mut self) -> Result<()>;

    /// Human-readable name for logging.
    fn name(&self) -> &str;
}

// ── McpTransport (server-side listener, P4) ───────────────────────────────────

/// Server-side listener trait for MCP transports (T-P4-E02-13).
///
/// An `McpTransport` manages the accept loop for one transport type: it binds
/// to the appropriate endpoint (socket, port, stdio), accepts connections,
/// performs the auth handshake, and spawns a [`connection::connection_loop`]
/// per accepted connection.
///
/// ## `!Send` note
///
/// `listen()` returns a `!Send` future because `connection_loop` contains a
/// `McpServer` which holds `Box<dyn Transport>` (Send but not Sync). Callers
/// must run `listen()` inside a `tokio::task::LocalSet`.
///
/// ## Implementations
///
/// - [`unix::UnixServer`] — Unix domain socket, 0600, token auth, 64 connections.
/// - [`stdio::StdioServer`] — stdin/stdout, no auth, one connection per process.
/// - [`http::HttpServer`] — axum POST /mcp, Bearer auth, loopback 127.0.0.1:7722.
/// - [`sse::SseServer`] — axum GET /mcp/events SSE, Bearer auth, same port.
/// - [`tcp::TcpServer`] — TCP 127.0.0.1:7723, token auth, 32 connections.
#[async_trait(?Send)]
pub trait McpTransport {
    /// Start the listener loop.
    ///
    /// Runs until a shutdown signal is received or an unrecoverable error occurs.
    /// Must be called within a `tokio::task::LocalSet`.
    async fn listen(&self) -> Result<()>;

    /// Signal the listener to stop and wait for in-flight connections to drain.
    async fn stop(&self) -> Result<()>;
}

// ── Auth handshake helpers (shared by unix.rs and tcp.rs) ────────────────────

pub(crate) mod auth_handshake {
    //! Shared auth handshake for socket-based transports (Unix + TCP).
    //!
    //! The first JSON-RPC message on each connection must be `method: "auth"`
    //! with `params: { "token": "cascade-mcp-..." }`. Any other first message
    //! causes the connection to be closed with an error response.

    use std::sync::Arc;
    use tokio::io::{AsyncBufReadExt, AsyncWrite, AsyncWriteExt, BufReader};
    use tracing::{info, warn};

    use crate::auth::McpAuth;

    /// Result of the auth handshake.
    pub enum AuthResult {
        /// Handshake succeeded; connection may proceed.
        Ok,
        /// Handshake failed; connection was already closed by the callee.
        Failed,
    }

    /// Perform the first-message auth handshake on a socket connection.
    ///
    /// Reads the first line from `reader`, validates it as an "auth" method with
    /// a valid token, and writes an ok or error response to `writer`.
    ///
    /// Returns `AuthResult::Ok` on success; `AuthResult::Failed` if the connection
    /// should be dropped (error response already written).
    pub async fn perform<R, W>(
        reader: &mut BufReader<R>,
        writer: &mut W,
        auth: &Arc<McpAuth>,
        conn_id: uuid::Uuid,
    ) -> AuthResult
    where
        R: tokio::io::AsyncRead + Unpin,
        W: AsyncWrite + Unpin,
    {
        let mut line = String::new();

        // 10-second timeout for the auth handshake.
        let read_result = tokio::time::timeout(
            std::time::Duration::from_secs(10),
            reader.read_line(&mut line),
        )
        .await;

        let n = match read_result {
            Ok(Ok(n)) => n,
            Ok(Err(e)) => {
                warn!(conn_id = %conn_id, error = %e, "auth read error");
                return AuthResult::Failed;
            }
            Err(_) => {
                warn!(conn_id = %conn_id, "auth handshake timed out");
                let _ = write_auth_error(writer, "auth timeout").await;
                return AuthResult::Failed;
            }
        };

        if n == 0 {
            // EOF before auth.
            return AuthResult::Failed;
        }

        // Parse the JSON.
        let trimmed = line.trim();
        let Ok(val) = serde_json::from_str::<serde_json::Value>(trimmed) else {
            warn!(conn_id = %conn_id, "auth message is not valid JSON");
            let _ = write_auth_error(writer, "invalid JSON").await;
            return AuthResult::Failed;
        };

        // Must be method "auth".
        if val.get("method").and_then(|m| m.as_str()) != Some("auth") {
            warn!(conn_id = %conn_id, "first message is not auth method");
            let _ = write_auth_error(writer, "first message must be auth").await;
            return AuthResult::Failed;
        }

        // Extract token.
        let token = val
            .get("params")
            .and_then(|p| p.get("token"))
            .and_then(|t| t.as_str())
            .unwrap_or("");

        if auth.validate_token(token).is_err() {
            warn!(conn_id = %conn_id, "auth token rejected");
            let _ = write_auth_error(writer, "invalid token").await;
            return AuthResult::Failed;
        }

        // Success.
        info!(conn_id = %conn_id, "auth handshake succeeded");
        let ok = r#"{"jsonrpc":"2.0","id":null,"result":{"ok":true}}"#;
        let _ = write_line(writer, ok).await;
        AuthResult::Ok
    }

    async fn write_auth_error<W: AsyncWrite + Unpin>(
        writer: &mut W,
        msg: &str,
    ) -> std::io::Result<()> {
        let resp = format!(
            r#"{{"jsonrpc":"2.0","id":null,"error":{{"code":-32003,"message":"auth failed: {}"}}}}"#,
            msg
        );
        write_line(writer, &resp).await
    }

    async fn write_line<W: AsyncWrite + Unpin>(writer: &mut W, msg: &str) -> std::io::Result<()> {
        writer.write_all(msg.as_bytes()).await?;
        writer.write_all(b"\n").await?;
        writer.flush().await
    }
}
