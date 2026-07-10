// RAG IPC commands — T-P4-E01-27 / T-P4-E01-29
//
// Purpose: Thin Tauri command shims for the RAG Explorer panel.
//   Wires each command to the daemon via JSON-RPC 2.0 over the cascade IPC
//   socket (cascade_cli::ipc_client::IpcClient), reusing the same make_client()
//   helper used by all other daemon-bridge commands.
//
// Commands exposed (invokeable from JS):
//   - rag_list_sources(project_root?)          → RagListSourcesResult
//   - rag_ingest_file(path, project_root?)      → RagIngestResult
//   - rag_index_stats(project_root?)            → RagIndexStatsResult
//   - rag_search(query, limit?, project_root?)  → RagSearchResult
//
// Daemon methods called:
//   rag.list_sources  / rag.ingest_file / rag.index_stats / rag.search
//
// Error handling: daemon-down surfaces as CascadeError::DaemonNotRunning
//   (kind = "daemon_not_running") so the frontend can distinguish daemon-down
//   from a real RPC error and show the correct UI state.
//
// SPORT: MASTER-COMMANDS.md — rag_list_sources, rag_ingest_file,
//   rag_index_stats, rag_search (T-P4-E01-27 / T-P4-E01-29)

use serde::{Deserialize, Serialize};
use tauri::State;

use crate::commands::core::make_client;
use crate::error::CascadeError;
use crate::state::AppState;

// ── IPC wire types (private — daemon shapes) ───────────────────────────────────

/// Params for daemon `rag.search`.
#[derive(Debug, Serialize)]
struct RagSearchIpcParams {
    query: String,
    project_root: String,
    k: u32,
}

/// Params for daemon `rag.list_sources`.
#[derive(Debug, Serialize)]
struct RagListSourcesIpcParams {
    project_root: String,
}

/// Params for daemon `rag.index_stats`.
#[derive(Debug, Serialize)]
struct RagIndexStatsIpcParams {
    project_root: String,
}

/// Params for daemon `rag.ingest_file`.
#[derive(Debug, Serialize)]
struct RagIngestFileIpcParams {
    path: String,
    project_root: String,
}

// ── Daemon response deserialization (private) ─────────────────────────────────

/// One citation from `rag.search` response (camelCase on the wire per RagCitation serde).
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RagCitationWire {
    source_path: String,
    heading_path: Option<String>,
    snippet: String,
    rrf_score: f64,
}

/// Raw daemon response for `rag.search`.
#[derive(Debug, Deserialize)]
struct RagSearchIpcResponse {
    citations: Vec<RagCitationWire>,
    duration_ms: u64,
}

/// One source row from `rag.list_sources` (snake_case — matches SourceInfo serialization).
#[derive(Debug, Deserialize)]
struct SourceInfoWire {
    path: String,
    chunk_count: u64,
    indexed_at: Option<i64>,
}

/// Raw daemon response for `rag.list_sources`.
#[derive(Debug, Deserialize)]
struct RagListSourcesIpcResponse {
    sources: Vec<SourceInfoWire>,
}

/// Raw daemon response for `rag.index_stats`.
#[derive(Debug, Deserialize)]
struct RagIndexStatsIpcResponse {
    file_count: u64,
    chunk_count: u64,
    index_size_bytes: u64,
    last_updated: Option<i64>,
}

/// Raw daemon response for `rag.ingest_file`.
#[derive(Debug, Deserialize)]
struct RagIngestFileIpcResponse {
    source_id: i64,
    chunks_created: usize,
    skipped: bool,
}

// ── Public return types (serialized to JS) ────────────────────────────────────

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

/// Response shape for rag_ingest_file.
#[derive(Debug, Serialize, Deserialize)]
pub struct RagIngestResult {
    pub source_id: i64,
    pub chunks_created: usize,
    pub skipped: bool,
}

// ── Helper — resolve project_root ─────────────────────────────────────────────

/// Return the caller-supplied project_root or fall back to CASCADE_PROJECT_ROOT
/// env var, then current working directory.
fn resolve_project_root(project_root: Option<String>) -> String {
    project_root
        .filter(|s| !s.is_empty())
        .or_else(|| std::env::var("CASCADE_PROJECT_ROOT").ok())
        .or_else(|| {
            std::env::current_dir()
                .ok()
                .map(|p| p.display().to_string())
        })
        .unwrap_or_default()
}

// ── Commands ──────────────────────────────────────────────────────────────────

/// List all indexed RAG sources.
/// JS: `invoke("rag_list_sources", { projectRoot? })`
///
/// Delegates to daemon `rag.list_sources`. Returns CascadeError::DaemonNotRunning
/// if the daemon is not up.
#[tauri::command]
pub async fn rag_list_sources(
    project_root: Option<String>,
    _state: State<'_, AppState>,
) -> Result<RagListSourcesResult, CascadeError> {
    let client = make_client()?;
    let root = resolve_project_root(project_root);
    let resp: RagListSourcesIpcResponse = client
        .send(
            "rag.list_sources",
            &RagListSourcesIpcParams { project_root: root },
        )
        .await
        .map_err(CascadeError::from)?;

    let sources = resp
        .sources
        .into_iter()
        .map(|s| RagSource {
            path: s.path,
            chunk_count: s.chunk_count as usize,
            // Unix timestamp as numeric string; JS: new Date(Number(ts) * 1000).
            indexed_at: s.indexed_at.map(|ts| ts.to_string()),
            status: SourceStatus::Indexed,
        })
        .collect();

    Ok(RagListSourcesResult { sources })
}

/// Trigger ingest of a single file into the RAG index.
/// JS: `invoke("rag_ingest_file", { path, projectRoot? })`
///
/// Delegates to daemon `rag.ingest_file`. The daemon handles chunking,
/// embedding, and deduplication (skipped: true if content hash is unchanged).
/// Returns CascadeError::DaemonNotRunning if the daemon is not up.
#[tauri::command]
pub async fn rag_ingest_file(
    path: String,
    project_root: Option<String>,
    _state: State<'_, AppState>,
) -> Result<RagIngestResult, CascadeError> {
    let client = make_client()?;
    let root = resolve_project_root(project_root);
    tracing::info!(path = %path, project_root = %root, "rag_ingest_file: delegating to daemon");
    let resp: RagIngestFileIpcResponse = client
        .send(
            "rag.ingest_file",
            &RagIngestFileIpcParams {
                path,
                project_root: root,
            },
        )
        .await
        .map_err(CascadeError::from)?;

    Ok(RagIngestResult {
        source_id: resp.source_id,
        chunks_created: resp.chunks_created,
        skipped: resp.skipped,
    })
}

/// Return aggregate statistics about the RAG index.
/// JS: `invoke("rag_index_stats", { projectRoot? })`
///
/// Delegates to daemon `rag.index_stats`.
/// Returns CascadeError::DaemonNotRunning if the daemon is not up.
#[tauri::command]
pub async fn rag_index_stats(
    project_root: Option<String>,
    _state: State<'_, AppState>,
) -> Result<RagIndexStatsResult, CascadeError> {
    let client = make_client()?;
    let root = resolve_project_root(project_root);
    let resp: RagIndexStatsIpcResponse = client
        .send(
            "rag.index_stats",
            &RagIndexStatsIpcParams { project_root: root },
        )
        .await
        .map_err(CascadeError::from)?;

    Ok(RagIndexStatsResult {
        total_files: resp.file_count as usize,
        total_chunks: resp.chunk_count as usize,
        index_size_bytes: resp.index_size_bytes,
        last_updated: resp.last_updated.map(|ts| ts.to_string()),
    })
}

/// Search the RAG index with a natural language query.
/// JS: `invoke("rag_search", { query, limit?, projectRoot? })`
///
/// Delegates to daemon `rag.search`. Returns CascadeError::DaemonNotRunning
/// if the daemon is not up — the frontend shows daemon-down error state, not
/// an empty-results state.
#[tauri::command]
pub async fn rag_search(
    query: String,
    limit: Option<usize>,
    project_root: Option<String>,
    _state: State<'_, AppState>,
) -> Result<RagSearchResult, CascadeError> {
    let client = make_client()?;
    let root = resolve_project_root(project_root);
    tracing::debug!(query = %query, k = ?limit, "rag_search: delegating to daemon");
    let resp: RagSearchIpcResponse = client
        .send(
            "rag.search",
            &RagSearchIpcParams {
                query,
                project_root: root,
                k: limit.unwrap_or(10) as u32,
            },
        )
        .await
        .map_err(CascadeError::from)?;

    let citations = resp
        .citations
        .into_iter()
        .map(|c| RagCitation {
            path: c.source_path,
            heading_path: c.heading_path.unwrap_or_default(),
            snippet: c.snippet,
            rrf_score: c.rrf_score as f32,
        })
        .collect();

    Ok(RagSearchResult {
        citations,
        query_ms: resp.duration_ms,
    })
}

// ── Unit tests ────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── resolve_project_root ─────────────────────────────────────────────────

    #[test]
    fn resolve_project_root_uses_explicit_value() {
        let result = resolve_project_root(Some("/some/project".to_string()));
        assert_eq!(result, "/some/project");
    }

    #[test]
    fn resolve_project_root_ignores_empty_string() {
        // Empty string falls through to env. Set, check, restore.
        std::env::set_var("CASCADE_PROJECT_ROOT", "/env/project");
        let result = resolve_project_root(Some("".to_string()));
        std::env::remove_var("CASCADE_PROJECT_ROOT");
        assert_eq!(result, "/env/project");
    }

    #[test]
    fn resolve_project_root_falls_back_to_env() {
        std::env::set_var("CASCADE_PROJECT_ROOT", "/env/root");
        let result = resolve_project_root(None);
        std::env::remove_var("CASCADE_PROJECT_ROOT");
        assert_eq!(result, "/env/root");
    }

    // ── IPC param serialization ──────────────────────────────────────────────

    #[test]
    fn rag_search_params_serialize_correctly() {
        let params = RagSearchIpcParams {
            query: "hello world".to_string(),
            project_root: "/tmp/proj".to_string(),
            k: 5,
        };
        let json = serde_json::to_value(&params).unwrap();
        assert_eq!(json["query"], "hello world");
        assert_eq!(json["project_root"], "/tmp/proj");
        assert_eq!(json["k"], 5);
    }

    #[test]
    fn rag_ingest_params_serialize_correctly() {
        let params = RagIngestFileIpcParams {
            path: "/some/file.md".to_string(),
            project_root: "/tmp/proj".to_string(),
        };
        let json = serde_json::to_value(&params).unwrap();
        assert_eq!(json["path"], "/some/file.md");
        assert_eq!(json["project_root"], "/tmp/proj");
    }

    // ── Daemon response deserialization ──────────────────────────────────────

    #[test]
    fn rag_search_response_deserializes_camel_case() {
        let raw = serde_json::json!({
            "citations": [
                {
                    "chunkId": 1,
                    "sourcePath": "/proj/README.md",
                    "lineStart": 10,
                    "lineEnd": 20,
                    "headingPath": "## Intro",
                    "snippet": "Some text here",
                    "rrfScore": 0.87,
                    "denseScore": null
                }
            ],
            "duration_ms": 42
        });
        let resp: RagSearchIpcResponse = serde_json::from_value(raw).unwrap();
        assert_eq!(resp.citations.len(), 1);
        assert_eq!(resp.citations[0].source_path, "/proj/README.md");
        assert_eq!(resp.citations[0].snippet, "Some text here");
        assert!((resp.citations[0].rrf_score - 0.87).abs() < 1e-6);
        assert_eq!(resp.duration_ms, 42);
    }

    #[test]
    fn rag_index_stats_response_deserializes() {
        let raw = serde_json::json!({
            "file_count": 7,
            "chunk_count": 312,
            "index_size_bytes": 102400,
            "last_updated": 1700000000_i64
        });
        let resp: RagIndexStatsIpcResponse = serde_json::from_value(raw).unwrap();
        assert_eq!(resp.file_count, 7);
        assert_eq!(resp.chunk_count, 312);
        assert_eq!(resp.index_size_bytes, 102400);
        assert_eq!(resp.last_updated, Some(1_700_000_000));
    }

    #[test]
    fn rag_list_sources_response_deserializes() {
        let raw = serde_json::json!({
            "sources": [
                { "path": "/proj/CLAUDE.md", "chunk_count": 14, "indexed_at": 1700000000_i64, "tier": "gci" }
            ]
        });
        let resp: RagListSourcesIpcResponse = serde_json::from_value(raw).unwrap();
        assert_eq!(resp.sources.len(), 1);
        assert_eq!(resp.sources[0].path, "/proj/CLAUDE.md");
        assert_eq!(resp.sources[0].chunk_count, 14);
    }

    #[test]
    fn rag_ingest_response_deserializes() {
        let raw = serde_json::json!({
            "source_id": 3,
            "chunks_created": 8,
            "skipped": false
        });
        let resp: RagIngestFileIpcResponse = serde_json::from_value(raw).unwrap();
        assert_eq!(resp.source_id, 3);
        assert_eq!(resp.chunks_created, 8);
        assert!(!resp.skipped);
    }

    // ── daemon-down error mapping ─────────────────────────────────────────────
    // make_client() reads ~/.cascade/ipc_token at runtime; untestable here.
    // We verify the From<IpcClientError> → CascadeError conversion the commands use.

    #[test]
    fn ipc_token_not_found_maps_to_daemon_not_running() {
        use cascade_cli::ipc_client::IpcClientError;
        let err = CascadeError::from(IpcClientError::TokenNotFound);
        let json = serde_json::to_value(&err).unwrap();
        assert_eq!(json["kind"], "daemon_not_running");
    }

    #[test]
    fn ipc_daemon_not_running_maps_correctly() {
        use cascade_cli::ipc_client::IpcClientError;
        let err = CascadeError::from(IpcClientError::DaemonNotRunning);
        let json = serde_json::to_value(&err).unwrap();
        assert_eq!(json["kind"], "daemon_not_running");
    }
}
