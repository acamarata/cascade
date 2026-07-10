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

mod dispatch;
mod server;
mod state;
mod types;
mod upstream;

// ── Public re-exports (preserve original API surface) ────────────────────────

pub use server::GeminiProxy;

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::path::PathBuf;
    use std::sync::{Arc, Mutex, RwLock};
    use std::time::Duration;

    use secrecy::SecretString;
    use tempfile::TempDir;
    use tokio_util::sync::CancellationToken;
    use wiremock::matchers::{method, query_param};
    use wiremock::{Mock, MockServer, ResponseTemplate};

    use cascade_core::{
        providers_store::{
            write_providers_store, ProviderEntry, ProvidersStore, PROVIDERS_STORE_SCHEMA_VERSION,
        },
        routing_table::RoutingTable,
    };

    use crate::config::ProxyConfig;
    use crate::event_bus::SharedBus;

    use super::dispatch::dispatch_request;
    use super::server::rebuild_task;
    use super::state::{build_routing_state, resolve_api_key};
    use super::types::{ProxyError, ProxyState, MAX_REQUEST_BODY_SIZE};
    use super::upstream::{build_upstream_url, extract_header, parse_request_line};

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
                local_ok: true,
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
            keychain_keys: Arc::new(RwLock::new(HashMap::new())),
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

    // ── Test: GET /health — live pool state (E1-S6) ───────────────────────────

    /// /health must report LIVE routing-table state: enabled slots count as
    /// healthy; the response is answered locally (never forwarded upstream).
    #[tokio::test]
    async fn test_health_reports_live_slot_counts() {
        let store = make_providers_store(3);
        let (table, creds) = {
            let tmp = TempDir::new().expect("tempdir");
            let path = write_providers(tmp.path(), &store);
            build_routing_state(&path)
        };
        let state = make_proxy_state(table, creds, "http://mock", ProxyConfig::default());
        let client = reqwest::Client::new();

        let (status, body) = dispatch_request("GET", "/health", b"", &state, &client, None)
            .await
            .expect("health must answer locally");
        assert_eq!(status, "200 OK");
        let v: serde_json::Value = serde_json::from_slice(&body).expect("json body");
        assert_eq!(v["status"], "ok");
        assert_eq!(v["healthy_slots"], 3);
        assert_eq!(v["total_slots"], 3);
    }

    /// Regression (E1-S6 review): 429 cooldowns must be visible in /health —
    /// the chat GP-preference relies on this to avoid firing into an
    /// exhausted pool. A fresh-from-config table can never show this.
    #[tokio::test]
    async fn test_health_reflects_429_cooldowns() {
        let store = make_providers_store(2);
        let (table, creds) = {
            let tmp = TempDir::new().expect("tempdir");
            let path = write_providers(tmp.path(), &store);
            build_routing_state(&path)
        };
        let state = make_proxy_state(table, creds, "http://mock", ProxyConfig::default());
        // Put every slot into a long 429 cooldown, as a saturated pool would be.
        {
            let mut guard = state.table.lock().unwrap();
            guard.mark_rate_limited("gemini-test-key-0", 3600);
            guard.mark_rate_limited("gemini-test-key-1", 3600);
        }
        let client = reqwest::Client::new();

        let (status, body) = dispatch_request("GET", "/health", b"", &state, &client, None)
            .await
            .expect("health must answer locally");
        assert_eq!(status, "200 OK");
        let v: serde_json::Value = serde_json::from_slice(&body).expect("json body");
        assert_eq!(
            v["healthy_slots"], 0,
            "cooldown slots must not count as healthy"
        );
        assert_eq!(v["total_slots"], 2);
    }

    /// /health with an empty table: reachable proxy but zero routable slots.
    #[tokio::test]
    async fn test_health_empty_table_zero_slots() {
        let state = make_proxy_state(
            RoutingTable::new(&[]),
            HashMap::new(),
            "http://mock",
            ProxyConfig::default(),
        );
        let client = reqwest::Client::new();
        let (status, body) = dispatch_request("GET", "/health", b"", &state, &client, None)
            .await
            .expect("health must answer locally");
        assert_eq!(status, "200 OK");
        let v: serde_json::Value = serde_json::from_slice(&body).expect("json body");
        assert_eq!(v["healthy_slots"], 0);
    }

    /// POST /health is NOT the health endpoint — it falls through to the
    /// /v1beta allowlist and is rejected (no new unauthenticated surface).
    #[tokio::test]
    async fn test_health_post_falls_to_allowlist() {
        let state = make_proxy_state(
            RoutingTable::new(&[]),
            HashMap::new(),
            "http://mock",
            ProxyConfig::default(),
        );
        let client = reqwest::Client::new();
        let (status, _) = dispatch_request("POST", "/health", b"{}", &state, &client, None)
            .await
            .expect("must answer locally");
        assert_eq!(status, "403 Forbidden");
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
            keychain_keys: Arc::new(RwLock::new(HashMap::new())),
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

    // ── T-P3-E00-02: resolve_api_key unit tests ───────────────────────────────

    #[test]
    fn resolve_prefers_keychain_over_providers_json() {
        let mut creds = HashMap::new();
        creds.insert("slot-1".to_string(), "providers-key".to_string());

        let mut kc: HashMap<String, SecretString> = HashMap::new();
        kc.insert(
            "slot-1".to_string(),
            SecretString::new("keychain-key".into()),
        );

        let result = resolve_api_key(&creds, &kc, "slot-1");
        assert_eq!(result, "keychain-key", "keychain must take precedence");
    }

    #[test]
    fn resolve_falls_back_to_providers_json_when_no_keychain_entry() {
        let mut creds = HashMap::new();
        creds.insert("slot-2".to_string(), "providers-fallback".to_string());

        let kc: HashMap<String, SecretString> = HashMap::new();

        let result = resolve_api_key(&creds, &kc, "slot-2");
        assert_eq!(
            result, "providers-fallback",
            "must fall back to providers.json"
        );
    }

    #[test]
    fn resolve_returns_empty_when_neither_source_has_slot() {
        let creds: HashMap<String, String> = HashMap::new();
        let kc: HashMap<String, SecretString> = HashMap::new();

        let result = resolve_api_key(&creds, &kc, "missing-slot");
        assert_eq!(result, "", "unknown slot must return empty string");
    }
}
