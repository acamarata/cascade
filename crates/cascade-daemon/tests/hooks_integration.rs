//! Integration tests for cascade daemon hooks.
//!
//! Purpose: test end-to-end hook firing behavior when daemon events occur.
//! Each test sets up a temp config with specific hook definitions, triggers
//! an event, and verifies the hook action executed.
//!
//! Helpers:
//!   - `spawn_daemon_with_hooks` — start daemon with a custom hooks config
//!   - `wait_for_log_line`        — grep the daemon logs for a specific line
//!
//! Tests:
//!   1. hook_fires_on_daemon_start — LogMessage hook fires on DaemonStart
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — hooks integration (T-P2-E02-27)

use std::{
    fs,
    path::{Path, PathBuf},
    process::{Child, Command},
    thread,
    time::{Duration, Instant},
};

use assert_cmd::cargo::cargo_bin;
use tempfile::TempDir;

// ── Helpers ────────────────────────────────────────────────────────────────────

/// Spawn the daemon with a custom hooks config.
///
/// Accepts a `hooks_toml_section` string (the full [[hooks]] array syntax)
/// and appends it to the test config.
///
/// Returns the Child process and the path to cascade_dir.
fn spawn_daemon_with_hooks(tmpdir: &TempDir, hooks_toml_section: &str) -> (Child, PathBuf) {
    let cascade_dir = tmpdir.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).expect("create .cascade dir");

    // Read the base test config.
    let fixture = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/test-config.toml");
    let mut config_content = fs::read_to_string(&fixture).expect("read test-config.toml fixture");

    // Append the hooks section.
    config_content.push('\n');
    config_content.push_str(hooks_toml_section);

    // Write the customized config.
    fs::write(cascade_dir.join("config.toml"), config_content)
        .expect("write customized config.toml");

    let bin = cargo_bin("cascaded");
    let child = Command::new(&bin)
        .env("HOME", tmpdir.path())
        .env_remove("CASCADE_OTEL_ENDPOINT")
        // Keep RUST_LOG at info level so we can see our LogMessage hooks.
        .env("RUST_LOG", "info")
        .spawn()
        .unwrap_or_else(|e| panic!("failed to spawn cascaded at {}: {e}", bin.display()));

    (child, cascade_dir)
}

/// Poll the daemon logs for a specific line.
///
/// Reads `HOME/.cascade/logs/cascaded.log` (the path the daemon writes to,
/// derived from `dirs::home_dir()` which resolves to the test's HOME tmpdir),
/// searches for a line containing `search_text`, and returns true if found
/// within `timeout_ms`.
fn wait_for_log_line(cascade_dir: &Path, search_text: &str, timeout_ms: u64) -> bool {
    // cascade_dir is tmpdir/.cascade; the daemon logs to tmpdir/.cascade/logs/cascaded.log
    // WHY daemon.log: cascade-daemon/src/log.rs defines LOG_FILE = "daemon.log".
    let log_path = cascade_dir.join("logs").join("daemon.log");
    let deadline = Instant::now() + Duration::from_millis(timeout_ms);

    while Instant::now() < deadline {
        if let Ok(content) = fs::read_to_string(&log_path) {
            if content.contains(search_text) {
                return true;
            }
        }
        thread::sleep(Duration::from_millis(100));
    }

    false
}

// ── Tests ──────────────────────────────────────────────────────────────────────

/// Test: LogMessage hook fires when daemon starts.
///
/// Sets up a LogMessage hook for the DaemonStart event and verifies the
/// hook fires by checking the daemon log for the expected message.
#[test]
fn hook_fires_on_daemon_start() {
    let tmpdir = TempDir::new().expect("tmpdir");

    // Define a LogMessage hook that fires on DaemonStart.
    // Note: HookAction uses snake_case serde rename, so type = "log_message".
    let hooks_config = r#"
[[hooks]]
name = "log-daemon-started"
event = "DaemonStart"
[hooks.action]
type = "log_message"
level = "info"
message = "Cascade daemon started successfully, hooks are working!"
"#;

    let (mut child, cascade_dir) = spawn_daemon_with_hooks(&tmpdir, hooks_config);

    // Wait for the log message to appear. The hook should fire shortly after
    // daemon startup completes.
    let found = wait_for_log_line(
        &cascade_dir,
        "Cascade daemon started successfully, hooks are working!",
        2000,
    );

    // Clean up: send SIGTERM to gracefully shut down the daemon.
    #[cfg(unix)]
    {
        let _ = Command::new("kill")
            .args(["-TERM", &child.id().to_string()])
            .status();
    }
    #[cfg(not(unix))]
    let _ = child.kill();

    let _ = child.wait();

    assert!(
        found,
        "log_message hook for DaemonStart should fire within 2 s and write the expected message to the log file"
    );
}
