//! Transport abstraction for MCP 2025-03.
//!
//! A `Transport` is the I/O layer beneath the JSON-RPC protocol. The server
//! calls `recv()` to read one raw JSON-RPC message and `send()` to write one.
//! All framing, buffering, and connection management are transport concerns.
//!
//! ## Available transports
//!
//! | Module | Use case |
//! |--------|----------|
//! | [`stdio`] | Claude Code / OpenCode (default; line-delimited JSON on stdin/stdout) |
//! | [`sse`] | Web clients (HTTP long-poll SSE stream) |
//! | [`http`] | Chunked HTTP streaming for large tool results |
//! | [`unix`] | Same-machine daemon ↔ client (high-throughput IPC) |
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

pub mod http;
pub mod sse;
pub mod stdio;
pub mod unix;

use async_trait::async_trait;
use cascade_types::error::Result;

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
