//! Claude Code `stream-json` → Anthropic SSE re-framing (T-P7-E13-09).
//!
//! Purpose: pure, unit-testable classification of Claude Code CLI
//! `--output-format stream-json` NDJSON stdout lines into (a) Anthropic SSE
//! wire frames to emit, (b) failure signals that should trigger a spill to
//! the next account, or (c) terminal `result` metadata. No I/O — every rule
//! below is grounded in empirically captured output (see the parent module's
//! "Empirically captured signatures" section), not documentation guesses.
//!
//! Inputs:  single NDJSON lines from `claude -p --output-format stream-json
//!          --include-partial-messages --verbose` stdout.
//! Outputs: [`LineOutcome`] — SSE frames / spill reason / terminal result /
//!          ignored.
//!
//! Why a NEW parser (not reusing `anthropic_compat/sse.rs`): that module
//! parses Gemini's `data: {...}` SSE framing and translates Gemini chunk
//! shapes. Claude Code's stream-json is NDJSON with an envelope
//! (`{"type":"stream_event","event":{...}}`) whose inner `event` is already a
//! literal Anthropic SSE event — so re-framing is an unwrap-and-re-emit, and
//! failure detection is a different problem entirely (no Gemini analogue).
//!
//! SPORT: `.opencode/phases/sport/MASTER-FEATURES.md` — F-FLEET-ANTHROPIC-FAILOVER

use serde_json::Value;

/// Classification of one parsed stream-json line.
#[derive(Debug, Clone, PartialEq)]
pub enum LineOutcome {
    /// One or more Anthropic SSE wire frames (`event: …\ndata: …\n\n`) that
    /// should be emitted to the caller in order.
    Frames(Vec<Vec<u8>>),
    /// A terminal `result` line reporting success (`is_error: false`). Not an
    /// SSE frame itself — the dispatcher uses it for usage accounting and to
    /// build non-streaming responses.
    Result(Value),
    /// A failure signature: this attempt should be abandoned and the next
    /// account tried (pre-commit) or surfaced as an error event
    /// (post-commit). `reason` is a human-readable audit-trail fragment.
    Spill { reason: String },
    /// Line consumed with no external effect (init/status prelude, assistant
    /// success snapshots, unparsable lines).
    Ignored,
}

/// Classify one raw NDJSON stdout line from the child `claude` process.
pub fn classify_line(line: &str) -> LineOutcome {
    let line = line.trim();
    if line.is_empty() {
        return LineOutcome::Ignored;
    }
    let Ok(v) = serde_json::from_str::<Value>(line) else {
        // Non-JSON stdout (CLI banners, stray output) — never fatal, never content.
        return LineOutcome::Ignored;
    };
    let line_type = v.get("type").and_then(Value::as_str).unwrap_or("");
    match line_type {
        "stream_event" => match v.get("event") {
            Some(event) if event.is_object() => LineOutcome::Frames(vec![frame_for_event(event)]),
            _ => LineOutcome::Ignored,
        },
        "system" => classify_system(&v),
        "assistant" => classify_assistant(&v),
        "result" => classify_result(&v),
        _ => LineOutcome::Ignored,
    }
}

/// `system` lines: `init`/`status` are prelude noise; `api_retry` is the
/// EARLY 429/rate-limit signature (arrives before any content — captured
/// 2026-08-17: `{"type":"system","subtype":"api_retry","attempt":1,
/// "max_retries":10,"retry_delay_ms":581.46,"error_status":429,
/// "error":"rate_limit",...}`). Spilling on the FIRST api_retry avoids
/// waiting out the CLI's internal ~135s/10-attempt retry ladder; the next
/// account may not be saturated at all.
fn classify_system(v: &Value) -> LineOutcome {
    let subtype = v.get("subtype").and_then(Value::as_str).unwrap_or("");
    if subtype != "api_retry" {
        return LineOutcome::Ignored;
    }
    let attempt = v.get("attempt").and_then(Value::as_u64).unwrap_or(0);
    let status = v
        .get("error_status")
        .map(|s| s.to_string())
        .unwrap_or_else(|| "null".into());
    let error = v.get("error").and_then(Value::as_str).unwrap_or("unknown");
    LineOutcome::Spill {
        reason: format!("api_retry (attempt {attempt}): HTTP {status} {error}"),
    }
}

/// `assistant` lines are full-message snapshots that ALSO appear in success
/// streams (content already delivered via `stream_event` deltas) — ignore
/// them unless they carry the CLI's synthetic error marker. Captured
/// signatures: `"error":"authentication_failed"` (invalid/expired account
/// credentials) and `"error":"rate_limit"` (retry ladder exhausted).
fn classify_assistant(v: &Value) -> LineOutcome {
    let Some(err) = v.get("error") else {
        return LineOutcome::Ignored;
    };
    if err.is_null() {
        return LineOutcome::Ignored;
    }
    let err_text = err.as_str().unwrap_or("unknown");
    LineOutcome::Spill {
        reason: format!("assistant error: {err_text}"),
    }
}

/// Terminal `result` line. WARNING: `subtype` is NOT trustworthy — captured
/// failure results carry `"subtype":"success"` alongside `"is_error":true`
/// (both for auth failure with `api_error_status:null` and for 429
/// exhaustion with `api_error_status:429`). Only `is_error` + `api_error_status`
/// classify correctly.
fn classify_result(v: &Value) -> LineOutcome {
    let is_error = v.get("is_error").and_then(Value::as_bool).unwrap_or(false);
    if !is_error {
        return LineOutcome::Result(v.clone());
    }
    let status = v
        .get("api_error_status")
        .map(|s| s.to_string())
        .unwrap_or_else(|| "null".into());
    let text = v
        .get("result")
        .and_then(Value::as_str)
        .unwrap_or("no detail");
    let truncated: String = text.chars().take(200).collect();
    LineOutcome::Spill {
        reason: format!("result is_error: api_error_status={status}, {truncated}"),
    }
}

/// Format one Anthropic SSE event as wire bytes. The inner `event` object of
/// a `stream_event` line IS a literal Anthropic SSE event, so the frame is
/// `event: <event.type>\ndata: <event verbatim>\n\n` — no translation.
pub fn frame_for_event(event: &Value) -> Vec<u8> {
    let name = event
        .get("type")
        .and_then(Value::as_str)
        .unwrap_or("unknown");
    format!("event: {name}\ndata: {event}\n\n").into_bytes()
}

/// True when the event is a `content_block_delta` — the dispatcher's COMMIT
/// signal (first real model output; after this the caller has received
/// generated bytes and a spill would corrupt the stream). Used by this
/// module's tests; the dispatcher itself checks the serialized frame via
/// [`frame_is_content_delta`].
#[cfg(test)]
pub fn is_content_delta(event: &Value) -> bool {
    event.get("type").and_then(Value::as_str) == Some("content_block_delta")
}

/// Byte-level counterpart of [`is_content_delta`] for frames already
/// serialized by [`frame_for_event`]: true when the wire frame carries a
/// `content_block_delta` event. The dispatcher buffers frames as bytes, so
/// the commit check happens on the frame, not the parsed event.
pub fn frame_is_content_delta(frame: &[u8]) -> bool {
    frame.starts_with(b"event: content_block_delta\n")
}

/// Format an Anthropic `error` SSE event (mid-stream failure surfacing).
pub fn error_frame(error_type: &str, message: &str) -> Vec<u8> {
    let data = serde_json::json!({
        "type": "error",
        "error": { "type": error_type, "message": message }
    });
    format!("event: error\ndata: {data}\n\n").into_bytes()
}

/// Defensive fallback: synthesize a complete, correctly-ordered Anthropic SSE
/// event sequence from a terminal `result` value, for the (never observed
/// with `--include-partial-messages`, but possible) case where an attempt
/// succeeds without emitting any `stream_event` deltas.
pub fn synthesize_success_frames(
    text: &str,
    model: &str,
    input_tokens: u64,
    output_tokens: u64,
) -> Vec<Vec<u8>> {
    let id = format!("msg_failover_{}", uuid::Uuid::new_v4().simple());
    let events = [
        serde_json::json!({
            "type": "message_start",
            "message": {
                "id": id, "type": "message", "role": "assistant", "content": [],
                "model": model, "stop_reason": null, "stop_sequence": null,
                "usage": { "input_tokens": input_tokens, "output_tokens": 0 }
            }
        }),
        serde_json::json!({ "type": "content_block_start", "index": 0,
                            "content_block": { "type": "text", "text": "" } }),
        serde_json::json!({ "type": "content_block_delta", "index": 0,
                            "delta": { "type": "text_delta", "text": text } }),
        serde_json::json!({ "type": "content_block_stop", "index": 0 }),
        serde_json::json!({ "type": "message_delta",
                            "delta": { "stop_reason": "end_turn", "stop_sequence": null },
                            "usage": { "output_tokens": output_tokens } }),
        serde_json::json!({ "type": "message_stop" }),
    ];
    events.iter().map(frame_for_event).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::model_ids::MODEL_CLAUDE_SONNET;

    // ── Fixtures below are VERBATIM lines from the 2026-08-17 empirical
    // captures under /tmp/cascade-failover-capture (see parent module docs),
    // with long prelude fields elided for brevity where classification
    // ignores them.

    #[test]
    fn stream_event_unwraps_to_literal_sse_frame() {
        let line = r#"{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_20260818","type":"message","role":"assistant","model":"glm-4.7","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}},"session_id":"c04fec36","uuid":"ab64b6e1"}"#;
        match classify_line(line) {
            LineOutcome::Frames(frames) => {
                assert_eq!(frames.len(), 1);
                let s = String::from_utf8(frames[0].clone()).unwrap();
                assert!(s.starts_with("event: message_start\ndata: {"), "{s}");
                assert!(s.ends_with("\n\n"), "{s}");
                assert!(s.contains(r#""model":"glm-4.7""#), "{s}");
            }
            other => panic!("expected Frames, got {other:?}"),
        }
    }

    #[test]
    fn content_delta_detected_as_commit_signal() {
        let line = r#"{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The"}},"uuid":"b4a413dd"}"#;
        match classify_line(line) {
            LineOutcome::Frames(frames) => {
                // The data payload after "data: " re-parses as the event itself.
                let raw = String::from_utf8(frames[0].clone()).unwrap();
                let data = raw
                    .strip_prefix("event: content_block_delta\ndata: ")
                    .unwrap();
                let event: Value = serde_json::from_str(data.trim_end()).unwrap();
                assert!(is_content_delta(&event));
            }
            other => panic!("expected Frames, got {other:?}"),
        }
    }

    #[test]
    fn non_delta_events_are_not_commit_signals() {
        let start: Value =
            serde_json::from_str(r#"{"type":"content_block_start","index":0}"#).unwrap();
        let stop: Value = serde_json::from_str(r#"{"type":"message_stop"}"#).unwrap();
        assert!(!is_content_delta(&start));
        assert!(!is_content_delta(&stop));
        // Frame-level variant agrees with the parsed-event variant.
        let delta = serde_json::json!({
            "type": "content_block_delta", "index": 0,
            "delta": { "type": "text_delta", "text": "x" }
        });
        assert!(frame_is_content_delta(&frame_for_event(&delta)));
        assert!(!frame_is_content_delta(&frame_for_event(&start)));
        assert!(!frame_is_content_delta(&frame_for_event(&stop)));
    }

    #[test]
    fn api_retry_429_is_early_spill_signal() {
        // Verbatim captured shape (2026-08-17, mock 429 upstream):
        let line = r#"{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":581.4606035390592,"error_status":429,"error":"rate_limit","session_id":"70ac1936","uuid":"6b62d005"}"#;
        match classify_line(line) {
            LineOutcome::Spill { reason } => {
                assert!(reason.contains("api_retry (attempt 1)"), "{reason}");
                assert!(reason.contains("429"), "{reason}");
                assert!(reason.contains("rate_limit"), "{reason}");
            }
            other => panic!("expected Spill, got {other:?}"),
        }
    }

    #[test]
    fn auth_failure_assistant_line_spills() {
        // Captured 2026-08-17 under an empty CLAUDE_CONFIG_DIR: synthetic
        // assistant message with the CLI's own error marker.
        let line = r#"{"type":"assistant","message":{"id":"650f2f03","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"Not logged in · Please run /login"}]},"error":"authentication_failed"}"#;
        match classify_line(line) {
            LineOutcome::Spill { reason } => {
                assert!(reason.contains("authentication_failed"), "{reason}");
            }
            other => panic!("expected Spill, got {other:?}"),
        }
    }

    #[test]
    fn rate_limit_exhausted_result_spills() {
        // Captured terminal result after the CLI's 10-attempt 429 ladder
        // exhausted (~182s). NOTE the misleading subtype:"success".
        let line = r#"{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"duration_ms":181832,"result":"API Error: Request rejected (429) · Number of requests has exceeded your rate limit (mock 429)","stop_reason":"stop_sequence"}"#;
        match classify_line(line) {
            LineOutcome::Spill { reason } => {
                assert!(reason.contains("api_error_status=429"), "{reason}");
            }
            other => panic!("expected Spill, got {other:?}"),
        }
    }

    #[test]
    fn success_result_is_terminal_metadata() {
        let line = r#"{"type":"result","subtype":"success","is_error":false,"api_error_status":null,"duration_ms":4321,"result":"ok","stop_reason":"end_turn","usage":{"input_tokens":23366,"output_tokens":31}}"#;
        match classify_line(line) {
            LineOutcome::Result(v) => {
                assert_eq!(v["usage"]["input_tokens"], 23366);
            }
            other => panic!("expected Result, got {other:?}"),
        }
    }

    #[test]
    fn prelude_and_noise_lines_are_ignored() {
        assert!(matches!(
            classify_line(
                r#"{"type":"system","subtype":"init","tools":["Bash"],"model":"claude-haiku-4-5-20251001"}"#
            ),
            LineOutcome::Ignored
        ));
        assert!(matches!(
            classify_line(r#"{"type":"system","subtype":"status","status":"requesting"}"#),
            LineOutcome::Ignored
        ));
        // assistant snapshots without an error marker appear in SUCCESS streams too.
        assert!(matches!(
            classify_line(
                r#"{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}"#
            ),
            LineOutcome::Ignored
        ));
        assert!(matches!(classify_line(""), LineOutcome::Ignored));
        assert!(matches!(
            classify_line("not json at all"),
            LineOutcome::Ignored
        ));
        assert!(matches!(
            classify_line(r#"{"type":"unknown_thing"}"#),
            LineOutcome::Ignored
        ));
        assert!(matches!(
            classify_line(r#"{"type":"stream_event"}"#), // missing event
            LineOutcome::Ignored
        ));
    }

    #[test]
    fn error_frame_is_well_formed_sse() {
        let f = error_frame("overloaded_error", "all accounts exhausted");
        let s = String::from_utf8(f).unwrap();
        assert!(s.starts_with("event: error\ndata: "));
        assert!(s.contains(r#""type":"overloaded_error""#));
        assert!(s.ends_with("\n\n"));
    }

    #[test]
    fn synthesized_frames_are_complete_sequence() {
        let frames = synthesize_success_frames("hi", MODEL_CLAUDE_SONNET, 10, 2);
        let joined: String = frames
            .iter()
            .map(|f| String::from_utf8_lossy(f))
            .collect::<Vec<_>>()
            .join("");
        for expected in [
            "event: message_start",
            "event: content_block_start",
            "event: content_block_delta",
            "event: content_block_stop",
            "event: message_delta",
            "event: message_stop",
        ] {
            assert!(joined.contains(expected), "missing {expected}");
        }
        assert!(joined.contains(r#""text":"hi""#));
        assert!(joined.contains(r#""input_tokens":10"#));
    }
}
