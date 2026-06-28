//! Shared test utilities for cascade-rag.
//!
//! # Purpose
//! Single process-global mutex for serializing env-mutating tests.

/// Serializes tests that mutate process-global env vars. Acquire at the start
/// of every test that calls `std::env::set_var` or `std::env::remove_var`.
pub(crate) static ENV_TEST_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());
