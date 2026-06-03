//! HTTP streaming transport — chunked HTTP for large tool results.
//!
//! Unlike SSE (which is a persistent long-poll), this transport uses chunked
//! transfer encoding on standard HTTP/1.1 or HTTP/2 POST/GET pairs.
//! Suitable when the client supports streaming but not SSE.
//!
//! ## Usage
//!
//! ```sh
//! cascade mcp serve --transport http --port 3763
//! ```
//!
//! ## Protocol
//!
//! 1. Client POSTs a JSON-RPC request to `/rpc`.
//! 2. Server responds with `Transfer-Encoding: chunked`.
//! 3. Each chunk is one complete JSON-RPC response object.
//! 4. An empty chunk signals the end of streaming.
//!
//! For bidirectional streaming the client opens a persistent connection via
//! HTTP/2 multiplexing; for HTTP/1.1, each request creates a new connection.

use async_trait::async_trait;
use tokio::sync::mpsc;
use tracing::debug;

use cascade_types::error::{CascadeError, Result};

use super::Transport;

/// Configuration for the HTTP streaming transport.
#[derive(Debug, Clone)]
pub struct HttpTransportConfig {
    /// TCP port to bind (default: 3763).
    pub port: u16,
    /// Host to bind (default: 127.0.0.1).
    pub host: String,
    /// Max response chunk size in bytes.
    pub chunk_size: usize,
}

impl Default for HttpTransportConfig {
    fn default() -> Self {
        Self {
            port: 3763,
            host: "127.0.0.1".into(),
            chunk_size: 65_536,
        }
    }
}

/// MCP transport over HTTP chunked streaming.
///
/// Uses mpsc channels internally; the HTTP server wires the channels to the
/// actual TCP connections.
pub struct HttpTransport {
    /// Transport configuration (port, host, chunk size) — used by the HTTP server
    /// binding layer (not yet wired in this stub transport).
    _config: HttpTransportConfig,
    send_tx: mpsc::Sender<String>,
    recv_rx: mpsc::Receiver<String>,
}

impl HttpTransport {
    /// Create an HTTP transport.
    ///
    /// Returns the transport, the outbound channel receiver (consumed by the
    /// chunked response writer), and the inbound channel sender (called by
    /// the POST request handler).
    pub fn new(
        config: HttpTransportConfig,
    ) -> (Self, mpsc::Receiver<String>, mpsc::Sender<String>) {
        let (send_tx, send_rx) = mpsc::channel(128);
        let (recv_tx, recv_rx) = mpsc::channel(128);
        let transport = Self {
            _config: config,
            send_tx,
            recv_rx,
        };
        (transport, send_rx, recv_tx)
    }
}

#[async_trait]
impl Transport for HttpTransport {
    async fn send(&mut self, message: &str) -> Result<()> {
        debug!(bytes = message.len(), "http-stream send");
        self.send_tx
            .send(message.to_owned())
            .await
            .map_err(|_| CascadeError::Io {
                path: "<http-channel>".into(),
                operation: "send",
                source: std::io::Error::new(
                    std::io::ErrorKind::BrokenPipe,
                    "HTTP send channel closed",
                ),
            })
    }

    async fn recv(&mut self) -> Result<Option<String>> {
        Ok(self.recv_rx.recv().await)
    }

    async fn close(&mut self) -> Result<()> {
        Ok(())
    }

    fn name(&self) -> &str {
        "http-stream"
    }
}
