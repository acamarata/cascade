//! Transport security integration tests — Origin guard, loopback check, capability gate.
//!
//! ## Coverage
//!
//! 1. `foreign_origin_post_returns_403` — POST /mcp with `Origin: https://evil.com` → 403
//! 2. `local_origin_post_returns_not_403` — POST /mcp with `Origin: http://localhost:7722`
//!    passes Origin check (may fail auth, but NOT 403)
//! 3. `loopback_addr_enforced` — unit test: `SocketAddr` with non-loopback IP → loopback
//!    check function returns the expected error variant
//! 4. `standard_cap_blocked_from_memory_write` — POST /mcp tools/call
//!    cascade.memory.write without PersonalData cap → 403
//! 5. `personal_data_cap_passes_memory_write_gate` — same call WITH PersonalData cap
//!    → passes origin+cap gate (may fail auth/dispatch)
//! 6. `health_endpoint_with_foreign_origin_returns_403`

use std::sync::Arc;

use axum::body::Body;
use axum::http::{Request, StatusCode};
use axum::Router;
use tower::ServiceExt as _;

use cascade_mcp::auth::McpAuth;
use cascade_mcp::security::{validate_local_origin, CapabilitySet};
use cascade_mcp::server::McpServerConfig;

// Re-export transport internals needed for app construction.

// ── Helpers ───────────────────────────────────────────────────────────────────

fn make_auth() -> Arc<McpAuth> {
    Arc::new(McpAuth::from_secret([42u8; 32]))
}

/// Build a minimal HTTP app with the same route structure as HttpServer.
///
/// We rely on the internal `handle_mcp_post` / `handle_health` handlers via the
/// public `HttpServer` + `listen` path — but for unit-style tests we construct the
/// router directly by mirroring `HttpServer::listen`'s app builder.  Since those
/// handlers are private, we use the public `HttpServer` and bind to port 0 (OS
/// picks a free port), then send requests over localhost.  This avoids port
/// conflicts and tests the full handler stack including security middleware.
///
/// For tests that only need to check the response status at the Axum layer
/// (not full TCP round-trips), we construct an equivalent router using the
/// public `cascade_mcp::transport::http` builders exposed for test use.
fn make_http_app() -> Router {
    use cascade_mcp::transport::http::build_http_app_for_test;
    let auth = make_auth();
    let config = McpServerConfig::default();
    build_http_app_for_test(auth, config)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

/// Foreign `Origin` header on POST /mcp must return 403 immediately.
#[tokio::test(flavor = "current_thread")]
async fn foreign_origin_post_returns_403() {
    let app = make_http_app();

    let req = Request::builder()
        .method("POST")
        .uri("/mcp")
        .header("origin", "https://evil.com")
        .header("content-type", "application/json")
        .body(Body::from(
            r#"{"jsonrpc":"2.0","id":1,"method":"tools/list"}"#,
        ))
        .unwrap();

    let resp = app.oneshot(req).await.unwrap();
    assert_eq!(
        resp.status(),
        StatusCode::FORBIDDEN,
        "foreign origin should be blocked"
    );
}

/// Localhost `Origin` header must pass the guard (may fail auth, but not 403).
#[tokio::test(flavor = "current_thread")]
async fn local_origin_post_returns_not_403() {
    let app = make_http_app();

    let req = Request::builder()
        .method("POST")
        .uri("/mcp")
        .header("origin", "http://localhost:7722")
        .header("content-type", "application/json")
        .body(Body::from(
            r#"{"jsonrpc":"2.0","id":1,"method":"tools/list"}"#,
        ))
        .unwrap();

    let resp = app.oneshot(req).await.unwrap();
    assert_ne!(
        resp.status(),
        StatusCode::FORBIDDEN,
        "local origin should not be blocked by origin guard"
    );
}

/// Foreign `Origin` on GET /mcp/health must return 403.
#[tokio::test(flavor = "current_thread")]
async fn health_endpoint_with_foreign_origin_returns_403() {
    let app = make_http_app();

    let req = Request::builder()
        .method("GET")
        .uri("/mcp/health")
        .header("origin", "https://attacker.example")
        .body(Body::empty())
        .unwrap();

    let resp = app.oneshot(req).await.unwrap();
    assert_eq!(resp.status(), StatusCode::FORBIDDEN);
}

/// `tools/call cascade.memory.write` without `PersonalData` cap bit → 403.
///
/// The auth header is omitted intentionally: the origin guard and cap gate both
/// fire before auth.  We supply a localhost origin so only the cap gate fires.
#[tokio::test(flavor = "current_thread")]
async fn standard_cap_blocked_from_memory_write() {
    let app = make_http_app();

    // X-Cascade-Cap = Standard only (bit 0x01), no PersonalData (bit 0x02).
    let cap_bits = CapabilitySet::STANDARD.bits();
    let body = r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cascade.memory.write","arguments":{}}}"#;

    let req = Request::builder()
        .method("POST")
        .uri("/mcp")
        .header("origin", "http://localhost:7722")
        .header("x-cascade-cap", cap_bits.to_string())
        .header("content-type", "application/json")
        // include a fake bearer so we get past auth (the token will fail auth
        // validation, but the cap gate fires first in the code path)
        .header("authorization", "Bearer fake-token")
        .body(Body::from(body))
        .unwrap();

    let resp = app.oneshot(req).await.unwrap();
    // Cap gate fires before auth dispatch, returns 403.
    assert_eq!(
        resp.status(),
        StatusCode::FORBIDDEN,
        "standard cap should be blocked from memory.write"
    );
}

/// Same call WITH PersonalData cap passes the cap gate (auth will reject the
/// fake token with 401, not 403).
#[tokio::test(flavor = "current_thread")]
async fn personal_data_cap_passes_memory_write_gate() {
    let app = make_http_app();

    // PersonalData bit OR'ed in.
    let cap_bits = CapabilitySet::STANDARD.bits() | CapabilitySet::PERSONAL_DATA.bits();
    let body = r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"cascade.memory.write","arguments":{}}}"#;

    let req = Request::builder()
        .method("POST")
        .uri("/mcp")
        .header("origin", "http://localhost:7722")
        .header("x-cascade-cap", cap_bits.to_string())
        .header("content-type", "application/json")
        .header("authorization", "Bearer fake-token")
        .body(Body::from(body))
        .unwrap();

    let resp = app.oneshot(req).await.unwrap();
    // Cap gate passed; auth rejects → 401 (not 403).
    assert_ne!(
        resp.status(),
        StatusCode::FORBIDDEN,
        "PersonalData cap should pass the gate"
    );
}

/// Unit test: `validate_local_origin` with non-loopback origin string.
///
/// This does not start any server — it directly calls the security function.
#[test]
fn loopback_addr_enforced_unit() {
    use axum::http::HeaderMap;
    use axum::http::HeaderValue;

    let mut headers = HeaderMap::new();
    headers.insert("origin", HeaderValue::from_static("http://192.168.1.1"));
    assert_eq!(
        validate_local_origin(&headers),
        Err(StatusCode::FORBIDDEN),
        "non-loopback origin must be rejected"
    );

    let mut headers2 = HeaderMap::new();
    headers2.insert("host", HeaderValue::from_static("192.168.1.1:7722"));
    assert_eq!(
        validate_local_origin(&headers2),
        Err(StatusCode::FORBIDDEN),
        "non-loopback host must be rejected"
    );
}
