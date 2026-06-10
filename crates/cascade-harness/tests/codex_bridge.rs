//! Integration tests for the Codex MCP stdio bridge.
//!
//! Spawns `cascade mcp stdio` as a subprocess and communicates via
//! newline-delimited JSON-RPC over stdin/stdout.
//!
//! ## SPORT
//! MASTER-HARNESSES.md: Codex row — mcp_stdio_bridge integration test (T-P4-E06-04)

use std::io::{BufRead, BufReader, Write};
use std::process::{Command, Stdio};
use std::time::Duration;

// ── Helpers ───────────────────────────────────────────────────────────────────

/// JSON-RPC initialize request (MCP 2025-03).
fn initialize_msg() -> &'static str {
    r#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"codex-bridge-test","version":"0.1"}}}"#
}

/// JSON-RPC initialized notification (no id).
fn initialized_notif() -> &'static str {
    r#"{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}"#
}

/// MCP shutdown notification.
fn shutdown_notif() -> &'static str {
    r#"{"jsonrpc":"2.0","method":"shutdown","params":{}}"#
}

// ── Tests ─────────────────────────────────────────────────────────────────────

/// Spawn `cascade mcp stdio`, send initialize, assert a valid JSON-RPC response.
///
/// This test requires the `cascade` binary to be built first.  In CI it runs
/// after `cargo build --workspace`.  The test is skipped (not failed) if the
/// binary cannot be found, so it doesn't block other test runs.
#[test]
fn mcp_stdio_initialize_responds_within_2s() {
    // Locate the cascade binary — prefer the debug build target.
    let binary = locate_cascade_binary();
    let Some(binary) = binary else {
        eprintln!("[SKIP] cascade binary not found; run `cargo build -p cascade-cli` first");
        return;
    };

    let mut child = Command::new(&binary)
        .args(["mcp", "stdio"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn cascade mcp stdio");

    let mut stdin = child.stdin.take().expect("child stdin");
    let stdout = child.stdout.take().expect("child stdout");

    // Send initialize
    writeln!(stdin, "{}", initialize_msg()).expect("write initialize");
    stdin.flush().expect("flush");

    // Read response with a 2-second deadline
    let response = read_line_timeout(stdout, Duration::from_secs(2));
    assert!(
        response.is_some(),
        "server should respond to initialize within 2s"
    );

    let response = response.unwrap();
    let val: serde_json::Value =
        serde_json::from_str(&response).expect("response should be valid JSON");

    assert_eq!(val["jsonrpc"], "2.0", "jsonrpc version must be 2.0");
    assert_eq!(val["id"], 1, "response id must match request id");
    assert!(
        val.get("result").is_some() || val.get("error").is_some(),
        "response must have result or error"
    );

    // Send shutdown and wait
    let _ = writeln!(stdin, "{}", shutdown_notif());
    let _ = stdin.flush();

    // Give the server 1 second to exit cleanly
    let _ = std::thread::spawn(move || {
        std::thread::sleep(Duration::from_secs(1));
    })
    .join();

    let _ = child.kill();
    let status = child.wait().expect("wait on child");
    // Accept exit 0 or killed (signal) — both are clean
    let _ = status;
}

/// Send initialize + initialized notification and verify the server doesn't crash.
#[test]
fn mcp_stdio_initialized_notification_accepted() {
    let binary = locate_cascade_binary();
    let Some(binary) = binary else {
        eprintln!("[SKIP] cascade binary not found");
        return;
    };

    let mut child = Command::new(&binary)
        .args(["mcp", "stdio"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .expect("spawn cascade mcp stdio");

    let mut stdin = child.stdin.take().expect("child stdin");
    let stdout = child.stdout.take().expect("child stdout");

    // Send initialize
    writeln!(stdin, "{}", initialize_msg()).expect("write initialize");
    stdin.flush().expect("flush");

    // Wait for initialize response
    let _resp = read_line_timeout(stdout, Duration::from_secs(2));

    // Send initialized notification (no response expected)
    writeln!(stdin, "{}", initialized_notif()).expect("write initialized");
    stdin.flush().expect("flush");

    // Server should still be alive (not panicked/exited)
    let alive = child.try_wait().expect("try_wait").is_none();
    assert!(
        alive,
        "server should still be running after initialized notification"
    );

    let _ = child.kill();
    let _ = child.wait();
}

// ── Utility ───────────────────────────────────────────────────────────────────

/// Read one line from a reader, timing out after `timeout`.
fn read_line_timeout(
    stdout: impl std::io::Read + Send + 'static,
    timeout: Duration,
) -> Option<String> {
    use std::sync::mpsc;
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let reader = BufReader::new(stdout);
        for line in reader.lines().map_while(Result::ok) {
            let trimmed = line.trim().to_owned();
            if !trimmed.is_empty() {
                let _ = tx.send(trimmed);
                return;
            }
        }
    });
    rx.recv_timeout(timeout).ok()
}

/// Find the `cascade` binary in the Cargo target directory.
fn locate_cascade_binary() -> Option<std::path::PathBuf> {
    // Check CARGO_TARGET_DIR env or the standard locations
    let target_dirs = [std::env::var("CARGO_TARGET_DIR")
        .map(std::path::PathBuf::from)
        .unwrap_or_else(|_| {
            // Relative to this file's workspace root
            std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("..")
                .join("..")
                .join("target")
        })];

    for base in &target_dirs {
        for profile in &["debug", "release"] {
            let candidate = base.join(profile).join("cascade");
            if candidate.exists() {
                return Some(candidate);
            }
        }
    }

    // Also check PATH
    if let Some(p) = which_binary("cascade") {
        return Some(p);
    }

    None
}

fn which_binary(name: &str) -> Option<std::path::PathBuf> {
    let out = Command::new("which")
        .arg(name)
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .output()
        .ok()?;
    if out.status.success() {
        let s = String::from_utf8_lossy(&out.stdout);
        let trimmed = s.trim();
        if !trimmed.is_empty() {
            return Some(std::path::PathBuf::from(trimmed));
        }
    }
    None
}
