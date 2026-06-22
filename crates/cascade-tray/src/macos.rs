//! # macos — macOS tray backend for cascade-tray
//!
//! Purpose: Implement [`TrayHandle`] for macOS using the `tray-item` crate
//!   (NSStatusItem via Cocoa bindings). Provides a system-tray icon in the
//!   macOS menu bar that displays Cascade daemon telemetry as a tooltip.
//!
//! Inputs:
//!   - `label: &str` — initial display label / icon title for the status item.
//!   - `TrayState` snapshots pushed by the daemon via `update()`.
//!
//! Outputs:
//!   - A visible NSStatusItem in the macOS menu bar.
//!   - A tooltip string formatted as
//!     `"Cascade: {total_rules} rules | {rag_fresh} | {agents} agents | {unread} inbox"`.
//!
//! Constraints:
//!   - All code in this module is gated with `#[cfg(target_os = "macos")]`.
//!   - `tray-item` 0.9 does not expose `set_tooltip` on macOS; the tooltip
//!     string is tracked internally and can be retrieved via `last_tooltip()`.
//!   - Compiles on both aarch64-apple-darwin and x86_64-apple-darwin without
//!     any target-architecture conditional logic.
//!   - `show()` and `hide()` are no-ops: NSStatusItem is always visible once
//!     created (tray-item macOS doesn't support programmatic hide/show).
//!   - `destroy()` consumes `self`; dropping the inner `TrayItem` removes the
//!     NSStatusItem from the status bar.
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray (macOS impl done)

#[cfg(target_os = "macos")]
use tray_item::{IconSource, TrayItem};

use std::sync::mpsc;

use crate::{TrayAction, TrayError, TrayHandle, TrayMenuItem, TrayMenuSpec, TrayState};

/// Build the tooltip string for a given [`TrayState`].
///
/// Purpose: Pure function — compute the formatted tooltip text that should
///   appear when the user hovers over the Cascade tray icon.
/// Inputs: a reference to the current [`TrayState`].
/// Outputs: a `String` of the form
///   `"Cascade: {total_rules} rules | {rag_fresh} | {agents} agents | {unread} inbox"`.
/// Constraints: deterministic; does not interact with the OS.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
pub fn tooltip_string(state: &TrayState) -> String {
    let total_rules: u32 = state.tier_counts.values().sum();
    let rag_fresh = if state.rag_freshness_secs < 300 {
        "index fresh"
    } else {
        "index stale"
    };
    let base = format!(
        "Cascade: {total_rules} rules | {rag_fresh} | {} agents | {} inbox",
        state.active_agents, state.inbox_unread,
    );
    if state.fleet_summary.is_empty() {
        base
    } else {
        format!("{base} | Fleet: {}", state.fleet_summary.join(", "))
    }
}

/// macOS implementation of [`TrayHandle`] backed by `tray-item` 0.9.
///
/// Purpose: Drive an NSStatusItem in the macOS menu bar and surface
///   Cascade daemon telemetry as a tooltip. Delivers menu-click events via an
///   optional `mpsc::Sender<TrayAction>`.
/// Inputs: constructed via [`MacosTrayImpl::new`]; updated via `TrayHandle::update`.
///   Menu clicks reported via the channel set with `set_menu()`.
/// Outputs: mutates the tray icon title; tracks the last tooltip string;
///   fires `TrayAction` values on menu-item clicks.
/// Constraints:
///   - `tray-item` 0.9 macOS does not expose `set_tooltip`; this impl stores
///     the last computed tooltip for inspection and uses `add_label` to update
///     the visible menu label instead.
///   - Must be constructed and used on the main thread (Cocoa requirement).
///   - `set_menu()` appends items via `add_menu_item`; tray-item 0.9 does not
///     support rebuilding the menu after the first call. Call `set_menu()` once
///     after construction.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
#[cfg(target_os = "macos")]
pub struct MacosTrayImpl {
    /// The underlying tray-item handle which wraps NSStatusItem.
    inner: TrayItem,
    /// The last tooltip string computed from the most recent `update()` call.
    /// Stored because tray-item 0.9 does not expose a tooltip setter on macOS.
    last_tooltip: String,
    /// Optional channel to deliver `TrayAction` values to the action dispatcher.
    /// Set when `set_menu()` is called.
    /// WHY Option: the tray can run without a menu (e.g. headless tests).
    action_tx: Option<mpsc::SyncSender<TrayAction>>,
}

#[cfg(target_os = "macos")]
impl MacosTrayImpl {
    /// Construct a new macOS tray icon with the given label.
    ///
    /// Purpose: Create the NSStatusItem and initialise the tray backend.
    /// Inputs: `label` — displayed as the status-bar icon title when no image
    ///   resource is found.
    /// Outputs: `Ok(MacosTrayImpl)` on success; `Err(TrayError::Platform)` if
    ///   tray-item fails to create the item (e.g. not running on the main thread).
    /// Constraints: must be called on the main thread.
    pub fn new(label: &str) -> Result<Self, TrayError> {
        // IconSource::Resource("") with an empty string tells tray-item to fall
        // back to a text label — avoids requiring a bundled image resource at
        // compile time. Replace "cascade_icon" with the asset name once icon
        // assets land (P3 scope).
        let inner = TrayItem::new(label, IconSource::Resource(""))
            .map_err(|e| TrayError::Platform(e.to_string()))?;
        Ok(Self {
            inner,
            last_tooltip: String::new(),
            action_tx: None,
        })
    }

    /// Return the last tooltip string computed by `update()`.
    ///
    /// Purpose: Primarily used in unit tests; also useful for diagnostics.
    pub fn last_tooltip(&self) -> &str {
        &self.last_tooltip
    }
}

#[cfg(target_os = "macos")]
impl TrayHandle for MacosTrayImpl {
    /// Refresh the tray tooltip from the supplied state snapshot.
    ///
    /// Purpose: Compute the formatted tooltip string and update the tray icon.
    /// Inputs: `state` — live telemetry snapshot from the cascade daemon.
    /// Outputs: `Ok(())` on success; `Err(TrayError::Platform)` on OS failure.
    /// Constraints: tray-item 0.9 macOS lacks `set_tooltip`; tooltip is stored
    ///   in `self.last_tooltip` and the menu label is refreshed as a proxy.
    fn update(&mut self, state: &TrayState) -> Result<(), TrayError> {
        let tooltip = tooltip_string(state);
        // tray-item 0.9 macOS does not expose set_tooltip; update the menu
        // label as a visible state indicator and track the string internally.
        // A future tray-item upgrade or a direct Cocoa binding can call
        // [statusItem setToolTip:] once available.
        self.inner
            .add_label(&tooltip)
            .map_err(|e| TrayError::Platform(e.to_string()))?;
        self.last_tooltip = tooltip;
        Ok(())
    }

    /// Show the tray icon.
    ///
    /// No-op on macOS: NSStatusItem is always visible once created by
    /// tray-item. Returns `Ok(())` unconditionally.
    fn show(&mut self) -> Result<(), TrayError> {
        // tray-item macOS always shows the icon — nothing to do.
        Ok(())
    }

    /// Hide the tray icon.
    ///
    /// No-op on macOS: tray-item 0.9 does not expose a programmatic hide API
    /// for NSStatusItem. The icon remains visible until `destroy()` is called.
    fn hide(&mut self) {
        // tray-item macOS does not support hiding — intentional no-op.
    }

    /// Release all native resources and remove the tray icon from the menu bar.
    ///
    /// Consumes `self`; dropping `inner` instructs tray-item to tear down the
    /// NSStatusItem via its `Drop` impl.
    fn destroy(self) {
        drop(self.inner);
    }

    /// Wire `TrayMenuSpec` items into the macOS NSMenu using `tray-item` callbacks.
    ///
    /// Purpose: Register a context-menu on the macOS status-bar icon. Each
    ///   `TrayMenuItem::Action` item is wired to a closure that sends the
    ///   corresponding `TrayAction` over a bounded sync channel. The channel
    ///   receiver is returned so callers can poll it on the tray thread.
    /// Inputs: `spec` — the menu specification to render.
    /// Outputs: `Ok(())` on success; `Err(TrayError::Platform)` if tray-item
    ///   rejects `add_menu_item`.
    /// Constraints:
    ///   - tray-item 0.9 does not support menu rebuilding; this method should be
    ///     called once after construction. Subsequent calls append new items
    ///     rather than replacing existing ones.
    ///   - Separators are not supported by tray-item 0.9's `add_menu_item` API;
    ///     they are silently skipped on macOS.
    ///   - The returned `Receiver<TrayAction>` MUST be polled by the tray thread's
    ///     event loop (see `cascade-daemon/src/tray.rs`).
    ///
    /// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray (menu wiring done)
    fn set_menu(&mut self, spec: &TrayMenuSpec) -> Result<(), TrayError> {
        // Create a bounded sync channel. Bound of 8 is enough to absorb a burst
        // of rapid menu clicks without blocking the NSMenu callback thread.
        // WHY SyncSender (not Sender): SyncSender is Clone + Send which allows
        // multiple menu-item closures to share it. A bounded buffer of 8 means
        // a misbehaving user clicking rapidly will not grow unbounded.
        let (tx, rx) = mpsc::sync_channel::<TrayAction>(8);
        self.action_tx = Some(tx.clone());

        for item in &spec.items {
            match item {
                TrayMenuItem::Action { label, action } => {
                    let sender = tx.clone();
                    let action = *action;
                    self.inner
                        .add_menu_item(label, move || {
                            // WHY try_send: the callback runs on the NSMenu thread.
                            // Blocking would freeze the menu UI. If the buffer is
                            // full (8 pending), drop the event — the user can
                            // click again.
                            let _ = sender.try_send(action);
                        })
                        .map_err(|e| TrayError::Platform(e.to_string()))?;
                }
                TrayMenuItem::Separator => {
                    // tray-item 0.9 does not expose a separator API on macOS.
                    // WHY no-op: skipping separators is preferable to returning
                    // an error that prevents menu items from being registered.
                }
            }
        }

        // Store the receiver in a thread-local so the tray event loop can poll
        // it. We use a module-level thread-local to avoid changing the
        // TrayHandle trait signature (which is object-safe and must stay clean).
        MACOS_ACTION_RX.with(|cell| {
            *cell.borrow_mut() = Some(rx);
        });

        Ok(())
    }

    /// Poll the most recently clicked menu action without blocking.
    ///
    /// Purpose: Called by the tray thread event loop after each message to
    ///   drain any pending menu-click events into the action dispatcher.
    /// Outputs: `Some(TrayAction)` if a menu item was clicked since the last
    ///   call; `None` otherwise.
    /// Constraints: non-blocking — uses `try_recv` internally.
    ///
    /// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
    fn last_action(&self) -> Option<TrayAction> {
        MACOS_ACTION_RX.with(|cell| cell.borrow().as_ref().and_then(|rx| rx.try_recv().ok()))
    }
}

// Thread-local storage for the macOS menu action receiver.
//
// WHY thread-local: tray-item 0.9's `add_menu_item` callbacks are `Send +
// Sync + 'static` closures. The receiver end of the channel must stay on the
// same thread that calls `last_action()`. Using a thread-local avoids
// introducing Mutex overhead and keeps the callback path lock-free.
//
// The RefCell is necessary because thread-local items are not automatically
// `RefCell`-wrapped and we need interior mutability to set the receiver when
// `set_menu()` is called.
//
// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
#[cfg(target_os = "macos")]
thread_local! {
    static MACOS_ACTION_RX: std::cell::RefCell<Option<mpsc::Receiver<TrayAction>>> =
        const { std::cell::RefCell::new(None) };
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{DaemonStatus, TrayState};
    use std::collections::HashMap;

    /// Verify tooltip_string produces the expected formatted string for a
    /// fully-populated TrayState.
    ///
    /// Acceptance criterion (from T-P2-E06-03):
    ///   TrayState{tier_counts:{GCI:2, PCI:3}, rag_freshness_secs:60,
    ///             active_agents:1, inbox_unread:0, daemon_status:Running}
    ///   → "Cascade: 5 rules | index fresh | 1 agents | 0 inbox"
    #[test]
    fn tooltip_string_populated_state() {
        let mut tier_counts = HashMap::new();
        tier_counts.insert("GCI".to_string(), 2u32);
        tier_counts.insert("PCI".to_string(), 3u32);

        let state = TrayState {
            tier_counts,
            rag_freshness_secs: 60,
            active_agents: 1,
            inbox_unread: 0,
            daemon_status: DaemonStatus::Running,
            fleet_summary: Vec::new(),
        };

        let result = tooltip_string(&state);
        assert_eq!(
            result, "Cascade: 5 rules | index fresh | 1 agents | 0 inbox",
            "tooltip must match the canonical format from the acceptance criteria"
        );
    }

    /// Verify tooltip_string uses "index stale" when rag_freshness_secs >= 300.
    #[test]
    fn tooltip_string_stale_index() {
        let state = TrayState {
            rag_freshness_secs: 300,
            active_agents: 0,
            inbox_unread: 2,
            daemon_status: DaemonStatus::Paused,
            ..Default::default()
        };

        let result = tooltip_string(&state);
        assert!(
            result.contains("index stale"),
            "tooltip must contain 'index stale' when rag_freshness_secs >= 300; got: {result}"
        );
    }

    /// Verify tooltip_string uses "index fresh" for freshness just under the threshold.
    #[test]
    fn tooltip_string_fresh_index_boundary() {
        let state = TrayState {
            rag_freshness_secs: 299,
            ..Default::default()
        };

        let result = tooltip_string(&state);
        assert!(
            result.contains("index fresh"),
            "tooltip must contain 'index fresh' when rag_freshness_secs < 300; got: {result}"
        );
    }

    /// Verify tooltip_string correctly sums tier_counts for total_rules.
    #[test]
    fn tooltip_string_tier_count_sum() {
        let mut tier_counts = HashMap::new();
        tier_counts.insert("T0".to_string(), 10u32);
        tier_counts.insert("T1".to_string(), 5u32);
        tier_counts.insert("T2".to_string(), 20u32);

        let state = TrayState {
            tier_counts,
            rag_freshness_secs: 0,
            active_agents: 3,
            inbox_unread: 7,
            ..Default::default()
        };

        let result = tooltip_string(&state);
        assert_eq!(
            result,
            "Cascade: 35 rules | index fresh | 3 agents | 7 inbox"
        );
    }

    /// Verify tooltip_string with an empty tier_counts map (no rules loaded).
    #[test]
    fn tooltip_string_empty_tier_counts() {
        let state = TrayState::default();
        let result = tooltip_string(&state);
        assert_eq!(
            result,
            "Cascade: 0 rules | index fresh | 0 agents | 0 inbox"
        );
    }
}
