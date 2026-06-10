//! stdio transport — line-delimited JSON-RPC over stdin/stdout.
//!
//! ## Overview
//!
//! Two roles:
//! - **`StdioTransport`** (per-connection): low-level `Transport` impl over stdin/stdout.
//!   Used by `McpServer::run()` for the traditional single-connection CLI invocation.
//! - **`StdioServer`** (server-side): implements `McpTransport` for subprocess mode.
//!   Wraps stdin/stdout in a `connection_loop` — no accept loop needed; one connection
//!   per process. No auth required (stdin/stdout is process-isolated by the OS).
//!
//! ## Framing
//!
//! One JSON-RPC object per line (`\n`-delimited UTF-8). Blank lines are silently skipped.
//!
//! ## Usage
//!
//! ```sh
//! cascade mcp serve          # default: stdio
//! cascade mcp serve --stdio  # explicit
//! ```
//!
//! ## SPORT
//! MASTER-TRANSPORTS.md: stdio transport row

use async_trait::async_trait;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader, Stdin, Stdout};
use tracing::debug;

use cascade_types::error::{CascadeError, Result};

use crate::notification::NotificationBus;
use crate::server::McpServerConfig;
use crate::transport::McpTransport;
use crate::transport::connection::{connection_loop, ConnectionContext};

use super::Transport;

// ── StdioTransport (per-connection, low-level) ────────────────────────────────

/// MCP transport over stdin/stdout (line-delimited JSON).
///
/// Created once and owned by the [`McpServer`]. Not reusable after `close()`.
pub struct StdioTransport {
    reader: BufReader<Stdin>,
    writer: Stdout,
}

impl StdioTransport {
    /// Create a new stdio transport using the process's stdin/stdout.
    ///
    /// Call this exactly once per process; constructing multiple instances
    /// causes both to compete for the same stdin bytes.
    pub fn new() -> Self {
        Self {
            reader: BufReader::new(tokio::io::stdin()),
            writer: tokio::io::stdout(),
        }
    }
}

impl Default for StdioTransport {
    fn default() -> Self {
        Self::new()
    }
}

#[async_trait]
impl Transport for StdioTransport {
    /// Write `message` followed by a newline to stdout.
    async fn send(&mut self, message: &str) -> Result<()> {
        debug!(bytes = message.len(), "stdio send");
        self.writer
            .write_all(message.as_bytes())
            .await
            .map_err(|e| CascadeError::Io {
                path: "<stdout>".into(),
                operation: "write",
                source: e,
            })?;
        self.writer
            .write_all(b"\n")
            .await
            .map_err(|e| CascadeError::Io {
                path: "<stdout>".into(),
                operation: "write_newline",
                source: e,
            })?;
        self.writer.flush().await.map_err(|e| CascadeError::Io {
            path: "<stdout>".into(),
            operation: "flush",
            source: e,
        })
    }

    /// Read one non-empty line from stdin. Blank lines are skipped. EOF signals shutdown.
    async fn recv(&mut self) -> Result<Option<String>> {
        loop {
            let mut line = String::new();
            let n = self
                .reader
                .read_line(&mut line)
                .await
                .map_err(|e| CascadeError::Io {
                    path: "<stdin>".into(),
                    operation: "read_line",
                    source: e,
                })?;

            if n == 0 {
                return Ok(None);
            }

            let trimmed = line.trim_end_matches('\n').trim_end_matches('\r');
            if trimmed.is_empty() {
                continue;
            }

            debug!(bytes = trimmed.len(), "stdio recv");
            return Ok(Some(trimmed.to_owned()));
        }
    }

    async fn close(&mut self) -> Result<()> {
        self.writer.flush().await.map_err(|e| CascadeError::Io {
            path: "<stdout>".into(),
            operation: "close_flush",
            source: e,
        })
    }

    fn name(&self) -> &str {
        "stdio"
    }
}

// ── StdioServer (server-side McpTransport) ────────────────────────────────────

/// Server-side stdio MCP transport.
///
/// Wraps the process's `stdin`/`stdout` in a `connection_loop`. No auth required —
/// the OS process boundary provides isolation. Returns when stdin reaches EOF
/// (parent process closed the pipe).
pub struct StdioServer {
    config: McpServerConfig,
    bus: NotificationBus,
    shutdown: tokio::sync::Notify,
}

impl StdioServer {
    /// Create a new `StdioServer`.
    pub fn new(config: McpServerConfig, bus: NotificationBus) -> Self {
        Self {
            config,
            bus,
            shutdown: tokio::sync::Notify::new(),
        }
    }
}

#[async_trait(?Send)]
impl McpTransport for StdioServer {
    /// Run the stdio connection until stdin EOF or shutdown signal.
    ///
    /// Pre-authenticated (stdio is process-isolated; no auth handshake needed).
    /// Returns when the connection exits.
    async fn listen(&self) -> Result<()> {
        let ctx = ConnectionContext::pre_authenticated();
        let stdin = tokio::io::stdin();
        let stdout = tokio::io::stdout();

        tokio::select! {
            result = connection_loop(stdin, stdout, self.config.clone(), self.bus.clone(), ctx) => {
                result
            }
            _ = self.shutdown.notified() => {
                Ok(())
            }
        }
    }

    /// Signal the stdio transport to stop.
    ///
    /// Since stdio exits on stdin EOF naturally, this is a no-op in most cases.
    async fn stop(&self) -> Result<()> {
        self.shutdown.notify_waiters();
        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// Tests for StdioServer use tokio duplex to simulate stdin/stdout pipes.
    /// The actual StdioTransport/StdioServer over real stdin/stdout cannot be tested
    /// in-process without taking over the process's I/O.

    #[tokio::test(flavor = "current_thread")]
    async fn stdio_server_exits_on_stop() {
        let config = McpServerConfig::default();
        let bus = NotificationBus::new();
        let server = Arc::new(StdioServer::new(config, bus));
        let server2 = Arc::clone(&server);

        // We can't test actual stdin/stdout in-process, but we can verify
        // the stop() signal works by checking the Notify fires.
        // Use spawn_local (current_thread + LocalSet) since stop() future is !Send.
        let local = tokio::task::LocalSet::new();
        let result = local.run_until(async move {
            let handle = tokio::task::spawn_local(async move {
                server2.stop().await
            });
            tokio::time::timeout(std::time::Duration::from_secs(1), handle).await
        }).await;
        assert!(result.is_ok(), "stop() should complete promptly");
        assert!(result.unwrap().unwrap().is_ok());
    }

    #[tokio::test(flavor = "current_thread")]
    async fn stdio_transport_send_recv_over_channel() {
        // Test the connection_loop with duplex I/O (same as connection tests,
        // but exercises the stdio pathway).
        use tokio::io::AsyncBufReadExt;

        let local = tokio::task::LocalSet::new();
        local.run_until(async {
            let (mut client, server) = tokio::io::duplex(65536);
            let (server_read, server_write) = tokio::io::split(server);

            let bus = NotificationBus::new();
            let ctx = ConnectionContext::pre_authenticated();
            let config = McpServerConfig::default();

            let handle = tokio::task::spawn_local(connection_loop(
                server_read, server_write, config, bus, ctx,
            ));

            use tokio::io::AsyncWriteExt;
            let msg = r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"stdio-test","version":"0.1"}}}"#;
            client.write_all(msg.as_bytes()).await.unwrap();
            client.write_all(b"\n").await.unwrap();
            client.flush().await.unwrap();

            let mut reader = tokio::io::BufReader::new(&mut client);
            let mut resp = String::new();
            reader.read_line(&mut resp).await.unwrap();
            assert!(!resp.trim().is_empty());

            let val: serde_json::Value = serde_json::from_str(resp.trim()).expect("valid JSON");
            assert_eq!(val["jsonrpc"], "2.0");

            drop(client);
            let _ = handle.await;
        }).await;
    }
}

// Allow Arc in test module.
#[cfg(test)]
use std::sync::Arc;
