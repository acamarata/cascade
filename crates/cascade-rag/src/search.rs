//! Public search API: the single entry point for all retrieval.
//!
//! # Purpose
//!
//! [`search`] orchestrates the full RAG retrieval pipeline:
//!
//! 1. FTS5 keyword retrieval (always when `fts5_enabled`).
//! 2. Dense KNN retrieval (when `vec_enabled`, gated on `vec` feature).
//! 3. RRF score fusion across active tiers.
//! 4. Optional cross-encoder reranking (when `rerank_enabled`, gated on `reranker` feature).
//! 5. Hydration of chunk IDs into [`RagCitation`] records via [`citations_from_chunk_ids`].
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
//! T-P4-E01-23
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::search

use std::sync::Arc;

use rusqlite::Connection;
use tokio::sync::Mutex;
use tracing::instrument;

use cascade_types::chunker::{Chunk as TypesChunk, ChunkMetadata as TypesChunkMetadata};
use cascade_types::error::{CascadeError, Result};
use cascade_types::reranker::{RerankOpts, Reranker};

use crate::citation::{citations_from_chunk_ids, RagCitation};
use crate::embed::EmbedModel;
use crate::retrieve::fts::query_fts5;
use crate::retrieve::rrf::{rrf_merge, FusedHit, RankedList};

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

    /// Enable cross-encoder reranking after RRF fusion.
    ///
    /// Only effective when the `reranker` feature is compiled in. Default: false.
    pub rerank_enabled: bool,

    /// RRF smoothing constant k (Cormack et al. 2009). Default: 60.0.
    pub rrf_k: f64,

    /// Optional project-scoping filter (reserved; not applied in this ticket).
    pub project: Option<String>,
}

impl Default for SearchConfig {
    fn default() -> Self {
        Self {
            k: 10,
            fts5_enabled: true,
            vec_enabled: false,
            rerank_enabled: false,
            rrf_k: 60.0,
            project: None,
        }
    }
}

// ── search() ─────────────────────────────────────────────────────────────────

/// Execute the full RAG retrieval pipeline for `query`.
///
/// # Steps
///
/// 1. Early-out: empty query → `Ok(vec![])`.
/// 2. If `config.fts5_enabled`: keyword search via FTS5, fetching `k*2` candidates.
/// 3. If `config.vec_enabled` + `vec` feature: embed query, brute-force cosine
///    scan via `rag_embeddings` BLOB table, fetching `k*2` candidates.
/// 4. RRF merge all active tier lists into `k*2` fused candidates.
/// 5. If `config.rerank_enabled` and `reranker` is `Some`: fetch chunk text from DB,
///    call the reranker, apply reranker scores to citations and re-sort.
/// 6. Else: take top-k from RRF output.
/// 7. Hydrate via `citations_from_chunk_ids` and annotate per-tier scores.
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
#[instrument(skip(conn, embed, reranker), fields(k = config.k))]
pub async fn search(
    query: &str,
    config: &SearchConfig,
    conn: Arc<Mutex<Connection>>,
    embed: Arc<dyn EmbedModel>,
    reranker: Option<Arc<dyn Reranker>>,
) -> Result<Vec<RagCitation>> {
    // Early-out for empty queries.
    if query.trim().is_empty() {
        return Ok(vec![]);
    }

    let k = config.k;
    let candidate_n = k.saturating_mul(2).max(1);
    let rrf_k = config.rrf_k;

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
    // When the `vec` feature is absent the rag_embeddings table holds plain
    // BLOB rows and we perform a brute-force cosine scan in Rust.
    // The `vec` feature adds vec0 KNN SQL (wired in T-P4-E01-05).

    let dense_hits: Vec<(i64, f64)> = if config.vec_enabled {
        let query_owned = query.to_owned();
        // Embed the query string synchronously inside spawn_blocking.
        let embed_arc = Arc::clone(&embed);
        let conn_arc = Arc::clone(&conn);
        tokio::task::spawn_blocking(move || -> Vec<(i64, f64)> {
            // Embed query to dense vector.
            let vecs = match embed_arc.embed_dense(&[query_owned.as_str()]) {
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

    // ── RRF fusion ───────────────────────────────────────────────────────────

    let fused: Vec<FusedHit> = {
        let mut lists: Vec<RankedList<'_>> = Vec::new();
        if !fts_hits.is_empty() {
            lists.push(RankedList {
                source: "fts5",
                weight: 1.0,
                hits: &fts_hits,
            });
        }
        if !dense_hits.is_empty() {
            lists.push(RankedList {
                source: "dense",
                weight: 1.0,
                hits: &dense_hits,
            });
        }
        if lists.is_empty() {
            return Ok(vec![]);
        }
        rrf_merge(&lists, rrf_k, candidate_n)
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

/// Fetch `(chunk_id, chunk_text)` pairs for the given IDs from `rag_chunks`.
///
/// IDs not found in the DB are silently omitted.  Order matches the input
/// `ids` slice (SQL `IN` clause; real order depends on SQLite's plan — we
/// sort by chunk_id to be deterministic).
fn fetch_chunk_texts(conn: &Connection, ids: &[i64]) -> Result<Vec<(i64, String)>> {
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
        let results = search("", &cfg, conn, embed, None).await.unwrap();
        assert!(results.is_empty(), "empty query must return empty vec");
    }

    #[tokio::test]
    async fn test_search_whitespace_only_returns_empty() {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();
        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig::default();
        let results = search("   \t\n", &cfg, conn, embed, None).await.unwrap();
        assert!(results.is_empty());
    }

    #[tokio::test]
    async fn test_search_empty_index_returns_empty() {
        let conn = Connection::open_in_memory().unwrap();
        run_migrations(&conn).unwrap();
        let conn = Arc::new(Mutex::new(conn));
        let embed: Arc<dyn EmbedModel> = Arc::new(MockEmbedModel::new(16));

        let cfg = SearchConfig::default(); // fts5_enabled = true
        let results = search("cascade rag search", &cfg, conn, embed, None)
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

        let results = search("cascade", &cfg, conn, embed, None).await.unwrap();
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

        let results = search("cascade retrieval", &cfg, conn, embed, None)
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
        let results = search("cascade", &cfg, conn, embed, Some(rr))
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

        let results = search("chunk text testing", &cfg, conn, embed, None)
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
}
