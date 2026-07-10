//! Linux system-tray backend for cascade-tray.
//!
//! Purpose: Implement [`crate::TrayHandle`] for Linux desktop environments via
//!   the StatusNotifierItem D-Bus specification (KDE/freedesktop), using the
//!   `ksni` crate (pure Rust, MIT, zbus/D-Bus, no GTK, no LGPL deps).
//!   When the D-Bus session bus is unavailable (headless CI) the impl stores a
//!   `NoOp` variant and returns `TrayError::Platform` from `update()`.
//! Inputs: [`crate::TrayState`] snapshots from the cascade daemon.
//! Outputs: Updated StatusNotifierItem title/tooltip and context menu via
//!   D-Bus, or `TrayError::Platform` on NoOp.
//! Constraints:
//!   - `new()` never returns `Err` — NoOp is the guaranteed final fallback.
//!   - The ksni `blocking` feature is required; it wraps the async zbus
//!     runtime in a std::thread so no async runtime is needed in the caller.
//!   - `CascadeTray` implements `ksni::Tray` and is driven by a `Handle<>`
//!     returned from `ksni::blocking::TrayMethods::spawn()`.
//!   - Menu items from `TrayMenuSpec` are converted to `ksni::MenuItem<CascadeTray>`
//!     inside `ksni::Tray::menu()` each time the desktop queries the menu.
//!   - `last_action_slot` captures the most recent `TrayAction` dispatched by
//!     a menu callback and is drained by `TrayHandle::last_action()`.
//!
//! SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray

use std::sync::{Arc, Mutex};

use crate::{TrayAction, TrayError, TrayHandle, TrayMenuItem, TrayMenuSpec, TrayState};

// ── CascadeTray (ksni::Tray impl) ─────────────────────────────────────────────

/// Internal tray state shared with the ksni event loop.
///
/// Purpose: carry the current cascade daemon telemetry snapshot and menu
///   specification so ksni can call `title()` / `tool_tip()` / `menu()` when
///   the desktop bar queries the StatusNotifierItem over D-Bus.
/// Constraints: `ksni::Tray` requires `Send + 'static`; all fields satisfy that.
///
/// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
struct CascadeTray {
    /// Most recent tooltip string derived from a `TrayState` snapshot.
    title: String,
    /// Current menu specification (updated via `TrayHandle::set_menu`).
    menu_spec: TrayMenuSpec,
    /// Shared slot written by menu-item callbacks and read by `last_action()`.
    last_action_slot: Arc<Mutex<Option<TrayAction>>>,
    /// Raw 32×32 ARGB32 icon data embedded at compile time.
    icon: ksni::Icon,
}

impl CascadeTray {
    /// Build a `CascadeTray` ready to be handed to ksni.
    fn new(
        title: String,
        menu_spec: TrayMenuSpec,
        last_action_slot: Arc<Mutex<Option<TrayAction>>>,
    ) -> Self {
        // The asset is 32×32 RGBA (4 096 bytes, R-G-B-A order).
        // ksni expects ARGB32 (network byte order), so we rotate each pixel
        // one byte right: [R, G, B, A] → [A, R, G, B].
        let rgba = include_bytes!("../assets/cascade_icon_32.rgba");
        let mut argb = rgba.to_vec();
        for pixel in argb.chunks_exact_mut(4) {
            pixel.rotate_right(1);
        }
        CascadeTray {
            title,
            menu_spec,
            last_action_slot,
            icon: ksni::Icon {
                width: 32,
                height: 32,
                data: argb,
            },
        }
    }
}

impl ksni::Tray for CascadeTray {
    /// Application id — stable across sessions, used by the SNI watcher.
    fn id(&self) -> String {
        "cascade".into()
    }

    /// Human-readable title shown in panel tooltips or alt-text.
    fn title(&self) -> String {
        self.title.clone()
    }

    /// Embedded 32×32 icon in ARGB32 format.
    fn icon_pixmap(&self) -> Vec<ksni::Icon> {
        vec![self.icon.clone()]
    }

    /// Tooltip shown on hover; re-uses the title text.
    fn tool_tip(&self) -> ksni::ToolTip {
        ksni::ToolTip {
            title: self.title.clone(),
            description: String::new(),
            icon_name: String::new(),
            icon_pixmap: Vec::new(),
        }
    }

    /// Build the context menu from the stored `TrayMenuSpec`.
    ///
    /// Called by ksni each time the desktop requests the menu (lazy, no cache).
    /// Items are converted from the platform-agnostic `TrayMenuItem` enum to
    /// the appropriate `ksni::menu::*` type.  `StandardItem`s carry a closure
    /// that writes the dispatched `TrayAction` into `last_action_slot`.
    fn menu(&self) -> Vec<ksni::MenuItem<Self>> {
        let mut items: Vec<ksni::MenuItem<CascadeTray>> = Vec::new();

        for menu_item in &self.menu_spec.items {
            match menu_item {
                TrayMenuItem::Separator => {
                    items.push(ksni::MenuItem::Separator);
                }
                TrayMenuItem::Action { label, action } => {
                    let action_captured = *action;
                    items.push(
                        ksni::menu::StandardItem {
                            label: label.clone(),
                            activate: Box::new(move |tray: &mut CascadeTray| {
                                if let Ok(mut slot) = tray.last_action_slot.lock() {
                                    *slot = Some(action_captured);
                                }
                            }),
                            ..Default::default()
                        }
                        .into(),
                    );
                }
            }
        }

        items
    }
}

// ── LinuxTrayVariant ──────────────────────────────────────────────────────────

/// Internal backend discriminant for [`LinuxTrayImpl`].
///
/// Purpose: distinguish between a live ksni service (with its `Handle`) and the
///   guaranteed headless no-op fallback.
/// Constraints: `ksni::blocking::Handle<CascadeTray>` is `Clone + Send`.
///
/// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
enum LinuxTrayVariant {
    /// Live StatusNotifierItem service driven by ksni.
    Ksni(ksni::blocking::Handle<CascadeTray>),
    /// Neither ksni spawn nor D-Bus session bus was available.
    NoOp,
}

// ── LinuxTrayImpl ─────────────────────────────────────────────────────────────

/// Linux implementation of [`crate::TrayHandle`].
///
/// Purpose: present the cascade daemon status in the OS status-notifier tray
///   using ksni (pure-Rust StatusNotifierItem, MIT, no GTK). Tracks the most
///   recently dispatched `TrayAction` for polling via `last_action()`.
/// Inputs: `TrayState` snapshots pushed by the daemon via `TrayHandle::update`.
///   Menu specs registered via `TrayHandle::set_menu()`.
/// Outputs: StatusNotifierItem title/tooltip updated over D-Bus; or no-op.
/// Constraints:
///   - `new()` never returns `Err` — NoOp is the guaranteed final fallback.
///   - `show()`/`hide()` are no-ops on SNI (visibility is controlled by the
///     StatusNotifierItem `Status` property; Active = shown, Passive = hidden;
///     we leave the default `Active` throughout as the cascade daemon's tray
///     should always be visible while the daemon is running).
///   - `set_menu()` propagates the new spec to the running ksni service via
///     `Handle::update`, which triggers a D-Bus property-changed signal so the
///     desktop bar refreshes its menu on the next open.
///
/// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
pub struct LinuxTrayImpl {
    variant: LinuxTrayVariant,
    /// Shared last-action slot written by menu callbacks and read by
    /// `last_action()`. Kept here so we can hand the same `Arc` to the
    /// `CascadeTray` held inside the ksni service thread.
    ///
    /// WHY Arc<Mutex>: menu callbacks run inside the ksni zbus thread; the
    /// outer `LinuxTrayImpl` lives on the daemon's tray thread. Mutex is the
    /// simplest safe primitive for this one-writer / one-reader pattern.
    last_action_slot: Arc<Mutex<Option<TrayAction>>>,
}

impl LinuxTrayImpl {
    /// Construct a new Linux tray handle using ksni.
    ///
    /// Purpose: attempt to spawn a ksni StatusNotifierItem service; fall back
    ///   to the NoOp variant when the D-Bus session bus is unavailable (e.g.
    ///   headless CI).
    /// Inputs: `label` — application name used as the initial tray tooltip.
    /// Outputs: always `Ok(LinuxTrayImpl)` — variant is `Ksni` or `NoOp`.
    /// Constraints: does NOT require a running GTK main loop.
    ///
    /// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
    pub fn new(label: &str) -> Result<Self, TrayError> {
        let last_action_slot: Arc<Mutex<Option<TrayAction>>> = Arc::new(Mutex::new(None));

        let tray = CascadeTray::new(
            label.to_owned(),
            TrayMenuSpec::default_menu(),
            Arc::clone(&last_action_slot),
        );

        use ksni::blocking::TrayMethods;
        match tray.spawn() {
            Ok(handle) => Ok(LinuxTrayImpl {
                variant: LinuxTrayVariant::Ksni(handle),
                last_action_slot,
            }),
            Err(_) => {
                // D-Bus unavailable (headless CI / unsupported DE). NoOp is
                // the guaranteed fallback — construction never fails.
                Ok(LinuxTrayImpl {
                    variant: LinuxTrayVariant::NoOp,
                    last_action_slot,
                })
            }
        }
    }

    // ── tooltip helper ────────────────────────────────────────────────────────

    /// Build the canonical tray label string from a [`TrayState`] snapshot.
    ///
    /// Purpose: single source of truth for the human-readable tooltip shown in
    ///   all Linux tray backends.  MUST produce the same output as the macOS
    ///   `MacosTrayImpl::tooltip_string` for the same state.
    /// Inputs: `state` — live daemon telemetry.
    /// Outputs: e.g. `"Cascade: 5 rules | index fresh | 1 agents | 0 inbox"`
    /// Constraints: pure function — no side effects, no OS calls.
    ///
    /// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
    pub fn tooltip_string(state: &TrayState) -> String {
        let total_rules: u32 = state.tier_counts.values().sum();
        let rag_fresh = if state.rag_freshness_secs < 300 {
            "index fresh"
        } else {
            "index stale"
        };
        format!(
            "Cascade: {total_rules} rules | {rag_fresh} | {} agents | {} inbox",
            state.active_agents, state.inbox_unread,
        )
    }
}

// ── TrayHandle impl ───────────────────────────────────────────────────────────

impl TrayHandle for LinuxTrayImpl {
    /// Push a new state snapshot to the StatusNotifierItem service.
    ///
    /// Ksni variant: calls `Handle::update` to set the new title on the
    ///   `CascadeTray` inside the ksni thread, which triggers a D-Bus
    ///   `NewTitle` property-changed signal so the desktop bar refreshes.
    /// NoOp variant: returns `TrayError::Platform`.
    fn update(&mut self, state: &TrayState) -> Result<(), TrayError> {
        let tooltip = Self::tooltip_string(state);

        match &self.variant {
            LinuxTrayVariant::Ksni(handle) => {
                handle.update(|tray: &mut CascadeTray| {
                    tray.title = tooltip;
                });
                Ok(())
            }
            LinuxTrayVariant::NoOp => Err(TrayError::Platform(
                "no tray backend available on this Linux DE".into(),
            )),
        }
    }

    /// Make the tray icon visible.
    ///
    /// The SNI `Active` status (set by default at ksni construction) makes the
    /// icon visible.  This method is a no-op — visibility is managed by ksni
    /// and the StatusNotifierItem `Status` property.
    fn show(&mut self) -> Result<(), TrayError> {
        Ok(())
    }

    /// Hide the tray icon.
    ///
    /// No-op for the ksni backend (SNI visibility management is intentionally
    /// deferred to a future P3 ticket; daemon is always "Active" while running).
    fn hide(&mut self) {}

    /// Release all native resources held by this tray handle.
    ///
    /// Ksni variant: calls `Handle::shutdown()` to gracefully stop the ksni
    ///   service thread and deregister the StatusNotifierItem from D-Bus.
    /// NoOp: nothing to release.
    fn destroy(self) {
        if let LinuxTrayVariant::Ksni(handle) = self.variant {
            handle.shutdown().wait();
        }
    }

    /// Register a menu specification on this Linux tray icon.
    ///
    /// Purpose: Store the new `TrayMenuSpec` so `CascadeTray::menu()` returns
    ///   updated items the next time the desktop bar opens the context menu.
    ///   For the ksni variant, `Handle::update` propagates the new spec into
    ///   the `CascadeTray` held inside the ksni service thread, triggering a
    ///   D-Bus `ItemsPropertiesUpdated` signal.
    /// Inputs: `spec` — ordered list of platform-agnostic menu items.
    /// Outputs: `Ok(())` always.
    ///
    /// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
    fn set_menu(&mut self, spec: &TrayMenuSpec) -> Result<(), TrayError> {
        let new_spec = spec.clone();
        match &self.variant {
            LinuxTrayVariant::Ksni(handle) => {
                handle.update(|tray: &mut CascadeTray| {
                    tray.menu_spec = new_spec;
                });
            }
            LinuxTrayVariant::NoOp => {
                // NoOp: accept silently (same as the old behaviour).
            }
        }
        Ok(())
    }

    /// Return the most recently dispatched `TrayAction`, if any.
    ///
    /// Purpose: allows the daemon tray thread to drain menu-click events from
    ///   the shared slot without polling a channel.
    /// Outputs: `Some(TrayAction)` if a menu item was clicked since the last
    ///   call; `None` otherwise. Uses take-semantics — consuming on read.
    /// Constraints: non-blocking; uses `Mutex::try_lock` internally.
    ///
    /// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
    fn last_action(&self) -> Option<TrayAction> {
        self.last_action_slot
            .try_lock()
            .ok()
            .and_then(|mut guard| guard.take())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{DaemonStatus, TrayState};
    use std::collections::HashMap;

    /// Verify tooltip_string produces the canonical format for the specified
    /// TrayState.  The expected value MUST match the macOS impl's output for
    /// the same input — both impls share the same format contract.
    ///
    /// Acceptance criterion from T-P2-E06-04:
    ///   TrayState{tier_counts:{GCI:2,PCI:3}, rag_freshness_secs:60,
    ///   active_agents:1, inbox_unread:0, daemon_status:Running}
    ///   → "Cascade: 5 rules | index fresh | 1 agents | 0 inbox"
    #[test]
    fn linux_tooltip_string_matches_macos_format() {
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

        let tooltip = LinuxTrayImpl::tooltip_string(&state);
        assert_eq!(
            tooltip, "Cascade: 5 rules | index fresh | 1 agents | 0 inbox",
            "tooltip must match canonical format (same as macOS impl)"
        );
    }

    /// Verify tooltip_string uses "index stale" when rag_freshness_secs >= 300.
    #[test]
    fn linux_tooltip_stale_index() {
        let state = TrayState {
            tier_counts: HashMap::new(),
            rag_freshness_secs: 300,
            active_agents: 0,
            inbox_unread: 0,
            daemon_status: DaemonStatus::Stopped,
            fleet_summary: Vec::new(),
        };
        let tooltip = LinuxTrayImpl::tooltip_string(&state);
        assert!(
            tooltip.contains("index stale"),
            "rag_freshness_secs=300 must produce 'index stale', got: {tooltip}"
        );
    }

    /// Verify tooltip_string sums all tier_count values for the rules total.
    #[test]
    fn linux_tooltip_tier_counts_summed() {
        let mut tier_counts = HashMap::new();
        tier_counts.insert("T0".to_string(), 10u32);
        tier_counts.insert("T1".to_string(), 5u32);
        tier_counts.insert("T2".to_string(), 20u32);

        let state = TrayState {
            tier_counts,
            rag_freshness_secs: 0,
            active_agents: 35,
            inbox_unread: 2,
            daemon_status: DaemonStatus::Running,
            fleet_summary: Vec::new(),
        };

        let tooltip = LinuxTrayImpl::tooltip_string(&state);
        assert!(
            tooltip.starts_with("Cascade: 35 rules"),
            "tier sum must be 35, got: {tooltip}"
        );
        assert!(
            tooltip.contains("35 agents"),
            "active_agents must be 35, got: {tooltip}"
        );
        assert!(
            tooltip.contains("2 inbox"),
            "inbox_unread must be 2, got: {tooltip}"
        );
    }

    /// Verify LinuxTrayImpl::new() always returns Ok — NoOp fallback ensures it.
    /// This test exercises the headless path (no D-Bus session bus on CI / macOS).
    #[test]
    fn linux_new_always_ok() {
        let result = LinuxTrayImpl::new("Cascade");
        assert!(
            result.is_ok(),
            "LinuxTrayImpl::new must succeed via NoOp fallback"
        );
    }

    /// Verify update() on the NoOp variant returns TrayError::Platform.
    /// Constructs the NoOp variant directly to mock the "no backend" scenario.
    #[test]
    fn linux_noop_update_returns_platform_error() {
        let mut tray = LinuxTrayImpl {
            variant: LinuxTrayVariant::NoOp,
            last_action_slot: Arc::new(Mutex::new(None)),
        };
        let state = TrayState::default();
        match tray.update(&state) {
            Err(TrayError::Platform(msg)) => {
                assert!(
                    msg.contains("no tray backend"),
                    "error must mention 'no tray backend', got: {msg}"
                );
            }
            other => panic!("expected TrayError::Platform for NoOp update, got: {other:?}"),
        }
    }

    /// Verify set_menu() returns Ok on NoOp variant (acceptance criterion AC-1).
    #[test]
    fn linux_set_menu_returns_ok() {
        let mut tray = LinuxTrayImpl {
            variant: LinuxTrayVariant::NoOp,
            last_action_slot: Arc::new(Mutex::new(None)),
        };
        let spec = crate::TrayMenuSpec::default_menu();
        assert!(
            tray.set_menu(&spec).is_ok(),
            "set_menu must return Ok on all Linux variants including NoOp"
        );
    }

    /// Verify last_action() returns None when no action has been dispatched.
    #[test]
    fn linux_last_action_none_by_default() {
        let tray = LinuxTrayImpl {
            variant: LinuxTrayVariant::NoOp,
            last_action_slot: Arc::new(Mutex::new(None)),
        };
        assert!(
            tray.last_action().is_none(),
            "last_action must return None when no action has been dispatched"
        );
    }

    /// Verify last_action() returns the action after it is written to the slot.
    #[test]
    fn linux_last_action_returns_written_value() {
        let slot: Arc<Mutex<Option<TrayAction>>> = Arc::new(Mutex::new(None));
        let tray = LinuxTrayImpl {
            variant: LinuxTrayVariant::NoOp,
            last_action_slot: Arc::clone(&slot),
        };
        // Simulate a menu callback writing an action into the slot.
        *slot.lock().expect("mutex poisoned") = Some(TrayAction::PauseDaemon);
        assert_eq!(
            tray.last_action(),
            Some(TrayAction::PauseDaemon),
            "last_action must return the value written into the slot"
        );
        // Verify the slot is consumed (take semantics).
        assert!(
            tray.last_action().is_none(),
            "last_action must consume the value (return None on second call)"
        );
    }

    /// Verify CascadeTray::menu() produces one item per TrayMenuItem::Action
    /// and one ksni::MenuItem::Separator per TrayMenuItem::Separator.
    #[test]
    fn cascade_tray_menu_builds_from_spec() {
        let slot: Arc<Mutex<Option<TrayAction>>> = Arc::new(Mutex::new(None));
        let spec = TrayMenuSpec::default_menu(); // 4 actions + 1 separator = 5 items
        let tray = CascadeTray::new("Cascade".to_owned(), spec.clone(), slot);

        let menu = ksni::Tray::menu(&tray);
        // default_menu: OpenApp, OpenDashboard, Separator, PauseDaemon, Quit → 5 items
        assert_eq!(
            menu.len(),
            5,
            "menu must have 5 items (4 actions + 1 separator)"
        );
        // Second item (index 2) must be a separator.
        assert!(
            matches!(menu[2], ksni::MenuItem::Separator),
            "item at index 2 must be a Separator"
        );
    }
}
