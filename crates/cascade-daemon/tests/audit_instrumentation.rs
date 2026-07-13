//! Integration tests for audit instrumentation of privileged IPC dispatch hooks.
//!
//! Purpose: verifies that the five privileged typed methods (gci_write,
//!   symlink_create, symlink_delete, cascade_resolve, key_rotation) emit an
//!   `audit::record()` call when routed through `try_typed_dispatch`.
//!   Resolves P2 residue E07-17.
//!
//! Implementation note: four of the five ops (gci_write, symlink_create,
//!   symlink_delete, key_rotation) still return METHOD_NOT_FOUND (-32601) —
//!   their real handlers are pending in later epics.  `cascade_resolve` now
//!   has a real handler (E-P5-02) that returns a success response; its audit
//!   entry is emitted after the successful operation (write-then-audit ordering).
//!
//! Approach: spin up a real IpcServer against a temp dir that has a real audit
//!   log path; send each of the five method names via the socket; then open the
//!   audit log directly and verify (a) entries were appended and (b)
//!   `AuditLog::verify_chain()` returns Ok with no violations.
//!
//! The audit log is initialised inside the daemon's main() in production; here
//!   we initialise it directly using `cascade_daemon::audit::init_for_test` —
//!   see the conditional re-export below.  Because `audit::init` uses a
//!   `OnceLock`, we use a fresh process-level audit path per test run by setting
//!   an environment variable that the IpcServer constructor reads when the
//!   `CASCADE_AUDIT_LOG` env var is set.  In practice for this test we initialise
//!   the global directly before the server starts.
//!
//! SPORT: MASTER-ENDPOINTS.md — audit=hook annotation for gci_write,
//!        symlink_create, symlink_delete, cascade_resolve, key_rotation.

use std::{fs, path::PathBuf, time::Duration};

use cascade_audit::{AuditLog, AuditOp};
use serial_test::serial;
use tempfile::TempDir;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::UnixStream;
use tokio_util::sync::CancellationToken;

use cascade_daemon::{audit, event_bus::EventBus, healthcheck::HealthState, ipc::IpcServer};

// ── Frame helpers ─────────────────────────────────────────────────────────

async fn write_frame(stream: &mut UnixStream, body: &[u8]) {
    // Big-endian length prefix — matches the canonical cascade-types FrameCodec
    // and crates/cascade-daemon/src/ipc.rs (handle_connection reads/writes BE).
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

// ── Test server factory ───────────────────────────────────────────────────

/// Read the IPC auth token the daemon wrote to `<config_dir>/ipc_token`
/// (crates/cascade-daemon/src/ipc.rs — `IpcServer::new` generates and
/// persists it there; every connection must echo it back per the
/// `{"auth": <token>, "rpc": <jsonrpc>}` envelope, added in 5f75f2f
/// "auth-gate daemon write..." — this test predates that commit and was
/// never updated, so it always got AUTH_FAILED before reaching dispatch).
fn read_ipc_token(config_dir: &std::path::Path) -> String {
    fs::read_to_string(config_dir.join("ipc_token")).expect("read ipc_token")
}

/// Spin up an IpcServer in a temp dir and initialise the global audit log in
/// that same dir.  Returns (socket_path, audit_log_path, auth_token, shutdown_token, handle).
///
/// audit::init uses a process-global OnceLock — first call wins.  We call init
/// here (it is a no-op if already set) then read the active path back via
/// audit::active_log_path() so assertions always target the correct file
/// regardless of test execution order.
async fn start_test_server_with_audit(
    tmp: &TempDir,
) -> (
    PathBuf,
    PathBuf,
    String,
    CancellationToken,
    tokio::task::JoinHandle<()>,
) {
    let config_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&config_dir).unwrap();

    let candidate_path = config_dir.join("audit.log");
    // Best-effort init; OnceLock means first call wins, later calls are no-ops.
    let _ = audit::init(&candidate_path);
    // Always use whichever path was actually registered.
    let audit_path = audit::active_log_path()
        .expect("audit OnceLock must be set after init")
        .to_path_buf();

    // If the OnceLock was already set by a prior test whose TempDir was dropped,
    // the parent directory may no longer exist.  Recreate it so that the audit
    // log can be written and read by this test.
    if let Some(parent) = audit_path.parent() {
        fs::create_dir_all(parent).unwrap();
    }
    // Ensure the file itself exists (empty is fine) so AuditLog::open succeeds.
    if !audit_path.exists() {
        fs::write(&audit_path, b"").unwrap();
    }

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

    let auth_token = read_ipc_token(&config_dir);

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

    (socket_path, audit_path, auth_token, shutdown, handle)
}

// ── Helpers ───────────────────────────────────────────────────────────────

/// Build an authenticated JSON-RPC request envelope for a privileged method.
/// The connection handler requires `{"auth": <token>, "rpc": <jsonrpc>}` for
/// every request — see `read_ipc_token`'s doc comment.
fn privileged_request(method: &str, token: &str) -> serde_json::Value {
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

// ── Tests ─────────────────────────────────────────────────────────────────

/// Sending each of the five privileged method names through the IPC socket
/// should (a) emit an audit entry for each and (b) verify_chain() returns Ok.
///
/// Four methods (gci_write, symlink_create, symlink_delete, key_rotation)
/// still return METHOD_NOT_FOUND (-32601) — their handlers are pending.
/// `cascade_resolve` has a real handler (E-P5-02) and returns a success
/// response; it is still audited (write-then-audit ordering).
// multi_thread: IpcServer::new initialises the RAG reranker, which uses
// tokio::task::block_in_place — illegal on the default current-thread test
// runtime (and the real daemon runs multi-threaded).
#[tokio::test(flavor = "multi_thread")]
#[serial(global_env)]
async fn privileged_methods_emit_audit_entries_and_chain_verifies() {
    let tmp = TempDir::new().unwrap();
    let (socket_path, audit_path, token, shutdown, handle) =
        start_test_server_with_audit(&tmp).await;

    let methods = [
        ("gci_write", AuditOp::GciWrite),
        ("symlink_create", AuditOp::SymlinkCreate),
        ("symlink_delete", AuditOp::SymlinkDelete),
        ("cascade_resolve", AuditOp::CascadeResolve),
        ("key_rotation", AuditOp::KeyRotation),
    ];

    // Count entries before sending any requests.
    // AuditLog has no read_entries() — count non-empty lines in the JSONL file directly.
    let entries_before = std::fs::read_to_string(&audit_path)
        .unwrap_or_default()
        .lines()
        .filter(|l| !l.trim().is_empty())
        .count();

    // Send each privileged method.
    // Four still-pending methods must return METHOD_NOT_FOUND (-32601).
    // `cascade_resolve` now has a real handler and must NOT return -32601.
    let mut stream = UnixStream::connect(&socket_path).await.unwrap();
    for (method, _expected_op) in &methods {
        let resp = send_request(&mut stream, privileged_request(method, &token)).await;
        // Errors are nested under "error": {"code", "message"} per the JSON-RPC
        // 2.0 envelope written by write_response() in ipc.rs.
        let code = resp
            .get("error")
            .and_then(|e| e.get("code"))
            .and_then(|c| c.as_i64());
        if *method == "cascade_resolve" {
            // Real handler: must succeed (no error code).
            assert!(
                code.is_none(),
                "cascade_resolve has a real handler and must NOT return an error; got: {resp}"
            );
        } else {
            // Pending handler: must return METHOD_NOT_FOUND.
            assert_eq!(
                code,
                Some(-32601),
                "method {method} should return METHOD_NOT_FOUND (-32601), got: {resp}"
            );
        }
    }
    drop(stream);

    // Give the server a moment to flush audit writes.
    tokio::time::sleep(Duration::from_millis(50)).await;

    // Shut down.
    shutdown.cancel();
    handle.await.ok();

    // Verify the audit log grew by at least five entries.
    // Count lines directly; keep log_after for verify_chain() below.
    let log_after = AuditLog::open(&audit_path).expect("open audit log after");
    let entries_after = std::fs::read_to_string(&audit_path)
        .unwrap_or_default()
        .lines()
        .filter(|l| !l.trim().is_empty())
        .count();
    let new_entries = entries_after.saturating_sub(entries_before);
    assert!(
        new_entries >= methods.len(),
        "expected at least {} new audit entries (one per privileged method), got {new_entries}",
        methods.len()
    );

    // Chain integrity must be clean.
    let violations = log_after.verify_chain().expect("verify_chain");
    assert!(
        violations.is_empty(),
        "audit chain has violations after privileged method dispatch: {violations:?}"
    );
}

/// Non-privileged typed methods must NOT produce audit entries.
/// Sending an unknown method name should not grow the audit log.
#[tokio::test(flavor = "multi_thread")]
#[serial(global_env)]
async fn non_privileged_methods_do_not_emit_audit_entries() {
    // Use a second temp dir — OnceLock means this test reuses the global from
    // the other test if run in the same process; that is acceptable because we
    // only assert the count does NOT change, and we read entries before/after.
    let tmp = TempDir::new().unwrap();
    let config_dir = tmp.path().join(".cascade");
    fs::create_dir_all(&config_dir).unwrap();
    let candidate_path = config_dir.join("audit.log");

    // Best-effort init; always read the active path back so assertions target
    // the correct file regardless of which test ran first.
    let _ = audit::init(&candidate_path);
    let audit_path = audit::active_log_path()
        .expect("audit OnceLock must be set after init")
        .to_path_buf();

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
    let socket_path = config_dir.join("daemon.sock");
    let shutdown = CancellationToken::new();
    let shutdown_clone = shutdown.clone();
    let handle = tokio::spawn(async move {
        ipc.serve(shutdown_clone).await.ok();
    });
    for _ in 0..100 {
        if socket_path.exists() {
            break;
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert!(socket_path.exists(), "socket did not appear");

    // Count entries before the request — read JSONL lines directly.
    let entries_before = std::fs::read_to_string(&audit_path)
        .unwrap_or_default()
        .lines()
        .filter(|l| !l.trim().is_empty())
        .count();

    let mut stream = UnixStream::connect(&socket_path).await.unwrap();
    let resp = send_request(
        &mut stream,
        serde_json::json!({
            "auth": token,
            "rpc": {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "unknown_nonprivileged_method",
                "params": {},
                "protocol_version": 1
            }
        }),
    )
    .await;
    // Errors are nested under "error": {"code", "message"} per the JSON-RPC
    // 2.0 envelope written by write_response() in ipc.rs.
    let code = resp
        .get("error")
        .and_then(|e| e.get("code"))
        .and_then(|c| c.as_i64())
        .unwrap_or(0);
    assert_eq!(
        code, -32601,
        "unknown method must return -32601, got: {resp}"
    );
    drop(stream);

    tokio::time::sleep(Duration::from_millis(50)).await;
    shutdown.cancel();
    handle.await.ok();

    let entries_after = std::fs::read_to_string(&audit_path)
        .unwrap_or_default()
        .lines()
        .filter(|l| !l.trim().is_empty())
        .count();
    assert_eq!(
        entries_after, entries_before,
        "unknown non-privileged method must not produce audit entries"
    );
}
