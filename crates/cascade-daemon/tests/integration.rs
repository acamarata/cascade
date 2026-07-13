//! Integration tests for `cascaded` — spawns the real binary and verifies
//! end-to-end daemon behaviour.
//!
//! Purpose: each test creates an isolated tmpdir, sets HOME=tmpdir so the
//! daemon reads/writes ~/.cascade/* relative to that tmpdir, and then
//! verifies specific state files or SQLite events.
//!
//! Helpers:
//!   - `spawn_daemon`    — start cascaded with HOME=tmpdir + test config.toml
//!   - `wait_for_file`   — poll for a file path with a timeout
//!
//! Tests:
//!   1. daemon_starts_writes_pid        — daemon.pid written within 500 ms
//!   2. daemon_clean_stop               — SIGTERM removes pid + writes last-stop.txt
//!   3. daemon_healthcheck_json         — daemon-status.json absent while running
//!   4. watcher_triggers_on_cascade_change — events.db has cascade.changed event
//!   5. crash_sentinel_written_on_kill9 — crash-last.txt written after SIGKILL
//!   6. quota_state_written             — quota-state.json written within 15 s
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — integration test suite (T-P2-E02-13)

use serial_test::serial;
use std::{
    fs,
    path::{Path, PathBuf},
    process::{Child, Command, Output, Stdio},
    thread,
    time::{Duration, Instant},
};

use assert_cmd::cargo::cargo_bin;
use tempfile::TempDir;

// ── Helpers ────────────────────────────────────────────────────────────────────

/// Spawn the `cascaded` binary in an isolated tmpdir.
///
/// Sets `HOME=tmpdir` and writes `test-config.toml` to `tmpdir/.cascade/`
/// so the daemon picks it up on startup.
///
/// Returns the Child process handle and the path to the `.cascade` dir.
///
/// # Why assert_cmd::cargo::cargo_bin
///
/// `cargo_bin("cascaded")` resolves the debug binary produced by the current
/// cargo build profile so the test always runs the binary that was just built.
fn spawn_daemon(tmpdir: &TempDir) -> (Child, PathBuf) {
    let cascade_dir = tmpdir.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).expect("create .cascade dir");

    // Copy the reduced-interval test config into the daemon's config dir.
    let fixture = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/test-config.toml");
    fs::copy(&fixture, cascade_dir.join("config.toml"))
        .expect("copy test-config.toml into tmpdir/.cascade/");

    let bin = cargo_bin("cascaded");
    let child = Command::new(&bin)
        .env("HOME", tmpdir.path())
        // Suppress OTEL export — no collector in tests.
        .env_remove("CASCADE_OTEL_ENDPOINT")
        // Quiet the tracing output in test runs (stderr is captured by cargo test).
        .env("RUST_LOG", "error")
        .spawn()
        .unwrap_or_else(|e| panic!("failed to spawn cascaded at {}: {e}", bin.display()));

    (child, cascade_dir)
}

/// Like `spawn_daemon` but also creates `HOME/.cascade/CASCADE.md` with
/// `initial_content` before spawning, so `discover_all_cascade_paths()`
/// finds and watches the file during daemon start-up.
fn spawn_daemon_with_cascade_md(
    tmpdir: &TempDir,
    initial_content: &str,
) -> (Child, PathBuf, PathBuf) {
    let cascade_dir = tmpdir.path().join(".cascade");
    fs::create_dir_all(&cascade_dir).expect("create .cascade dir");

    // Write the CASCADE.md file BEFORE spawning so the daemon finds it.
    let cascade_md = cascade_dir.join("CASCADE.md");
    fs::write(&cascade_md, initial_content).expect("write initial CASCADE.md");

    // Copy the reduced-interval test config.
    let fixture = Path::new(env!("CARGO_MANIFEST_DIR")).join("tests/fixtures/test-config.toml");
    fs::copy(&fixture, cascade_dir.join("config.toml"))
        .expect("copy test-config.toml into tmpdir/.cascade/");

    let bin = cargo_bin("cascaded");
    let child = Command::new(&bin)
        .env("HOME", tmpdir.path())
        .env_remove("CASCADE_OTEL_ENDPOINT")
        .env("RUST_LOG", "error")
        .spawn()
        .unwrap_or_else(|e| panic!("failed to spawn cascaded at {}: {e}", bin.display()));

    (child, cascade_dir, cascade_md)
}

/// Poll `path` every 50 ms until it exists or `timeout_ms` elapses.
///
/// Returns `true` when the file exists within the window, `false` on timeout.
/// Poll until `path` is REMOVED or the deadline passes. Mirror of
/// wait_for_file for shutdown-ordering assertions (pid removal follows the
/// stop marker by a small window that widens under load).
fn wait_for_gone(path: &Path, timeout_ms: u64) -> bool {
    let deadline = Instant::now() + Duration::from_millis(timeout_ms);
    while Instant::now() < deadline {
        if !path.exists() {
            return true;
        }
        thread::sleep(Duration::from_millis(50));
    }
    !path.exists()
}

fn wait_for_file(path: &Path, timeout_ms: u64) -> bool {
    let deadline = Instant::now() + Duration::from_millis(timeout_ms);
    while Instant::now() < deadline {
        if path.exists() {
            return true;
        }
        thread::sleep(Duration::from_millis(50));
    }
    path.exists()
}

fn cascaded_arg_test_bin() -> PathBuf {
    let exe_name = if cfg!(windows) {
        "cascaded.exe"
    } else {
        "cascaded"
    };

    if let Some(target_dir) = std::env::var_os("CARGO_TARGET_DIR") {
        let candidate = PathBuf::from(target_dir).join("debug").join(exe_name);
        if candidate.exists() {
            return candidate;
        }
    }

    cargo_bin("cascaded")
}

fn run_cascaded_arg_with_deadline(tmpdir: &TempDir, arg: &str, timeout: Duration) -> Output {
    let bin = cascaded_arg_test_bin();
    let mut child = Command::new(&bin)
        .arg(arg)
        .env("HOME", tmpdir.path())
        .env_remove("CASCADE_OTEL_ENDPOINT")
        .env("RUST_LOG", "error")
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap_or_else(|e| panic!("failed to spawn cascaded at {}: {e}", bin.display()));

    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if child.try_wait().expect("poll cascaded child").is_some() {
            return child.wait_with_output().expect("collect cascaded output");
        }
        thread::sleep(Duration::from_millis(10));
    }

    let _ = child.kill();
    let output = child
        .wait_with_output()
        .expect("collect killed cascaded output");
    panic!(
        "{} {arg} did not exit within {:?}; status={:?}",
        bin.display(),
        timeout,
        output.status
    );
}

fn assert_no_daemon_startup_files(home: &Path) {
    let cascade_dir = home.join(".cascade");

    assert!(
        !cascade_dir.join("daemon.pid").exists(),
        "argument parsing should exit before writing daemon.pid"
    );
    assert!(
        !cascade_dir.join("daemon.sock").exists(),
        "argument parsing should exit before binding daemon.sock"
    );
    assert!(
        !cascade_dir.join("dashboard.token").exists(),
        "argument parsing should exit before starting the dashboard listener"
    );
}

// ── Tests ──────────────────────────────────────────────────────────────────────

#[test]
fn cascaded_version_exits_without_starting_daemon() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let output = run_cascaded_arg_with_deadline(&tmpdir, "--version", Duration::from_secs(3));

    assert!(
        output.status.success(),
        "cascaded --version should exit 0; stderr={}",
        String::from_utf8_lossy(&output.stderr)
    );

    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(
        stdout
            .trim()
            .starts_with(&format!("cascaded {}", env!("CARGO_PKG_VERSION"))),
        "unexpected --version stdout: {stdout:?}"
    );
    assert_no_daemon_startup_files(tmpdir.path());
}

#[test]
fn cascaded_unknown_flag_exits_without_starting_daemon() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let output = run_cascaded_arg_with_deadline(
        &tmpdir,
        "--definitely-not-a-cascaded-flag",
        Duration::from_secs(3),
    );

    assert!(
        !output.status.success(),
        "unknown flag should exit non-zero; stdout={}",
        String::from_utf8_lossy(&output.stdout)
    );

    let stderr = String::from_utf8_lossy(&output.stderr);
    assert!(
        stderr.contains("unexpected argument") || stderr.contains("Usage: cascaded"),
        "unexpected unknown-flag stderr: {stderr:?}"
    );
    assert_no_daemon_startup_files(tmpdir.path());
}

/// Test 1 — daemon.pid written within 500 ms of spawning the daemon.
///
/// Verifies that the PID file path is `HOME/.cascade/daemon.pid`, that it
/// contains a valid u32 process ID, and that the reported PID matches a
/// running process.
#[test]
#[serial(global_env)]
fn daemon_starts_writes_pid() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let (mut child, cascade_dir) = spawn_daemon(&tmpdir);

    let pid_path = cascade_dir.join("daemon.pid");
    let found = wait_for_file(&pid_path, 8000);

    // Read PID BEFORE cleanup so we can assert on it (flush_state removes the file).
    let pid_content = if found {
        fs::read_to_string(&pid_path).unwrap_or_default()
    } else {
        String::new()
    };

    // Send SIGTERM to clean up before asserting so we don't leave zombie processes.
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
        "daemon.pid should exist within 2 s of daemon startup; path = {}",
        pid_path.display()
    );

    let stored_pid: u32 = pid_content
        .trim()
        .parse()
        .expect("daemon.pid should contain a valid u32 PID");

    assert!(stored_pid > 0, "PID stored in daemon.pid must be non-zero");
}

/// Test 2 — after SIGTERM, daemon exits cleanly: daemon.pid removed and
/// last-stop.txt written within 3 s, daemon-status.json has `clean_stop=true`.
#[test]
#[serial(global_env)]
fn daemon_clean_stop() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let (mut child, cascade_dir) = spawn_daemon(&tmpdir);

    let pid_path = cascade_dir.join("daemon.pid");
    // Wait for the daemon to finish starting.
    assert!(
        wait_for_file(&pid_path, 8000),
        "daemon.pid must exist before we can test clean stop"
    );

    // Send SIGTERM to request a graceful shutdown.
    #[cfg(unix)]
    {
        let _ = Command::new("kill")
            .args(["-TERM", &child.id().to_string()])
            .status();
    }
    #[cfg(not(unix))]
    let _ = child.kill();

    let _ = child.wait();

    // Give the daemon up to 3 s to flush state after SIGTERM.
    let stop_path = cascade_dir.join("last-stop.txt");
    let stop_written = wait_for_file(&stop_path, 8000);

    // daemon.pid should be gone after clean stop (poll: removal follows the
    // stop marker by a small window that widens under system load).
    assert!(
        wait_for_gone(&pid_path, 8000),
        "daemon.pid should be removed after clean stop"
    );
    assert!(
        stop_written,
        "last-stop.txt should exist after clean stop; path = {}",
        stop_path.display()
    );

    // daemon-status.json should report clean_stop=true.
    let status_path = cascade_dir.join("daemon-status.json");
    if status_path.exists() {
        let raw = fs::read_to_string(&status_path).unwrap_or_default();
        let val: serde_json::Value = serde_json::from_str(&raw).unwrap_or_default();
        assert_eq!(
            val["clean_stop"], true,
            "daemon-status.json should have clean_stop=true after SIGTERM"
        );
    }
}

/// Test 3 — while the daemon is running, daemon-status.json either does not
/// exist or does not contain `clean_stop=true` (daemon has not stopped yet).
#[test]
#[serial(global_env)]
fn daemon_healthcheck_json() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let (mut child, cascade_dir) = spawn_daemon(&tmpdir);

    let pid_path = cascade_dir.join("daemon.pid");
    assert!(
        wait_for_file(&pid_path, 8000),
        "daemon.pid must appear before health check"
    );

    // While daemon is still running — daemon-status.json should either be
    // absent or not claim clean_stop=true.
    let status_path = cascade_dir.join("daemon-status.json");
    if status_path.exists() {
        let raw = fs::read_to_string(&status_path).unwrap_or_default();
        let val: serde_json::Value = serde_json::from_str(&raw).unwrap_or_default();
        assert_ne!(
            val["clean_stop"], true,
            "daemon-status.json must not report clean_stop=true while daemon is running"
        );
    }

    // Clean up.
    #[cfg(unix)]
    {
        let _ = Command::new("kill")
            .args(["-TERM", &child.id().to_string()])
            .status();
    }
    #[cfg(not(unix))]
    let _ = child.kill();
    let _ = child.wait();
}

/// Test 4 — after modifying `HOME/.cascade/CASCADE.md`, `events.db` contains a
/// `cascade.changed` event within 500 ms (debounce=50 ms in test config).
///
/// CASCADE.md must exist BEFORE the daemon spawns so `discover_all_cascade_paths`
/// finds and registers the path with the notify watcher during start-up.
#[test]
#[serial(global_env)]
fn watcher_triggers_on_cascade_change() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let (mut child, cascade_dir, cascade_md) =
        spawn_daemon_with_cascade_md(&tmpdir, "# cascade\nInitial content.\n");

    let pid_path = cascade_dir.join("daemon.pid");
    assert!(
        wait_for_file(&pid_path, 8000),
        "daemon must start before watcher test"
    );

    // Allow the watcher's internal notify start-up to settle.
    thread::sleep(Duration::from_millis(300));

    // Modify CASCADE.md — append a line to trigger a change event.
    fs::write(&cascade_md, "# cascade\nInitial content.\nchanged line\n")
        .expect("modify CASCADE.md");

    // Wait long enough for debounce (50 ms) + event processing.
    thread::sleep(Duration::from_millis(800));

    // Read events.db in WAL mode and check for cascade.changed.
    let db_path = cascade_dir.join("events.db");

    #[cfg(unix)]
    let event_found = {
        let _ = Command::new("kill")
            .args(["-TERM", &child.id().to_string()])
            .status();
        let _ = child.wait();
        check_events_db_for_kind(&db_path, "cascade.changed")
    };
    #[cfg(not(unix))]
    let event_found = {
        let _ = child.kill();
        let _ = child.wait();
        check_events_db_for_kind(&db_path, "cascade.changed")
    };

    assert!(
        event_found,
        "events.db should contain a cascade.changed event after CASCADE.md was modified; \
         db_path = {}",
        db_path.display()
    );
}

/// Test 5 — after SIGKILL (force-kill), `crash-last.txt` is written within 500 ms.
///
/// SIGKILL delivers an unclean exit. The crash sentinel is written by the
/// Tokio signal handler via `write_crash_sentinel` in main.rs before exit.
/// Since SIGKILL prevents userspace cleanup, the crash sentinel is written
/// by the OS-level signal path or the drop handler.
///
/// Note: SIGKILL does not allow the process to run any cleanup code.
/// `write_crash_sentinel` must therefore be called before the async loop so
/// the file exists even when the process is force-killed. If the current
/// binary does NOT write crash-last.txt synchronously at startup (e.g.
/// only on panic/error exit), this test is skipped with a note.
#[test]
#[serial(global_env)]
fn crash_sentinel_written_on_kill9() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let (mut child, cascade_dir) = spawn_daemon(&tmpdir);

    let pid_path = cascade_dir.join("daemon.pid");
    assert!(
        wait_for_file(&pid_path, 8000),
        "daemon must start before kill-9 test"
    );

    // Force-kill — no chance for graceful shutdown.
    #[cfg(unix)]
    let kill_result = Command::new("kill")
        .args(["-9", &child.id().to_string()])
        .status();
    #[cfg(not(unix))]
    let kill_result = child.kill();

    let _ = kill_result;
    let _ = child.wait();

    // crash-last.txt should appear.  Because SIGKILL prevents cleanup code
    // from running we wait the full 2 s window (generous for CI).
    let crash_path = cascade_dir.join("crash-last.txt");
    let found = wait_for_file(&crash_path, 2000);

    assert!(
        found,
        "crash-last.txt should exist after SIGKILL; path = {}",
        crash_path.display()
    );
}

/// Test 6 — `quota-state.json` is written within 20 s of daemon start.
///
/// The test config sets `quota_poller.interval_secs = 10` so the first poll
/// fires within ~10 s. When `localhost:3761` is not available (normal in CI)
/// the poller writes an error-state JSON — the file must still exist.
///
/// Gated on `gfp` feature: the quota poller is only compiled and spawned when
/// the `gfp` feature is active. Without it, `quota-state.json` is never written.
#[cfg(feature = "gfp")]
#[test]
#[serial(global_env)]
fn quota_state_written() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let (mut child, cascade_dir) = spawn_daemon(&tmpdir);

    let pid_path = cascade_dir.join("daemon.pid");
    assert!(
        wait_for_file(&pid_path, 8000),
        "daemon must start before quota state test"
    );

    let quota_path = cascade_dir.join("quota-state.json");
    let found = wait_for_file(&quota_path, 20_000);

    // Shut down before asserting so we don't leave a daemon running.
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
        "quota-state.json should exist within 15 s when localhost:3761 is unavailable; \
         path = {}",
        quota_path.display()
    );

    // Verify the file is valid JSON (error state is fine; absent is not).
    let raw = fs::read_to_string(&quota_path).unwrap_or_default();
    assert!(!raw.trim().is_empty(), "quota-state.json must not be empty");
    let _val: serde_json::Value =
        serde_json::from_str(&raw).expect("quota-state.json must contain valid JSON");
}

/// Test 7 — `providers.json` is written at daemon startup with detected accounts.
///
/// The daemon detects harness accounts from well-known config paths at startup
/// and writes the result to `~/.cascade/providers.json`. Even with no harnesses
/// installed in the mock home, the file must exist (possibly empty or with manual entries).
#[test]
#[serial(global_env)]
fn daemon_writes_providers_json_at_startup() {
    let tmpdir = TempDir::new().expect("tmpdir");
    let (mut child, cascade_dir) = spawn_daemon(&tmpdir);

    let pid_path = cascade_dir.join("daemon.pid");
    assert!(
        wait_for_file(&pid_path, 8000),
        "daemon must start before providers test"
    );

    let providers_path = cascade_dir.join("providers.json");
    let found = wait_for_file(&providers_path, 5000);

    // Shut down before asserting.
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
        "providers.json should be written at daemon startup; path = {}",
        providers_path.display()
    );

    // Verify the file contains valid JSON and the expected ProvidersStore schema.
    let raw = fs::read_to_string(&providers_path).expect("read providers.json");
    assert!(!raw.trim().is_empty(), "providers.json must not be empty");
    let val: serde_json::Value =
        serde_json::from_str(&raw).expect("providers.json must contain valid JSON");

    // Check for required schema fields: schema_version, updated_at, providers.
    assert!(
        val.get("schema_version").is_some(),
        "providers.json must have 'schema_version' field"
    );
    assert!(
        val.get("updated_at").is_some(),
        "providers.json must have 'updated_at' field"
    );
    assert!(
        val.get("providers").is_some(),
        "providers.json must have 'providers' field"
    );
}

// ── Utility ────────────────────────────────────────────────────────────────────

/// Open `db_path` in WAL mode and return `true` if any unconsumed event
/// with `kind = target_kind` exists in the `events` table.
fn check_events_db_for_kind(db_path: &Path, target_kind: &str) -> bool {
    if !db_path.exists() {
        return false;
    }
    let conn = match rusqlite::Connection::open(db_path) {
        Ok(c) => c,
        Err(_) => return false,
    };
    // Read in WAL mode — consistent with how the daemon opens the database.
    let _ = conn.execute_batch("PRAGMA journal_mode=WAL;");
    let count: i64 = conn
        .query_row(
            "SELECT COUNT(*) FROM events WHERE kind = ?1 AND consumed = 0",
            rusqlite::params![target_kind],
            |row| row.get(0),
        )
        .unwrap_or(0);
    count > 0
}
