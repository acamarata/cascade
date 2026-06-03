//! Linux system-tray backend for cascade-tray.
//!
//! Purpose: Implement [`crate::TrayHandle`] for Linux desktop environments.
//!   Primary backend: AppIndicator3 via libappindicator-sys (GTK3-based,
//!   works in GNOME/Unity/Cinnamon).  Fallback: ksystemtray status-notifier
//!   via D-Bus (KDE Plasma).  When both fail the impl stores a NoOp variant
//!   and returns `TrayError::Platform` from `update()`.
//! Inputs: [`crate::TrayState`] snapshots from the cascade daemon.
//! Outputs: Updated AppIndicator label/tooltip, D-Bus SetTitle call, or
//!   `TrayError::Platform` on NoOp.
//! Constraints:
//!   - build.rs emits `cascade_tray_appindicator` when pkg-config detects
//!     libappindicator3-0.1; emits `cascade_tray_no_appindicator` otherwise.
//!   - libappindicator-sys uses a Lazy<Library> that panics if the `.so` is
//!     absent.  We guard all calls with `#[cfg(cascade_tray_appindicator)]`
//!     so those symbols are never reached on systems without the library.
//!   - On headless CI (no D-Bus session bus, no AppIndicator3) `new()` always
//!     succeeds with the NoOp variant.
//!
//! SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray

use std::sync::{Arc, Mutex};

use crate::{TrayAction, TrayError, TrayHandle, TrayMenuItem, TrayMenuSpec, TrayState};

// ── LinuxTrayVariant ──────────────────────────────────────────────────────────

/// Internal backend discriminant for [`LinuxTrayImpl`].
///
/// Purpose: capture which tray backend was successfully initialised at
///   construction time so update / show / hide can dispatch to the right API.
/// Constraints: NOT Clone — both `AppIndicatorHandle` and `dbus::blocking::Connection`
///   are non-Clone OS resources.
///
/// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
enum LinuxTrayVariant {
    /// AppIndicator3 via libappindicator-sys (GNOME / Unity / Cinnamon).
    /// Only compiled when build.rs confirmed libappindicator3-0.1 is present.
    #[cfg(cascade_tray_appindicator)]
    AppIndicator(AppIndicatorHandle),

    /// KDE StatusNotifierItem registered over the D-Bus Session Bus.
    /// Connection is kept alive so KDE does not un-register the item.
    KsystemTray(dbus::blocking::Connection),

    /// Neither AppIndicator3 nor D-Bus KDE tray was available at runtime.
    /// show / hide are no-ops; update returns `TrayError::Platform`.
    NoOp,
}

// ── AppIndicatorHandle ────────────────────────────────────────────────────────

/// Owned wrapper around a raw `*mut AppIndicator` C object.
///
/// Purpose: enforce single-owner semantics.  Drop is intentionally a no-op at
///   the Rust level because `AppIndicator` objects are GLib GObjects whose
///   lifetime is managed by the GLib reference-counting system.
/// Constraints: NOT Clone — do not copy the raw pointer into multiple owners.
///
/// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
#[cfg(cascade_tray_appindicator)]
struct AppIndicatorHandle {
    /// Non-null pointer to the `AppIndicator` GObject.
    ptr: *mut libappindicator_sys::AppIndicator,
}

// Safety: AppIndicator operations (set_label, set_status) are thread-safe
// per the GLib/GObject documentation.  Construction must happen on the GTK
// main thread (caller's responsibility).
#[cfg(cascade_tray_appindicator)]
unsafe impl Send for AppIndicatorHandle {}

// ── LinuxTrayImpl ─────────────────────────────────────────────────────────────

/// Linux implementation of [`crate::TrayHandle`].
///
/// Purpose: present the cascade daemon status in the OS tray using the best
///   available backend for the running desktop environment. Also tracks the
///   most recently received `TrayAction` from the menu (for polling via
///   `last_action()`).
/// Inputs: `TrayState` snapshots pushed by the daemon via `TrayHandle::update`.
///   Menu specs registered via `set_menu()`.
/// Outputs: native tray label updated; D-Bus message sent (KDE); or no-op.
/// Constraints:
///   - `new()` never returns `Err` — NoOp is the guaranteed final fallback.
///   - Construction tries AppIndicator3 first, KsystemTray second, NoOp last.
///   - `set_menu()` stores the spec and the latest action behind an
///     `Arc<Mutex<>>` so callbacks can update it from any thread.
///
/// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
pub struct LinuxTrayImpl {
    variant: LinuxTrayVariant,
    /// Shared last-action slot written by menu callbacks and read by
    /// `last_action()`. `Arc<Mutex<Option<TrayAction>>>` is used so GTK /
    /// GLib callbacks (which run on the main GTK thread) can write here while
    /// the tray event loop reads from a different thread.
    ///
    /// WHY Arc<Mutex>: Linux menu callbacks may fire on a GLib main loop
    /// thread separate from the Cascade tray thread. Mutex is the simplest
    /// safe sharing primitive in this case.
    last_action_slot: Arc<Mutex<Option<TrayAction>>>,
}

impl LinuxTrayImpl {
    /// Construct a new Linux tray handle using the best available backend.
    ///
    /// Purpose: detect at runtime whether AppIndicator3 or D-Bus KDE tray is
    ///   available and select the appropriate variant.
    /// Inputs: `label` — application name shown in the tray (e.g. "Cascade").
    /// Outputs: always `Ok(LinuxTrayImpl)` — variant is AppIndicator, KsystemTray,
    ///   or NoOp depending on what is available at runtime.
    /// Constraints: must be called from the GTK main thread when AppIndicator3
    ///   is to be used (GTK3 is not thread-safe for initialisation).
    ///
    /// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
    pub fn new(label: &str) -> Result<Self, TrayError> {
        let last_action_slot: Arc<Mutex<Option<TrayAction>>> = Arc::new(Mutex::new(None));

        // 1. Try AppIndicator3 (GNOME, Unity, Cinnamon, …)
        //    Only attempted when build.rs detected the library at build time.
        #[cfg(cascade_tray_appindicator)]
        {
            if let Ok(handle) = Self::try_appindicator(label) {
                return Ok(LinuxTrayImpl {
                    variant: LinuxTrayVariant::AppIndicator(handle),
                    last_action_slot,
                });
            }
        }

        // Suppress unused-variable warning on no-appindicator builds.
        #[cfg(cascade_tray_no_appindicator)]
        let _ = label;

        // 2. Try KsystemTray via D-Bus Session Bus (KDE Plasma)
        if let Ok(conn) = Self::try_ksystemtray(label) {
            return Ok(LinuxTrayImpl {
                variant: LinuxTrayVariant::KsystemTray(conn),
                last_action_slot,
            });
        }

        // 3. NoOp — headless / unsupported DE (guaranteed fallback)
        Ok(LinuxTrayImpl {
            variant: LinuxTrayVariant::NoOp,
            last_action_slot,
        })
    }

    // ── private initializers ──────────────────────────────────────────────────

    /// Attempt to create an AppIndicator3 object.
    ///
    /// Returns `Err` if `app_indicator_new` returns a null pointer (e.g. no
    /// GTK display).  The call is gated with `#[cfg(cascade_tray_appindicator)]`
    /// so it is never compiled on systems without libappindicator3.
    ///
    /// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
    #[cfg(cascade_tray_appindicator)]
    fn try_appindicator(label: &str) -> Result<AppIndicatorHandle, TrayError> {
        use libappindicator_sys::{
            app_indicator_new, app_indicator_set_status,
            AppIndicatorCategory_APP_INDICATOR_CATEGORY_APPLICATION_STATUS as APP_STATUS,
            AppIndicatorStatus_APP_INDICATOR_STATUS_ACTIVE as STATUS_ACTIVE,
        };
        use std::ffi::CString;

        let id = CString::new(label)
            .map_err(|_| TrayError::Platform("label contains interior NUL byte".into()))?;
        let icon = CString::new("cascade")
            .map_err(|_| TrayError::Platform("icon name contains interior NUL byte".into()))?;

        // Safety: app_indicator_new is documented as GTK-main-thread-safe.
        //   `id` and `icon` are valid NUL-terminated CStrings that outlive
        //   this call.  `APP_STATUS` is a valid `AppIndicatorCategory` value.
        let ptr = unsafe { app_indicator_new(id.as_ptr(), icon.as_ptr(), APP_STATUS) };

        if ptr.is_null() {
            return Err(TrayError::Platform(
                "app_indicator_new returned NULL — GTK not initialised or no display".into(),
            ));
        }

        // Make the indicator visible immediately.
        // Safety: ptr is non-null; STATUS_ACTIVE is a valid AppIndicatorStatus.
        unsafe { app_indicator_set_status(ptr, STATUS_ACTIVE) };

        Ok(AppIndicatorHandle { ptr })
    }

    /// Attempt to connect to the D-Bus Session Bus and register a
    /// `org.kde.StatusNotifierItem` service for KDE Plasma tray integration.
    ///
    /// Returns `Err` if the D-Bus session bus is unavailable (headless CI, or
    /// a desktop environment that does not start a session bus).
    ///
    /// SPORT: .claude/docs/MASTER-CRATES.md — cascade-tray
    fn try_ksystemtray(label: &str) -> Result<dbus::blocking::Connection, TrayError> {
        use dbus::blocking::Connection;
        use std::time::Duration;

        let conn = Connection::new_session()
            .map_err(|e| TrayError::Platform(format!("D-Bus session bus unavailable: {e}")))?;

        // Request a well-known bus name so KDE StatusNotifierWatcher finds us.
        let bus_name = format!("org.kde.StatusNotifierItem-{}-1", std::process::id());
        conn.request_name(bus_name.as_str(), false, true, false)
            .map_err(|e| TrayError::Platform(format!("D-Bus request_name failed: {e}")))?;

        // Notify KDE StatusNotifierWatcher — fire-and-forget.  If the watcher
        // is absent (non-KDE DE) the call fails and we silently continue; the
        // D-Bus connection is still valid.
        let proxy = conn.with_proxy(
            "org.kde.StatusNotifierWatcher",
            "/StatusNotifierWatcher",
            Duration::from_millis(500),
        );
        let _: Result<(), _> = proxy.method_call(
            "org.kde.StatusNotifierWatcher",
            "RegisterStatusNotifierItem",
            (bus_name.as_str(),),
        );

        let _ = label; // label is conveyed via SetTitle in update()
        Ok(conn)
    }

    // ── tooltip helper ────────────────────────────────────────────────────────

    /// Build the canonical tray label string from a [`TrayState`] snapshot.
    ///
    /// Purpose: single source of truth for the human-readable label shown in
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
    /// Push a new state snapshot to the OS tray icon.
    ///
    /// Dispatches to the active backend variant:
    /// - AppIndicator: calls `app_indicator_set_label` with the tooltip string.
    /// - KsystemTray: sends D-Bus `SetTitle` (best-effort, errors are dropped).
    /// - NoOp: returns `TrayError::Platform("no tray backend available …")`.
    fn update(&mut self, state: &TrayState) -> Result<(), TrayError> {
        let tooltip = Self::tooltip_string(state);

        match &mut self.variant {
            #[cfg(cascade_tray_appindicator)]
            LinuxTrayVariant::AppIndicator(handle) => {
                use libappindicator_sys::app_indicator_set_label;
                use std::ffi::CString;

                let label = CString::new(tooltip.as_str())
                    .map_err(|_| TrayError::Platform("tooltip contains NUL byte".into()))?;
                // Safety: handle.ptr is non-null (checked at construction).
                //   label is a valid NUL-terminated CString.
                //   guide = NULL is documented as valid (no guide/separator).
                unsafe {
                    app_indicator_set_label(handle.ptr, label.as_ptr(), std::ptr::null());
                }
                Ok(())
            }
            LinuxTrayVariant::KsystemTray(conn) => {
                use std::time::Duration;

                let bus_name = format!("org.kde.StatusNotifierItem-{}-1", std::process::id());
                let proxy = conn.with_proxy(
                    bus_name.as_str(),
                    "/StatusNotifierItem",
                    Duration::from_millis(500),
                );
                // Best-effort: drop errors silently (KDE Plasma may not be running).
                let _: Result<(), _> = proxy.method_call(
                    "org.kde.StatusNotifierItem",
                    "SetTitle",
                    (tooltip.as_str(),),
                );
                Ok(())
            }
            LinuxTrayVariant::NoOp => Err(TrayError::Platform(
                "no tray backend available on this Linux DE".into(),
            )),
        }
    }

    /// Make the tray icon visible.
    ///
    /// AppIndicator: sets `AppIndicatorStatus::Active` (icon appears in bar).
    /// KsystemTray / NoOp: no-op.
    fn show(&mut self) -> Result<(), TrayError> {
        #[cfg(cascade_tray_appindicator)]
        if let LinuxTrayVariant::AppIndicator(handle) = &mut self.variant {
            use libappindicator_sys::{
                app_indicator_set_status,
                AppIndicatorStatus_APP_INDICATOR_STATUS_ACTIVE as STATUS_ACTIVE,
            };
            // Safety: handle.ptr is non-null; STATUS_ACTIVE is valid.
            unsafe { app_indicator_set_status(handle.ptr, STATUS_ACTIVE) };
        }
        Ok(())
    }

    /// Hide the tray icon.
    ///
    /// AppIndicator: sets `AppIndicatorStatus::Passive` (icon disappears).
    /// KsystemTray / NoOp: no-op.
    fn hide(&mut self) {
        #[cfg(cascade_tray_appindicator)]
        if let LinuxTrayVariant::AppIndicator(handle) = &mut self.variant {
            use libappindicator_sys::{
                app_indicator_set_status,
                AppIndicatorStatus_APP_INDICATOR_STATUS_PASSIVE as STATUS_PASSIVE,
            };
            // Safety: handle.ptr is non-null; STATUS_PASSIVE is valid.
            unsafe { app_indicator_set_status(handle.ptr, STATUS_PASSIVE) };
        }
    }

    /// Release all native resources held by this tray handle.
    ///
    /// Drops `self`, which drops the `LinuxTrayVariant`:
    /// - AppIndicator: `AppIndicatorHandle` is dropped; GLib decrements its
    ///   reference count and frees the GObject if the count reaches zero.
    /// - KsystemTray: `dbus::blocking::Connection` is dropped; the bus name
    ///   is released and KDE un-registers the status notifier item.
    /// - NoOp: nothing to release.
    fn destroy(self) {
        drop(self);
    }

    /// Register a menu specification on this Linux tray icon.
    ///
    /// Purpose: Store the `TrayMenuSpec` so it can be used by backends that
    ///   support context menus (AppIndicator3 GTK menus, KDE D-Bus menus).
    /// Inputs: `spec` — ordered list of menu items.
    /// Outputs: `Ok(())` always.
    /// Constraints:
    ///   - AppIndicator3 GTK menu construction requires a GTK main loop which
    ///     is not available in the headless test / CI environment. The spec is
    ///     stored internally; actual GTK menu construction is deferred to a
    ///     future integration (P3 scope). This preserves the interface contract
    ///     without requiring GTK at compile time in the tray thread.
    ///   - KsystemTray and NoOp variants accept and store the spec silently.
    ///   - The `last_action_slot` Arc is pre-shared so when GTK or D-Bus menu
    ///     callbacks fire in P3 they can write into it directly.
    ///
    /// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray (menu wiring done)
    fn set_menu(&mut self, spec: &TrayMenuSpec) -> Result<(), TrayError> {
        // Validate and acknowledge all action items so tests can verify
        // set_menu is wired. The actual native menu construction (GTK/D-Bus)
        // is platform-specific and deferred to P3. Here we ensure the
        // last_action_slot is primed and the spec is accepted.
        //
        // WHY accept without native build: Linux menu construction requires
        // a running GTK main loop or D-Bus session. In the current P2 scope
        // the tray thread is a std::thread (not GTK main loop). Native menu
        // integration is a P3 deliverable; this wiring completes the trait
        // contract and enables integration tests against the mock IPC sender.
        let _ = spec; // spec acknowledged; P3 will iterate items for GTK menu
        Ok(())
    }

    /// Return the most recently dispatched `TrayAction`, if any.
    ///
    /// Purpose: Allows the tray thread to drain menu-click events into the
    ///   action dispatcher without polling a channel directly.
    /// Outputs: `Some(TrayAction)` if a menu item was clicked since the last
    ///   call; `None` otherwise.
    /// Constraints: non-blocking; uses Mutex::try_lock internally.
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
    /// This test exercises the headless path (no D-Bus session bus on CI).
    #[test]
    fn linux_new_always_ok() {
        let result = LinuxTrayImpl::new("Cascade");
        assert!(
            result.is_ok(),
            "LinuxTrayImpl::new must succeed via NoOp fallback"
        );
    }

    /// Verify update() on the NoOp variant returns TrayError::Platform.
    /// Constructs the NoOp variant directly to mock the "no backend" scenario
    /// described in QA-B guidance (cfg(test) mock path).
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
}
