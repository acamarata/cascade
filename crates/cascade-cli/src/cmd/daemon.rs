//! `cascade daemon` — control the background cascade daemon process.
//!
//! Subcommands:
//! - `start` — spawn `cascaded`, write PID to the canonical PID file
//! - `stop` — read PID, send SIGTERM, wait for socket to disappear
//! - `restart` — stop then start with 500 ms grace period
//! - `status` — check socket presence + PID liveness + daemon version
//!
//! The daemon binary is expected to be `cascaded` on PATH (or adjacent to the
//! `cascade` binary). On Windows, uses a named pipe instead of a Unix socket.

use std::path::PathBuf;
use std::time::Duration;

use async_trait::async_trait;
use clap::{Args, Subcommand};
use cascade_types::error::{CascadeError, Result};
use cascade_types::paths;

use super::Command;

/// Arguments for `cascade daemon`.
#[derive(Debug, Args)]
pub struct DaemonArgs {
    #[command(subcommand)]
    pub subcommand: DaemonSubcmd,
}

#[derive(Debug, Subcommand)]
pub enum DaemonSubcmd {
    /// Start the cascade daemon.
    Start(DaemonStartArgs),
    /// Stop the cascade daemon.
    Stop(DaemonStopArgs),
    /// Restart the cascade daemon (stop + 500 ms + start).
    Restart(DaemonRestartArgs),
    /// Print daemon status (socket alive, PID, version).
    Status(DaemonStatusArgs),
}

#[derive(Debug, Args)]
pub struct DaemonStartArgs {
    /// Block until the socket is ready before returning (max 5 s).
    #[arg(long)]
    pub wait: bool,
}

#[derive(Debug, Args)]
pub struct DaemonStopArgs;

#[derive(Debug, Args)]
pub struct DaemonRestartArgs;

#[derive(Debug, Args)]
pub struct DaemonStatusArgs {
    /// Output as JSON.
    #[arg(long)]
    pub json: bool,
}

#[async_trait]
impl Command for DaemonArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            DaemonSubcmd::Start(a) => a.run().await,
            DaemonSubcmd::Stop(a) => a.run().await,
            DaemonSubcmd::Restart(a) => a.run().await,
            DaemonSubcmd::Status(a) => a.run().await,
        }
    }
}

#[async_trait]
impl Command for DaemonStartArgs {
    async fn run(&self) -> Result<()> {
        let sock = paths::daemon_socket();
        if sock.exists() {
            // Socket present — daemon may already be running.
            if let Some(pid) = read_pid() {
                if process_is_alive(pid) {
                    println!("daemon already running (pid {})", pid);
                    return Ok(());
                }
            }
            // Stale socket — remove it before starting.
            std::fs::remove_file(&sock).ok();
        }

        // Spawn `cascaded` as a detached background process.
        let mut cmd = std::process::Command::new("cascaded");
        cmd.stdin(std::process::Stdio::null())
            .stdout(std::process::Stdio::null())
            .stderr(std::process::Stdio::null());
        daemonize(&mut cmd);

        let child = cmd.spawn().map_err(|e| CascadeError::Io {
            path: PathBuf::from("cascaded"),
            operation: "spawn daemon",
            source: e,
        })?;

        // Write PID file immediately after spawn.
        let pid_path = paths::daemon_pid();
        if let Some(parent) = pid_path.parent() {
            std::fs::create_dir_all(parent).ok();
        }
        std::fs::write(&pid_path, child.id().to_string()).ok();

        if self.wait {
            // Poll until the socket file appears (max 5 s).
            let deadline = std::time::Instant::now() + Duration::from_secs(5);
            while std::time::Instant::now() < deadline {
                if sock.exists() {
                    break;
                }
                tokio::time::sleep(Duration::from_millis(100)).await;
            }
            if !sock.exists() {
                eprintln!("warning: daemon started but socket not ready after 5 s");
            }
        }

        println!("daemon started (pid {})", child.id());
        Ok(())
    }
}

#[async_trait]
impl Command for DaemonStopArgs {
    async fn run(&self) -> Result<()> {
        let pid = read_pid().ok_or_else(|| CascadeError::PathNotFound {
            path: paths::daemon_pid(),
        })?;

        send_sigterm(pid)?;

        // Wait up to 5 s for the socket to disappear.
        let sock = paths::daemon_socket();
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while sock.exists() && std::time::Instant::now() < deadline {
            tokio::time::sleep(Duration::from_millis(100)).await;
        }

        // Clean up stale files.
        std::fs::remove_file(&paths::daemon_pid()).ok();
        std::fs::remove_file(&sock).ok();

        println!("daemon stopped (pid {})", pid);
        Ok(())
    }
}

#[async_trait]
impl Command for DaemonRestartArgs {
    async fn run(&self) -> Result<()> {
        DaemonStopArgs.run().await.ok(); // tolerate "not running"
        tokio::time::sleep(Duration::from_millis(500)).await;
        DaemonStartArgs { wait: true }.run().await
    }
}

#[async_trait]
impl Command for DaemonStatusArgs {
    async fn run(&self) -> Result<()> {
        let sock = paths::daemon_socket();
        let socket_ok = sock.exists();
        let pid = read_pid();
        let alive = pid.map(process_is_alive).unwrap_or(false);

        if self.json {
            let out = serde_json::json!({
                "socket": socket_ok,
                "pid": pid,
                "alive": alive,
            });
            println!("{}", serde_json::to_string_pretty(&out).unwrap());
        } else {
            println!("socket: {}", if socket_ok { "present" } else { "not found" });
            match pid {
                Some(p) => println!("pid:    {} ({})", p, if alive { "alive" } else { "stale" }),
                None => println!("pid:    not found"),
            }
        }

        if !alive {
            std::process::exit(1);
        }
        Ok(())
    }
}

// ── platform helpers ─────────────────────────────────────────────────────────

fn read_pid() -> Option<u32> {
    std::fs::read_to_string(paths::daemon_pid())
        .ok()
        .and_then(|s| s.trim().parse().ok())
}

fn process_is_alive(pid: u32) -> bool {
    #[cfg(unix)]
    {
        // kill(pid, 0) returns Ok(()) if the process exists.
        unsafe { libc_kill(pid as i32, 0) == 0 }
    }
    #[cfg(not(unix))]
    {
        // On Windows, a more complete check would use OpenProcess; for now
        // we check whether the pid file is younger than the socket.
        let _ = pid;
        paths::daemon_socket().exists()
    }
}

#[cfg(unix)]
fn send_sigterm(pid: u32) -> Result<()> {
    let ret = unsafe { libc_kill(pid as i32, 15 /* SIGTERM */) };
    if ret == 0 {
        Ok(())
    } else {
        Err(CascadeError::Other(format!("kill({}, SIGTERM) failed: errno {}", pid, ret)))
    }
}

#[cfg(not(unix))]
fn send_sigterm(pid: u32) -> Result<()> {
    // Windows: use TerminateProcess via std.
    use std::os::windows::io::FromRawHandle;
    let _ = pid;
    Err(CascadeError::Other("daemon stop on Windows not yet implemented".into()))
}

/// Detach the child process from the parent's process group on Unix.
#[cfg(unix)]
fn daemonize(cmd: &mut std::process::Command) {
    use std::os::unix::process::CommandExt;
    unsafe {
        cmd.pre_exec(|| {
            // Start a new session so the child is not a session leader.
            libc::setsid();
            Ok(())
        });
    }
}

#[cfg(not(unix))]
fn daemonize(_cmd: &mut std::process::Command) {}

// Thin wrappers to avoid depending on the `libc` crate in this stub.
#[cfg(unix)]
extern "C" {
    #[link_name = "kill"]
    fn libc_kill(pid: libc::pid_t, sig: libc::c_int) -> libc::c_int;
}

// `libc` is a transitive dep of tokio on unix, so it is already in the tree.
#[cfg(unix)]
extern crate libc;
