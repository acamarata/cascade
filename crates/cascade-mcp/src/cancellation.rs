//! MCP Cancellation — client-initiated request cancellation.
//!
//! Clients can cancel in-flight requests by sending a
//! `notifications/cancelled` notification with the original request ID.
//! Long-running tool handlers (index rebuild, large search) should poll the
//! cancellation token periodically and stop early if cancelled.
//!
//! ## Wire format
//!
//! ```json
//! {
//!   "jsonrpc": "2.0",
//!   "method": "notifications/cancelled",
//!   "params": {
//!     "requestId": "req-abc123",
//!     "reason": "user cancelled"
//!   }
//! }
//! ```
//!
//! ## Usage
//!
//! ```rust,ignore
//! use cascade_mcp::cancellation::{CancellationRegistry, CancelToken};
//!
//! # let registry = CancellationRegistry::new();
//! let token = registry.register("req-abc123");
//!
//! // In the tool handler — poll periodically:
//! if token.is_cancelled() {
//!     return Err(/* cancelled error */);
//! }
//!
//! // From the notification handler:
//! registry.cancel("req-abc123");
//! ```

use std::collections::HashMap;
use std::sync::{Arc, RwLock};

use tracing::debug;

// ── CancelToken ───────────────────────────────────────────────────────────────

/// A cancellation token for a single in-flight request.
///
/// Clone-cheap; backed by a shared atomic bool. Pass to async tool handlers
/// so they can poll `is_cancelled()` inside loops.
#[derive(Clone)]
pub struct CancelToken {
    cancelled: Arc<std::sync::atomic::AtomicBool>,
    request_id: String,
}

impl CancelToken {
    fn new(request_id: impl Into<String>) -> Self {
        Self {
            cancelled: Arc::new(std::sync::atomic::AtomicBool::new(false)),
            request_id: request_id.into(),
        }
    }

    /// Returns `true` if the client has cancelled this request.
    pub fn is_cancelled(&self) -> bool {
        self.cancelled.load(std::sync::atomic::Ordering::Relaxed)
    }

    /// Mark this token as cancelled. Called by [`CancellationRegistry::cancel`].
    fn mark_cancelled(&self) {
        self.cancelled
            .store(true, std::sync::atomic::Ordering::Relaxed);
        debug!(request_id = %self.request_id, "request cancelled");
    }

    /// The request ID this token belongs to.
    pub fn request_id(&self) -> &str {
        &self.request_id
    }
}

// ── CancellationRegistry ──────────────────────────────────────────────────────

/// Registry of in-flight request cancellation tokens.
///
/// Held by `McpServer`. Tool handlers call `register` to get a `CancelToken`
/// at the start of their work. The notification handler calls `cancel` when
/// a `notifications/cancelled` message arrives.
pub struct CancellationRegistry {
    tokens: RwLock<HashMap<String, CancelToken>>,
}

impl CancellationRegistry {
    pub fn new() -> Self {
        Self {
            tokens: RwLock::new(HashMap::new()),
        }
    }

    /// Register a new cancellation token for `request_id`.
    ///
    /// If a token for this ID already exists (e.g. a request was re-issued),
    /// the old token is replaced. The returned token is the one the handler
    /// should poll.
    pub fn register(&self, request_id: impl Into<String>) -> CancelToken {
        let id = request_id.into();
        let token = CancelToken::new(id.clone());
        let mut map = self.tokens.write().unwrap();
        map.insert(id, token.clone());
        token
    }

    /// Cancel the request with the given ID, if it is still in flight.
    ///
    /// If no matching token exists, this is a no-op (the request may have
    /// already completed).
    pub fn cancel(&self, request_id: &str) {
        let map = self.tokens.read().unwrap();
        if let Some(token) = map.get(request_id) {
            token.mark_cancelled();
        }
    }

    /// Remove the token for `request_id` when the request completes.
    ///
    /// Call this from the server loop after a tool handler returns to avoid
    /// unbounded growth of the registry.
    pub fn unregister(&self, request_id: &str) {
        let mut map = self.tokens.write().unwrap();
        map.remove(request_id);
    }

    /// Number of currently registered (in-flight) tokens.
    pub fn in_flight(&self) -> usize {
        self.tokens.read().unwrap().len()
    }
}

impl Default for CancellationRegistry {
    fn default() -> Self {
        Self::new()
    }
}
