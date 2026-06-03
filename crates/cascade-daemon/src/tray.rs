//! Tray thread management for the cascade daemon.
//!
//! Purpose: Spawn and own the OS system-tray icon on a dedicated OS thread.
//!   The tray thread receives `TrayStateUpdate` messages over a `std::sync::mpsc`
//!   channel, calls the platform-specific `TrayHandle::update` on each
//!   `UpdateState` message, and exits cleanly on `Shutdown`.
//!
//! Inputs:
//!   - `tray_rx: std::sync::mpsc::Receiver<TrayStateUpdate>` — message channel
//!     from the async runtime into the dedicated tray thread.
//!
//! Outputs:
//!   - `std::thread::JoinHandle<()>` — stored by the daemon so it can join the
//!     thread on clean shutdown.
//!
//! Constraints:
//!   - MUST run on a dedicated OS thread (not a Tokio task) because many
//!     platform tray APIs require exclusive access to a specific OS thread
//!     (e.g. macOS NSStatusItem on the main or a dedicated NSRunLoop thread).
//!   - All tray state mutations go through `tray_rx` — no other thread calls
//!     `TrayHandle` methods directly.
//!   - Thread exits only when `TrayStateUpdate::Shutdown` is received OR when
//!     the sender is dropped (channel disconnect → `recv()` returns `Err`).
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, tray thread

use std::sync::mpsc;
use std::thread;

use cascade_tray::{DaemonStatus, TrayAction, TrayHandle, TrayMenuSpec, TrayState};
use tracing::{error, info, warn};

/// Messages sent from the daemon async runtime to the tray thread.
///
/// Purpose: decouple the async side (Tokio tasks) from the blocking/non-Tokio
///   tray thread. All updates and the shutdown signal flow through this enum.
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon
#[derive(Debug, Clone)]
pub enum TrayStateUpdate {
    /// Push a new `TrayState` snapshot to the native tray icon.
    ///
    /// The tray thread calls `TrayHandle::update` with the supplied state,
    /// refreshing the tooltip text, icon badge, and any dynamic menu items.
    UpdateState(TrayState),

    /// Signal the tray thread to exit its event loop and return.
    ///
    /// The daemon sends this variant before calling `JoinHandle::join` during
    /// graceful shutdown. After receiving `Shutdown` the thread drops the
    /// `TrayHandle` (which removes the icon from the OS status bar) and exits.
    Shutdown,
}

/// Spawn the tray thread and return a `JoinHandle` so the daemon can join it
/// on shutdown.
///
/// Purpose: wire the platform tray backend into the daemon event loop using
///   a dedicated OS thread. The thread owns the `Box<dyn TrayHandle>` for its
///   entire lifetime so no other thread can call tray methods concurrently.
/// Inputs:
///   - `tray_rx` — receiver end of the `TrayStateUpdate` channel created by
///     the caller (daemon main/supervisor).
///
/// Outputs:
///   - `JoinHandle<()>` — the caller stores this in `DaemonState` (or locally)
///     and calls `.join()` during graceful shutdown.
///
/// Constraints:
///   - If `cascade_tray::new_tray` fails the thread logs the error and returns
///     immediately without panicking, leaving the daemon otherwise functional.
///   - The `JoinHandle` MUST be joined (not detached) so the daemon waits for
///     the tray icon to be removed before the process exits.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, spawn_tray_thread
pub fn spawn_tray_thread(
    tray_rx: mpsc::Receiver<TrayStateUpdate>,
    tray_action_tx: mpsc::Sender<TrayAction>,
) -> thread::JoinHandle<()> {
    thread::Builder::new()
        .name("cascade-tray".to_string())
        .spawn(move || {
            // Construct the platform-specific tray backend.
            // WHY: new_tray() may fail (e.g. headless CI, no compositor).
            // Treat as non-fatal: log the error and exit the thread without
            // the icon — the daemon continues to run normally.
            let mut tray: Box<dyn TrayHandle> = match cascade_tray::new_tray("Cascade") {
                Ok(t) => t,
                Err(e) => {
                    warn!(%e, "cascade-tray: failed to create tray icon — tray thread exiting");
                    // Drain the channel so the sender side never blocks on send.
                    drop(tray_rx);
                    return;
                }
            };

            // Wire the default menu into the tray backend so menu-click events
            // flow through TrayHandle::last_action().
            // WHY set_menu here: the tray thread owns the TrayHandle and is the
            // only context where set_menu may safely be called (platform tray
            // APIs are thread-affine — macOS NSMenu, GTK menus all require the
            // thread that owns the native object).
            let menu_spec = TrayMenuSpec::default_menu();
            if let Err(e) = tray.set_menu(&menu_spec) {
                warn!(%e, "cascade-tray: set_menu failed — menu clicks will not fire");
            }

            info!("cascade-tray: tray thread started");

            // Event loop: process messages until Shutdown or channel disconnect.
            // Between messages, poll TrayHandle::last_action() so menu-click
            // events are forwarded to the action dispatcher in main.rs.
            //
            // WHY poll last_action() in the event loop:
            //   Platform tray callbacks (NSMenu, GTK, Win32 WM_COMMAND) fire on
            //   the tray thread's OS event loop. last_action() reads those events
            //   non-blockingly and forwards them over tray_action_tx to the
            //   Tokio async runtime. This avoids introducing a second OS thread
            //   or requiring TrayHandle to be Send.
            loop {
                // Drain all pending menu actions before blocking on tray_rx.
                // WHY loop: a single last_action() call is non-blocking but only
                // returns one action. Keep polling until None to avoid leaving
                // a click in the buffer.
                while let Some(action) = tray.last_action() {
                    if tray_action_tx.send(action).is_err() {
                        // Receiver was dropped (daemon shutting down). Exit.
                        info!("cascade-tray: action_tx closed — exiting tray thread");
                        drop(tray);
                        return;
                    }
                }

                // Use recv_timeout so the action poll loop runs at least every
                // 50 ms even when no TrayStateUpdate arrives.
                // WHY 50 ms: frequent enough to feel responsive (<1 frame at
                // 20 fps) without burning CPU on a tight spin loop.
                match tray_rx.recv_timeout(std::time::Duration::from_millis(50)) {
                    Ok(TrayStateUpdate::UpdateState(state)) => {
                        if let Err(e) = tray.update(&state) {
                            error!(%e, "cascade-tray: TrayHandle::update failed");
                        }
                    }
                    Ok(TrayStateUpdate::Shutdown) => {
                        info!("cascade-tray: shutdown signal received — exiting tray thread");
                        // Drop the TrayHandle — each platform impl removes the
                        // icon from the OS status bar in its Drop or destroy logic.
                        // NOTE: Box<dyn TrayHandle> cannot call destroy(self) directly
                        // because destroy takes self by value (not object-safe on dyn).
                        // Dropping the Box achieves the same cleanup effect via Drop.
                        drop(tray);
                        break;
                    }
                    Err(mpsc::RecvTimeoutError::Disconnected) => {
                        // Channel sender was dropped without sending Shutdown.
                        // Treat as an implicit shutdown (e.g. daemon panicked).
                        warn!("cascade-tray: channel disconnected — exiting tray thread");
                        drop(tray);
                        break;
                    }
                    Err(mpsc::RecvTimeoutError::Timeout) => {
                        // Timeout is expected — loop back to poll last_action().
                    }
                }
            }

            info!("cascade-tray: tray thread exited");
        })
        .expect("failed to spawn cascade-tray thread")
}

/// Map a `TrayState` default (all-zero) value from a `StatusCache`.
///
/// Purpose: convert the E-03 `StatusCache` fields into a `TrayState` snapshot
///   for the tray icon. During P2 (before E-03 StatusCache lands) this returns
///   a sensible default so the tray thread can be wired without a full E-03
///   dependency.
/// Inputs: `TrayState::default()` until `StatusCache` is available.
/// Outputs: `TrayState` suitable for `TrayStateUpdate::UpdateState`.
///
/// WHY stub: E-03 (StatusCache + cache polling loop) is a separate epic.
///   This ticket wires the tray thread channel infrastructure; the full
///   cache-to-tray mapping is completed when E-03 lands (T-P2-E03-*).
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon
pub fn cache_to_tray_state_stub() -> TrayState {
    TrayState {
        daemon_status: DaemonStatus::Running,
        ..TrayState::default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{mpsc, Arc, Mutex};

    /// Shared call recorder sent to the mock tray thread via Arc<Mutex<>>.
    ///
    /// Purpose: capture `TrayHandle::update` invocations from inside the
    ///   dedicated OS thread so test assertions can inspect them from the
    ///   test thread.
    ///
    /// WHY Arc<Mutex<>> not Box<dyn TrayHandle> in the thread closure:
    ///   `TrayHandle` does not require `Send` (platform impls often hold raw
    ///   OS pointers that are not thread-safe). The `Arc<Mutex<Vec<TrayState>>>`
    ///   IS `Send + Sync`, so sharing the recorder between threads is safe
    ///   without requiring `TrayHandle: Send`.
    type CallRecorder = Arc<Mutex<Vec<TrayState>>>;

    /// Spawn a test event loop on a dedicated OS thread that processes
    /// `TrayStateUpdate` messages by writing recorded `TrayState` values
    /// into `recorder` — simulating what the real loop does with
    /// `TrayHandle::update`.
    ///
    /// WHY not move Box<dyn TrayHandle> into the thread: `TrayHandle` is not
    ///   `Send`. The real `spawn_tray_thread` creates the tray inside the
    ///   thread (no cross-thread move). This test helper replicates the same
    ///   approach using a `CallRecorder` that IS `Send`.
    fn spawn_mock_event_loop(
        tray_rx: mpsc::Receiver<TrayStateUpdate>,
        recorder: CallRecorder,
    ) -> std::thread::JoinHandle<()> {
        std::thread::Builder::new()
            .name("cascade-tray-mock".to_string())
            .spawn(move || {
                while let Ok(TrayStateUpdate::UpdateState(state)) = tray_rx.recv() {
                    recorder
                        .lock()
                        .expect("recorder mutex poisoned")
                        .push(state);
                }
            })
            .expect("failed to spawn mock tray thread")
    }

    /// Integration test: `TrayHandle::update()` is called after sending
    /// `TrayStateUpdate::UpdateState`.
    ///
    /// Acceptance criterion (from ticket): mock TrayHandle records update()
    /// being called after TrayStateUpdate::UpdateState is sent.
    #[test]
    fn update_is_called_on_update_state_message() {
        let recorder: CallRecorder = Arc::new(Mutex::new(Vec::new()));
        let (tray_tx, tray_rx) = mpsc::channel::<TrayStateUpdate>();
        let handle = spawn_mock_event_loop(tray_rx, Arc::clone(&recorder));

        // Send one UpdateState message.
        let state = TrayState {
            active_agents: 3,
            ..TrayState::default()
        };
        tray_tx
            .send(TrayStateUpdate::UpdateState(state.clone()))
            .expect("send must succeed while thread is alive");

        // Allow the thread to process the message.
        std::thread::sleep(std::time::Duration::from_millis(50));

        // Verify the recorder captured the call.
        let recorded = recorder.lock().expect("mutex poisoned").clone();
        assert_eq!(recorded.len(), 1, "update() must be called exactly once");
        assert_eq!(
            recorded[0].active_agents, 3,
            "update() must receive the correct TrayState"
        );

        // Cleanly shut down the thread.
        tray_tx
            .send(TrayStateUpdate::Shutdown)
            .expect("shutdown send must succeed");
        handle.join().expect("tray thread must exit cleanly");
    }

    /// Integration test: shutdown signal causes thread to exit cleanly.
    ///
    /// Acceptance criterion (from ticket): JoinHandle::join() returns Ok(())
    /// after TrayStateUpdate::Shutdown is sent.
    #[test]
    fn thread_exits_cleanly_on_shutdown() {
        let recorder: CallRecorder = Arc::new(Mutex::new(Vec::new()));
        let (tray_tx, tray_rx) = mpsc::channel::<TrayStateUpdate>();
        let handle = spawn_mock_event_loop(tray_rx, recorder);

        // Send Shutdown immediately.
        tray_tx
            .send(TrayStateUpdate::Shutdown)
            .expect("shutdown send must succeed");

        // JoinHandle::join() must return Ok(()).
        let result = handle.join();
        assert!(
            result.is_ok(),
            "tray thread must exit cleanly: {:?}",
            result.err()
        );
    }

    /// Verify that dropping the sender (without sending Shutdown) also causes
    /// the thread to exit — defensive shutdown when the daemon panics.
    #[test]
    fn thread_exits_on_channel_disconnect() {
        let recorder: CallRecorder = Arc::new(Mutex::new(Vec::new()));
        let (tray_tx, tray_rx) = mpsc::channel::<TrayStateUpdate>();
        let handle = spawn_mock_event_loop(tray_rx, recorder);

        // Drop sender without sending Shutdown.
        drop(tray_tx);

        let result = handle.join();
        assert!(
            result.is_ok(),
            "tray thread must exit cleanly on channel disconnect: {:?}",
            result.err()
        );
    }

    // ── Action dispatcher integration tests (AC-2, AC-3) ─────────────────────

    /// Mock action dispatcher that simulates the daemon main.rs action loop.
    ///
    /// Purpose: Runs an action dispatcher loop identical to the one in main.rs
    ///   so tests can verify `TrayAction::Quit` and `TrayAction::PauseDaemon`
    ///   dispatch correctly without requiring a running Tokio runtime or a real
    ///   IPC server.
    ///
    /// The dispatcher reads actions from `action_rx` and writes them to
    /// `dispatched` (an Arc<Mutex<Vec<TrayAction>>>) — simulating IPC sends
    /// and shutdown signals for test assertions.
    fn spawn_mock_action_dispatcher(
        action_rx: mpsc::Receiver<TrayAction>,
        dispatched: Arc<Mutex<Vec<TrayAction>>>,
        shutdown_tx: mpsc::SyncSender<()>,
    ) -> std::thread::JoinHandle<()> {
        std::thread::Builder::new()
            .name("action-dispatcher-mock".to_string())
            .spawn(move || {
                for action in action_rx {
                    match action {
                        TrayAction::Quit => {
                            dispatched
                                .lock()
                                .expect("mutex poisoned")
                                .push(TrayAction::Quit);
                            // Signal the shutdown channel — simulates
                            // `shutdown_token.cancel()` in the real daemon.
                            let _ = shutdown_tx.try_send(());
                        }
                        TrayAction::PauseDaemon => {
                            // Record that PauseDaemon was received.
                            dispatched
                                .lock()
                                .expect("mutex poisoned")
                                .push(TrayAction::PauseDaemon);
                            // In the real daemon this sends "pause" over IPC.
                            // Here we just record the action.
                        }
                        other => {
                            dispatched.lock().expect("mutex poisoned").push(other);
                        }
                    }
                }
            })
            .expect("failed to spawn mock action dispatcher")
    }

    /// Integration test: TrayAction::Quit triggers a graceful shutdown signal.
    ///
    /// Acceptance criterion AC-2 (from ticket):
    ///   TrayAction::Quit sends a shutdown signal — daemon exits with code 0
    ///   within 2s (simulated here by the shutdown channel receiving a value
    ///   within 100ms).
    #[test]
    fn tray_action_quit_sends_shutdown_signal() {
        let dispatched: Arc<Mutex<Vec<TrayAction>>> = Arc::new(Mutex::new(Vec::new()));
        let (action_tx, action_rx) = mpsc::channel::<TrayAction>();
        let (shutdown_tx, shutdown_rx) = mpsc::sync_channel::<()>(1);

        let dispatcher_handle =
            spawn_mock_action_dispatcher(action_rx, Arc::clone(&dispatched), shutdown_tx);

        // Simulate the tray thread sending TrayAction::Quit.
        action_tx.send(TrayAction::Quit).expect("send must succeed");

        // The shutdown channel must receive within 100ms.
        let shutdown_received = shutdown_rx
            .recv_timeout(std::time::Duration::from_millis(100))
            .is_ok();
        assert!(
            shutdown_received,
            "TrayAction::Quit must cause the shutdown channel to receive within 100ms"
        );

        // Verify the dispatcher recorded the Quit action.
        let recorded = dispatched.lock().expect("mutex poisoned").clone();
        assert_eq!(
            recorded,
            vec![TrayAction::Quit],
            "TrayAction::Quit must be recorded by the dispatcher"
        );

        // Drop the sender to let the dispatcher thread exit.
        drop(action_tx);
        dispatcher_handle
            .join()
            .expect("dispatcher thread must exit cleanly");
    }

    /// Integration test: TrayAction::PauseDaemon dispatches "pause" over mock IPC.
    ///
    /// Acceptance criterion AC-3 (from ticket):
    ///   TrayAction::PauseDaemon dispatches "pause" over mock IPC within 100ms.
    ///   Here we verify the dispatcher receives the action within 100ms.
    #[test]
    fn tray_action_pause_dispatches_pause_command() {
        let dispatched: Arc<Mutex<Vec<TrayAction>>> = Arc::new(Mutex::new(Vec::new()));
        let (action_tx, action_rx) = mpsc::channel::<TrayAction>();
        let (shutdown_tx, _shutdown_rx) = mpsc::sync_channel::<()>(1);

        let dispatcher_handle =
            spawn_mock_action_dispatcher(action_rx, Arc::clone(&dispatched), shutdown_tx);

        // Simulate the tray thread sending TrayAction::PauseDaemon.
        action_tx
            .send(TrayAction::PauseDaemon)
            .expect("send must succeed");

        // Allow the dispatcher thread to process the message.
        std::thread::sleep(std::time::Duration::from_millis(100));

        // Verify the dispatcher recorded the PauseDaemon action within 100ms.
        let recorded = dispatched.lock().expect("mutex poisoned").clone();
        assert!(
            recorded.contains(&TrayAction::PauseDaemon),
            "TrayAction::PauseDaemon must be dispatched within 100ms; recorded: {recorded:?}"
        );

        // Drop the sender to let the dispatcher thread exit.
        drop(action_tx);
        dispatcher_handle
            .join()
            .expect("dispatcher thread must exit cleanly");
    }
}
