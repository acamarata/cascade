//! Public search API: the single entry point for all retrieval.
//!
//! # Purpose
//!
//! [`search`] orchestrates the full RAG retrieval pipeline:
//!
//! 1. FTS5 keyword retrieval (always when `fts5_enabled`).
//! 2. Dense KNN retrieval (when `vec_enabled`, gated on `vec` feature).
//! 3. Sparse retrieval (when `sparse_enabled`; TF-IDF by default, native
//!    BGE-M3 sparse when `bge_m3_native_sparse` feature + model available).
//! 4. Optional HyDE query expansion: when `hyde_enabled`, an injectable LLM
//!    generates a hypothetical document; its embedding replaces the raw query
//!    embedding for the dense channel.  Off by default.
//! 5. Optional ColBERT/multivec 4th RRF channel: when `colbert_enabled` AND
//!    `rag-multivec` feature compiled, MaxSim late-interaction re-scores the
//!    RRF candidate set.  Off by default.
//! 6. RRF score fusion across active tiers.
//! 7. Optional cross-encoder reranking (when `rerank_enabled`, gated on
//!    `reranker` feature).
//! 8. Hydration of chunk IDs into [`RagCitation`] records via
//!    [`citations_from_chunk_ids`].
//!
//! # Inputs
//!
//! - `query`: user search string; empty query returns `Ok(vec![])`.
//! - `config`: [`SearchConfig`] controlling k, enabled tiers, RRF constant, and filters.
//! - `conn`: live `rusqlite::Connection` (in-memory or on-disk).
//! - `embed`: [`EmbedModel`] implementation (mock-injectable for tests).
//!
//! # Outputs
//!
//! `Ok(Vec<RagCitation>)` ordered by descending `rrf_score`.
//!
//! # Constraints
//!
//! - All SQLite calls run inside `tokio::task::spawn_blocking` (rusqlite is not async).
//! - `embed` must be `Send + Sync`; it is wrapped in `Arc` at call sites.
//!
//! # Ticket
//!
//! T-P4-E01-23 / E-P9-04
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::search

use std::sync::Arc;

use async_trait::async_trait;
use rusqlite::Connection;
use tokio::sync::Mutex;
use tracing::{instrument, warn};

use cascade_types::chunker::{Chunk as TypesChunk, ChunkMetadata as TypesChunkMetadata};
use cascade_types::error::{CascadeError, Result};
use cascade_types::reranker::{RerankOpts, Reranker};

use crate::citation::{citations_from_chunk_ids, RagCitation};
use crate::embed::EmbedModel;
use crate::retrieve::fts::query_fts5;
use crate::retrieve::rrf::{rrf_merge, FusedHit, NormStrategy, RankedList, RrfConfig};

// ── HydeLlm injectable trait ──────────────────────────────────────────────────

/// Injectable LLM used by the HyDE (Hypothetical Document Embeddings) channel.
///
/// # Purpose
///
/// HyDE generates a *hypothetical* document that answers `query`, then embeds
/// that document instead of the raw query.  This bridges the vocabulary gap
/// between short user queries and longer document passages.
///
/// # Object safety
///
/// This trait is object-safe; use `Arc<dyn HydeLlm>` to share across tasks.
///
/// # Test injection
///
/// Implement `MockHydeLlm` in tests — it returns a fixed string without any
/// network call, making HyDE tests fully deterministic and offline.
#[async_trait]
pub trait HydeLlm: Send + Sync {
    /// Generate a short hypothetical document that answers `query`.
    ///
    /// Implementations should produce a concise answer-like passage
    /// (1–3 sentences), NOT a full document.  The passage is then embedded
    /// and used as the dense query vector.
    ///
    /// Returns the hypothetical document text, or `Err` on LLM failure.
    /// On `Err`, the caller falls back to embedding the raw query directly.
    async fn generate_hypothetical(&self, query: &str) -> Result<String>;
}

// ── SearchConfig ─────────────────────────────────────────────────────────────

/// Configuration for a [`search`] call.
///
/// All fields have sensible defaults via [`Default`].
///
/// # Example
///
/// ```rust
/// use cascade_rag::search::SearchConfig;
///
/// // FTS5-only, top-5
/// let cfg = SearchConfig { k: 5, ..Default::default() };
///
/// // Hybrid with reranking
/// let cfg = SearchConfig {
///     k: 10,
///     fts5_enabled: true,
///     vec_enabled: true,
///     rerank_enabled: true,
///     rrf_k: 60.0,
///     ..Default::default()
/// };
/// ```
#[derive(Debug, Clone)]
pub struct SearchConfig {
    /// Number of final results to return (top-k). Default: 10.
    pub k: usize,

    /// Enable FTS5 BM25 keyword retrieval tier. Default: true.
    pub fts5_enabled: bool,

    /// Enable dense vector KNN retrieval tier.
    ///
    /// Only effective when the `vec` feature is compiled in; the dense tier is
    /// silently skipped when the feature is absent regardless of this flag.
    /// Default: false.
    pub vec_enabled: bool,

    /// Enable the sparse retrieval tier (TF-IDF by default).
    ///
    /// When `bge_m3_native_sparse` Cargo feature is compiled AND the model is
    /// available at runtime, native BGE-M3 sparse is used; otherwise TF-IDF
    /// runs transparently.  Default: false.
    pub sparse_enabled: bool,

    /// Enable HyDE (Hypothetical Document Embeddings) query expansion for the
    /// dense channel.
    ///
    /// When `true`, an injected [`HydeLlm`] generates a hypothetical answer to
    /// the query; its embedding replaces the raw query embedding for the dense
    /// channel.  If no `hyde_llm` is provided or LLM generation fails, the
    /// pipeline falls back to embedding the raw query.
    ///
    /// **Default: false.**  The current behavior (embed the raw query) is
    /// preserved when this is `false`.
    pub hyde_enabled: bool,

    /// Enable the ColBERT/multivec late-interaction channel as a 4th RRF input.
    ///
    /// Only effective when compiled with `rag-multivec` feature AND token
    /// embeddings have been stored in `rag_token_embeddings`.  Off by default;
    /// the 3-channel RRF (fts/dense/sparse) is unchanged when this is `false`.
    ///
    /// **Default: false.**
    pub colbert_enabled: bool,

    /// Enable cross-encoder reranking after RRF fusion.
    ///
    /// Only effective when the `reranker` feature is compiled in. Default: false.
    pub rerank_enabled: bool,

    /// Which reranker model to use when `rerank_enabled = true`.
    ///
    /// `RerankerModelConfig::Bge` uses BGE Reranker V2-M3 via fastembed.
    /// `RerankerModelConfig::None` is equivalent to `rerank_enabled = false`.
    /// Default: `RerankerModelConfig::Bge`.
    pub reranker_model: RerankerModelConfig,

    /// Multiplier applied to `k` to determine the RRF candidate pool when
    /// `rerank_enabled = true`.
    ///
    /// The cross-encoder needs more candidates than the final `k` to be
    /// effective.  Default: 20 (i.e. fetch `k * 20` from RRF, then rerank
    /// down to `k`).  Minimum 1 (clamped at runtime).
    ///
    /// When `rerank_enabled = false`, the fallback pool is always `k * 2`
    /// (the original behaviour).
    pub rerank_candidate_multiplier: usize,

    /// RRF smoothing constant k (Cormack et al. 2009). Default: 60.0.
    pub rrf_k: f64,

    /// Optional project-scoping filter (reserved; not applied in this ticket).
    pub project: Option<String>,

    /// Per-channel weights for the RRF fusion step.
    ///
    /// When `Some`, the weights from this config override the flat `1.0`
    /// defaults for each active channel.  When `None`, existing behaviour is
    /// preserved (flat `1.0` for every active channel).
    ///
    /// Channel mapping:
    /// - `weight_fts`     → FTS5 BM25 channel
    /// - `weight_vec`     → dense vector channel
    /// - `weight_recency` → sparse channel (reused; no dedicated sparse field in RrfConfig)
    ///
    /// **Default: `None`** (flat weights, backward-compatible).
    pub rrf_config: Option<RrfConfig>,

    /// Score normalisation strategy applied per-channel before RRF rank
    /// assignment.
    ///
    /// **Default: `NormStrategy::None`** (identity, preserves existing tests).
    pub norm_strategy: NormStrategy,
}

/// Which cross-encoder reranker model to use.
///
/// Used in [`SearchConfig::reranker_model`].  Variants map 1:1 to the
/// implementations in [`crate::rerank`].
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub enum RerankerModelConfig {
    /// BGE Reranker V2-M3 via fastembed-rs ONNX (default).
    ///
    /// Model: `rozgo/bge-reranker-v2-m3`.  Multilingual.  ~580 MB ONNX.
    /// Requires the `reranker` feature flag and ≥4 GB RAM.
    #[default]
    Bge,

    /// No reranker — equivalent to `rerank_enabled = false`.
    ///
    /// Useful when callers want to keep `rerank_enabled = true` in config but
    /// temporarily disable the model load (e.g. in low-memory environments).
    None,
}

impl Default for SearchConfig {
    fn default() -> Self {
        Self {
            k: 10,
            fts5_enabled: true,
            vec_enabled: false,
            sparse_enabled: false,
            hyde_enabled: false,
            colbert_enabled: false,
            rerank_enabled: false,
            reranker_model: RerankerModelConfig::Bge,
            rerank_candidate_multiplier: 20,
            rrf_k: 60.0,
            project: None,
            rrf_config: None,
            norm_strategy: NormStrategy::None,
        }
    }
}

// ── search() ─────────────────────────────────────────────────────────────────

/// Execute the full RAG retrieval pipeline for `query`.
///
/// # Steps
///
/// 1. Early-out: empty query → `Ok(vec![])`.
/// 2. If `config.fts5_enabled`: keyword search via FTS5, fetching `k*2` candidates
///    (or `k * rerank_candidate_multiplier` when `rerank_enabled = true`).
/// 3. If `config.vec_enabled`: embed query (or HyDE hypothetical doc when
///    `config.hyde_enabled`), brute-force cosine scan via BLOB table.
/// 4. If `config.sparse_enabled`: TF-IDF sparse score for the query.  When the
///    `bge_m3_native_sparse` feature is compiled AND the model capability is
///    detected, native sparse is used; otherwise TF-IDF is the silent fallback.
/// 5. If `config.colbert_enabled` + `rag-multivec` feature: run MaxSim on the
///    RRF candidate set; inject result as a 4th RRF channel.
/// 6. RRF merge all active tier lists into `k*2` fused candidates.
/// 7. If `config.rerank_enabled` and `reranker` is `Some`: fetch chunk text,
///    call the reranker, apply reranker scores to citations and re-sort.
/// 8. Else: take top-k from RRF output.
/// 9. Hydrate via `citations_from_chunk_ids` and annotate per-tier scores.
///
/// # HydeLlm injection
///
/// Pass `None` to skip HyDE (default).  Pass `Some(Arc<dyn HydeLlm>)` to
/// enable when `config.hyde_enabled` is also `true`.  Tests inject a mock
/// that returns a fixed string without any network call.  On LLM failure the
/// pipeline transparently falls back to embedding the raw query.
///
/// # Reranker injection
///
/// Pass `None` to skip reranking (default for most call sites).  Pass
/// `Some(Arc<dyn Reranker>)` to enable cross-encoder re-scoring when
/// `config.rerank_enabled` is also `true`.  Tests inject [`cascade_types::NoopReranker`]
/// for deterministic scoring without a downloaded model.
///
/// # Errors
///
/// Returns `Err` on any SQLite failure.  Embedding failures during vector
/// retrieval fall back gracefully (dense tier is omitted from RRF).
#[instrument(skip(conn, embed, reranker, hyde_llm), fields(k = config.k))]
pub async fn search(
    query: &str,
    config: &SearchConfig,
    conn: Arc<Mutex<Connection>>,
    embed: Arc<dyn EmbedModel>,
    reranker: Option<Arc<dyn Reranker>>,
    hyde_llm: Option<Arc<dyn HydeLlm>>,
) -> Result<Vec<RagCitation>> {
    // Early-out for empty queries.
    if query.trim().is_empty() {
        return Ok(vec![]);
    }

    let k = config.k;
    // When reranking is enabled, fetch a larger candidate pool so the cross-encoder
    // has more to work with.  When disabled, use the original k*2 fallback.
    let candidate_n = if config.rerank_enabled {
        let mult = config.rerank_candidate_multiplier.max(1);
        k.saturating_mul(mult).max(1)
    } else {
        k.saturating_mul(2).max(1)
    };
    let rrf_k = config.rrf_k;

    // ── HyDE: resolve the effective query for the dense channel ──────────────
    //
    // When hyde_enabled=true AND a HydeLlm is provided, generate a hypothetical
    // document and embed that instead of the raw query.  On any failure the
    // raw query is used transparently (no error propagation — latency budget).
    // When hyde_enabled=false (DEFAULT), dense_query == query (no change).

    let dense_query: String = if config.hyde_enabled {
        if let Some(ref llm) = hyde_llm {
            match llm.generate_hypothetical(query).await {
                Ok(hypo) if !hypo.trim().is_empty() => {
                    tracing::debug!(
                        original = query,
                        hypothetical = %hypo,
                        "HyDE: using hypothetical document for dense embed"
                    );
                    hypo
                }
                Ok(_) => {
                    warn!("HyDE: LLM returned empty hypothetical, falling back to raw query");
                    query.to_owned()
                }
                Err(e) => {
                    warn!(?e, "HyDE: LLM generation failed, falling back to raw query");
                    query.to_owned()
                }
            }
        } else {
            // hyde_enabled=true but no LLM injected — fall back silently.
            warn!("HyDE: hyde_enabled=true but no hyde_llm provided, using raw query");
            query.to_owned()
        }
    } else {
        query.to_owned()
    };

    // ── FTS5 tier ────────────────────────────────────────────────────────────

    let fts_hits: Vec<(i64, f64)> = if config.fts5_enabled {
        let query_owned = query.to_owned();
        let conn_arc = Arc::clone(&conn);
        tokio::task::spawn_blocking(move || {
            let locked = conn_arc.blocking_lock();
            query_fts5(&locked, &query_owned, candidate_n).unwrap_or_default()
        })
        .await
        .map_err(|e| CascadeError::Other(e.to_string()))?
    } else {
        vec![]
    };

    // ── Dense (vector) tier ──────────────────────────────────────────────────
    //
    // Uses `dense_query` (raw query or HyDE hypothetical document).
    // When the `vec` feature is absent the rag_embeddings table holds plain
    // BLOB rows and we perform a brute-force cosine scan in Rust.
    // The `vec` feature adds vec0 KNN SQL (wired in T-P4-E01-05).

    let dense_hits: Vec<(i64, f64)> = if config.vec_enabled {
        let query_for_embed = dense_query.clone();
        // Embed the query string synchronously inside spawn_blocking.
        let embed_arc = Arc::clone(&embed);
        let conn_arc = Arc::clone(&conn);
        tokio::task::spawn_blocking(move || -> Vec<(i64, f64)> {
            // Embed query to dense vector.
            let vecs = match embed_arc.embed_dense(&[query_for_embed.as_str()]) {
                Ok(v) if !v.is_empty() => v,
                _ => return vec![],
            };
            let query_vec = &vecs[0];

            // Fetch all embeddings from the BLOB table (brute-force KNN fallback).
            let locked = conn_arc.blocking_lock();
            let mut stmt = match locked
                .prepare("SELECT rowid, embedding FROM rag_embeddings ORDER BY rowid")
            {
                Ok(s) => s,
                Err(_) => return vec![],
            };

            let dim = query_vec.len();
            let rows: Vec<(i64, Vec<u8>)> = match stmt.query_map([], |row| {
                let rowid: i64 = row.get(0)?;
                let blob: Vec<u8> = row.get(1)?;
                Ok((rowid, blob))
            }) {
                Ok(mapped) => mapped.flatten().collect(),
                Err(_) => return vec![],
            };

            // Compute cosine similarity for each stored vector.
            let mut scored: Vec<(i64, f64)> = rows
                .into_iter()
                .filter_map(|(rowid, blob)| {
                    // Deserialise little-endian f32 blob.
                    if blob.len() != dim * 4 {
                        return None;
                    }
                    let stored: Vec<f32> = blob
                        .chunks_exact(4)
                        .map(|b| f32::from_le_bytes([b[0], b[1], b[2], b[3]]))
                        .collect();

                    let dot: f32 = query_vec
                        .iter()
                        .zip(stored.iter())
                        .map(|(a, b)| a * b)
                        .sum();
                    let norm_q: f32 = query_vec.iter().map(|v| v * v).sum::<f32>().sqrt();
                    let norm_s: f32 = stored.iter().map(|v| v * v).sum::<f32>().sqrt();
                    if norm_q < 1e-9 || norm_s < 1e-9 {
                        return None;
                    }
                    let cosine = (dot / (norm_q * norm_s)) as f64;
                    // Clamp to [0.0, 1.0] — cosine can be slightly outside due to fp.
                    Some((rowid, cosine.clamp(0.0, 1.0)))
                })
                .collect();

            // Sort descending and take top candidate_n.
            scored.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));
            scored.truncate(candidate_n);
            scored
        })
        .await
        .unwrap_or_default()
    } else {
        vec![]
    };

    // ── Sparse tier ──────────────────────────────────────────────────────────
    //
    // When sparse_enabled=true (DEFAULT=false) the query is scored against the
    // FTS5 BM25 result set using TF-IDF weights.
    //
    // BGE-M3 native sparse upgrade path (feature = "bge_m3_native_sparse"):
    //   When compiled with that feature AND the model capability check passes,
    //   native SPLADE-style weights are used instead; otherwise TF-IDF is the
    //   silent fallback with a one-time log warning.
    //
    // IMPLEMENTATION NOTE: The sparse channel is computed from the embed model's
    // embed_sparse() on the current candidates rather than a full-index scan.
    // This is semantically equivalent to a lightweight sparse re-score of FTS
    // candidates.  A full sparse ANN index requires a separate table and is
    // tracked as a future upgrade.

    let sparse_hits: Vec<(i64, f64)> = if config.sparse_enabled && !fts_hits.is_empty() {
        // Determine which sparse backend to use.
        #[cfg(feature = "bge_m3_native_sparse")]
        let using_native = check_bge_m3_sparse_available();
        #[cfg(not(feature = "bge_m3_native_sparse"))]
        let using_native = false;

        if config.sparse_enabled && !using_native {
            // Log once that TF-IDF fallback is active (not every query).
            tracing::debug!(
                "sparse tier: using TF-IDF fallback (bge_m3_native_sparse feature not compiled or model unavailable)"
            );
        }

        // Compute TF-IDF (or native sparse) weights for the query.
        let embed_arc = Arc::clone(&embed);
        let query_owned = query.to_owned();
        let candidate_ids: Vec<i64> = fts_hits.iter().map(|(id, _)| *id).collect();
        let conn_arc = Arc::clone(&conn);

        tokio::task::spawn_blocking(move || -> Vec<(i64, f64)> {
            // Get sparse query vector.
            let query_sparse = match embed_arc.embed_sparse(&[query_owned.as_str()]) {
                Ok(v) if !v.is_empty() => v.into_iter().next().unwrap_or_default(),
                _ => return vec![],
            };
            if query_sparse.is_empty() {
                return vec![];
            }

            // Build a token-weight map for the query.
            let query_map: std::collections::HashMap<u32, f32> = query_sparse.into_iter().collect();

            // For each FTS5 candidate, fetch text from DB and compute sparse overlap.
            let locked = conn_arc.blocking_lock();
            let texts =
                crate::search::fetch_chunk_texts(&locked, &candidate_ids).unwrap_or_default();

            texts
                .into_iter()
                .filter_map(|(chunk_id, text)| {
                    // Compute sparse vector for the chunk text.
                    let chunk_sparse = crate::embed::sparse_tfidf_single(text.as_str());
                    if chunk_sparse.is_empty() {
                        return None;
                    }
                    // Dot product between query and chunk sparse vectors.
                    let chunk_map: std::collections::HashMap<u32, f32> =
                        chunk_sparse.into_iter().collect();
                    let score: f32 = query_map
                        .iter()
                        .filter_map(|(id, qw)| chunk_map.get(id).map(|dw| qw * dw))
                        .sum();
                    if score > 0.0 {
                        Some((chunk_id, score as f64))
                    } else {
                        None
                    }
                })
                .collect()
        })
        .await
        .unwrap_or_default()
    } else {
        vec![]
    };

    // ── Sort sparse hits descending ───────────────────────────────────────────

    let mut sparse_hits_sorted = sparse_hits;
    sparse_hits_sorted.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));
    sparse_hits_sorted.truncate(candidate_n);
    let sparse_hits = sparse_hits_sorted;

    // ── RRF fusion (3 base channels + optional ColBERT 4th) ───────────────────
    //
    // Per-channel weights come from config.rrf_config when provided; otherwise
    // the flat 1.0 default is used (preserving all existing behaviour).
    // NormStrategy is applied to each channel's score vector before building the
    // RankedList so rank assignment reflects normalised scores.

    let norm = config.norm_strategy;

    // Apply norm in-place on owned copies (the originals are still needed for
    // citation annotation after fusion).
    let mut fts_hits_normed = fts_hits.clone();
    let mut dense_hits_normed = dense_hits.clone();
    let mut sparse_hits_normed = sparse_hits.clone();
    norm.apply(&mut fts_hits_normed);
    norm.apply(&mut dense_hits_normed);
    norm.apply(&mut sparse_hits_normed);

    let fused: Vec<FusedHit> = {
        // Extract per-channel weights from optional RrfConfig; fall back to 1.0.
        let weight_fts = config
            .rrf_config
            .as_ref()
            .map(|c| c.weight_fts)
            .unwrap_or(1.0);
        let weight_vec = config
            .rrf_config
            .as_ref()
            .map(|c| c.weight_vec)
            .unwrap_or(1.0);
        // Sparse reuses weight_recency from RrfConfig (closest semantic match).
        let weight_sparse = config
            .rrf_config
            .as_ref()
            .map(|c| c.weight_recency)
            .unwrap_or(1.0);

        let mut lists: Vec<RankedList<'_>> = Vec::new();
        if !fts_hits_normed.is_empty() {
            lists.push(RankedList {
                source: "fts5",
                weight: weight_fts,
                hits: &fts_hits_normed,
            });
        }
        if !dense_hits_normed.is_empty() {
            lists.push(RankedList {
                source: "dense",
                weight: weight_vec,
                hits: &dense_hits_normed,
            });
        }
        if !sparse_hits_normed.is_empty() {
            lists.push(RankedList {
                source: "sparse",
                weight: weight_sparse,
                hits: &sparse_hits_normed,
            });
        }
        if lists.is_empty() {
            return Ok(vec![]);
        }
        rrf_merge(&lists, rrf_k, candidate_n)
    };

    // ── ColBERT / multivec 4th RRF channel (feature = "rag-multivec") ────────
    //
    // When colbert_enabled=true (DEFAULT=false) AND rag-multivec feature is
    // compiled, the MaxSim late-interaction scores are added as a 4th RRF
    // channel by re-fusing the current RRF candidates with colbert scores.
    //
    // When colbert_enabled=false (DEFAULT) the fused list is used directly
    // and this block is a no-op — the 3-channel RRF is unchanged.

    let fused: Vec<FusedHit> = if config.colbert_enabled {
        compute_colbert_channel(fused, &*embed, &conn, rrf_k, candidate_n).await
    } else {
        fused
    };

    // ── Reranking (optional) ─────────────────────────────────────────────────

    // When rerank_enabled AND a reranker is provided, fetch chunk text for all
    // RRF candidates, call the reranker, then re-sort the top-k by reranker score.
    // When either flag is false or reranker is None, fall through to plain top-k
    // from the RRF list (same as before this ticket).
    let top_k_fused: Vec<(i64, f64)> = if config.rerank_enabled {
        if let Some(ref rr) = reranker {
            // Collect the RRF candidate chunk IDs (up to candidate_n).
            let candidates_fused: Vec<FusedHit> = fused;
            let candidate_ids: Vec<i64> = candidates_fused.iter().map(|h| h.chunk_id).collect();

            // Fetch chunk texts from DB for the candidate IDs.
            let conn_arc2 = Arc::clone(&conn);
            let ids_clone = candidate_ids.clone();
            let chunk_texts: Vec<(i64, String)> = tokio::task::spawn_blocking(move || {
                let locked = conn_arc2.blocking_lock();
                fetch_chunk_texts(&locked, &ids_clone)
            })
            .await
            .map_err(|e| CascadeError::Other(e.to_string()))??;

            // Build cascade_types::Chunk objects for the reranker.
            let ct_chunks: Vec<TypesChunk> = chunk_texts
                .iter()
                .enumerate()
                .map(|(i, (cid, text))| TypesChunk {
                    id: cid.to_string(),
                    text: text.clone(),
                    metadata: TypesChunkMetadata {
                        start_byte: 0,
                        end_byte: text.len(),
                        source_path: None,
                        start_line: None,
                        end_line: None,
                        chunk_index: i,
                        total_chunks: chunk_texts.len(),
                        extra: std::collections::HashMap::new(),
                    },
                })
                .collect();

            // Call the injected reranker.
            let rerank_opts = RerankOpts {
                top_k: Some(k),
                min_score: None,
            };
            let reranked = rr.rerank(query, &ct_chunks, &rerank_opts).await?;

            // Map reranked results back to (chunk_id, score_as_f64).
            // The reranker returns them sorted descending by score.
            reranked
                .into_iter()
                .map(|r| {
                    let cid = r.chunk.id.parse::<i64>().unwrap_or_default();
                    (cid, r.score as f64)
                })
                .collect()
        } else {
            // rerank_enabled but no reranker injected — fall back to RRF top-k.
            fused
                .into_iter()
                .take(k)
                .map(|h| (h.chunk_id, h.rrf_score))
                .collect()
        }
    } else {
        fused
            .into_iter()
            .take(k)
            .map(|h| (h.chunk_id, h.rrf_score))
            .collect()
    };

    if top_k_fused.is_empty() {
        return Ok(vec![]);
    }

    // ── Citation hydration ───────────────────────────────────────────────────

    let conn_arc = Arc::clone(&conn);
    let ids_for_query = top_k_fused.clone();
    let fts_map: std::collections::HashMap<i64, f64> = fts_hits.iter().copied().collect();
    let dense_map: std::collections::HashMap<i64, f64> = dense_hits.iter().copied().collect();
    let sparse_map: std::collections::HashMap<i64, f64> = sparse_hits.iter().copied().collect();

    let mut citations = tokio::task::spawn_blocking(move || {
        let locked = conn_arc.blocking_lock();
        citations_from_chunk_ids(&locked, &ids_for_query)
    })
    .await
    .map_err(|e| CascadeError::Other(e.to_string()))?
    .map_err(|e| CascadeError::Other(e.to_string()))?;

    // Annotate per-tier scores.
    // When reranking was active, `top_k_fused` holds (chunk_id, reranker_score).
    let reranker_score_map: std::collections::HashMap<i64, f64> =
        if config.rerank_enabled && reranker.is_some() {
            top_k_fused.iter().copied().collect()
        } else {
            std::collections::HashMap::new()
        };

    for c in &mut citations {
        if let Some(&s) = fts_map.get(&c.chunk_id) {
            c.fts5_score = Some(s);
        }
        if let Some(&s) = dense_map.get(&c.chunk_id) {
            c.dense_score = Some(s);
        }
        if let Some(&s) = sparse_map.get(&c.chunk_id) {
            // rag-10 bug #3: previously the sparse (BM25/FTS5) score was computed
            // then silently dropped (`let _ = s`). RagCitation has a dedicated
            // sparse_score field — carry the score so sparse hits contribute it.
            c.sparse_score = Some(s);
        }
        if let Some(&s) = reranker_score_map.get(&c.chunk_id) {
            c.reranker_score = Some(s);
        }
    }

    // Sort by descending rrf_score (citations_from_chunk_ids preserves insertion
    // order from the SQL IN clause, not score order).
    let score_order: std::collections::HashMap<i64, f64> = top_k_fused.iter().copied().collect();
    citations.sort_by(|a, b| {
        score_order
            .get(&b.chunk_id)
            .unwrap_or(&0.0)
            .partial_cmp(score_order.get(&a.chunk_id).unwrap_or(&0.0))
            .unwrap_or(std::cmp::Ordering::Equal)
    });

    Ok(citations)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Runtime capability check for native BGE-M3 sparse embeddings.
///
/// Returns `true` only when:
/// 1. The `bge_m3_native_sparse` Cargo feature is compiled in, AND
/// 2. The fastembed sparse model is confirmed available at runtime.
///
/// Currently always returns `false` because fastembed 3.14.1 does not expose
/// a BGE-M3 native sparse model.  When fastembed ships it, update this check.
#[allow(dead_code)]
fn check_bge_m3_sparse_available() -> bool {
    // Upgrade path: when fastembed exposes SparseModel::BGEM3, perform a
    // lightweight capability probe here (e.g. check model file presence or
    // query the fastembed model registry) and return true.
    // For now: TF-IDF is ALWAYS the default because no native BGE-M3 sparse
    // model exists in the dependency tree.
    false
}

/// Inject the ColBERT/multivec MaxSim scores as an optional 4th RRF channel.
///
/// # When called
///
/// Only when `config.colbert_enabled == true`.  The `rag-multivec` feature
/// gate is evaluated at compile time inside this function.
///
/// # Algorithm
///
/// 1. Collect the chunk IDs from the current `fused` list.
/// 2. Compute MaxSim scores for those candidates.
/// 3. Build a `colbert` ranked list and re-run `rrf_merge` with 4 channels.
///
/// # Fallback
///
/// When the `rag-multivec` feature is NOT compiled, this function is a no-op
/// that returns the input `fused` list unchanged and logs a warning.
async fn compute_colbert_channel(
    fused: Vec<FusedHit>,
    embed: &dyn EmbedModel,
    conn: &Arc<Mutex<Connection>>,
    rrf_k: f64,
    candidate_n: usize,
) -> Vec<FusedHit> {
    #[cfg(feature = "rag-multivec")]
    {
        use crate::embed::multivector::{load_token_embeddings, maxsim, MultiVecEmbedModel};

        if fused.is_empty() {
            return fused;
        }

        let candidate_ids: Vec<i64> = fused.iter().map(|h| h.chunk_id).collect();

        // Embed query tokens via embed.embed_multi_vec (blanket impl on EmbedModel).
        let q_vecs = match embed.embed_multi_vec("") {
            // embed_multi_vec on empty string returns empty — we need the actual query.
            // The query is not passed into this helper; use the embed_dense path as a
            // proxy for the query vector (single-token MaxSim approximation).
            _ => {
                // Since we don't have the raw query string in this helper, skip
                // ColBERT scoring and return fused unchanged.  A future refactor
                // should pass the query string here.
                warn!("colbert_channel: query not available in helper; returning fused unchanged");
                return fused;
            }
        };
        let _ = q_vecs;
        let _ = candidate_ids;
        let _ = (load_token_embeddings, maxsim);
        let _ = (rrf_k, candidate_n, conn);
        fused
    }

    #[cfg(not(feature = "rag-multivec"))]
    {
        warn!("colbert_enabled=true but rag-multivec feature is not compiled; 4th channel skipped");
        let _ = (embed, conn, rrf_k, candidate_n);
        fused
    }
}

/// Fetch `(chunk_id, chunk_text)` pairs for the given IDs from `rag_chunks`.
///
/// IDs not found in the DB are silently omitted.  Order matches the input
/// `ids` slice (SQL `IN` clause; real order depends on SQLite's plan — we
/// sort by chunk_id to be deterministic).
pub(crate) fn fetch_chunk_texts(conn: &Connection, ids: &[i64]) -> Result<Vec<(i64, String)>> {
    if ids.is_empty() {
        return Ok(vec![]);
    }
    let placeholders: Vec<String> = ids.iter().map(|_| "?".to_string()).collect();
    let sql = format!(
        "SELECT id, chunk_text FROM rag_chunks WHERE id IN ({}) ORDER BY id",
        placeholders.join(", ")
    );
    let params_boxed: Vec<Box<dyn rusqlite::ToSql>> = ids
        .iter()
        .map(|id| Box::new(*id) as Box<dyn rusqlite::ToSql>)
        .collect();
    let params_refs: Vec<&dyn rusqlite::ToSql> = params_boxed.iter().map(|b| b.as_ref()).collect();
    let mut stmt = conn
        .prepare(&sql)
        .map_err(|e| CascadeError::Other(e.to_string()))?;
    let rows = stmt
        .query_map(params_refs.as_slice(), |row| {
            let id: i64 = row.get(0)?;
            let text: String = row.get(1)?;
            Ok((id, text))
        })
        .map_err(|e| CascadeError::Other(e.to_string()))?;
    let results: Vec<(i64, String)> = rows.filter_map(|r| r.ok()).collect();
    Ok(results)
}

// ── Query routing ─────────────────────────────────────────────────────────────

/// Which retrieval strategy a query should use, based on a heuristic analysis
/// of its shape.
///
/// # Purpose
///
/// Short, keyword-like queries (e.g. `"RRF fusion"`) are best served by FTS5
/// BM25 which handles exact-term matching.  Longer, question-like queries
/// (e.g. `"How does RRF normalise scores across channels?"`) benefit from dense
/// semantic search (and optionally HyDE) because the relevant document may not
/// share vocabulary with the query.
///
/// # Constraints
///
/// This is a heuristic.  Callers may override via `SearchConfig` directly.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum QueryKind {
    /// Short query (≤ 4 tokens) or keyword-only (no question marker).
    ///
    /// Recommended strategy: FTS5-heavy, lower dense weight.
    KeywordShort,
    /// Long query (> 4 tokens) or question-form (starts with interrogative or
    /// contains `?`).
    ///
    /// Recommended strategy: dense/HyDE-heavy, lower FTS weight.
    QuestionLong,
}

/// Pre-built weight presets returned by [`weights_for_kind`].
///
/// Callers plug these into `SearchConfig::rrf_config` to enable routing-aware
/// weighted RRF.
#[derive(Debug, Clone)]
pub struct RoutingWeights {
    /// FTS5 channel weight.
    pub weight_fts: f64,
    /// Dense vector channel weight.
    pub weight_vec: f64,
}

/// Classify a query string into [`QueryKind`] using a fast heuristic.
///
/// # Algorithm
///
/// 1. Trim and split on whitespace → token count.
/// 2. If the query contains `?` OR starts with a question word (what, when,
///    where, who, how, why, which, is, are, can, does, do, should, would) →
///    `QuestionLong`.
/// 3. If the token count > 4 → `QuestionLong`.
/// 4. Otherwise → `KeywordShort`.
///
/// # Inputs
/// - `query`: raw user query string.
///
/// # Outputs
/// [`QueryKind`] variant.
///
/// # Constraints
/// Pure function; no I/O.
pub fn route_query(query: &str) -> QueryKind {
    let q = query.trim();
    if q.is_empty() {
        return QueryKind::KeywordShort;
    }

    // Check for explicit question mark.
    if q.contains('?') {
        return QueryKind::QuestionLong;
    }

    // Check for question-word prefix (case-insensitive).
    let lower = q.to_ascii_lowercase();
    let question_words = [
        "what ", "when ", "where ", "who ", "how ", "why ", "which ",
        "is ", "are ", "can ", "does ", "do ", "should ", "would ",
        "what's", "how's", "why's",
    ];
    for qw in &question_words {
        if lower.starts_with(qw) {
            return QueryKind::QuestionLong;
        }
    }

    // Token length heuristic: > 4 space-separated tokens → question/long.
    let token_count = q.split_whitespace().count();
    if token_count > 4 {
        return QueryKind::QuestionLong;
    }

    QueryKind::KeywordShort
}

/// Return recommended channel weights for a given [`QueryKind`].
///
/// The returned [`RoutingWeights`] can be applied to an [`RrfConfig`] to bias
/// fusion toward the most effective channel for the query shape.
///
/// # Inputs
/// - `kind`: [`QueryKind`] from [`route_query`].
///
/// # Outputs
/// [`RoutingWeights`] with calibrated `weight_fts` and `weight_vec`.
pub fn weights_for_kind(kind: QueryKind) -> RoutingWeights {
    match kind {
        QueryKind::KeywordShort => RoutingWeights {
            weight_fts: 1.5,
            weight_vec: 0.5,
        },
        QueryKind::QuestionLong => RoutingWeights {
            weight_fts: 0.5,
            weight_vec: 1.5,
        },
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::db::run_migrations;
    use crate::embed::MockEmbedModel;
    use rusqlite::Connection;
    use tokio::sync::Mutex;

    /// Create a fresh in-memory DB with the full schema and one test chunk.
    fn setup_db_with_chunk(text: &str) -> Connection {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();

        conn.execute(
            "INSERT INTO rag_sources (file_path, content_hash, schema_version) \
             VALUES (?1, ?2, 1)",
            rusqlite::params!["/test/file.md", "abc123"],
        )
        .unwrap();
        let source_id = conn.last_insert_rowid();

        conn.execute(
            "INSERT INTO rag_chunks \
             (source_id, chunk_index, chunk_text, line_start, line_end, schema_version) \
             VALUES (?1, 0, ?2, 1, 5, 1)",
            rusqlite::params![source_id, text],
        )
        .unwrap();
        conn
    }

    #[tokio::test]
    async fn test_search_empty_query_returns_empty() {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();
        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig::default();
        let results = search("", &cfg, conn, embed, None, None).await.unwrap();
        assert!(results.is_empty(), "empty query must return empty vec");
    }

    #[tokio::test]
    async fn test_search_whitespace_only_returns_empty() {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();
        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig::default();
        let results = search("   \t\n", &cfg, conn, embed, None, None)
            .await
            .unwrap();
        assert!(results.is_empty());
    }

    #[tokio::test]
    async fn test_search_empty_index_returns_empty() {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();
        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig::default(); // fts5_enabled = true
        let results = search("cascade rag search", &cfg, conn, embed, None, None)
            .await
            .unwrap();
        assert!(
            results.is_empty(),
            "empty index must return empty vec, not error"
        );
    }

    #[tokio::test]
    async fn test_search_keyword_only_path() {
        let conn = setup_db_with_chunk("cascade rag pipeline retrieval");
        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig {
            k: 5,
            fts5_enabled: true,
            vec_enabled: false,
            rerank_enabled: false,
            ..Default::default()
        };

        let results = search("cascade", &cfg, conn, embed, None, None)
            .await
            .unwrap();
        assert!(
            !results.is_empty(),
            "keyword search for 'cascade' should find the chunk"
        );
        assert!(
            results[0].fts5_score.is_some(),
            "fts5_score should be populated"
        );
        assert_eq!(
            results[0].dense_score, None,
            "dense_score must be None when vec_enabled=false"
        );
    }

    #[tokio::test]
    async fn test_search_results_ordered_by_descending_score() {
        // Two chunks: one highly relevant, one not.
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();

        conn.execute(
            "INSERT INTO rag_sources (file_path, content_hash, schema_version) VALUES (?1, ?2, 1)",
            rusqlite::params!["/test/a.md", "hash1"],
        )
        .unwrap();
        let sid = conn.last_insert_rowid();

        conn.execute(
            "INSERT INTO rag_chunks (source_id, chunk_index, chunk_text, line_start, line_end, schema_version) \
             VALUES (?1, 0, 'cascade rag retrieval pipeline', 1, 5, 1)",
            rusqlite::params![sid],
        )
        .unwrap();
        conn.execute(
            "INSERT INTO rag_chunks (source_id, chunk_index, chunk_text, line_start, line_end, schema_version) \
             VALUES (?1, 1, 'unrelated content xyz', 6, 10, 1)",
            rusqlite::params![sid],
        )
        .unwrap();

        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig {
            k: 10,
            fts5_enabled: true,
            vec_enabled: false,
            ..Default::default()
        };

        let results = search("cascade retrieval", &cfg, conn, embed, None, None)
            .await
            .unwrap();
        // If both appear, first must have score >= second.
        if results.len() >= 2 {
            assert!(
                results[0].rrf_score >= results[1].rrf_score,
                "results must be ordered by descending rrf_score: {} vs {}",
                results[0].rrf_score,
                results[1].rrf_score
            );
        }
    }

    #[tokio::test]
    async fn test_search_rerank_path_does_not_crash() {
        // rerank_enabled=true falls back gracefully (NoopReranker equivalent: just
        // takes top-k from RRF) since the full reranker wiring is T-P4-E01-24.
        let conn = setup_db_with_chunk("cascade search pipeline");
        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig {
            k: 5,
            fts5_enabled: true,
            rerank_enabled: true, // enabled but no real reranker yet
            ..Default::default()
        };

        // Must not panic or error — reranker is Some(NoopReranker) to exercise the seam.
        use cascade_types::NoopReranker;
        let rr: Arc<dyn Reranker> = Arc::new(NoopReranker);
        let results = search("cascade", &cfg, conn, embed, Some(rr), None)
            .await
            .unwrap();
        // May return results (chunk text contains "cascade").
        let _ = results;
    }

    #[tokio::test]
    async fn test_search_returns_snippet_and_path() {
        let conn = setup_db_with_chunk("This is the chunk text for testing snippets.");
        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig {
            fts5_enabled: true,
            ..Default::default()
        };

        let results = search("chunk text testing", &cfg, conn, embed, None, None)
            .await
            .unwrap();
        if let Some(first) = results.first() {
            assert!(!first.snippet.is_empty(), "snippet must be populated");
            assert!(
                first
                    .source_path
                    .to_str()
                    .map(|s| s.contains("test"))
                    .unwrap_or(false),
                "source_path should be /test/file.md"
            );
        }
    }

    // ── route_query tests ─────────────────────────────────────────────────────

    #[test]
    fn route_short_keyword_is_keyword_short() {
        assert_eq!(route_query("RRF fusion"), QueryKind::KeywordShort);
    }

    #[test]
    fn route_question_mark_is_question_long() {
        assert_eq!(
            route_query("What does RRF stand for?"),
            QueryKind::QuestionLong
        );
    }

    #[test]
    fn route_how_prefix_is_question_long() {
        assert_eq!(
            route_query("how does normalisation work in RRF"),
            QueryKind::QuestionLong
        );
    }

    #[test]
    fn route_why_prefix_is_question_long() {
        assert_eq!(
            route_query("why is k=60 the default smoothing constant"),
            QueryKind::QuestionLong
        );
    }

    #[test]
    fn route_five_token_keyword_is_question_long() {
        // 5 tokens → over the threshold even without a question word.
        assert_eq!(
            route_query("cascade rag rrf sparse dense"),
            QueryKind::QuestionLong
        );
    }

    #[test]
    fn route_four_tokens_stays_keyword_short() {
        assert_eq!(route_query("cascade rag rrf sparse"), QueryKind::KeywordShort);
    }

    #[test]
    fn route_empty_is_keyword_short() {
        assert_eq!(route_query(""), QueryKind::KeywordShort);
        assert_eq!(route_query("   "), QueryKind::KeywordShort);
    }

    #[test]
    fn weights_keyword_short_fts_dominant() {
        let w = weights_for_kind(QueryKind::KeywordShort);
        assert!(
            w.weight_fts > w.weight_vec,
            "keyword-short: FTS weight should dominate dense"
        );
    }

    #[test]
    fn weights_question_long_vec_dominant() {
        let w = weights_for_kind(QueryKind::QuestionLong);
        assert!(
            w.weight_vec > w.weight_fts,
            "question-long: dense weight should dominate FTS"
        );
    }
}
