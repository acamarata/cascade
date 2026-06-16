//! Retrieval strategy implementations.
//!
//! All implementations satisfy the [`cascade_types::Retriever`] trait.
//! The active strategy is selected from the project's `cascade.toml`:
//!
//! ```toml
//! [rag]
//! retrieval_strategy = "hybrid_rrf"  # default
//! # retrieval_strategy = "pure_fts"
//! # retrieval_strategy = "pure_vec"
//! ```
//!
//! ## Strategy overview
//!
//! | Strategy | Description |
//! |----------|-------------|
//! | [`fts::FtsRetriever`] | BM25 keyword search via FTS5 |
//! | [`vector::VectorRetriever`] | ANN cosine search via sqlite-vec |
//! | [`rrf::RrfRetriever`] | Hybrid: FTS5 + dense vectors fused with RRF |
//! | [`hyde::HydeRetriever`] | Hypothetical Document Embeddings query expansion |
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve

pub mod curated;
pub mod fts;
pub mod hyde;
pub mod recency;
pub mod rrf;
pub mod vector;

/// Multi-vector (ColBERT-style) MaxSim retriever.
///
/// Gated behind the `rag-multivec` feature (OFF by default). When enabled,
/// adds a fourth retrieval channel to the RRF pipeline using BGE-M3
/// per-token late-interaction scoring.
#[cfg(feature = "rag-multivec")]
pub mod multivec;

pub use cascade_types::{RetrievalHit, RetrieveOpts, Retriever};
