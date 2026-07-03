//! # gfp_http — GFP lane HTTP bridge to the local :3762 Anthropic-compat proxy
//!
//! ## Purpose
//!
//! Real execution path for [`super::delegate::GfpLane`] (E1-S6): POSTs the
//! prompt to the daemon's Anthropic-compat adapter at
//! `http://127.0.0.1:3762/v1/messages` and extracts the completion text.
//!
//! Transport is a `curl` subprocess with discrete args (no `sh -c`), matching
//! the delegate-lane subprocess style and the CLI conductor's proven
//! `execute_gfp` pattern — cascade-core deliberately carries no HTTP client
//! dependency, and the lanes are synchronous.
//!
//! ## Outputs
//!
//! - `LaneResult::Output(text)` — the extracted completion text.
//! - `LaneResult::Unavailable(reason)` — proxy not running, pool exhausted,
//!   error envelope, timeout, or unparseable body. The caller spills onward;
//!   output is NEVER fabricated.
//!
//! ## Constraints
//!
//! - Bounded timeout: capped at [`MAX_TIMEOUT_SECS`] (60 s).
//! - No `sh -c`; prompt travels via stdin (`--data-binary @-`), never argv.
//! - Response parsing ([`parse_messages_response`]) is pure and unit-tested
//!   without I/O.
//!
//! ## SPORT
//!
//! MASTER-CRATES.md — cascade-core: routing::gfp_http (E1-S6)

use std::io::Write;
use std::process::{Command as StdCommand, Stdio};
use std::time::{Duration, Instant};

use cascade_types::error::{CascadeError, Result};
use serde_json::{json, Value};

use crate::model_ids::MODEL_CLAUDE_HAIKU;
use crate::routing::delegate::{find_binary_opt, LaneResult};

/// The local Anthropic-compat GP proxy endpoint.
pub const GFP_MESSAGES_URL: &str = "http://127.0.0.1:3762/v1/messages";

/// The GP proxy's live pool-health endpoint (:3761, gemini_proxy).
///
/// Returns `{"status":"ok","healthy_slots":N,"total_slots":M}` computed from
/// the LIVE in-memory routing table (429-cooldown state included) — the only
/// honest GP health signal outside the daemon process.
pub const GFP_HEALTH_URL: &str = "http://127.0.0.1:3761/health";

/// Hard upper bound on the request timeout (seconds).
const MAX_TIMEOUT_SECS: u64 = 60;

/// max_tokens for lane completions (classification / triage sized).
const MAX_OUTPUT_TOKENS: u64 = 1024;

/// POST `prompt` to the GP proxy at `url` and return the completion text.
///
/// Inputs:  `url` — injectable for tests; production callers pass
///          [`GFP_MESSAGES_URL`]. `prompt` — user content. `timeout` —
///          clamped to `1..=60` seconds.
/// Outputs: `LaneResult` per the module contract; `Err` only on subprocess
///          spawn/wait I/O failure.
///
/// The request uses a haiku-class model id so the proxy's model map routes to
/// the cheap Flash tier — the GFP lane serves Cheap/T3 work by design.
pub(crate) fn post_messages(url: &str, prompt: &str, timeout: Duration) -> Result<LaneResult> {
    let Some(curl) = find_binary_opt("curl") else {
        return Ok(LaneResult::Unavailable(
            "`curl` not on PATH; cannot reach the GP proxy".into(),
        ));
    };

    let body = json!({
        "model": MODEL_CLAUDE_HAIKU,
        "max_tokens": MAX_OUTPUT_TOKENS,
        "messages": [{"role": "user", "content": prompt}]
    })
    .to_string();

    let secs = timeout.as_secs().clamp(1, MAX_TIMEOUT_SECS);

    let mut child = StdCommand::new(&curl)
        .args([
            "-s",
            "--max-time",
            &secs.to_string(),
            "-X",
            "POST",
            "-H",
            "Content-Type: application/json",
            "--data-binary",
            "@-",
            url,
        ])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|e| CascadeError::Io {
            path: curl.clone(),
            operation: "spawn",
            source: e,
        })?;

    // Feed the JSON body via stdin FROM A DEDICATED THREAD; dropping the
    // handle closes the pipe. curl with `--data-binary @-` normally reads
    // stdin to EOF eagerly, but if curl wedges before draining stdin and the
    // payload exceeds the OS pipe buffer (~64 KiB), an inline `write_all`
    // would block the calling thread with no bound — exactly the scenario the
    // deadline loop below guards against. Writing from a separate thread
    // keeps the deadline authoritative: on timeout the child is killed, the
    // pipe's read end closes, and the writer unblocks with EPIPE.
    let writer = child.stdin.take().map(|mut stdin| {
        let bytes = body.into_bytes();
        std::thread::spawn(move || {
            let _ = stdin.write_all(&bytes);
            // stdin dropped here → EOF for curl.
        })
    });

    // Bounded wait: curl enforces --max-time itself; the +5 s margin only
    // guards against a wedged curl process.
    let deadline = Instant::now() + Duration::from_secs(secs + 5);
    loop {
        match child.try_wait().map_err(|e| CascadeError::Io {
            path: curl.clone(),
            operation: "wait",
            source: e,
        })? {
            Some(_) => break,
            None => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    // Child is dead → pipe closed → writer thread unblocks.
                    if let Some(w) = writer {
                        let _ = w.join();
                    }
                    return Ok(LaneResult::Unavailable(format!(
                        "GP proxy request timed out after {secs}s"
                    )));
                }
                std::thread::sleep(Duration::from_millis(50));
            }
        }
    }

    let output = child.wait_with_output().map_err(|e| CascadeError::Io {
        path: curl,
        operation: "wait_with_output",
        source: e,
    })?;

    // Child has exited — the writer thread is unblocked (or long done); reap it.
    if let Some(w) = writer {
        let _ = w.join();
    }

    if !output.status.success() {
        let reason = match output.status.code() {
            Some(7) => "GP proxy not running (connection refused on :3762)".to_string(),
            Some(28) => format!("GP proxy request timed out after {secs}s"),
            code => format!("curl exit {code:?} reaching the GP proxy"),
        };
        return Ok(LaneResult::Unavailable(reason));
    }

    Ok(parse_messages_response(&String::from_utf8_lossy(&output.stdout)))
}

/// Parse an Anthropic-format `/v1/messages` response body (pure, no I/O).
///
/// - Error envelope (`{"error": {...}}`) — including the pool-exhausted case
///   where every Gemini key is rate-limited — maps to `Unavailable` so the
///   caller's spill continues. Output is never fabricated.
/// - Success — all `content[].text` blocks concatenated. An empty completion
///   is also `Unavailable` (nothing usable to return).
pub(crate) fn parse_messages_response(body: &str) -> LaneResult {
    let v: Value = match serde_json::from_str(body) {
        Ok(v) => v,
        Err(_) => {
            return LaneResult::Unavailable(format!(
                "GP proxy returned an unparseable response ({} bytes)",
                body.len()
            ))
        }
    };

    if let Some(err) = v.get("error") {
        let etype = err.get("type").and_then(Value::as_str).unwrap_or("unknown");
        let msg = err
            .get("message")
            .and_then(Value::as_str)
            .unwrap_or("no message");
        return LaneResult::Unavailable(format!("GP proxy error ({etype}): {msg}"));
    }

    let text: String = v
        .get("content")
        .and_then(Value::as_array)
        .map(|blocks| {
            blocks
                .iter()
                .filter(|b| b.get("type").and_then(Value::as_str) == Some("text"))
                .filter_map(|b| b.get("text").and_then(Value::as_str))
                .collect::<Vec<_>>()
                .join("")
        })
        .unwrap_or_default();

    if text.is_empty() {
        return LaneResult::Unavailable("GP proxy returned an empty completion".into());
    }
    LaneResult::Output(text)
}

// ── Live GP pool-health probe ─────────────────────────────────────────────────

/// Probe the LIVE GP pool health endpoint (`GET <url>`) and build a
/// [`GpHealthSnapshot`](crate::selection::GpHealthSnapshot).
///
/// Inputs:  `url` — injectable for tests; production callers pass
///          [`GFP_HEALTH_URL`]. `timeout` — clamped to `1..=10` seconds
///          (health must be cheap; localhost answers in milliseconds).
/// Outputs: the live snapshot on success; `GpHealthSnapshot::default()`
///          (0 slots = unhealthy) on ANY failure — proxy down, curl missing,
///          timeout, or unparseable body. Health is never guessed upward.
///
/// This is the sync (curl-subprocess) probe for CLI callers; the daemon's
/// async chat path uses its own reqwest probe against the same endpoint.
pub fn probe_gp_health(url: &str, timeout: Duration) -> crate::selection::GpHealthSnapshot {
    let Some(curl) = find_binary_opt("curl") else {
        return crate::selection::GpHealthSnapshot::default();
    };
    let secs = timeout.as_secs().clamp(1, 10);

    let child = StdCommand::new(&curl)
        .args(["-s", "--max-time", &secs.to_string(), url])
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn();
    let Ok(mut child) = child else {
        return crate::selection::GpHealthSnapshot::default();
    };

    // Bounded wait mirroring post_messages: curl enforces --max-time; the
    // +5 s margin guards against a wedged curl process.
    let deadline = Instant::now() + Duration::from_secs(secs + 5);
    loop {
        match child.try_wait() {
            Ok(Some(_)) => break,
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    return crate::selection::GpHealthSnapshot::default();
                }
                std::thread::sleep(Duration::from_millis(50));
            }
            Err(_) => return crate::selection::GpHealthSnapshot::default(),
        }
    }

    let Ok(output) = child.wait_with_output() else {
        return crate::selection::GpHealthSnapshot::default();
    };
    if !output.status.success() {
        return crate::selection::GpHealthSnapshot::default();
    }
    parse_gp_health(&String::from_utf8_lossy(&output.stdout))
}

/// Parse the `/health` response body into a snapshot (pure, no I/O).
///
/// Expected shape: `{"status":"ok","healthy_slots":N,...}`. Anything else —
/// missing field, wrong type, non-JSON — yields the default (unhealthy)
/// snapshot so a broken or lying endpoint can never enable the GP preference.
pub fn parse_gp_health(body: &str) -> crate::selection::GpHealthSnapshot {
    let healthy_slots = serde_json::from_str::<Value>(body)
        .ok()
        .and_then(|v| v.get("healthy_slots").and_then(Value::as_u64))
        .unwrap_or(0) as usize;
    crate::selection::GpHealthSnapshot { healthy_slots }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── parse_messages_response (pure) ────────────────────────────────────────

    #[test]
    fn parse_valid_message_extracts_text() {
        let body = r#"{
            "id": "msg_gp_abc",
            "type": "message",
            "role": "assistant",
            "content": [{"type": "text", "text": "decision, security"}],
            "model": "gemini-2.0-flash",
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 10, "output_tokens": 5}
        }"#;
        assert_eq!(
            parse_messages_response(body),
            LaneResult::Output("decision, security".into())
        );
    }

    #[test]
    fn parse_concatenates_multiple_text_blocks() {
        let body = r#"{"content": [
            {"type": "text", "text": "hello "},
            {"type": "text", "text": "world"}
        ]}"#;
        assert_eq!(
            parse_messages_response(body),
            LaneResult::Output("hello world".into())
        );
    }

    #[test]
    fn parse_pool_exhausted_error_is_unavailable() {
        let body = r#"{"error": {"type": "api_error", "message": "all Gemini providers exhausted"}}"#;
        match parse_messages_response(body) {
            LaneResult::Unavailable(reason) => {
                assert!(reason.contains("exhausted"), "reason: {reason}");
            }
            other => panic!("pool-exhausted must be Unavailable, got: {other:?}"),
        }
    }

    #[test]
    fn parse_error_envelope_is_unavailable_never_output() {
        let body = r#"{"error": {"type": "overloaded_error", "message": "try later"}}"#;
        assert!(matches!(
            parse_messages_response(body),
            LaneResult::Unavailable(_)
        ));
    }

    #[test]
    fn parse_non_json_is_unavailable() {
        assert!(matches!(
            parse_messages_response("<html>bad gateway</html>"),
            LaneResult::Unavailable(_)
        ));
    }

    #[test]
    fn parse_empty_completion_is_unavailable() {
        let body = r#"{"content": []}"#;
        assert!(matches!(
            parse_messages_response(body),
            LaneResult::Unavailable(_)
        ));
    }

    // ── post_messages transport (localhost, no live proxy dependency) ─────────

    /// Connection-refused must map to Unavailable so the spill continues.
    /// Uses an ephemeral port that was bound then released — nothing listens.
    #[test]
    fn post_to_dead_port_is_unavailable() {
        let port = {
            let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
            listener.local_addr().expect("addr").port()
            // listener dropped here — port is closed again
        };
        let url = format!("http://127.0.0.1:{port}/v1/messages");
        let result = post_messages(&url, "ping", Duration::from_secs(3)).expect("no io error");
        assert!(
            matches!(result, LaneResult::Unavailable(_)),
            "dead port must be Unavailable, got: {result:?}"
        );
    }

    /// Regression (E1-S6 review): a payload larger than the OS pipe buffer
    /// sent to a server that accepts but never reads must still return within
    /// the bounded deadline — the stdin write must not block the caller.
    #[test]
    fn post_large_payload_to_stuck_server_is_bounded() {
        use std::net::TcpListener;
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind");
        let addr = listener.local_addr().expect("addr");
        // Accept connections but never read from them.
        let _accept = std::thread::spawn(move || {
            let mut held = Vec::new();
            while let Ok((stream, _)) = listener.accept() {
                held.push(stream);
            }
        });

        let big_prompt = "x".repeat(1_000_000); // ~1 MiB, >> 64 KiB pipe buffer
        let url = format!("http://{addr}/v1/messages");
        let start = Instant::now();
        let result =
            post_messages(&url, &big_prompt, Duration::from_secs(2)).expect("no io error");
        assert!(
            matches!(result, LaneResult::Unavailable(_)),
            "stuck server must be Unavailable, got Output"
        );
        assert!(
            start.elapsed() < Duration::from_secs(2 + 5 + 3),
            "must return within the secs+5 deadline, took {:?}",
            start.elapsed()
        );
    }

    // ── GP health probe ───────────────────────────────────────────────────────

    #[test]
    fn parse_gp_health_reads_healthy_slots() {
        let gp = parse_gp_health(r#"{"status":"ok","healthy_slots":4,"total_slots":6}"#);
        assert_eq!(gp.healthy_slots, 4);
        assert!(gp.is_healthy());
    }

    #[test]
    fn parse_gp_health_zero_slots_is_unhealthy() {
        let gp = parse_gp_health(r#"{"status":"ok","healthy_slots":0,"total_slots":6}"#);
        assert!(!gp.is_healthy());
    }

    #[test]
    fn parse_gp_health_garbage_is_unhealthy() {
        assert!(!parse_gp_health("<html>502</html>").is_healthy());
        assert!(!parse_gp_health("").is_healthy());
        assert!(!parse_gp_health(r#"{"status":"ok"}"#).is_healthy());
        assert!(!parse_gp_health(r#"{"healthy_slots":"three"}"#).is_healthy());
    }

    /// Probe against a dead port must report unhealthy — never a guess upward.
    #[test]
    fn probe_gp_health_dead_port_is_unhealthy() {
        let port = {
            let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("bind");
            listener.local_addr().expect("addr").port()
        };
        let url = format!("http://127.0.0.1:{port}/health");
        let gp = probe_gp_health(&url, Duration::from_secs(2));
        assert!(!gp.is_healthy());
    }
}
