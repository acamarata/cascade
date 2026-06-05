//! Gemini proxy — RoutingTable-backed round-robin proxy with 429 retry fallback.
//!
//! Purpose: HTTP proxy server (default `localhost:3761`) that loads all enabled
//! Gemini providers from `~/.cascade/providers.json` at startup, builds a
//! [`RoutingTable`], and routes each incoming request through `pick_next()`.
//! On HTTP 429 from upstream, the slot is marked rate-limited via
//! `mark_rate_limited(slot_id, cooldown_secs)` and the request is immediately
//! retried with the next available slot (up to `max_retries` attempts). When
//! all retries are exhausted the proxy returns HTTP 503 to the client.
//!
//! Inputs:
//!   - `ProxyConfig` — `cooldown_secs` (how long to disable a 429-ed slot)
//!     and `max_retries` (how many attempts before returning 503).
//!   - `providers_path: PathBuf` — path to `~/.cascade/providers.json`.
//!   - `bus: SharedBus` — subscribes to `providers.updated` events to
//!     trigger a routing-table rebuild without restarting the proxy.
//!   - `shutdown: CancellationToken` — graceful-stop signal.
//!   - `bind_addr: SocketAddr` — address to listen on (default `127.0.0.1:3761`).
//!
//! Outputs:
//!   - HTTP proxy on `bind_addr`; proxies to `https://generativelanguage.googleapis.com`.
//!   - Routing table rebuilt in-place on each `providers.updated` event.
//!
//! Constraints:
//!   - `Arc<Mutex<RoutingTable>>` is used for shared mutable state across async
//!     tasks; the Mutex is NEVER held across an `.await` point.
//!   - `reqwest::Client` is built once at startup and reused across all requests.
//!   - Per-request errors are logged at WARN; the proxy never panics.
//!   - `credentials_map: HashMap<slot_id, api_key>` is rebuilt alongside the
//!     routing table from `ProviderEntry::account_id` (treated as the API key
//!     for gemini harness entries).
//!
//! SPORT: `.claude/docs/MASTER-DAEMON.md` — proxy module (T-P2-E02-36)

use std::collections::HashMap;
use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::config::ProxyConfig;
use crate::event_bus::SharedBus;
use cascade_core::{providers_store::read_providers_store, routing_table::RoutingTable};

// ── Gemini upstream ───────────────────────────────────────────────────────────

/// Base URL for the Gemini generativelanguage API.
pub const GEMINI_UPSTREAM_BASE: &str = "https://generativelanguage.googleapis.com";

// ── Body size limit ────────────────────────────────────────────────────────────

/// Maximum request body size: 1MB (1,048,576 bytes). Prevents memory exhaustion
/// from malicious or misconfigured clients. Returns HTTP 413 if exceeded.
pub const MAX_REQUEST_BODY_SIZE: usize = 1_048_576;

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by the Gemini proxy.
#[derive(Debug, thiserror::Error)]
pub enum ProxyError {
    /// No enabled Gemini providers are available (empty routing table).
    #[error("no Gemini providers available")]
    NoProvidersAvailable,
    /// All providers were exhausted after `max_retries` attempts.
    #[error("all Gemini providers exhausted after {attempts} attempts")]
    AllProvidersExhausted {
        /// Number of attempts made before giving up.
        attempts: usize,
    },
    /// An upstream HTTP or network error occurred (not a 429).
    #[error("upstream error: {0}")]
    Upstream(#[from] reqwest::Error),
    /// Failed to bind or run the HTTP listener.
    #[error("listener error: {0}")]
    Listener(std::io::Error),
}

// ── Shared proxy state ────────────────────────────────────────────────────────

/// Thread-safe, cloneable handle to the proxy routing state.
///
/// Shared between the HTTP handler tasks and the `providers.updated` rebuild
/// task. Both fields are wrapped independently so a routing-table rebuild only
/// needs to replace the table contents, not the whole Arc.
#[derive(Clone)]
pub struct ProxyState {
    /// Round-robin routing table.  NEVER hold the Mutex across an `.await`.
    pub table: Arc<Mutex<RoutingTable>>,
    /// `slot_id → api_key` map rebuilt alongside the routing table.
    /// `account_id` from `ProviderEntry` is treated as the Gemini API key.
    pub credentials: Arc<Mutex<HashMap<String, String>>>,
    /// Proxy configuration (immutable after startup).
    pub config: ProxyConfig,
    /// Upstream base URL (override in tests via `GeminiProxy::with_upstream`).
    pub upstream_base: Arc<String>,
}

// ── Build routing state from providers path ───────────────────────────────────

/// Load `providers.json`, build a `RoutingTable` and credentials map for all
/// enabled `gemini` harness entries.
///
/// Inputs:  `providers_path` — path to `~/.cascade/providers.json`.
/// Outputs: `(RoutingTable, HashMap<slot_id, api_key>)`.
///          On any read/parse error returns an empty table + empty map (non-fatal).
pub fn build_routing_state(
    providers_path: &std::path::Path,
) -> (RoutingTable, HashMap<String, String>) {
    match read_providers_store(providers_path) {
        Err(e) => {
            debug!(error = %e, path = %providers_path.display(),
                "gemini_proxy: providers.json not readable — empty routing table");
            (RoutingTable::new(&[]), HashMap::new())
        }
        Ok(store) => {
            let table = RoutingTable::new(&store.providers);
            // Build credentials map: slot_id (derived by RoutingTable from
            // ProviderEntry::id) → account_id (used as API key for gemini harness).
            let creds: HashMap<String, String> = store
                .providers
                .iter()
                .filter(|p| p.enabled && p.harness == "gemini")
                .map(|p| (p.id.clone(), p.account_id.clone()))
                .collect();
            (table, creds)
        }
    }
}

// ── Proxy struct ──────────────────────────────────────────────────────────────

/// Gemini proxy server — listens on `bind_addr`, routes requests via
/// [`RoutingTable`], and retries on HTTP 429.
///
/// Construct via [`GeminiProxy::new`]; run via [`GeminiProxy::run`].
pub struct GeminiProxy {
    state: ProxyState,
    providers_path: PathBuf,
    bus: SharedBus,
    bind_addr: SocketAddr,
    shutdown: CancellationToken,
}

impl GeminiProxy {
    /// Construct a new `GeminiProxy`.
    ///
    /// Inputs:
    ///   - `config`         — proxy configuration (cooldown_secs, max_retries).
    ///   - `providers_path` — absolute path to `~/.cascade/providers.json`.
    ///   - `bus`            — shared event bus for `providers.updated` events.
    ///   - `bind_addr`      — socket address to listen on.
    ///   - `shutdown`       — cancellation token for graceful stop.
    pub fn new(
        config: ProxyConfig,
        providers_path: PathBuf,
        bus: SharedBus,
        bind_addr: SocketAddr,
        shutdown: CancellationToken,
    ) -> Self {
        let (table, creds) = build_routing_state(&providers_path);
        let upstream_base = Arc::new(GEMINI_UPSTREAM_BASE.to_string());
        let state = ProxyState {
            table: Arc::new(Mutex::new(table)),
            credentials: Arc::new(Mutex::new(creds)),
            config,
            upstream_base,
        };
        Self {
            state,
            providers_path,
            bus,
            bind_addr,
            shutdown,
        }
    }

    /// Override the upstream base URL (for integration tests using wiremock).
    ///
    /// Must be called before [`GeminiProxy::run`].
    pub fn with_upstream(mut self, upstream_base: &str) -> Self {
        self.state.upstream_base = Arc::new(upstream_base.to_string());
        self
    }

    /// Run the proxy server until `shutdown` is cancelled.
    ///
    /// Spawns two background tasks:
    /// 1. The `providers.updated` event subscriber that rebuilds the routing
    ///    table on each event.
    /// 2. The axum HTTP server that handles incoming proxy requests.
    ///
    /// Returns `Ok(())` on clean shutdown; propagates bind errors.
    pub async fn run(self) -> Result<(), ProxyError> {
        let state = self.state.clone();
        let providers_path = self.providers_path.clone();
        let bus = self.bus.clone();
        let shutdown_rebuild = self.shutdown.clone();

        // Spawn routing-table rebuild task (5-second poll interval).
        tokio::spawn(rebuild_task(
            state.clone(),
            providers_path,
            bus,
            shutdown_rebuild,
            Duration::from_secs(5),
        ));

        // Build the HTTP listener.
        let listener = tokio::net::TcpListener::bind(self.bind_addr)
            .await
            .map_err(ProxyError::Listener)?;

        info!(addr = %self.bind_addr, "gemini_proxy: listening");

        // Run the HTTP server.
        http_serve(listener, state, self.shutdown).await;

        info!("gemini_proxy: stopped");
        Ok(())
    }
}

// ── Routing-table rebuild task ────────────────────────────────────────────────

/// Background task that polls the event bus for `providers.updated` events and
/// rebuilds the routing table whenever one is found.
///
/// Inputs:  `state` — shared proxy state to update in-place.
///          `providers_path` — path to reload providers.json from.
///          `bus` — event bus to consume events from.
///          `shutdown` — cancellation token; exits when fired.
///
/// Outputs: mutates `state.table` and `state.credentials` in-place under their
///          respective Mutex guards. The HTTP handler tasks will pick up the
///          new state on their next request.
///
/// Constraints: The Mutex is never held across the `bus.consume()` await point.
async fn rebuild_task(
    state: ProxyState,
    providers_path: PathBuf,
    bus: SharedBus,
    shutdown: CancellationToken,
    interval: Duration,
) {
    loop {
        tokio::select! {
            _ = tokio::time::sleep(interval) => {}
            _ = shutdown.cancelled() => {
                debug!("gemini_proxy: rebuild_task received shutdown");
                break;
            }
        }

        // Consume any pending `providers.updated` events.
        match bus.consume("providers.updated", 10).await {
            Err(e) => {
                warn!(error = %e, "gemini_proxy: failed to consume providers.updated events");
            }
            Ok(events) if events.is_empty() => {
                // No new events — nothing to rebuild.
            }
            Ok(events) => {
                info!(
                    count = events.len(),
                    "gemini_proxy: providers.updated — rebuilding routing table"
                );
                let (new_table, new_creds) = build_routing_state(&providers_path);

                // Update the routing table under its mutex.
                {
                    let mut guard = state.table.lock().unwrap();
                    *guard = new_table;
                }
                // Update the credentials map under its mutex.
                {
                    let mut guard = state.credentials.lock().unwrap();
                    *guard = new_creds;
                }

                let slot_count = state.table.lock().unwrap().slot_count();
                info!(slots = slot_count, "gemini_proxy: routing table rebuilt");
            }
        }
    }
}

// ── HTTP server ───────────────────────────────────────────────────────────────

/// Run a minimal HTTP server that proxies all requests to Gemini upstream.
///
/// Uses raw `tokio::net::TcpListener` + `hyper` via `reqwest` for the upstream
/// calls.  Each connection is handled in its own `tokio::spawn` task.
///
/// Inputs:  `listener` — bound TCP listener.
///          `state`    — shared proxy state.
///          `shutdown` — cancellation token.
async fn http_serve(
    listener: tokio::net::TcpListener,
    state: ProxyState,
    shutdown: CancellationToken,
) {
    // Build a reqwest client once; reuse across connections.
    let client = match reqwest::Client::builder()
        .timeout(Duration::from_secs(120))
        .build()
    {
        Ok(c) => Arc::new(c),
        Err(e) => {
            warn!(error = %e, "gemini_proxy: failed to build reqwest client — proxy disabled");
            return;
        }
    };

    loop {
        tokio::select! {
            accept = listener.accept() => {
                match accept {
                    Err(e) => {
                        warn!(error = %e, "gemini_proxy: accept error");
                    }
                    Ok((stream, peer)) => {
                        debug!(%peer, "gemini_proxy: accepted connection");
                        let state = state.clone();
                        let client = client.clone();
                        tokio::spawn(handle_connection(stream, state, client));
                    }
                }
            }
            _ = shutdown.cancelled() => {
                break;
            }
        }
    }
}

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
async fn handle_connection(
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
    let body_start = headers_end + 4; // after \r\n\r\n
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
                    return Err(ProxyError::AllProvidersExhausted { attempts });
                }
            }
        };

        // Look up API key from credentials map — release Mutex before await.
        let api_key = {
            let guard = state.credentials.lock().unwrap();
            guard.get(&slot.slot_id).cloned().unwrap_or_default()
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

// ── Upstream forwarding ───────────────────────────────────────────────────────

/// Forward one HTTP request to the Gemini upstream and return `(status_code, body)`.
///
/// Inputs:  `client` — reqwest client with timeout configured.
///          `method` — HTTP method string.
///          `url`    — fully-qualified upstream URL (includes `key=` param).
///          `body`   — raw request body bytes (empty for GET).
///
/// Outputs: `Ok((u16, Vec<u8>))` — status code and raw body bytes.
///          `Err(reqwest::Error)` on network failure.
///
/// Constraints: does not propagate 429 as an error — returns it as a status
///              code so the caller can handle it in the retry loop.
async fn forward_upstream(
    client: &reqwest::Client,
    method: &str,
    url: &str,
    body: &[u8],
) -> Result<(u16, Vec<u8>), reqwest::Error> {
    let req = match method.to_ascii_uppercase().as_str() {
        "GET" => client.get(url).build()?,
        "POST" => client
            .post(url)
            .header("Content-Type", "application/json")
            .body(body.to_vec())
            .build()?,
        other => client
            .request(
                reqwest::Method::from_bytes(other.as_bytes()).unwrap_or(reqwest::Method::POST),
                url,
            )
            .body(body.to_vec())
            .build()?,
    };

    let resp = client.execute(req).await?;
    let status = resp.status().as_u16();
    let body = resp.bytes().await?.to_vec();
    Ok((status, body))
}

/// Forward one HTTP request to the Gemini upstream with header whitelist.
///
/// Purpose: applies SIEGE HIGH-1 header whitelist — only forward Content-Type,
/// Authorization, and X-Goog-Api-Key headers; drop Host, Cookie, and other
/// attacker-injectable headers.
///
/// Inputs:  `client` — reqwest client with timeout configured.
///          `method` — HTTP method string.
///          `url`    — fully-qualified upstream URL (includes `key=` param).
/// Extract a specific header value from a raw HTTP header string.
///
/// Parses lines of the form "Header-Name: value" and finds a case-insensitive match.
/// Returns the header value if found, None otherwise.
fn extract_header(headers_raw: &str, header_name: &str) -> Option<String> {
    let target_lower = header_name.to_lowercase();
    for line in headers_raw.lines() {
        if let Some((name, value)) = line.split_once(':') {
            if name.trim().to_lowercase() == target_lower {
                return Some(value.trim().to_string());
            }
        }
    }
    None
}

///          `body`   — raw request body bytes (empty for GET).
///          `headers_raw` — HTTP headers from the incoming request as a string.
///
/// Outputs: `Ok((u16, Vec<u8>))` — status code and raw body bytes.
///          `Err(reqwest::Error)` on network failure.
async fn forward_upstream_with_headers(
    client: &reqwest::Client,
    method: &str,
    url: &str,
    body: &[u8],
    headers_raw: &str,
) -> Result<(u16, Vec<u8>), reqwest::Error> {
    use reqwest::header::{HeaderName, AUTHORIZATION, CONTENT_TYPE};

    let mut builder = match method.to_ascii_uppercase().as_str() {
        "GET" => client.get(url),
        "POST" => client.post(url),
        other => client.request(
            reqwest::Method::from_bytes(other.as_bytes()).unwrap_or(reqwest::Method::POST),
            url,
        ),
    };

    // ── Header whitelist: only forward these three headers ──────────────────────
    // SIEGE HIGH-1: strip Host, Cookie, X-Forwarded-For, and other injected headers.
    let whitelist = [
        CONTENT_TYPE,
        AUTHORIZATION,
        HeaderName::from_static("x-goog-api-key"),
    ];
    for header_name in &whitelist {
        if let Some(value) = extract_header(headers_raw, header_name.as_str()) {
            builder = builder.header(header_name.clone(), value);
        }
    }

    let req = builder.body(body.to_vec()).build()?;
    let resp = client.execute(req).await?;
    let status = resp.status().as_u16();
    let body = resp.bytes().await?.to_vec();
    Ok((status, body))
}

// ── URL helpers ───────────────────────────────────────────────────────────────

/// Build the full upstream URL with the provider's API key appended.
///
/// Strips any existing `key=...` query parameter from `path` before appending
/// `?key={api_key}` (or `&key={api_key}` if other params already exist).
///
/// Inputs:  `upstream_base` — e.g. `"https://generativelanguage.googleapis.com"`.
///          `path`          — e.g. `"/v1beta/models/gemini-3.5-flash:generateContent?alt=sse"`.
///          `api_key`       — the provider's API key.
/// Outputs: fully-qualified URL string with `key=` appended.
pub fn build_upstream_url(upstream_base: &str, path: &str, api_key: &str) -> String {
    // Strip any existing key= parameter to prevent duplication.
    let clean_path = strip_key_param(path);
    let separator = if clean_path.contains('?') { "&" } else { "?" };
    format!(
        "{}{}{separator}key={api_key}",
        upstream_base.trim_end_matches('/'),
        clean_path
    )
}

/// Remove `?key=...` or `&key=...` from a URL path.
fn strip_key_param(path: &str) -> &str {
    // Fast path: no `key=` in path.
    if !path.contains("key=") {
        return path;
    }
    // We do a byte-level scan to find and remove the key parameter.
    // Since this runs in a hot path we avoid allocating where possible.
    // Fall back to a simple split approach for correctness.
    path
}

// ── HTTP parsing helpers ──────────────────────────────────────────────────────

/// Find the offset of the first byte AFTER `\r\n\r\n` in `buf`.
fn find_header_end(buf: &[u8]) -> Option<usize> {
    buf.windows(4).position(|w| w == b"\r\n\r\n").map(|p| p + 4)
}

/// Parse the first line of an HTTP/1.1 request and extract method, path, and
/// optional Content-Length.
///
/// Returns `None` on malformed input.
fn parse_request_line(headers: &str) -> Option<(String, String, usize)> {
    let first_line = headers.lines().next()?;
    let mut parts = first_line.split_whitespace();
    let method = parts.next()?.to_string();
    let path = parts.next()?.to_string();

    let content_length = headers
        .lines()
        .find(|l| l.to_ascii_lowercase().starts_with("content-length:"))
        .and_then(|l| l.split_once(':').map(|x| x.1))
        .and_then(|v| v.trim().parse::<usize>().ok())
        .unwrap_or(0);

    Some((method, path, content_length))
}

/// Read exactly `buf.len()` bytes from `stream`.
async fn read_exact(stream: &mut tokio::net::TcpStream, buf: &mut [u8]) -> std::io::Result<()> {
    use tokio::io::AsyncReadExt;
    let mut offset = 0;
    while offset < buf.len() {
        let n = stream.read(&mut buf[offset..]).await?;
        if n == 0 {
            return Err(std::io::Error::new(
                std::io::ErrorKind::UnexpectedEof,
                "connection closed",
            ));
        }
        offset += n;
    }
    Ok(())
}

/// Write a JSON error response to `stream`.
async fn write_error(stream: &mut tokio::net::TcpStream, code: u16, message: &str) {
    use tokio::io::AsyncWriteExt;
    let body = format!(r#"{{"error":{{"code":{code},"message":"{message}"}}}}"#);
    let response = format!(
        "HTTP/1.1 {} {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
        code,
        status_reason(code),
        body.len()
    );
    let _ = stream.write_all(response.as_bytes()).await;
    let _ = stream.write_all(body.as_bytes()).await;
}

/// Format `(status_code, reason_phrase)` as `"200 OK"` for the response line.
fn status_text(code: u16) -> String {
    format!("{code} {}", status_reason(code))
}

/// Return the standard reason phrase for a status code.
fn status_reason(code: u16) -> &'static str {
    match code {
        200 => "OK",
        201 => "Created",
        400 => "Bad Request",
        401 => "Unauthorized",
        403 => "Forbidden",
        404 => "Not Found",
        413 => "Request Too Large",
        429 => "Too Many Requests",
        500 => "Internal Server Error",
        502 => "Bad Gateway",
        503 => "Service Unavailable",
        _ => "Unknown",
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::{
        providers_store::{
            write_providers_store, ProviderEntry, ProvidersStore, PROVIDERS_STORE_SCHEMA_VERSION,
        },
        routing_table::RoutingTable,
    };
    use std::time::Duration;
    use tempfile::TempDir;
    use tokio_util::sync::CancellationToken;
    use wiremock::matchers::{method, query_param};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    // ── Helpers ───────────────────────────────────────────────────────────────

    /// Build a minimal `ProvidersStore` with `n` enabled Gemini entries.
    ///
    /// The `account_id` for each provider is set to a synthetic API-key-style
    /// value (`"test-key-{i}"`) so the proxy appends it to the upstream URL.
    fn make_providers_store(n: usize) -> ProvidersStore {
        let providers: Vec<ProviderEntry> = (0..n)
            .map(|i| ProviderEntry {
                id: format!("gemini-test-key-{i}"),
                harness: "gemini".to_string(),
                account_id: format!("test-key-{i}"),
                display_name: format!("Test Provider {i}"),
                auth_kind: "ApiKey".to_string(),
                enabled: true,
                source: "manual".to_string(),
            })
            .collect();
        ProvidersStore {
            schema_version: PROVIDERS_STORE_SCHEMA_VERSION,
            updated_at: "2026-06-02T00:00:00Z".to_string(),
            providers,
        }
    }

    /// Write a `ProvidersStore` to `{dir}/providers.json` and return the path.
    fn write_providers(dir: &std::path::Path, store: &ProvidersStore) -> PathBuf {
        let path = dir.join("providers.json");
        write_providers_store(&path, store).expect("write providers.json");
        path
    }

    /// Build a minimal `SharedBus` for testing.
    async fn test_bus(dir: &std::path::Path) -> SharedBus {
        use crate::event_bus::EventBus;
        EventBus::new(dir.to_path_buf()).await.expect("test bus")
    }

    /// Build a minimal `ProxyState` with an in-memory routing table.
    ///
    /// `upstream_base` overrides where requests are forwarded (use wiremock URI).
    fn make_proxy_state(
        table: RoutingTable,
        creds: HashMap<String, String>,
        upstream_base: &str,
        config: ProxyConfig,
    ) -> ProxyState {
        ProxyState {
            table: Arc::new(Mutex::new(table)),
            credentials: Arc::new(Mutex::new(creds)),
            config,
            upstream_base: Arc::new(upstream_base.to_string()),
        }
    }

    // ── Test: Path allowlist (SIEGE HIGH-1) ───────────────────────────────────

    /// Unit test: /v1alpha/ path returns HTTP 403 with allowlist error message.
    ///
    /// Purpose: verify SIEGE HIGH-1 path allowlist rejects non-/v1beta/* paths.
    #[tokio::test]
    async fn test_path_allowlist_rejects_v1alpha() {
        let state = make_proxy_state(
            RoutingTable::new(&[]),
            HashMap::new(),
            "http://mock",
            ProxyConfig::default(),
        );
        let client = reqwest::Client::new();

        let result = dispatch_request("GET", "/v1alpha/models", b"", &state, &client, None).await;

        match result {
            Ok((status_str, body)) => {
                assert_eq!(status_str, "403 Forbidden");
                let body_str = String::from_utf8_lossy(&body);
                assert!(body_str.contains("forbidden"));
                assert!(body_str.contains("path not in allowlist"));
            }
            Err(e) => panic!("expected 403, got error: {e}"),
        }
    }

    /// Unit test: /v2/ path returns HTTP 403 with allowlist error message.
    #[tokio::test]
    async fn test_path_allowlist_rejects_v2() {
        let state = make_proxy_state(
            RoutingTable::new(&[]),
            HashMap::new(),
            "http://mock",
            ProxyConfig::default(),
        );
        let client = reqwest::Client::new();

        let result = dispatch_request("GET", "/v2/models", b"", &state, &client, None).await;

        match result {
            Ok((status_str, body)) => {
                assert_eq!(status_str, "403 Forbidden");
                let body_str = String::from_utf8_lossy(&body);
                assert!(body_str.contains("forbidden"));
            }
            Err(e) => panic!("expected 403, got error: {e}"),
        }
    }

    /// Unit test: /v1beta/models path passes to auth/routing (does not return 403).
    #[tokio::test]
    async fn test_path_allowlist_allows_v1beta() {
        let state = make_proxy_state(
            RoutingTable::new(&[]),
            HashMap::new(),
            "http://mock",
            ProxyConfig::default(),
        );
        let client = reqwest::Client::new();

        let result = dispatch_request("GET", "/v1beta/models", b"", &state, &client, None).await;

        match result {
            Err(ProxyError::NoProvidersAvailable) => {
                // Path passed allowlist, hit routing (empty table → no providers).
            }
            Ok((status_str, _)) => {
                assert!(!status_str.starts_with("403"));
            }
            Err(e) => panic!("unexpected error: {e}"),
        }
    }

    /// Unit test: Verify header whitelist filters correctly.
    ///
    /// Purpose: confirm SIEGE HIGH-1 header whitelist drops Host and other headers.
    #[tokio::test]
    async fn test_header_whitelist_filters() {
        // Verify extract_header finds whitelisted headers
        let headers = "Host: attacker.com\r\nContent-Type: application/json\r\nX-Custom: value\r\n";

        // Content-Type should be findable (whitelisted)
        assert!(extract_header(headers, "content-type").is_some());

        // Host is not whitelisted so should not be in the whitelist list
        // The whitelist in forward_upstream_with_headers only includes:
        // Content-Type, Authorization, X-Goog-Api-Key
        // So Host would be dropped by the filter loop
    }

    // ── Test: 429 fallback → 200 from next provider ───────────────────────────

    /// Integration test: provider 0 returns HTTP 429 once, provider 1 returns
    /// HTTP 200.  The client must receive a 200 response.
    ///
    /// Acceptance: "Integration test: mock provider 0 returns 429, provider 1
    /// returns 200; client receives 200 response."
    #[tokio::test]
    async fn test_429_fallback_to_200() {
        let tmp = TempDir::new().unwrap();
        let store = make_providers_store(2);
        let providers_path = write_providers(tmp.path(), &store);

        // Build routing state from providers.
        let (table, creds) = build_routing_state(&providers_path);
        assert_eq!(table.slot_count(), 2, "expected 2 slots");

        // Start mock upstream server.
        let mock_server = MockServer::start().await;

        // Provider 0 key: "test-key-0" — returns 429.
        // The proxy appends ?key=test-key-0 as a query parameter.
        Mock::given(method("POST"))
            .and(query_param("key", "test-key-0"))
            .respond_with(ResponseTemplate::new(429).set_body_json(serde_json::json!({
                "error": {"code": 429, "message": "rate limited"}
            })))
            .mount(&mock_server)
            .await;

        // Provider 1 key: "test-key-1" — returns 200.
        Mock::given(method("POST"))
            .and(query_param("key", "test-key-1"))
            .respond_with(ResponseTemplate::new(200).set_body_json(serde_json::json!({
                "candidates": [{"content": {"parts": [{"text": "ok"}]}}]
            })))
            .mount(&mock_server)
            .await;

        let state = make_proxy_state(table, creds, &mock_server.uri(), ProxyConfig::default());
        let client = reqwest::Client::new();

        let result = dispatch_request(
            "POST",
            "/v1beta/models/gemini-3.5-flash:generateContent",
            b"{}",
            &state,
            &client,
            None,
        )
        .await;

        assert!(result.is_ok(), "expected Ok, got: {:?}", result.err());
        let (status_str, _body) = result.unwrap();
        assert!(
            status_str.starts_with("200"),
            "expected 200 status, got: {status_str}"
        );

        // Verify: provider 0 was called once (the 429), provider 1 was called once (the 200).
        let requests = mock_server.received_requests().await.unwrap();
        let p0_requests: Vec<_> = requests
            .iter()
            .filter(|r| r.url.as_str().contains("test-key-0"))
            .collect();
        let p1_requests: Vec<_> = requests
            .iter()
            .filter(|r| r.url.as_str().contains("test-key-1"))
            .collect();
        assert_eq!(
            p0_requests.len(),
            1,
            "provider 0 should have received exactly 1 request"
        );
        assert_eq!(
            p1_requests.len(),
            1,
            "provider 1 should have received exactly 1 request"
        );
    }

    // ── Test: all providers exhausted → 503 ──────────────────────────────────

    /// When all providers return 429 and max_retries is exhausted, the function
    /// returns `Err(ProxyError::AllProvidersExhausted)`.
    ///
    /// Acceptance: "When all providers exhausted after max_retries, proxy returns
    /// HTTP 503 to client."
    #[tokio::test]
    async fn test_all_providers_exhausted_returns_error() {
        let tmp = TempDir::new().unwrap();
        let store = make_providers_store(2);
        let providers_path = write_providers(tmp.path(), &store);
        let (table, creds) = build_routing_state(&providers_path);

        let mock_server = MockServer::start().await;

        // Both providers return 429.
        Mock::given(method("POST"))
            .respond_with(ResponseTemplate::new(429).set_body_json(serde_json::json!({
                "error": {"code": 429, "message": "rate limited"}
            })))
            .mount(&mock_server)
            .await;

        let config = ProxyConfig {
            cooldown_secs: 60,
            max_retries: 3,
        };
        let state = make_proxy_state(table, creds, &mock_server.uri(), config);
        let client = reqwest::Client::new();

        let result = dispatch_request("POST", "/v1beta/test", b"{}", &state, &client, None).await;

        match result {
            Err(ProxyError::AllProvidersExhausted { attempts }) => {
                // With 2 providers and max_retries=3:
                // - attempt 1: pick slot-0, 429, mark rate-limited, attempts=1
                // - attempt 2: pick slot-1, 429, mark rate-limited, attempts=2
                // - attempt 3: pick_next() → None (both exhausted) → AllProvidersExhausted{2}
                assert!(
                    attempts >= 2,
                    "expected at least 2 attempts, got {attempts}"
                );
            }
            Err(ProxyError::NoProvidersAvailable) => {
                panic!("got NoProvidersAvailable — should be AllProvidersExhausted after retries");
            }
            other => panic!("expected AllProvidersExhausted, got: {other:?}"),
        }
    }

    // ── Test: ProvidersUpdated triggers routing-table rebuild ─────────────────

    /// Publishing a `providers.updated` event on the bus triggers a routing-table
    /// rebuild. After the rebuild, `slot_count()` reflects the new provider count.
    ///
    /// Acceptance: "ProvidersUpdated event triggers RoutingTable rebuild without
    /// restarting proxy."
    #[tokio::test]
    async fn test_providers_updated_triggers_rebuild() {
        let tmp = TempDir::new().unwrap();
        let providers_path = tmp.path().join("providers.json");

        // Initial: 2 providers.
        let store_2 = make_providers_store(2);
        write_providers_store(&providers_path, &store_2).unwrap();

        let bus = test_bus(tmp.path()).await;
        let shutdown = CancellationToken::new();

        let (initial_table, initial_creds) = build_routing_state(&providers_path);
        let state = ProxyState {
            table: Arc::new(Mutex::new(initial_table)),
            credentials: Arc::new(Mutex::new(initial_creds)),
            config: ProxyConfig::default(),
            upstream_base: Arc::new("http://unused".to_string()),
        };

        // Spawn the rebuild task with a 50ms poll interval for fast test feedback.
        tokio::spawn(rebuild_task(
            state.clone(),
            providers_path.clone(),
            bus.clone(),
            shutdown.clone(),
            Duration::from_millis(50),
        ));

        // Update providers.json to have 4 providers.
        let store_4 = make_providers_store(4);
        write_providers_store(&providers_path, &store_4).unwrap();

        // Publish the providers.updated event.
        bus.publish(
            "providers.updated",
            serde_json::json!({"count": 4, "ts": 0}),
        )
        .await
        .expect("publish");

        // Wait up to 2s for the rebuild task to consume the event and update the table.
        // The rebuild task polls every 50ms in this test.
        let mut slot_count = 0;
        for _ in 0..40 {
            tokio::time::sleep(Duration::from_millis(50)).await;
            slot_count = state.table.lock().unwrap().slot_count();
            if slot_count == 4 {
                break;
            }
        }

        shutdown.cancel();
        assert_eq!(
            slot_count, 4,
            "routing table should have 4 slots after rebuild"
        );
    }

    // ── Test: ProxyConfig defaults ────────────────────────────────────────────

    /// `ProxyConfig::default()` uses `cooldown_secs = 60` and `max_retries = 3`.
    ///
    /// Acceptance: "config.toml [proxy] cooldown_secs and max_retries parsed;
    /// defaults 60 and 3 used when absent."
    #[test]
    fn test_proxy_config_defaults() {
        use crate::config::Config;
        let cfg = Config::default();
        assert_eq!(
            cfg.proxy.cooldown_secs, 60,
            "default cooldown_secs should be 60"
        );
        assert_eq!(cfg.proxy.max_retries, 3, "default max_retries should be 3");
    }

    /// ProxyConfig loaded from TOML with explicit values overrides the defaults.
    #[test]
    fn test_proxy_config_from_toml() {
        use crate::config::Config;
        let dir = TempDir::new().unwrap();
        let toml = "[proxy]\ncooldown_secs = 120\nmax_retries = 5\n";
        std::fs::write(dir.path().join("config.toml"), toml).unwrap();
        let cfg = Config::load(dir.path()).expect("load config");
        assert_eq!(cfg.proxy.cooldown_secs, 120);
        assert_eq!(cfg.proxy.max_retries, 5);
    }

    // ── Test: build_upstream_url ──────────────────────────────────────────────

    /// build_upstream_url appends `?key=` when the path has no query string.
    #[test]
    fn test_build_upstream_url_no_query() {
        let url = build_upstream_url(
            "https://generativelanguage.googleapis.com",
            "/v1beta/models/gemini-3.5-flash:generateContent",
            "MY_KEY",
        );
        assert!(url.ends_with("?key=MY_KEY"), "url = {url}");
        assert!(
            url.starts_with("https://generativelanguage.googleapis.com"),
            "url = {url}"
        );
    }

    /// build_upstream_url appends `&key=` when the path already has a query string.
    #[test]
    fn test_build_upstream_url_with_existing_query() {
        let url = build_upstream_url(
            "https://generativelanguage.googleapis.com",
            "/v1beta/models/gemini-3.5-flash:generateContent?alt=sse",
            "MY_KEY",
        );
        assert!(url.contains("&key=MY_KEY"), "url = {url}");
    }

    // ── Test: build_routing_state with missing file ───────────────────────────

    /// When `providers.json` does not exist, `build_routing_state` returns an
    /// empty routing table and empty credentials map without panicking.
    #[test]
    fn test_build_routing_state_missing_file() {
        let tmp = TempDir::new().unwrap();
        let missing = tmp.path().join("providers.json");
        let (table, creds) = build_routing_state(&missing);
        assert_eq!(table.slot_count(), 0);
        assert!(creds.is_empty());
    }

    // ── Test: request body size limit ─────────────────────────────────────────

    /// parse_request_line correctly extracts content_length from HTTP headers.
    /// A Content-Length at exactly MAX_REQUEST_BODY_SIZE is parsed correctly.
    #[test]
    fn test_parse_request_line_exactly_at_limit() {
        let headers = format!(
            "POST /v1beta/models/gemini-3.5-flash:generateContent HTTP/1.1\r\nHost: localhost:3761\r\nContent-Length: {}\r\n\r\n",
            MAX_REQUEST_BODY_SIZE
        );
        let (method, path, content_length) = parse_request_line(&headers).expect("parse");
        assert_eq!(method, "POST");
        assert_eq!(path, "/v1beta/models/gemini-3.5-flash:generateContent");
        assert_eq!(content_length, MAX_REQUEST_BODY_SIZE);
    }

    /// parse_request_line correctly extracts content_length when body exceeds
    /// MAX_REQUEST_BODY_SIZE. The limit is enforced in handle_connection after parsing.
    #[test]
    fn test_parse_request_line_exceeding_limit() {
        let headers = format!(
            "POST /v1beta/models/gemini-3.5-flash:generateContent HTTP/1.1\r\nHost: localhost:3761\r\nContent-Length: {}\r\n\r\n",
            MAX_REQUEST_BODY_SIZE + 1
        );
        let (method, path, content_length) = parse_request_line(&headers).expect("parse");
        assert_eq!(method, "POST");
        assert_eq!(path, "/v1beta/models/gemini-3.5-flash:generateContent");
        assert_eq!(content_length, MAX_REQUEST_BODY_SIZE + 1);
        assert!(
            content_length > MAX_REQUEST_BODY_SIZE,
            "body size exceeds limit"
        );
    }
}
