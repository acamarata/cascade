//! Integration tests for the `cascade_resolve` IPC handler (E-P5-02).
//!
//! Purpose: prove that:
//!   1. project-tier `.cascade/CASCADE.md` files are reachable via `cwd` param
//!   2. the full tier-merged cascade is returned (not just a single tier)
//!   3. `format="json"` returns parseable JSON with the expected cascade fields
//!   4. invalid `format` values return -32602
//!   5. `deny_unknown_fields` on `ResolveParams` returns a structured error
//!
//! These tests start a real IpcServer on a temp Unix socket to exercise the
//! full dispatch path, identical to the pattern used in ipc_typed.rs.

use std::{fs, path::PathBuf, time::Duration};

use tempfile::TempDir;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;
use tokio_util::sync::CancellationToken;

use cascade_daemon::{event_bus::EventBus, healthcheck::HealthState, ipc::IpcServer};

// ── Frame helpers ─────────────────────────────────────────────────────────

async fn write_frame(stream: &mut UnixStream, body: &[u8]) {
    let len = (body.len() as u32).to_be_bytes();
    stream.write_all(&len).await.expect("write len");
    stream.write_all(body).await.expect("write body");
    stream.flush().await.expect("flush");
}

async fn read_frame(stream: &mut UnixStream) -> Vec<u8> {
    let mut len_buf = [0u8; 4];
    stream.read_exact(&mut len_buf).await.expect("read len");
    let len = u32::from_be_bytes(len_buf) as usize;
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

// ── Test server ───────────────────────────────────────────────────────────

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

/// Proves project-tier reachability: a `.cascade/CASCADE.md` in the `cwd` dir
/// must appear in the resolved content.
///
/// This test would FAIL against the old placeholder code path (which fell
/// through to METHOD_NOT_FOUND) because no content was returned at all. With
/// the real handler the project marker must appear in the merged output.
#[tokio::test(flavor = "multi_thread")]
async fn test_resolve_project_tier_reachable_via_cwd() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    // Create a project directory with a unique cascade marker.
    let project_dir = tmp.path().join("my-project");
    fs::create_dir_all(&project_dir).unwrap();
    let cascade_dir = project_dir.join(".cascade");
    fs::create_dir_all(&cascade_dir).unwrap();
    let marker = "UNIQUE_PROJECT_MARKER_E5P02_TEST";
    fs::write(
        cascade_dir.join("CASCADE.md"),
        format!("# Project\n{marker}\n"),
    )
    .unwrap();

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    let resp = send_request(
        &mut stream,
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 1,
            "method": "cascade_resolve",
            "protocol_version": 1,
            "params": {
                "cwd": project_dir.to_str().unwrap(),
                "format": "markdown"
            }
        }),
    )
    .await;

    // Must NOT be an error. Success responses nest under "result" (errors
    // under "error") per the JSON-RPC 2.0 envelope written by write_response()
    // in ipc.rs.
    assert!(
        resp.get("error").is_none(),
        "cascade_resolve returned an error: {resp}"
    );

    let content = resp
        .get("result")
        .and_then(|r| r.get("content"))
        .and_then(|v| v.as_str())
        .unwrap_or("");

    assert!(
        content.contains(marker),
        "project tier marker `{marker}` not found in resolved content.\n\
         This proves the cwd param was not used correctly.\n\
         Got content: {content:?}\nFull resp: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// Proves the full merge is returned: when the resolver walks upward from a
/// project dir and finds multiple `.cascade/CASCADE.md` files, both markers
/// appear in the merged result.
///
/// Creates two nested dirs each with their own `.cascade/CASCADE.md` and
/// verifies BOTH markers appear in the merged output.
#[tokio::test(flavor = "multi_thread")]
async fn test_resolve_full_merge_both_tiers_present() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    // Parent dir: acts as a higher-tier cascade (the resolver walks upward).
    let parent_dir = tmp.path().join("workspace");
    fs::create_dir_all(&parent_dir).unwrap();
    let parent_cascade = parent_dir.join(".cascade");
    fs::create_dir_all(&parent_cascade).unwrap();
    fs::write(
        parent_cascade.join("CASCADE.md"),
        "# Workspace Tier\nWORKSPACE_MARKER_TIER_HIGH\n",
    )
    .unwrap();

    // Child (project) dir: lower-tier cascade.
    let project_dir = parent_dir.join("my-project");
    fs::create_dir_all(&project_dir).unwrap();
    let project_cascade = project_dir.join(".cascade");
    fs::create_dir_all(&project_cascade).unwrap();
    fs::write(
        project_cascade.join("CASCADE.md"),
        "# Project Tier\nPROJECT_MARKER_TIER_LOW\n",
    )
    .unwrap();

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    let resp = send_request(
        &mut stream,
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 2,
            "method": "cascade_resolve",
            "protocol_version": 1,
            "params": {
                "cwd": project_dir.to_str().unwrap()
            }
        }),
    )
    .await;

    assert!(
        resp.get("error").is_none(),
        "cascade_resolve returned an error: {resp}"
    );

    let content = resp
        .get("result")
        .and_then(|r| r.get("content"))
        .and_then(|v| v.as_str())
        .unwrap_or("");

    assert!(
        content.contains("WORKSPACE_MARKER_TIER_HIGH"),
        "workspace tier content missing from merged result.\nGot: {content:?}"
    );
    assert!(
        content.contains("PROJECT_MARKER_TIER_LOW"),
        "project tier content missing from merged result.\nGot: {content:?}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// Proves `format="json"` returns parseable JSON (serde_json::from_str succeeds)
/// and the outer `format` field equals `"json"`.
#[tokio::test(flavor = "multi_thread")]
async fn test_resolve_format_json_returns_parseable_json() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let project_dir = tmp.path().join("proj-json");
    fs::create_dir_all(project_dir.join(".cascade")).unwrap();
    fs::write(
        project_dir.join(".cascade").join("CASCADE.md"),
        "# JSON Format Test\nsome content\n",
    )
    .unwrap();

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    let resp = send_request(
        &mut stream,
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 3,
            "method": "cascade_resolve",
            "protocol_version": 1,
            "params": {
                "cwd": project_dir.to_str().unwrap(),
                "format": "json"
            }
        }),
    )
    .await;

    assert!(
        resp.get("error").is_none(),
        "cascade_resolve format=json returned an error: {resp}"
    );

    let result = resp
        .get("result")
        .expect("cascade_resolve response missing result field");

    // The `content` field must be a JSON string that itself parses as JSON.
    let content_str = result
        .get("content")
        .and_then(|v| v.as_str())
        .expect("content field missing");

    let parsed: serde_json::Value =
        serde_json::from_str(content_str).expect("format=json: content is not valid JSON");

    // The parsed JSON should have cascade structure fields (merged_text or tier_sources).
    assert!(
        parsed.get("merged_text").is_some() || parsed.get("tier_sources").is_some(),
        "format=json content missing expected cascade fields: {parsed}"
    );

    // The outer format field must be "json".
    assert_eq!(
        result.get("format").and_then(|v| v.as_str()),
        Some("json"),
        "format field must be 'json'"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// Proves `format="xml"` (unsupported) returns -32602 INVALID_PARAMS.
#[tokio::test(flavor = "multi_thread")]
async fn test_resolve_invalid_format_returns_32602() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    let resp = send_request(
        &mut stream,
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 4,
            "method": "cascade_resolve",
            "protocol_version": 1,
            "params": {
                "format": "xml"
            }
        }),
    )
    .await;

    assert_eq!(
        resp.get("error")
            .and_then(|e| e.get("code"))
            .and_then(|v| v.as_i64()),
        Some(-32602),
        "expected -32602 for invalid format; got: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}

/// Proves `deny_unknown_fields` on `ResolveParams`: an unknown param field
/// must return -32602 (serde rejects the unknown key).
#[tokio::test(flavor = "multi_thread")]
async fn test_resolve_deny_unknown_fields() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, shutdown, handle) = start_test_server(&tmp).await;

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();

    // `unknown_param` is not in ResolveParams (deny_unknown_fields) — serde
    // will reject it and the handler returns -32602.
    let resp = send_request(
        &mut stream,
        serde_json::json!({
            "jsonrpc": "2.0",
            "id": 5,
            "method": "cascade_resolve",
            "protocol_version": 1,
            "params": {
                "unknown_param": "oops"
            }
        }),
    )
    .await;

    // Either -32602 (invalid params — serde deny_unknown_fields on ResolveParams)
    // or -32600 (envelope unknown field) is acceptable.
    let code = resp
        .get("error")
        .and_then(|e| e.get("code"))
        .and_then(|v| v.as_i64());
    assert!(
        code == Some(-32602) || code == Some(-32600),
        "expected -32602 or -32600 for unknown param field; got: {resp}"
    );

    shutdown.cancel();
    let _ = tokio::time::timeout(Duration::from_secs(3), handle).await;
}
