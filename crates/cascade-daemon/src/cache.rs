//! StatusCache polling loop for the cascade daemon.
//!
//! Purpose: Monitor the E-03 cache state (persisted in ~/.cascade/state.json or similar)
//!   and push TrayStateUpdate messages to the tray thread every 10 seconds. The tray
//!   thread then calls TrayHandle::update() to refresh the tooltip and icon on the
//!   OS status bar.
//!
//! Inputs:
//!   - `tray_tx: std::sync::mpsc::Sender<TrayStateUpdate>` — channel to the tray thread
//!   - polling interval = 10 seconds (hard-coded per T-P2-E06-12)
//!
//! Outputs:
//!   - TrayStateUpdate::UpdateState sent to tray_tx on each 10s tick
//!   - Initial TrayStateUpdate sent immediately before first interval
//!
//! Constraints:
//!   - Runs as a spawned Tokio task (async, not blocking the supervisor loop).
//!   - All cache reads happen through a single status_cache::fetch() function
//!     (future T-P2-E03-* deliverable).
//!   - If tray_tx.send() fails the task logs and continues — the tray thread may
//!     have exited cleanly but polling continues.
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, cache polling loop

use std::sync::atomic::Ordering;
use std::sync::mpsc;
use std::time::Duration;

use cascade_tray::TrayState;
use tokio::time;
use tracing::{error, info, warn};

use crate::supervisor::DAEMON_PAUSED;
use crate::tray::TrayStateUpdate;

/// Spawn the StatusCache polling task. Sends TrayStateUpdate every 10s.
///
/// Purpose: run the cache polling loop in a spawned Tokio task so the
///   supervisor's main loop can continue running IPC, healthcheck, etc.
///   in parallel.
/// Inputs:
///   - `tray_tx` — the mpsc sender to the tray thread (must be Send + Sync
///     across thread boundaries).
///
/// Outputs:
///   - `JoinHandle<()>` so the supervisor can cancel or join the task
///     during shutdown.
///
/// Constraints:
///   - The task runs forever unless cancelled via the JoinHandle.
///   - If tray_tx.send() fails, the task does NOT panic — it logs the
///     error and continues polling.
///   - While `DAEMON_PAUSED` is set (tray PauseDaemon, T-P7-E05-04), each
///     tick skips the fetch + send but keeps ticking — pausing suspends
///     the work, never the loop.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, spawn_status_cache_poller
pub fn spawn_status_cache_poller(
    tray_tx: mpsc::Sender<TrayStateUpdate>,
) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        info!("cache polling loop: started");

        // Create a 10-second interval. tokio::time::interval fires its FIRST
        // tick immediately, so it doubles as the initial update users expect on
        // daemon start; subsequent ticks fire every 10 seconds thereafter.
        // WHY rely on the immediate first tick instead of a separate initial
        // send: a standalone initial send PLUS the interval's immediate first
        // tick produced two updates at t=0 (a startup double-send). Using the
        // first tick as the initial update yields exactly one update at t=0,
        // then one every 10s — which is what the acceptance criteria specify
        // ("initial update immediate on daemon start").
        // WHY tokio::time::interval: precise and composes with tokio::select!
        // for shutdown cancellation (future enhancement).
        let mut interval = time::interval(Duration::from_secs(10));

        loop {
            // Wait for the next tick. The first iteration completes immediately
            // (the initial update); every later iteration waits 10 seconds.
            interval.tick().await;

            // T-P7-E05-04: while the daemon is paused (tray PauseDaemon),
            // skip the fetch + tray send but keep ticking — the loop must
            // not exit or block, and the first unpaused tick resumes tray
            // updates with no state rebuild.
            if DAEMON_PAUSED.load(Ordering::SeqCst) {
                continue;
            }

            // Fetch the current tray state from live quota.json.
            let state = fetch_tray_state();

            // Send the TrayStateUpdate to the tray thread.
            if let Err(e) = tray_tx.send(TrayStateUpdate::UpdateState(state)) {
                // The tray thread may have exited (e.g. early daemon shutdown,
                // TrayHandle init failed on headless CI). Log and continue polling.
                error!(%e, "cache polling loop: send failed (tray thread exited)");
                continue;
            }
        }
    })
}

/// Fetch a TrayState snapshot from live data.
///
/// Purpose: compute a TrayState from `~/.cascade/accounts/quota.json` so the
///   tray tooltip and icon reflect real account state (auth status, active
///   account count) rather than hardcoded zeros.
///
/// Inputs:  `~/.cascade/accounts/quota.json` (written by the fleet poller).
/// Outputs: `TrayState` populated from live quota data; falls back to
///   `daemon_status = Running` + zeros if the file is absent or malformed.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, cache polling
fn fetch_tray_state() -> TrayState {
    use cascade_core::accounts_store::quota_json_path;
    use cascade_tray::DaemonStatus;

    // Baseline: daemon is running (we are the daemon); counts start at 0.
    let mut state = TrayState {
        daemon_status: DaemonStatus::Running,
        ..TrayState::default()
    };

    let path = quota_json_path();
    let bytes = match std::fs::read(&path) {
        Ok(b) => b,
        Err(e) => {
            // File absent on first boot — not an error worth logging every 10s.
            if e.kind() != std::io::ErrorKind::NotFound {
                warn!(path = %path.display(), error = %e, "cache: failed to read quota.json");
            }
            return state;
        }
    };

    let v: serde_json::Value = match serde_json::from_slice(&bytes) {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "cache: quota.json is malformed — using defaults");
            return state;
        }
    };

    // Build fleet_summary lines from the accounts array in quota.json.
    // Each line: "<account>: <5h_pct>% (5h)" — or "needs-auth" when auth_dead.
    // WHY fleet_summary: this is the TrayState field the menu-bar icon renders;
    // total/active counts don't have a dedicated field in TrayState today.
    if let Some(accounts) = v.get("accounts").and_then(|a| a.as_array()) {
        let mut summary: Vec<String> = Vec::new();
        for entry in accounts {
            let account_id = entry
                .get("account")
                .and_then(|v| v.as_str())
                .unwrap_or("unknown");
            let auth_dead = entry
                .get("auth_dead")
                .and_then(|v| v.as_bool())
                .unwrap_or(false);
            if auth_dead {
                summary.push(format!("{account_id}: needs-auth"));
                continue;
            }
            // Extract five-hour utilisation percentage if present.
            let five_h_pct = entry
                .get("usage")
                .and_then(|u| u.get("five_hour"))
                .and_then(|fh| fh.get("utilization"))
                .and_then(|p| p.as_f64());
            match five_h_pct {
                Some(pct) => summary.push(format!("{account_id}: {:.0}% (5h)", pct * 100.0)),
                None => summary.push(format!("{account_id}: ok")),
            }
        }
        state.fleet_summary = summary;
    }

    state
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::mpsc;

    /// Integration test: verify the cache polling loop sends TrayStateUpdate
    /// messages at approximately 10-second intervals using tokio::time::pause.
    ///
    /// Purpose: confirm that the polling loop respects the 10-second interval
    ///   as specified in T-P2-E06-12 acceptance criteria.
    ///
    /// Test strategy:
    ///   1. Enable tokio::time::pause (deterministic time) so the test doesn't
    ///      wait 10+ seconds of wall-clock time.
    ///   2. Spawn the poller.
    ///   3. Advance time to 10s + beyond the second tick.
    ///   4. Verify exactly 3 updates were sent:
    ///      - 1 initial (the interval's immediate first tick at t=0)
    ///      - 2 on the next two 10s ticks
    ///
    /// WHY no spawned drain task: `rx` is a std::sync::mpsc::Receiver whose
    ///   `recv()` BLOCKS the calling thread. Calling it inside a tokio task on
    ///   the current-thread runtime (which time::pause() forces) parks the only
    ///   worker thread and deadlocks the test forever. Instead we drain the
    ///   channel synchronously with the non-blocking `try_recv()` after
    ///   advancing time and aborting the poller.
    #[tokio::test]
    async fn cache_poller_sends_updates_every_10s_with_paused_time() {
        // T-P7-E05-04: this poller now observes DAEMON_PAUSED; serialize
        // against tests that flip the process-global flag.
        let _pause_guard = crate::supervisor::TEST_PAUSE_LOCK.lock().await;

        // Enable deterministic time so we can fast-forward without sleeping.
        time::pause();

        let (tx, rx) = mpsc::channel::<TrayStateUpdate>();

        // Spawn the cache poller. It holds its own clone of the sender.
        let poller_task = spawn_status_cache_poller(tx.clone());
        drop(tx); // Drop the original sender; only the poller's clone remains.

        let mut recorded = Vec::new();

        // Pump the runtime until a specific count of updates has been observed,
        // or a bounded number of yield rounds elapse. Bounded => cannot hang.
        // WHY: the current-thread runtime does not guarantee the spawned poller
        // runs to its next send within a single yield_now(); we pump until the
        // expected message is drained.
        async fn pump_until(
            rx: &mpsc::Receiver<TrayStateUpdate>,
            recorded: &mut Vec<TrayStateUpdate>,
            want: usize,
        ) {
            for _ in 0..100 {
                tokio::task::yield_now().await;
                while let Ok(msg) = rx.try_recv() {
                    recorded.push(msg);
                }
                if recorded.len() >= want {
                    break;
                }
            }
        }

        // The interval's immediate first tick produces the initial update.
        pump_until(&rx, &mut recorded, 1).await;

        // Advance time by 10s + small buffer for the second tick to fire.
        time::advance(Duration::from_secs(10) + Duration::from_millis(100)).await;
        pump_until(&rx, &mut recorded, 2).await;

        // Advance by another 10s for the third tick.
        time::advance(Duration::from_secs(10) + Duration::from_millis(100)).await;
        pump_until(&rx, &mut recorded, 3).await;

        // Stop the poller, then drain any straggler that slipped in.
        poller_task.abort();
        while let Ok(msg) = rx.try_recv() {
            recorded.push(msg);
        }

        // Verify we got exactly 3 updates:
        //   1 initial (immediate first tick at t=0)
        //   2 more (on the next two 10s ticks)
        assert_eq!(
            recorded.len(),
            3,
            "expected 3 updates (initial + 2 at 10s intervals); got {}",
            recorded.len()
        );

        // Verify each update is an UpdateState (not Shutdown or other variants).
        for (idx, update) in recorded.iter().enumerate() {
            match update {
                TrayStateUpdate::UpdateState(_) => {
                    // Expected.
                }
                other => {
                    panic!("update {} was {:?}, expected UpdateState", idx, other);
                }
            }
        }
    }

    /// Unit test: verify initial state is sent immediately.
    ///
    /// Acceptance criterion: "Initial TrayStateUpdate is sent before first poll
    /// interval (immediate on daemon start)".
    ///
    /// Test strategy:
    ///   1. Spawn the poller with time paused.
    ///   2. Yield once so the immediate first tick fires (no time advance).
    ///   3. Drain non-blockingly and verify we got at least 1 update.
    #[tokio::test]
    async fn cache_poller_sends_initial_update_immediately() {
        // T-P7-E05-04: same flag serialization as the interval test above.
        let _pause_guard = crate::supervisor::TEST_PAUSE_LOCK.lock().await;

        time::pause();

        let (tx, rx) = mpsc::channel::<TrayStateUpdate>();

        let poller_task = spawn_status_cache_poller(tx.clone());
        drop(tx);

        // Drive the immediate first tick deterministically. Under
        // tokio::time::pause(), an interval tick only fires when time ADVANCES
        // past its deadline — a bare poll never marks it elapsed. The interval's
        // first deadline is the moment the poller is first polled (registers the
        // timer), so we interleave: yield (let the poller register/run) + a
        // tiny advance (fire the now-past first deadline). Bounded to 100 rounds
        // => cannot hang. Total advance stays far under the 10s period, so ONLY
        // the immediate first tick fires here, proving immediacy on daemon start.
        let mut recorded = Vec::new();
        for _ in 0..100 {
            tokio::task::yield_now().await;
            time::advance(Duration::from_millis(1)).await;
            while let Ok(msg) = rx.try_recv() {
                recorded.push(msg);
            }
            if !recorded.is_empty() {
                break;
            }
        }

        poller_task.abort();

        assert!(
            !recorded.is_empty(),
            "expected at least 1 update (the initial one); got 0"
        );
    }

    /// T-P7-E05-04 behavior test: while DAEMON_PAUSED is set the polling loop
    /// emits NO tray updates (fetch + send are skipped), and after unpause the
    /// next 10 s tick emits again. Replaces the old tautological flag-toggle
    /// test in main.rs, which proved nothing about any consumer of the flag.
    ///
    /// Test strategy (follows `cache_poller_sends_updates_every_10s_with_paused_time`):
    ///   1. Deterministic time; spawn the poller; drain the initial update.
    ///   2. Set DAEMON_PAUSED; advance past TWO tick deadlines; pump yields;
    ///      assert the recorded count is unchanged.
    ///   3. Clear DAEMON_PAUSED; advance past ONE tick; pump until the next
    ///      update arrives; abort + drain stragglers; assert EXACTLY one more
    ///      update arrived in total (a leaked paused-window send would show up
    ///      as a third).
    #[tokio::test]
    async fn cache_poller_skips_updates_while_daemon_paused_and_resumes() {
        let _pause_guard = crate::supervisor::TEST_PAUSE_LOCK.lock().await;
        DAEMON_PAUSED.store(false, Ordering::SeqCst);

        time::pause();

        let (tx, rx) = mpsc::channel::<TrayStateUpdate>();
        let poller_task = spawn_status_cache_poller(tx.clone());
        drop(tx);

        let mut recorded = Vec::new();

        // Same bounded pump as the interval test: yield rounds + non-blocking
        // drain until `want` updates are observed (cannot hang).
        async fn pump_until(
            rx: &mpsc::Receiver<TrayStateUpdate>,
            recorded: &mut Vec<TrayStateUpdate>,
            want: usize,
        ) {
            for _ in 0..100 {
                tokio::task::yield_now().await;
                while let Ok(msg) = rx.try_recv() {
                    recorded.push(msg);
                }
                if recorded.len() >= want {
                    break;
                }
            }
        }

        // Initial update (the interval's immediate first tick). Under paused
        // time a tick only fires once time advances past its deadline — a
        // bare poll never marks it elapsed (same caveat the immediacy test
        // documents) — so interleave yields with 1 ms advances until it
        // lands. Total advance stays far under the 10 s period, so ONLY the
        // initial tick can fire here.
        for _ in 0..100 {
            tokio::task::yield_now().await;
            time::advance(Duration::from_millis(1)).await;
            while let Ok(msg) = rx.try_recv() {
                recorded.push(msg);
            }
            if !recorded.is_empty() {
                break;
            }
        }
        assert_eq!(recorded.len(), 1, "expected exactly the initial update");

        // ── Paused: two full 10 s ticks must emit NOTHING. ─────────────────
        DAEMON_PAUSED.store(true, Ordering::SeqCst);
        time::advance(Duration::from_secs(20)).await;
        // Give any (wrong) send ample scheduling room to surface, then drain.
        for _ in 0..100 {
            tokio::task::yield_now().await;
        }
        while let Ok(msg) = rx.try_recv() {
            recorded.push(msg);
        }
        assert_eq!(
            recorded.len(),
            1,
            "no tray updates expected while DAEMON_PAUSED is set; got {}",
            recorded.len()
        );

        // ── Unpaused: the next 10 s tick emits again. ──────────────────────
        DAEMON_PAUSED.store(false, Ordering::SeqCst);
        time::advance(Duration::from_secs(10) + Duration::from_millis(100)).await;
        pump_until(&rx, &mut recorded, 2).await;

        poller_task.abort();
        // Drain any straggler: total must be exactly 2 (a leaked send from the
        // paused window would make it 3).
        while let Ok(msg) = rx.try_recv() {
            recorded.push(msg);
        }
        assert_eq!(
            recorded.len(),
            2,
            "expected exactly one resumed update (initial + 1); got {}",
            recorded.len()
        );

        // Reset the process-global flag for other tests.
        DAEMON_PAUSED.store(false, Ordering::SeqCst);
    }
}
