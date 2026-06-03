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
/// Outputs:
///   - `JoinHandle<()>` so the supervisor can cancel or join the task
///     during shutdown.
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

        // Send initial TrayStateUpdate immediately (no delay on daemon start).
        // WHY: users expect the tray to show the current status right away, not
        // after the first 10s poll tick.
        let initial_state = fetch_tray_state_stub();
        if let Err(e) = tray_tx.send(TrayStateUpdate::UpdateState(initial_state)) {
            error!(%e, "cache polling loop: initial send failed (tray thread may have exited)");
            // Continue anyway — the polling loop keeps running in case the
            // tray thread restarts or reconnects.
        }

        // Create a 10-second interval. The first tick completes after 10 seconds;
        // subsequent ticks fire every 10 seconds thereafter.
        // WHY tokio::time::interval: It's precise and plays nicely with
        // tokio::select! for shutdown cancellation (future enhancement).
        let mut interval = time::interval(Duration::from_secs(10));

        loop {
            // Wait for the next 10-second tick.
            interval.tick().await;

            // Fetch the current cache state (stub until E-03 is done).
            // Future: call status_cache::fetch() here when E-03 lands.
            let state = fetch_tray_state_stub();

            // Send the TrayStateUpdate to the tray thread.
            if let Err(e) = tray_tx.send(TrayStateUpdate::UpdateState(state)) {
                // The tray thread may have exited (e.g. early daemon shutdown,
                // TrayHandle init failed on headless CI). Log and continue polling.
                error!(%e, "cache polling loop: send failed at 10s tick (tray thread exited)");
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
    use std::sync::{mpsc, Arc, Mutex};
    use std::time::Instant;

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
    ///      - 1 initial (immediate)
    ///      - 2 on the first two 10s ticks
    ///
    /// WHY Arc<Mutex<>>: Same as in tray.rs — we need a Send + Sync container
    ///   to capture updates from inside the spawned task.
    #[tokio::test]
    async fn cache_poller_sends_updates_every_10s_with_paused_time() {
        // Enable deterministic time so we can fast-forward without sleeping.
        time::pause();

        // Create a channel and wrapped receiver for capturing updates.
        let (tx, rx) = mpsc::channel::<TrayStateUpdate>();
        let updates: Arc<Mutex<Vec<TrayStateUpdate>>> = Arc::new(Mutex::new(Vec::new()));
        let updates_clone = Arc::clone(&updates);

        // Spawn a task that drains the receiver into the Arc<Mutex<>>.
        // This simulates the tray thread receiving and processing updates.
        let drain_task = tokio::spawn(async move {
            while let Ok(msg) = rx.recv() {
                updates_clone.lock().expect("mutex poisoned").push(msg);
            }
        });

        // Spawn the cache poller.
        let poller_task = spawn_status_cache_poller(tx.clone());
        drop(tx); // Drop the original sender so the drain task can exit when done.

        // Allow the initial send to complete (it's immediate, no await needed,
        // but yield to let the async task run).
        tokio::task::yield_now().await;

        // Advance time by 10s + small buffer for the first tick to fire.
        time::advance(Duration::from_secs(10) + Duration::from_millis(100)).await;

        // Allow the task to send the first 10s update.
        tokio::task::yield_now().await;

        // Advance by another 10s for the second tick.
        time::advance(Duration::from_secs(10) + Duration::from_millis(100)).await;

        // Allow the task to send the second 10s update.
        tokio::task::yield_now().await;

        // Cancel the poller task and drain task to end the test.
        poller_task.abort();
        drain_task.abort();

        // Small sleep to let any pending sends complete (best-effort).
        tokio::time::sleep(Duration::from_millis(50)).await;

        // Verify we got exactly 3 updates:
        //   1 initial (before first interval)
        //   2 more (on first and second 10s ticks)
        let recorded = updates.lock().expect("mutex poisoned").clone();
        assert_eq!(
            recorded.len(),
            3,
            "expected 3 updates (1 initial + 2 at 10s intervals); got {}",
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
    ///   2. Before advancing time, check if the initial update was sent.
    ///   3. Verify we got at least 1 update.
    #[tokio::test]
    async fn cache_poller_sends_initial_update_immediately() {
        time::pause();

        let (tx, rx) = mpsc::channel::<TrayStateUpdate>();
        let updates: Arc<Mutex<Vec<TrayStateUpdate>>> = Arc::new(Mutex::new(Vec::new()));
        let updates_clone = Arc::clone(&updates);

        let drain_task = tokio::spawn(async move {
            while let Ok(msg) = rx.recv() {
                updates_clone.lock().expect("mutex poisoned").push(msg);
            }
        });

        let poller_task = spawn_status_cache_poller(tx.clone());
        drop(tx);

        // Let the initial send complete without advancing time.
        tokio::task::yield_now().await;

        poller_task.abort();
        drain_task.abort();

        let recorded = updates.lock().expect("mutex poisoned").clone();
        assert!(
            !recorded.is_empty(),
            "expected at least 1 update (the initial one); got 0"
        );
    }
}
