//! Purpose: Shared RAG/RRF status response types for the cascade daemon HTTP API.
//! Inputs:  None (pure type definitions — no runtime inputs).
//! Outputs: RagStatusResponse serializes to/from JSON for GET /api/gci/rag-status.
//! Constraints:
//!   - Fields must be forward-compatible with P4's real RAG pipeline data; no field
//!     should need to be renamed or type-changed when P4 fills in real values.
//!   - serving=false + index_count=0 is the canonical P3 stub state.
//!   - last_indexed is Option<String> (ISO 8601) so absence of an index is explicit.
//! SPORT: T-P3-E02-29 — MASTER-ROUTES.md GET /api/gci/rag-status

use serde::{Deserialize, Serialize};

/// RAG/RRF serve status returned by GET /api/gci/rag-status.
///
/// P3 stub: serving=false, index_count=0, index_size_bytes=0, last_indexed=None.
/// P4 will populate real values from the BGE-M3 + FTS5 + sqlite-vec pipeline.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct RagStatusResponse {
    /// Whether the RAG/RRF context-serving engine is running with a loaded index.
    /// false in P3 (no index exists yet); true once P4 builds and loads one.
    pub serving: bool,

    /// Number of indexed documents in the active RAG index.
    /// 0 in P3; populated by P4 indexing pipeline.
    pub index_count: u32,

    /// ISO 8601 UTC timestamp of the last successful index build, or None if never indexed.
    pub last_indexed: Option<String>,

    /// Size of the on-disk RAG index in bytes.
    /// 0 in P3; populated by P4.
    pub index_size_bytes: u64,

    /// The HTTP endpoint the RAG serve layer listens on.
    /// P3 stub returns the planned P4 address; real binding confirmed in P4.
    pub serve_endpoint: String,
}
