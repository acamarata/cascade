//! Purpose: Static-file mount for the dashboard SPA + /api/ping health endpoint.
//! Inputs:  `static_dir: PathBuf` — root of the compiled dashboard dist/ directory.
//! Outputs: An axum `Router` that serves:
//!          - GET /api/ping  → `{"ok":true,"version":"<CARGO_PKG_VERSION>"}`
//!          - GET /*         → ServeDir from `static_dir`; falls back to index.html for SPA routing.
//! Constraints: Non-/api/* paths all return index.html so React Router handles client-side routes.
//!              ServeDir does NOT expose directory listings (tower-http default: off).
//! SPORT: MASTER-ENDPOINTS.md § GET /api/ping, GET /* (static SPA)

use std::path::PathBuf;

use axum::{
    http::StatusCode,
    response::{Html, IntoResponse},
    routing::get,
    Json, Router,
};
use serde_json::{json, Value};
use tower_http::services::ServeDir;

/// Build a router that serves the dashboard SPA and the /api/ping endpoint.
///
/// # Arguments
/// * `static_dir` – the directory containing `index.html` and all compiled assets.
///
/// # Returns
/// An axum `Router` that can be merged into the main server router.
pub fn static_router<S>(static_dir: PathBuf) -> Router<S>
where
    S: Clone + Send + Sync + 'static,
{
    let index_html = static_dir.join("index.html");
    let assets_dir = static_dir.join("assets");

    Router::new()
        // /api/ping — lightweight health check; version comes from the Cargo manifest.
        .route("/api/ping", get(ping))
        // Compiled, content-hashed Vite assets (JS/CSS) live under dist/assets/.
        .nest_service("/assets", ServeDir::new(assets_dir))
        // SPA fallback: every other path (/, /projects/42, any client-side React route)
        // returns index.html so React Router handles navigation. Deterministic file read
        // — no dependence on ServeDir's not_found_service (which does not fire as a
        // Router fallback_service in axum 0.7 / tower-http 0.5).
        .fallback(move || {
            let index = index_html.clone();
            async move {
                match tokio::fs::read_to_string(&index).await {
                    Ok(body) => Html(body).into_response(),
                    Err(_) => StatusCode::NOT_FOUND.into_response(),
                }
            }
        })
}

/// GET /api/ping — returns `{"ok":true,"version":"<CARGO_PKG_VERSION>"}`.
///
/// Purpose: allows the dashboard frontend and external health checks to confirm
/// the daemon is running and report its version without additional auth.
async fn ping() -> Json<Value> {
    Json(json!({
        "ok": true,
        "version": env!("CARGO_PKG_VERSION"),
    }))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use axum::{
        body::Body,
        http::{Request, StatusCode},
    };
    use std::fs;
    use tower::ServiceExt; // for `.oneshot()`

    /// Build a temporary dist/ directory with a minimal index.html.
    fn make_dist_dir(dir: &tempfile::TempDir) -> PathBuf {
        let dist = dir.path().to_path_buf();
        fs::write(
            dist.join("index.html"),
            r#"<!doctype html><html><body><div id="root"></div></body></html>"#,
        )
        .expect("write index.html");
        dist
    }

    #[tokio::test]
    async fn test_ping_returns_200_ok() {
        let dir = tempfile::tempdir().expect("tempdir");
        let dist = make_dist_dir(&dir);
        let app: Router = static_router(dist);

        let response = app
            .oneshot(
                Request::builder()
                    .uri("/api/ping")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::OK);

        let body = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .unwrap();
        let v: serde_json::Value = serde_json::from_slice(&body).unwrap();
        assert_eq!(v["ok"], true);
        assert!(v["version"].is_string());
    }

    #[tokio::test]
    async fn test_static_index_returns_200() {
        let dir = tempfile::tempdir().expect("tempdir");
        let dist = make_dist_dir(&dir);
        let app: Router = static_router(dist);

        let response = app
            .oneshot(Request::builder().uri("/").body(Body::empty()).unwrap())
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::OK);
    }

    #[tokio::test]
    async fn test_spa_fallback_unknown_route() {
        // GET /some-react-route — ServeDir won't find a file, falls back to index.html.
        let dir = tempfile::tempdir().expect("tempdir");
        let dist = make_dist_dir(&dir);
        let app: Router = static_router(dist);

        let response = app
            .oneshot(
                Request::builder()
                    .uri("/some-react-route")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();

        // ServeFile fallback returns 200 with the index.html content.
        assert_eq!(response.status(), StatusCode::OK);

        let body = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .unwrap();
        let text = String::from_utf8_lossy(&body);
        assert!(
            text.contains("id=\"root\""),
            "SPA fallback must return index.html"
        );
    }
}
