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
//! - **`LiveCcDriver`** — real PTY/pipe implementation compiled only when the
//!   `live_cc` feature is active (stub is shipped; full PTY impl is a
//!   documented stub — see § Live Implementation Status).
//!
//! # Live Implementation Status
//!
//! The `LiveCcDriver` provided here is a **documented stub**: it compiles,
//! typechecks, and implements the trait interface, but its `send_prompt` method
//! returns `Err(CcApiError::DriverError("live_cc stub …"))` rather than
//! actually driving a PTY. A robust PTY implementation requires platform-specific
//! (`libc`/`nix`) pty pair creation, ANSI stripping, and output framing that
//! is fragile by nature (CC's output format is not a stable API).
//!
//! This is an intentional scope boundary: the bridge logic (`bridge.rs`),
//! quota (`quota.rs`), and seam (`auth.rs`, CLI) are fully exercised via
//! `MockDriver`. The PTY driving is the highest-risk surface and is deferred
//! until a future iteration with dedicated PTY integration tests.
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
        Self { text: text.into(), done: false }
    }

    /// Construct the terminal sentinel chunk.
    pub fn done() -> Self {
        Self { text: String::new(), done: true }
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
        Self { canned_response: response.into(), force_error: false }
    }

    /// Construct a mock that always returns a driver error.
    pub fn failing() -> Self {
        Self { canned_response: String::new(), force_error: true }
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

// ── LiveCcDriver (stub, feature-gated) ───────────────────────────────────────

/// Real PTY/pipe driver that spawns the interactive `claude` CLI.
///
/// # Live Implementation Status: DOCUMENTED STUB
///
/// This struct compiles under `cfg(feature = "live_cc")` but `send_prompt`
/// returns `Err(CcApiError::DriverError("live_cc stub …"))`. A full PTY
/// implementation requires:
///
/// 1. Platform PTY pair allocation (`libc::openpty` / `nix::pty::openpty`).
/// 2. Spawning `claude` with the slave PTY as its controlling terminal.
/// 3. Writing the prompt (+ newline) to the master PTY fd.
/// 4. Reading and ANSI-stripping output until an end-of-turn sentinel.
/// 5. Detecting CC's "Human:" / "Assistant:" turn delimiters.
///
/// Steps 4-5 are fragile: CC's output format is not a public API and can
/// change on any CC release. This stub ships the interface so the bridge and
/// CLI compile; the PTY impl is deferred to a future iteration.
///
/// To activate: `cargo build --features live_cc` (never in CI or tests).
#[cfg(feature = "live_cc")]
pub struct LiveCcDriver;

#[cfg(feature = "live_cc")]
#[async_trait]
impl ProcessDriver for LiveCcDriver {
    async fn send_prompt(&self, prompt: &str) -> Result<Vec<OutputChunk>, CcApiError> {
        // PTY implementation is a documented stub.
        // The full implementation would:
        //   1. Allocate a PTY pair (openpty).
        //   2. Spawn `claude` with slave as controlling terminal.
        //   3. Write `{prompt}\n` to the master fd.
        //   4. Read + ANSI-strip output until end-of-turn sentinel.
        //
        // WHY stub: CC's terminal output format is not a stable API.
        // Risk: any CC release can break the parser without notice.
        // Deferred until a dedicated PTY integration test harness exists.
        let _ = prompt;
        Err(CcApiError::DriverError(
            "live_cc stub: PTY implementation is deferred; \
             see crates/cascade-ccapi/src/driver.rs § LiveCcDriver"
                .into(),
        ))
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
        let text: String = chunks.iter().filter(|c| !c.done).map(|c| c.text.clone()).collect();
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
        let text: String = chunks.iter().filter(|c| !c.done).map(|c| c.text.clone()).collect();
        assert!(!text.is_empty(), "default mock should return non-empty text");
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
}
