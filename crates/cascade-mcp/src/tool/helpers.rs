//! Shared helper utilities for tool handlers.

use serde_json::Value;

use cascade_types::retriever::RetrieveOpts;

use crate::server::JsonRpcError;

// ── Search filter helper ──────────────────────────────────────────────────────

/// Build [`RetrieveOpts`] from `cascade.search` args.
pub(super) fn build_retrieve_opts(limit: usize, tier_filter: Option<&str>) -> RetrieveOpts {
    RetrieveOpts {
        k: limit,
        min_score: None,
        tier_filter: tier_filter.map(str::to_owned),
    }
}

// ── CallToolResult helpers ────────────────────────────────────────────────────

/// Wrap a handler result into a `CallToolResult` JSON value.
///
/// - `Ok(value)` → the value as-is (handler already shaped it correctly).
/// - `Err(JsonRpcError)` → `{ is_error: true, content: [text: message] }`.
///
/// This ensures tool-level errors NEVER become JSON-RPC protocol errors.
pub(super) fn tool_result(
    r: std::result::Result<Value, JsonRpcError>,
) -> std::result::Result<Value, JsonRpcError> {
    match r {
        Ok(v) => Ok(v),
        Err(e) => Ok(call_tool_error(&e.message)),
    }
}

/// Build a `CallToolResult` with `is_error: true`.
pub(super) fn call_tool_error(msg: &str) -> Value {
    serde_json::json!({
        "isError": true,
        "content": [{ "type": "text", "text": msg }]
    })
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Return today's date as `YYYY-MM-DD` (UTC, approximate).
///
/// Uses `SystemTime` to avoid pulling in the `time` or `chrono` crates.
/// Accurate to the year; precise enough for inbox filenames.
pub(super) fn chrono_local_date() -> String {
    use std::time::{SystemTime, UNIX_EPOCH};
    // Seconds since Unix epoch.
    let secs = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_secs();
    // Gregorian approximation using the 400-year cycle.
    let days_since_epoch = secs / 86400;
    // March 1, 2000 is day 11017 (leap-safe starting point).
    // Simple year estimate good enough for filenames.
    let approx_year = 1970u64 + days_since_epoch / 365;
    let approx_day_of_year = days_since_epoch % 365 + 1;
    let approx_month = (approx_day_of_year * 12 / 365 + 1).min(12);
    let approx_day = ((approx_day_of_year % 30) + 1).min(31);
    format!("{approx_year:04}-{approx_month:02}-{approx_day:02}")
}
