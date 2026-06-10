//! Crash-loop detection and exponential backoff state management.
//!
//! Purpose: Detect rapid daemon restarts and apply exponential backoff before
//! resuming the supervisor. This prevents resource exhaustion when a buggy daemon
//! enters a crash loop.
//!
//! Design:
//!   - On daemon start, read crash-last.txt. If it was written within the last 60s
//!     AND restart-count.txt exists, increment the restart count and compute
//!     exponential backoff.
//!   - Compute backoff_secs = min(10 * 2^restart_count, 300).
//!   - Sleep before supervisor::run(). Log the backoff value.
//!   - If restart_count >= 10, write "crash-loop" to daemon-status.json and exit 1.
//!   - On clean exit (flush_state called): delete restart-count.txt.
//!   - On crash (crash sentinel written): restart-count.txt is NOT deleted.
//!
//! State files (under `~/.cascade/`):
//!   crash-last.txt    — written by shutdown::write_crash_sentinel on unclean exit
//!   restart-count.txt — incremented on each rapid restart; deleted on clean exit
//!   daemon-status.json — contains clean_stop flag and (if crash-loop) "crash-loop" reason

use std::path::Path;
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tracing::{info, warn};

/// Compute exponential backoff in seconds, capped at 300.
///
/// Formula: 10 * 2^restart_count, minimum 300.
/// Examples:
///   backoff_secs(0) = 10
///   backoff_secs(1) = 20
///   backoff_secs(2) = 40
///   backoff_secs(5) = 320 → capped at 300
pub fn backoff_secs(restart_count: u32) -> u64 {
    let base: u64 = 10;
    base.saturating_mul(2u64.saturating_pow(restart_count))
        .min(300)
}

/// Check for crash-loop state and apply exponential backoff if necessary.
///
/// Logic:
///   1. Read crash-last.txt. If absent or older than 60s, return Ok(()) (no backoff).
///   2. If crash-last.txt is recent (within 60s), read restart-count.txt (default 0).
///   3. Increment restart_count and write back.
///   4. If restart_count >= 10: write "crash-loop" status and return DaemonError::CrashLoop.
///   5. Otherwise: compute backoff_secs, sleep, log, and return Ok(()).
///
/// Inputs: `config_dir` — `~/.cascade` path.
/// Outputs: writes/updates restart-count.txt; may write daemon-status.json if crash-loop.
/// Constraints: async; never panics.
pub async fn check_and_apply(config_dir: &Path) -> Result<(), crate::supervisor::DaemonError> {
    let crash_path = config_dir.join("crash-last.txt");
    let restart_path = config_dir.join("restart-count.txt");

    // Step 1: Check if crash-last.txt exists and is recent (within 60s).
    let crash_ts = match tokio::fs::read_to_string(&crash_path).await {
        Ok(content) => {
            let parts: Vec<&str> = content.split_whitespace().collect();
            match parts.first() {
                Some(ts_str) => ts_str.parse::<u64>().ok(),
                None => None,
            }
        }
        Err(_) => {
            // No crash-last.txt → no backoff needed.
            info!("no crash sentinel found — resuming immediately");
            return Ok(());
        }
    };

    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0);

    // If crash was older than 60s, not a rapid restart → no backoff.
    if let Some(ts) = crash_ts {
        if now.saturating_sub(ts) > 60 {
            info!(
                seconds_ago = now.saturating_sub(ts),
                "crash was stale (>60s) — no backoff"
            );
            return Ok(());
        }
    }

    // Step 2: Recent crash detected. Read restart-count.txt (default 0).
    let restart_count = match tokio::fs::read_to_string(&restart_path).await {
        Ok(content) => content.trim().parse::<u32>().unwrap_or(0),
        Err(_) => 0, // File doesn't exist; start at 0.
    };

    // Step 3: Increment and write back.
    let new_count = restart_count.saturating_add(1);
    if let Err(e) = tokio::fs::write(&restart_path, format!("{}\n", new_count)).await {
        warn!(%e, "failed to update restart-count.txt");
        // Non-fatal; continue anyway.
    }

    // Step 4: Check for crash-loop threshold.
    if new_count >= 10 {
        warn!(
            restart_count = new_count,
            "crash-loop detected — exiting with status 1"
        );
        let ts = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        let status = serde_json::json!({
            "clean_stop": false,
            "ts": ts,
            "version": env!("CARGO_PKG_VERSION"),
            "crash_reason": "crash-loop"
        });
        if let Ok(json_bytes) = serde_json::to_vec(&status) {
            let status_path = config_dir.join("daemon-status.json");
            let _ = tokio::fs::write(&status_path, json_bytes).await;
        }
        return Err(crate::supervisor::DaemonError::CrashLoop);
    }

    // Step 5: Compute backoff and sleep.
    let backoff = backoff_secs(restart_count);
    warn!(
        restart_count = new_count,
        backoff_secs = backoff,
        "entering exponential backoff"
    );
    tokio::time::sleep(Duration::from_secs(backoff)).await;
    info!("backoff complete — resuming daemon");

    Ok(())
}

/// Reset backoff state on clean shutdown.
///
/// Inputs: `config_dir` — `~/.cascade` path.
/// Outputs: removes restart-count.txt (clean stop resets the counter).
/// Constraints: async; errors are logged but not fatal.
pub async fn reset(config_dir: &Path) {
    let restart_path = config_dir.join("restart-count.txt");
    if restart_path.exists() {
        if let Err(e) = tokio::fs::remove_file(&restart_path).await {
            warn!(%e, "failed to remove restart-count.txt on clean shutdown");
        } else {
            info!("restart-count.txt removed on clean shutdown");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    #[tokio::test]
    async fn test_no_crash_sentinel() {
        // No crash-last.txt → check_and_apply returns Ok(()) immediately (no sleep).
        let tmpdir = TempDir::new().unwrap();
        let result = check_and_apply(tmpdir.path()).await;
        assert!(result.is_ok());

        // restart-count.txt should NOT be created.
        let restart_path = tmpdir.path().join("restart-count.txt");
        assert!(!restart_path.exists());
    }

    #[tokio::test]
    async fn test_stale_crash_sentinel() {
        // crash-last.txt older than 60s → Ok(()) immediately (stale crash, not a loop).
        let tmpdir = TempDir::new().unwrap();
        let crash_path = tmpdir.path().join("crash-last.txt");
        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        let old_ts = now.saturating_sub(120); // 120s in the past
        std::fs::write(&crash_path, format!("{} old crash\n", old_ts)).unwrap();

        let result = check_and_apply(tmpdir.path()).await;
        assert!(result.is_ok());

        // restart-count.txt should NOT be created (stale crash).
        let restart_path = tmpdir.path().join("restart-count.txt");
        assert!(!restart_path.exists());
    }

    #[tokio::test]
    async fn test_recent_crash_with_backoff() {
        // Recent crash-last.txt + restart-count=2 → backoff = 40s (assert computed value).
        let tmpdir = TempDir::new().unwrap();
        let crash_path = tmpdir.path().join("crash-last.txt");
        let restart_path = tmpdir.path().join("restart-count.txt");

        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        std::fs::write(&crash_path, format!("{} recent crash\n", now)).unwrap();
        std::fs::write(&restart_path, "2\n").unwrap();

        let start = std::time::Instant::now();
        let result = check_and_apply(tmpdir.path()).await;
        let elapsed = start.elapsed();

        assert!(result.is_ok());
        assert!(elapsed >= Duration::from_secs(40));

        // restart-count.txt should now contain 3.
        let content = std::fs::read_to_string(&restart_path).unwrap();
        assert_eq!(content.trim(), "3");
    }

    #[tokio::test]
    async fn test_crash_loop_threshold() {
        // restart-count=10 → returns Err(DaemonError::CrashLoop).
        let tmpdir = TempDir::new().unwrap();
        let crash_path = tmpdir.path().join("crash-last.txt");
        let restart_path = tmpdir.path().join("restart-count.txt");

        let now = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|d| d.as_secs())
            .unwrap_or(0);
        std::fs::write(&crash_path, format!("{} recent crash\n", now)).unwrap();
        std::fs::write(&restart_path, "10\n").unwrap();

        let result = check_and_apply(tmpdir.path()).await;
        assert!(result.is_err());
        // Verify the error is DaemonError::CrashLoop.
        match result {
            Err(crate::supervisor::DaemonError::CrashLoop) => {}
            _ => panic!("expected CrashLoop error"),
        }
    }

    #[tokio::test]
    async fn test_reset_on_clean_shutdown() {
        // flush_state calls backoff::reset → restart-count.txt removed on clean stop.
        let tmpdir = TempDir::new().unwrap();
        let restart_path = tmpdir.path().join("restart-count.txt");
        std::fs::write(&restart_path, "5\n").unwrap();
        assert!(restart_path.exists());

        reset(tmpdir.path()).await;

        assert!(!restart_path.exists());
    }

    #[test]
    fn test_backoff_secs_formula() {
        // Verify the backoff formula computes correctly and caps at 300.
        assert_eq!(backoff_secs(0), 10);
        assert_eq!(backoff_secs(1), 20);
        assert_eq!(backoff_secs(2), 40);
        assert_eq!(backoff_secs(5), 300); // capped at 300
        assert_eq!(backoff_secs(6), 300); // still capped
    }
}
