//! Dashboard HTTP server for cascaded.
//!
//! Purpose: Serves the local management dashboard on `127.0.0.1:9761` with
//! bearer-token auth on GCI write API routes and a restrictive CORS policy.
//! Fixes SIEGE HIGH-3: (1) dashboard was bound 0.0.0.0 (LAN-reachable);
//! (2) GCI write API had no authentication.
//!
//! Inputs:
//!   - `config_dir: PathBuf` — `~/.cascade`; used to locate `dashboard.token`.
//!   - `bind_addr: SocketAddr` — must be `127.0.0.1:9761`; validated at startup.
//!   - `shutdown: CancellationToken` — graceful-stop signal.
//!
//! Outputs:
//!   - HTTP server on `bind_addr` serving:
//!     - `GET /health` — unauthenticated, returns `{"status":"ok"}`.
//!     - `GET /api/*`  — unauthenticated read endpoints (future P3 scope).
//!     - `PUT /api/gci/*` etc. — authenticated write routes; 401 on missing/
//!       wrong Authorization header.
//!   - `~/.cascade/dashboard.token` — 64-char hex token, 0600 permissions.
//!
//! Constraints:
//!   - Bind address MUST be `127.0.0.1`; a bind attempt to 0.0.0.0 panics in
//!     debug and returns Err in release.
//!   - Token is NOT logged at any log level.
//!   - `subtle::ConstantTimeEq` used for token comparison to resist timing attacks.
//!   - CORS allows only `http://127.0.0.1:9761`; wildcard origins are rejected.
//!   - Token rotation (SIGHUP) is P3 scope; token is written once at daemon start.
//!
//! SPORT: `.claude/docs/MASTER-SECURITY.md` — dashboard-auth row, dashboard-bind row (T-P2-E07-04)

use std::net::SocketAddr;
use std::path::Path;
use std::sync::Arc;

use axum::body::Body;
use axum::extract::State;
use axum::http::{header::AUTHORIZATION, HeaderMap, HeaderValue, Method, Request, StatusCode};
use axum::middleware::{self, Next};
use axum::response::{IntoResponse, Response};
use axum::routing::{delete, get, post, put};
use axum::{Json, Router};
use rand::RngCore;
use subtle::ConstantTimeEq;
use tokio_util::sync::CancellationToken;
use tower_http::cors::CorsLayer;
use tracing::{debug, info};

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by the Dashboard server.
#[derive(Debug, thiserror::Error)]
pub enum DashboardError {
    /// Failed to bind or run the HTTP listener.
    #[error("dashboard listener error: {0}")]
    Listener(#[from] std::io::Error),

    /// Attempted to bind to a non-loopback address.
    #[error("dashboard bind rejected: '{addr}' is not 127.0.0.1 — refusing to bind to LAN")]
    NonLoopback { addr: SocketAddr },
}

// ── Shared dashboard state ────────────────────────────────────────────────────

/// Thread-safe state shared across all Axum handler tasks.
///
/// Cloned cheaply via `Arc` on each request.
#[derive(Clone)]
pub struct DashboardState {
    /// 64-char hex bearer token for GCI write API auth.
    /// NEVER log this value at any level.
    pub token: Arc<String>,
}

// ── Token generation + persistence ───────────────────────────────────────────

/// Generate a 32-byte cryptographic token, hex-encode it to a 64-char string,
/// write it to `~/.cascade/dashboard.token` with 0600 permissions, and return
/// the token string.
///
/// Inputs:  `config_dir` — `~/.cascade` directory (must already exist).
/// Outputs: `Ok(token_string)` — 64-char hex string.
///          `Err(std::io::Error)` — on any file I/O failure.
/// Constraints:
///   - Uses `rand::thread_rng()` which sources from OS CSPRNG (adequate for a
///     short-lived local loopback secret).
///   - Permissions set to 0o600 using `std::fs::set_permissions`.
///   - Token is NEVER written to any log, trace, or error message.
pub fn generate_dashboard_token(config_dir: &Path) -> Result<String, std::io::Error> {
    let mut bytes = [0u8; 32];
    rand::thread_rng().fill_bytes(&mut bytes);
    let token = hex::encode(bytes);

    let token_path = config_dir.join("dashboard.token");
    std::fs::write(&token_path, token.as_bytes())?;

    // Set 0600 permissions so only the owning user can read the token.
    #[cfg(unix)]
    {
        use std::fs::Permissions;
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&token_path, Permissions::from_mode(0o600))?;
    }

    debug!("dashboard: token written to {}", token_path.display());
    Ok(token)
}

// ── Middleware: bearer token validation ──────────────────────────────────────

/// Axum middleware that validates the `Authorization: Bearer <token>` header.
///
/// Purpose: gate every `/api/gci/*` route behind a constant-time token check.
/// Inputs:  `state` — shared DashboardState containing the expected token.
///          `req`   — the incoming HTTP request.
///          `next`  — the next handler in the middleware chain.
/// Outputs: the next handler's response on success;
///          HTTP 401 `{"error":"unauthorized"}` on missing or wrong token.
/// Constraints:
///   - Uses `subtle::ConstantTimeEq` to prevent timing side-channels.
///   - The supplied token value is NOT logged on mismatch.
///   - Applied ONLY to the `/api/gci/*` router group (not to `/health` or
///     read-only GET routes the frontend uses without auth).
pub async fn validate_dashboard_token(
    State(state): State<DashboardState>,
    headers: HeaderMap,
    req: Request<Body>,
    next: Next,
) -> Response {
    // Extract the Authorization header value.
    let supplied = match headers.get(AUTHORIZATION) {
        None => {
            debug!("dashboard: missing Authorization header → 401");
            return unauthorized();
        }
        Some(v) => v,
    };

    // Parse "Bearer <token>" — must have the "Bearer " prefix.
    let supplied_str = match supplied.to_str() {
        Err(_) => return unauthorized(),
        Ok(s) => s,
    };

    let bearer_token = match supplied_str.strip_prefix("Bearer ") {
        None => return unauthorized(),
        Some(t) => t,
    };

    // Constant-time comparison to prevent timing attacks.
    // WHY subtle::ConstantTimeEq: a standard `==` comparison on strings
    // short-circuits on the first differing byte, leaking timing information.
    let expected = state.token.as_bytes();
    let supplied_bytes = bearer_token.as_bytes();

    // Constant-time check: compare length first (mismatch is a safe fast-path
    // since the length itself is not secret), then the content in constant time.
    if expected.len() != supplied_bytes.len() || expected.ct_eq(supplied_bytes).unwrap_u8() == 0 {
        debug!("dashboard: bearer token mismatch → 401");
        return unauthorized();
    }

    next.run(req).await
}

/// Return an HTTP 401 Unauthorized response.
fn unauthorized() -> Response {
    (
        StatusCode::UNAUTHORIZED,
        Json(serde_json::json!({"error": "unauthorized"})),
    )
        .into_response()
}

// ── Handlers ──────────────────────────────────────────────────────────────────

/// `GET /health` — unauthenticated liveness probe.
///
/// Purpose: allows the frontend and external tooling to check whether the
/// dashboard is responsive without supplying auth credentials.
async fn health() -> impl IntoResponse {
    Json(serde_json::json!({"status": "ok"}))
}

/// `PUT /api/gci/file` — authenticated GCI file write stub.
///
/// Purpose: placeholder handler for the GCI write API. Full GCI file write
/// implementation is tracked in T-P2-E07-05 (P3 scope).
/// This handler exists now so the auth middleware is exercised and the route
/// group is established correctly.
async fn gci_write_file() -> impl IntoResponse {
    (StatusCode::OK, Json(serde_json::json!({"written": true})))
}

/// `DELETE /api/gci/file` — authenticated GCI file delete stub.
async fn gci_delete_file() -> impl IntoResponse {
    (StatusCode::OK, Json(serde_json::json!({"deleted": true})))
}

// ── Router builder ────────────────────────────────────────────────────────────

/// Build the Axum Router for the dashboard server.
///
/// Purpose: wire the route tree with CORS policy and token-auth middleware.
/// Inputs:  `state` — shared DashboardState.
/// Outputs: fully configured Axum Router.
/// Constraints:
///   - CORS: allow only `http://127.0.0.1:9761`; no wildcard origins.
///   - Auth middleware applied ONLY to the `/api/gci` router group.
///   - Read-only GET routes outside `/api/gci` do NOT require auth.
///
/// WHY separate router groups: the frontend's read-only GET calls (e.g.
/// `/api/status`, future P3 endpoints) must work without a token so the
/// browser-based dashboard can display the fleet status before the user
/// is presented with a token prompt. Only state-mutating GCI writes
/// require auth, consistent with the SIEGE HIGH-3 threat model.
pub fn build_router(state: DashboardState) -> Router {
    // GCI write API — requires Bearer token.
    // WHY from_fn_with_state: the middleware needs access to `state.token`
    // for the constant-time comparison.
    let gci_router = Router::new()
        .route("/file", put(gci_write_file))
        .route("/file", delete(gci_delete_file))
        .route("/file", post(gci_write_file))
        .layer(middleware::from_fn_with_state(
            state.clone(),
            validate_dashboard_token,
        ));

    // CORS policy: allow only the loopback dashboard origin.
    // WHY restrictive: this prevents a malicious web page on another origin
    // from making cross-origin requests to the dashboard API.
    let cors = CorsLayer::new()
        .allow_origin(
            "http://127.0.0.1:9761"
                .parse::<HeaderValue>()
                .expect("valid origin header value"),
        )
        .allow_methods([Method::GET, Method::POST, Method::PUT, Method::DELETE])
        .allow_headers([axum::http::header::CONTENT_TYPE, AUTHORIZATION]);

    let mut app = Router::new()
        .route("/health", get(health))
        .nest("/api/gci", gci_router);

    // Mount the browser dashboard SPA (ADR-P3-002): when CASCADE_DASHBOARD_DIST
    // points at a built dist/, merge the static-file router so http://127.0.0.1:9761
    // serves the dashboard. Explicit routes above (/health, /api/gci) keep priority
    // over the static catch-all. Unset (dev/tests) → API-only, unchanged behavior.
    if let Some(dist) = crate::http::dashboard_dist_dir() {
        app = app.merge(crate::http::static_handler::static_router(dist));
    }

    app.layer(cors).with_state(state)
}

// ── Dashboard server ──────────────────────────────────────────────────────────

/// The dashboard HTTP server.
///
/// Construct via [`Dashboard::new`]; run via [`Dashboard::run`].
pub struct Dashboard {
    state: DashboardState,
    bind_addr: SocketAddr,
    shutdown: CancellationToken,
}

impl Dashboard {
    /// Construct a new Dashboard server.
    ///
    /// Inputs:
    ///   - `config_dir` — `~/.cascade`; used to generate and persist the token.
    ///   - `bind_addr`  — must be `127.0.0.1:9761`.
    ///   - `shutdown`   — cancellation token for graceful stop.
    ///
    /// Outputs: `Ok(Dashboard)` with token generated and written to disk.
    ///          `Err(DashboardError::NonLoopback)` if `bind_addr` is not 127.0.0.1.
    ///          `Err(DashboardError::Listener)` on token file write failure.
    pub fn new(
        config_dir: &Path,
        bind_addr: SocketAddr,
        shutdown: CancellationToken,
    ) -> Result<Self, DashboardError> {
        // Hard guard: reject any non-loopback bind address (SIEGE HIGH-3).
        // WHY: binding to 0.0.0.0 exposes the dashboard to all LAN peers.
        if !bind_addr.ip().is_loopback() {
            return Err(DashboardError::NonLoopback { addr: bind_addr });
        }

        let token = generate_dashboard_token(config_dir).map_err(DashboardError::Listener)?;
        let state = DashboardState {
            token: Arc::new(token),
        };

        Ok(Self {
            state,
            bind_addr,
            shutdown,
        })
    }

    /// Return the bind address this server will listen on.
    pub fn bind_addr(&self) -> SocketAddr {
        self.bind_addr
    }

    /// Run the dashboard server until `shutdown` is cancelled.
    ///
    /// Inputs:  (self)
    /// Outputs: `Ok(())` on clean shutdown; `Err(DashboardError::Listener)` on
    ///          bind failure.
    /// Constraints:
    ///   - Binds only to the loopback address validated in `new()`.
    ///   - Accepts connections until `shutdown` fires, then drains gracefully.
    pub async fn run(self) -> Result<(), DashboardError> {
        let router = build_router(self.state);

        let listener = tokio::net::TcpListener::bind(self.bind_addr)
            .await
            .map_err(DashboardError::Listener)?;

        info!(addr = %self.bind_addr, "dashboard: listening on 127.0.0.1");

        let shutdown = self.shutdown.clone();
        axum::serve(listener, router)
            .with_graceful_shutdown(async move {
                shutdown.cancelled().await;
                debug!("dashboard: graceful shutdown signal received");
            })
            .await
            .map_err(DashboardError::Listener)?;

        info!("dashboard: stopped");
        Ok(())
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::to_bytes;
    use axum::http::Request;
    use tempfile::TempDir;
    use tower::util::ServiceExt;

    // ── Helpers ───────────────────────────────────────────────────────────────

    /// Build a test DashboardState with a known token.
    fn test_state(token: &str) -> DashboardState {
        DashboardState {
            token: Arc::new(token.to_string()),
        }
    }

    // ── Test 1: Dashboard binds 127.0.0.1, not 0.0.0.0 ───────────────────────

    /// Dashboard::new() rejects a 0.0.0.0 bind address with NonLoopback error.
    ///
    /// Acceptance: "curl http://LAN_IP:9761/health from another host on LAN
    /// returns 'connection refused'" — enforced by refusing to start.
    #[tokio::test]
    async fn test_non_loopback_bind_is_rejected() {
        let tmp = TempDir::new().unwrap();
        let lan_addr: SocketAddr = "0.0.0.0:9761".parse().unwrap();
        let shutdown = CancellationToken::new();

        let result = Dashboard::new(tmp.path(), lan_addr, shutdown);

        match result {
            Err(DashboardError::NonLoopback { addr }) => {
                assert_eq!(addr, lan_addr, "error should report the rejected address");
            }
            Ok(_) => panic!("Dashboard::new should have rejected 0.0.0.0"),
            Err(e) => panic!("unexpected error: {e}"),
        }
    }

    // ── Test 2: Unauthenticated GCI write returns 401 ─────────────────────────

    /// PUT /api/gci/file without Authorization header returns HTTP 401.
    ///
    /// Acceptance: "curl http://127.0.0.1:9761/api/gci/file -X PUT -d '{}'
    /// without Authorization returns HTTP 401"
    #[tokio::test]
    async fn test_unauthenticated_gci_write_returns_401() {
        let state = test_state("correct_token_here");
        let app = build_router(state);

        let req = Request::builder()
            .method(Method::PUT)
            .uri("/api/gci/file")
            .header("content-type", "application/json")
            .body(Body::from("{}"))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(
            resp.status(),
            StatusCode::UNAUTHORIZED,
            "missing Authorization header must return 401"
        );

        // Verify response body contains "unauthorized".
        let body = to_bytes(resp.into_body(), 1024).await.unwrap();
        let json: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(
            json["error"], "unauthorized",
            "body must contain {{\"error\":\"unauthorized\"}}"
        );
    }

    // ── Test 3: Authenticated GCI write passes ────────────────────────────────

    /// PUT /api/gci/file with correct Authorization: Bearer <token> returns 200.
    ///
    /// Acceptance: "curl http://127.0.0.1:9761/api/gci/file -X PUT -d '{}'
    /// with Authorization: Bearer <dashboard.token> returns HTTP 200 or 204"
    #[tokio::test]
    async fn test_authenticated_gci_write_passes() {
        let token = "a".repeat(64); // 64-char test token
        let state = test_state(&token);
        let app = build_router(state);

        let req = Request::builder()
            .method(Method::PUT)
            .uri("/api/gci/file")
            .header("content-type", "application/json")
            .header(AUTHORIZATION, format!("Bearer {token}"))
            .body(Body::from("{}"))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();

        assert!(
            resp.status().is_success(),
            "correct token must return 200 or 204, got: {}",
            resp.status()
        );
    }

    // ── Test 4: Wrong token returns 401 ──────────────────────────────────────

    /// PUT /api/gci/file with wrong token returns HTTP 401.
    ///
    /// Defensive test beyond the 3 acceptance tests: ensures constant-time
    /// comparison rejects a plausible-looking wrong token, not just missing headers.
    #[tokio::test]
    async fn test_wrong_token_returns_401() {
        let state =
            test_state("correct_token_here_64_chars_padded_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx");
        let app = build_router(state);

        let req = Request::builder()
            .method(Method::PUT)
            .uri("/api/gci/file")
            .header("content-type", "application/json")
            .header(AUTHORIZATION, "Bearer wrong_token_value")
            .body(Body::from("{}"))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(
            resp.status(),
            StatusCode::UNAUTHORIZED,
            "wrong token must return 401"
        );
    }

    // ── Test 5: Health endpoint is unauthenticated ────────────────────────────

    /// GET /health requires no Authorization header and returns {"status":"ok"}.
    #[tokio::test]
    async fn test_health_endpoint_unauthenticated() {
        let state = test_state("any_token");
        let app = build_router(state);

        let req = Request::builder()
            .method(Method::GET)
            .uri("/health")
            .body(Body::empty())
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();

        assert_eq!(
            resp.status(),
            StatusCode::OK,
            "/health must return 200 without auth"
        );
    }

    // ── Test 6: Token file permissions are 0600 ───────────────────────────────

    /// generate_dashboard_token writes a file with 0600 permissions.
    ///
    /// Acceptance: "~/.cascade/dashboard.token has permissions 0600"
    #[cfg(unix)]
    #[test]
    fn test_dashboard_token_permissions_0600() {
        use std::os::unix::fs::MetadataExt;
        let tmp = TempDir::new().unwrap();
        let token = generate_dashboard_token(tmp.path()).expect("generate token");

        // Token should be 64 hex chars.
        assert_eq!(token.len(), 64, "token should be 64 hex chars");

        // Permissions should be 0600.
        let token_path = tmp.path().join("dashboard.token");
        let meta = std::fs::metadata(&token_path).expect("stat token file");
        let mode = meta.mode() & 0o777;
        assert_eq!(mode, 0o600, "token file must be 0600, got {mode:o}");
    }
}
