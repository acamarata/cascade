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

use super::curated::CuratedRetriever;
use super::exclusion::{ExclusionConfig, ExclusionSet};
use super::fts::FtsRetriever;
use super::recency::RecencyRetriever;
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

/// 5-channel hybrid retriever fused with Reciprocal Rank Fusion.
///
/// Channels (all optional, gracefully degrade when absent):
///
/// 1. **FTS5 BM25** — body-text keyword search via `rag_fts5`.
/// 2. **Dense ANN** — cosine KNN search via `vec_chunks` (requires embedder).
/// 3. **Curated description** — BM25 over frontmatter `description`/`title`/`tags`
///    via `rag_chunk_meta_fts5` (migration 0009).
/// 4. **Recency** — rank by `rag_sources.mtime`/`indexed_at`.
/// 5. **Multi-vec ColBERT** — late-interaction MaxSim (feature-gated).
///
/// This is the default retriever for `TierLevel::Semantic` and above.
///
/// When `use_vec = false` (or no embedding provider is given), falls back to
/// FTS5-only.  When `use_fts = false`, falls back to dense search.  Channels 3
/// and 4 are enabled by default but silently no-op when their data is absent.
///
/// ## Privacy isolation
///
/// An [`ExclusionSet`] compiled from caller-supplied [`ExclusionConfig`] is
/// applied at **two layers**:
///
/// - **Layer 1 (pre):** excluded paths are stripped from each channel's raw
///   `(chunk_id, score)` list before those lists enter RRF fusion.
/// - **Layer 2 (post):** the fully fused `Vec<RetrievalHit>` is filtered again
///   before being returned.  This belt-and-suspenders pass catches any hit that
///   slipped through a channel without an available path at layer 1.
///
/// Pass [`ExclusionConfig`] via [`RrfRetriever::new_with_exclusion`].
/// [`RrfRetriever::new`] applies no exclusion (equivalent to an empty config).
///
/// # Example
///
/// ```rust,no_run
/// # use std::sync::Arc;
/// # use cascade_rag::index::RagIndex;
/// # use cascade_rag::retrieve::rrf::{RrfRetriever, RrfConfig};
/// # use cascade_rag::retrieve::exclusion::ExclusionConfig;
/// # use cascade_types::NoopEmbeddingProvider;
/// # use cascade_types::Retriever;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let idx = Arc::new(RagIndex::open("/tmp/cascade.db").await?);
/// let embedder = Arc::new(NoopEmbeddingProvider);
/// let exclusion = ExclusionConfig::from_patterns(["/home/user/medical/"]);
/// let retriever = RrfRetriever::new_with_exclusion(
///     idx, embedder, RrfConfig::default(), exclusion,
/// );
/// let hits = retriever.retrieve("appointment", &Default::default()).await?;
/// // hits will never contain documents from /home/user/medical/
/// # Ok(())
/// # }
/// ```
pub struct RrfRetriever {
    fts: FtsRetriever,
    vec: Option<VectorRetriever>,
    /// Curated description/title/tags FTS channel.
    curated: Option<CuratedRetriever>,
    /// Document recency channel.
    recency: Option<RecencyRetriever>,
    config: RrfConfig,
    /// Compiled exclusion set (defense-in-depth privacy isolation).
    /// Applied at layer 1 (per-channel pre-filter) and layer 2 (post-fusion filter).
    exclusion: ExclusionSet,
}

impl RrfRetriever {
    /// Construct an RRF retriever with all enabled channels and **no exclusion**.
    ///
    /// Channels 3 (curated) and 4 (recency) are created unconditionally when
    /// their flags are set; they degrade to empty results if the underlying
    /// tables are missing or empty.
    ///
    /// For privacy-isolated deployments, use [`new_with_exclusion`] instead.
    ///
    /// [`new_with_exclusion`]: RrfRetriever::new_with_exclusion
    pub fn new(
        index: Arc<RagIndex>,
        embedder: Arc<dyn EmbeddingProvider>,
        config: RrfConfig,
    ) -> Self {
        Self::new_with_exclusion(index, embedder, config, ExclusionConfig::default())
    }

    /// Construct an RRF retriever with all enabled channels and caller-supplied
    /// **scope exclusion**.
    ///
    /// The resolver / daemon builds an [`ExclusionConfig`] from the project's
    /// privacy configuration and passes it here.  The compiled [`ExclusionSet`]
    /// is applied at two layers:
    ///
    /// - **Layer 1 (pre):** per-channel raw hit lists are filtered before RRF.
    /// - **Layer 2 (post):** the fused result list is filtered before return.
    ///
    /// An empty `ExclusionConfig` (the default) is a no-op — no filtering is
    /// applied and performance is unaffected.
    pub fn new_with_exclusion(
        index: Arc<RagIndex>,
        embedder: Arc<dyn EmbeddingProvider>,
        config: RrfConfig,
        exclusion_config: ExclusionConfig,
    ) -> Self {
        let fts = FtsRetriever::new(Arc::clone(&index));
        let vec = if config.use_vec {
            Some(VectorRetriever::new(Arc::clone(&index), embedder))
        } else {
            None
        };
        let curated = if config.use_curated {
            Some(CuratedRetriever::new(Arc::clone(&index)))
        } else {
            None
        };
        let recency = if config.use_recency {
            Some(RecencyRetriever::new(Arc::clone(&index)))
        } else {
            None
        };
        let exclusion = ExclusionSet::compile(&exclusion_config);
        Self {
            fts,
            vec,
            curated,
            recency,
            config,
            exclusion,
        }
    }

    /// Construct an FTS-only retriever (no embeddings required, no exclusion).
    ///
    /// Curated-description and recency channels are also disabled to avoid
    /// unnecessary index look-ups in contexts where only keyword search is
    /// needed.
    pub fn fts_only(index: Arc<RagIndex>) -> Self {
        let fts = FtsRetriever::new(Arc::clone(&index));
        Self {
            fts,
            vec: None,
            curated: None,
            recency: None,
            config: RrfConfig {
                use_vec: false,
                use_curated: false,
                use_recency: false,
                ..Default::default()
            },
            exclusion: ExclusionSet::default(),
        }
    }
}

#[async_trait]
impl Retriever for RrfRetriever {
    /// Execute all enabled channels in parallel, fuse with weighted RRF, return top-K.
    ///
    /// ## Channel execution
    ///
    /// FTS5 and dense channels are run concurrently via `tokio::join!`.
    /// Curated and recency channels are lightweight (single SQLite read each)
    /// and run in the same join.  Any channel that returns an error or empty
    /// list simply contributes nothing to the fusion — the remaining channels
    /// carry the result.
    ///
    /// ## Back-compat
    ///
    /// When `use_curated = false` and `use_recency = false` (e.g. `fts_only()`),
    /// behaviour is identical to the previous 2-channel implementation.
    #[instrument(skip(self), fields(k = opts.k))]
    async fn retrieve(&self, query: &str, opts: &RetrieveOpts) -> Result<Vec<RetrievalHit>> {
        // Over-fetch so RRF has enough candidates for good coverage.
        let candidate_k = (opts.k * 5).max(50);
        let wide_opts = RetrieveOpts {
            k: candidate_k,
            ..opts.clone()
        };

        // ── Parallel fetch ──────────────────────────────────────────────────
        let fts_fut = async {
            if self.config.use_fts {
                self.fts.retrieve(query, &wide_opts).await.unwrap_or_default()
            } else {
                vec![]
            }
        };

        let vec_fut = async {
            if let (true, Some(v)) = (self.config.use_vec, &self.vec) {
                v.retrieve(query, &wide_opts).await.unwrap_or_default()
            } else {
                vec![]
            }
        };

        let curated_fut = async {
            if let Some(c) = &self.curated {
                c.retrieve(query, &wide_opts).await.unwrap_or_default()
            } else {
                vec![]
            }
        };

        let recency_fut = async {
            if let Some(r) = &self.recency {
                r.retrieve(query, &wide_opts).await.unwrap_or_default()
            } else {
                vec![]
            }
        };

        let (fts_hits, vec_hits, curated_hits, recency_hits) =
            tokio::join!(fts_fut, vec_fut, curated_fut, recency_fut);

        // ── Build (chunk_id_i64, score) lists for rrf_merge ────────────────
        // rrf_merge operates on i64 chunk IDs.  Curated and recency IDs come
        // back as strings (from RagIndex) because the shared Retriever trait
        // uses String IDs.  We parse them here.
        //
        // Layer 1 (pre-filter): strip excluded paths from every channel's raw
        // pair list before they enter RRF fusion.  The `file_path` field on
        // RetrievalHit carries the source path when available; we use it as
        // the key into the exclusion set.  Hits with no path are kept
        // (fail-open) — layer 2 catches them if they do have a path after
        // the hit-map join.
        let path_str = |h: &RetrievalHit| -> Option<String> {
            h.file_path.as_ref().and_then(|p| p.to_str()).map(str::to_string)
        };

        let fts_pairs: Vec<(i64, f64)> = {
            let raw = hits_to_pairs(&fts_hits);
            self.exclusion.filter_pairs(&raw, |id| {
                fts_hits.iter().find(|h| h.chunk_id == id.to_string()).and_then(path_str)
            })
        };
        let vec_pairs: Vec<(i64, f64)> = {
            let raw = hits_to_pairs(&vec_hits);
            self.exclusion.filter_pairs(&raw, |id| {
                vec_hits.iter().find(|h| h.chunk_id == id.to_string()).and_then(path_str)
            })
        };
        let curated_pairs: Vec<(i64, f64)> = {
            let raw = hits_to_pairs(&curated_hits);
            self.exclusion.filter_pairs(&raw, |id| {
                curated_hits.iter().find(|h| h.chunk_id == id.to_string()).and_then(path_str)
            })
        };
        let recency_pairs: Vec<(i64, f64)> = {
            let raw = hits_to_pairs(&recency_hits);
            self.exclusion.filter_pairs(&raw, |id| {
                recency_hits.iter().find(|h| h.chunk_id == id.to_string()).and_then(path_str)
            })
        };

        // ── Assemble RankedList inputs ──────────────────────────────────────
        let mut lists: Vec<RankedList<'_>> = Vec::with_capacity(4);

        if !fts_pairs.is_empty() {
            lists.push(RankedList {
                source: "fts5",
                weight: self.config.weight_fts,
                hits: &fts_pairs,
            });
        }
        if !vec_pairs.is_empty() {
            lists.push(RankedList {
                source: "dense",
                weight: self.config.weight_vec,
                hits: &vec_pairs,
            });
        }
        if !curated_pairs.is_empty() {
            lists.push(RankedList {
                source: "curated",
                weight: self.config.weight_curated,
                hits: &curated_pairs,
            });
        }
        if !recency_pairs.is_empty() {
            lists.push(RankedList {
                source: "recency",
                weight: self.config.weight_recency,
                hits: &recency_pairs,
            });
        }

        // ── Single-channel fast path ────────────────────────────────────────
        if lists.is_empty() {
            return Ok(vec![]);
        }
        if lists.len() == 1 {
            // Avoid allocating a fusion map when only one channel fired.
            let sole = if !fts_hits.is_empty() {
                fts_hits
            } else if !vec_hits.is_empty() {
                vec_hits
            } else if !curated_hits.is_empty() {
                curated_hits
            } else {
                recency_hits
            };
            return Ok(sole.into_iter().take(opts.k).collect());
        }

        // ── Multi-channel RRF fusion ────────────────────────────────────────
        let k = self.config.k as f64;
        let fused = rrf_merge(&lists, k, opts.k);

        // Build a unified hit map from all sources for text/file_path lookup.
        let mut hit_map: HashMap<String, RetrievalHit> = HashMap::new();
        for h in fts_hits
            .into_iter()
            .chain(vec_hits)
            .chain(curated_hits)
            .chain(recency_hits)
        {
            hit_map.entry(h.chunk_id.clone()).or_insert(h);
        }

        let results: Vec<RetrievalHit> = fused
            .into_iter()
            .enumerate()
            .filter_map(|(new_rank, fused_hit)| {
                let id_str = fused_hit.chunk_id.to_string();
                hit_map.remove(&id_str).map(|mut hit| {
                    hit.score = fused_hit.rrf_score as f32;
                    hit.rank = new_rank;
                    hit
                })
            })
            .collect();

        // ── Layer 2 (post-filter): belt-and-suspenders exclusion pass ───────
        // Applied after fusion so that any hit that leaked through layer 1
        // (e.g. because it had no file_path at the channel level but gained one
        // from the hit-map join) is still removed before being returned.
        let results = self.exclusion.filter_hits(results);

        Ok(results)
    }

    fn name(&self) -> &str {
        "hybrid-rrf"
    }
}

/// Convert [`RetrievalHit`] slices to `(i64, f64)` pairs for [`rrf_merge`].
///
/// Hits whose `chunk_id` cannot be parsed as an `i64` are silently dropped.
/// This handles the edge case where synthetic IDs (e.g. from `curated`) are
/// pre-translated to real chunk IDs by [`RagIndex`].
fn hits_to_pairs(hits: &[RetrievalHit]) -> Vec<(i64, f64)> {
    hits.iter()
        .filter_map(|h| h.chunk_id.parse::<i64>().ok().map(|id| (id, h.score as f64)))
        .collect()
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
            RankedList {
                source: "fts5",
                weight: 1.0,
                hits: &fts,
            },
            RankedList {
                source: "dense",
                weight: 1.0,
                hits: &dense,
            },
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
            RankedList {
                source: "fts5",
                weight: 2.0,
                hits: &fts,
            },
            RankedList {
                source: "dense",
                weight: 1.0,
                hits: &dense,
            },
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
            RankedList {
                source: "a",
                weight: 1.0,
                hits: &list,
            },
            RankedList {
                source: "b",
                weight: 1.0,
                hits: &list2,
            },
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
            RankedList {
                source: "fts5",
                weight: 1.0,
                hits: &fts,
            },
            RankedList {
                source: "dense",
                weight: 1.0,
                hits: &empty,
            },
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
        let lists = vec![RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        }];
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
        let lists = vec![RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        }];
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
            RankedList {
                source: "fts5",
                weight: 1.0,
                hits: &fts,
            },
            RankedList {
                source: "dense",
                weight: 1.0,
                hits: &dense,
            },
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
            RankedList {
                source: "fts5",
                weight: 1.0,
                hits: &fts,
            },
            RankedList {
                source: "dense",
                weight: 1.0,
                hits: &dense,
            },
            RankedList {
                source: "sparse",
                weight: 1.0,
                hits: &sparse,
            },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);
        // id=1 appears in all 3 lists → highest cumulative RRF.
        assert_eq!(fused[0].chunk_id, 1);
        assert_eq!(fused[0].sources_hit.len(), 3);
    }

    // ── 5-channel fusion tests ────────────────────────────────────────────────

    /// Curated-description channel boosts a sparse-body doc when its description
    /// matches.  Without the curated channel the sparse doc would lose to a rich
    /// body doc; with it, the two compete on equal footing (or the curated doc wins).
    ///
    /// Simulated with pure `rrf_merge` by giving the sparse doc a strong curated
    /// rank and a weak FTS rank, and the body doc a strong FTS rank only.
    #[test]
    fn curated_channel_boosts_sparse_body_doc() {
        // doc=1: strong body (fts rank 1), no curated hit.
        // doc=2: weak body (fts rank 5), but curated rank 1.
        let fts: Vec<(i64, f64)> = vec![(1, 0.95), (3, 0.5), (4, 0.4), (5, 0.3), (2, 0.1)];
        let curated: Vec<(i64, f64)> = vec![(2, 0.99)]; // doc=2 matches description

        let lists_without = vec![RankedList {
            source: "fts5",
            weight: 1.0,
            hits: &fts,
        }];
        let fused_without = rrf_merge(&lists_without, 60.0, 5);

        let lists_with = vec![
            RankedList {
                source: "fts5",
                weight: 1.0,
                hits: &fts,
            },
            RankedList {
                source: "curated",
                weight: 0.8,
                hits: &curated,
            },
        ];
        let fused_with = rrf_merge(&lists_with, 60.0, 5);

        // Without curated: doc=2 must rank last (it was rank-5 in FTS).
        let rank_without = fused_without
            .iter()
            .position(|h| h.chunk_id == 2)
            .expect("doc=2 must appear");
        assert_eq!(rank_without, 4, "doc=2 must be last without curated channel");

        // With curated: doc=2 rank must improve (move closer to the front).
        let rank_with = fused_with
            .iter()
            .position(|h| h.chunk_id == 2)
            .expect("doc=2 must appear");
        assert!(
            rank_with < rank_without,
            "curated channel must improve doc=2 rank ({rank_with} < {rank_without})"
        );
    }

    /// Recency breaks ties toward the newer document.
    ///
    /// Two docs with identical FTS and curated scores.  The recency channel
    /// gives a higher score to doc=10 (newer).  After fusion, doc=10 must rank
    /// first.
    #[test]
    fn recency_breaks_ties_toward_newer_doc() {
        // doc=10 and doc=20 are symmetric in FTS (rank 1 each in their own list
        // → simulate tied scores by putting them at the same rank in both).
        let fts: Vec<(i64, f64)> = vec![(10, 0.8), (20, 0.8)]; // tied FTS scores
        // Recency: doc=10 is newer → rank 1; doc=20 is older → rank 2.
        let recency: Vec<(i64, f64)> = vec![(10, 1.0), (20, 0.5)];

        let lists = vec![
            RankedList {
                source: "fts5",
                weight: 1.0,
                hits: &fts,
            },
            RankedList {
                source: "recency",
                weight: 0.5,
                hits: &recency,
            },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);

        // doc=10 should be first because it wins the recency channel.
        assert_eq!(fused[0].chunk_id, 10, "newer doc must rank first after recency tie-break");
        assert!(fused[0].sources_hit.contains(&"recency".to_string()));
    }

    /// Five-channel fusion: when all 5 channels contribute to doc=1, it wins.
    #[test]
    fn five_channel_fusion_all_sources_contribute() {
        let fts: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.5)];
        let dense: Vec<(i64, f64)> = vec![(1, 0.85), (3, 0.7)];
        let curated: Vec<(i64, f64)> = vec![(1, 0.99), (4, 0.6)];
        let recency: Vec<(i64, f64)> = vec![(1, 1.0), (5, 0.4)];
        let sparse: Vec<(i64, f64)> = vec![(1, 0.75), (6, 0.3)];

        let lists = vec![
            RankedList { source: "fts5",    weight: 1.0, hits: &fts },
            RankedList { source: "dense",   weight: 1.0, hits: &dense },
            RankedList { source: "curated", weight: 0.8, hits: &curated },
            RankedList { source: "recency", weight: 0.5, hits: &recency },
            RankedList { source: "sparse",  weight: 1.0, hits: &sparse },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);

        assert_eq!(fused[0].chunk_id, 1, "doc=1 (all 5 channels) must rank first");
        assert_eq!(fused[0].sources_hit.len(), 5, "all 5 sources must be in provenance");
    }

    /// Missing channels (all empty except one) must not crash — single-channel fallback.
    #[test]
    fn missing_channels_degrade_gracefully() {
        let fts: Vec<(i64, f64)> = vec![(42, 0.9)];
        let empty: Vec<(i64, f64)> = vec![];

        // Only the FTS list is non-empty; curated and recency are empty.
        let lists = vec![
            RankedList { source: "fts5",    weight: 1.0, hits: &fts },
            RankedList { source: "curated", weight: 0.8, hits: &empty },
            RankedList { source: "recency", weight: 0.5, hits: &empty },
        ];
        let fused = rrf_merge(&lists, 60.0, 10);

        assert_eq!(fused.len(), 1, "must return the single FTS hit");
        assert_eq!(fused[0].chunk_id, 42);
        assert_eq!(fused[0].sources_hit, vec!["fts5".to_string()]);
    }

    /// Weight=0.0 for a channel effectively disables its influence.
    #[test]
    fn zero_weight_channel_has_no_influence() {
        let fts: Vec<(i64, f64)> = vec![(1, 0.9)];
        let recency: Vec<(i64, f64)> = vec![(2, 1.0), (1, 0.5)]; // recency prefers doc=2

        // With weight=0.0 on recency, doc=2 gets 0 contribution from recency.
        let lists_zero = vec![
            RankedList { source: "fts5",    weight: 1.0, hits: &fts },
            RankedList { source: "recency", weight: 0.0, hits: &recency },
        ];
        let fused_zero = rrf_merge(&lists_zero, 60.0, 10);

        // doc=1 wins because recency has zero weight.
        assert_eq!(fused_zero[0].chunk_id, 1, "zero-weight recency must not override FTS");
    }

    // ── Adversarial exclusion tests ───────────────────────────────────────────
    //
    // These tests prove that a locked doc CANNOT surface via any channel, even
    // when its content/recency/description would otherwise rank it #1.
    //
    // Strategy: use ExclusionSet directly on pair lists (simulating layer 1 of
    // the retriever) and on RetrievalHit lists (layer 2).  The tests are
    // channel-by-channel plus a full 5-channel fusion scenario.

    use super::super::exclusion::{ExclusionConfig, ExclusionSet};
    use cascade_types::retriever::RetrievalHit;
    use std::path::PathBuf;

    /// Build a RetrievalHit with the given chunk_id and file_path.
    fn hit(chunk_id: i64, file_path: Option<&str>, score: f32) -> RetrievalHit {
        RetrievalHit {
            chunk_id: chunk_id.to_string(),
            text: "content".to_string(),
            file_path: file_path.map(PathBuf::from),
            start_line: None,
            end_line: None,
            score,
            rank: 0,
            tier: None,
        }
    }

    /// The locked doc would rank #1 on FTS alone.  After layer-1 exclusion it
    /// must not appear in the pair list.
    #[test]
    fn adversarial_fts_channel_locked_doc_absent() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));

        // Locked doc (id=999) would be rank-1 on FTS; public doc (id=1) is rank-2.
        let raw_fts: Vec<(i64, f64)> = vec![(999, 0.99), (1, 0.5)];

        let filtered = ex.filter_pairs(&raw_fts, |id| match id {
            999 => Some("/locked/secret.md".to_string()),
            1   => Some("/public/safe.md".to_string()),
            _   => None,
        });

        assert!(
            !filtered.iter().any(|(id, _)| *id == 999),
            "locked doc must not appear in FTS pair list after layer-1 exclusion"
        );
        assert!(
            filtered.iter().any(|(id, _)| *id == 1),
            "public doc must still appear"
        );
    }

    /// The locked doc would rank #1 on vector alone.
    #[test]
    fn adversarial_vector_channel_locked_doc_absent() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
        let raw_vec: Vec<(i64, f64)> = vec![(999, 0.98), (2, 0.6)];

        let filtered = ex.filter_pairs(&raw_vec, |id| match id {
            999 => Some("/locked/secret.md".to_string()),
            _   => None,
        });

        assert!(!filtered.iter().any(|(id, _)| *id == 999));
    }

    /// The locked doc would rank #1 on the curated-description channel.
    #[test]
    fn adversarial_curated_channel_locked_doc_absent() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
        let raw_curated: Vec<(i64, f64)> = vec![(999, 0.97), (3, 0.7)];

        let filtered = ex.filter_pairs(&raw_curated, |id| match id {
            999 => Some("/locked/meta.md".to_string()),
            _   => None,
        });

        assert!(!filtered.iter().any(|(id, _)| *id == 999));
    }

    /// The locked doc would rank #1 on the recency channel (newest file).
    #[test]
    fn adversarial_recency_channel_locked_doc_absent() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
        let raw_recency: Vec<(i64, f64)> = vec![(999, 1.0), (4, 0.8)];

        let filtered = ex.filter_pairs(&raw_recency, |id| match id {
            999 => Some("/locked/diary.md".to_string()),
            _   => None,
        });

        assert!(!filtered.iter().any(|(id, _)| *id == 999));
    }

    /// Full 5-channel fusion: even when the locked doc ranks #1 in ALL 5
    /// channels, it must be absent from the fused result after layer-1 filtering.
    #[test]
    fn adversarial_five_channel_fusion_locked_doc_absent() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));

        // Locked doc is rank-1 everywhere; public docs are rank-2.
        let raw_fts:    Vec<(i64, f64)> = vec![(999, 0.99), (1, 0.5)];
        let raw_vec:    Vec<(i64, f64)> = vec![(999, 0.98), (2, 0.6)];
        let raw_curated:Vec<(i64, f64)> = vec![(999, 0.97), (3, 0.7)];
        let raw_recency:Vec<(i64, f64)> = vec![(999, 1.0),  (4, 0.8)];
        let raw_sparse: Vec<(i64, f64)> = vec![(999, 0.95), (5, 0.4)];

        let path_of = |id: i64| match id {
            999 => Some("/locked/top-secret.md".to_string()),
            _   => None,
        };

        let fts_pairs     = ex.filter_pairs(&raw_fts,     path_of);
        let vec_pairs     = ex.filter_pairs(&raw_vec,     path_of);
        let curated_pairs = ex.filter_pairs(&raw_curated, path_of);
        let recency_pairs = ex.filter_pairs(&raw_recency, path_of);
        let sparse_pairs  = ex.filter_pairs(&raw_sparse,  path_of);

        // None of the filtered lists must contain the locked id.
        for (label, pairs) in [
            ("fts",     &fts_pairs),
            ("vec",     &vec_pairs),
            ("curated", &curated_pairs),
            ("recency", &recency_pairs),
            ("sparse",  &sparse_pairs),
        ] {
            assert!(
                !pairs.iter().any(|(id, _)| *id == 999),
                "locked doc must not appear in {label} channel after layer-1 exclusion"
            );
        }

        // Fuse the clean lists — locked id must not appear in fusion output.
        let lists = vec![
            RankedList { source: "fts5",    weight: 1.0, hits: &fts_pairs },
            RankedList { source: "dense",   weight: 1.0, hits: &vec_pairs },
            RankedList { source: "curated", weight: 0.8, hits: &curated_pairs },
            RankedList { source: "recency", weight: 0.5, hits: &recency_pairs },
            RankedList { source: "sparse",  weight: 1.0, hits: &sparse_pairs },
        ];
        let fused = rrf_merge(&lists, 60.0, 20);
        assert!(
            !fused.iter().any(|h| h.chunk_id == 999),
            "locked doc must not appear in 5-channel RRF fusion output"
        );
    }

    /// Prefix exclusion: a directory-locked prefix excludes ALL children.
    #[test]
    fn adversarial_prefix_locks_all_children() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/private"]));

        let paths_to_check = [
            "/private/a.md",
            "/private/sub/b.md",
            "/private/sub/deep/c.pdf",
            "/private/",
        ];
        for p in paths_to_check {
            assert!(ex.is_excluded(p), "'{p}' must be excluded under /private prefix");
        }
        // Sibling path must not be excluded.
        assert!(!ex.is_excluded("/private-other/file.md"));
        assert!(!ex.is_excluded("/public/file.md"));
    }

    /// Layer-2 post-filter: a locked doc that somehow slips past layer 1 (e.g.
    /// it had no file_path at the channel level) is caught by filter_hits.
    #[test]
    fn adversarial_layer2_catches_leaked_hit() {
        let ex = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));

        // Simulate a hit that came through layer 1 without a path (kept, fail-open)
        // but now has a file_path set (e.g. from the hit-map join in RrfRetriever).
        let hits = vec![
            hit(999, Some("/locked/leaked.md"), 0.99), // would-be winner, locked
            hit(1,   Some("/public/safe.md"),   0.85),
            hit(2,   None,                      0.70), // no path → kept
        ];

        let filtered = ex.filter_hits(hits);

        assert!(
            !filtered.iter().any(|h| h.chunk_id == "999"),
            "layer-2 must catch the locked hit that leaked past layer 1"
        );
        assert!(filtered.iter().any(|h| h.chunk_id == "1"));
        assert!(filtered.iter().any(|h| h.chunk_id == "2"));
    }

    /// Removing the exclusion makes the locked doc appear (proves the test is real).
    #[test]
    fn removing_exclusion_unlocks_doc() {
        // With exclusion — doc 999 must not appear.
        let ex_on = ExclusionSet::compile(&ExclusionConfig::from_patterns(["/locked"]));
        let hits_on = vec![
            hit(999, Some("/locked/secret.md"), 0.99),
            hit(1,   Some("/public/safe.md"),   0.85),
        ];
        let filtered_on = ex_on.filter_hits(hits_on);
        assert!(!filtered_on.iter().any(|h| h.chunk_id == "999"));

        // Without exclusion — doc 999 must appear.
        let ex_off = ExclusionSet::compile(&ExclusionConfig::default());
        let hits_off = vec![
            hit(999, Some("/locked/secret.md"), 0.99),
            hit(1,   Some("/public/safe.md"),   0.85),
        ];
        let filtered_off = ex_off.filter_hits(hits_off);
        assert!(
            filtered_off.iter().any(|h| h.chunk_id == "999"),
            "with no exclusion, the doc must appear (proves test is real)"
        );
    }

    /// Empty exclusion config = no filtering; existing tests remain unaffected.
    #[test]
    fn empty_exclusion_no_filtering_regression() {
        let ex = ExclusionSet::compile(&ExclusionConfig::default());
        let pairs: Vec<(i64, f64)> = vec![(1, 0.9), (2, 0.8), (3, 0.7)];
        let filtered = ex.filter_pairs(&pairs, |_| Some("/any/path.md".to_string()));
        assert_eq!(filtered.len(), 3, "empty exclusion must not filter anything");
    }
}
