//! # cascade_daemon::claude_usage
//!
//! Live Claude Max usage fetcher.
//!
//! Purpose: hit `GET https://api.anthropic.com/api/oauth/usage` with the
//! account's current access token and return a usage Value in the shape that
//! `write_quota_json` / the native widget expect.
//!
//! ## Design
//!
//! - All network I/O is async with a 10-second timeout per request.
//! - The pure response parser (`parse_usage_response`) is factored out so it
//!   can be unit-tested without hitting the network.
//! - On transport error, non-2xx response, or the `{"error":{...}}` envelope
//!   (which has no `five_hour` key), the function returns `None`.  Callers
//!   MUST NOT overwrite existing usage with zeros on `None`.
//! - Token values are NEVER logged.
//!
//! Inputs:  `access_token` — the current OAuth2 access token for the account.
//! Outputs: `Some(Value)` shaped as the `usage` object in `quota.json`, or
//!          `None` on any failure.
//! Constraints: no `unwrap()` outside `#[cfg(test)]`; never log tokens.
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — claude_usage (live usage fetcher)

use std::time::Duration;

use serde_json::{json, Value};
use tracing::warn;

// ── Constants ─────────────────────────────────────────────────────────────────

const USAGE_ENDPOINT: &str = "https://api.anthropic.com/api/oauth/usage";
const REQUEST_TIMEOUT: Duration = Duration::from_secs(10);

// ── Public API ────────────────────────────────────────────────────────────────

/// Fetch live Claude Max usage for one account.
///
/// Makes a `GET` to `api.anthropic.com/api/oauth/usage` with the supplied
/// `access_token`.  Applies a 10-second timeout.
///
/// Returns `Some(usage_value)` on a successful 200 response that contains a
/// `five_hour` key.  Returns `None` on transport error, non-2xx status, or
/// the `{"error":{...}}` envelope (rate-limit / auth error from Anthropic).
///
/// Inputs:  `access_token` — current OAuth2 bearer token (not logged).
/// Outputs: `Some(Value)` with keys `five_hour`, `seven_day`,
///          `seven_day_sonnet`, `seven_day_opus`, `extra_usage`,
///          `fable_available`; or `None`.
/// Constraints: async, 10-second network timeout; never logs token values.
pub async fn fetch_claude_usage(access_token: &str) -> Option<Value> {
    let client = reqwest::Client::builder()
        .timeout(REQUEST_TIMEOUT)
        .build()
        .map_err(|e| warn!("claude_usage: failed to build reqwest client: {e}"))
        .ok()?;

    let response = client
        .get(USAGE_ENDPOINT)
        .header("Authorization", format!("Bearer {access_token}"))
        .header("anthropic-beta", "oauth-2025-04-20")
        .header("Content-Type", "application/json")
        .send()
        .await
        .map_err(|e| warn!("claude_usage: request failed: {e}"))
        .ok()?;

    if !response.status().is_success() {
        warn!(status = %response.status(), "claude_usage: non-2xx response");
        return None;
    }

    let body: Value = response
        .json()
        .await
        .map_err(|e| warn!("claude_usage: failed to parse JSON response: {e}"))
        .ok()?;

    parse_usage_response(&body)
}

/// Parse a raw API response body into the `usage` Value shape used by
/// `quota.json`.
///
/// Expected successful response:
/// ```json
/// {
///   "five_hour":         { "utilization": 3.0, "resets_at": "2026-07-01T01:19:59.6+00:00", ... },
///   "seven_day":         { ... },
///   "seven_day_sonnet":  { ... },
///   "seven_day_opus":    { ... },
///   "limits":            [ ... ],
///   "extra_usage":       ...
/// }
/// ```
///
/// Error envelope (treat as failed pull — return `None`):
/// ```json
/// { "error": { "type": "rate_limit_error", ... } }
/// ```
///
/// Outputs: `Some(Value)` with the five usage keys remapped; `None` on error
///          envelope or missing `five_hour`.
///
/// This function is `pub` for unit testing; callers should use
/// `fetch_claude_usage` in production.
pub fn parse_usage_response(body: &Value) -> Option<Value> {
    // If the response is an error envelope ({"error":{...}}), treat as failure.
    if body.get("error").is_some() {
        warn!("claude_usage: API returned error envelope — treating as failed pull");
        return None;
    }

    // `five_hour` must be present for a valid usage response.
    let five_hour = body.get("five_hour")?;

    Some(json!({
        "five_hour":        remap_window(five_hour),
        "seven_day":        body.get("seven_day").map(remap_window).unwrap_or(Value::Null),
        "seven_day_sonnet": body.get("seven_day_sonnet").map(remap_window).unwrap_or(Value::Null),
        "seven_day_opus":   body.get("seven_day_opus").map(remap_window).unwrap_or(Value::Null),
        "extra_usage":      body.get("extra_usage").cloned().unwrap_or(Value::Null),
        "fable_available":  fable_quota_available(body),
    }))
}

// ── Internal helpers ──────────────────────────────────────────────────────────

/// Remap one usage window from the API shape to the widget shape.
///
/// API shape:
///   `{ "utilization": f64, "resets_at": "<iso8601>", "limit_dollars": null, ... }`
///
/// Widget shape:
///   `{ "utilization": f64, "resets_at": f64 (epoch seconds), "resets_in": "Xh YYm" }`
///
/// `resets_at` is parsed from ISO-8601 to epoch seconds.  On parse failure
/// the field is set to `null`.  `resets_in` is computed from the delta
/// between `resets_at` and now; if `resets_at` is null or in the past,
/// `resets_in` is `null`.
fn remap_window(window: &Value) -> Value {
    let utilization = window.get("utilization").and_then(|v| v.as_f64());

    let (resets_at_epoch, resets_in) = match window
        .get("resets_at")
        .and_then(|v| v.as_str())
        .and_then(parse_iso8601_to_epoch)
    {
        Some(epoch) => {
            let now = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|d| d.as_secs() as i64)
                .unwrap_or(0);
            let remaining_secs = epoch - now;
            let resets_in = if remaining_secs > 0 {
                Some(format_duration_secs(remaining_secs as u64))
            } else {
                None
            };
            (Some(epoch as f64), resets_in)
        }
        None => (None, None),
    };

    json!({
        "utilization": utilization,
        "resets_at":   resets_at_epoch,
        "resets_in":   resets_in,
    })
}

/// Return whether this account has explicit Claude Fable quota available.
///
/// Observed Anthropic OAuth usage responses expose aggregate/session windows
/// (`five_hour`, `seven_day`) and sometimes Sonnet/Opus-specific weekly caps,
/// but no Fable-specific quota field. Do not infer Fable availability from the
/// aggregate windows because Fable may be governed by its own allowance. This
/// returns `None` unless the response includes an explicit Fable window/model
/// entry, such as `seven_day_fable`, a `models["claude-fable-*"]` entry, or a
/// `limits[]` entry whose fields mention Fable.
fn fable_quota_available(body: &Value) -> Option<bool> {
    for key in [
        "seven_day_fable",
        "fable",
        "fable_quota",
        "claude_fable",
        "claude-fable-5",
    ] {
        if let Some(value) = body.get(key) {
            if let Some(available) = quota_value_available(value) {
                return Some(available);
            }
        }
    }

    if let Some(models) = body.get("models").and_then(|v| v.as_object()) {
        for (model_id, model) in models {
            if mentions_fable_str(model_id) || value_mentions_fable(model) {
                if let Some(available) = quota_value_available(model) {
                    return Some(available);
                }
            }
        }
    }

    if let Some(limits) = body.get("limits").and_then(|v| v.as_array()) {
        for limit in limits {
            if value_mentions_fable(limit) {
                if let Some(available) = quota_value_available(limit) {
                    return Some(available);
                }
            }
        }
    }

    None
}

fn quota_value_available(value: &Value) -> Option<bool> {
    if let Some(available) = value.as_bool() {
        return Some(available);
    }

    let obj = value.as_object()?;

    for key in ["available", "is_available", "quota_available"] {
        if let Some(available) = obj.get(key).and_then(|v| v.as_bool()) {
            return Some(available);
        }
    }

    if obj
        .get("disabled_reason")
        .and_then(|v| v.as_str())
        .map(|s| !s.is_empty())
        .unwrap_or(false)
    {
        return Some(false);
    }

    for key in [
        "remaining",
        "remaining_tokens",
        "remaining_requests",
        "remaining_credits",
        "left",
    ] {
        if let Some(remaining) = obj.get(key).and_then(|v| v.as_f64()) {
            return Some(remaining > 0.0);
        }
    }

    for key in ["utilization", "percent", "usage_percent", "pct_used", "used_percent"] {
        if let Some(percent) = obj.get(key).and_then(|v| v.as_f64()) {
            return Some(percent < 100.0);
        }
    }

    let used = obj.get("used").and_then(|v| v.as_f64());
    let limit = obj.get("limit").and_then(|v| v.as_f64());
    match (used, limit) {
        (Some(used), Some(limit)) if limit > 0.0 => Some(used < limit),
        _ => None,
    }
}

fn value_mentions_fable(value: &Value) -> bool {
    match value {
        Value::String(s) => mentions_fable_str(s),
        Value::Array(items) => items.iter().any(value_mentions_fable),
        Value::Object(obj) => obj.iter().any(|(key, value)| {
            mentions_fable_str(key) || value_mentions_fable(value)
        }),
        _ => false,
    }
}

fn mentions_fable_str(value: &str) -> bool {
    value.to_ascii_lowercase().contains("fable")
}

/// Parse an ISO-8601 datetime string to Unix epoch seconds.
///
/// Handles strings like `"2026-07-01T01:19:59.6+00:00"` using
/// `chrono::DateTime::parse_from_rfc3339` (lenient for fractional seconds).
///
/// Returns `None` on parse failure.
fn parse_iso8601_to_epoch(s: &str) -> Option<i64> {
    chrono::DateTime::parse_from_rfc3339(s)
        .ok()
        .map(|dt| dt.timestamp())
}

/// Format a duration in seconds as `"Xh YYm"`.
///
/// Examples: 7200 → `"2h 00m"`, 3661 → `"1h 01m"`, 45 → `"0h 01m"`.
fn format_duration_secs(total_secs: u64) -> String {
    let hours = total_secs / 3600;
    let mins = (total_secs % 3600) / 60;
    format!("{hours}h {mins:02}m")
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    // ── parse_usage_response ─────────────────────────────────────────────────

    /// Happy path: full response with all four usage windows parses correctly.
    #[test]
    fn parse_usage_response_happy_path() {
        let body = json!({
            "five_hour": {
                "utilization": 3.0,
                "resets_at": "2026-07-01T01:19:59+00:00",
                "limit_dollars": null
            },
            "seven_day": {
                "utilization": 15.5,
                "resets_at": "2026-07-05T00:00:00+00:00"
            },
            "seven_day_sonnet": {
                "utilization": 8.2,
                "resets_at": "2026-07-05T00:00:00+00:00"
            },
            "seven_day_opus": {
                "utilization": 1.1,
                "resets_at": "2026-07-05T00:00:00+00:00"
            },
            "extra_usage": null
        });

        let usage = parse_usage_response(&body).expect("must return Some on valid response");

        // five_hour must be present and carry utilization.
        let fh = usage.get("five_hour").expect("must have five_hour");
        assert_eq!(fh.get("utilization").and_then(|v| v.as_f64()), Some(3.0));

        // seven_day must be present.
        assert!(usage.get("seven_day").is_some());
        assert!(usage.get("seven_day_sonnet").is_some());
        assert!(usage.get("seven_day_opus").is_some());

        // extra_usage is null but must be present.
        assert!(usage.get("extra_usage").is_some());
        assert!(usage.get("fable_available").map(|v| v.is_null()).unwrap_or(false));
    }

    /// Current observed usage shape has no Fable-specific quota field.
    #[test]
    fn fable_quota_available_missing_signal_returns_none() {
        let body = json!({
            "five_hour": { "utilization": 0.0 },
            "seven_day": { "utilization": 0.0 },
            "seven_day_sonnet": { "utilization": 0.0 },
            "seven_day_opus": { "utilization": 0.0 }
        });

        assert_eq!(fable_quota_available(&body), None);
        let usage = parse_usage_response(&body).expect("usage parses");
        assert!(usage.get("fable_available").map(|v| v.is_null()).unwrap_or(false));
    }

    /// If Anthropic adds an explicit Fable window, expose availability.
    #[test]
    fn fable_quota_available_from_fable_window() {
        let body = json!({
            "five_hour": { "utilization": 0.0 },
            "seven_day_fable": { "utilization": 42.0, "resets_at": "2026-07-05T00:00:00+00:00" }
        });

        assert_eq!(fable_quota_available(&body), Some(true));
        let usage = parse_usage_response(&body).expect("usage parses");
        assert_eq!(usage.get("fable_available").and_then(|v| v.as_bool()), Some(true));
    }

    /// If Anthropic exposes per-model limits, saturated Fable quota is detected.
    #[test]
    fn fable_quota_available_from_limits_entry() {
        let body = json!({
            "five_hour": { "utilization": 0.0 },
            "limits": [
                { "type": "weekly_all", "utilization": 10.0 },
                { "model": "claude-fable-5", "utilization": 100.0 }
            ]
        });

        assert_eq!(fable_quota_available(&body), Some(false));
    }

    /// Error envelope ({"error":{...}}) must return None — never write zeros.
    #[test]
    fn parse_usage_response_error_envelope_returns_none() {
        let body = json!({
            "error": {
                "type": "rate_limit_error",
                "message": "You have exceeded your rate limit."
            }
        });
        assert!(
            parse_usage_response(&body).is_none(),
            "error envelope must return None"
        );
    }

    /// Missing five_hour key must return None.
    #[test]
    fn parse_usage_response_missing_five_hour_returns_none() {
        let body = json!({ "seven_day": { "utilization": 10.0, "resets_at": null } });
        assert!(
            parse_usage_response(&body).is_none(),
            "missing five_hour must return None"
        );
    }

    /// resets_at ISO-8601 → epoch conversion is correct for a known timestamp.
    #[test]
    fn parse_iso8601_to_epoch_known_value() {
        // 2026-07-01T00:00:00+00:00 = 1782864000
        let epoch = parse_iso8601_to_epoch("2026-07-01T00:00:00+00:00");
        assert_eq!(epoch, Some(1_782_864_000));
    }

    /// Garbage ISO-8601 string returns None.
    #[test]
    fn parse_iso8601_to_epoch_invalid_returns_none() {
        assert!(parse_iso8601_to_epoch("not a date").is_none());
        assert!(parse_iso8601_to_epoch("").is_none());
    }

    /// format_duration_secs produces correct "Xh YYm" strings.
    #[test]
    fn format_duration_secs_examples() {
        assert_eq!(format_duration_secs(7200), "2h 00m");
        assert_eq!(format_duration_secs(3661), "1h 01m");
        assert_eq!(format_duration_secs(45),   "0h 00m");
        assert_eq!(format_duration_secs(3600), "1h 00m");
        assert_eq!(format_duration_secs(90),   "0h 01m");
    }

    /// remap_window extracts utilization and resets_at epoch from a window Value.
    #[test]
    fn remap_window_extracts_fields() {
        let window = json!({
            "utilization": 7.5,
            "resets_at": "2026-07-01T00:00:00+00:00",
            "limit_dollars": null
        });
        let mapped = remap_window(&window);
        assert_eq!(mapped.get("utilization").and_then(|v| v.as_f64()), Some(7.5));
        // resets_at must be epoch seconds (f64).
        let epoch = mapped.get("resets_at").and_then(|v| v.as_f64());
        assert!(epoch.is_some(), "resets_at must be Some(f64)");
        assert_eq!(epoch.unwrap() as i64, 1_782_864_000);
    }

    /// remap_window with a null/missing resets_at produces null fields.
    #[test]
    fn remap_window_missing_resets_at() {
        let window = json!({ "utilization": 5.0 });
        let mapped = remap_window(&window);
        assert!(mapped.get("resets_at").map(|v| v.is_null()).unwrap_or(true));
        assert!(mapped.get("resets_in").map(|v| v.is_null()).unwrap_or(true));
    }

    /// Response with only five_hour (partial) still parses; missing windows → null.
    #[test]
    fn parse_usage_response_partial_windows_ok() {
        let body = json!({
            "five_hour": { "utilization": 1.0, "resets_at": "2026-07-01T00:00:00+00:00" }
        });
        let usage = parse_usage_response(&body).expect("partial response must parse");
        assert!(usage.get("five_hour").is_some());
        // Missing optional windows become null.
        assert!(usage.get("seven_day").and_then(|v| if v.is_null() { Some(()) } else { None }).is_some(),
            "missing seven_day must be null in output");
    }

    /// Fractional-seconds ISO-8601 (as returned by the real API) parses correctly.
    #[test]
    fn parse_iso8601_fractional_seconds() {
        // Real API returns "2026-07-01T01:19:59.6+00:00"
        let epoch = parse_iso8601_to_epoch("2026-07-01T01:19:59.6+00:00");
        assert!(epoch.is_some(), "fractional-second ISO-8601 must parse");
        // Should be 2026-07-01T01:19:59Z = 1782868799
        assert_eq!(epoch.unwrap(), 1_782_868_799);
    }
}
