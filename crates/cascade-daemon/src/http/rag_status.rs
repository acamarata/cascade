//! Purpose: HTTP handler for GET /api/gci/rag-status — returns RAG/RRF serve status.
//!   Returns REAL stats by reading the global index root (~/.cascade/index/):
//!   - index_count: COUNT(*) FROM index_state in cascade_state.db
//!   - serving: true when index_count > 0
//!   - last_indexed: MAX(indexed_at) from cascade_state.db, formatted as ISO 8601 UTC
//!   - index_size_bytes: sum of file sizes under ~/.cascade/index/
//!   Degrades gracefully to zeros/false/None when no index exists.
//! Inputs:  HTTP GET (no query params, no request body).
//! Outputs: 200 JSON — cascade_types::rag::RagStatusResponse.
//! Constraints:
//!   - All I/O errors swallowed; fields default to 0/false/None.
//!   - No writes; no side effects; always returns 200.
//!     SPORT: T-P3-E02-29 — MASTER-ROUTES.md GET /api/gci/rag-status

use axum::{routing::get, Json, Router};
use cascade_types::paths;
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
/// Reads real index stats from ~/.cascade/index/cascade_state.db.
/// Degrades gracefully (zeros/false/None) when no index has been built yet.
async fn rag_status_handler() -> Json<RagStatusResponse> {
    // Run SQLite + fs I/O off the async executor.
    let stats = tokio::task::spawn_blocking(read_index_stats)
        .await
        .unwrap_or_else(|_| RagStatusResponse {
            serving: false,
            index_count: 0,
            last_indexed: None,
            index_size_bytes: 0,
            serve_endpoint: String::new(),
            query_cache_hits: 0,
            query_cache_misses: 0,
            query_cache_size: 0,
        });
    Json(stats)
}

// ── Real stats reader (sync, runs in spawn_blocking) ─────────────────────────

/// Read real index statistics from the global cascade index root.
///
/// # Purpose
/// Replaces the P3 zero-stub with live data from cascade_state.db and the
/// index directory. Called via `spawn_blocking` to avoid blocking the executor.
fn read_index_stats() -> RagStatusResponse {
    let index_root = paths::global_cascade_dir().join("index");
    read_index_stats_at(&index_root)
}

/// Read index statistics from an explicit `index_root` path.
///
/// # Source of each field
/// - `index_count`:      `COUNT(*) FROM index_state` in cascade_state.db
/// - `serving`:          true when `index_count > 0`
/// - `last_indexed`:     `MAX(indexed_at)` in cascade_state.db, ISO 8601 UTC
/// - `index_size_bytes`: recursive sum of file sizes under `index_root`
/// - `query_cache_*`:    0 (CachedIndex not yet wired into DaemonState, P4)
/// - `serve_endpoint`:   empty string (P4 scope)
///
/// # Constraints
/// - All errors swallowed; fields degrade to 0/false/None.
fn read_index_stats_at(index_root: &std::path::Path) -> RagStatusResponse {
    // Only open the state DB if it already exists — never create it on a status read.
    let state_db = index_root.join("cascade_state.db");
    if !state_db.exists() {
        return RagStatusResponse {
            serving: false,
            index_count: 0,
            last_indexed: None,
            index_size_bytes: 0,
            serve_endpoint: String::new(),
            query_cache_hits: 0,
            query_cache_misses: 0,
            query_cache_size: 0,
        };
    }

    let store = match cascade_rag::IndexStateStore::open(index_root) {
        Ok(s) => s,
        Err(_) => {
            return RagStatusResponse {
                serving: false,
                index_count: 0,
                last_indexed: None,
                index_size_bytes: 0,
                serve_endpoint: String::new(),
                query_cache_hits: 0,
                query_cache_misses: 0,
                query_cache_size: 0,
            };
        }
    };

    let index_count = store.count() as u32;
    let serving = index_count > 0;

    // Convert Unix-second timestamp to ISO 8601 UTC string.
    let last_indexed = store.last_indexed_at().map(unix_secs_to_iso8601);

    // Sum file sizes of every regular file under the index root.
    let index_size_bytes = dir_size_bytes(index_root);

    RagStatusResponse {
        serving,
        index_count,
        last_indexed,
        index_size_bytes,
        serve_endpoint: String::new(),
        query_cache_hits: 0,
        query_cache_misses: 0,
        query_cache_size: 0,
    }
}

/// Format a Unix-second timestamp as an ISO 8601 UTC string (`YYYY-MM-DDTHH:MM:SSZ`).
///
/// Uses the same `Utc.timestamp_opt` pattern established in usage_history.rs.
/// chrono is already a workspace dependency of cascade-daemon.
fn unix_secs_to_iso8601(secs: i64) -> String {
    use chrono::{TimeZone, Utc};
    let dt = Utc
        .timestamp_opt(secs, 0)
        .single()
        .unwrap_or_else(Utc::now);
    dt.format("%Y-%m-%dT%H:%M:%SZ").to_string()
}

/// Recursively sum the sizes of all files under `dir`.
/// Returns 0 when the directory does not exist or cannot be read.
fn dir_size_bytes(dir: &std::path::Path) -> u64 {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return 0;
    };
    let mut total: u64 = 0;
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            total += dir_size_bytes(&path);
        } else if let Ok(meta) = std::fs::metadata(&path) {
            total += meta.len();
        }
    }
    total
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

    use super::*;
    use crate::dashboard::DashboardState;

    fn test_state() -> DashboardState {
        DashboardState {
            token: std::sync::Arc::new("test-token".to_string()),
            provider_registry: None,
        }
    }

    // ── rag_status::returns_200 ───────────────────────────────────────────────

    /// Handler always returns HTTP 200 with a valid RagStatusResponse body.
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
        // Must deserialize without error.
        let _: RagStatusResponse =
            serde_json::from_slice(&body).expect("body must be valid RagStatusResponse");
    }

    // ── rag_status::zero_when_no_index ───────────────────────────────────────

    /// read_index_stats_at returns all-zero/false/None when the index root
    /// does not exist (never been built).
    #[test]
    fn read_stats_degrades_when_no_index() {
        let tmp = tempfile::tempdir().expect("tmpdir");
        // Point at a non-existent sub-dir — no DB, no files.
        let index_root = tmp.path().join("no_such_index");

        let stats = read_index_stats_at(&index_root);

        assert!(!stats.serving, "serving must be false with no index");
        assert_eq!(stats.index_count, 0, "index_count must be 0 with no index");
        assert!(stats.last_indexed.is_none(), "last_indexed must be None with no index");
        assert_eq!(stats.index_size_bytes, 0, "index_size_bytes must be 0 with no index");
    }

    // ── rag_status::real_count_and_serving ───────────────────────────────────

    /// After seeding cascade_state.db with 2 rows, read_index_stats_at reports
    /// count==2, serving==true, last_indexed is Some, and index_size_bytes > 0.
    #[test]
    fn read_stats_returns_real_count_when_index_exists() {
        use cascade_rag::IndexStateStore;
        use std::io::Write;

        let tmp = tempfile::tempdir().expect("tmpdir");
        let index_root = tmp.path().join("index");
        std::fs::create_dir_all(&index_root).expect("create index_root");

        // Seed two documents into cascade_state.db.
        let store = IndexStateStore::open(&index_root).expect("open state store");
        for content in &[b"file alpha content" as &[u8], b"file beta content"] {
            let mut f = tempfile::NamedTempFile::new_in(tmp.path()).expect("tempfile");
            f.write_all(content).expect("write");
            f.flush().expect("flush");
            let (_, hash) = store.classify(f.path()).expect("classify");
            store.set_hash(f.path(), &hash.unwrap()).expect("set_hash");
        }
        assert_eq!(store.count(), 2, "precondition: 2 docs seeded");
        drop(store);

        let stats = read_index_stats_at(&index_root);

        assert_eq!(stats.index_count, 2, "index_count must equal seeded rows");
        assert!(stats.serving, "serving must be true when index_count > 0");
        assert!(
            stats.last_indexed.is_some(),
            "last_indexed must be Some after indexing"
        );
        let ts = stats.last_indexed.as_deref().unwrap();
        assert!(ts.contains('T') && ts.ends_with('Z'), "last_indexed ISO 8601 format: {ts}");
        assert!(
            stats.index_size_bytes > 0,
            "index_size_bytes must be > 0 when DB file exists"
        );
    }

    // ── rag_status::unix_secs_to_iso8601 ─────────────────────────────────────

    /// Known epoch value must produce the correct ISO 8601 string.
    #[test]
    fn unix_secs_iso8601_epoch_zero() {
        let s = unix_secs_to_iso8601(0);
        assert_eq!(s, "1970-01-01T00:00:00Z");
    }

    /// A realistic 2024 timestamp produces a plausible ISO 8601 string.
    #[test]
    fn unix_secs_iso8601_recent_timestamp() {
        // 2024-01-15 12:00:00 UTC = 1705320000
        let s = unix_secs_to_iso8601(1_705_320_000);
        assert_eq!(s, "2024-01-15T12:00:00Z");
    }

    // ── rag_status::dir_size_bytes ────────────────────────────────────────────

    #[test]
    fn dir_size_bytes_zero_for_missing_dir() {
        assert_eq!(dir_size_bytes(std::path::Path::new("/no/such/dir")), 0);
    }

    #[test]
    fn dir_size_bytes_sums_known_files() {
        use std::io::Write;
        let tmp = tempfile::tempdir().expect("tmpdir");
        let mut f1 =
            std::fs::File::create(tmp.path().join("a.txt")).expect("create a");
        f1.write_all(b"hello").expect("write a");
        let mut f2 =
            std::fs::File::create(tmp.path().join("b.txt")).expect("create b");
        f2.write_all(b"world!").expect("write b");
        assert_eq!(dir_size_bytes(tmp.path()), 11, "5 + 6 = 11 bytes");
    }
}
