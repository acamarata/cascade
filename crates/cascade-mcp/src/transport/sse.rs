//! SSE transport — HTTP Server-Sent Events for web MCP clients.
//!
//! This transport serves MCP over a long-lived HTTP connection using the
//! SSE protocol. Clients connect to a GET endpoint; the server pushes events
//! via `data: {...}\n\n` frames. Client-to-server messages are sent via a
//! separate POST endpoint.
//!
//! ## Endpoints
//!
//! | Method | Path | Direction |
//! |--------|------|-----------|
//! | GET | `/sse` | Server → Client (SSE stream) |
//! | POST | `/message` | Client → Server |
//!
//! ## Usage
//!
//! ```sh
//! cascade mcp serve --transport sse --port 3762
//! ```
//!
//! ## Design note
//!
//! SSE is half-duplex from the TCP perspective: the server pushes over an
//! open GET connection while the client sends via separate POSTs. This
//! transport pairs the two channels via a shared in-process `mpsc` channel.

use async_trait::async_trait;
use tokio::sync::mpsc;
use tracing::debug;

use cascade_types::error::{CascadeError, Result};

use super::Transport;

/// Configuration for the SSE transport.
#[derive(Debug, Clone)]
pub struct SseConfig {
    /// TCP port to bind (default: 3762).
    pub port: u16,
    /// Host to bind (default: 127.0.0.1).
    pub host: String,
    /// Maximum event queue depth before back-pressure.
    pub queue_depth: usize,
}

impl Default for SseConfig {
    fn default() -> Self {
        Self {
            port: 3762,
            host: "127.0.0.1".into(),
            queue_depth: 256,
        }
    }
}

/// MCP transport over HTTP SSE.
///
/// The SSE stream is opened at `/sse`; client POSTs arrive at `/message`.
/// Internally, `recv_rx` receives messages forwarded from the POST handler,
/// and `send_tx` is consumed by the SSE push loop.
pub struct SseTransport {
    /// SSE configuration (port, host, queue depth).
    /// Retained for the HTTP server binding layer; not read after construction.
    _config: SseConfig,
    /// Outbound messages queued for SSE push (server → client).
    send_tx: mpsc::Sender<String>,
    /// Inbound messages from client POSTs (client → server).
    recv_rx: mpsc::Receiver<String>,
}

impl SseTransport {
    /// Create an SSE transport.
    ///
    /// The caller is responsible for wiring `send_rx` (the SSE event loop)
    /// and `recv_tx` (the POST endpoint handler) into an HTTP server. This
    /// constructor returns the transport side; the HTTP plumbing runs
    /// alongside via `SseTransport::start_http`.
    pub fn new(config: SseConfig) -> (Self, mpsc::Receiver<String>, mpsc::Sender<String>) {
        let (send_tx, send_rx) = mpsc::channel(config.queue_depth);
        let (recv_tx, recv_rx) = mpsc::channel(config.queue_depth);
        let transport = Self {
            _config: config,
            send_tx,
            recv_rx,
        };
        (transport, send_rx, recv_tx)
    }
}

#[async_trait]
impl Transport for SseTransport {
    /// Queue a JSON-RPC message for SSE delivery.
    ///
    /// The actual `data: ...\n\n` framing is applied by the HTTP handler
    /// that drains `send_rx`.
    async fn send(&mut self, message: &str) -> Result<()> {
        debug!(bytes = message.len(), "sse send");
        self.send_tx
            .send(message.to_owned())
            .await
            .map_err(|_| CascadeError::Io {
                path: "<sse-channel>".into(),
                operation: "send",
                source: std::io::Error::new(
                    std::io::ErrorKind::BrokenPipe,
                    "SSE send channel closed",
                ),
            })
    }

    /// Receive the next inbound message from a client POST.
    ///
    /// Returns `None` when the POST handler is dropped (client disconnected).
    async fn recv(&mut self) -> Result<Option<String>> {
        Ok(self.recv_rx.recv().await)
    }

    async fn close(&mut self) -> Result<()> {
        // Dropping send_tx signals the SSE loop to close.
        // recv_rx will drain naturally.
        Ok(())
    }

    fn name(&self) -> &str {
        "sse"
    }
}
