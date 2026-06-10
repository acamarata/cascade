//! clock-widget — Cascade Widget plugin example (annotated).
//!
//! Purpose: demonstrates the `Widget` pattern — how to write a Cascade plugin
//!   that renders a read-only informational panel in the Cascade desktop GUI.
//!
//! Widget plugins implement `render() -> Result<WidgetData>`. The host polls
//! `render()` on its own schedule (typically every few seconds) and updates
//! the panel. Widgets must be fast — no blocking I/O inside `render()`.
//!
//! This example reads the current UTC time using the WASI clock syscall
//! (`std::time::SystemTime::now()`) and formats it without external deps.
//!
//! To build:  cargo build --target wasm32-wasip1
//! To install: copy target/wasm32-wasip1/debug/clock_widget.wasm to ~/.cascade/plugins/
//! To test:   cargo test

// --- Imports -----------------------------------------------------------------
//
// For a Widget plugin you need:
//
// `cascade_plugin` — attribute macro (generates the WASM ABI).
// `Plugin`         — the trait to implement.
// `PluginKind`     — use `PluginKind::Widget` to declare this as a widget.
// `WidgetData`     — the render payload: { title, body, metadata }.
// `PluginError`    — returned from `render()` on plugin-level failure.
use cascade_pdk::{cascade_plugin, Plugin, PluginError, PluginKind, WidgetData};

// --- Plugin struct ------------------------------------------------------------
#[cascade_plugin]
pub struct ClockWidget;

// --- Plugin trait implementation ---------------------------------------------
impl Plugin for ClockWidget {
    fn name(&self) -> &str {
        "clock-widget"
    }

    // Declare this plugin as a Widget. The host calls `render()` on each
    // poll cycle and passes the returned `WidgetData` to the GUI panel.
    fn kind(&self) -> PluginKind {
        PluginKind::Widget
    }

    // `render()` is the only method you need to override for Widget plugins.
    //
    // The host calls this on demand. Keep it fast:
    //   - No blocking I/O.
    //   - No network calls.
    //   - No filesystem reads (unless performance is acceptable).
    //
    // Return:
    //   `Ok(WidgetData { title, body, metadata })` — success.
    //   `Err(PluginError::...)` — plugin crash (the GUI shows an error state).
    fn render(&self) -> Result<WidgetData, PluginError> {
        // Read the current UTC time via the WASI clock syscall.
        // On wasm32-wasip1 this maps to __wasi_clock_time_get(CLOCK_REALTIME).
        // On host (test) builds it uses the standard library clock.
        let body = format_utc_now();

        Ok(WidgetData {
            // `title` is shown in the widget panel header.
            title: String::from("Clock"),
            // `body` is the main content rendered in the panel.
            body,
            // `metadata` is optional JSON passed to the GUI for filtering.
            // Use `serde_json::Value::Null` if you have nothing to add.
            metadata: serde_json::Value::Null,
        })
    }
}

// ── RFC 3339 formatting (no heavy deps) ──────────────────────────────────────
//
// We format the time manually to avoid pulling in chrono (or any dep with std).
// The format is "UTC: YYYY-MM-DDTHH:MM:SSZ" — readable and RFC 3339 compliant.

fn format_utc_now() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    let duration = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default();
    let secs = duration.as_secs();
    let (year, month, day, hour, min, sec) = epoch_secs_to_ymd_hms(secs);
    format!(
        "UTC: {:04}-{:02}-{:02}T{:02}:{:02}:{:02}Z",
        year, month, day, hour, min, sec
    )
}

/// Convert UNIX epoch seconds to (year, month, day, hour, minute, second).
///
/// Civil time algorithm — integer-only, no floating point, public domain.
/// Source: https://howardhinnant.github.io/date_algorithms.html
const fn epoch_secs_to_ymd_hms(secs: u64) -> (u32, u32, u32, u32, u32, u32) {
    let sec_of_day = (secs % 86400) as u32;
    let hour = sec_of_day / 3600;
    let min  = (sec_of_day % 3600) / 60;
    let sec  = sec_of_day % 60;

    let z = (secs / 86400) as i64 + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = (z - era * 146097) as u32;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y   = (yoe as i64) + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp  = (5 * doy + 2) / 153;
    let d   = doy - (153 * mp + 2) / 5 + 1;
    let m   = if mp < 10 { mp + 3 } else { mp - 9 };
    let y   = if m <= 2 { y + 1 } else { y };

    (y as u32, m, d, hour, min, sec)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn render_returns_ok() {
        let plugin = ClockWidget;
        assert!(plugin.render().is_ok());
    }

    #[test]
    fn render_body_starts_with_utc() {
        let plugin = ClockWidget;
        let data = plugin.render().unwrap();
        assert!(
            data.body.starts_with("UTC: "),
            "body should start with 'UTC: ', got: {}",
            data.body
        );
    }

    #[test]
    fn render_body_contains_rfc3339_date() {
        let plugin = ClockWidget;
        let data = plugin.render().unwrap();
        let dt_part = data.body.trim_start_matches("UTC: ");
        assert!(dt_part.len() >= 20, "date-time part too short: {}", dt_part);
        let bytes = dt_part.as_bytes();
        assert_eq!(bytes[4], b'-');
        assert_eq!(bytes[7], b'-');
        assert_eq!(bytes[10], b'T');
        assert_eq!(bytes[13], b':');
        assert_eq!(bytes[16], b':');
        for &pos in &[0u8, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18] {
            assert!(bytes[pos as usize].is_ascii_digit());
        }
    }

    #[test]
    fn render_title_is_clock() {
        let plugin = ClockWidget;
        let data = plugin.render().unwrap();
        assert_eq!(data.title, "Clock");
    }

    #[test]
    fn epoch_secs_known_value() {
        let (y, mo, d, h, mi, s) = epoch_secs_to_ymd_hms(1_781_049_600);
        assert_eq!((y, mo, d, h, mi, s), (2026, 6, 10, 0, 0, 0));
    }
}
