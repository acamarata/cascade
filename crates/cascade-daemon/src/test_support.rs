//! Shared test utilities for cascade-daemon.
//!
//! # Purpose
//! Provides a single process-global mutex that serializes all tests mutating
//! env vars (HOME, XDG_CONFIG_HOME, CASCADE_*, etc.) within this crate's
//! test binary, preventing races under the parallel test runner.
//!
//! # Usage
//! ```rust,ignore
//! use crate::test_support::ENV_TEST_LOCK;
//! let _env_guard = ENV_TEST_LOCK.lock().unwrap_or_else(|e| e.into_inner());
//! ```

/// Serializes tests that mutate process-global env vars so they don't race
/// under the parallel test runner. Acquire at the start of every test that
/// calls `std::env::set_var` or `std::env::remove_var`.
pub(crate) static ENV_TEST_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());
