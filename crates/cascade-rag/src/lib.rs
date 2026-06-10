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

pub mod cache;
pub mod chunk;
pub mod citation;
pub mod context;
pub mod db;
pub mod embed;
pub mod eval;
pub mod index;
pub mod index_manager;
pub mod ingest;
pub mod parse;
pub mod rerank;
pub mod retrieve;
pub mod search;
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

pub use cache::{CachedEmbedModel, ChunkCache, EmbedCache, EmbedCacheError, LegacyQueryCache, QueryCache};
pub use context::{ContextOptimizer, ContextResult};
pub use workers::{EmbedResult, RawDoc, WorkerPool, WorkerPoolConfig};
pub use index_manager::{IndexManager, IndexRegistry, SourceInfo, resolve_db_path};
pub use citation::{Citation, CitationSet, RagCitation, citations_from_chunk_ids};
pub use eval::{EvalConfig, EvalHarness, EvalMetrics, EvalQuery, EvalReport, GroundTruth, QueryResult, SearchFn};
pub use index::{CachedIndex, RagIndex};
pub use index::sharding::{EmbedResult as ShardEmbedResult, SearchHit as ShardSearchHit, ShardedIndex, shard_for};
pub use parse::{DocumentParser, DocumentText, ParseDispatcher};
pub use retrieve::rrf::RrfRetriever;
pub use search::{search, SearchConfig};
pub use ingest::{IngestPipeline, IngestConfig, IngestResult, IngestStats};
pub use index::state::{IndexStateStore, ChangeKind};
pub use db::run_migrations;
