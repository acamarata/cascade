// RAG IPC command stubs — T-P4-E01-27
//
// Purpose: Thin Tauri command shims for the RAG Explorer panel.
//   These return empty/placeholder results until T-P4-E01-29 wires the
//   actual cascade_rag::RagEngine calls through the daemon IPC.
//
// Commands exposed (invokeable from JS):
//   - rag_list_sources  → RagListSourcesResult
//   - rag_ingest_file   → ()
//   - rag_index_stats   → RagIndexStatsResult
//
// Note: rag_search is intentionally NOT added here — it is scope of T-P4-E01-29
//       and wires to the daemon search implementation. The JS side falls back to
//       the error state when the command is missing.
//
// SPORT: MASTER-COMMANDS.md — rag_list_sources, rag_ingest_file, rag_index_stats
//   (T-P4-E01-27)

use serde::{Deserialize, Serialize};
use tauri::State;

use crate::state::AppState;

// ── Return types ──────────────────────────────────────────────────────────────

/// Status of a single indexed source.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum SourceStatus {
    Indexed,
    Indexing,
    Error,
    Pending,
}

/// One row in the SourceList table.
#[derive(Debug, Serialize, Deserialize)]
pub struct RagSource {
    pub path: String,
    pub chunk_count: usize,
    pub indexed_at: Option<String>,
    pub status: SourceStatus,
}

/// Response shape for rag_list_sources.
#[derive(Debug, Serialize, Deserialize)]
pub struct RagListSourcesResult {
    pub sources: Vec<RagSource>,
}

/// Response shape for rag_index_stats.
#[derive(Debug, Serialize, Deserialize)]
pub struct RagIndexStatsResult {
    pub total_files: usize,
    pub total_chunks: usize,
    pub index_size_bytes: u64,
    pub last_updated: Option<String>,
}

// ── Commands ──────────────────────────────────────────────────────────────────

/// List all indexed RAG sources.
/// JS: `invoke("rag_list_sources")`
/// TODO(T-P4-E01-29): delegate to RagEngine::list_sources() via daemon IPC.
#[tauri::command]
pub async fn rag_list_sources(_state: State<'_, AppState>) -> Result<RagListSourcesResult, String> {
    // Stub: daemon RAG engine not yet wired. Returns empty list.
    Ok(RagListSourcesResult { sources: vec![] })
}

/// Trigger ingest of a single file into the RAG index.
/// JS: `invoke("rag_ingest_file", { path })`
/// A Tauri event "rag-ingest-progress" is emitted by the daemon as chunks are processed.
/// TODO(T-P4-E01-29): delegate to RagEngine::ingest_file() via daemon IPC.
#[tauri::command]
pub async fn rag_ingest_file(path: String, _state: State<'_, AppState>) -> Result<(), String> {
    // Stub: log the request; real ingest wired in T-P4-E01-29.
    tracing::info!(path = %path, "rag_ingest_file: stub — T-P4-E01-29 wires daemon");
    Ok(())
}

/// Return aggregate statistics about the RAG index.
/// JS: `invoke("rag_index_stats")`
/// TODO(T-P4-E01-29): delegate to RagEngine::index_stats() via daemon IPC.
#[tauri::command]
pub async fn rag_index_stats(_state: State<'_, AppState>) -> Result<RagIndexStatsResult, String> {
    // Stub: returns zeroed stats until daemon is wired.
    Ok(RagIndexStatsResult {
        total_files: 0,
        total_chunks: 0,
        index_size_bytes: 0,
        last_updated: None,
    })
}

/// Search the RAG index (stub).
/// JS: `invoke("rag_search", { query, k })`
/// TODO(T-P4-E01-29): delegate to RagEngine::search() via daemon IPC.
/// Returns an error so the frontend can show the "daemon not wired" message.
#[tauri::command]
pub async fn rag_search(
    query: String,
    _k: Option<usize>,
    _state: State<'_, AppState>,
) -> Result<RagSearchResult, String> {
    tracing::debug!(query = %query, "rag_search: stub — T-P4-E01-29 wires daemon");
    // Return empty results (no error) so UI shows empty state, not error banner.
    Ok(RagSearchResult {
        citations: vec![],
        query_ms: 0,
    })
}

/// A single search result citation.
#[derive(Debug, Serialize, Deserialize)]
pub struct RagCitation {
    pub path: String,
    pub heading_path: String,
    pub snippet: String,
    pub rrf_score: f32,
}

/// Response shape for rag_search.
#[derive(Debug, Serialize, Deserialize)]
pub struct RagSearchResult {
    pub citations: Vec<RagCitation>,
    pub query_ms: u64,
}
