//! # driver
//!
//! `ProcessDriver` trait — the abstraction that isolates bridge logic from the
//! specifics of how the Claude Code process is driven.
//!
//! # Purpose
//! Defines the contract between the HTTP bridge and the underlying process
//! management strategy. Two implementations exist:
//!
//! - **`MockDriver`** — deterministic fake used in all tests; never spawns a
//!   real process.
//! - **`LiveCcDriver`** — real PTY implementation compiled only when the
//!   `live_cc` feature is active. Uses `portable-pty` to allocate a PTY pair,
//!   spawn the command, write the prompt, and read back all output with a
//!   hard 30-second timeout.
//!
//! # Live Implementation Notes
//!
//! `LiveCcDriver` is a real PTY driver: it allocates a PTY pair via
//! `portable_pty::NativePtySystem`, spawns the target command (default:
//! `claude`) with the slave PTY as its controlling terminal, writes the prompt,
//! and drains output via a reader thread + channel with `recv_timeout` to
//! enforce the hard timeout without blocking the async executor.
//!
//! CC's terminal output format is not a stable API — any CC release can change
//! ANSI sequences or turn delimiters. The bridge logic (`bridge.rs`),
//! quota (`quota.rs`), and seam (`auth.rs`, CLI) are fully exercised via
//! `MockDriver`. The `live_cc` feature enables the PTY path for manual/
//! integration use; it must never be enabled in CI.
//!
//! # Inputs
//! - A prompt string
//!
//! # Outputs
//! - An async stream of `OutputChunk` items
//!
//! # Constraints
//! - Tests MUST use `MockDriver`. The `live_cc` feature must NOT be enabled in CI.

use async_trait::async_trait;

use crate::error::CcApiError;

// ── OutputChunk ───────────────────────────────────────────────────────────────

/// A single streamed chunk from the CC process.
#[derive(Debug, Clone, PartialEq)]
pub struct OutputChunk {
    /// The text fragment emitted by the model.
    pub text: String,
    /// True on the final chunk of a response.
    pub done: bool,
}

impl OutputChunk {
    /// Construct a non-terminal chunk.
    pub fn text(text: impl Into<String>) -> Self {
        Self {
            text: text.into(),
            done: false,
        }
    }

    /// Construct the terminal sentinel chunk.
    pub fn done() -> Self {
        Self {
            text: String::new(),
            done: true,
        }
    }
}

// ── ProcessDriver trait ───────────────────────────────────────────────────────

/// Abstraction over the mechanism used to drive an interactive Claude Code
/// process (or a mock/stub thereof).
///
/// # Purpose
/// Decouples the HTTP bridge from process management so the bridge can be
/// fully tested without spawning a real `claude` process.
///
/// # Inputs
/// A UTF-8 prompt string.
///
/// # Outputs
/// A `Vec<OutputChunk>` representing the complete response. In a real streaming
/// implementation this would be a stream; the `Vec` return keeps the trait
/// object-safe and deterministic for testing. The bridge converts it to SSE.
///
/// # Constraints
/// - Must not spawn a real `claude` process unless `cfg(feature = "live_cc")`.
/// - Must be `Send + Sync` for use behind an `Arc`.
#[async_trait]
pub trait ProcessDriver: Send + Sync {
    /// Send a prompt to the CC process and collect all response chunks.
    ///
    /// Returns `Err(CcApiError::DriverError(_))` on I/O failure.
    async fn send_prompt(&self, prompt: &str) -> Result<Vec<OutputChunk>, CcApiError>;

    /// Shut down the driver gracefully. May be a no-op for stateless impls.
    async fn shutdown(&self) -> Result<(), CcApiError> {
        Ok(())
    }
}

// ── MockDriver ────────────────────────────────────────────────────────────────

/// Deterministic mock driver for tests.
///
/// # Purpose
/// Lets the HTTP bridge, quota guard, and session lifecycle be tested without
/// any subprocess or PTY. The mock returns a configurable canned response.
///
/// # Constraints
/// Always compiled (not feature-gated). CI must use this driver.
pub struct MockDriver {
    /// The canned response to return for every prompt.
    pub canned_response: String,
    /// If true, `send_prompt` returns an error.
    pub force_error: bool,
}

impl Default for MockDriver {
    fn default() -> Self {
        Self {
            canned_response: "mock response: Hello from MockDriver".into(),
            force_error: false,
        }
    }
}

impl MockDriver {
    /// Construct a mock that returns the given canned response.
    pub fn with_response(response: impl Into<String>) -> Self {
        Self {
            canned_response: response.into(),
            force_error: false,
        }
    }

    /// Construct a mock that always returns a driver error.
    pub fn failing() -> Self {
        Self {
            canned_response: String::new(),
            force_error: true,
        }
    }
}

#[async_trait]
impl ProcessDriver for MockDriver {
    async fn send_prompt(&self, _prompt: &str) -> Result<Vec<OutputChunk>, CcApiError> {
        if self.force_error {
            return Err(CcApiError::DriverError("MockDriver forced error".into()));
        }
        // Split response into chunks to exercise SSE streaming in tests.
        let words: Vec<&str> = self.canned_response.split_whitespace().collect();
        let mut chunks: Vec<OutputChunk> = words
            .iter()
            .map(|w| OutputChunk::text(format!("{} ", w)))
            .collect();
        chunks.push(OutputChunk::done());
        Ok(chunks)
    }
}

// ── LiveCcDriver (feature-gated PTY implementation) ──────────────────────────

/// Real PTY driver that spawns the interactive `claude` CLI (or any command
/// configured via `LiveCcDriver::with_command`) inside a pseudo-terminal.
///
/// # Purpose
/// Drives `claude` as a full TTY process so it does not detect a non-TTY pipe
/// and fall back to JSON/non-interactive mode.  The PTY master is used to write
/// the prompt and read back the raw output.
///
/// # Inputs
/// - A UTF-8 prompt string (a single newline is appended automatically).
///
/// # Outputs
/// - The raw text read from the PTY master until EOF (child exit), returned as
///   a single `OutputChunk` followed by a `done` sentinel.
///
/// # Constraints
/// - Only compiled when `cfg(feature = "live_cc")`.
/// - Never used in CI or standard tests — `MockDriver` covers those paths.
/// - Blocking I/O is performed on a `tokio::task::spawn_blocking` thread so the
///   async executor is never blocked.
/// - Hard timeout: 30 seconds.  Returns `Err` if the child does not exit in time.
///
/// To activate: `cargo build --features live_cc` (never in CI or standard tests).
#[cfg(feature = "live_cc")]
pub struct LiveCcDriver {
    /// Path (or name on PATH) of the binary to spawn. Defaults to `"claude"`.
    pub command: String,
    /// Arguments passed to the binary. Defaults to `[]`.
    pub args: Vec<String>,
    /// Read timeout in seconds. Defaults to 30.
    pub timeout_secs: u64,
}

#[cfg(feature = "live_cc")]
impl Default for LiveCcDriver {
    fn default() -> Self {
        Self {
            command: "claude".into(),
            args: Vec::new(),
            timeout_secs: 30,
        }
    }
}

#[cfg(feature = "live_cc")]
impl LiveCcDriver {
    /// Construct a driver that will spawn `claude` with default settings.
    pub fn new() -> Self {
        Self::default()
    }

    /// Construct a driver targeting a specific command (useful for tests).
    ///
    /// # Inputs
    /// - `cmd`: binary path or name on `PATH`
    /// - `args`: arguments to pass after the binary name
    pub fn with_command(cmd: impl Into<String>, args: impl IntoIterator<Item = impl Into<String>>) -> Self {
        Self {
            command: cmd.into(),
            args: args.into_iter().map(Into::into).collect(),
            timeout_secs: 30,
        }
    }
}

#[cfg(feature = "live_cc")]
#[async_trait]
impl ProcessDriver for LiveCcDriver {
    /// Spawn the command inside a PTY, write the prompt, read all output, and
    /// return it as a two-element `Vec<OutputChunk>` (text + done sentinel).
    ///
    /// # WHY PTY
    /// Claude Code detects whether its stdout is a TTY.  A plain pipe causes it
    /// to switch to a non-interactive JSON output mode that is harder to parse.
    /// A PTY presents a TTY file-descriptor so the CLI behaves interactively.
    ///
    /// # Timeout / not-found handling
    /// - Binary not on PATH → `Err("claude binary not found: …")` (surface the OS error).
    /// - Child runs longer than `timeout_secs` → `Err("PTY read timed out after …s")`.
    /// - Any PTY I/O error → `Err` with the underlying OS message.
    async fn send_prompt(&self, prompt: &str) -> Result<Vec<OutputChunk>, CcApiError> {
        use std::io::{Read, Write};
        use std::sync::mpsc;
        use std::time::Duration;
        use portable_pty::{CommandBuilder, NativePtySystem, PtySize, PtySystem as _};

        let cmd_name = self.command.clone();
        let cmd_args = self.args.clone();
        let prompt_owned = format!("{}\n", prompt);
        let timeout = Duration::from_secs(self.timeout_secs);

        // All blocking PTY I/O runs on a dedicated thread so we don't block
        // the async executor.
        let result = tokio::task::spawn_blocking(move || -> Result<String, String> {
            // Allocate a PTY pair (platform-native: posix_openpt on Unix,
            // ConPTY on Windows).
            let pty_system = NativePtySystem::default();
            let pty_pair = pty_system
                .openpty(PtySize {
                    rows: 24,
                    cols: 80,
                    pixel_width: 0,
                    pixel_height: 0,
                })
                .map_err(|e| format!("PTY allocation failed: {e}"))?;

            // Build the child command.
            let mut builder = CommandBuilder::new(&cmd_name);
            for arg in &cmd_args {
                builder.arg(arg);
            }

            // Spawn into the slave side of the PTY.
            // WHY: this makes the child's stdin/stdout/stderr the slave PTY
            // device; the child sees a real terminal.
            let mut child = pty_pair
                .slave
                .spawn_command(builder)
                .map_err(|e| {
                    // Produce a clear message when the binary is absent.
                    let msg = e.to_string();
                    if msg.contains("No such file") || msg.contains("not found") || msg.contains("os error 2") {
                        format!("claude binary not found: {msg}")
                    } else {
                        format!("PTY spawn failed: {msg}")
                    }
                })?;

            // Write the prompt to the master side.
            {
                let mut writer = pty_pair
                    .master
                    .take_writer()
                    .map_err(|e| format!("PTY writer acquisition failed: {e}"))?;
                writer
                    .write_all(prompt_owned.as_bytes())
                    .map_err(|e| format!("PTY write failed: {e}"))?;
                // Drop writer; the child's stdin sees EOF.
            }

            // Read all output from the master until EOF (child exit) on a
            // dedicated reader thread, then collect via a channel with timeout.
            //
            // WHY channel + separate thread: `read()` on the PTY master blocks
            // indefinitely when the child is still running.  Checking a deadline
            // between reads only fires after each read returns, which never
            // happens for a hanging child.  Spawning a reader thread and using
            // `recv_timeout` gives a clean timeout that actually fires.
            let mut reader = pty_pair
                .master
                .try_clone_reader()
                .map_err(|e| format!("PTY reader acquisition failed: {e}"))?;

            let (tx, rx) = mpsc::channel::<Result<Vec<u8>, String>>();

            std::thread::spawn(move || {
                let mut buf = [0u8; 512];
                loop {
                    match reader.read(&mut buf) {
                        Ok(0) => {
                            // EOF — child has exited.
                            let _ = tx.send(Ok(Vec::new()));
                            break;
                        }
                        Ok(n) => {
                            if tx.send(Ok(buf[..n].to_vec())).is_err() {
                                // Receiver dropped (timeout fired); stop reading.
                                break;
                            }
                        }
                        Err(e) => {
                            // EIO (raw OS error 5 on macOS, 5 on Linux) means
                            // the slave side was closed (child exited) — treat
                            // as EOF rather than error.
                            if e.raw_os_error() == Some(5) {
                                let _ = tx.send(Ok(Vec::new()));
                            } else {
                                let _ = tx.send(Err(format!("PTY read error: {e}")));
                            }
                            break;
                        }
                    }
                }
            });

            let mut output: Vec<u8> = Vec::with_capacity(4096);
            let deadline = std::time::Instant::now() + timeout;

            loop {
                let remaining = deadline.saturating_duration_since(std::time::Instant::now());
                if remaining.is_zero() {
                    let _ = child.kill();
                    return Err(format!(
                        "PTY read timed out after {}s",
                        timeout.as_secs()
                    ));
                }

                match rx.recv_timeout(remaining) {
                    Ok(Ok(chunk)) if chunk.is_empty() => break, // EOF sentinel
                    Ok(Ok(chunk)) => output.extend_from_slice(&chunk),
                    Ok(Err(e)) => return Err(e),
                    Err(mpsc::RecvTimeoutError::Timeout) => {
                        let _ = child.kill();
                        return Err(format!(
                            "PTY read timed out after {}s",
                            timeout.as_secs()
                        ));
                    }
                    Err(mpsc::RecvTimeoutError::Disconnected) => {
                        // Reader thread finished without sending EOF sentinel;
                        // treat as normal completion.
                        break;
                    }
                }
            }

            // Reap the child so it doesn't become a zombie.
            let _ = child.wait();

            String::from_utf8(output)
                .map_err(|e| format!("PTY output is not valid UTF-8: {e}"))
        })
        .await
        .map_err(|e| CcApiError::DriverError(format!("spawn_blocking panicked: {e}")))?
        .map_err(CcApiError::DriverError)?;

        Ok(vec![OutputChunk::text(result), OutputChunk::done()])
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn mock_driver_returns_chunks() {
        let driver = MockDriver::with_response("hello world");
        let chunks = driver.send_prompt("test prompt").await.unwrap();
        // Expect word chunks + done sentinel.
        assert!(chunks.len() >= 2, "expected at least 2 chunks");
        assert!(chunks.last().unwrap().done, "last chunk must be done");
        // Text of non-done chunks should contain the words.
        let text: String = chunks
            .iter()
            .filter(|c| !c.done)
            .map(|c| c.text.clone())
            .collect();
        assert!(text.contains("hello"), "text should contain 'hello'");
        assert!(text.contains("world"), "text should contain 'world'");
    }

    #[tokio::test]
    async fn mock_driver_force_error() {
        let driver = MockDriver::failing();
        let result = driver.send_prompt("anything").await;
        assert!(result.is_err(), "forced error should return Err");
        match result.unwrap_err() {
            CcApiError::DriverError(msg) => assert!(msg.contains("MockDriver")),
            other => panic!("unexpected error: {other:?}"),
        }
    }

    #[tokio::test]
    async fn mock_driver_default_response() {
        let driver = MockDriver::default();
        let chunks = driver.send_prompt("anything").await.unwrap();
        let text: String = chunks
            .iter()
            .filter(|c| !c.done)
            .map(|c| c.text.clone())
            .collect();
        assert!(
            !text.is_empty(),
            "default mock should return non-empty text"
        );
    }

    #[tokio::test]
    async fn output_chunk_constructors() {
        let t = OutputChunk::text("hi");
        assert_eq!(t.text, "hi");
        assert!(!t.done);

        let d = OutputChunk::done();
        assert!(d.done);
        assert!(d.text.is_empty());
    }

    // ── LiveCcDriver PTY tests (live_cc feature only) ─────────────────────────

    /// Spawn `/bin/sh -c "echo hello"` via the PTY driver and verify the output
    /// contains "hello".  This exercises the real PTY path without requiring the
    /// actual `claude` binary.
    #[cfg(feature = "live_cc")]
    #[tokio::test]
    async fn live_cc_driver_captures_echo_output() {
        // Use /bin/sh with -c flag so the command exits immediately after printing.
        let driver = crate::driver::LiveCcDriver::with_command(
            "/bin/sh",
            ["-c", "echo hello from pty"],
        );
        let chunks = driver
            .send_prompt("ignored prompt — command echoes on its own")
            .await
            .expect("PTY driver should succeed with /bin/sh echo");

        // Must have at least a text chunk and a done sentinel.
        assert!(
            chunks.len() >= 2,
            "expected at least 2 chunks, got {}",
            chunks.len()
        );
        assert!(chunks.last().unwrap().done, "last chunk must be done=true");

        let text: String = chunks
            .iter()
            .filter(|c| !c.done)
            .map(|c| c.text.clone())
            .collect();
        assert!(
            text.contains("hello from pty"),
            "output should contain 'hello from pty', got: {text:?}"
        );
    }

    /// Attempt to spawn a non-existent binary and verify a clear error is returned.
    #[cfg(feature = "live_cc")]
    #[tokio::test]
    async fn live_cc_driver_returns_error_for_missing_binary() {
        let driver = crate::driver::LiveCcDriver::with_command(
            "/absolutely/does/not/exist/binary_xyz",
            [] as [&str; 0],
        );
        let result = driver.send_prompt("test").await;
        assert!(
            result.is_err(),
            "spawning a non-existent binary should return Err"
        );
        match result.unwrap_err() {
            CcApiError::DriverError(msg) => {
                assert!(
                    msg.contains("not found") || msg.contains("spawn") || msg.contains("PTY"),
                    "error message should describe the failure, got: {msg:?}"
                );
            }
            other => panic!("unexpected error variant: {other:?}"),
        }
    }

    /// Verify the timeout fires when a command hangs (sleeps longer than the
    /// driver timeout).
    #[cfg(feature = "live_cc")]
    #[tokio::test]
    async fn live_cc_driver_times_out_on_hanging_command() {
        let mut driver = crate::driver::LiveCcDriver::with_command(
            "/bin/sh",
            ["-c", "sleep 60"],
        );
        // Very short timeout so the test does not actually wait 60 seconds.
        driver.timeout_secs = 2;

        let result = driver.send_prompt("anything").await;
        assert!(result.is_err(), "hanging command should time out");
        match result.unwrap_err() {
            CcApiError::DriverError(msg) => {
                assert!(
                    msg.contains("timed out"),
                    "error should mention timeout, got: {msg:?}"
                );
            }
            other => panic!("unexpected error variant: {other:?}"),
        }
    }
}
