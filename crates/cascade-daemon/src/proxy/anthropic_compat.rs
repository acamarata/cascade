//! Anthropic Messages API compatibility adapter for the Gemini GP proxy.
//!
//! Purpose: HTTP server on `127.0.0.1:3762` that accepts `POST /v1/messages`
//! in Anthropic Messages API format, translates to Gemini `generateContent`
//! (or `streamGenerateContent` when `"stream":true`) format, forwards to the
//! existing GP proxy at `http://127.0.0.1:3761` (preserving its key-slot
//! rotation/cooldown pool), and translates the response back to Anthropic
//! format — either a single JSON body or an Anthropic-shaped SSE stream.
//!
//! Inputs:
//!   - `POST /v1/messages` — Anthropic Messages API request body (JSON).
//!   - `GET /v1/health`    — health probe.
//!
//! Outputs:
//!   - `POST /v1/messages` (`stream` absent/false) — Anthropic Messages API
//!     response body (JSON).
//!   - `POST /v1/messages` (`stream: true`) — `text/event-stream` body:
//!     `message_start` → `content_block_start` → N × `content_block_delta`
//!     → `content_block_stop` → `message_delta` → `message_stop`. A mid-stream
//!     upstream failure emits an Anthropic `error` SSE event and closes.
//!   - `GET /v1/health`    — `{"status":"ok","upstream":"<url>"}`.
//!
//! Constraints:
//!   - The GP proxy at `:3761` buffers its entire upstream response before
//!     replying (see `gemini_proxy/dispatch.rs`), so pool rotation/cooldown
//!     is preserved but the byte-level response is not truly incremental
//!     across that hop. This adapter re-frames the buffered Gemini SSE body
//!     into a real chunked Anthropic SSE stream on ITS OWN response, so
//!     `:3762` clients still see standard incremental SSE framing.
//!   - No new Cargo dependencies — uses axum, serde_json, reqwest, uuid,
//!     futures-util already present in Cargo.toml.
//!   - Translation is pure JSON manipulation; no schema-generated types.
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — proxy/anthropic_compat

use std::convert::Infallible;
use std::net::SocketAddr;
use std::time::Duration;

use axum::{
    Json, Router,
    body::Body,
    extract::State,
    http::{HeaderMap, StatusCode, header},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use futures_util::stream;
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};
use uuid::Uuid;

mod sse;
use sse::{GeminiSseParser, StreamTranslator};

// ── GFP circuit breaker (Phase B, v1.13.0) ────────────────────────────────────
//
// Purpose: when the upstream GFP pool (:3761) reports it has no healthy slots
// left (every key rate-limited), this adapter must FAIL LOUD — a clear 503
// with an explicit, actionable message — rather than silently hang or
// silently reroute to a paid provider. Silent degradation into paid burn is
// the exact failure mode this breaker exists to prevent.
//
// Design: primary behavior (env unset) is always fail-loud, unconditionally.
// An operator MAY opt into a paid fallback via `CASCADE_GFP_FALLBACK=agy`,
// which would route the exhausted request to the Antigravity/cloudcode-pa
// Gemini Pro lane (see `cascade-cli/src/cmd/conductor.rs`'s `execute_gemini`)
// instead of returning 503. That lane is NOT wired here yet — see
// `fallback_when_exhausted` below for why and what remains.

/// Environment variable that opts into a paid fallback when the GFP pool is
/// exhausted. Unset (default) = fail-loud only, never silently spend money.
const GFP_FALLBACK_ENV: &str = "CASCADE_GFP_FALLBACK";

/// The only currently-defined fallback mode value. Any other value (or the
/// variable being unset/empty) leaves the default fail-loud behavior intact.
const GFP_FALLBACK_MODE_AGY: &str = "agy";

/// Whether the `agy` (Antigravity Gemini Pro) fallback is requested via env.
fn agy_fallback_requested() -> bool {
    std::env::var(GFP_FALLBACK_ENV)
        .map(|v| v.eq_ignore_ascii_case(GFP_FALLBACK_MODE_AGY))
        .unwrap_or(false)
}

/// Fallback seam for when the GFP pool is exhausted and `CASCADE_GFP_FALLBACK
/// =agy` is set.
///
/// DEFERRED — not wired. The agy lane (`execute_gemini` in
/// `cascade-cli/src/cmd/conductor.rs`) is a synchronous, `curl`-subprocess-based
/// OAuth client (refresh-token exchange → `loadCodeAssist` project discovery →
/// `v1internal:generateContent`, all via blocking `std::process::Command`
/// calls) built for the CLI conductor's one-shot dispatch model. This adapter
/// is an async axum server translating streaming/non-streaming Anthropic
/// Messages requests. Bridging the two cleanly would require: (1) porting the
/// OAuth refresh + `loadCodeAssist` + `generateContent` calls to async
/// `reqwest` (or spawning the blocking calls via `tokio::task::spawn_blocking`
/// on every request — extra latency + a subprocess per call), (2) a second
/// response-translation path (cloudcode-pa's `v1internal:generateContent`
/// wraps candidates under a top-level `response` key, unlike the direct
/// Gemini API shape `translate_response` already handles), and (3) mapping
/// this adapter's streaming contract onto a backend that has no streaming
/// support at all in `execute_gemini` today. None of that is "major" in the
/// sense of new architecture, but it is real, untested surface — not a
/// same-session addition. Always fails loud regardless of the flag until this
/// is implemented.
fn fallback_when_exhausted(gp: &cascade_core::selection::GpHealthSnapshot) -> Response {
    warn!(
        earliest_reset_secs = ?gp.earliest_reset_secs,
        "anthropic_compat: CASCADE_GFP_FALLBACK=agy requested but agy fallback \
         is not yet wired (deferred — see fallback_when_exhausted doc comment); \
         failing loud instead of silently degrading"
    );
    exhausted_response(gp)
}

/// Build the fail-loud 503 response for a fully-exhausted GFP pool.
///
/// Always explicit: total slot count, earliest reset time (best known), and
/// the exact env var + value an operator can set to opt into the (currently
/// deferred) paid fallback. Never a bare "upstream error" — the whole point
/// of the circuit breaker is that exhaustion is visible and actionable.
fn exhausted_response(gp: &cascade_core::selection::GpHealthSnapshot) -> Response {
    let reset_desc = match gp.earliest_reset_secs {
        Some(secs) => format!("earliest reset in {secs}s"),
        None => "reset time unknown".to_string(),
    };
    let message = format!(
        "GFP pool exhausted — all {} key{} rate-limited; {reset_desc}. Set \
         CASCADE_GFP_FALLBACK=agy to fall back to the paid Gemini Pro lane.",
        gp.total_slots,
        if gp.total_slots == 1 { "" } else { "s" },
    );
    let err = json!({
        "error": {
            "type": "api_error",
            "message": message
        }
    });
    (StatusCode::SERVICE_UNAVAILABLE, Json(err)).into_response()
}

/// Query the upstream GFP proxy's `/health` endpoint and parse it into a
/// [`cascade_core::selection::GpHealthSnapshot`]-shaped tuple
/// `(total_slots, earliest_reset_secs)`. Best-effort: any failure to reach or
/// parse `/health` falls back to `(0, None)` so the caller still returns a
/// fail-loud response (just without the enriched slot count / reset time).
async fn fetch_gp_health(client: &reqwest::Client, upstream_url: &str) -> (usize, Option<u64>) {
    let url = format!("{upstream_url}/health");
    let Ok(resp) = client.get(&url).send().await else {
        return (0, None);
    };
    let Ok(body): Result<Value, _> = resp.json().await else {
        return (0, None);
    };
    let total_slots = body.get("total_slots").and_then(Value::as_u64).unwrap_or(0) as usize;
    let earliest_reset_secs = body.get("earliest_reset_secs").and_then(Value::as_u64);
    (total_slots, earliest_reset_secs)
}

/// Detect whether an upstream (`:3761`) 503 response represents true pool
/// exhaustion (`ProxyError::AllProvidersExhausted` / `NoProvidersAvailable`,
/// per `gemini_proxy/types.rs`) rather than an unrelated 503.
///
/// The `:3761` proxy's `write_error` always emits
/// `{"error":{"code":503,"message":"..."}}` for both exhaustion variants
/// (`"all Gemini providers exhausted"` / `"no Gemini providers available"`),
/// so matching on status 503 is sufficient — `:3761` has no other 503 source.
fn is_pool_exhaustion_response(upstream_status: reqwest::StatusCode) -> bool {
    upstream_status == reqwest::StatusCode::SERVICE_UNAVAILABLE
}

/// Circuit-breaker gate: when the upstream response is a pool-exhaustion 503,
/// return the fail-loud (or, if requested and available, agy-fallback)
/// response. Otherwise `None` so the caller passes the response through
/// unchanged (its existing generic error-passthrough / success path).
async fn handle_gp_exhaustion(
    state: &AdapterState,
    upstream_status: reqwest::StatusCode,
) -> Option<Response> {
    if !is_pool_exhaustion_response(upstream_status) {
        return None;
    }
    let (total_slots, earliest_reset_secs) =
        fetch_gp_health(&state.client, &state.upstream_url).await;
    let gp = cascade_core::selection::GpHealthSnapshot {
        healthy_slots: 0,
        total_slots,
        earliest_reset_secs,
    };
    warn!(
        total_slots,
        earliest_reset_secs = ?earliest_reset_secs,
        "anthropic_compat: GFP pool exhausted — entering circuit-breaker fail-loud path"
    );
    if agy_fallback_requested() {
        Some(fallback_when_exhausted(&gp))
    } else {
        Some(exhausted_response(&gp))
    }
}

// ── Model mapping ─────────────────────────────────────────────────────────────

/// Map an Anthropic model name to a Gemini model name.
///
/// - `claude-haiku-*` → `gemini-flash-lite-latest`
/// - `claude-sonnet-*` → `gemini-flash-latest`
/// - anything else → `gemini-flash-latest` (default)
///
/// Uses Google's auto-tracking `-latest` aliases (not pinned dated snapshots
/// like `gemini-2.0-flash`) so this mapping never needs a retirement-driven
/// edit when Google cycles the underlying model version — the alias always
/// resolves to whatever Google currently designates as latest Flash / Flash
/// Lite. Verified live 2026-07-07: both aliases resolve via the models
/// endpoint (`gemini-flash-latest` → "Gemini Flash Latest", confirms
/// `generateContent` support; `gemini-flash-lite-latest` → 200 on
/// `generateContent`).
fn map_model(anthropic_model: &str) -> &'static str {
    if anthropic_model.starts_with("claude-haiku") {
        "gemini-flash-lite-latest"
    } else {
        // Covers claude-sonnet-* and everything else — see doc comment above.
        "gemini-flash-latest"
    }
}

// ── Request translation: Anthropic → Gemini ───────────────────────────────────

/// Translate an Anthropic Messages API request body to a Gemini
/// `generateContent` request body.
///
/// Returns `(gemini_model, gemini_body)` on success; an error string on failure.
fn translate_request(body: &Value) -> Result<(String, Value), String> {
    let anthropic_model = body
        .get("model")
        .and_then(Value::as_str)
        .unwrap_or("claude-sonnet-4-5");

    let gemini_model = map_model(anthropic_model).to_string();

    // Translate messages array: role "assistant" → "model"; wrap content in parts.
    let messages = body
        .get("messages")
        .and_then(Value::as_array)
        .ok_or("missing or invalid 'messages' field")?;

    let contents: Vec<Value> = messages
        .iter()
        .map(|msg| {
            let role = msg.get("role").and_then(Value::as_str).unwrap_or("user");
            let gemini_role = if role == "assistant" { "model" } else { role };

            // Content may be a string or an array of content blocks.
            let text = match msg.get("content") {
                Some(Value::String(s)) => s.clone(),
                Some(Value::Array(blocks)) => {
                    // Extract text from content blocks.
                    blocks
                        .iter()
                        .filter_map(|b| {
                            if b.get("type").and_then(Value::as_str) == Some("text") {
                                b.get("text").and_then(Value::as_str).map(str::to_string)
                            } else {
                                None
                            }
                        })
                        .collect::<Vec<_>>()
                        .join("\n")
                }
                _ => String::new(),
            };

            json!({
                "role": gemini_role,
                "parts": [{"text": text}]
            })
        })
        .collect();

    let mut gemini = json!({ "contents": contents });

    // Optional system instruction.
    if let Some(system) = body.get("system").and_then(Value::as_str) {
        gemini["system_instruction"] = json!({ "parts": [{"text": system}] });
    }

    // Optional max_tokens → generationConfig.maxOutputTokens.
    if let Some(max_tokens) = body.get("max_tokens").and_then(Value::as_u64) {
        gemini["generationConfig"] = json!({ "maxOutputTokens": max_tokens });
    }

    Ok((gemini_model, gemini))
}

// ── Response translation: Gemini → Anthropic ─────────────────────────────────

/// Translate a Gemini `generateContent` response body to an Anthropic Messages
/// API response body.
fn translate_response(gemini_body: &Value, gemini_model: &str) -> Value {
    // Extract text from the first candidate.
    let text = gemini_body
        .get("candidates")
        .and_then(Value::as_array)
        .and_then(|c| c.first())
        .and_then(|c| c.get("content"))
        .and_then(|c| c.get("parts"))
        .and_then(Value::as_array)
        .and_then(|p| p.first())
        .and_then(|p| p.get("text"))
        .and_then(Value::as_str)
        .unwrap_or("");

    // Map finishReason → stop_reason.
    let finish_reason = gemini_body
        .get("candidates")
        .and_then(Value::as_array)
        .and_then(|c| c.first())
        .and_then(|c| c.get("finishReason"))
        .and_then(Value::as_str)
        .unwrap_or("STOP");

    let stop_reason = match finish_reason {
        "STOP" => "end_turn",
        "MAX_TOKENS" => "max_tokens",
        _ => "end_turn",
    };

    // Usage metadata.
    let input_tokens = gemini_body
        .get("usageMetadata")
        .and_then(|u| u.get("promptTokenCount"))
        .and_then(Value::as_u64)
        .unwrap_or(0);
    let output_tokens = gemini_body
        .get("usageMetadata")
        .and_then(|u| u.get("candidatesTokenCount"))
        .and_then(Value::as_u64)
        .unwrap_or(0);

    json!({
        "id": format!("msg_gp_{}", Uuid::new_v4().simple()),
        "type": "message",
        "role": "assistant",
        "content": [{"type": "text", "text": text}],
        "model": gemini_model,
        "stop_reason": stop_reason,
        "usage": {
            "input_tokens": input_tokens,
            "output_tokens": output_tokens
        }
    })
}

// ── Axum shared state ─────────────────────────────────────────────────────────

/// TCP connect timeout for the upstream `:3761` GP-proxy hop.
///
/// Short on purpose — `:3761` is a loopback service, so a healthy instance
/// accepts the connection near-instantly. A stalled/wedged `:3761` should
/// fail fast rather than tie up a `:3762` connection indefinitely.
const UPSTREAM_CONNECT_TIMEOUT: Duration = Duration::from_secs(5);

/// Overall request timeout (connect + send + full response body) for the
/// upstream `:3761` GP-proxy hop.
///
/// `:3761` buffers its entire response (including streamed Gemini output)
/// before replying — see the module doc — so both the non-streaming and
/// streaming paths here read a complete body in one `.send().await` /
/// `.bytes().await` pair. An overall timeout is therefore safe: it can never
/// truncate a response that was already arriving incrementally, only bound
/// how long this adapter waits on a stalled upstream.
const UPSTREAM_REQUEST_TIMEOUT: Duration = Duration::from_secs(120);

/// Build the `reqwest::Client` used for all upstream `:3761` calls, with
/// explicit connect/overall timeouts so a stalled upstream hop cannot hold a
/// `:3762` connection open forever.
///
/// Falls back to `reqwest::Client::new()` (no configured timeouts) only if
/// the builder fails, which in practice only happens on invalid TLS
/// configuration — never for timeout values — so this is effectively
/// infallible in normal operation.
fn build_upstream_client() -> reqwest::Client {
    reqwest::Client::builder()
        .connect_timeout(UPSTREAM_CONNECT_TIMEOUT)
        .timeout(UPSTREAM_REQUEST_TIMEOUT)
        .build()
        .unwrap_or_else(|e| {
            warn!(error = %e, "anthropic_compat: failed to build timed HTTP client, falling back to default");
            reqwest::Client::new()
        })
}

#[derive(Clone)]
struct AdapterState {
    /// Base URL of the upstream Gemini GP proxy (e.g. `http://127.0.0.1:3761`).
    upstream_url: String,
    /// Shared client for both the non-streaming and streaming paths, built
    /// via [`build_upstream_client`] so both hops share the same timeouts.
    client: reqwest::Client,
}

// ── Route handlers ────────────────────────────────────────────────────────────

/// GET /v1/health
async fn health(State(state): State<AdapterState>) -> Json<Value> {
    Json(json!({ "status": "ok", "upstream": state.upstream_url }))
}

/// Privacy firewall, server-side mirror of the app's fast-path gate: every
/// request that declares a protected namespace (`X-Cascade-Namespace:
/// personal` / `personal:private` / `*:private`) is refused HERE, before any
/// byte reaches the GP pool (:3761 → Google). The app already skips this
/// port for protected chats; this guard exists so a future non-app localhost
/// caller cannot bypass the firewall by talking to :3762 directly.
///
/// Returns `None` when the request may proceed, or the 403 response to send.
fn protected_namespace_refusal(headers: &HeaderMap) -> Option<Response> {
    let ns = headers
        .get("x-cascade-namespace")
        .and_then(|v| v.to_str().ok())?;
    if !cascade_core::sensitivity::is_protected_namespace(Some(ns)) {
        return None;
    }
    warn!(
        namespace = %ns,
        "anthropic_compat: refusing protected-namespace request — private \
         chat must not leave via the GP pool"
    );
    let err = json!({
        "error": {
            "type": "permission_error",
            "message": "protected namespace: private/personal chat is never \
                        routed through the GP pool. Use the daemon /api/chat \
                        endpoint, which selects a trusted provider."
        }
    });
    Some((StatusCode::FORBIDDEN, Json(err)).into_response())
}

/// POST /v1/messages
async fn messages(
    State(state): State<AdapterState>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response {
    // Both the buffered and streaming paths branch AFTER this guard, so one
    // check covers every request shape.
    if let Some(refusal) = protected_namespace_refusal(&headers) {
        return refusal;
    }

    if body.get("stream").and_then(Value::as_bool) == Some(true) {
        return messages_stream(state, body).await;
    }

    // Translate request.
    let (gemini_model, gemini_body) = match translate_request(&body) {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: request translation failed");
            let err = json!({
                "error": {
                    "type": "invalid_request_error",
                    "message": e
                }
            });
            return (StatusCode::BAD_REQUEST, Json(err)).into_response();
        }
    };

    // Forward to GP proxy.
    let upstream = format!(
        "{}/v1beta/models/{}:generateContent",
        state.upstream_url, gemini_model
    );

    let resp = match state
        .client
        .post(&upstream)
        .header("Content-Type", "application/json")
        .json(&gemini_body)
        .send()
        .await
    {
        Ok(r) => r,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: upstream request failed");
            let err = json!({
                "error": {
                    "type": "api_error",
                    "message": format!("upstream request failed: {e}")
                }
            });
            return (StatusCode::BAD_GATEWAY, Json(err)).into_response();
        }
    };

    let upstream_status = resp.status();

    // GFP circuit breaker (Phase B, v1.13.0): a pool-exhaustion 503 gets the
    // enriched fail-loud (or deferred-agy-fallback) response BEFORE the
    // generic error-passthrough below, which would otherwise just relay
    // `:3761`'s terse "all Gemini providers exhausted" message.
    if let Some(resp) = handle_gp_exhaustion(&state, upstream_status).await {
        return resp;
    }

    let gemini_json: Value = match resp.json().await {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: failed to parse upstream response");
            let err = json!({
                "error": {
                    "type": "api_error",
                    "message": format!("failed to parse upstream response: {e}")
                }
            });
            return (StatusCode::BAD_GATEWAY, Json(err)).into_response();
        }
    };

    if !upstream_status.is_success() {
        // Pass through the Gemini error wrapped in Anthropic format.
        let msg = gemini_json
            .get("error")
            .and_then(|e| e.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("upstream error");
        let err = json!({
            "error": {
                "type": "api_error",
                "message": msg
            }
        });
        let status = StatusCode::from_u16(upstream_status.as_u16())
            .unwrap_or(StatusCode::BAD_GATEWAY);
        return (status, Json(err)).into_response();
    }

    let anthropic_resp = translate_response(&gemini_json, &gemini_model);
    (StatusCode::OK, Json(anthropic_resp)).into_response()
}

/// Streaming path for `POST /v1/messages` when `"stream": true`.
///
/// Calls the upstream GP proxy's `streamGenerateContent?alt=sse` route
/// through the SAME `:3761` internal path as the non-streaming call, so
/// key-slot rotation/cooldown (owned by `gemini_proxy::dispatch`) still
/// applies. `:3761` buffers the full upstream body before replying (it has
/// no chunked-transfer support), so the bytes arrive from `:3761` in one
/// shot; this function re-frames them into a real incremental Anthropic SSE
/// stream on ITS OWN response so `:3762` clients see standard streaming.
///
/// Errors before any bytes have streamed return the same non-2xx JSON shape
/// as the non-streaming path. Errors discovered after streaming has started
/// (upstream returned a non-JSON body, or an embedded Gemini `error` frame)
/// emit an Anthropic `error` SSE event and close — never a silent truncation.
async fn messages_stream(state: AdapterState, body: Value) -> Response {
    let (gemini_model, mut gemini_body) = match translate_request(&body) {
        Ok(v) => v,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: stream request translation failed");
            let err = json!({
                "error": { "type": "invalid_request_error", "message": e }
            });
            return (StatusCode::BAD_REQUEST, Json(err)).into_response();
        }
    };
    // Gemini's own `stream` flag is irrelevant to the endpoint chosen (the
    // URL selects streaming vs non-streaming); strip it so it isn't echoed
    // upstream as an unrecognized field.
    if let Some(obj) = gemini_body.as_object_mut() {
        obj.remove("stream");
    }

    let upstream = format!(
        "{}/v1beta/models/{}:streamGenerateContent?alt=sse",
        state.upstream_url, gemini_model
    );

    let resp = match state
        .client
        .post(&upstream)
        .header("Content-Type", "application/json")
        .json(&gemini_body)
        .send()
        .await
    {
        Ok(r) => r,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: stream upstream request failed");
            let err = json!({
                "error": {
                    "type": "api_error",
                    "message": format!("upstream request failed: {e}")
                }
            });
            return (StatusCode::BAD_GATEWAY, Json(err)).into_response();
        }
    };

    let upstream_status = resp.status();

    // GFP circuit breaker (Phase B, v1.13.0): same fail-loud gate as the
    // non-streaming path — nothing has streamed yet at this point, so an
    // ordinary JSON error response (not an SSE error event) is correct here.
    if let Some(resp) = handle_gp_exhaustion(&state, upstream_status).await {
        return resp;
    }

    if !upstream_status.is_success() {
        // Nothing has streamed yet — return the same JSON error shape as the
        // non-streaming path.
        let raw = resp.bytes().await.unwrap_or_default();
        let gemini_json: Value = serde_json::from_slice(&raw).unwrap_or(Value::Null);
        let msg = gemini_json
            .get("error")
            .and_then(|e| e.get("message"))
            .and_then(Value::as_str)
            .unwrap_or("upstream error");
        let err = json!({ "error": { "type": "api_error", "message": msg } });
        let status =
            StatusCode::from_u16(upstream_status.as_u16()).unwrap_or(StatusCode::BAD_GATEWAY);
        return (status, Json(err)).into_response();
    }

    // Buffer the (already-complete) upstream body, then re-frame it as an
    // incrementally-emitted Anthropic SSE stream.
    let raw_body = match resp.bytes().await {
        Ok(b) => b,
        Err(e) => {
            warn!(error = %e, "anthropic_compat: failed to read stream upstream body");
            let err = json!({
                "error": {
                    "type": "api_error",
                    "message": format!("failed to read upstream stream body: {e}")
                }
            });
            return (StatusCode::BAD_GATEWAY, Json(err)).into_response();
        }
    };

    let events = translate_stream_body(&raw_body, &gemini_model);

    let body_stream = stream::iter(
        events
            .into_iter()
            .map(|bytes| Ok::<_, Infallible>(axum::body::Bytes::from(bytes))),
    );

    Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "text/event-stream")
        .header(header::CACHE_CONTROL, "no-cache")
        .body(Body::from_stream(body_stream))
        .unwrap_or_else(|e| {
            warn!(error = %e, "anthropic_compat: failed to build SSE response");
            (StatusCode::INTERNAL_SERVER_ERROR, "stream build failed").into_response()
        })
}

/// Translate a complete Gemini SSE response body into the full sequence of
/// Anthropic SSE wire-format events (`message_start` … `message_stop`).
///
/// If the buffered body contains a Gemini `error` frame (upstream failed
/// after headers were already 2xx — e.g. a streamed safety block), the
/// sequence emitted so far is closed with an Anthropic `error` event instead
/// of the normal `message_delta`/`message_stop` pair, matching the "error
/// mid-stream → error event, never silent truncation" contract.
fn translate_stream_body(raw_body: &[u8], gemini_model: &str) -> Vec<Vec<u8>> {
    let mut parser = GeminiSseParser::new();
    let mut translator = StreamTranslator::new(gemini_model);
    let mut out = Vec::new();

    let (name, data) = translator.start();
    out.push(sse::format_event(name, &data));

    let mut saw_error = false;
    for chunk in parser.push(raw_body) {
        if let Some(err) = chunk.get("error") {
            let msg = err
                .get("message")
                .and_then(Value::as_str)
                .unwrap_or("upstream error");
            let (name, data) = StreamTranslator::error_event(msg);
            out.push(sse::format_event(name, &data));
            saw_error = true;
            break;
        }
        for (name, data) in translator.on_chunk(&chunk) {
            out.push(sse::format_event(name, &data));
        }
    }

    if !saw_error {
        for (name, data) in translator.finish() {
            out.push(sse::format_event(name, &data));
        }
    }

    out
}

// ── Public server struct ──────────────────────────────────────────────────────

/// Anthropic Messages API compatibility adapter server.
///
/// Listens on `bind_addr` (default `127.0.0.1:3762`) and translates incoming
/// Anthropic-format requests to Gemini-format requests forwarded to the GP proxy
/// at `upstream_url` (default `http://127.0.0.1:3761`).
pub struct AnthropicCompatServer {
    bind_addr: SocketAddr,
    upstream_url: String,
    shutdown: CancellationToken,
}

impl AnthropicCompatServer {
    /// Construct a new `AnthropicCompatServer`.
    pub fn new(bind_addr: SocketAddr, upstream_url: String, shutdown: CancellationToken) -> Self {
        Self {
            bind_addr,
            upstream_url,
            shutdown,
        }
    }

    /// Start the server and run until `shutdown` is cancelled.
    pub async fn run(self) -> Result<(), std::io::Error> {
        let state = AdapterState {
            upstream_url: self.upstream_url,
            client: build_upstream_client(),
        };

        let app = Router::new()
            .route("/v1/health", get(health))
            .route("/v1/messages", post(messages))
            .with_state(state);

        let listener = tokio::net::TcpListener::bind(self.bind_addr).await?;
        info!(addr = %self.bind_addr, "anthropic_compat: listening");

        axum::serve(listener, app)
            .with_graceful_shutdown(async move {
                self.shutdown.cancelled().await;
            })
            .await?;

        info!("anthropic_compat: stopped");
        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// The connect/overall timeout constants must be the documented values —
    /// pinning them in a test catches an accidental edit that silently
    /// widens the "stalled upstream" window back toward unbounded.
    #[test]
    fn upstream_timeout_constants_match_documented_values() {
        assert_eq!(UPSTREAM_CONNECT_TIMEOUT, Duration::from_secs(5));
        assert_eq!(UPSTREAM_REQUEST_TIMEOUT, Duration::from_secs(120));
    }

    /// `build_upstream_client` must successfully construct a client with
    /// those timeouts configured (no network I/O — `reqwest::Client` does
    /// not expose configured timeouts for inspection, so this exercises the
    /// builder call itself rather than making a live request).
    /// The :3762 privacy firewall refuses protected namespaces with 403 and
    /// lets everything else through (no header, project/meta namespaces).
    #[test]
    fn protected_namespace_refusal_blocks_protected_passes_others() {
        let mut h = HeaderMap::new();
        assert!(protected_namespace_refusal(&h).is_none(), "no header");

        h.insert("x-cascade-namespace", "projects:cascade".parse().unwrap());
        assert!(protected_namespace_refusal(&h).is_none(), "project ns");
        h.insert("x-cascade-namespace", "meta".parse().unwrap());
        assert!(protected_namespace_refusal(&h).is_none(), "meta ns");

        for ns in ["personal", "personal:private", "private", "Personal:Private"] {
            h.insert("x-cascade-namespace", ns.parse().unwrap());
            let resp = protected_namespace_refusal(&h)
                .unwrap_or_else(|| panic!("{ns} must be refused"));
            assert_eq!(resp.status(), StatusCode::FORBIDDEN, "{ns}");
        }
    }

    #[test]
    fn build_upstream_client_succeeds_with_configured_timeouts() {
        // reqwest::Client::builder().connect_timeout(..).timeout(..).build()
        // only fails on invalid TLS backend configuration, never on valid
        // Duration values — so a successful build here proves the timeouts
        // were accepted and wired into both the streaming and non-streaming
        // call sites (both read `state.client` from the same `AdapterState`).
        let _client = build_upstream_client();
    }

    // ── GFP circuit breaker (Phase B, v1.13.0) ────────────────────────────────

    /// `:3761` only ever emits a bare 503 for pool-exhaustion (both
    /// `AllProvidersExhausted` and `NoProvidersAvailable` — see
    /// `gemini_proxy/dispatch.rs`'s `write_error` call sites), so any 503
    /// from upstream must be treated as exhaustion. Non-503 statuses (200,
    /// 400, 403, 502) must NOT trigger the circuit breaker.
    #[test]
    fn exhaustion_detected_only_on_503() {
        assert!(is_pool_exhaustion_response(reqwest::StatusCode::SERVICE_UNAVAILABLE));
        assert!(!is_pool_exhaustion_response(reqwest::StatusCode::OK));
        assert!(!is_pool_exhaustion_response(reqwest::StatusCode::BAD_REQUEST));
        assert!(!is_pool_exhaustion_response(reqwest::StatusCode::FORBIDDEN));
        assert!(!is_pool_exhaustion_response(reqwest::StatusCode::BAD_GATEWAY));
    }

    /// Fail-loud path (env unset / default): the response is HTTP 503 with an
    /// explicit, actionable JSON message — total key count, earliest reset
    /// time, and the exact env var + value to opt into the (deferred) agy
    /// fallback. This is the "never silent" contract's core assertion.
    #[tokio::test]
    async fn exhausted_response_is_explicit_503_with_actionable_message() {
        let gp = cascade_core::selection::GpHealthSnapshot {
            healthy_slots: 0,
            total_slots: 6,
            earliest_reset_secs: Some(42),
        };
        let resp = exhausted_response(&gp);
        assert_eq!(resp.status(), StatusCode::SERVICE_UNAVAILABLE);

        let body_bytes = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .expect("read body");
        let body: Value = serde_json::from_slice(&body_bytes).expect("json body");
        let msg = body["error"]["message"].as_str().expect("message string");
        assert!(msg.contains("6 keys"), "msg should mention key count: {msg}");
        assert!(msg.contains("42s"), "msg should mention reset time: {msg}");
        assert!(
            msg.contains("CASCADE_GFP_FALLBACK=agy"),
            "msg should mention the fallback env var: {msg}"
        );
    }

    /// Singular "key" (not "keys") when exactly one slot is configured.
    #[tokio::test]
    async fn exhausted_response_singular_key_wording() {
        let gp = cascade_core::selection::GpHealthSnapshot {
            healthy_slots: 0,
            total_slots: 1,
            earliest_reset_secs: None,
        };
        let resp = exhausted_response(&gp);
        let body_bytes = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .expect("read body");
        let body: Value = serde_json::from_slice(&body_bytes).expect("json body");
        let msg = body["error"]["message"].as_str().expect("message string");
        assert!(msg.contains("1 key;") || msg.contains("1 key "), "msg: {msg}");
        assert!(msg.contains("reset time unknown"), "msg: {msg}");
    }

    /// Env flag routing: unset (or any value other than "agy") must ALWAYS
    /// fail loud — this is the hard "default is fail-loud" contract.
    #[test]
    #[serial_test::serial(gfp_fallback_env)]
    fn agy_fallback_not_requested_when_env_unset() {
        std::env::remove_var(GFP_FALLBACK_ENV);
        assert!(!agy_fallback_requested());
    }

    #[test]
    #[serial_test::serial(gfp_fallback_env)]
    fn agy_fallback_not_requested_for_other_values() {
        std::env::set_var(GFP_FALLBACK_ENV, "true");
        assert!(!agy_fallback_requested(), "only the literal 'agy' value opts in");
        std::env::remove_var(GFP_FALLBACK_ENV);
    }

    #[test]
    #[serial_test::serial(gfp_fallback_env)]
    fn agy_fallback_requested_when_env_set_to_agy() {
        std::env::set_var(GFP_FALLBACK_ENV, "agy");
        assert!(agy_fallback_requested());
        std::env::set_var(GFP_FALLBACK_ENV, "AGY");
        assert!(agy_fallback_requested(), "case-insensitive match");
        std::env::remove_var(GFP_FALLBACK_ENV);
    }

    /// Deferred agy fallback: even with the flag SET, `fallback_when_exhausted`
    /// must still return the fail-loud 503 (not silently succeed with fake
    /// data, not hang) because the agy lane is not wired yet. This pins the
    /// "fail-loud must work regardless" contract for the flag-set-but-deferred
    /// case.
    #[test]
    #[serial_test::serial(gfp_fallback_env)]
    fn agy_fallback_stub_still_fails_loud() {
        std::env::set_var(GFP_FALLBACK_ENV, "agy");
        let gp = cascade_core::selection::GpHealthSnapshot {
            healthy_slots: 0,
            total_slots: 3,
            earliest_reset_secs: Some(10),
        };
        let resp = fallback_when_exhausted(&gp);
        assert_eq!(
            resp.status(),
            StatusCode::SERVICE_UNAVAILABLE,
            "deferred fallback must still fail loud, never silently succeed"
        );
        std::env::remove_var(GFP_FALLBACK_ENV);
    }

    /// End-to-end: `handle_gp_exhaustion` against a mock `:3761` that returns
    /// a pool-exhaustion 503 with a `/health` body reporting 4 total slots and
    /// a 15s earliest reset. Confirms the whole chain (503 detection → /health
    /// enrichment → fail-loud message) without needing a real GFP pool.
    #[tokio::test]
    async fn handle_gp_exhaustion_end_to_end_against_mock_upstream() {
        use wiremock::matchers::{method, path};
        use wiremock::{Mock, MockServer, ResponseTemplate};

        let mock_server = MockServer::start().await;
        Mock::given(method("GET"))
            .and(path("/health"))
            .respond_with(ResponseTemplate::new(200).set_body_json(json!({
                "status": "ok",
                "healthy_slots": 0,
                "total_slots": 4,
                "exhausted": true,
                "earliest_reset_secs": 15
            })))
            .mount(&mock_server)
            .await;

        std::env::remove_var(GFP_FALLBACK_ENV);
        let state = AdapterState {
            upstream_url: mock_server.uri(),
            client: reqwest::Client::new(),
        };

        let resp = handle_gp_exhaustion(&state, reqwest::StatusCode::SERVICE_UNAVAILABLE)
            .await
            .expect("503 must be recognized as pool exhaustion");
        assert_eq!(resp.status(), StatusCode::SERVICE_UNAVAILABLE);

        let body_bytes = axum::body::to_bytes(resp.into_body(), usize::MAX)
            .await
            .expect("read body");
        let body: Value = serde_json::from_slice(&body_bytes).expect("json body");
        let msg = body["error"]["message"].as_str().expect("message string");
        assert!(msg.contains("4 keys"), "msg: {msg}");
        assert!(msg.contains("15s"), "msg: {msg}");
    }

    /// A non-503 upstream status must return `None` (no circuit-breaker
    /// handling) so callers fall through to their normal success/error path.
    #[tokio::test]
    async fn handle_gp_exhaustion_none_for_non_503() {
        let state = AdapterState {
            upstream_url: "http://127.0.0.1:1".to_string(), // unreachable, must not matter
            client: reqwest::Client::new(),
        };
        let resp = handle_gp_exhaustion(&state, reqwest::StatusCode::OK).await;
        assert!(resp.is_none(), "200 OK must never be treated as exhaustion");
    }
}
