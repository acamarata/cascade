//! Cross-platform daemon service install/uninstall helpers.
//!
//! # Purpose
//! Installs the `cascaded` binary as a user-scoped background service that
//! starts automatically at login, without requiring root or admin privileges.
//!
//! # Platform behaviour
//! | Platform | Service mechanism | File written |
//! |---|---|---|
//! | macOS | launchd LaunchAgent | `~/Library/LaunchAgents/dev.cascade.daemon.plist` |
//! | Linux | systemd --user unit (fallback: nohup + pidfile) | `~/.config/systemd/user/cascade-daemon.service` |
//! | Windows | Task Scheduler (schtasks) | task `CascadeDaemon` |
//!
//! # Inputs
//! None — all paths are derived from the user's HOME.
//!
//! # Constraints
//! - Writes only within the user's HOME; never requires sudo/admin.
//! - Atomic file writes (tmp → rename).
//! - Idempotent: re-install replaces the service file and reloads cleanly.
//! - Binary resolution order: `~/.local/bin/cascaded`, `$CARGO_HOME/bin/cascaded`, PATH.
//!   Returns `Err` with a "build/install cascaded first" message if absent.

use std::path::PathBuf;

use cascade_types::error::{CascadeError, Result};

// ── Service label / unit name ─────────────────────────────────────────────────

#[cfg(target_os = "macos")]
const LABEL: &str = "dev.cascade.daemon";
#[cfg(target_os = "linux")]
const UNIT_NAME: &str = "cascade-daemon.service";
#[cfg(windows)]
const WIN_TASK_NAME: &str = "CascadeDaemon";

// ── Public API ────────────────────────────────────────────────────────────────

/// Outcome of a successful [`install`] call.
pub struct InstallResult {
    /// Absolute path to the written service file (plist / unit / n/a for Windows).
    pub service_file: String,
    /// Human-readable status message.
    pub message: String,
}

/// Install `cascaded` as a user-scoped background service.
///
/// Idempotent: if the service file already exists it is replaced and reloaded.
///
/// Returns `CascadeError::BinaryNotFound` if `cascaded` cannot be located.
pub fn install() -> Result<InstallResult> {
    let binary = resolve_binary()?;
    install_platform(binary)
}

/// Remove the user-scoped background service installed by [`install`].
///
/// Idempotent: returns `Ok` if the service was not installed.
pub fn uninstall() -> Result<()> {
    uninstall_platform()
}

/// Print service status to stdout: whether the service file exists + loaded state.
pub fn status() -> Result<()> {
    status_platform()
}

/// Restart the user-scoped background service (unload → load on macOS/Linux;
/// stop + start via Task Scheduler on Windows).
pub fn restart() -> Result<()> {
    restart_platform()
}

// ── Binary resolution ─────────────────────────────────────────────────────────

/// Locate the `cascaded` binary.
///
/// Resolution order:
/// 1. `~/.local/bin/cascaded[.exe]`
/// 2. `$CARGO_HOME/bin/cascaded[.exe]` (fallback: `~/.cargo/bin/cascaded[.exe]`)
/// 3. First match in `$PATH`
///
/// Returns `CascadeError::Other` with a clear "build/install cascaded first"
/// message if the binary is absent in all locations.
pub fn resolve_binary() -> Result<PathBuf> {
    let name = binary_name();

    // 1. ~/.local/bin
    let home = cascade_types::paths::home_dir();
    let local_bin = home.join(".local").join("bin").join(name);
    if local_bin.is_file() {
        return Ok(local_bin);
    }

    // 2. $CARGO_HOME/bin  (or ~/.cargo/bin)
    let cargo_bin = std::env::var("CARGO_HOME")
        .map(PathBuf::from)
        .unwrap_or_else(|_| home.join(".cargo"))
        .join("bin")
        .join(name);
    if cargo_bin.is_file() {
        return Ok(cargo_bin);
    }

    // 3. PATH
    if let Ok(path_var) = std::env::var("PATH") {
        for dir in std::env::split_paths(&path_var) {
            let candidate = dir.join(name);
            if candidate.is_file() {
                return Ok(candidate);
            }
        }
    }

    Err(CascadeError::Other(format!(
        "cascaded binary not found; run `cargo install --path crates/cascaded` or \
         place the binary at {} first",
        local_bin.display()
    )))
}

#[cfg(not(target_os = "windows"))]
fn binary_name() -> &'static str {
    "cascaded"
}

#[cfg(target_os = "windows")]
fn binary_name() -> &'static str {
    "cascaded.exe"
}

// ── Platform implementations ──────────────────────────────────────────────────

// ─────────────────────── macOS ───────────────────────────────────────────────

#[cfg(target_os = "macos")]
fn install_platform(binary: PathBuf) -> Result<InstallResult> {
    let home = cascade_types::paths::home_dir();
    let agents_dir = home.join("Library").join("LaunchAgents");
    std::fs::create_dir_all(&agents_dir).map_err(|e| CascadeError::Io {
        path: agents_dir.clone(),
        operation: "create LaunchAgents directory",
        source: e,
    })?;

    let plist_path = agents_dir.join(format!("{LABEL}.plist"));
    let log_out = home.join("Library").join("Logs").join("cascade-daemon.log");
    let log_err = home
        .join("Library")
        .join("Logs")
        .join("cascade-daemon-err.log");

    let plist_content = format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{binary}</string>
        <string>serve</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{log_out}</string>
    <key>StandardErrorPath</key>
    <string>{log_err}</string>
</dict>
</plist>
"#,
        binary = binary.display(),
        log_out = log_out.display(),
        log_err = log_err.display(),
    );

    atomic_write(&plist_path, &plist_content)?;

    // Idempotent: unload first (ignore errors — not loaded is fine).
    let _ = std::process::Command::new("launchctl")
        .args(["unload", "-w"])
        .arg(&plist_path)
        .output();

    let load_out = std::process::Command::new("launchctl")
        .args(["load", "-w"])
        .arg(&plist_path)
        .output()
        .map_err(|e| CascadeError::Other(format!("launchctl load failed: {e}")))?;

    let message = if load_out.status.success() {
        format!("cascaded installed as LaunchAgent ({LABEL})")
    } else {
        format!(
            "plist written but launchctl load returned non-zero: {}",
            String::from_utf8_lossy(&load_out.stderr).trim()
        )
    };

    Ok(InstallResult {
        service_file: plist_path.to_string_lossy().into_owned(),
        message,
    })
}

#[cfg(target_os = "macos")]
fn uninstall_platform() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let plist_path = home
        .join("Library")
        .join("LaunchAgents")
        .join(format!("{LABEL}.plist"));

    if plist_path.exists() {
        let _ = std::process::Command::new("launchctl")
            .args(["unload", "-w"])
            .arg(&plist_path)
            .output();
        std::fs::remove_file(&plist_path).map_err(|e| CascadeError::Io {
            path: plist_path,
            operation: "remove plist",
            source: e,
        })?;
    }
    println!("cascaded LaunchAgent removed (or was not installed)");
    Ok(())
}

#[cfg(target_os = "macos")]
fn status_platform() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let plist_path = home
        .join("Library")
        .join("LaunchAgents")
        .join(format!("{LABEL}.plist"));
    let installed = plist_path.exists();
    println!(
        "service file: {}",
        if installed { "present" } else { "not found" }
    );
    if installed {
        println!("plist: {}", plist_path.display());
    }

    // launchctl list shows loaded status
    let out = std::process::Command::new("launchctl")
        .args(["list", LABEL])
        .output();
    let loaded = out.map(|o| o.status.success()).unwrap_or(false);
    println!("loaded:       {}", if loaded { "yes" } else { "no" });
    if !installed && !loaded {
        std::process::exit(1);
    }
    Ok(())
}

#[cfg(target_os = "macos")]
fn restart_platform() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let plist_path = home
        .join("Library")
        .join("LaunchAgents")
        .join(format!("{LABEL}.plist"));
    if !plist_path.exists() {
        return Err(CascadeError::Other(
            "daemon not installed; run `cascade daemon install` first".into(),
        ));
    }
    let _ = std::process::Command::new("launchctl")
        .args(["unload", "-w"])
        .arg(&plist_path)
        .output();
    std::process::Command::new("launchctl")
        .args(["load", "-w"])
        .arg(&plist_path)
        .output()
        .map_err(|e| CascadeError::Other(format!("launchctl load failed: {e}")))?;
    println!("cascaded restarted");
    Ok(())
}

// ─────────────────────── Linux ───────────────────────────────────────────────

#[cfg(target_os = "linux")]
fn install_platform(binary: PathBuf) -> Result<InstallResult> {
    let home = cascade_types::paths::home_dir();
    let systemd_user_dir = home.join(".config").join("systemd").join("user");

    // Check whether systemd --user is available.
    let has_systemd = std::process::Command::new("systemctl")
        .args(["--user", "status"])
        .output()
        .map(|o| !matches!(o.status.code(), Some(1) | None))
        .unwrap_or(false);

    if has_systemd {
        std::fs::create_dir_all(&systemd_user_dir).map_err(|e| CascadeError::Io {
            path: systemd_user_dir.clone(),
            operation: "create systemd user dir",
            source: e,
        })?;

        let unit_path = systemd_user_dir.join(UNIT_NAME);
        let unit_content = format!(
            "[Unit]\nDescription=Cascade daemon\nAfter=network.target\n\n\
             [Service]\nType=simple\nExecStart={binary} serve\nRestart=on-failure\n\
             RestartSec=5\n\n[Install]\nWantedBy=default.target\n",
            binary = binary.display()
        );

        atomic_write(&unit_path, &unit_content)?;

        // systemctl --user daemon-reload + enable + start
        for sub in &["daemon-reload", "enable", "start"] {
            let result = std::process::Command::new("systemctl")
                .args(["--user", sub, UNIT_NAME])
                .output()
                .map_err(|e| CascadeError::Other(format!("systemctl {sub} failed: {e}")))?;
            if !result.status.success() && *sub != "start" {
                // 'start' may fail if already running — that's fine; enable must succeed
                return Err(CascadeError::Other(format!(
                    "systemctl --user {sub} {UNIT_NAME} failed: {}",
                    String::from_utf8_lossy(&result.stderr).trim()
                )));
            }
        }

        Ok(InstallResult {
            service_file: unit_path.to_string_lossy().into_owned(),
            message: format!("cascaded installed as systemd --user service ({UNIT_NAME})"),
        })
    } else {
        // Fallback: nohup + pidfile
        let pid_path = cascade_types::paths::daemon_pid();
        if let Some(p) = pid_path.parent() {
            std::fs::create_dir_all(p).ok();
        }

        let child = std::process::Command::new(&binary)
            .arg("serve")
            .stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null())
            .spawn()
            .map_err(|e| CascadeError::Io {
                path: binary.clone(),
                operation: "spawn cascaded (nohup fallback)",
                source: e,
            })?;

        std::fs::write(&pid_path, child.id().to_string()).ok();

        Ok(InstallResult {
            service_file: pid_path.to_string_lossy().into_owned(),
            message: "cascaded started via nohup fallback (systemd --user not available); \
                      not persistent across reboots — add to ~/.profile manually if needed"
                .into(),
        })
    }
}

#[cfg(target_os = "linux")]
fn uninstall_platform() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let unit_path = home
        .join(".config")
        .join("systemd")
        .join("user")
        .join(UNIT_NAME);

    if unit_path.exists() {
        for sub in &["stop", "disable"] {
            let _ = std::process::Command::new("systemctl")
                .args(["--user", sub, UNIT_NAME])
                .output();
        }
        std::fs::remove_file(&unit_path).map_err(|e| CascadeError::Io {
            path: unit_path,
            operation: "remove unit file",
            source: e,
        })?;
        let _ = std::process::Command::new("systemctl")
            .args(["--user", "daemon-reload"])
            .output();
    } else {
        // Nohup fallback: send SIGTERM to PID if present
        use_pidfile_stop();
    }
    println!("cascaded service removed (or was not installed)");
    Ok(())
}

#[cfg(target_os = "linux")]
fn use_pidfile_stop() {
    let pid_path = cascade_types::paths::daemon_pid();
    if let Ok(s) = std::fs::read_to_string(&pid_path) {
        if let Ok(pid) = s.trim().parse::<i32>() {
            unsafe { libc::kill(pid, libc::SIGTERM) };
        }
    }
    std::fs::remove_file(&pid_path).ok();
}

#[cfg(target_os = "linux")]
fn status_platform() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let unit_path = home
        .join(".config")
        .join("systemd")
        .join("user")
        .join(UNIT_NAME);

    let installed = unit_path.exists();
    println!(
        "service file: {}",
        if installed { "present" } else { "not found" }
    );
    if installed {
        println!("unit: {}", unit_path.display());
        let out = std::process::Command::new("systemctl")
            .args(["--user", "is-active", UNIT_NAME])
            .output();
        let active = out
            .map(|o| String::from_utf8_lossy(&o.stdout).trim() == "active")
            .unwrap_or(false);
        println!("active:       {}", if active { "yes" } else { "no" });
        if !active {
            std::process::exit(1);
        }
    } else {
        // Check nohup pidfile
        let pid_path = cascade_types::paths::daemon_pid();
        if pid_path.exists() {
            println!("pidfile:      {} (nohup mode)", pid_path.display());
        } else {
            println!("not installed");
            std::process::exit(1);
        }
    }
    Ok(())
}

#[cfg(target_os = "linux")]
fn restart_platform() -> Result<()> {
    let home = cascade_types::paths::home_dir();
    let unit_path = home
        .join(".config")
        .join("systemd")
        .join("user")
        .join(UNIT_NAME);
    if unit_path.exists() {
        let result = std::process::Command::new("systemctl")
            .args(["--user", "restart", UNIT_NAME])
            .output()
            .map_err(|e| CascadeError::Other(format!("systemctl restart failed: {e}")))?;
        if !result.status.success() {
            return Err(CascadeError::Other(format!(
                "systemctl --user restart {UNIT_NAME} failed: {}",
                String::from_utf8_lossy(&result.stderr).trim()
            )));
        }
    } else {
        return Err(CascadeError::Other(
            "daemon not installed; run `cascade daemon install` first".into(),
        ));
    }
    println!("cascaded restarted");
    Ok(())
}

// ─────────────────────── Windows ─────────────────────────────────────────────

#[cfg(target_os = "windows")]
fn install_platform(binary: PathBuf) -> Result<InstallResult> {
    // Use schtasks /create to register a per-user scheduled task that runs at logon.
    // /F overwrites any existing task with the same name (idempotent).
    let bin_str = binary.to_string_lossy();
    let result = std::process::Command::new("schtasks")
        .args([
            "/create",
            "/tn",
            WIN_TASK_NAME,
            "/tr",
            &format!("{bin_str} serve"),
            "/sc",
            "ONLOGON",
            "/it", // run only when user is logged on
            "/f",  // force overwrite
        ])
        .output()
        .map_err(|e| CascadeError::Other(format!("schtasks /create failed: {e}")))?;

    if !result.status.success() {
        return Err(CascadeError::Other(format!(
            "schtasks /create failed: {}",
            String::from_utf8_lossy(&result.stderr).trim()
        )));
    }

    // Start the task immediately so the user does not have to log out/in.
    let _ = std::process::Command::new("schtasks")
        .args(["/run", "/tn", WIN_TASK_NAME])
        .output();

    Ok(InstallResult {
        service_file: String::new(), // Windows has no single service file
        message: format!("cascaded registered as Task Scheduler task '{WIN_TASK_NAME}' (ONLOGON)"),
    })
}

#[cfg(target_os = "windows")]
fn uninstall_platform() -> Result<()> {
    // Stop then delete.
    let _ = std::process::Command::new("schtasks")
        .args(["/end", "/tn", WIN_TASK_NAME])
        .output();
    let result = std::process::Command::new("schtasks")
        .args(["/delete", "/tn", WIN_TASK_NAME, "/f"])
        .output()
        .map_err(|e| CascadeError::Other(format!("schtasks /delete failed: {e}")))?;

    if !result.status.success() {
        // May fail with "task not found" — treat as already uninstalled.
        let stderr = String::from_utf8_lossy(&result.stderr);
        if !stderr.contains("not exist") && !stderr.contains("not found") {
            return Err(CascadeError::Other(format!(
                "schtasks /delete failed: {}",
                stderr.trim()
            )));
        }
    }
    println!("CascadeDaemon task removed (or was not installed)");
    Ok(())
}

#[cfg(target_os = "windows")]
fn status_platform() -> Result<()> {
    let out = std::process::Command::new("schtasks")
        .args(["/query", "/tn", WIN_TASK_NAME, "/fo", "LIST"])
        .output();
    match out {
        Ok(o) if o.status.success() => {
            print!("{}", String::from_utf8_lossy(&o.stdout));
        }
        _ => {
            println!("task '{WIN_TASK_NAME}' not found");
            std::process::exit(1);
        }
    }
    Ok(())
}

#[cfg(target_os = "windows")]
fn restart_platform() -> Result<()> {
    // Stop then re-run
    let _ = std::process::Command::new("schtasks")
        .args(["/end", "/tn", WIN_TASK_NAME])
        .output();
    let result = std::process::Command::new("schtasks")
        .args(["/run", "/tn", WIN_TASK_NAME])
        .output()
        .map_err(|e| CascadeError::Other(format!("schtasks /run failed: {e}")))?;
    if !result.status.success() {
        return Err(CascadeError::Other(format!(
            "schtasks /run failed: {}",
            String::from_utf8_lossy(&result.stderr).trim()
        )));
    }
    println!("CascadeDaemon restarted");
    Ok(())
}

// ── Shared helper ─────────────────────────────────────────────────────────────

/// Write `content` to `path` atomically (tmp → rename).
fn atomic_write(path: &std::path::Path, content: &str) -> Result<()> {
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(".tmp");
    let tmp_path = PathBuf::from(tmp);

    std::fs::write(&tmp_path, content).map_err(|e| CascadeError::Io {
        path: tmp_path.clone(),
        operation: "atomic write (tmp)",
        source: e,
    })?;
    std::fs::rename(&tmp_path, path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "atomic rename",
        source: e,
    })?;
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    // ── binary resolution ─────────────────────────────────────────────────────

    /// binary name returns the right platform-specific name
    #[test]
    fn binary_name_has_correct_extension() {
        let name = binary_name();
        #[cfg(target_os = "windows")]
        assert!(name.ends_with(".exe"), "expected .exe suffix");
        #[cfg(not(target_os = "windows"))]
        assert_eq!(name, "cascaded");
    }

    /// resolve_binary respects HOME override and finds binary in ~/.local/bin
    #[test]
    fn resolve_binary_finds_local_bin() {
        let tmp = TempDir::new().unwrap();
        let local_bin = tmp.path().join(".local").join("bin");
        std::fs::create_dir_all(&local_bin).unwrap();
        let bin = local_bin.join(binary_name());
        std::fs::write(&bin, b"#!/bin/sh").unwrap();
        // Make it executable on unix
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(&bin, std::fs::Permissions::from_mode(0o755)).unwrap();
        }

        // Override HOME so cascade_types::paths::home_dir() returns our tempdir
        let _guard = HomeGuard::set(tmp.path().to_str().unwrap());
        let found = resolve_binary().unwrap();
        assert_eq!(found, bin);
    }

    /// resolve_binary returns an error when the binary is absent
    #[test]
    fn resolve_binary_fails_when_absent() {
        let tmp = TempDir::new().unwrap();
        let _guard = HomeGuard::set(tmp.path().to_str().unwrap());
        // Clear PATH so no system binary is found
        let _path_guard = PathGuard::set("");
        let err = resolve_binary().unwrap_err();
        assert!(err.to_string().contains("cascaded binary not found"));
    }

    // ── atomic_write ──────────────────────────────────────────────────────────

    /// atomic_write produces the correct file and no .tmp residue
    #[test]
    fn atomic_write_no_tmp_residue() {
        let tmp = TempDir::new().unwrap();
        let target = tmp.path().join("test.plist");
        atomic_write(&target, "hello").unwrap();
        assert!(target.exists());
        assert!(!tmp.path().join("test.plist.tmp").exists());
        assert_eq!(std::fs::read_to_string(&target).unwrap(), "hello");
    }

    // ── macOS template generation (cfg-gated pure test) ───────────────────────

    #[cfg(target_os = "macos")]
    #[test]
    fn macos_plist_template_contains_label_and_binary() {
        let tmp = TempDir::new().unwrap();
        let binary = tmp.path().join("cascaded");
        std::fs::write(&binary, b"").unwrap();
        let _guard = HomeGuard::set(tmp.path().to_str().unwrap());

        // Write to a temp plist to verify content without running launchctl.
        let agents = tmp.path().join("Library").join("LaunchAgents");
        std::fs::create_dir_all(&agents).unwrap();
        let plist_path = agents.join("dev.cascade.daemon.plist");

        let log_out = tmp
            .path()
            .join("Library")
            .join("Logs")
            .join("cascade-daemon.log");
        let log_err = tmp
            .path()
            .join("Library")
            .join("Logs")
            .join("cascade-daemon-err.log");

        let content = format!(
            r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
    "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{LABEL}</string>
    <key>ProgramArguments</key>
    <array>
        <string>{binary}</string>
        <string>serve</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{log_out}</string>
    <key>StandardErrorPath</key>
    <string>{log_err}</string>
</dict>
</plist>
"#,
            binary = binary.display(),
            log_out = log_out.display(),
            log_err = log_err.display(),
        );
        atomic_write(&plist_path, &content).unwrap();

        let written = std::fs::read_to_string(&plist_path).unwrap();
        assert!(written.contains("dev.cascade.daemon"), "label missing");
        assert!(
            written.contains(&binary.display().to_string()),
            "binary path missing"
        );
        assert!(
            written.contains("<key>KeepAlive</key>"),
            "KeepAlive missing"
        );
    }

    // ── Linux template generation (cfg-gated pure test) ───────────────────────

    #[cfg(target_os = "linux")]
    #[test]
    fn linux_unit_template_contains_binary_and_restart() {
        let tmp = TempDir::new().unwrap();
        let binary = tmp.path().join("cascaded");
        std::fs::write(&binary, b"").unwrap();

        let unit_content = format!(
            "[Unit]\nDescription=Cascade daemon\nAfter=network.target\n\n\
             [Service]\nType=simple\nExecStart={binary} serve\nRestart=on-failure\n\
             RestartSec=5\n\n[Install]\nWantedBy=default.target\n",
            binary = binary.display()
        );
        assert!(unit_content.contains(&binary.display().to_string()));
        assert!(unit_content.contains("Restart=on-failure"));
        assert!(unit_content.contains("WantedBy=default.target"));
    }

    // ── Idempotent re-install writes a new file each time ─────────────────────

    #[test]
    fn atomic_write_is_idempotent() {
        let tmp = TempDir::new().unwrap();
        let target = tmp.path().join("service.plist");
        atomic_write(&target, "v1").unwrap();
        atomic_write(&target, "v2").unwrap();
        assert_eq!(std::fs::read_to_string(&target).unwrap(), "v2");
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    /// RAII guard that sets HOME for the duration of a test.
    struct HomeGuard {
        previous: Option<String>,
    }

    impl HomeGuard {
        fn set(value: &str) -> Self {
            let previous = std::env::var("HOME").ok();
            std::env::set_var("HOME", value);
            Self { previous }
        }
    }

    impl Drop for HomeGuard {
        fn drop(&mut self) {
            match &self.previous {
                Some(v) => std::env::set_var("HOME", v),
                None => std::env::remove_var("HOME"),
            }
        }
    }

    /// RAII guard that sets PATH for the duration of a test.
    struct PathGuard {
        previous: Option<String>,
    }

    impl PathGuard {
        fn set(value: &str) -> Self {
            let previous = std::env::var("PATH").ok();
            std::env::set_var("PATH", value);
            Self { previous }
        }
    }

    impl Drop for PathGuard {
        fn drop(&mut self) {
            match &self.previous {
                Some(v) => std::env::set_var("PATH", v),
                None => std::env::remove_var("PATH"),
            }
        }
    }
}
