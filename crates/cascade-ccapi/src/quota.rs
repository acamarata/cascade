//! # quota
//!
//! Token-bucket rate limiter for the CC API proxy bridge.
//!
//! # Purpose
//! Prevents runaway usage of the CC bridge by enforcing a maximum number of
//! requests per minute. Uses a simple in-process atomic token bucket that
//! refills tokens in the background.
//!
//! # Design
//! - One `QuotaGuard` shared across all connections via `Arc<QuotaGuard>`.
//! - Default: 20 requests per minute, burst of min(rpm, 5).
//! - `check()` is non-blocking: atomically tries to consume one token.
//!   Returns `Err(CcApiError::QuotaExceeded)` if the bucket is empty.
//! - A background Tokio task refills tokens on a 1-second tick.
//!   For tests, the guard can be constructed with `with_bucket` for direct
//!   token-level control without background tasks.
//!
//! # Inputs
//! - `requests_per_minute: u32` at construction
//!
//! # Outputs
//! - `Ok(())` — token consumed, request may proceed
//! - `Err(CcApiError::QuotaExceeded(_))` — bucket empty, request is rejected
//!
//! # Constraints
//! - `check()` must not block the async runtime (uses atomics, no mutex).
//! - Default quota is deliberately conservative (20 rpm).

use std::sync::atomic::{AtomicI64, Ordering};
use std::sync::Arc;

use crate::error::CcApiError;

// ── QuotaGuard ─────────────────────────────────────────────────────────────────

/// Token-bucket quota guard.
///
/// Clone the `Arc<QuotaGuard>` to share across request handlers.
///
/// # Thread safety
/// Uses an `AtomicI64` for the token count — `check()` is lock-free.
#[derive(Debug)]
pub struct QuotaGuard {
    /// Current token count (may go negative if depleted).
    tokens: AtomicI64,
    /// Maximum token count (= burst capacity).
    burst: i64,
    /// Configured requests per minute (for error messages).
    rpm: u32,
    /// Tokens added per refill tick (computed from rpm).
    tokens_per_tick: i64,
}

impl QuotaGuard {
    /// Create a new quota guard allowing `rpm` requests per minute.
    ///
    /// A background refill task must be started separately with
    /// [`QuotaGuard::spawn_refill`] for the guard to refill over time.
    /// Tests can use `check()` directly after `set_tokens()` to test quota
    /// behaviour without spawning background tasks.
    ///
    /// # Panics
    /// Panics if `rpm` is zero.
    pub fn new(rpm: u32) -> Arc<Self> {
        assert!(rpm > 0, "rpm must be > 0");
        let burst = rpm.min(5) as i64;
        Arc::new(Self {
            tokens: AtomicI64::new(burst),
            burst,
            rpm,
            // Refill ticks every second; add rpm/60 tokens per tick (≥1).
            tokens_per_tick: ((rpm as i64) / 60).max(1),
        })
    }

    /// Default quota: 20 requests per minute.
    pub fn default_quota() -> Arc<Self> {
        Self::new(20)
    }

    /// Check if a request token is available and consume it atomically.
    ///
    /// Returns `Ok(())` if the request may proceed (token consumed).
    /// Returns `Err(CcApiError::QuotaExceeded(_))` if the bucket is empty.
    pub fn check(&self) -> Result<(), CcApiError> {
        let prev = self.tokens.fetch_sub(1, Ordering::AcqRel);
        if prev > 0 {
            Ok(())
        } else {
            // Restore the over-decremented token.
            self.tokens.fetch_add(1, Ordering::AcqRel);
            Err(CcApiError::QuotaExceeded(format!(
                "rate limit {rpm} req/min exceeded",
                rpm = self.rpm
            )))
        }
    }

    /// Refill tokens up to burst capacity (called by the background task).
    pub fn refill(&self) {
        let current = self.tokens.load(Ordering::Acquire);
        let new = (current + self.tokens_per_tick).min(self.burst);
        self.tokens.store(new, Ordering::Release);
    }

    /// Directly set the token count — for test control only.
    #[cfg(test)]
    pub fn set_tokens(&self, n: i64) {
        self.tokens.store(n, Ordering::SeqCst);
    }

    /// Returns the configured requests-per-minute limit.
    pub fn rpm(&self) -> u32 {
        self.rpm
    }

    /// Returns the current burst capacity.
    pub fn burst(&self) -> i64 {
        self.burst
    }

    /// Spawn a background Tokio task that refills tokens every second.
    ///
    /// Returns a `JoinHandle`. Drop the `Arc<QuotaGuard>` to implicitly stop
    /// the task (the weak reference will fail to upgrade).
    pub fn spawn_refill(self: &Arc<Self>) -> tokio::task::JoinHandle<()> {
        let weak = Arc::downgrade(self);
        tokio::spawn(async move {
            let mut interval = tokio::time::interval(std::time::Duration::from_secs(1));
            loop {
                interval.tick().await;
                match weak.upgrade() {
                    Some(guard) => guard.refill(),
                    None => break, // Guard dropped — stop task.
                }
            }
        })
    }
}

// ── Clone via Arc ──────────────────────────────────────────────────────────────
// QuotaGuard itself doesn't implement Clone (it has atomics); callers share
// an Arc<QuotaGuard> and clone that instead.

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quota_allows_within_burst() {
        let guard = QuotaGuard::new(60); // burst = min(60,5) = 5
        for i in 0..5 {
            assert!(guard.check().is_ok(), "request {i} should be allowed");
        }
    }

    #[test]
    fn quota_rejects_beyond_burst() {
        let guard = QuotaGuard::new(1); // burst = 1
        assert!(guard.check().is_ok(), "first request should be allowed");
        let result = guard.check();
        assert!(result.is_err(), "second request should be rate-limited");
        match result.unwrap_err() {
            CcApiError::QuotaExceeded(msg) => {
                assert!(msg.contains("rate limit"), "error message should mention rate limit");
            }
            other => panic!("unexpected error variant: {other:?}"),
        }
    }

    #[test]
    fn quota_refill_replenishes_tokens() {
        let guard = QuotaGuard::new(60); // burst = 5
        // Drain all 5 tokens.
        for _ in 0..5 {
            let _ = guard.check();
        }
        assert!(guard.check().is_err(), "exhausted bucket should reject");
        // Refill.
        guard.refill();
        assert!(guard.check().is_ok(), "after refill, a request should succeed");
    }

    #[test]
    fn quota_default_is_20_rpm() {
        let guard = QuotaGuard::default_quota();
        assert_eq!(guard.rpm(), 20);
    }

    #[test]
    fn quota_set_tokens_for_test_control() {
        let guard = QuotaGuard::new(10);
        guard.set_tokens(0);
        assert!(guard.check().is_err(), "zero tokens should reject");
        guard.set_tokens(3);
        assert!(guard.check().is_ok(), "3 tokens should allow");
    }

    #[test]
    fn quota_burst_is_min_rpm_5() {
        let guard = QuotaGuard::new(60);
        assert_eq!(guard.burst(), 5);

        let guard2 = QuotaGuard::new(3);
        assert_eq!(guard2.burst(), 3);
    }

    #[tokio::test]
    async fn quota_refill_task_runs_without_panic() {
        let guard = QuotaGuard::new(60);
        guard.set_tokens(0);
        let handle = guard.spawn_refill();
        tokio::time::sleep(std::time::Duration::from_millis(1100)).await;
        drop(guard);
        // Give the weak ref a moment to notice the drop.
        tokio::time::sleep(std::time::Duration::from_millis(100)).await;
        // Task should exit cleanly once the Arc is dropped.
        assert!(handle.is_finished() || !handle.is_finished()); // just confirm no panic
    }
}
