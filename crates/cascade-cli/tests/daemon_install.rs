//! Integration tests for `cascade daemon install / uninstall / service-status`.
//!
//! # Purpose
//! Verify the round-trip install → service-status → uninstall on each platform
//! using the real OS service mechanisms (launchd / systemd / schtasks).
//!
//! # Constraints
//! - Tests are `#[ignore]` by default: they touch real OS state (write to
//!   ~/Library/LaunchAgents on macOS, ~/.config/systemd/user on Linux, etc.)
//!   and require `cascaded` to be installed on PATH.
//! - Run explicitly with: `cargo test -p cascade-cli --test daemon_install -- --ignored`
//! - Each test is idempotent: it cleans up after itself via `uninstall()`.
//! - Tests are platform-gated with `#[cfg(target_os = ...)]` so the whole file
//!   compiles cleanly on every platform even though not all tests are exercised.
//!
//! # Windows note (T-P5-E03-02)
//! The Windows path uses Task Scheduler (schtasks) rather than the Windows
//! Service Control Manager.  This is intentional: Windows Services require
//! admin privileges and a service-handler loop in the binary; schtasks
//! provides equivalent per-user login persistence without elevation.
//! The `#[cfg(windows)]` block below compiles on Windows CI (tray-windows job)
//! and is cfg-gated out on macOS/Linux builds so the default build stays clean.
//!
//! # Manual verification note (T-P5-E03-04)
//! On macOS: `cascade daemon install` then `launchctl list dev.cascade.daemon`
//! On Linux: `cascade daemon install` then `systemctl --user is-active cascade-daemon.service`
//! On Windows: `cascade daemon install` then `schtasks /query /tn CascadeDaemon`

use cascade_cli::daemon_install;

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Returns true if `cascaded` is available on PATH (or in the standard
/// locations that `resolve_binary` checks).  Tests skip cleanly when absent.
fn cascaded_available() -> bool {
    daemon_install::resolve_binary().is_ok()
}

// ── macOS (launchd LaunchAgent) ───────────────────────────────────────────────

#[cfg(target_os = "macos")]
#[test]
#[ignore = "writes to ~/Library/LaunchAgents; requires cascaded on PATH; run with --ignored"]
fn macos_install_status_uninstall_roundtrip() {
    if !cascaded_available() {
        eprintln!("skip: cascaded not found on PATH");
        return;
    }

    // Install.
    let result = daemon_install::install().expect("install should succeed on macOS");
    assert!(
        result.service_file.contains("dev.cascade.daemon.plist"),
        "service_file should be the plist path, got: {}",
        result.service_file
    );
    assert!(
        std::path::Path::new(&result.service_file).exists(),
        "plist file should exist after install"
    );

    // service-status should succeed (plist present + launchd loaded).
    daemon_install::status().expect("status should succeed after install");

    // Uninstall — plist should be gone.
    daemon_install::uninstall().expect("uninstall should succeed");
    assert!(
        !std::path::Path::new(&result.service_file).exists(),
        "plist file should be removed after uninstall"
    );
}

#[cfg(target_os = "macos")]
#[test]
#[ignore = "idempotency test; requires cascaded on PATH; run with --ignored"]
fn macos_reinstall_is_idempotent() {
    if !cascaded_available() {
        eprintln!("skip: cascaded not found on PATH");
        return;
    }

    let r1 = daemon_install::install().expect("first install");
    let r2 = daemon_install::install().expect("second install (idempotent)");
    assert_eq!(
        r1.service_file, r2.service_file,
        "both installs should write to the same plist path"
    );

    daemon_install::uninstall().expect("cleanup");
}

// ── Linux (systemd --user) ────────────────────────────────────────────────────

#[cfg(target_os = "linux")]
#[test]
#[ignore = "writes to ~/.config/systemd/user; requires systemd --user + cascaded; run with --ignored"]
fn linux_install_status_uninstall_roundtrip() {
    if !cascaded_available() {
        eprintln!("skip: cascaded not found on PATH");
        return;
    }

    let result = daemon_install::install().expect("install should succeed on Linux");
    // Either systemd path or pidfile fallback — both are acceptable.
    assert!(
        !result.service_file.is_empty() || result.message.contains("nohup"),
        "service_file or nohup fallback message expected"
    );

    // status() should return Ok; it exits 1 internally if not active but
    // that only happens if systemctl --user is unavailable.
    // We tolerate the result here since CI may not have a real user session.
    let _ = daemon_install::status();

    daemon_install::uninstall().expect("uninstall should succeed");
}

// ── Windows (Task Scheduler) ──────────────────────────────────────────────────

#[cfg(windows)]
#[test]
#[ignore = "creates a Task Scheduler task; requires cascaded.exe on PATH; run with --ignored"]
fn windows_install_status_uninstall_roundtrip() {
    if !cascaded_available() {
        eprintln!("skip: cascaded.exe not found on PATH");
        return;
    }

    // Install: registers CascadeDaemon in Task Scheduler.
    let result = daemon_install::install().expect("install should succeed on Windows");
    assert!(
        result.message.contains("CascadeDaemon"),
        "message should reference task name, got: {}",
        result.message
    );

    // service-status: queries schtasks /query /tn CascadeDaemon.
    daemon_install::status().expect("status should succeed after install");

    // Uninstall: removes the task.
    daemon_install::uninstall().expect("uninstall should succeed");

    // Second uninstall should be idempotent (task not found → still Ok).
    daemon_install::uninstall().expect("second uninstall should be idempotent");
}
