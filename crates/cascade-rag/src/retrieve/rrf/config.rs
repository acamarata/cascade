//! [`RrfConfig`] — configuration for the RRF fusion step.

/// Default RRF smoothing constant.  60 is the canonical default from the
/// original Cormack et al. 2009 paper.
pub(super) const DEFAULT_K: f64 = 60.0;

/// Configuration for the RRF fusion step.
///
/// Up to 5 retrieval channels may contribute:
///
/// | Channel | Flag | Weight field | Default weight |
/// |---------|------|-------------|----------------|
/// | FTS5 BM25 body | `use_fts` | `weight_fts` | 1.0 |
/// | Dense ANN | `use_vec` | `weight_vec` | 1.0 |
/// | Curated description/title/tags | `use_curated` | `weight_curated` | 0.8 |
/// | Document recency | `use_recency` | `weight_recency` | 0.5 |
/// | Multi-vec ColBERT | `use_multivec` | (same as vec, 1.0) | 1.0 |
///
/// The description and recency channels must not dominate — their weights are
/// intentionally below 1.0 so they nudge results without overriding FTS/dense
/// relevance.  Set a weight to `0.0` to effectively disable a channel at
/// runtime (equivalent to setting its flag to `false`, but without re-allocating
/// the retriever).
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct RrfConfig {
    /// RRF smoothing constant.  Larger values reduce the influence of top-1
    /// results relative to the rest of the list.
    pub k: f32,
    /// Whether to use FTS5 BM25 body-text results.
    pub use_fts: bool,
    /// Whether to use dense vector ANN results.
    pub use_vec: bool,
    /// Whether to use the curated description/title/tags FTS channel.
    ///
    /// Requires migration 0009 (`rag_chunk_meta` table).  Gracefully degrades
    /// to empty results when the table is absent or empty.
    pub use_curated: bool,
    /// Whether to use document recency as an RRF channel.
    ///
    /// Ranks candidates by `rag_sources.mtime` (falling back to `indexed_at`).
    /// Query-independent — same top-K candidates for every query.
    pub use_recency: bool,
    /// Whether to use sparse token-overlap results (reserved for P5).
    pub use_sparse: bool,
    /// Weight applied to FTS5 body-text contributions.  `1.0` = neutral.
    pub weight_fts: f64,
    /// Weight applied to dense ANN contributions.  `1.0` = neutral.
    pub weight_vec: f64,
    /// Weight applied to curated description/title/tags contributions.
    ///
    /// Default `0.8`: meaningful boost when description matches, but body FTS
    /// and dense remain dominant for on-topic queries.
    pub weight_curated: f64,
    /// Weight applied to recency contributions.
    ///
    /// Default `0.5`: only nudges ties; never overrides a strong relevance gap.
    pub weight_recency: f64,
    /// Whether to use multi-vector (ColBERT-style MaxSim) as an optional
    /// RRF channel.  Only effective when compiled with `rag-multivec` feature
    /// and `cascade.toml [rag] multi_vec = true`.  OFF by default.
    ///
    /// Disk usage: +3-4x the dense embedding table (rag_token_embeddings).
    #[cfg(feature = "rag-multivec")]
    pub use_multivec: bool,
}

impl Default for RrfConfig {
    fn default() -> Self {
        Self {
            k: DEFAULT_K as f32,
            use_fts: true,
            use_vec: true,
            use_curated: true,
            use_recency: true,
            use_sparse: false,
            weight_fts: 1.0,
            weight_vec: 1.0,
            weight_curated: 0.8,
            weight_recency: 0.5,
            #[cfg(feature = "rag-multivec")]
            use_multivec: false,
        }
    }
}
