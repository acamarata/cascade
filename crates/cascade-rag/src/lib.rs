//! # cascade-rag
//!
//! Retrieval-Augmented Generation pipeline for Cascade.
//!
//! This crate implements the full RAG stack on top of SQLite (FTS5 full-text
//! search + sqlite-vec dense vector search), local ONNX model inference via
//! fastembed-rs, and a composable chunking / retrieval / reranking pipeline.
//!
//! ## Architecture
//!
//! ```text
//! Document (file on disk)
//!   → Parser       (src/parse/)   — format-specific text extraction
//!   → Chunker      (src/chunk/)   — split into indexed units
//!   → EmbedProvider(src/embed/)   — dense + optional sparse vectors
//!   → Index        (src/index.rs) — FTS5 + sqlite-vec storage
//!
//! Query (user string)
//!   → QueryStrategy (src/retrieve/hyde.rs + …) — optional expansion
//!   → Retriever     (src/retrieve/)             — FTS5 / vector / RRF
//!   → Reranker      (src/rerank/)               — optional cross-encoder
//!   → Citation      (src/citation.rs)           — provenance annotation
//! ```
//!
//! ## Feature flags
//!
//! | Flag | What it enables |
//! |------|-----------------|
//! | `fastembed`    | BGE-M3, Nomic, Jina local ONNX inference via fastembed-rs |
//! | `vec`          | sqlite-vec vec0 virtual table for dense vector search |
//! | `code-chunker` | tree-sitter code-aware chunking for Rust/TS/Python/JS |
//!
//! ## Design constraints
//!
//! - No LangChain, LangGraph, AWS, or Bedrock dependencies anywhere.
//! - All trait implementations satisfy the `cascade-types` contracts.
//! - `Send + Sync` throughout; safe to use inside `tokio::spawn`.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag (crate root)

// ── Module declarations ───────────────────────────────────────────────────────

pub mod agentic;
pub mod cache;
pub mod chunk;
pub mod citation;
pub mod context;
pub mod db;
pub mod embed;
pub mod eval;
pub mod feedback;
pub mod index;
pub mod index_manager;
pub mod ingest;
pub mod parse;
pub mod privacy;
pub mod rerank;
pub mod retrieve;
pub mod search;
pub mod study;
pub mod workers;

// Feature-gated placeholder modules — logic wired in subsequent tickets
#[cfg(feature = "vec")]
mod vec_index;

#[cfg(feature = "code-chunker")]
mod code_chunk;

// ── Tier configuration ───────────────────────────────────────────────────────

use serde::{Deserialize, Serialize};

/// The active RAG tier, controlling which retrieval engines are enabled.
///
/// Higher tiers are strictly more capable but require more disk space and RAM.
/// The tier is set in the project's `cascade.toml` and can be changed without
/// re-indexing (the index stores all tiers; the tier filter is query-time only).
///
/// SPORT: MASTER-LIBS.md → cascade-rag::TierLevel
#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Serialize, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum TierLevel {
    /// FTS5 BM25 keyword search only. No embeddings required.
    /// Index size: ~text size. RAM: negligible.
    #[default]
    Minimal,

    /// Hybrid FTS5 + BGE-M3 dense vectors via RRF. Default for most users.
    /// Index size: ~text + 4KB/chunk. RAM: ~500 MB during indexing.
    Semantic,

    /// Semantic + cross-encoder reranking (bge-reranker-v2-m3).
    /// Adds ~200 ms latency per query for improved precision.
    Reranker,

    /// Semantic + multi-vector ColBERT-style scoring.
    /// Index size: ~5× baseline. Enable with explicit config flag.
    MultiVector,
}

impl TierLevel {
    /// Returns `true` if dense vector search is required at this tier.
    pub fn requires_embeddings(self) -> bool {
        self >= TierLevel::Semantic
    }

    /// Returns `true` if a reranker must be present at this tier.
    pub fn requires_reranker(self) -> bool {
        self >= TierLevel::Reranker
    }
}

// ── Re-exports ────────────────────────────────────────────────────────────────

pub use cache::{
    CachedEmbedModel, ChunkCache, EmbedCache, EmbedCacheError, InMemoryEmbedCache,
    LegacyQueryCache, QueryCache,
};
pub use citation::{citations_from_chunk_ids, Citation, CitationSet, RagCitation};
pub use context::{ContextOptimizer, ContextResult};
pub use db::run_migrations;
pub use eval::{
    EvalConfig, EvalHarness, EvalMetrics, EvalQuery, EvalReport, GroundTruth, QueryResult, SearchFn,
};
pub use index::sharding::{
    shard_for, EmbedResult as ShardEmbedResult, SearchHit as ShardSearchHit, ShardedIndex,
};
pub use index::state::{ChangeKind, IndexStateStore};
pub use index::{CachedIndex, RagIndex};
pub use index_manager::{resolve_db_path, IndexManager, IndexRegistry, SourceInfo};
pub use ingest::{IngestConfig, IngestPipeline, IngestResult, IngestStats};
pub use parse::{DocumentParser, DocumentText, ParseDispatcher};
pub use retrieve::exclusion::{ExclusionConfig, ExclusionSet};
pub use retrieve::rrf::{NormStrategy, RrfRetriever};
pub use search::{
    route_query, search, weights_for_kind, HydeLlm, QueryKind, RerankerModelConfig,
    RoutingWeights, SearchConfig,
};
pub use privacy::{RedactionConfig, RedactionPipeline};
pub use workers::{EmbedResult, RawDoc, WorkerPool, WorkerPoolConfig};

// ── rag-09 exports ────────────────────────────────────────────────────────────
pub use agentic::{agentic_retrieve, AdequacyAssessor, AgenticConfig, AgenticResult};
pub use feedback::{
    ensure_feedback_schema, ingest_feedback, signals_for_query, FeedbackSignal, SignalKind,
};
pub use retrieve::multi_query::{MultiQueryStrategy, QueryExpansionLlm, StepBackStrategy};
pub use study::{
    classify_structural, code_graph_query, GraphQueryResult, GraphRaw, StructuralIntent,
};
