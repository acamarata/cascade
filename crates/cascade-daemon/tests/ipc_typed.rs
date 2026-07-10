//! Integration tests for the typed JSON-RPC dispatch scaffold (ADR-P3-001).
//!
//! Purpose: verifies the two-path dispatch wired in `handle_connection`:
//!   1. Legacy path: requests matching the frozen 8-method `Request` enum are
//!      dispatched as before (all P2 methods still work).
//!   2. Typed scaffold: requests that fail legacy deserialization (unknown fields,
//!      unknown methods) are validated via `cascade_types::ipc` and return
//!      structured IpcError responses.
//!
//! These tests use the full socket path (UnixStream → handle_connection) because
//! `try_typed_dispatch` is `pub(crate)` and not directly callable from here.
//!
//! SPORT: .opencode/phases/sport/MASTER-ENDPOINTS.md — IPC routing note
//!        updated to "typed JSON-RPC (ADR-P3-001)"

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

/// Serialize `rpc` as a length-prefixed frame, send it, and read back the
/// response JSON value.
async fn send_request(stream: &mut UnixStream, rpc: serde_json::Value) -> serde_json::Value {
    let body = serde_json::to_vec(&rpc).unwrap();
    write_frame(stream, &body).await;
    let resp_bytes = read_frame(stream).await;
    serde_json::from_slice(&resp_bytes).expect("parse response JSON")
}

// ── Test server factory ───────────────────────────────────────────────────

/// Spin up an IpcServer in a background task on a tmp socket.
/// Returns (socket_path, shutdown_token, task_handle).
async fn start_test_server(
    tmp: &TempDir,
) -> (PathBuf, CancellationToken, tokio::task::JoinHandle<()>) {
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

    (socket_path, shutdown, handle)
}

// ── Tests ─────────────────────────────────────────────────────────────────

/// A request with a top-level field unknown to the legacy enum (but a valid
/// JSON object) falls through to the typed scaffold.  The typed envelope
/// validator detects `deny_unknown_fields` and returns a structured -32600
/// IpcError (unknown field).
///
/// Acceptance: response must contain `"code": -32600`.
#[tokio::test(flavor = "multi_thread")]
async fn test_typed_unknown_field_returns_structured_error() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    // This request uses a method name not in the legacy enum (so it falls
    // through to typed dispatch) AND has an unknown field — which triggers
    // deny_unknown_fields on the typed Request<P> envelope.
    let resp = send_request(
        &mut stream,
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "nonexistent_method",
            "unknown_field_xyz": true,
            "protocol_version": 1
        }),
    )
    .await;

    assert_eq!(
        resp.get("error")
            .and_then(|e| e.get("code"))
            .and_then(|v| v.as_i64()),
        Some(-32600),
        "expected -32600 for unknown field; got: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// A well-formed typed envelope whose method name is not among the 8 legacy
/// methods returns METHOD_NOT_FOUND (-32601) from the typed scaffold.
///
/// Acceptance: response must contain `"code": -32601`.
#[tokio::test(flavor = "multi_thread")]
async fn test_typed_unknown_method_returns_method_not_found() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    // `nonexistent_typed_method` does not match any arm in the legacy Request
    // enum, so the typed scaffold handles it and returns METHOD_NOT_FOUND.
    // The envelope must include all required Request<P> fields so that
    // deserialize_request succeeds and we reach the Ok(typed_req) branch.
    let resp = send_request(
        &mut stream,
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "nonexistent_typed_method",
            "protocol_version": 1
        }),
    )
    .await;

    assert_eq!(
        resp.get("error")
            .and_then(|e| e.get("code"))
            .and_then(|v| v.as_i64()),
        Some(-32601),
        "expected -32601 for unknown method; got: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// The legacy `ping` method still works through the unchanged legacy dispatch
/// path.  A successful ping response has no `code` error field.
///
/// Acceptance: confirms P2 backward-compatibility — all 8 legacy methods still
/// reachable; typed scaffold is additive only.
#[tokio::test(flavor = "multi_thread")]
async fn test_legacy_ping_still_works_through_typed_routing() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    let resp = send_request(
        &mut stream,
        serde_json::json!({ "method": "ping", "echo": null }),
    )
    .await;

    assert!(
        resp.get("code").is_none(),
        "legacy ping unexpectedly returned an error: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}
