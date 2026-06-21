//! End-to-end IPC wire test for ReadQuotaStore (T-P2-E02-31).
//!
//! Spawns the real `cascaded` binary in an isolated tmpdir, writes a fixture
//! `quota-store.json`, sends `{"method":"read_quota_store"}` over the Unix
//! socket using the length-prefixed framing protocol, and asserts the
//! response deserialises to a `QuotaStore` containing the expected account.
//!
//! Follows the same pattern as the existing `integration.rs` binary tests.
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — quota_store_wire test (T-P2-E02-31)

use std::{
    fs,
    io::{Read, Write},
    os::unix::net::UnixStream,
    path::{Path, PathBuf},
    process::{Child, Command},
    thread,
    time::{Duration, Instant},
};

use assert_cmd::cargo::cargo_bin;
use cascade_core::quota_store::{write_quota_store, QuotaStore, QUOTA_STORE_SCHEMA_VERSION};
use cascade_types::quota_store::{AccountEntry, ModelUsage, PROVIDER_CLAUDE_MAX};
use std::collections::HashMap;
use tempfile::TempDir;

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Spawn the `cascaded` binary in an isolated tmpdir with the test config.
///
/// Sets `HOME=tmpdir` and copies the reduced-interval test config so the daemon
/// starts up quickly. Returns the child process and the `.cascade` dir path.
fn spawn_daemon(tmpdir: &TempDir) -> (Child, PathBuf) {
    let cascade_dir = tmpdir.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).expect("create .cascade dir");

    let fixture = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/test-config.toml");
    fs::copy(&fixture, cascade_dir.join("config.toml")).expect("copy test-config.toml");

    let bin = cargo_bin("cascaded");
    let child = Command::new(&bin)
        .env("HOME", tmpdir.path())
        .env_remove("CASCADE_OTEL_ENDPOINT")
        .env("RUST_LOG", "error")
        .spawn()
        .unwrap_or_else(|e| panic!("failed to spawn cascaded: {e}"));

    (child, cascade_dir)
}

/// Poll until `path` exists or `timeout` elapses, returning true when found.
fn wait_for_file(path: &Path, timeout: Duration) -> bool {
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if path.exists() {
            return true;
        }
        thread::sleep(Duration::from_millis(50));
    }
    false
}

/// Send one length-prefixed JSON frame over a `UnixStream` and read the
/// length-prefixed JSON response.
fn ipc_round_trip(stream: &mut UnixStream, request: &serde_json::Value) -> serde_json::Value {
    let body = serde_json::to_vec(request).expect("serialise request");
    let len = (body.len() as u32).to_le_bytes();
    stream.write_all(&len).expect("write len");
    stream.write_all(&body).expect("write body");
    stream.flush().expect("flush");

    let mut len_buf = [0u8; 4];
    stream.read_exact(&mut len_buf).expect("read len");
    let resp_len = u32::from_le_bytes(len_buf) as usize;

    let mut resp_body = vec![0u8; resp_len];
    stream.read_exact(&mut resp_body).expect("read body");

    serde_json::from_slice(&resp_body).expect("deserialise response")
}

/// Minimal `QuotaStore` fixture with one account entry.
fn make_quota_store() -> QuotaStore {
    let mut model_usage = HashMap::new();
    model_usage.insert(
        "claude-sonnet-4-6".to_string(),
        ModelUsage {
            used: 100,
            limit: Some(1000),
            reset_at: None,
            pct_used: Some(10.0),
            cost_usd: None, // v2 field
        },
    );

    QuotaStore {
        schema_version: QUOTA_STORE_SCHEMA_VERSION,
        updated_at: "2026-01-01T00:00:00Z".to_string(),
        accounts: vec![AccountEntry {
            account_id: "test-acct-1".to_string(),
            harness: "cc".to_string(),
            provider: PROVIDER_CLAUDE_MAX.to_string(), // v2 field
            models: model_usage,
            week_total_used: 100,
            month_total_used: 200,
            last_polled: "2026-01-01T00:00:00Z".to_string(),
            rate_windows: vec![], // v2 field
        }],
        week_totals: HashMap::new(),
        month_totals: HashMap::new(),
        rolling_history: vec![],
    }
}

// ── Test ──────────────────────────────────────────────────────────────────────

/// End-to-end ReadQuotaStore IPC wire test.
///
/// Verifies that the `ReadQuotaStore` variant is wired up: the daemon reads
/// the fixture quota-store.json and returns it serialised as JSON with the
/// expected `schema_version` and `accounts` fields.
#[test]
fn quota_store_wire() {
    let tmpdir = TempDir::new().expect("create temp home");
    let (mut child, cascade_dir) = spawn_daemon(&tmpdir);

    // Write fixture quota-store.json AFTER spawning so it exists when the
    // daemon handles the request (the daemon does not cache it on startup).
    let quota_path = cascade_dir.join("quota-store.json");
    write_quota_store(&quota_path, &make_quota_store()).expect("write fixture quota store");

    // Wait for the IPC socket to appear (daemon is ready to accept connections).
    let socket_path = cascade_dir.join("daemon.sock");
    let ready = wait_for_file(&socket_path, Duration::from_secs(10));
    if !ready {
        let _ = child.kill();
        panic!("IPC socket did not appear within 10 s");
    }

    // Connect and send ReadQuotaStore.
    let mut stream = UnixStream::connect(&socket_path).expect("connect to IPC socket");
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .expect("set read timeout");

    let response = ipc_round_trip(
        &mut stream,
        &serde_json::json!({ "method": "read_quota_store" }),
    );

    // Shut down daemon before asserting so it does not linger on failure.
    let _ = child.kill();
    let _ = child.wait();

    // Assert the response contains the expected QuotaStore fields.
    assert!(
        response.get("schema_version").is_some(),
        "response must contain schema_version; got: {response}"
    );
    assert!(
        response.get("accounts").is_some(),
        "response must contain accounts; got: {response}"
    );
    let accounts = response["accounts"]
        .as_array()
        .expect("accounts is an array");
    assert_eq!(accounts.len(), 1, "expected exactly one account");
    assert_eq!(
        accounts[0]["account_id"], "test-acct-1",
        "account_id must match fixture"
    );
}
