//! Unix socket transport — high-throughput IPC for same-machine clients.
//!
//! Preferred when the MCP client runs on the same machine as the Cascade
//! daemon (e.g. the `cascade` Python SDK, `cascade` CLI sub-commands).
//! Avoids TCP stack overhead; latency is typically <1ms.
//!
//! ## Socket path
//!
//! Default: `~/.cascade/mcp.sock`
//!
//! The daemon removes the socket on clean shutdown. On crash recovery the
//! socket may linger; `UnixTransport::listen` removes any stale socket
//! before binding.
//!
//! ## Usage
//!
//! ```sh
//! cascade mcp serve --transport unix  # uses ~/.cascade/mcp.sock
//! cascade mcp serve --transport unix --socket /tmp/my.sock
//! ```
//!
//! ## Framing
//!
//! Same as stdio: one JSON-RPC object per line (`\n`-delimited UTF-8).

use std::path::{Path, PathBuf};

use async_trait::async_trait;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tracing::{debug, info};

use cascade_types::error::{CascadeError, Result};

use super::Transport;

/// MCP transport over a Unix domain socket.
///
/// Created by calling [`UnixTransport::connect`] (client side) or
/// [`UnixTransport::accept`] (server side — accepts exactly one connection).
pub struct UnixTransport {
    socket_path: PathBuf,
    reader: BufReader<tokio::io::ReadHalf<UnixStream>>,
    writer: tokio::io::WriteHalf<UnixStream>,
}

impl UnixTransport {
    /// Connect to an existing socket as a client.
    ///
    /// Used by the Python SDK and CLI sub-commands to reach a running daemon.
    pub async fn connect(socket_path: impl AsRef<Path>) -> Result<Self> {
        let path = socket_path.as_ref().to_path_buf();
        let stream = UnixStream::connect(&path)
            .await
            .map_err(|e| CascadeError::Io {
                path: path.clone(),
                operation: "unix_connect",
                source: e,
            })?;
        Ok(Self::from_stream(path, stream))
    }

    /// Bind a socket and accept exactly one incoming connection.
    ///
    /// Any stale socket at `socket_path` is removed before binding.
    /// Designed for the daemon's startup sequence where it listens then
    /// hands the accepted stream to the server loop.
    pub async fn accept(socket_path: impl AsRef<Path>) -> Result<Self> {
        let path = socket_path.as_ref().to_path_buf();

        // Remove stale socket from a prior crash.
        if path.exists() {
            std::fs::remove_file(&path).map_err(|e| CascadeError::Io {
                path: path.clone(),
                operation: "remove_stale_socket",
                source: e,
            })?;
        }

        let listener = UnixListener::bind(&path).map_err(|e| CascadeError::Io {
            path: path.clone(),
            operation: "unix_bind",
            source: e,
        })?;

        info!(socket = %path.display(), "Unix socket listening");

        let (stream, _addr) = listener.accept().await.map_err(|e| CascadeError::Io {
            path: path.clone(),
            operation: "unix_accept",
            source: e,
        })?;

        Ok(Self::from_stream(path, stream))
    }

    fn from_stream(socket_path: PathBuf, stream: UnixStream) -> Self {
        let (read_half, write_half) = tokio::io::split(stream);
        Self {
            socket_path,
            reader: BufReader::new(read_half),
            writer: write_half,
        }
    }
}

#[async_trait]
impl Transport for UnixTransport {
    async fn send(&mut self, message: &str) -> Result<()> {
        debug!(bytes = message.len(), "unix send");
        self.writer
            .write_all(message.as_bytes())
            .await
            .map_err(|e| CascadeError::Io {
                path: self.socket_path.clone(),
                operation: "unix_write",
                source: e,
            })?;
        self.writer
            .write_all(b"\n")
            .await
            .map_err(|e| CascadeError::Io {
                path: self.socket_path.clone(),
                operation: "unix_write_newline",
                source: e,
            })?;
        self.writer.flush().await.map_err(|e| CascadeError::Io {
            path: self.socket_path.clone(),
            operation: "unix_flush",
            source: e,
        })
    }

    async fn recv(&mut self) -> Result<Option<String>> {
        loop {
            let mut line = String::new();
            let n = self
                .reader
                .read_line(&mut line)
                .await
                .map_err(|e| CascadeError::Io {
                    path: self.socket_path.clone(),
                    operation: "unix_read_line",
                    source: e,
                })?;

            if n == 0 {
                return Ok(None);
            }

            let trimmed = line.trim_end_matches('\n').trim_end_matches('\r');
            if trimmed.is_empty() {
                continue;
            }

            debug!(bytes = trimmed.len(), "unix recv");
            return Ok(Some(trimmed.to_owned()));
        }
    }

    async fn close(&mut self) -> Result<()> {
        self.writer.flush().await.map_err(|e| CascadeError::Io {
            path: self.socket_path.clone(),
            operation: "unix_close_flush",
            source: e,
        })
    }

    fn name(&self) -> &str {
        "unix"
    }
}
