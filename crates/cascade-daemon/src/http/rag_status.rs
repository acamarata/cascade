//! Purpose: HTTP handler for GET /api/gci/rag-status — returns RAG/RRF serve status.
//!   In P3 this returns a stub/placeholder response (serving=false, index_count=0).
//!   The real RAG pipeline (BGE-M3 + FTS5 + sqlite-vec + RRF) is P4 scope; this
//!   endpoint establishes the contract that the RagStatusCard UI depends on.
//! Inputs:  HTTP GET (no query params, no request body).
//! Outputs: 200 JSON — cascade_types::rag::RagStatusResponse.
//! Constraints:
//!   - P3 stub values only; no actual index querying.
//!   - serve_endpoint value is the planned P4 address; not bound yet.
//!   - No writes; no side effects; always returns 200.
//!     SPORT: T-P3-E02-29 — MASTER-ROUTES.md GET /api/gci/rag-status

use axum::{routing::get, Json, Router};
use cascade_types::rag::RagStatusResponse;

use crate::dashboard::DashboardState;

// ── Router ────────────────────────────────────────────────────────────────────

/// Builds the rag-status sub-router, nested at `/api/gci` by the parent router.
///
/// Exposes:
///   GET /rag-status  →  `rag_status_handler`  →  200 RagStatusResponse JSON
pub fn router() -> Router<DashboardState> {
    Router::new().route("/rag-status", get(rag_status_handler))
}

// ── Handler ───────────────────────────────────────────────────────────────────

/// GET /api/gci/rag-status
///
/// Returns the current RAG/RRF serve status. In P3 all values are stubs:
/// - `serving`: false — no index has been built yet (P4 scope).
/// - `index_count`: 0 — no documents indexed.
/// - `last_indexed`: None — never indexed.
/// - `index_size_bytes`: 0 — no on-disk index.
/// - `serve_endpoint`: planned P4 address, not yet bound.
///
/// The RagStatusCard renders this as a yellow "No index" status pill.
async fn rag_status_handler() -> Json<RagStatusResponse> {
    // P3 stub: all values are defaults.
    // T-P4-E04-08: query_cache_* fields are 0 until CachedIndex is wired into
    // DaemonState and passed through StatusBuilder (P4 scope).
    Json(RagStatusResponse {
        serving: false,
        index_count: 0,
        last_indexed: None,
        index_size_bytes: 0,
        serve_endpoint: "localhost:9762/rag (P4)".to_string(),
        query_cache_hits: 0,
        query_cache_misses: 0,
        query_cache_size: 0,
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use axum::{
        body::Body,
        http::{Request, StatusCode},
    };
    use tower::ServiceExt;

    use cascade_types::rag::RagStatusResponse;

    use super::router;
    use crate::dashboard::DashboardState;

    /// Construct a minimal test DashboardState.
    fn test_state() -> DashboardState {
        DashboardState {
            token: std::sync::Arc::new("test-token".to_string()),
        }
    }

    #[tokio::test]
    async fn rag_status_returns_200_with_valid_response() {
        let app = router().with_state(test_state());

        let response = app
            .oneshot(
                Request::builder()
                    .uri("/rag-status")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();

        assert_eq!(response.status(), StatusCode::OK);

        let body = axum::body::to_bytes(response.into_body(), usize::MAX)
            .await
            .unwrap();
        let parsed: RagStatusResponse = serde_json::from_slice(&body)
            .expect("response body must deserialize as RagStatusResponse");

        // P3 stub contract
        assert!(!parsed.serving, "P3 stub: serving must be false");
        assert_eq!(parsed.index_count, 0, "P3 stub: index_count must be 0");
        assert!(
            parsed.last_indexed.is_none(),
            "P3 stub: last_indexed must be None"
        );
        assert_eq!(
            parsed.index_size_bytes, 0,
            "P3 stub: index_size_bytes must be 0"
        );
        assert!(
            !parsed.serve_endpoint.is_empty(),
            "serve_endpoint must be non-empty"
        );
    }
}
