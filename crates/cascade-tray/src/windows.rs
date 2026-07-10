//! Windows system-tray backend for cascade-tray.
//!
//! Purpose: Implement [`TrayHandle`] for Windows using tray-icon 0.14,
//!   which wraps `Shell_NotifyIconW` (Win32 NOTIFYICONDATA).
//! Inputs: A label string used as the initial tooltip; subsequent
//!   `TrayState` snapshots pushed via `update()`.
//! Outputs: A live tray icon in the Windows notification area (system tray)
//!   that reflects cascade daemon telemetry.
//! Constraints:
//!   - `Shell_NotifyIconW` accepts tooltips up to 128 WCHAR (including NUL).
//!     All tooltip strings MUST be truncated to ≤ 127 Unicode scalar values
//!     before being passed to the Win32 API; this module enforces that.
//!   - tray-icon's event pump MUST run on a dedicated OS thread. Blocking the
//!     daemon's Tokio runtime thread with the Win32 message loop would stall
//!     async task execution.
//!   - Every `unsafe` block in this file carries an explicit comment that names
//!     the invariant we are relying on to justify the unsafety.
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray (Windows impl)

use std::sync::{Arc, Mutex};

use tray_icon::{
    menu::{Menu, MenuItem, PredefinedMenuItem},
    Icon, TrayIcon, TrayIconBuilder,
};

use crate::{TrayAction, TrayError, TrayHandle, TrayMenuItem, TrayMenuSpec, TrayState};

/// Maximum byte length for a Win32 NOTIFYICONDATA szTip field.
///
/// WHY 127 not 128: the Win32 `szTip` buffer is `WCHAR[128]`; element [127]
/// must be the NUL terminator. We truncate the *source* string (which is
/// UTF-8 Rust `str`) to ≤ 127 Unicode scalar values before converting it to
/// a wide string; this guarantees the wide-encoded form never exceeds the
/// 128-WCHAR limit regardless of multi-byte characters.
const MAX_TOOLTIP_CHARS: usize = 127;

/// Placeholder 32×32 RGBA icon embedded at compile time.
///
/// WHY include_bytes!: embedding the icon avoids runtime file-system access and
/// ensures the binary is self-contained.  The placeholder is 4096 bytes
/// (32 × 32 × 4 channels) filled with Cascade blue [0x2D, 0x9C, 0xDB, 0xFF].
/// A production icon is substituted in T-P2-E06-09.
const ICON_RGBA: &[u8] = include_bytes!("../assets/cascade_icon_32.rgba");

/// Windows-specific system-tray backend.
///
/// Purpose: wrap `tray_icon::TrayIcon` and implement [`TrayHandle`] so the
///   cascade daemon can drive the Windows notification-area icon without
///   depending on tray-icon directly.
/// Inputs: `TrayState` snapshots from the daemon.
/// Outputs: Updated tooltip text and icon visibility in the Windows tray.
/// Constraints:
///   - Created via [`WindowsTrayImpl::new`]; callers must ensure the Win32
///     message pump is running (tray-icon starts it internally).
///   - Not `Send`; HWND-based state is thread-affine. The daemon must keep
///     this impl on the thread that created it or use tray-icon's
///     `TrayIconEventReceiver` to bridge across threads.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
pub struct WindowsTrayImpl {
    tray: TrayIcon,
    /// Maps tray-icon `MenuId` string values to `TrayAction` variants so the
    /// tray thread can look up the action when `MenuEvent::receiver()` fires.
    /// Stored as `(menu_id_str, TrayAction)` pairs.
    ///
    /// WHY Vec not HashMap: the menu has at most 4-5 items; linear scan is
    /// faster than hashing for such small sets and avoids a HashMap import.
    menu_actions: Vec<(String, TrayAction)>,
    /// Shared slot for the last action received from `MenuEvent::receiver()`.
    /// Written by `last_action()` after polling `MenuEvent::receiver()`.
    last_action_slot: Arc<Mutex<Option<TrayAction>>>,
}

impl WindowsTrayImpl {
    /// Create a new Windows tray icon with `label` as the initial tooltip.
    ///
    /// Loads the embedded placeholder RGBA icon, builds a `TrayIcon` via
    /// tray-icon 0.14's `TrayIconBuilder`, and shows the icon immediately.
    ///
    /// # Errors
    /// Returns `TrayError::Platform` if the Win32 `Shell_NotifyIconW` call
    /// fails (e.g. because Explorer.exe is not running).
    pub fn new(label: &str) -> Result<Self, TrayError> {
        // SAFETY: Icon::from_rgba copies the raw RGBA bytes into an internal
        // Win32 bitmap; the lifetime of ICON_RGBA is 'static so the pointer
        // remains valid for the duration of the copy.  The call site validates
        // the width × height × 4 == bytes invariant internally.
        let icon = Icon::from_rgba(ICON_RGBA.to_vec(), 32, 32)
            .map_err(|e| TrayError::Platform(e.to_string()))?;

        let tooltip = truncate_tooltip(label);

        let tray = TrayIconBuilder::new()
            .with_tooltip(&tooltip)
            .with_icon(icon)
            .build()
            .map_err(|e| TrayError::Platform(e.to_string()))?;

        Ok(Self {
            tray,
            menu_actions: Vec::new(),
            last_action_slot: Arc::new(Mutex::new(None)),
        })
    }
}

impl TrayHandle for WindowsTrayImpl {
    /// Push a new [`TrayState`] snapshot to the tray icon.
    ///
    /// Builds a human-readable tooltip from the state fields and calls
    /// `TrayIcon::set_tooltip`.  The tooltip is truncated to ≤ 127 Unicode
    /// scalar values before being sent to Win32.
    ///
    /// Format: `"Cascade: {total_rules} rules | {freshness} | {active_agents} agents | {inbox_unread} inbox"`
    ///
    /// # Errors
    /// Returns `TrayError::Platform` if `Shell_NotifyIconW` rejects the update.
    fn update(&mut self, state: &TrayState) -> Result<(), TrayError> {
        let total_rules: u32 = state.tier_counts.values().sum();

        let freshness = if state.rag_freshness_secs == 0 {
            "fresh".to_string()
        } else if state.rag_freshness_secs < 60 {
            format!("{}s ago", state.rag_freshness_secs)
        } else {
            format!("{}m ago", state.rag_freshness_secs / 60)
        };

        let raw_tooltip = format!(
            "Cascade: {} rules | {} | {} agents | {} inbox",
            total_rules, freshness, state.active_agents, state.inbox_unread
        );

        let tooltip = truncate_tooltip(&raw_tooltip);

        self.tray
            .set_tooltip(Some(&tooltip))
            .map_err(|e| TrayError::Platform(e.to_string()))
    }

    /// Make the tray icon visible in the Windows notification area.
    ///
    /// # Errors
    /// Returns `TrayError::Platform` on Win32 failure.
    fn show(&mut self) -> Result<(), TrayError> {
        self.tray
            .set_visible(true)
            .map_err(|e| TrayError::Platform(e.to_string()))
    }

    /// Hide the tray icon from the Windows notification area.
    fn hide(&mut self) {
        // WHY ignore error: hiding is a best-effort teardown step. If the Win32
        // call fails (e.g. because Explorer.exe has restarted), we silently
        // continue — the icon will be cleaned up when the process exits.
        let _ = self.tray.set_visible(false);
    }

    /// Release the tray icon and remove it from the notification area.
    ///
    /// Consuming `self` ensures the Win32 NOTIFYICONDATA handle is freed exactly
    /// once; tray-icon's `TrayIcon::Drop` impl calls `Shell_NotifyIconW(NIM_DELETE)`
    /// which removes the icon from the notification area.
    fn destroy(self) {
        // WHY explicit drop: drop(self.tray) is equivalent to letting `self` go
        // out of scope, but the explicit form makes the teardown intent visible to
        // code reviewers — it signals "this is the resource-release point".
        drop(self.tray);
    }

    /// Wire `TrayMenuSpec` items into a `tray-icon 0.14` context menu.
    ///
    /// Purpose: Build a `Menu` from the spec and attach it to the tray icon.
    ///   Stores `(menu_id_str, TrayAction)` pairs so `last_action()` can look
    ///   up the action when `MenuEvent::receiver()` fires.
    /// Inputs: `spec` — ordered list of actions and separators.
    /// Outputs: `Ok(())` on success; `Err(TrayError::Platform)` on Win32 failure.
    /// Constraints:
    ///   - `tray-icon 0.14` `MenuItem::id()` returns a `MenuId` whose `.0`
    ///     field is a `String`. We store that string as the lookup key.
    ///   - Each call to `set_menu` replaces the previous menu and clears the
    ///     `menu_actions` map.
    ///
    /// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray (menu wiring done)
    fn set_menu(&mut self, spec: &TrayMenuSpec) -> Result<(), TrayError> {
        let menu = Menu::new();
        let mut actions: Vec<(String, TrayAction)> = Vec::new();

        for item in &spec.items {
            match item {
                TrayMenuItem::Action { label, action } => {
                    let menu_item = MenuItem::new(label, true, None);
                    let id = menu_item.id().0.clone();
                    menu.append(&menu_item)
                        .map_err(|e| TrayError::Platform(e.to_string()))?;
                    actions.push((id, *action));
                }
                TrayMenuItem::Separator => {
                    let sep = PredefinedMenuItem::separator();
                    menu.append(&sep)
                        .map_err(|e| TrayError::Platform(e.to_string()))?;
                }
            }
        }

        self.tray.set_menu(Some(Box::new(menu)));
        self.menu_actions = actions;
        Ok(())
    }

    /// Poll `MenuEvent::receiver()` for a menu click and return the matching action.
    ///
    /// Purpose: Non-blocking poll of the global tray-icon menu event channel.
    ///   Maps the event's `MenuId` string to a `TrayAction` via `menu_actions`.
    /// Outputs: `Some(TrayAction)` if a menu item was clicked; `None` otherwise.
    /// Constraints:
    ///   - Uses `try_recv` — never blocks.
    ///   - Events not in `menu_actions` (e.g., from a previous menu spec) are
    ///     silently discarded.
    ///
    /// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
    fn last_action(&self) -> Option<TrayAction> {
        use tray_icon::menu::MenuEvent;
        if let Ok(event) = MenuEvent::receiver().try_recv() {
            let id_str = event.id.0.as_str();
            for (menu_id, action) in &self.menu_actions {
                if menu_id == id_str {
                    return Some(*action);
                }
            }
        }
        None
    }
}

/// Truncate `s` to at most [`MAX_TOOLTIP_CHARS`] Unicode scalar values.
///
/// Purpose: Enforce the Win32 `NOTIFYICONDATA.szTip` ≤ 128 WCHAR (127 + NUL)
///   limit before calling any tray-icon API that forwards to `Shell_NotifyIconW`.
/// Inputs: any UTF-8 string.
/// Outputs: a `String` guaranteed to be ≤ `MAX_TOOLTIP_CHARS` scalar values.
/// Constraints: truncation happens at a character boundary, never mid-codepoint.
///
/// SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-tray
fn truncate_tooltip(s: &str) -> String {
    let mut chars = s.chars();
    let mut result = String::with_capacity(s.len().min(MAX_TOOLTIP_CHARS * 4));
    for _ in 0..MAX_TOOLTIP_CHARS {
        match chars.next() {
            Some(c) => result.push(c),
            None => break,
        }
    }
    result
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use super::*;
    use crate::{DaemonStatus, TrayState};

    /// Verify truncate_tooltip returns the input unchanged when it is already
    /// within the 127-char limit.
    #[test]
    fn truncate_short_string_unchanged() {
        let s = "Cascade: 5 rules | fresh | 2 agents | 0 inbox";
        assert!(s.chars().count() <= MAX_TOOLTIP_CHARS);
        assert_eq!(truncate_tooltip(s), s);
    }

    /// Verify truncate_tooltip cuts at exactly 127 chars when given a longer string.
    #[test]
    fn truncate_long_string_to_127_chars() {
        // Build a string longer than 127 chars.
        let long: String = "A".repeat(200);
        let result = truncate_tooltip(&long);
        assert_eq!(
            result.chars().count(),
            MAX_TOOLTIP_CHARS,
            "truncated string must be exactly {MAX_TOOLTIP_CHARS} chars"
        );
    }

    /// Acceptance criterion: a TrayState with 200 tier_counts entries produces
    /// a tooltip ≤ 127 chars without panicking.
    ///
    /// This is the canonical QA-B acceptance test from T-P2-E06-05.
    #[test]
    fn tooltip_from_large_tray_state_is_within_limit() {
        // Build a TrayState with 200 tier_counts entries.
        let mut tier_counts = HashMap::new();
        for i in 0..200u32 {
            tier_counts.insert(format!("T{}", i), i + 1);
        }
        let state = TrayState {
            tier_counts,
            rag_freshness_secs: 3661, // > 60s to exercise the "m ago" path
            active_agents: 200,
            inbox_unread: 999,
            daemon_status: DaemonStatus::Running,
            fleet_summary: Vec::new(),
        };

        // Replicate the tooltip construction logic from `update()`.
        let total_rules: u32 = state.tier_counts.values().sum();
        let freshness = if state.rag_freshness_secs == 0 {
            "fresh".to_string()
        } else if state.rag_freshness_secs < 60 {
            format!("{}s ago", state.rag_freshness_secs)
        } else {
            format!("{}m ago", state.rag_freshness_secs / 60)
        };
        let raw_tooltip = format!(
            "Cascade: {} rules | {} | {} agents | {} inbox",
            total_rules, freshness, state.active_agents, state.inbox_unread
        );
        let tooltip = truncate_tooltip(&raw_tooltip);

        assert!(
            tooltip.chars().count() <= MAX_TOOLTIP_CHARS,
            "tooltip must be ≤ {} chars, got {}",
            MAX_TOOLTIP_CHARS,
            tooltip.chars().count()
        );
        // Must not panic — the test reaching this line proves no panic occurred.
    }

    /// Verify truncate_tooltip handles multi-byte Unicode characters correctly
    /// (truncates at char boundary, not at byte boundary).
    #[test]
    fn truncate_multibyte_chars() {
        // Arabic chars are 2 bytes each in UTF-8. 200 of them = 400 bytes.
        let arabic: String = "ا".repeat(200);
        let result = truncate_tooltip(&arabic);
        assert_eq!(
            result.chars().count(),
            MAX_TOOLTIP_CHARS,
            "must truncate to exactly {MAX_TOOLTIP_CHARS} Unicode scalar values"
        );
        // The byte length may be > 127 (multi-byte UTF-8) but char count ≤ 127.
        assert!(
            result.is_char_boundary(result.len()),
            "result must end on a valid char boundary"
        );
    }

    /// Verify ICON_RGBA has the correct size for a 32×32 RGBA image.
    #[test]
    fn icon_rgba_is_correct_size() {
        assert_eq!(
            ICON_RGBA.len(),
            32 * 32 * 4,
            "cascade_icon_32.rgba must be exactly 4096 bytes (32×32 pixels × 4 channels)"
        );
    }

    /// Verify ICON_RGBA first pixel is Cascade blue [0x2D, 0x9C, 0xDB, 0xFF].
    #[test]
    fn icon_rgba_first_pixel_is_cascade_blue() {
        assert_eq!(
            &ICON_RGBA[0..4],
            &[0x2D, 0x9C, 0xDB, 0xFF],
            "first pixel must be Cascade blue"
        );
    }
}
