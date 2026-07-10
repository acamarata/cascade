//! [`RrfRetriever`] — 5-channel hybrid retriever fused with RRF.

use async_trait::async_trait;
use std::collections::HashMap;
use std::sync::Arc;
use tracing::instrument;

use cascade_types::{
    error::Result,
    retriever::{RetrievalHit, RetrieveOpts, Retriever},
    EmbeddingProvider,
};

use super::super::curated::CuratedRetriever;
use super::super::exclusion::{ExclusionConfig, ExclusionSet};
use super::super::fts::FtsRetriever;
use super::super::recency::RecencyRetriever;
use super::super::vector::VectorRetriever;
use super::config::RrfConfig;
use super::merge::{rrf_merge, RankedList};
use crate::index::RagIndex;

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
    pub(super) config: RrfConfig,
    /// Compiled exclusion set (defense-in-depth privacy isolation).
    /// Applied at layer 1 (per-channel pre-filter) and layer 2 (post-fusion filter).
    pub(super) exclusion: ExclusionSet,
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
                self.fts
                    .retrieve(query, &wide_opts)
                    .await
                    .unwrap_or_default()
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
            h.file_path
                .as_ref()
                .and_then(|p| p.to_str())
                .map(str::to_string)
        };

        let fts_pairs: Vec<(i64, f64)> = {
            let raw = hits_to_pairs(&fts_hits);
            self.exclusion.filter_pairs(&raw, |id| {
                fts_hits
                    .iter()
                    .find(|h| h.chunk_id == id.to_string())
                    .and_then(path_str)
            })
        };
        let vec_pairs: Vec<(i64, f64)> = {
            let raw = hits_to_pairs(&vec_hits);
            self.exclusion.filter_pairs(&raw, |id| {
                vec_hits
                    .iter()
                    .find(|h| h.chunk_id == id.to_string())
                    .and_then(path_str)
            })
        };
        let curated_pairs: Vec<(i64, f64)> = {
            let raw = hits_to_pairs(&curated_hits);
            self.exclusion.filter_pairs(&raw, |id| {
                curated_hits
                    .iter()
                    .find(|h| h.chunk_id == id.to_string())
                    .and_then(path_str)
            })
        };
        let recency_pairs: Vec<(i64, f64)> = {
            let raw = hits_to_pairs(&recency_hits);
            self.exclusion.filter_pairs(&raw, |id| {
                recency_hits
                    .iter()
                    .find(|h| h.chunk_id == id.to_string())
                    .and_then(path_str)
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
        //
        // `lists` only contains channels whose hits survived `hits_to_pairs`
        // (i.e. whose chunk_ids parse as i64).  When chunk_ids are non-numeric
        // strings (e.g. "c1" from `RagIndex::upsert_chunk`), all pairs are
        // dropped and `lists` ends up empty even though the raw hit slices are
        // non-empty.  In that case we must still return the raw hits rather than
        // silently returning an empty vec.
        if lists.is_empty() {
            // Fall back to whichever raw channel fired (non-numeric IDs bypass
            // RRF fusion and are returned in channel score order).
            let sole = if !fts_hits.is_empty() {
                fts_hits
            } else if !vec_hits.is_empty() {
                vec_hits
            } else if !curated_hits.is_empty() {
                curated_hits
            } else if !recency_hits.is_empty() {
                recency_hits
            } else {
                return Ok(vec![]);
            };
            return Ok(sole.into_iter().take(opts.k).collect());
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
pub(super) fn hits_to_pairs(hits: &[RetrievalHit]) -> Vec<(i64, f64)> {
    hits.iter()
        .filter_map(|h| {
            h.chunk_id
                .parse::<i64>()
                .ok()
                .map(|id| (id, h.score as f64))
        })
        .collect()
}
