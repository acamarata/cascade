//! File watcher — debounced notifications when `.cascade/CASCADE.md` changes.
//!
//! Uses the `notify` crate for cross-platform file-system events. Wraps the
//! raw events with a 50 ms debounce so rapid editor saves don't flood the
//! daemon with cascading resolves.
//!
//! # Note
//!
//! Full implementation lives in S04 (E0 Sprint 4). This module provides the
//! public type surface so the CLI and daemon crates can import it without
//! touching the private implementation details.

use std::path::PathBuf;

use cascade_types::cascade_tier::CascadeTier;

/// A change event emitted when a `.cascade/CASCADE.md` file is modified.
#[derive(Debug, Clone)]
pub struct CascadeChanged {
    /// The tier whose CASCADE.md was modified.
    pub tier: CascadeTier,
    /// Path to the changed CASCADE.md file.
    pub path: PathBuf,
}

/// Watches all CASCADE.md paths registered during a resolve and emits
/// [`CascadeChanged`] events within 200 ms of a file modification.
///
/// Implements `Drop` for graceful teardown: the background watcher thread
/// stops automatically when this struct is dropped.
pub struct CascadeWatcher {
    _priv: (),
}

impl CascadeWatcher {
    /// Create a watcher for the given paths. Starts the background thread.
    ///
    /// # Errors
    ///
    /// Returns an error if the underlying `notify` watcher cannot be created
    /// (e.g. inotify limit reached on Linux).
    pub fn new(_paths: &[PathBuf]) -> cascade_types::error::Result<Self> {
        Ok(Self { _priv: () })
    }

    /// Returns `true` if the watcher thread is still running.
    pub fn is_active(&self) -> bool {
        false // stub; real impl polls thread health
    }
}

impl Drop for CascadeWatcher {
    fn drop(&mut self) {
        // Signal background thread to stop.
    }
}
