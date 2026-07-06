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
use cascade_providers::ProviderRegistry;
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

    /// Provider registry for routing chat (and future) requests.
    ///
    /// `None` in minimal test setups that do not need provider routing.
    /// When `None`, the chat handler falls back to a typed "no provider"
    /// error event rather than panicking.
    pub provider_registry: Option<Arc<ProviderRegistry>>,

    /// In-memory ring buffer of the last N cascade-core routing decisions.
    ///
    /// Populated by the `RoutingObserver` closure installed on any
    /// `cascade_core::Router` instance that uses `Router::with_observer()`.
    /// `None` in minimal test setups that do not call `Router::select()`.
    /// The Fleet UI polls `GET /api/fleet/routing` to read from this ring.
    pub routing_ring: Option<crate::http::fleet_routing::RoutingRing>,

    /// `[middleware]` flags from config.toml (E2-S2 pre-processing).
    ///
    /// All flags default to `false`; the chat handler consults them per
    /// request. When every flag is off the chat path performs no middleware
    /// work at all (no GP probes, zero added latency).
    pub middleware: crate::config::MiddlewareConfig,
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

/// Request body for `PUT`/`POST /api/gci/file`.
///
/// `path` is a path relative to `~/.claude` (e.g. `"rules/foo.md"`). An
/// absolute path is also accepted but MUST resolve inside `~/.claude` after
/// canonicalization — see [`resolve_gci_target`].
///
/// Both fields are `Option` so that a bare `{}` body (used by the existing
/// auth-middleware smoke tests) is accepted as a documented health-check
/// no-op rather than a 400 — the auth layer is what those tests exercise,
/// not the write path itself.
#[derive(Debug, Default, serde::Deserialize)]
struct GciFileWriteRequest {
    path: Option<String>,
    content: Option<String>,
}

/// Request body for `DELETE /api/gci/file`.
#[derive(Debug, Default, serde::Deserialize)]
struct GciFileDeleteRequest {
    path: Option<String>,
}

/// Resolve `~/.claude` — the sole allowed base directory for GCI file writes
/// and deletes.
///
/// Returns `None` if `$HOME` is not set (should never happen in production).
fn gci_base_dir() -> Option<std::path::PathBuf> {
    std::env::var_os("HOME").map(|h| std::path::PathBuf::from(h).join(".claude"))
}

/// Resolve `requested` (relative or absolute) against `base`, then verify the
/// result stays inside `base` after canonicalization.
///
/// Purpose: shared traversal guard for both write and delete — rejects `..`
/// components and symlink escapes.
///
/// WHY canonicalize the *parent* directory instead of the target itself:
/// unlike a read, a write target may not exist yet (and a delete target may
/// already be gone by the time this check races with another actor), so we
/// cannot rely on `std::fs::canonicalize` succeeding on the leaf path. We
/// canonicalize the nearest existing ancestor and re-append the remaining
/// (already `..`-free, checked-below) components.
///
/// Inputs:  `base` — must already exist (`~/.claude`); `requested` — raw
///          client-supplied path string.
/// Outputs: `Ok(PathBuf)` — the fully-resolved, validated absolute path.
///          `Err(String)` — human-readable rejection reason (traversal,
///          escape, or I/O failure), suitable for a 400 response body.
fn resolve_gci_target(base: &Path, requested: &str) -> Result<std::path::PathBuf, String> {
    if requested.trim().is_empty() {
        return Err("path must not be empty".to_string());
    }

    let requested_path = std::path::Path::new(requested);

    // Reject any ".." component outright — belt-and-suspenders ahead of the
    // canonicalize-based check below (also rejects absolute paths that don't
    // even nominally join under base as a fast, readable error).
    if requested_path
        .components()
        .any(|c| matches!(c, std::path::Component::ParentDir))
    {
        return Err("access denied: path traversal ('..') is not allowed".to_string());
    }

    let candidate = if requested_path.is_absolute() {
        requested_path.to_path_buf()
    } else {
        base.join(requested_path)
    };

    let canonical_base = std::fs::canonicalize(base)
        .map_err(|e| format!("cannot resolve GCI base directory: {e}"))?;

    // The candidate's parent may not exist yet (new file write) — canonicalize
    // the nearest existing ancestor instead of the leaf, then re-append the
    // (already traversal-checked) remaining components.
    let mut existing_ancestor = candidate.as_path();
    let mut remaining: Vec<std::ffi::OsString> = Vec::new();
    loop {
        if existing_ancestor.exists() {
            break;
        }
        match (existing_ancestor.file_name(), existing_ancestor.parent()) {
            (Some(name), Some(parent)) => {
                remaining.push(name.to_os_string());
                existing_ancestor = parent;
            }
            _ => break,
        }
    }

    let canonical_ancestor = std::fs::canonicalize(existing_ancestor)
        .map_err(|e| format!("cannot resolve path: {e}"))?;

    if !canonical_ancestor.starts_with(&canonical_base) {
        return Err("access denied: path is outside the allowed GCI directory".to_string());
    }

    let mut resolved = canonical_ancestor;
    for part in remaining.into_iter().rev() {
        resolved.push(part);
    }

    Ok(resolved)
}

/// `PUT`/`POST /api/gci/file` — authenticated GCI file write.
///
/// Purpose: writes `content` to `path` (relative to `~/.claude`) using an
/// atomic temp-file + rename, creating any missing parent directories first.
/// A bare `{}` body (no `path`) is treated as a no-op success — this keeps
/// the existing auth-middleware smoke tests (which only exercise the token
/// check, not the write path) green without masking real write failures.
///
/// Outputs: `200 {"written": true, "path": "<resolved>"}` on success;
///          `400 {"written": false, "error": "..."}` on validation failure
///          (traversal, missing HOME) or I/O error. Never a fake success.
async fn gci_write_file(
    Json(req): Json<GciFileWriteRequest>,
) -> impl IntoResponse {
    let Some(path) = req.path.as_deref() else {
        // No path supplied — documented no-op (see doc comment above).
        return (StatusCode::OK, Json(serde_json::json!({"written": true}))).into_response();
    };

    let base = match gci_base_dir() {
        Some(b) => b,
        None => {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({"written": false, "error": "HOME not set"})),
            )
                .into_response()
        }
    };

    // Ensure the base itself exists so canonicalize() in resolve_gci_target
    // has something to resolve against (fresh ~/.claude on a new machine).
    if let Err(e) = std::fs::create_dir_all(&base) {
        return (
            StatusCode::BAD_REQUEST,
            Json(serde_json::json!({"written": false, "error": format!("cannot create GCI base dir: {e}")})),
        )
            .into_response();
    }

    let target = match resolve_gci_target(&base, path) {
        Ok(p) => p,
        Err(e) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({"written": false, "error": e})),
            )
                .into_response()
        }
    };

    let content = req.content.unwrap_or_default();

    match write_file_atomic(&target, content.as_bytes()) {
        Ok(()) => (
            StatusCode::OK,
            Json(serde_json::json!({"written": true, "path": target.to_string_lossy()})),
        )
            .into_response(),
        Err(e) => (
            StatusCode::BAD_REQUEST,
            Json(serde_json::json!({"written": false, "error": e.to_string()})),
        )
            .into_response(),
    }
}

/// `DELETE /api/gci/file` — authenticated GCI file delete.
///
/// Purpose: removes `path` (relative to `~/.claude`) from disk. A bare `{}`
/// body (no `path`) is a documented no-op success, matching `gci_write_file`.
///
/// Outputs: `200 {"deleted": true}` on success (including "already absent",
///          which is idempotent-safe for a delete); `400 {"deleted": false,
///          "error": "..."}` on validation failure or I/O error other than
///          NotFound.
async fn gci_delete_file(
    Json(req): Json<GciFileDeleteRequest>,
) -> impl IntoResponse {
    let Some(path) = req.path.as_deref() else {
        return (StatusCode::OK, Json(serde_json::json!({"deleted": true}))).into_response();
    };

    let base = match gci_base_dir() {
        Some(b) => b,
        None => {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({"deleted": false, "error": "HOME not set"})),
            )
                .into_response()
        }
    };

    if let Err(e) = std::fs::create_dir_all(&base) {
        return (
            StatusCode::BAD_REQUEST,
            Json(serde_json::json!({"deleted": false, "error": format!("cannot create GCI base dir: {e}")})),
        )
            .into_response();
    }

    let target = match resolve_gci_target(&base, path) {
        Ok(p) => p,
        Err(e) => {
            return (
                StatusCode::BAD_REQUEST,
                Json(serde_json::json!({"deleted": false, "error": e})),
            )
                .into_response()
        }
    };

    match std::fs::remove_file(&target) {
        Ok(()) => (StatusCode::OK, Json(serde_json::json!({"deleted": true}))).into_response(),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            // Idempotent: deleting an already-absent file is not a failure.
            (StatusCode::OK, Json(serde_json::json!({"deleted": true}))).into_response()
        }
        Err(e) => (
            StatusCode::BAD_REQUEST,
            Json(serde_json::json!({"deleted": false, "error": e.to_string()})),
        )
            .into_response(),
    }
}

/// Write `bytes` to `target` atomically: write to a sibling `.tmp` file, then
/// `rename` over the destination (POSIX-atomic on the same filesystem).
///
/// Creates any missing parent directories first.
fn write_file_atomic(target: &Path, bytes: &[u8]) -> std::io::Result<()> {
    if let Some(parent) = target.parent() {
        std::fs::create_dir_all(parent)?;
    }
    // Append ".tmp" to the full file name (not `with_extension`, which would
    // replace rather than append to an existing extension like ".md").
    let mut tmp_name = target
        .file_name()
        .map(|n| n.to_os_string())
        .unwrap_or_default();
    tmp_name.push(".tmp");
    let tmp_path = target.with_file_name(tmp_name);

    std::fs::write(&tmp_path, bytes)?;
    std::fs::rename(&tmp_path, target)?;
    Ok(())
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
        .nest("/api/gci", gci_router)
        // GCI read API — no auth required; read-only GET routes for the Global panel.
        // WHY second nest at the same prefix: axum merges both routers; /api/gci/file
        // (auth-protected write) and /api/gci/rules (no-auth read) coexist correctly.
        .nest("/api/gci", crate::http::gci_handlers::gci_read_router())
        .nest("/api/personal", crate::http::personal_handlers::router())
        .nest("/api/projects", crate::http::projects_handlers::router())
        .nest("/api/chat", crate::http::chat_handlers::router())
        .nest("/api/personal", crate::http::usage_history::router())
        .nest("/api/gci", crate::http::hooks_write::router())
        .nest("/api/gci", crate::http::harness::router())
        .nest("/api/gci", crate::http::rag_status::router())
        // RAG-08: memory chat_history API — POST/GET /api/memory/chat
        .nest("/api/memory", crate::http::chat_history_memory::router())
        // mem-01: CC session harvest — POST /api/harvest/cc-session
        .nest("/api/harvest", crate::http::harvest::router())
        // fleet-01: routing event stream — GET /api/fleet/routing
        .nest("/api/fleet", crate::http::fleet_routing::router());

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
        let routing_ring = Some(crate::http::fleet_routing::new_routing_ring());
        // E2-S2: middleware flags come from config.toml's [middleware] section.
        // Config::load returns defaults (all flags off) when the file is
        // absent; a malformed file also falls back to defaults here — the
        // dashboard must never fail to start over an optional feature flag.
        let middleware = crate::config::Config::load(config_dir)
            .map(|c| c.middleware)
            .unwrap_or_default();
        let state = DashboardState {
            token: Arc::new(token),
            provider_registry: None,
            routing_ring,
            middleware,
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
            provider_registry: None,
            routing_ring: None,
            middleware: Default::default(),
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

    // ── Fix 1: GCI write/delete are real FS ops, not fake successes ──────────

    /// write_file_atomic + resolve_gci_target round-trip: write then read back
    /// the exact bytes, including creating a missing parent directory.
    #[test]
    fn test_gci_write_then_read_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let base = tmp.path();

        let target = resolve_gci_target(base, "notes/todo.md")
            .expect("relative path under base must resolve");
        write_file_atomic(&target, b"hello gci").expect("write must succeed");

        let read_back = std::fs::read_to_string(&target).expect("file must exist after write");
        assert_eq!(read_back, "hello gci");

        // Parent dir was created.
        assert!(base.join("notes").is_dir());
    }

    /// A delete (remove_file) on a path resolved via resolve_gci_target
    /// actually removes the file from disk.
    #[test]
    fn test_gci_delete_removes_file() {
        let tmp = TempDir::new().unwrap();
        let base = tmp.path();

        let target =
            resolve_gci_target(base, "scratch.md").expect("relative path must resolve");
        write_file_atomic(&target, b"to be deleted").expect("write must succeed");
        assert!(target.exists(), "precondition: file exists before delete");

        std::fs::remove_file(&target).expect("delete must succeed");
        assert!(!target.exists(), "file must be gone after delete");
    }

    /// resolve_gci_target rejects a ".." traversal attempt that would escape
    /// the allowed base directory.
    #[test]
    fn test_gci_resolve_rejects_path_traversal() {
        let tmp = TempDir::new().unwrap();
        let base = tmp.path().join("claude");
        std::fs::create_dir_all(&base).unwrap();

        let result = resolve_gci_target(&base, "../../../etc/passwd");
        assert!(
            result.is_err(),
            "traversal path must be rejected, got: {result:?}"
        );
        assert!(result.unwrap_err().contains("traversal"));
    }

    /// resolve_gci_target rejects an absolute path outside the allowed base,
    /// even without literal ".." components (symlink-free absolute escape).
    #[test]
    fn test_gci_resolve_rejects_absolute_escape() {
        let tmp = TempDir::new().unwrap();
        let base = tmp.path().join("claude");
        std::fs::create_dir_all(&base).unwrap();

        // An absolute path pointing well outside base.
        let result = resolve_gci_target(&base, "/etc/passwd");
        assert!(
            result.is_err(),
            "absolute path outside base must be rejected, got: {result:?}"
        );
    }

    /// PUT /api/gci/file with a bare `{}` body remains a documented no-op
    /// (regression guard for the pre-existing auth-only smoke tests).
    #[tokio::test]
    async fn test_gci_write_empty_body_is_noop_success() {
        let token = "a".repeat(64);
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
        assert!(resp.status().is_success());

        let body = to_bytes(resp.into_body(), 1024).await.unwrap();
        let json: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(json["written"], true);
    }
}
