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

use std::sync::mpsc;
use std::time::Duration;

use cascade_tray::TrayState;
use tokio::time;
use tracing::{error, info};

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

            // Fetch the current cache state (stub until E-03 is done).
            // Future: call status_cache::fetch() here when E-03 lands.
            let state = fetch_tray_state_stub();

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

/// Fetch a TrayState snapshot from the cache (stub until E-03 StatusCache lands).
///
/// Purpose: compute a TrayState from the current daemon state. During P2
///   (before E-03 is integrated) this returns a sensible default so polling
///   infrastructure can be tested. When E-03 StatusCache lands, this function
///   will be replaced with calls to status_cache::get_state() or similar.
///
/// Outputs: `TrayState` with sane defaults (all zeros except daemon_status = Running).
///
/// WHY stub: E-03 (StatusCache persistent state) is a separate epic.
///   This task polls every 10 seconds per spec; the actual cache integration
///   is completed when E-03 lands (T-P2-E03-*).
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon
fn fetch_tray_state_stub() -> TrayState {
    use cascade_tray::DaemonStatus;

    TrayState {
        daemon_status: DaemonStatus::Running,
        ..TrayState::default()
    }
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
}
