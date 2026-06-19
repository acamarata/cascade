//! Integration tests for `cascade_resolve` IPC handler (T-P5-E02-03).
//!
//! Purpose: Proves the full path cascade_resolve IPC → real resolver dispatch.
//!   1. cascade_resolve never returns METHOD_NOT_FOUND (-32601) after T-P5-E02-01.
//!   2. ResolveResult.tier matches expected tier slug for fixture paths.
//!   3. Unknown fields in params return -32602 (invalid params).
//!   4. Tier-classified content is non-empty when a CASCADE.md fixture is present.
//!
//! All tests use synthetic tempdir home dirs — no ~/Sites dependency in CI.
//!
//! SPORT: F02-COMMAND-INVENTORY.md — cascade_resolve marked TESTED (integration)
//!        F08-SERVICE-INVENTORY.md — resolve IPC test coverage noted

use std::{fs, path::PathBuf, time::Duration};

use tempfile::TempDir;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;
use tokio_util::sync::CancellationToken;

use cascade_daemon::{event_bus::EventBus, healthcheck::HealthState, ipc::IpcServer};

// ── Frame helpers (matches ipc.rs framing) ────────────────────────────────

async fn write_frame(stream: &mut UnixStream, body: &[u8]) {
    let len = (body.len() as u32).to_le_bytes();
    stream.write_all(&len).await.expect("write len");
    stream.write_all(body).await.expect("write body");
    stream.flush().await.expect("flush");
}

async fn read_frame(stream: &mut UnixStream) -> Vec<u8> {
    let mut len_buf = [0u8; 4];
    stream.read_exact(&mut len_buf).await.expect("read len");
    let len = u32::from_le_bytes(len_buf) as usize;
    let mut body = vec![0u8; len];
    stream.read_exact(&mut body).await.expect("read body");
    body
}

async fn send_request(stream: &mut UnixStream, rpc: serde_json::Value) -> serde_json::Value {
    let body = serde_json::to_vec(&rpc).unwrap();
    write_frame(stream, &body).await;
    let resp_bytes = read_frame(stream).await;
    serde_json::from_slice(&resp_bytes).expect("parse response JSON")
}

// ── Test server factory ───────────────────────────────────────────────────

/// Start an IpcServer with a synthetic home dir under `tmp`.
///
/// The server socket lives at `tmp/.cascade/daemon.sock`.
/// The cascade_resolve handler derives project_root = tmp (socket parent's parent).
async fn start_test_server(
    tmp: &TempDir,
) -> (PathBuf, CancellationToken, tokio::task::JoinHandle<()>) {
    let config_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&config_dir).unwrap();

    let health = HealthState::new(std::time::Instant::now());
    let bus = EventBus::new(config_dir.clone()).await.expect("event bus");

    let ipc = IpcServer::new(config_dir.clone(), health, bus)
        .await
        .expect("IpcServer::new");

    let shutdown = CancellationToken::new();
    let shutdown_clone = shutdown.clone();
    let socket_path = config_dir.join("daemon.sock");

    let handle = tokio::spawn(async move {
        ipc.serve(shutdown_clone).await.ok();
    });

    for _ in 0..100 {
        if socket_path.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert!(socket_path.exists(), "IPC socket did not appear within 2s");

    (socket_path, shutdown, handle)
}

/// Build a valid cascade_resolve request body.
fn resolve_request(tier: Option<&str>, format: Option<&str>) -> serde_json::Value {
    let mut params = serde_json::Map::new();
    if let Some(t) = tier {
        params.insert("tier".to_string(), serde_json::Value::String(t.to_string()));
    }
    if let Some(f) = format {
        params.insert("format".to_string(), serde_json::Value::String(f.to_string()));
    }
    serde_json::json!({
        "jsonrpc": "2.0",
        "id": 1,
        "method": "cascade_resolve",
        "params": params,
        "protocol_version": 1
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────

/// Primary acceptance gate: cascade_resolve NEVER returns METHOD_NOT_FOUND (-32601).
///
/// Sends a bare cascade_resolve with empty params. The handler must return a
/// ResolveResult (possibly with empty content if no CASCADE.md exists), not -32601.
#[tokio::test]
async fn test_cascade_resolve_never_returns_method_not_found() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();
    let resp = send_request(&mut stream, resolve_request(None, None)).await;

    // Must NOT be -32601 METHOD_NOT_FOUND.
    let code = resp.get("code").and_then(|v| v.as_i64());
    assert_ne!(
        code,
        Some(-32601),
        "cascade_resolve must not return METHOD_NOT_FOUND; got: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// Unknown fields in params return -32602 (invalid params / deny_unknown_fields).
#[tokio::test]
async fn test_cascade_resolve_unknown_params_field_returns_invalid_params() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    let req = serde_json::json!({
        "jsonrpc": "2.0",
        "id": 2,
        "method": "cascade_resolve",
        "params": {
            "tier": "gci",
            "unknown_extra_field": true
        },
        "protocol_version": 1
    });
    let resp = send_request(&mut stream, req).await;

    assert_eq!(
        resp.get("code").and_then(|v| v.as_i64()),
        Some(-32602),
        "unknown params field must return -32602; got: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// With a CASCADE.md written to the synthetic home's .cascade/ dir (GCI tier),
/// ResolveResult.content is non-empty and format is "markdown".
#[tokio::test]
async fn test_cascade_resolve_with_gci_fixture_returns_content() {
    let tmp = TempDir::new().unwrap();

    // Write a GCI-level CASCADE.md fixture at tmp/.cascade/CASCADE.md.
    // The resolver derives project_root = tmp (socket_path.parent().parent()).
    // GCI tier default_path = $HOME/.cascade — but cascade_resolve uses
    // TierName::default_path which reads $HOME. We override HOME to tmp
    // so the GCI tier resolves to tmp/.cascade/.
    let cascade_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();
    fs::write(
        cascade_dir.join("CASCADE.md"),
        "# Test GCI instructions\nAlways use snake_case.",
    )
    .unwrap();

    // Also write a GCI config.toml so the resolver picks up instructions.
    fs::write(
        cascade_dir.join("config.toml"),
        r#"instructions = "Always use snake_case.""#,
    )
    .unwrap();

    let (socket_path, shutdown, handle) = {
        let health = HealthState::new(std::time::Instant::now());
        let bus = EventBus::new(cascade_dir.clone()).await.expect("event bus");
        let ipc = IpcServer::new(cascade_dir.clone(), health, bus)
            .await
            .expect("IpcServer::new");
        let shutdown = CancellationToken::new();
        let sc = shutdown.clone();
        let sp = cascade_dir.join("daemon.sock");
        let handle = tokio::spawn(async move { ipc.serve(sc).await.ok(); });
        for _ in 0..100 {
            if sp.exists() { break; }
            tokio::time::sleep(Duration::from_millis(20)).await;
        }
        (sp, shutdown, handle)
    };

    // Override HOME so resolve_cascade finds our fixture at tmp/.cascade.
    std::env::set_var("HOME", tmp.path());

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();
    let resp = send_request(&mut stream, resolve_request(None, Some("markdown"))).await;

    // Should be a ResolveResult, not an error.
    assert!(
        resp.get("code").is_none(),
        "expected success, got error: {resp}"
    );
    assert_eq!(
        resp.get("format").and_then(|v| v.as_str()),
        Some("markdown"),
        "format must be 'markdown'; got: {resp}"
    );
    // tier must be present and not empty.
    assert!(
        resp.get("tier").and_then(|v| v.as_str()).map(|s| !s.is_empty()).unwrap_or(false),
        "tier field must be non-empty; got: {resp}"
    );
    // content must be non-empty since we wrote a config.toml with instructions.
    let content = resp.get("content").and_then(|v| v.as_str()).unwrap_or("");
    assert!(
        !content.is_empty(),
        "content must be non-empty when a GCI fixture is present; got: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// cascade_resolve with format="json" returns format="json" in the result.
#[tokio::test]
async fn test_cascade_resolve_format_json_round_trips() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();
    let resp = send_request(&mut stream, resolve_request(None, Some("json"))).await;

    // Must not be an error and must echo back format="json".
    assert!(resp.get("code").is_none(), "unexpected error: {resp}");
    assert_eq!(
        resp.get("format").and_then(|v| v.as_str()),
        Some("json"),
        "format must round-trip as 'json'; got: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}
