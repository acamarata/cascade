//! GeminiProxy server struct, run loop, and routing-table rebuild task.
//!
//! Purpose: constructs the proxy, spawns background tasks (rebuild + HTTP server).
//! SPORT: proxy module (T-P2-E02-36)

use std::collections::HashMap;
use std::net::SocketAddr;
use std::path::PathBuf;
use std::sync::{Arc, Mutex, RwLock};
use std::time::Duration;

use secrecy::SecretString;
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

use crate::config::ProxyConfig;
use crate::event_bus::SharedBus;

use super::dispatch::handle_connection;
use super::state::build_routing_state;
use super::types::{GEMINI_UPSTREAM_BASE, ProxyError, ProxyState};

// ── Proxy struct ──────────────────────────────────────────────────────────────

/// Gemini proxy server — listens on `bind_addr`, routes requests via
/// `RoutingTable`, and retries on HTTP 429.
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
        keychain_keys: HashMap<String, SecretString>,
    ) -> Self {
        let (table, creds) = build_routing_state(&providers_path);
        let upstream_base = Arc::new(GEMINI_UPSTREAM_BASE.to_string());
        let state = ProxyState {
            table: Arc::new(Mutex::new(table)),
            credentials: Arc::new(Mutex::new(creds)),
            keychain_keys: Arc::new(RwLock::new(keychain_keys)),
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
pub(crate) async fn rebuild_task(
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
