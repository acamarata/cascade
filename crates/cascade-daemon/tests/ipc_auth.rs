//! Integration tests for IPC hardening: auth token, connection pool, graceful drain.
//!
//! Purpose: verifies the security/reliability items for T-P2-E03-03:
//!   1. Wrong auth token → -32002 error response, connection closed.
//!   2. Correct auth token + Ping → {"pong":""} result.
//!   3. Graceful drain: cancel token, wait up to 5 s, in-flight handler exits cleanly.
//!
//! NOTE: every request must be wrapped as `{"auth": <token>, "rpc": <jsonrpc>}`
//! (auth-gate added in 5f75f2f) — see `authed_request`/`read_ipc_token` below.
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — ipc_auth integration test row (T-P2-E03-03)

use std::{fs, path::PathBuf, time::Duration};

use tempfile::TempDir;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;
use tokio_util::sync::CancellationToken;

use cascade_daemon::{event_bus::EventBus, healthcheck::HealthState, ipc::IpcServer};

// ── Frame helpers (LE framing matching ipc.rs) ────────────────────────────

/// Write a 4-byte LE length-prefixed frame to a UnixStream.
async fn write_frame(stream: &mut UnixStream, body: &[u8]) {
    let len = (body.len() as u32).to_be_bytes();
    stream.write_all(&len).await.expect("write len");
    stream.write_all(body).await.expect("write body");
    stream.flush().await.expect("flush");
}

/// Read a 4-byte LE length-prefixed frame from a UnixStream.
async fn read_frame(stream: &mut UnixStream) -> Vec<u8> {
    let mut len_buf = [0u8; 4];
    stream.read_exact(&mut len_buf).await.expect("read len");
    let len = u32::from_be_bytes(len_buf) as usize;
    let mut body = vec![0u8; len];
    stream.read_exact(&mut body).await.expect("read body");
    body
}

/// Write a method-only RPC frame and read back the response JSON.
async fn send_request(stream: &mut UnixStream, rpc: serde_json::Value) -> serde_json::Value {
    let body = serde_json::to_vec(&rpc).unwrap();
    write_frame(stream, &body).await;
    let resp_bytes = read_frame(stream).await;
    serde_json::from_slice(&resp_bytes).expect("parse response JSON")
}

/// Read the IPC auth token the daemon wrote to `<config_dir>/ipc_token`.
///
/// Every request must be wrapped as `{"auth": <token>, "rpc": <jsonrpc>}` —
/// see the auth-gate added in 5f75f2f. A bare `{"method": ...}` frame (the
/// pre-auth-gate shape these tests used to send) always gets "auth failed".
fn read_ipc_token(config_dir: &std::path::Path) -> String {
    fs::read_to_string(config_dir.join("ipc_token")).expect("read ipc_token")
}

/// Build an authenticated JSON-RPC request envelope.
fn authed_request(method: &str, token: &str) -> serde_json::Value {
    serde_json::json!({
        "auth": token,
        "rpc": {
            "jsonrpc": "2.0",
            "id": 1,
            "method": method,
            "params": {},
            "protocol_version": 1
        }
    })
}

// ── Test server factory ───────────────────────────────────────────────────

/// Spin up an IpcServer in a background task on a tmp socket.
/// Returns (socket_path, auth_token, shutdown_token, task_handle).
async fn start_test_server(
    tmp: &TempDir,
) -> (PathBuf, String, CancellationToken, tokio::task::JoinHandle<()>) {
    let config_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&config_dir).unwrap();

    let health = HealthState::new(std::time::Instant::now());
    let bus = EventBus::new(config_dir.clone()).await.expect("event bus");

    let ipc = IpcServer::new(
        config_dir.clone(),
        health,
        bus,
        std::sync::Arc::new(cascade_providers::ProviderRegistry::new()),
    )
    .await
    .expect("IpcServer::new");

    let token = read_ipc_token(&config_dir);

    let shutdown = CancellationToken::new();
    let shutdown_clone = shutdown.clone();
    let socket_path = config_dir.join("daemon.sock");

    let handle = tokio::spawn(async move {
        ipc.serve(shutdown_clone).await.ok();
    });

    // Wait until the socket file appears (daemon is ready to accept).
    for _ in 0..100 {
        if socket_path.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert!(socket_path.exists(), "IPC socket did not appear within 2s");

    (socket_path, token, shutdown, handle)
}

// ── Tests ─────────────────────────────────────────────────────────────────

/// Wrong auth token → -32002 error response (T-P2-E03-03 item 1).
#[tokio::test(flavor = "multi_thread")]
async fn test_wrong_auth_token_rejected() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, _token, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();
    let resp = send_request(&mut stream, authed_request("ping", "definitely-wrong-token")).await;

    assert_eq!(
        resp["error"]["code"].as_i64(),
        Some(-32002),
        "wrong auth token must yield -32002: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// Ping method returns a response (smoke test: server accepts connections and
/// responds to a known method).
#[tokio::test(flavor = "multi_thread")]
async fn test_ping_returns_response() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, token, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    let resp = send_request(&mut stream, authed_request("ping", &token)).await;

    assert!(
        resp.get("error").is_none(),
        "unexpected error in ping response: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// Health method returns a minimal response with only "status": "ok".
/// Verifies that internal state (pid, uptime, queue depth, etc.) is not exposed.
#[tokio::test(flavor = "multi_thread")]
async fn test_health_returns_minimal_response() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, token, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    let resp = send_request(&mut stream, authed_request("health", &token)).await;

    // Success responses are wrapped in the JSON-RPC 2.0 envelope under "result"
    // (see write_response() in ipc.rs); errors would be nested under "error".
    assert!(
        resp.get("error").is_none(),
        "health response should not contain an error: {resp}"
    );
    let result = resp
        .get("result")
        .expect("health response must contain a result field");

    // The health result must be exactly {"status":"ok"} with no other fields.
    assert_eq!(
        result.get("status"),
        Some(&serde_json::Value::String("ok".to_string())),
        "health response status must be \"ok\": {resp}"
    );
    assert!(
        result.get("pid").is_none(),
        "health response must not expose pid: {resp}"
    );
    assert!(
        result.get("uptime_secs").is_none(),
        "health response must not expose uptime_secs: {resp}"
    );
    assert!(
        result.get("queue_depth").is_none(),
        "health response must not expose queue_depth: {resp}"
    );
    assert!(
        result.get("ram_kb").is_none(),
        "health response must not expose ram_kb: {resp}"
    );
    assert!(
        result.get("cpu_pct").is_none(),
        "health response must not expose cpu_pct: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// Graceful drain: cancel the shutdown token while a connection is held open,
/// then verify the server exits within 6 s (5 s drain + 1 s margin).
#[tokio::test(flavor = "multi_thread")]
async fn test_graceful_drain_completes() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, token, shutdown, handle) = start_test_server(&tmp).await;

    // Open a connection, send one ping, but keep the stream open.
    let mut stream = UnixStream::connect(&socket_path).await.unwrap();
    let _ = send_request(&mut stream, authed_request("ping", &token)).await;

    // Cancel the server. Graceful drain gives in-flight handlers up to 5 s.
    shutdown.cancel();

    // Drop the stream so the handler sees EOF and exits.
    drop(stream);

    // Server should fully exit within 6 s.
    let result = tokio::time::timeout(Duration::from_secs(6), handle).await;
    assert!(
        result.is_ok(),
        "IpcServer::serve did not complete within 6s"
    );
}
