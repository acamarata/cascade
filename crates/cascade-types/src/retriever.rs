//! Retriever trait and associated types.
//!
//! A `Retriever` accepts a natural-language query and returns a ranked list of
//! `RetrievalHit`s from an index. Implementations may use vector search, BM25,
//! hybrid RRF, or any other strategy — the trait is strategy-agnostic.

use crate::error::Result;
use async_trait::async_trait;
use serde::{Deserialize, Serialize};

// ── RetrievalHit ──────────────────────────────────────────────────────────────

/// A single result returned by a [`Retriever`].
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetrievalHit {
    /// Unique identifier of the source chunk (matches `Chunk::id`).
    pub chunk_id: String,

    /// The text content of the matching chunk.
    pub text: String,

    /// Absolute path to the file that contains this chunk.
    /// `None` for in-memory or synthetic documents.
    pub file_path: Option<std::path::PathBuf>,

    /// 1-based line number in the source file where this chunk starts.
    /// `None` if line-level provenance is not available.
    pub start_line: Option<usize>,

    /// 1-based line number where this chunk ends.
    pub end_line: Option<usize>,

    /// Raw similarity or relevance score from the underlying index.
    ///
    /// The scale is implementation-defined (cosine distance, BM25 score,
    /// RRF combined score, etc.). Use `rank` for stable comparisons.
    pub score: f32,

    /// 0-based rank within this result set. `rank == 0` is the most relevant.
    pub rank: usize,

    /// Tier label indicating which cascade tier owns the source document,
    /// if known (e.g. `"gci"`, `"prc"`).
    pub tier: Option<String>,
}

impl RetrievalHit {
    /// Returns `true` if this hit has a source file path.
    pub fn has_file(&self) -> bool {
        self.file_path.is_some()
    }
}

// ── RetrieveOpts ─────────────────────────────────────────────────────────────

/// Parameters passed to [`Retriever::retrieve`].
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetrieveOpts {
    /// Maximum number of hits to return.
    pub k: usize,

    /// Minimum score threshold. Hits below this value are excluded.
    /// `None` means no threshold.
    pub min_score: Option<f32>,

    /// If set, only return hits from the specified cascade tier.
    pub tier_filter: Option<String>,
}

impl Default for RetrieveOpts {
    fn default() -> Self {
        Self {
            k: 10,
            min_score: None,
            tier_filter: None,
        }
    }
}

// ── Trait ─────────────────────────────────────────────────────────────────────

/// Returns ranked chunks from an index for a given query string.
///
/// Retriever implementations are responsible for their own index management
/// (building, updating, persisting). The trait covers only the query path.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_types::retriever::{Retriever, RetrieveOpts, RetrievalHit};
/// # use cascade_types::error::Result;
/// # use async_trait::async_trait;
/// struct VectorRetriever;
///
/// #[async_trait]
/// impl Retriever for VectorRetriever {
///     async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
///         // embed query, search index, return top-k …
///         Ok(vec![])
///     }
/// }
/// ```
#[async_trait]
pub trait Retriever: Send + Sync {
    /// Retrieve the top hits for `query`.
    ///
    /// The returned vector is ordered by relevance (most relevant first).
    /// Its length is at most `opts.k`.
    async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>>;

    /// Human-readable name for this retriever (used in logging and `cascade status`).
    fn name(&self) -> &str {
        "unknown-retriever"
    }

    /// Returns `true` if the underlying index is ready to serve queries.
    ///
    /// `cascade status` calls this method to surface index health.
    async fn is_ready(&self) -> bool {
        true
    }
}

// ── No-op implementation (for tests) ─────────────────────────────────────────

/// A retriever that always returns an empty result set.
///
/// Used in unit tests and as a placeholder when no index is configured.
#[derive(Debug, Default)]
pub struct NoopRetriever;

#[async_trait]
impl Retriever for NoopRetriever {
    async fn retrieve(&self, _query: &str, _opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        Ok(vec![])
    }

    fn name(&self) -> &str {
        "noop"
    }
}

// ── Object-safety check ───────────────────────────────────────────────────────

fn _assert_object_safe(_: &dyn Retriever) {}
