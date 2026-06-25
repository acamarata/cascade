// RAG (Retrieval-Augmented Generation) IPC commands.
//
// Why: wraps the daemon's `rag.search` JSON-RPC endpoint and maps the response
// to the frontend's RagResult type. Isolated here because the RAG surface will
// grow in T-P4-E01 when the cascade_rag crate lands.

use serde::{Deserialize, Serialize};
use tauri::State;

use crate::state::AppState;

use super::core::{make_client, RagResult};

// ---------------------------------------------------------------------------
// Private IPC wire types (not exposed to TypeScript)
// ---------------------------------------------------------------------------

/// Params forwarded to the daemon's `rag.search` JSON-RPC method.
///
/// Matches `cascade_daemon::search_handler::RagSearchParams` without requiring
/// cascade-daemon as a direct dependency.
#[derive(Debug, Serialize)]
struct RagSearchIpcParams<'a> {
    query: &'a str,
    project_root: String,
    k: u32,
}

/// Daemon `rag.search` response shape (mirrors `RagSearchResponse`).
///
/// We only deserialize the fields we need for mapping to `RagResult`.
#[derive(Debug, Deserialize)]
struct RagSearchIpcResponse {
    citations: Vec<RagCitationIpc>,
}

/// Minimal citation shape from `cascade_rag::citation::RagCitation` (camelCase).
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RagCitationIpc {
    source_path: String,
    snippet: String,
    rrf_score: f64,
    line_start: usize,
}

// ---------------------------------------------------------------------------
// RAG commands (T-P4-E01-29: wired to daemon rag.search endpoint)
// ---------------------------------------------------------------------------

/// Run a RAG search query against the daemon's indexed knowledge base.
///
/// # Purpose
/// Delegates to the daemon's `rag.search` IPC method (T-P4-E01-29).
/// Returns a mapped `Vec<RagResult>` ordered by descending relevance score.
///
/// # JS
/// `invoke("rag_query", { query, topK })`
///
/// # Inputs
/// - `query`: User search string.
/// - `top_k`: Max results (defaults to 10).
///
/// # Outputs
/// Ordered list of RAG results. Returns empty vec if daemon returns no hits.
#[tauri::command]
pub async fn rag_query(
    query: String,
    top_k: Option<usize>,
    _state: State<'_, AppState>,
) -> Result<Vec<RagResult>, String> {
    let client = make_client().map_err(|e| e.to_string())?;

    // Resolve the project root from the environment (same as other commands).
    let project_root = std::env::var("CASCADE_PROJECT_ROOT")
        .or_else(|_| std::env::current_dir().map(|p| p.display().to_string()))
        .unwrap_or_default();

    let params = RagSearchIpcParams {
        query: &query,
        project_root,
        k: top_k.unwrap_or(10) as u32,
    };

    let resp: RagSearchIpcResponse = client
        .send("rag.search", &params)
        .await
        .map_err(|e| e.to_string())?;

    let results = resp
        .citations
        .into_iter()
        .map(|c| RagResult {
            content: c.snippet,
            score: c.rrf_score as f32,
            source_path: c.source_path,
            chunk_index: c.line_start,
        })
        .collect();

    Ok(results)
}
