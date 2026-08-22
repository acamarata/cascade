//! Unix domain socket transport — high-throughput IPC for same-machine clients.
//!
//! ## Overview
//!
//! Two roles:
//! - **Client side** (`UnixTransport`): connects to an existing socket; used by the
//!   Python SDK and `cascade` CLI sub-commands to reach a running daemon.
//! - **Server side** (`UnixServer`): binds a socket, accepts connections, performs auth
//!   handshake, enforces connection limits, and runs `connection_loop` per connection.
//!
//! ## Socket path
//!
//! Default: `~/.cascade/runtime/mcp.sock`
//! Permissions: 0600 (owner only).
//!
//! ## Auth
//!
//! First message must be `method: "auth"` with `params: { "token": "cascade-mcp-..." }`.
//! Token validated by `McpAuth::validate_token`. Invalid token closes the connection.
//!
//! ## Framing
//!
//! Newline-delimited JSON-RPC (one object per line).
//!
//! ## SPORT
//! MASTER-TRANSPORTS.md: Unix socket transport row

use std::path::{Path, PathBuf};
use std::sync::Arc;

use async_trait::async_trait;
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::{UnixListener, UnixStream};
use tokio::sync::Semaphore;
use tracing::{debug, error, info, warn};

use cascade_types::error::{CascadeError, Result};

use crate::auth::McpAuth;
use crate::notification::NotificationBus;
use crate::server::McpServerConfig;
use crate::transport::auth_handshake::{perform as auth_perform, AuthResult};
use crate::transport::connection::{connection_loop, ConnectionContext};
use crate::transport::McpTransport;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Default socket path: `~/.cascade/runtime/mcp.sock`.
pub fn default_socket_path() -> PathBuf {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|_| PathBuf::from("/tmp"));
    home.join(".cascade").join("runtime").join("mcp.sock")
}

/// Maximum concurrent connections on the Unix socket.
const MAX_CONNECTIONS: usize = 64;

// ── UnixTransport (client side) ───────────────────────────────────────────────

/// MCP transport over a Unix domain socket — client side.
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
    pub async fn accept(socket_path: impl AsRef<Path>) -> Result<Self> {
        let path = socket_path.as_ref().to_path_buf();

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
impl super::Transport for UnixTransport {
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

// ── UnixServer (server side) ──────────────────────────────────────────────────

/// Server-side Unix socket MCP transport.
///
/// Binds `~/.cascade/runtime/mcp.sock` (or custom path), enforces 0600
/// permissions, limits to 64 concurrent connections, and performs the
/// `auth` handshake before starting `connection_loop`.
pub struct UnixServer {
    socket_path: PathBuf,
    auth: Arc<McpAuth>,
    config: McpServerConfig,
    bus: NotificationBus,
    shutdown: tokio::sync::Notify,
}

impl UnixServer {
    /// Create a new `UnixServer`.
    ///
    /// - `socket_path`: override the socket path (default: `~/.cascade/runtime/mcp.sock`).
    /// - `auth`: shared auth instance.
    /// - `config`: server config passed to each `McpServer` per connection.
    /// - `bus`: notification bus — each connection subscribes.
    pub fn new(
        socket_path: Option<PathBuf>,
        auth: Arc<McpAuth>,
        config: McpServerConfig,
        bus: NotificationBus,
    ) -> Self {
        Self {
            socket_path: socket_path.unwrap_or_else(default_socket_path),
            auth,
            config,
            bus,
            shutdown: tokio::sync::Notify::new(),
        }
    }
}

#[async_trait(?Send)]
impl McpTransport for UnixServer {
    async fn listen(&self) -> Result<()> {
        let path = &self.socket_path;

        // Ensure parent directory exists.
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
                path: parent.to_path_buf(),
                operation: "create_socket_dir",
                source: e,
            })?;
        }

        // Remove stale socket from prior crash.
        if path.exists() {
            std::fs::remove_file(path).map_err(|e| CascadeError::Io {
                path: path.clone(),
                operation: "remove_stale_socket",
                source: e,
            })?;
        }

        let listener = UnixListener::bind(path).map_err(|e| CascadeError::Io {
            path: path.clone(),
            operation: "unix_bind",
            source: e,
        })?;

        // Set 0600 permissions — only owner can connect.
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600)).map_err(
                |e| CascadeError::Io {
                    path: path.clone(),
                    operation: "set_socket_permissions",
                    source: e,
                },
            )?;
        }

        info!(socket = %path.display(), "Unix MCP server listening (max {} conns)", MAX_CONNECTIONS);

        let semaphore = Arc::new(Semaphore::new(MAX_CONNECTIONS));
        let shutdown = &self.shutdown;

        loop {
            tokio::select! {
                _ = shutdown.notified() => {
                    info!(socket = %path.display(), "Unix MCP server shutting down");
                    break;
                }
                accept_result = listener.accept() => {
                    match accept_result {
                        Err(e) => {
                            error!(error = %e, "accept error");
                            continue;
                        }
                        Ok((stream, _)) => {
                            // Check connection limit.
                            let permit = match semaphore.clone().try_acquire_owned() {
                                Ok(p) => p,
                                Err(_) => {
                                    warn!("Unix connection limit ({}) reached — rejecting", MAX_CONNECTIONS);
                                    let (read, mut write) = tokio::io::split(stream);
                                    drop(read);
                                    let _ = write.write_all(
                                        b"{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32000,\"message\":\"connection limit reached\"}}\n"
                                    ).await;
                                    continue;
                                }
                            };

                            let auth = Arc::clone(&self.auth);
                            let config = self.config.clone();
                            let bus = self.bus.clone();
                            let socket_path = path.clone();

                            // spawn_local: connection_loop is !Send (McpServer contains Box<dyn Transport>
                            // which is Send but not Sync). Callers must run listen() inside a LocalSet.
                            tokio::task::spawn_local(async move {
                                let _permit = permit; // released on drop
                                let conn_id = uuid::Uuid::new_v4();
                                let (read, write) = tokio::io::split(stream);
                                let mut reader = BufReader::new(read);
                                let mut writer = write;

                                // Auth handshake.
                                match auth_perform(&mut reader, &mut writer, &auth, conn_id).await {
                                    AuthResult::Failed => {
                                        debug!(conn_id = %conn_id, socket = %socket_path.display(), "auth failed — connection closed");
                                        return;
                                    }
                                    AuthResult::Ok => {}
                                }

                                let ctx = ConnectionContext::pre_authenticated();
                                let raw_read = reader.into_inner();

                                if let Err(e) = connection_loop(raw_read, writer, config, bus, ctx).await {
                                    error!(conn_id = %conn_id, error = %e, "connection_loop error");
                                }
                            });
                        }
                    }
                }
            }
        }

        // Cleanup socket file.
        if path.exists() {
            let _ = std::fs::remove_file(path);
        }

        Ok(())
    }

    async fn stop(&self) -> Result<()> {
        self.shutdown.notify_waiters();
        // Sticky permit: notify_waiters only wakes CURRENT waiters; if stop()
        // fires while the accept loop is mid-iteration the wakeup is lost and
        // shutdown hangs (observed). notify_one stores a permit consumed by
        // the next notified() poll.
        self.shutdown.notify_one();
        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
#[cfg(unix)]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;
    use tempfile::TempDir;
    use tokio::io::{AsyncBufReadExt, AsyncWriteExt};

    /// Wrap test body in a LocalSet so spawn_local works for non-Send futures.
    macro_rules! local_test {
        (async $body:block) => {{
            let local = tokio::task::LocalSet::new();
            local.run_until(async $body).await;
        }};
    }

    fn make_auth() -> McpAuth {
        McpAuth::from_secret([42u8; 32])
    }

    // Unix-only: asserts POSIX mode bits, which Windows does not have.
    #[cfg(unix)]
    #[tokio::test(flavor = "current_thread")]
    async fn unix_server_binds_with_0600_permissions() {
        let tmp = TempDir::new().unwrap();
        let sock = tmp.path().join("test.sock");

        let auth = Arc::new(make_auth());
        let server = Arc::new(UnixServer::new(
            Some(sock.clone()),
            auth,
            McpServerConfig::default(),
            NotificationBus::new(),
        ));

        local_test!(async {
            let server2 = Arc::clone(&server);
            let handle = tokio::task::spawn_local(async move { server2.listen().await });

            tokio::time::sleep(std::time::Duration::from_millis(50)).await;

            let meta = std::fs::metadata(&sock).expect("socket should exist");
            let mode = meta.permissions().mode() & 0o777;
            assert_eq!(mode, 0o600, "socket should have mode 0600, got {:#o}", mode);

            server.stop().await.unwrap();
            let _ = handle.await;
        });
    }

    #[tokio::test(flavor = "current_thread")]
    async fn unix_server_valid_auth_proceeds() {
        let tmp = TempDir::new().unwrap();
        let sock = tmp.path().join("valid.sock");

        let auth_secret = [17u8; 32];
        let auth = Arc::new(McpAuth::from_secret(auth_secret));
        let token = auth.generate_token();

        local_test!(async {
            let server = Arc::new(UnixServer::new(
                Some(sock.clone()),
                Arc::clone(&auth),
                McpServerConfig::default(),
                NotificationBus::new(),
            ));
            let server2 = Arc::clone(&server);
            let handle = tokio::task::spawn_local(async move { server2.listen().await });

            tokio::time::sleep(std::time::Duration::from_millis(50)).await;

            let mut stream = UnixStream::connect(&sock).await.expect("connect");
            let auth_msg = format!(
                "{{\"jsonrpc\":\"2.0\",\"id\":null,\"method\":\"auth\",\"params\":{{\"token\":\"{}\"}}}}\n",
                token
            );
            stream.write_all(auth_msg.as_bytes()).await.unwrap();

            let mut reader = BufReader::new(&mut stream);
            let mut response = String::new();
            tokio::time::timeout(
                std::time::Duration::from_secs(2),
                reader.read_line(&mut response),
            )
            .await
            .unwrap()
            .unwrap();

            let val: serde_json::Value = serde_json::from_str(response.trim()).unwrap();
            assert!(
                val["result"]["ok"].as_bool().unwrap_or(false),
                "Expected ok:true, got: {}",
                response
            );

            server.stop().await.unwrap();
            let _ = handle.await;
        });
    }

    #[tokio::test(flavor = "current_thread")]
    async fn unix_server_invalid_auth_closes_connection() {
        let tmp = TempDir::new().unwrap();
        let sock = tmp.path().join("invalid.sock");

        let auth = Arc::new(make_auth());

        local_test!(async {
            let server = Arc::new(UnixServer::new(
                Some(sock.clone()),
                Arc::clone(&auth),
                McpServerConfig::default(),
                NotificationBus::new(),
            ));
            let server2 = Arc::clone(&server);
            let handle = tokio::task::spawn_local(async move { server2.listen().await });

            tokio::time::sleep(std::time::Duration::from_millis(50)).await;

            let mut stream = UnixStream::connect(&sock).await.expect("connect");
            stream
                .write_all(b"{\"jsonrpc\":\"2.0\",\"id\":null,\"method\":\"auth\",\"params\":{\"token\":\"bad-token\"}}\n")
                .await
                .unwrap();

            let mut reader = BufReader::new(&mut stream);
            let mut response = String::new();
            let _ = tokio::time::timeout(
                std::time::Duration::from_secs(2),
                reader.read_line(&mut response),
            )
            .await;

            if !response.trim().is_empty() {
                let val: serde_json::Value = serde_json::from_str(response.trim()).unwrap();
                assert!(val.get("error").is_some(), "Expected error response");
            }

            let mut extra = String::new();
            let n = tokio::time::timeout(
                std::time::Duration::from_secs(1),
                reader.read_line(&mut extra),
            )
            .await
            .unwrap_or(Ok(0))
            .unwrap_or(0);
            assert_eq!(n, 0, "Connection should be closed after auth failure");

            server.stop().await.unwrap();
            let _ = handle.await;
        });
    }

    #[tokio::test(flavor = "current_thread")]
    async fn unix_server_socket_removed_on_shutdown() {
        let tmp = TempDir::new().unwrap();
        let sock = tmp.path().join("cleanup.sock");

        let auth = Arc::new(make_auth());

        local_test!(async {
            let server = Arc::new(UnixServer::new(
                Some(sock.clone()),
                Arc::clone(&auth),
                McpServerConfig::default(),
                NotificationBus::new(),
            ));
            let server2 = Arc::clone(&server);
            let handle = tokio::task::spawn_local(async move { server2.listen().await });

            tokio::time::sleep(std::time::Duration::from_millis(50)).await;
            assert!(sock.exists(), "Socket should exist after bind");

            server.stop().await.unwrap();
            let _ = handle.await;

            assert!(!sock.exists(), "Socket should be removed after shutdown");
        });
    }
}
