//! Hybrid Reciprocal Rank Fusion (RRF) retriever.
//!
//! Combines FTS5 BM25 keyword results and sqlite-vec dense ANN results using
//! Reciprocal Rank Fusion.  RRF is rank-based (not score-based), which makes
//! it robust to score distribution differences between retrieval engines.
//!
//! ## RRF formula (S07-01)
//!
//! ```text
//! rrf_score(d) = Σ_i  1 / (k + rank_i(d))
//! ```
//!
//! where `k = 60` (configurable), `rank_i(d)` is the 1-based rank of document
//! `d` in the i-th result list, and the sum is over all retrieval engines.
//! Documents not appearing in a result list are treated as having rank = ∞
//! (contributing 0 to the sum).
//!
//! ## Performance target (S07-06)
//!
//! Hybrid query p95 < 200 ms at K=10 on a 100K-chunk index.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::retrieve::RrfRetriever

use async_trait::async_trait;
use std::collections::HashMap;
use std::sync::Arc;
use tracing::instrument;

use cascade_types::{
    error::Result,
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
    EmbeddingProvider,
};

use super::fts::FtsRetriever;
use super::vector::VectorRetriever;
use crate::index::RagIndex;

/// Default RRF smoothing constant.  60 is the canonical default from the
/// original Cormack et al. 2009 paper.
const DEFAULT_K: f64 = 60.0;

// ── Pure RRF merger (no I/O) ─────────────────────────────────────────────────

/// A single entry in an input ranked list for [`rrf_merge`].
///
/// The list must already be **sorted highest-score-first** — `rrf_merge`
/// converts position to 1-based rank internally.
#[derive(Debug, Clone)]
pub struct RankedList<'a> {
    /// Human-readable label used for per-source provenance (e.g. `"fts5"`,
    /// `"dense"`, `"sparse"`).
    pub source: &'a str,
    /// Optional weight multiplier applied to every RRF contribution from this
    /// list.  `1.0` is neutral (standard RRF).  Higher values boost the list's
    /// influence; `0.0` disables it entirely.
    pub weight: f64,
    /// Pre-ranked `(chunk_id, score)` pairs, descending by score.
    pub hits: &'a [(i64, f64)],
}

/// Output of [`rrf_merge`] — one fused entry per unique `chunk_id`.
#[derive(Debug, Clone, PartialEq)]
pub struct FusedHit {
    /// The chunk identifier.
    pub chunk_id: i64,
    /// Weighted RRF score: Σ_i  weight_i / (k + rank_i(d)).
    pub rrf_score: f64,
    /// Names of the source lists that contributed to this score (for citation).
    pub sources_hit: Vec<String>,
}

/// Pure RRF merger.
///
/// # Parameters
///
/// * `lists` — up to N pre-ranked input lists, each with an optional weight.
/// * `k`     — RRF smoothing constant (default 60.0).
/// * `top_n` — maximum results to return; `0` means return all.
///
/// # Algorithm
///
/// For each list `i` and each document `d` at 1-based rank `r`:
///
/// ```text
/// rrf_score(d) += weight_i / (k + r)
/// ```
///
/// Documents not present in a list contribute 0.
///
/// ## Tie-breaking
///
/// Ties are broken deterministically by `chunk_id` ascending, giving a stable
/// ordering across calls with identical inputs.
///
/// ## Empty / single-list behaviour
///
/// * Empty list entries are silently skipped.
/// * A single non-empty list degenerates to a pure rank-preserving sort scaled
///   by `weight / (k + rank)`.
///
/// # Example
///
/// ```
/// use cascade_rag::retrieve::rrf::{RankedList, rrf_merge};
///
/// let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.6)];
/// let dense: Vec<(i64, f64)> = vec![(2, 0.85), (1, 0.7)];
/// let lists = vec![
///     RankedList { source: "fts5",  weight: 1.0, hits: &fts },
///     RankedList { source: "dense", weight: 1.0, hits: &dense },
/// ];
/// let fused = rrf_merge(&lists, 60.0, 10);
/// // chunk_id 1 appeared as rank-1 in fts and rank-2 in dense → higher score than
/// // chunk_id 2 (rank-2 fts, rank-1 dense) because scores are symmetric here,
/// // but fts rank-1 beats dense rank-1 for chunk_id 2 is offset by fts rank-2.
/// assert!(!fused.is_empty());
/// ```
pub fn rrf_merge(lists: &[RankedList<'_>], k: f64, top_n: usize) -> Vec<FusedHit> {
    // Accumulate: chunk_id -> (rrf_score, sources)
    let mut scores: HashMap<i64, (f64, Vec<String>)> = HashMap::new();

    for list in lists {
        if list.hits.is_empty() {
            continue;
        }
        for (zero_based, (chunk_id, _score)) in list.hits.iter().enumerate() {
            let rank = (zero_based + 1) as f64; // 1-based
            let contribution = list.weight / (k + rank);
            let entry = scores.entry(*chunk_id).or_insert_with(|| (0.0, Vec::new()));
            entry.0 += contribution;
            if !entry.1.iter().any(|s| s == list.source) {
                entry.1.push(list.source.to_owned());
            }
        }
    }

    let mut fused: Vec<FusedHit> = scores
        .into_iter()
        .map(|(chunk_id, (rrf_score, sources_hit))| FusedHit {
            chunk_id,
            rrf_score,
            sources_hit,
        })
        .collect();

    // Primary sort: descending RRF score.
    // Tie-break: ascending chunk_id (deterministic).
    fused.sort_by(|a, b| {
        b.rrf_score
            .partial_cmp(&a.rrf_score)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then_with(|| a.chunk_id.cmp(&b.chunk_id))
    });

    if top_n > 0 {
        fused.truncate(top_n);
    }
    fused
}

// ── RrfConfig & RrfRetriever ──────────────────────────────────────────────────

/// Configuration for the RRF fusion step.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct RrfConfig {
    /// RRF smoothing constant.  Larger values reduce the influence of top-1
    /// results relative to the rest of the list.
    pub k: f32,
    /// Whether to use FTS5 results.
    pub use_fts: bool,
    /// Whether to use dense vector results.
    pub use_vec: bool,
    /// Whether to use sparse token-overlap results (reserved for P5).
    pub use_sparse: bool,
    /// Whether to use multi-vector (ColBERT-style MaxSim) as an optional 4th
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
            use_sparse: false,
            #[cfg(feature = "rag-multivec")]
            use_multivec: false,
        }
    }
}

/// Hybrid FTS5 + dense-vector retriever fused with Reciprocal Rank Fusion.
///
/// This is the default retriever for `TierLevel::Semantic` and above.
///
/// When `use_vec = false` (or no embedding provider is given), falls back to
/// pure FTS5.  When `use_fts = false`, falls back to pure vector search.
///
/// # Example
///
/// ```rust,no_run
/// # use std::sync::Arc;
/// # use cascade_rag::index::RagIndex;
/// # use cascade_rag::retrieve::rrf::{RrfRetriever, RrfConfig};
/// # use cascade_types::NoopEmbeddingProvider;
/// # use cascade_types::Retriever;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let idx = Arc::new(RagIndex::open("/tmp/cascade.db").await?);
/// let embedder = Arc::new(NoopEmbeddingProvider);
/// let retriever = RrfRetriever::new(idx, embedder, RrfConfig::default());
/// let hits = retriever.retrieve("prayer time", &Default::default()).await?;
/// # Ok(())
/// # }
/// ```
pub struct RrfRetriever {
    fts: FtsRetriever,
    vec: Option<VectorRetriever>,
    config: RrfConfig,
}

impl RrfRetriever {
    /// Construct an RRF retriever with both FTS5 and dense-vector legs.
    pub fn new(
        index: Arc<RagIndex>,
        embedder: Arc<dyn EmbeddingProvider>,
        config: RrfConfig,
    ) -> Self {
        let fts = FtsRetriever::new(Arc::clone(&index));
        let vec = if config.use_vec {
            Some(VectorRetriever::new(index, embedder))
        } else {
            None
        };
        Self { fts, vec, config }
    }

    /// Construct an FTS-only retriever (no embeddings required).
    pub fn fts_only(index: Arc<RagIndex>) -> Self {
        let fts = FtsRetriever::new(index);
        Self {
            fts,
            vec: None,
            config: RrfConfig {
                use_vec: false,
                ..Default::default()
            },
        }
    }
}

#[async_trait]
impl Retriever for RrfRetriever {
    /// Execute FTS5 and ANN queries in parallel, fuse with RRF, return top-K.
    ///
    /// If the vector leg is not configured (FTS-only mode), returns BM25 hits
    /// directly without score transformation.
    #[instrument(skip(self), fields(k = opts.k))]
    async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        // Fetch candidates from both legs (overfetch to ensure good RRF coverage).
        let candidate_k = (opts.k * 5).max(50);
        let fts_opts = RetrieveOpts {
            k: candidate_k,
            ..opts.clone()
        };

        let fts_hits = if self.config.use_fts {
            self.fts.retrieve(query, &fts_opts).await?
        } else {
            vec![]
        };

        let vec_hits = if let (true, Some(vec_ret)) = (self.config.use_vec, &self.vec) {
            vec_ret.retrieve(query, &fts_opts).await?
        } else {
            vec![]
        };

        // If only one leg contributed, return its results directly.
        if fts_hits.is_empty() {
            return Ok(vec_hits.into_iter().take(opts.k).collect());
        }
        if vec_hits.is_empty() {
            return Ok(fts_hits.into_iter().take(opts.k).collect());
        }

        // Fuse with RRF.
        let mut scores: HashMap<String, f32> = HashMap::new();
        let k = self.config.k;

        for (rank, hit) in fts_hits.iter().enumerate() {
            *scores.entry(hit.chunk_id.clone()).or_insert(0.0) += 1.0 / (k + (rank + 1) as f32);
        }
        for (rank, hit) in vec_hits.iter().enumerate() {
            *scores.entry(hit.chunk_id.clone()).or_insert(0.0) += 1.0 / (k + (rank + 1) as f32);
        }

        // Build a unified hit index from FTS results (text is available there).
        let mut hit_map: HashMap<String, RetrievalHit> = fts_hits
            .into_iter()
            .map(|h| (h.chunk_id.clone(), h))
            .collect();
        for h in vec_hits {
            hit_map.entry(h.chunk_id.clone()).or_insert(h);
        }

        // Sort by RRF score descending.
        let mut ranked: Vec<(String, f32)> = scores.into_iter().collect();
        ranked.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));

        let results: Vec<RetrievalHit> = ranked
            .into_iter()
            .take(opts.k)
            .enumerate()
            .filter_map(|(new_rank, (chunk_id, rrf_score))| {
                hit_map.remove(&chunk_id).map(|mut hit| {
                    hit.score = rrf_score;
                    hit.rank = new_rank;
                    hit
                })
            })
            .collect();

        Ok(results)
    }

    fn name(&self) -> &str {
        "hybrid-rrf"
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // Helper: approximate float equality.
    fn approx_eq(a: f64, b: f64) -> bool {
        (a - b).abs() < 1e-10
    }

    /// Two lists with known overlap — verify RRF scores match manual calculation.
    ///
    /// fts:   [(1, 0.9), (2, 0.6)]         rank 1 → id=1, rank 2 → id=2
    /// dense: [(2, 0.85), (1, 0.7)]         rank 1 → id=2, rank 2 → id=1
    ///
    /// k=60, weight=1.0:
    ///   id=1: 1/(60+1) + 1/(60+2) = 1/61 + 1/62
    ///   id=2: 1/(60+2) + 1/(60+1) = 1/62 + 1/61  (same! symmetric)
    ///   tie broken by chunk_id ascending → id=1 first.
    #[test]
    fn known_vector_fusion() {
        let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.6)];
        let dense: Vec<(i64, f64)> = vec![(2, 0.85), (1, 0.7)];
        let lists = vec![
            RankedList { source: "fts5",  weight: 1.0, hits: &fts },
            RankedList { source: "dense", weight: 1.0, hits: &dense },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);

        assert_eq!(fused.len(), 2, "expect 2 unique chunks");

        let expected_id1 = 1.0 / 61.0 + 1.0 / 62.0;
        let expected_id2 = 1.0 / 62.0 + 1.0 / 61.0;
        assert!(approx_eq(fused[0].rrf_score, expected_id1), "id=1 score");
        assert!(approx_eq(fused[1].rrf_score, expected_id2), "id=2 score");

        // Tie-break: ascending chunk_id → id=1 before id=2.
        assert_eq!(fused[0].chunk_id, 1);
        assert_eq!(fused[1].chunk_id, 2);

        // Both lists contributed to both chunks.
        assert!(fused[0].sources_hit.contains(&"fts5".to_string()));
        assert!(fused[0].sources_hit.contains(&"dense".to_string()));
    }

    /// Source weights: doubling the weight of one list doubles its contribution.
    #[test]
    fn source_weights() {
        let fts: Vec<(i64, f64)> = vec![(10, 0.9)];
        let dense: Vec<(i64, f64)> = vec![(20, 0.85)];
        let lists = vec![
            RankedList { source: "fts5",  weight: 2.0, hits: &fts },
            RankedList { source: "dense", weight: 1.0, hits: &dense },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);

        // id=10 gets 2.0/(60+1), id=20 gets 1.0/(60+1).
        let score_10 = fused.iter().find(|h| h.chunk_id == 10).unwrap().rrf_score;
        let score_20 = fused.iter().find(|h| h.chunk_id == 20).unwrap().rrf_score;
        assert!(
            approx_eq(score_10, 2.0 / 61.0),
            "weighted fts score for id=10"
        );
        assert!(
            approx_eq(score_20, 1.0 / 61.0),
            "unweighted dense score for id=20"
        );
        // Higher weight → higher score → id=10 ranked first.
        assert_eq!(fused[0].chunk_id, 10);
    }

    /// Tie-break: two chunks with identical scores are sorted by ascending chunk_id.
    #[test]
    fn tie_break_determinism() {
        let list: Vec<(i64, f64)> = vec![(100, 1.0), (50, 1.0)];
        // Second list gives same rank to both ids (one each, mirrored).
        let list2: Vec<(i64, f64)> = vec![(50, 1.0), (100, 1.0)];
        let lists = vec![
            RankedList { source: "a", weight: 1.0, hits: &list },
            RankedList { source: "b", weight: 1.0, hits: &list2 },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);
        // id=50: rank 2 in a + rank 1 in b → 1/62 + 1/61
        // id=100: rank 1 in a + rank 2 in b → 1/61 + 1/62  (same scores, tie)
        // Tie broken by id ascending → 50 before 100.
        assert_eq!(fused[0].chunk_id, 50);
        assert_eq!(fused[1].chunk_id, 100);
    }

    /// Empty lists: all-empty input → empty output.
    #[test]
    fn empty_lists_return_empty() {
        let lists: Vec<RankedList<'_>> = vec![];
        let fused = rrf_merge(&lists, 60.0, 10);
        assert!(fused.is_empty());
    }

    /// Empty lists: one non-empty, one empty → non-empty list drives results.
    #[test]
    fn one_empty_one_non_empty() {
        let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.6)];
        let empty: Vec<(i64, f64)> = vec![];
        let lists = vec![
            RankedList { source: "fts5",  weight: 1.0, hits: &fts },
            RankedList { source: "dense", weight: 1.0, hits: &empty },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);
        assert_eq!(fused.len(), 2);
        // id=1 is rank-1 → higher rrf than id=2 (rank-2).
        assert_eq!(fused[0].chunk_id, 1);
        assert!(approx_eq(fused[0].rrf_score, 1.0 / 61.0));
        assert!(approx_eq(fused[1].rrf_score, 1.0 / 62.0));
    }

    /// Single-list passthrough: results are ordered rank-preservingly.
    #[test]
    fn single_list_passthrough() {
        let fts: Vec<(i64, f64)> = vec![(5, 0.99), (3, 0.75), (7, 0.4)];
        let lists = vec![
            RankedList { source: "fts5", weight: 1.0, hits: &fts },
        ];
        let fused = rrf_merge(&lists, 60.0, 0); // top_n=0 → return all
        assert_eq!(fused.len(), 3);
        // rank 1 → id=5 → score 1/61; rank 2 → id=3 → 1/62; rank 3 → id=7 → 1/63
        assert_eq!(fused[0].chunk_id, 5);
        assert_eq!(fused[1].chunk_id, 3);
        assert_eq!(fused[2].chunk_id, 7);
    }

    /// Top-N cut: only `top_n` results returned.
    #[test]
    fn top_n_cut() {
        let fts: Vec<(i64, f64)> = vec![(1, 1.0), (2, 0.9), (3, 0.8), (4, 0.7)];
        let lists = vec![
            RankedList { source: "fts5", weight: 1.0, hits: &fts },
        ];
        let fused = rrf_merge(&lists, 60.0, 2);
        assert_eq!(fused.len(), 2);
        assert_eq!(fused[0].chunk_id, 1);
        assert_eq!(fused[1].chunk_id, 2);
    }

    /// Provenance: sources_hit lists only the sources that contributed.
    #[test]
    fn provenance_sources_hit() {
        // id=99 only in fts; id=77 only in dense; id=55 in both.
        let fts: Vec<(i64, f64)> = vec![(55, 0.9), (99, 0.5)];
        let dense: Vec<(i64, f64)> = vec![(55, 0.8), (77, 0.4)];
        let lists = vec![
            RankedList { source: "fts5",  weight: 1.0, hits: &fts },
            RankedList { source: "dense", weight: 1.0, hits: &dense },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);

        let hit_55 = fused.iter().find(|h| h.chunk_id == 55).unwrap();
        assert!(hit_55.sources_hit.contains(&"fts5".to_string()));
        assert!(hit_55.sources_hit.contains(&"dense".to_string()));

        let hit_99 = fused.iter().find(|h| h.chunk_id == 99).unwrap();
        assert_eq!(hit_99.sources_hit, vec!["fts5".to_string()]);

        let hit_77 = fused.iter().find(|h| h.chunk_id == 77).unwrap();
        assert_eq!(hit_77.sources_hit, vec!["dense".to_string()]);
    }

    /// Three-list fusion: FTS5 + dense + sparse — verifies N-list path.
    #[test]
    fn three_list_fusion() {
        let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.5)];
        let dense: Vec<(i64, f64)> = vec![(1, 0.8), (3, 0.7)];
        let sparse: Vec<(i64, f64)> = vec![(2, 0.6), (1, 0.3)];
        let lists = vec![
            RankedList { source: "fts5",   weight: 1.0, hits: &fts },
            RankedList { source: "dense",  weight: 1.0, hits: &dense },
            RankedList { source: "sparse", weight: 1.0, hits: &sparse },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);
        // id=1 appears in all 3 lists → highest cumulative RRF.
        assert_eq!(fused[0].chunk_id, 1);
        assert_eq!(fused[0].sources_hit.len(), 3);
    }
}
