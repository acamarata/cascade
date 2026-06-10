//! Per-connection context and loop for MCP server-side transports.
//!
//! ## Purpose
//! Provides the `ConnectionContext` per-connection metadata and `connection_loop`
//! which runs a complete MCP session over any `AsyncRead + AsyncWrite` I/O pair.
//!
//! ## Inputs
//! - `read`: async read half (Unix stream, TCP stream, stdin, etc.).
//! - `write`: async write half.
//! - `config`: `McpServerConfig` for the `McpServer` instance serving this connection.
//! - `bus`: the server's `NotificationBus`; this connection subscribes on entry.
//! - `ctx`: `ConnectionContext` — must be authenticated before calling this function.
//!
//! ## Outputs
//! - Returns `Ok(())` when the connection closes cleanly (EOF).
//! - Returns `Err` on unrecoverable I/O errors.
//!
//! ## Constraints
//! - Max frame size: 4 MB. Exceeding it sends a `ParseError` response and closes.
//! - `connection_loop` runs on the calling task — no `tokio::spawn` internally.
//!
//! ## SPORT
//! MASTER-TRANSPORTS.md: McpTransport trait + connection_loop

use std::collections::HashSet;

use futures_util::StreamExt;
use tokio::io::{AsyncRead, AsyncWrite, AsyncWriteExt};
use tokio::sync::broadcast;
use tokio_util::codec::{FramedRead, LinesCodec, LinesCodecError};
use tracing::{debug, error, info, warn};
use uuid::Uuid;

use cascade_types::error::{CascadeError, Result};

use crate::notification::NotificationBus;
use crate::server::{McpServer, McpServerConfig};

// ── Constants ─────────────────────────────────────────────────────────────────

/// Maximum frame size: 4 MB per spec.
pub const MAX_FRAME_BYTES: usize = 4 * 1024 * 1024;

// ── SubscriptionSet ───────────────────────────────────────────────────────────

/// Tracks resource URI subscriptions for this connection.
#[derive(Debug, Default, Clone)]
pub struct SubscriptionSet(pub HashSet<String>);

impl SubscriptionSet {
    pub fn insert(&mut self, uri: String) {
        self.0.insert(uri);
    }
    pub fn remove(&mut self, uri: &str) {
        self.0.remove(uri);
    }
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
}

// ── ConnectionContext ─────────────────────────────────────────────────────────

/// Per-connection metadata.
#[derive(Debug)]
pub struct ConnectionContext {
    pub conn_id: Uuid,
    pub authenticated: bool,
    pub subscriptions: SubscriptionSet,
}

impl ConnectionContext {
    pub fn new() -> Self {
        Self {
            conn_id: Uuid::new_v4(),
            authenticated: false,
            subscriptions: SubscriptionSet::default(),
        }
    }

    /// Pre-authenticated context for stdio (OS-isolated, no auth needed).
    pub fn pre_authenticated() -> Self {
        Self {
            conn_id: Uuid::new_v4(),
            authenticated: true,
            subscriptions: SubscriptionSet::default(),
        }
    }
}

impl Default for ConnectionContext {
    fn default() -> Self {
        Self::new()
    }
}

// ── connection_loop ───────────────────────────────────────────────────────────

/// Run a complete MCP session over one accepted connection.
///
/// Reads newline-delimited JSON-RPC frames from `read`, dispatches each through
/// a fresh `McpServer` (via internal channels), writes responses to `write`, and
/// forwards `NotificationBus` broadcasts as they arrive.
///
/// Auth must be completed by the caller — `ctx.authenticated` must be `true`.
///
/// Returns when the connection closes cleanly or on unrecoverable I/O error.
pub async fn connection_loop<R, W>(
    read: R,
    write: W,
    config: McpServerConfig,
    notification_bus: NotificationBus,
    mut ctx: ConnectionContext,
) -> Result<()>
where
    R: AsyncRead + Unpin + Send + 'static,
    W: AsyncWrite + Unpin + Send + 'static,
{
    let conn_id = ctx.conn_id;
    info!(conn_id = %conn_id, "connection_loop started");

    // Build channel-backed transport so McpServer can use its normal run() API.
    // McpServer::run() consumes self and is not Send (Transport isn't Sync).
    // We drive the server via channels from this task — no extra spawn needed.
    let (in_tx, in_rx) = tokio::sync::mpsc::channel::<String>(64);
    let (out_tx, mut out_rx) = tokio::sync::mpsc::channel::<String>(64);

    let server_transport = ChannelTransportPub {
        recv: in_rx,
        send: out_tx,
    };
    let server = McpServer::new(config, Box::new(server_transport));
    // Run the server as a local future driven by poll in our select! loop.
    let mut server_fut = std::pin::pin!(server.run());

    let mut bus_rx: broadcast::Receiver<serde_json::Value> = notification_bus.subscribe();
    let codec = LinesCodec::new_with_max_length(MAX_FRAME_BYTES);
    let mut framed = FramedRead::new(read, codec);
    let mut write = write;

    let result = loop {
        tokio::select! {
            // Drive the McpServer future (it reads from in_rx, writes to out_tx).
            server_result = &mut server_fut => {
                // Server exited — drain any remaining output then break.
                if let Err(e) = server_result {
                    debug!(conn_id = %conn_id, error = %e, "McpServer exited with error");
                }
                break Ok(());
            }

            // Inbound: read a frame from the remote client.
            frame = framed.next() => {
                match frame {
                    None => {
                        info!(conn_id = %conn_id, "connection EOF");
                        drop(in_tx);
                        break Ok(());
                    }
                    Some(Err(LinesCodecError::MaxLineLengthExceeded)) => {
                        warn!(conn_id = %conn_id, "frame exceeds 4 MB — closing");
                        let err = parse_error_response();
                        let _ = write_line(&mut write, &err).await;
                        break Ok(());
                    }
                    Some(Err(e)) => {
                        error!(conn_id = %conn_id, error = %e, "frame read error");
                        break Ok(());
                    }
                    Some(Ok(line)) => {
                        if line.trim().is_empty() {
                            continue;
                        }
                        if !ctx.authenticated {
                            let err = auth_required_response();
                            let _ = write_line(&mut write, &err).await;
                            break Ok(());
                        }
                        debug!(conn_id = %conn_id, bytes = line.len(), "recv frame");
                        if in_tx.send(line).await.is_err() {
                            break Ok(()); // server channel closed
                        }
                    }
                }
            }

            // Outbound: response from McpServer.
            resp = out_rx.recv() => {
                match resp {
                    None => break Ok(()), // channel closed
                    Some(msg) => {
                        if let Err(e) = write_line(&mut write, &msg).await {
                            error!(conn_id = %conn_id, error = %e, "write error");
                            break Ok(());
                        }
                    }
                }
            }

            // Notification bus — forward to client.
            notif = bus_rx.recv() => {
                match notif {
                    Ok(val) => {
                        if let Ok(s) = serde_json::to_string(&val) {
                            if let Err(e) = write_line(&mut write, &s).await {
                                error!(conn_id = %conn_id, error = %e, "notification write error");
                                break Ok(());
                            }
                        }
                    }
                    Err(broadcast::error::RecvError::Lagged(n)) => {
                        warn!(conn_id = %conn_id, missed = n, "notification bus lagged");
                    }
                    Err(broadcast::error::RecvError::Closed) => break Ok(()),
                }
            }
        }
    };

    // Clean up.
    if !ctx.subscriptions.is_empty() {
        ctx.subscriptions.0.clear();
    }
    info!(conn_id = %conn_id, "connection_loop exited");
    result
}

// ── Helpers ───────────────────────────────────────────────────────────────────

async fn write_line<W: AsyncWrite + Unpin>(writer: &mut W, msg: &str) -> std::io::Result<()> {
    writer.write_all(msg.as_bytes()).await?;
    writer.write_all(b"\n").await?;
    writer.flush().await
}

fn parse_error_response() -> String {
    r#"{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Parse error: message exceeds 4 MB limit"}}"#.into()
}

fn auth_required_response() -> String {
    r#"{"jsonrpc":"2.0","id":null,"error":{"code":-32003,"message":"Authentication required"}}"#
        .into()
}

// ── ChannelTransportPub ──────────────────────────────────────────────────────

/// Public channel-backed `Transport` for stateless HTTP dispatch and connection_loop.
///
/// Callers create matching channels, pass this as the transport to `McpServer::new()`,
/// send messages via the input channel, and receive responses via the output channel.
pub struct ChannelTransportPub {
    pub recv: tokio::sync::mpsc::Receiver<String>,
    pub send: tokio::sync::mpsc::Sender<String>,
}

#[async_trait::async_trait]
impl crate::transport::Transport for ChannelTransportPub {
    async fn send(&mut self, message: &str) -> cascade_types::error::Result<()> {
        self.send
            .send(message.to_owned())
            .await
            .map_err(|_| CascadeError::Io {
                path: "<channel-transport>".into(),
                operation: "send",
                source: std::io::Error::new(std::io::ErrorKind::BrokenPipe, "channel closed"),
            })
    }

    async fn recv(&mut self) -> cascade_types::error::Result<Option<String>> {
        Ok(self.recv.recv().await)
    }

    async fn close(&mut self) -> cascade_types::error::Result<()> {
        Ok(())
    }

    fn name(&self) -> &str {
        "channel"
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// Run a connection_loop test body inside a LocalSet so spawn_local works
    /// for the non-Send McpServer future.
    macro_rules! local_test {
        (async $body:block) => {{
            let local = tokio::task::LocalSet::new();
            local.run_until(async $body).await;
        }};
    }

    #[tokio::test(flavor = "current_thread")]
    async fn connection_loop_initialize_round_trip() {
        local_test!(async {
            let (mut client, server) = tokio::io::duplex(65536);
            let (server_read, server_write) = tokio::io::split(server);

            let bus = NotificationBus::new();
            let ctx = ConnectionContext::pre_authenticated();
            let config = McpServerConfig::default();

            let loop_handle = tokio::task::spawn_local(connection_loop(
                server_read,
                server_write,
                config,
                bus,
                ctx,
            ));

            use tokio::io::AsyncWriteExt;
            let init = r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1"}}}"#;
            client.write_all(init.as_bytes()).await.unwrap();
            client.write_all(b"\n").await.unwrap();
            client.flush().await.unwrap();

            use tokio::io::AsyncBufReadExt;
            let mut reader = tokio::io::BufReader::new(&mut client);
            let mut response = String::new();
            reader.read_line(&mut response).await.unwrap();
            assert!(!response.trim().is_empty(), "Expected a JSON response");

            let val: serde_json::Value = serde_json::from_str(response.trim()).expect("valid JSON");
            assert_eq!(val["jsonrpc"], "2.0");
            assert!(val.get("result").is_some() || val.get("error").is_some());

            drop(client);
            let result = loop_handle.await.unwrap();
            assert!(
                result.is_ok(),
                "connection_loop should exit cleanly: {:?}",
                result
            );
        });
    }

    #[tokio::test(flavor = "current_thread")]
    async fn connection_loop_oversized_frame_closes_connection() {
        local_test!(async {
            let (mut client, server) = tokio::io::duplex(MAX_FRAME_BYTES * 2 + 1024);
            let (server_read, server_write) = tokio::io::split(server);

            let bus = NotificationBus::new();
            let ctx = ConnectionContext::pre_authenticated();
            let config = McpServerConfig::default();

            let loop_handle = tokio::task::spawn_local(connection_loop(
                server_read,
                server_write,
                config,
                bus,
                ctx,
            ));

            use tokio::io::AsyncWriteExt;
            let big = vec![b'x'; MAX_FRAME_BYTES + 1];
            client.write_all(&big).await.unwrap();
            client.write_all(b"\n").await.unwrap();
            client.flush().await.unwrap();

            use tokio::io::AsyncBufReadExt;
            let mut reader = tokio::io::BufReader::new(&mut client);
            let mut response = String::new();
            let _ = tokio::time::timeout(
                std::time::Duration::from_secs(2),
                reader.read_line(&mut response),
            )
            .await;

            if !response.trim().is_empty() {
                let val: serde_json::Value =
                    serde_json::from_str(response.trim()).expect("valid JSON");
                if let Some(err) = val.get("error") {
                    assert_eq!(err["code"], -32700);
                }
            }

            drop(client);
            let _ = loop_handle.await;
        });
    }

    #[tokio::test(flavor = "current_thread")]
    async fn connection_loop_unauthenticated_is_rejected() {
        local_test!(async {
            let (mut client, server) = tokio::io::duplex(65536);
            let (server_read, server_write) = tokio::io::split(server);

            let bus = NotificationBus::new();
            let ctx = ConnectionContext::new(); // NOT pre-authenticated
            let config = McpServerConfig::default();

            let loop_handle = tokio::task::spawn_local(connection_loop(
                server_read,
                server_write,
                config,
                bus,
                ctx,
            ));

            use tokio::io::AsyncWriteExt;
            let msg = r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}"#;
            client.write_all(msg.as_bytes()).await.unwrap();
            client.write_all(b"\n").await.unwrap();
            client.flush().await.unwrap();

            use tokio::io::AsyncBufReadExt;
            let mut reader = tokio::io::BufReader::new(&mut client);
            let mut response = String::new();
            let _ = tokio::time::timeout(
                std::time::Duration::from_secs(2),
                reader.read_line(&mut response),
            )
            .await;

            if !response.trim().is_empty() {
                let val: serde_json::Value =
                    serde_json::from_str(response.trim()).expect("valid JSON");
                assert!(val.get("error").is_some());
            }

            drop(client);
            let _ = loop_handle.await;
        });
    }

    #[tokio::test(flavor = "current_thread")]
    async fn notification_bus_forwarded_to_connection() {
        use cascade_types::mcp::JsonRpcNotification;
        use std::time::Duration;

        local_test!(async {
            let (mut client, server) = tokio::io::duplex(65536);
            let (server_read, server_write) = tokio::io::split(server);

            let bus = NotificationBus::new();
            let bus2 = bus.clone();
            let ctx = ConnectionContext::pre_authenticated();
            let config = McpServerConfig::default();

            let loop_handle = tokio::task::spawn_local(connection_loop(
                server_read,
                server_write,
                config,
                bus,
                ctx,
            ));

            // Let connection_loop subscribe to bus before broadcasting.
            tokio::time::sleep(Duration::from_millis(20)).await;

            let notif = JsonRpcNotification::<serde_json::Value>::new(
                "notifications/tools/list_changed",
                Some(serde_json::json!({})),
            );
            bus2.broadcast(notif);

            use tokio::io::AsyncBufReadExt;
            let mut reader = tokio::io::BufReader::new(&mut client);
            let mut line = String::new();

            let result =
                tokio::time::timeout(Duration::from_millis(500), reader.read_line(&mut line)).await;

            assert!(result.is_ok(), "Notification should arrive within 500ms");
            let n = result.unwrap().unwrap_or(0);
            assert!(n > 0, "Expected notification data, got empty");

            drop(client);
            let _ = loop_handle.await;
        });
    }
}
