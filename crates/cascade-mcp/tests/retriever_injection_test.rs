//! Integration test: live retriever injection into ToolRegistry.
//!
//! Verifies E11.1: when a real `RrfRetriever::fts_only` is attached to a
//! `ToolRegistry`, `cascade.search` returns genuine index hits instead of the
//! graceful "index not ready" fallback.
//!
//! ## Test outline
//!
//! 1. Open a `RagIndex` in a temp dir.
//! 2. Insert three fixture chunks via `RagIndex::upsert_chunk`.
//! 3. Build `RrfRetriever::fts_only(Arc<RagIndex>)`.
//! 4. Construct `ToolRegistry::new().with_retriever(Arc<dyn Retriever>)`.
//! 5. Call the `cascade.search` handler via `ToolRegistry::call`.
//! 6. Assert the response is `ready: true` with at least one hit.

use std::sync::Arc;

use cascade_mcp::tool::ToolRegistry;
use cascade_rag::index::RagIndex;
use cascade_rag::retrieve::rrf::RrfRetriever;
use tempfile::TempDir;

// ── Helper ─────────────────────────────────────────────────────────────────────

/// Open a fresh `RagIndex` in `dir` and ingest three fixture chunks.
async fn seeded_index(dir: &TempDir) -> Arc<RagIndex> {
    let db_path = dir.path().join("cascade.db");
    let idx = Arc::new(
        RagIndex::open(&db_path)
            .await
            .expect("RagIndex::open should succeed"),
    );

    // Three fixture documents covering different keyword spaces.
    let chunks = [
        (
            "chunk-hijri-001",
            "The Hijri calendar is a lunar calendar used in Islamic observance.",
        ),
        (
            "chunk-prayer-001",
            "Prayer times are calculated using solar position algorithms.",
        ),
        (
            "chunk-qibla-001",
            "The Qibla direction points towards the Kaaba in Mecca.",
        ),
    ];

    for (id, text) in &chunks {
        idx.upsert_chunk(id, None, None, None, text, None)
            .await
            .unwrap_or_else(|e| panic!("upsert_chunk({id}) failed: {e}"));
    }

    idx
}

// ── Tests ──────────────────────────────────────────────────────────────────────

/// When a real retriever is injected, `cascade.search` returns `ready: true`
/// and at least one matching hit for a query that exists in the index.
#[tokio::test]
async fn search_returns_live_hits_when_retriever_injected() {
    let dir = TempDir::new().expect("tempdir");
    let idx = seeded_index(&dir).await;

    // E11.1: FTS-only retriever (no embedder required).
    let retriever = Arc::new(RrfRetriever::fts_only(Arc::clone(&idx)));
    let registry = ToolRegistry::new().with_retriever(retriever);

    let params = serde_json::json!({
        "name": "cascade.search",
        "arguments": {
            "query": "Hijri calendar lunar",
            "limit": 5
        }
    });

    let result = registry
        .call(&params)
        .await
        .expect("cascade.search should return Ok");

    // `ready: true` confirms the live path was taken (not the fallback).
    let ready = result
        .get("metadata")
        .and_then(|m| m.get("ready"))
        .and_then(|v| v.as_bool())
        .expect("metadata.ready must be present");
    assert!(ready, "metadata.ready should be true when retriever is injected");

    // At least one hit for a query matching the seeded fixture text.
    let hits_returned = result
        .get("metadata")
        .and_then(|m| m.get("hits_returned"))
        .and_then(|v| v.as_u64())
        .expect("metadata.hits_returned must be present");
    assert!(
        hits_returned >= 1,
        "expected at least 1 hit for 'Hijri calendar lunar', got {hits_returned}"
    );
}

/// When NO retriever is attached, `cascade.search` returns `ready: false` and
/// the "index not ready" message (graceful degradation path — not a regression
/// check for E11.1 behaviour, but verifies the baseline still holds).
#[tokio::test]
async fn search_returns_not_ready_without_retriever() {
    let registry = ToolRegistry::new(); // no retriever

    let params = serde_json::json!({
        "name": "cascade.search",
        "arguments": { "query": "test query" }
    });

    let result = registry
        .call(&params)
        .await
        .expect("cascade.search should return Ok even without retriever");

    let ready = result
        .get("metadata")
        .and_then(|m| m.get("ready"))
        .and_then(|v| v.as_bool())
        .expect("metadata.ready must be present");
    assert!(!ready, "metadata.ready should be false without a retriever");
}

/// Retriever can be swapped via `with_retriever` — verifies the builder chain
/// works on ToolRegistry (SEAM test, not an E11.1 functional test).
#[tokio::test]
async fn with_retriever_is_chainable() {
    let dir = TempDir::new().expect("tempdir");
    let idx = seeded_index(&dir).await;
    let retriever = Arc::new(RrfRetriever::fts_only(idx));

    // Building and calling should not panic.
    let registry = ToolRegistry::new().with_retriever(retriever);
    let params = serde_json::json!({
        "name": "cascade.search",
        "arguments": { "query": "prayer times solar" }
    });
    let result = registry.call(&params).await.expect("call should succeed");
    let ready = result
        .get("metadata")
        .and_then(|m| m.get("ready"))
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    assert!(ready, "retriever should be active after with_retriever chain");
}
