//! clock-widget — Cascade Widget plugin.
//!
//! Purpose: minimal `Widget` reference implementation that returns the current
//!   UTC time as a `WidgetData` blob. Demonstrates WASI clock access
//!   (`std::time::SystemTime::now()`) within the plugin sandbox and the
//!   `PluginKind::Widget` + `render()` pattern.
//!
//! Inputs:  none — `render()` takes no arguments.
//! Outputs: `WidgetData` — title="Clock", body="UTC: <ISO-8601 string>",
//!           metadata=null.
//! Constraints: no filesystem or network permissions needed; uses only the
//!   WASI clock syscall available on wasm32-wasip1.
//! SPORT: plugins / clock-widget (T-P4-E03-18)

use cascade_pdk::{cascade_plugin, Plugin, PluginError, PluginKind, WidgetData};

/// Clock-widget plugin struct.
///
/// Stateless — time is read fresh on every `render()` call.
#[cascade_plugin]
pub struct ClockWidget;

impl Plugin for ClockWidget {
    /// Returns `"clock-widget"` — the unique identifier shown in `cascade plugin list`.
    fn name(&self) -> &str {
        "clock-widget"
    }

    /// Declares this plugin as a `Widget` so the host calls `render()`.
    fn kind(&self) -> PluginKind {
        PluginKind::Widget
    }

    /// Render the current UTC time as a WidgetData payload.
    ///
    /// Uses `std::time::SystemTime::now()` which is available on wasm32-wasip1
    /// via the WASI clock_time_get syscall. The time is formatted as an
    /// RFC 3339 / ISO 8601 UTC timestamp without pulling in chrono.
    fn render(&self) -> Result<WidgetData, PluginError> {
        let body = format_utc_now();
        Ok(WidgetData {
            title: String::from("Clock"),
            body,
            metadata: serde_json::Value::Null,
        })
    }
}

// ── RFC 3339 formatting ───────────────────────────────────────────────────────

/// Format the current time as `"UTC: YYYY-MM-DDTHH:MM:SSZ"`.
///
/// On wasm32-wasip1, `std::time::SystemTime::now()` maps to the WASI
/// `clock_time_get(CLOCK_REALTIME)` syscall. wasm32-wasip1 is a full WASI
/// target with std support — no special no_std handling needed.
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
/// Uses the civil time algorithm from https://howardhinnant.github.io/date_algorithms.html
/// (public domain, integer-only, handles all dates from 0001-01-01 to 9999-12-31).
const fn epoch_secs_to_ymd_hms(secs: u64) -> (u32, u32, u32, u32, u32, u32) {
    let sec_of_day = (secs % 86400) as u32;
    let hour = sec_of_day / 3600;
    let min  = (sec_of_day % 3600) / 60;
    let sec  = sec_of_day % 60;

    // Days since Unix epoch (1970-01-01).
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

    // Test (1): render() returns Ok.
    #[test]
    fn render_returns_ok() {
        let plugin = ClockWidget;
        assert!(plugin.render().is_ok());
    }

    // Test (2): body starts with "UTC: ".
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

    // Test (3): body contains a date string matching \d{4}-\d{2}-\d{2}T.
    #[test]
    fn render_body_contains_rfc3339_date() {
        let plugin = ClockWidget;
        let data = plugin.render().unwrap();
        // Verify the pattern YYYY-MM-DDTHH:MM:SSZ is present.
        let body = &data.body;
        // body = "UTC: 2026-06-10T12:34:56Z"
        // After "UTC: " (5 chars) the date/time section starts.
        let dt_part = body.trim_start_matches("UTC: ");
        // Verify minimal length for YYYY-MM-DDTHH:MM:SSZ (20 chars).
        assert!(
            dt_part.len() >= 20,
            "date-time part too short: {}",
            dt_part
        );
        // Verify digit positions for YYYY-MM-DDTHH:MM:SSZ.
        let bytes = dt_part.as_bytes();
        assert!(bytes[4] == b'-', "expected '-' at pos 4, got: {}", bytes[4] as char);
        assert!(bytes[7] == b'-', "expected '-' at pos 7, got: {}", bytes[7] as char);
        assert!(bytes[10] == b'T', "expected 'T' at pos 10, got: {}", bytes[10] as char);
        assert!(bytes[13] == b':', "expected ':' at pos 13, got: {}", bytes[13] as char);
        assert!(bytes[16] == b':', "expected ':' at pos 16, got: {}", bytes[16] as char);
        // All digit positions contain ASCII digits.
        for &pos in &[0u8, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18] {
            assert!(
                bytes[pos as usize].is_ascii_digit(),
                "expected digit at pos {pos}, got: {}",
                bytes[pos as usize] as char
            );
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
        // 2026-06-10T00:00:00Z = 1781049600 seconds since epoch.
        let (y, mo, d, h, mi, s) = epoch_secs_to_ymd_hms(1_781_049_600);
        assert_eq!((y, mo, d, h, mi, s), (2026, 6, 10, 0, 0, 0));
    }
}
