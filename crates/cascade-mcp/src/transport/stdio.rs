//! stdio transport — line-delimited JSON-RPC over stdin/stdout.
//!
//! This is the default MCP transport, compatible with Claude Code and OpenCode.
//! Each message is a single JSON object terminated by a newline (`\n`).
//! The server reads from stdin and writes to stdout; stderr is left free for
//! diagnostic logging.
//!
//! ## Framing
//!
//! - **Server ← Client:** one JSON-RPC object per line on stdin.
//! - **Server → Client:** one JSON-RPC object per line on stdout.
//! - Blank lines are silently skipped on recv.
//!
//! ## Usage
//!
//! This transport is selected when `cascade` is launched via the `mcp` CLI
//! subcommand without `--transport` flags:
//!
//! ```sh
//! cascade mcp serve  # defaults to stdio
//! ```

use async_trait::async_trait;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader, Stdin, Stdout};
use tracing::debug;

use cascade_types::error::{CascadeError, Result};

use super::Transport;

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
    ///
    /// stdout is line-buffered by most shells; flushing after every write
    /// ensures the client sees the response immediately.
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

    /// Read one non-empty line from stdin.
    ///
    /// Blank lines are skipped. EOF (empty read) signals clean shutdown.
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
                // EOF — clean shutdown
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
