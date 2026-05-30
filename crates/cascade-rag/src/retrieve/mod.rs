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

pub mod fts;
pub mod hyde;
pub mod rrf;
pub mod vector;

pub use cascade_types::{RetrievalHit, RetrieveOpts, Retriever};
