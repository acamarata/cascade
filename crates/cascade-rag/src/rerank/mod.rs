//! Reranker implementations.
//!
//! All implementations satisfy the [`cascade_types::Reranker`] trait.
//! Reranking is an optional pipeline stage (requires `TierLevel::Reranker` or
//! higher) that re-scores the top-N candidates from the retrieval step using a
//! cross-encoder model for higher precision.
//!
//! ## Cross-encoder vs bi-encoder
//!
//! Retrieval (vector search) uses a bi-encoder: query and document are embedded
//! separately and compared with cosine similarity.  This is fast but imprecise
//! for subtle relevance differences.
//!
//! Rerankers use a cross-encoder: query and document are concatenated and scored
//! jointly.  This is 10–100x slower but significantly more accurate.  The
//! typical pipeline is:
//!
//! ```text
//! Retrieval (RRF top-50)  →  Reranker  →  Final top-K
//! ```
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::rerank

pub mod bge;

pub use cascade_types::{NoopReranker, RerankOpts, RerankResult, Reranker};
