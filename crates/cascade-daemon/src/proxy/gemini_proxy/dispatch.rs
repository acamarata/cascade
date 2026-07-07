//! HTTP connection handling and request dispatch.
//!
//! Purpose: handle_connection reads one HTTP/1.1 request from a TCP stream,
//! dispatch_request routes it through the RoutingTable with 429 retry logic.
//! SPORT: proxy module (T-P2-E02-36)

use std::sync::Arc;

use tracing::{debug, warn};

use super::state::resolve_api_key;
use super::types::{MAX_REQUEST_BODY_SIZE, ProxyError, ProxyState};
use super::upstream::{
    build_upstream_url, find_header_end, forward_upstream_with_headers, parse_request_line,
    read_exact, status_text, write_error,
};

// ── Request dispatch ──────────────────────────────────────────────────────────

/// Core routing dispatch function.
///
/// Picks the next available slot via `pick_next()`, forwards the request to
/// the Gemini upstream with the slot's API key appended, and handles HTTP 429
/// by marking the slot rate-limited and retrying with the next slot.
///
/// Inputs:  `method`  — HTTP method string ("GET", "POST", etc.).
///          `path`    — URL path (e.g. `/v1beta/models/gemini-3.5-flash:generateContent`).
///          `body`    — raw request body bytes.
///          `state`   — shared proxy state (routing table + credentials + config).
///          `client`  — reqwest client for upstream calls.
///
/// Outputs: `Ok((status_code_str, body_bytes))` on success (200-level from upstream).
///          `Err(ProxyError::AllProvidersExhausted)` when all retries fail with 429.
///          `Err(ProxyError::NoProvidersAvailable)` when the routing table is empty.
///          `Err(ProxyError::Upstream)` on non-429 upstream errors.
///
/// Constraints:
///   - The `state.table` Mutex is acquired, `pick_next()` called, and the
///     Mutex is released BEFORE the async upstream call.
///   - `mark_rate_limited` is called BEFORE the retry attempt (inside the loop)
///     so the exhausted slot is immediately skipped on the next `pick_next` call.
pub async fn dispatch_request(
    method: &str,
    path: &str,
    body: &[u8],
    state: &ProxyState,
    client: &reqwest::Client,
    headers_raw: Option<&str>,
) -> Result<(String, Vec<u8>), ProxyError> {
    // ── Live pool-health endpoint ─────────────────────────────────────────────
    // GET /health reports the LIVE routing-table state — healthy_slots counts
    // enabled (or cooldown-elapsed) slots, so 429 cooldowns are visible. This
    // is the only honest GP-health signal outside this process: the chat
    // GP-preference (:daemon http) and the CLI conductor's T3-GP preference
    // both consume it instead of guessing from static providers.json config.
    // Never forwarded upstream; answered locally before the /v1beta allowlist.
    if path == "/health" && (method == "GET" || method == "HEAD") {
        let gp = {
            let guard = state.table.lock().unwrap();
            cascade_core::selection::gp_health_from_slots(guard.slots())
        };
        // Circuit-breaker observability (Phase B, v1.13.0): "exhausted" and
        // "earliest_reset_secs" make pool degradation visible on /health
        // instead of only surfacing as an opaque 503 at request time.
        let body = serde_json::json!({
            "status": "ok",
            "healthy_slots": gp.healthy_slots,
            "total_slots": gp.total_slots,
            "exhausted": gp.is_exhausted(),
            "earliest_reset_secs": gp.earliest_reset_secs,
        });
        return Ok((
            "200 OK".to_string(),
            serde_json::to_vec(&body).unwrap_or_default(),
        ));
    }

    // ── Path allowlist check ──────────────────────────────────────────────────
    // SIEGE HIGH-1: reject paths outside /v1beta/ to prevent SSRF.
    if !path.starts_with("/v1beta/") {
        let error_body = serde_json::json!({"error":"forbidden","reason":"path not in allowlist"});
        return Ok((
            "403 Forbidden".to_string(),
            serde_json::to_vec(&error_body).unwrap_or_default(),
        ));
    }

    let max_retries = state.config.max_retries;
    let cooldown_secs = state.config.cooldown_secs;

    let mut attempts = 0usize;

    loop {
        // Pick next slot — release Mutex BEFORE the await.
        let slot_opt = {
            let mut guard = state.table.lock().unwrap();
            guard.pick_next()
        };

        let slot = match slot_opt {
            Some(s) => s,
            None => {
                // No available slot — could be empty table (no providers) or all
                // providers currently rate-limited. Distinguish by attempt count:
                // if we've already tried at least once, this means all available
                // providers got rate-limited during this request, i.e. exhausted.
                if attempts == 0 {
                    return Err(ProxyError::NoProvidersAvailable);
                } else {
                    warn!(
                        attempts,
                        "gemini_proxy: GFP pool exhausted — every slot rate-limited, \
                         returning 503 (fail-loud circuit breaker, Phase B v1.13.0)"
                    );
                    return Err(ProxyError::AllProvidersExhausted { attempts });
                }
            }
        };

        // Look up API key: prefer OS keychain, fall back to providers.json.
        // Both guards are released before the .await below.
        let api_key = {
            let creds_guard = state.credentials.lock().unwrap();
            let kc_guard = state.keychain_keys.read().unwrap();
            resolve_api_key(&creds_guard, &kc_guard, &slot.slot_id)
        };

        // Build the upstream URL: strip any existing `key=` param and append ours.
        let upstream_url = build_upstream_url(&state.upstream_base, path, &api_key);

        debug!(
            slot_id = %slot.slot_id,
            attempt = attempts + 1,
            url = %upstream_url,
            "gemini_proxy: forwarding request"
        );

        // Record the request start time for latency measurement.
        let start_time = std::time::Instant::now();

        // Forward the request upstream with header whitelist — async point; Mutex is NOT held here.
        let upstream_result = forward_upstream_with_headers(
            client,
            method,
            &upstream_url,
            body,
            headers_raw.unwrap_or(""),
        )
        .await;

        // Record latency on completion (for both success and 429 responses).
        let latency_ms = start_time.elapsed().as_millis() as u64;

        match upstream_result {
            Ok((429, resp_body)) => {
                // Mark slot rate-limited BEFORE incrementing attempts so the slot
                // is skipped immediately on the next pick_next() call.
                {
                    let mut guard = state.table.lock().unwrap();
                    guard.mark_rate_limited(&slot.slot_id, cooldown_secs);
                    // Record rate-limit response latency and increment rate_limit_count.
                    if let Some(slot_data) =
                        guard.slots_mut().iter_mut().find(|s| s.id == slot.slot_id)
                    {
                        cascade_core::routing_table::record_latency(slot_data, latency_ms);
                        slot_data.rate_limit_count += 1;
                    }
                }
                attempts += 1;
                warn!(
                    slot_id = %slot.slot_id,
                    attempt = attempts,
                    max = max_retries,
                    "gemini_proxy: 429 — slot rate-limited"
                );
                if attempts >= max_retries {
                    return Err(ProxyError::AllProvidersExhausted { attempts });
                }
                // Log the 429 body for debugging but don't return it.
                debug!(body = %String::from_utf8_lossy(&resp_body), "429 upstream body");
                // Continue loop to retry with next slot.
            }
            Ok((status, resp_body)) => {
                // Record latency and success path.
                {
                    let mut guard = state.table.lock().unwrap();
                    if let Some(slot_data) =
                        guard.slots_mut().iter_mut().find(|s| s.id == slot.slot_id)
                    {
                        cascade_core::routing_table::record_latency(slot_data, latency_ms);
                    }
                }
                let status_str = status_text(status);
                return Ok((status_str, resp_body));
            }
            Err(e) => {
                // Record latency and error path (non-429 errors).
                {
                    let mut guard = state.table.lock().unwrap();
                    if let Some(slot_data) =
                        guard.slots_mut().iter_mut().find(|s| s.id == slot.slot_id)
                    {
                        cascade_core::routing_table::record_latency(slot_data, latency_ms);
                        slot_data.error_count += 1;
                    }
                }
                return Err(ProxyError::Upstream(e));
            }
        }
    }
}

// ── HTTP connection handler ───────────────────────────────────────────────────

/// Read one HTTP/1.1 request from `stream`, proxy it via [`dispatch_request`],
/// and write the response back.
///
/// This is a minimal HTTP/1.1 implementation sufficient for the proxy use-case.
/// It reads the request line + headers, reads the body (Content-Length only),
/// calls the routing dispatch function, and writes the upstream response back.
///
/// Inputs:  `stream` — accepted TCP stream.
///          `state`  — shared proxy state.
///          `client` — reqwest client for upstream calls.
pub async fn handle_connection(
    stream: tokio::net::TcpStream,
    state: ProxyState,
    client: Arc<reqwest::Client>,
) {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    let mut stream = stream;
    let mut buf = vec![0u8; 65536];
    let mut offset = 0usize;

    // Read until we find \r\n\r\n (end of HTTP headers).
    let headers_end = loop {
        let n = match stream.read(&mut buf[offset..]).await {
            Ok(0) => return, // connection closed
            Ok(n) => n,
            Err(e) => {
                warn!(error = %e, "gemini_proxy: read error");
                return;
            }
        };
        offset += n;
        if let Some(pos) = find_header_end(&buf[..offset]) {
            break pos;
        }
        if offset >= buf.len() {
            warn!("gemini_proxy: request too large");
            let _ = stream
                .write_all(b"HTTP/1.1 413 Request Too Large\r\n\r\n")
                .await;
            return;
        }
    };

    // Parse the request line and headers.
    let header_slice = match std::str::from_utf8(&buf[..headers_end]) {
        Ok(s) => s,
        Err(_) => {
            let _ = stream.write_all(b"HTTP/1.1 400 Bad Request\r\n\r\n").await;
            return;
        }
    };

    let (method, path, content_length) = match parse_request_line(header_slice) {
        Some(v) => v,
        None => {
            let _ = stream.write_all(b"HTTP/1.1 400 Bad Request\r\n\r\n").await;
            return;
        }
    };

    // Check request body size limit.
    if content_length > MAX_REQUEST_BODY_SIZE {
        let error_body = format!(
            r#"{{"error":"request_too_large","limit_bytes":{}}}"#,
            MAX_REQUEST_BODY_SIZE
        );
        let response = format!(
            "HTTP/1.1 413 Request Too Large\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
            error_body.len()
        );
        let _ = stream.write_all(response.as_bytes()).await;
        let _ = stream.write_all(error_body.as_bytes()).await;
        return;
    }

    // Read body if Content-Length is set.
    let body_start = headers_end; // find_header_end already returns pos after \r\n\r\n
    let already_read = offset.saturating_sub(body_start);
    let body_bytes = if content_length > 0 {
        let remaining = content_length.saturating_sub(already_read);
        let mut body = buf[body_start..offset].to_vec();
        if remaining > 0 {
            let mut extra = vec![0u8; remaining];
            if let Err(e) = read_exact(&mut stream, &mut extra).await {
                warn!(error = %e, "gemini_proxy: body read error");
                return;
            }
            body.extend_from_slice(&extra);
        }
        body
    } else {
        Vec::new()
    };

    // Dispatch the request through the routing table with header whitelist protection.
    let result = dispatch_request(
        &method,
        &path,
        &body_bytes,
        &state,
        &client,
        Some(header_slice),
    )
    .await;

    match result {
        Ok((status, response_body)) => {
            let response = format!(
                "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
                response_body.len()
            );
            let _ = stream.write_all(response.as_bytes()).await;
            let _ = stream.write_all(&response_body).await;
        }
        Err(ProxyError::NoProvidersAvailable) => {
            write_error(&mut stream, 503, "no Gemini providers available").await;
        }
        Err(ProxyError::AllProvidersExhausted { attempts }) => {
            debug!(attempts, "gemini_proxy: all providers exhausted");
            write_error(&mut stream, 503, "all Gemini providers exhausted").await;
        }
        Err(ProxyError::Upstream(e)) => {
            warn!(error = %e, "gemini_proxy: upstream error");
            write_error(&mut stream, 502, "upstream error").await;
        }
        Err(ProxyError::Listener(_)) => {
            write_error(&mut stream, 500, "internal error").await;
        }
    }
}
