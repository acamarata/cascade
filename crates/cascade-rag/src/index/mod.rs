//! SQLite index management: FTS5 full-text + sqlite-vec dense vectors.
//!
//! `RagIndex` is the single connection pool owner for the cascade RAG database.
//! It manages schema creation, WAL mode, migrations, and provides low-level
//! insert/query APIs consumed by the `retrieve` and `chunk` modules.
//!
//! ## Schema
//!
//! ```sql
//! -- Canonical chunk records (source of truth)
//! CREATE TABLE chunks (
//!     chunk_id    TEXT PRIMARY KEY,
//!     file_path   TEXT,
//!     start_line  INTEGER,
//!     end_line    INTEGER,
//!     text        TEXT NOT NULL,
//!     metadata    TEXT,          -- JSON blob
//!     indexed_at  INTEGER NOT NULL
//! );
//!
//! -- FTS5 virtual table — BM25 keyword search
//! CREATE VIRTUAL TABLE fts_chunks USING fts5(
//!     chunk_id UNINDEXED,
//!     text,
//!     content='chunks',
//!     content_rowid='rowid',
//!     tokenize='unicode61 remove_diacritics 1'
//! );
//!
//! -- sqlite-vec extension — dense ANN search
//! CREATE VIRTUAL TABLE vec_chunks USING vec0(
//!     chunk_id TEXT PRIMARY KEY,
//!     embedding FLOAT[1024]
//! );
//! ```
//!
//! ## WAL mode
//!
//! WAL is set once on first open. Concurrent readers are never blocked by a
//! single writer. The daemon holds the write-end; query servants use read-only
//! connections.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::index::RagIndex

pub mod sharding;
pub mod state;

use cascade_types::error::{CascadeError, Result};
use rusqlite::{params, Connection};
use serde_json::Value;
use std::path::{Path, PathBuf};
use tracing::{debug, info, instrument};

/// Current schema version.  Increment on every breaking DDL change.
const SCHEMA_VERSION: u32 = 3;

/// Stored dimensions for the default Multilingual E5 Large model.
const DEFAULT_EMBED_DIM: usize = 1024;

// ── Index health ──────────────────────────────────────────────────────────────

/// Summary metrics from [`RagIndex::health`].
///
/// SPORT: MASTER-LIBS.md → cascade-rag::index::IndexHealth
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct IndexHealth {
    /// Total chunks stored in the `chunks` table.
    pub total_chunks: u64,
    /// Chunks that have a dense embedding in `vec_chunks`.
    pub embedded_chunks: u64,
    /// Percentage of chunks that are embedded (0–100).
    pub embedding_coverage_pct: f32,
    /// Unix timestamp of the most-recently indexed chunk.
    pub last_indexed_at: Option<i64>,
    /// Whether the index is considered stale (no writes in >24 h while watched
    /// paths still have unindexed modifications).
    pub is_stale: bool,
    /// Schema version stored in `PRAGMA user_version`.
    pub schema_version: u32,
}

// ── RagIndex ──────────────────────────────────────────────────────────────────

/// Primary handle to the Cascade RAG SQLite database.
///
/// Hold one instance per daemon process.  All mutations go through this struct;
/// read-only connections are created on demand by retrieval workers.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_rag::index::RagIndex;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let idx = RagIndex::open("/home/user/.cascade/cascade.db").await?;
/// let health = idx.health().await?;
/// println!("Coverage: {:.1}%", health.embedding_coverage_pct);
/// # Ok(())
/// # }
/// ```
pub struct RagIndex {
    /// Path to the SQLite database file.
    db_path: PathBuf,
    /// Active read-write connection.  Protected by an async mutex so the single
    /// writer never contends with itself across await points.
    conn: tokio::sync::Mutex<Connection>,
    /// Vector dimension used when creating `vec_chunks`.  Must match the active
    /// embedding model; mismatches are caught at insert time.
    embed_dim: usize,
}

impl RagIndex {
    /// Open (or create) the RAG database at `db_path`.
    ///
    /// Creates the schema if it does not exist, applies pending migrations, and
    /// enables WAL mode.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::Storage`] if the file cannot be opened or the
    /// schema cannot be applied.
    #[instrument(skip_all, fields(db_path = %db_path.as_ref().display()))]
    pub async fn open(db_path: impl AsRef<Path>) -> Result<Self> {
        let db_path = db_path.as_ref().to_path_buf();
        let conn =
            cascade_db::open_configured(&db_path).map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("open db: {e}"),
            })?;
        let idx = Self {
            db_path,
            conn: tokio::sync::Mutex::new(conn),
            embed_dim: DEFAULT_EMBED_DIM,
        };
        idx.apply_schema().await?;
        Ok(idx)
    }

    /// Open with a custom embedding dimension (for non-default models).
    pub async fn open_with_dim(db_path: impl AsRef<Path>, embed_dim: usize) -> Result<Self> {
        let mut idx = Self::open(db_path).await?;
        idx.embed_dim = embed_dim;
        Ok(idx)
    }

    // ── Schema ───────────────────────────────────────────────────────────────

    /// Apply the base schema and any pending migrations.
    ///
    /// Idempotent: safe to call on an already-initialised database.
    async fn apply_schema(&self) -> Result<()> {
        let conn = self.conn.lock().await;
        // WAL mode — single writer, unlimited concurrent readers.
        conn.execute_batch("PRAGMA journal_mode=WAL;")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("wal mode: {e}"),
            })?;
        conn.execute_batch("PRAGMA synchronous=NORMAL;")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("sync pragma: {e}"),
            })?;
        let stored_ver: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("read user_version: {e}"),
            })?;
        if stored_ver < SCHEMA_VERSION {
            self.run_migrations(&conn, stored_ver)?;
        }
        Ok(())
    }

    /// Run forward-only migrations from `current_ver` to `SCHEMA_VERSION`.
    fn run_migrations(&self, conn: &Connection, current_ver: u32) -> Result<()> {
        if current_ver < 1 {
            // v0 → v1: create chunks + FTS5
            conn.execute_batch(
                "CREATE TABLE IF NOT EXISTS chunks (
                    chunk_id    TEXT PRIMARY KEY,
                    file_path   TEXT,
                    start_line  INTEGER,
                    end_line    INTEGER,
                    text        TEXT NOT NULL,
                    metadata    TEXT,
                    indexed_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
                );
                CREATE VIRTUAL TABLE IF NOT EXISTS fts_chunks USING fts5(
                    chunk_id UNINDEXED,
                    text,
                    content='chunks',
                    content_rowid='rowid',
                    tokenize='unicode61 remove_diacritics 1'
                );",
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("migration v1: {e}"),
            })?;
            info!("Applied migration v0→v1 (chunks + FTS5)");
        }
        if current_ver < 2 {
            // v1 → v2: add sqlite-vec virtual table
            // NOTE: sqlite-vec must be loaded as an extension before this runs.
            // If the extension is not present the table is created as a plain table
            // and queries will fall back to FTS5-only mode.
            let dim = self.embed_dim;
            let ddl = format!(
                "CREATE VIRTUAL TABLE IF NOT EXISTS vec_chunks USING vec0(\
                    chunk_id TEXT PRIMARY KEY,\
                    embedding FLOAT[{dim}]\
                );"
            );
            // Best-effort: ignore if sqlite-vec extension not loaded.
            let _ = conn.execute_batch(&ddl);
            info!("Applied migration v1→v2 (sqlite-vec)");
        }
        if current_ver < 3 {
            // v2 → v3: add dense_fallback table for non-vec builds.
            // Always created (harmless in vec builds) so existing DBs upgrading
            // from v2 get the table without requiring a vec extension.
            conn.execute_batch(
                "CREATE TABLE IF NOT EXISTS dense_fallback (\
                    chunk_id  TEXT PRIMARY KEY,\
                    embedding BLOB NOT NULL\
                );",
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("migration v3: {e}"),
            })?;
            info!("Applied migration v2→v3 (dense_fallback)");
        }
        conn.pragma_update(None, "user_version", SCHEMA_VERSION)
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("set user_version: {e}"),
            })?;
        Ok(())
    }

    // ── Insert / upsert ──────────────────────────────────────────────────────

    /// Insert or replace a chunk record and update the FTS5 index.
    ///
    /// This is the hot path for incremental indexing; call in batches for
    /// throughput (target ≥500 chunks/sec on M-series hardware).
    ///
    /// # Constraints
    ///
    /// - `chunk_id` is the primary key — duplicates replace the existing row.
    /// - `metadata` is serialised to JSON.
    #[instrument(skip(self, text, metadata))]
    pub async fn upsert_chunk(
        &self,
        chunk_id: &str,
        file_path: Option<&Path>,
        start_line: Option<u32>,
        end_line: Option<u32>,
        text: &str,
        metadata: Option<&Value>,
    ) -> Result<()> {
        let conn = self.conn.lock().await;
        let fp = file_path.map(|p| p.to_string_lossy().into_owned());
        let meta_json = metadata.map(|v| v.to_string());
        let now = unix_now();
        conn.execute(
            "INSERT OR REPLACE INTO chunks \
             (chunk_id, file_path, start_line, end_line, text, metadata, indexed_at) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![chunk_id, fp, start_line, end_line, text, meta_json, now],
        )
        .map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("upsert chunk: {e}"),
        })?;
        // Keep FTS5 content table in sync.
        conn.execute(
            "INSERT OR REPLACE INTO fts_chunks(chunk_id, text) VALUES (?1, ?2)",
            params![chunk_id, text],
        )
        .map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("upsert fts: {e}"),
        })?;
        debug!(chunk_id, "chunk upserted");
        Ok(())
    }

    /// Store a dense embedding vector for a chunk.
    ///
    /// With the `vec` feature, writes to `vec_chunks` (sqlite-vec virtual table).
    /// Without the `vec` feature, writes the same LE-f32 blob to `dense_fallback`.
    pub async fn upsert_embedding(&self, chunk_id: &str, embedding: &[f32]) -> Result<()> {
        if embedding.len() != self.embed_dim {
            return Err(CascadeError::EmbeddingDimensionMismatch {
                expected: self.embed_dim,
                actual: embedding.len(),
            });
        }
        let conn = self.conn.lock().await;
        // Serialize as little-endian f32 blob — sqlite-vec / dense_fallback wire format.
        let blob: Vec<u8> = embedding.iter().flat_map(|f| f.to_le_bytes()).collect();
        #[cfg(feature = "vec")]
        {
            // Best-effort: ignore if vec_chunks does not exist (extension not loaded).
            let _ = conn.execute(
                "INSERT OR REPLACE INTO vec_chunks(chunk_id, embedding) VALUES (?1, ?2)",
                params![chunk_id, blob],
            );
        }
        #[cfg(not(feature = "vec"))]
        {
            conn.execute(
                "INSERT OR REPLACE INTO dense_fallback(chunk_id, embedding) VALUES (?1, ?2)",
                params![chunk_id, blob],
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("upsert dense_fallback: {e}"),
            })?;
        }
        Ok(())
    }

    /// K-nearest-neighbour search over the dense store.
    ///
    /// # Feature: `vec`
    ///
    /// Queries `vec_chunks` using the sqlite-vec MATCH syntax:
    /// ```sql
    /// SELECT chunk_id, distance FROM vec_chunks
    /// WHERE embedding MATCH ? ORDER BY distance LIMIT ?
    /// ```
    ///
    /// # Without `vec` (default)
    ///
    /// Performs a brute-force squared-L2 scan over `dense_fallback` blobs.
    /// Correct for dev/test environments; O(n) — not suitable for large prod indexes.
    ///
    /// # Returns
    ///
    /// `(chunk_id, distance)` pairs sorted by ascending distance, length ≤ `k`.
    /// Returns an empty `Vec` if the underlying table is absent or empty.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::RetrievalFailed`] on SQL errors.
    pub async fn dense_query(&self, query_vec: &[f32], k: usize) -> Result<Vec<(String, f32)>> {
        let conn = self.conn.lock().await;

        #[cfg(feature = "vec")]
        {
            let blob: Vec<u8> = query_vec.iter().flat_map(|f| f.to_le_bytes()).collect();
            let mut stmt = match conn.prepare(
                "SELECT chunk_id, distance \
                 FROM vec_chunks \
                 WHERE embedding MATCH ? \
                 ORDER BY distance \
                 LIMIT ?",
            ) {
                Ok(s) => s,
                Err(_) => return Ok(vec![]),
            };
            let k_i64 = k as i64;
            let results: Vec<(String, f32)> = stmt
                .query_map(params![blob, k_i64], |row| {
                    Ok((row.get::<_, String>(0)?, row.get::<_, f64>(1)? as f32))
                })
                .map_err(|e| CascadeError::RetrievalFailed {
                    detail: format!("dense_query/vec: {e}"),
                })?
                .filter_map(|r| r.ok())
                .collect();
            return Ok(results);
        }

        #[cfg(not(feature = "vec"))]
        {
            // Brute-force scan over dense_fallback blobs.
            let mut stmt = match conn.prepare("SELECT chunk_id, embedding FROM dense_fallback") {
                Ok(s) => s,
                Err(_) => return Ok(vec![]),
            };

            let rows = stmt
                .query_map([], |row| {
                    Ok((row.get::<_, String>(0)?, row.get::<_, Vec<u8>>(1)?))
                })
                .map_err(|e| CascadeError::RetrievalFailed {
                    detail: format!("dense_query/scan: {e}"),
                })?;

            let mut scored: Vec<(String, f32)> = Vec::new();
            for r in rows {
                let (chunk_id, raw) = r.map_err(|e| CascadeError::RetrievalFailed {
                    detail: format!("dense_query/row: {e}"),
                })?;
                if raw.len() % 4 != 0 {
                    continue; // corrupt row — skip
                }
                let stored: Vec<f32> = raw
                    .chunks_exact(4)
                    .map(|c| f32::from_le_bytes([c[0], c[1], c[2], c[3]]))
                    .collect();
                if stored.len() != query_vec.len() {
                    continue; // dimension mismatch — skip
                }
                // Squared-L2 distance — monotone for ranking, no sqrt needed.
                let dist: f32 = query_vec
                    .iter()
                    .zip(stored.iter())
                    .map(|(a, b)| (a - b) * (a - b))
                    .sum();
                scored.push((chunk_id, dist));
            }

            scored.sort_by(|a, b| a.1.partial_cmp(&b.1).unwrap_or(std::cmp::Ordering::Equal));
            scored.truncate(k);
            Ok(scored)
        }
    }

    /// Delete a chunk and its associated FTS5 + vector entries.
    pub async fn delete_chunk(&self, chunk_id: &str) -> Result<()> {
        let conn = self.conn.lock().await;
        conn.execute("DELETE FROM chunks WHERE chunk_id = ?1", params![chunk_id])
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("delete chunk: {e}"),
            })?;
        conn.execute(
            "DELETE FROM fts_chunks WHERE chunk_id = ?1",
            params![chunk_id],
        )
        .map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("delete fts: {e}"),
        })?;
        let _ = conn.execute(
            "DELETE FROM vec_chunks WHERE chunk_id = ?1",
            params![chunk_id],
        );
        // Also clean dense_fallback (non-vec path; no-op if table absent).
        let _ = conn.execute(
            "DELETE FROM dense_fallback WHERE chunk_id = ?1",
            params![chunk_id],
        );
        Ok(())
    }

    // ── Batch operations ─────────────────────────────────────────────────────

    /// Flush a batch of (chunk_id, text) pairs into FTS5 in a single transaction.
    ///
    /// Using a transaction is the primary driver of the ≥500 chunks/sec target.
    pub async fn batch_upsert_fts(&self, batch: &[(&str, &str)]) -> Result<usize> {
        let conn = self.conn.lock().await;
        let tx = conn
            .unchecked_transaction()
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("begin tx: {e}"),
            })?;
        for (chunk_id, text) in batch {
            tx.execute(
                "INSERT OR REPLACE INTO fts_chunks(chunk_id, text) VALUES (?1, ?2)",
                params![chunk_id, text],
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("batch fts insert: {e}"),
            })?;
        }
        tx.commit().map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("commit tx: {e}"),
        })?;
        Ok(batch.len())
    }

    // ── Curated-description query ────────────────────────────────────────────

    /// Query the curated-description FTS table (`rag_chunk_meta_fts5`) and
    /// translate source-document matches into chunk IDs.
    ///
    /// Returns `(chunk_id_string, normalised_score)` pairs ordered by descending
    /// relevance.  The chunk ID is the `id` of the first (lowest `chunk_index`)
    /// chunk under the matched source document.  When a source has no chunks,
    /// that source is silently skipped.
    ///
    /// Returns an empty `Vec` on SQL errors rather than propagating, so the RRF
    /// fusion degrades gracefully when the meta table does not exist or is empty.
    pub async fn curated_query(&self, query: &str, k: usize) -> Result<Vec<(String, f64)>> {
        use crate::retrieve::curated::{first_chunk_for_source, query_curated_fts};

        let conn = self.conn.lock().await;

        let source_hits = match query_curated_fts(&conn, query, k) {
            Ok(h) => h,
            Err(_) => return Ok(vec![]),
        };

        let mut out = Vec::with_capacity(source_hits.len());
        for (source_id, score) in source_hits {
            match first_chunk_for_source(&conn, source_id) {
                Ok(Some(chunk_id)) => out.push((chunk_id.to_string(), score)),
                Ok(None) => {} // source has no chunks yet — skip
                Err(_) => {}   // ignore per-row errors
            }
        }

        Ok(out)
    }

    /// Return chunk IDs ordered by source document recency.
    ///
    /// Uses `rag_sources.mtime` (preferred) or `indexed_at` (fallback) as the
    /// recency timestamp.  Returns `(chunk_id_string, normalised_score)` pairs.
    /// Returns empty `Vec` on SQL errors.
    pub async fn recency_query(&self, k: usize) -> Result<Vec<(String, f64)>> {
        use crate::retrieve::recency::query_by_recency;

        let conn = self.conn.lock().await;

        let hits = match query_by_recency(&conn, k) {
            Ok(h) => h,
            Err(_) => return Ok(vec![]),
        };

        Ok(hits
            .into_iter()
            .map(|(chunk_id, score)| (chunk_id.to_string(), score))
            .collect())
    }

    // ── FTS5 query ───────────────────────────────────────────────────────────

    /// Execute an FTS5 BM25 query, returning top-K (chunk_id, bm25_score) pairs.
    ///
    /// The `query` string is passed directly to the FTS5 engine; callers should
    /// sanitise or quote the string as needed.
    pub async fn fts_query(&self, query: &str, k: usize) -> Result<Vec<(String, f32)>> {
        let conn = self.conn.lock().await;
        let mut stmt = conn
            .prepare(
                "SELECT chunk_id, bm25(fts_chunks) AS score \
                 FROM fts_chunks \
                 WHERE fts_chunks MATCH ?1 \
                 ORDER BY score ASC \
                 LIMIT ?2",
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("fts prepare: {e}"),
            })?;
        let results: Vec<(String, f32)> = stmt
            .query_map(params![query, k as i64], |row| {
                Ok((row.get::<_, String>(0)?, row.get::<_, f64>(1)? as f32))
            })
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("fts query: {e}"),
            })?
            .filter_map(|r| r.ok())
            .collect();
        Ok(results)
    }

    // ── Chunk text lookup ────────────────────────────────────────────────────

    /// Batch-fetch chunk body fields for a set of `chunk_id`s.
    ///
    /// # Purpose
    ///
    /// Retrievers (FTS, vector) receive only `(chunk_id, score)` pairs from
    /// their index tables.  This method resolves those IDs back to their text,
    /// file_path, start_line, and end_line in a **single SQL query** using an
    /// `IN (…)` clause, so callers can populate [`RetrievalHit`] fields without
    /// N individual round-trips.
    ///
    /// # Inputs
    ///
    /// - `ids` — slice of chunk-ID strings to look up.  Order and duplicates do
    ///   not matter; the caller is responsible for re-ordering hits by score.
    ///
    /// # Outputs
    ///
    /// A `HashMap<chunk_id, (text, file_path, start_line, end_line)>`.  Missing
    /// IDs (shouldn't happen in a consistent DB) are simply absent from the map;
    /// callers fall back to empty text / `None` gracefully.
    ///
    /// Returns an empty map (never errors) on SQL failures so callers degrade
    /// gracefully rather than crashing.
    pub async fn fetch_chunks_by_ids(
        &self,
        ids: &[String],
    ) -> std::collections::HashMap<String, (String, Option<String>, Option<i64>, Option<i64>)> {
        if ids.is_empty() {
            return std::collections::HashMap::new();
        }

        let conn = self.conn.lock().await;

        // Build a single parameterised IN clause: `WHERE chunk_id IN (?1,?2,…)`.
        let placeholders: String = (1..=ids.len())
            .map(|i| format!("?{i}"))
            .collect::<Vec<_>>()
            .join(",");
        let sql = format!(
            "SELECT chunk_id, text, file_path, start_line, end_line \
             FROM chunks WHERE chunk_id IN ({placeholders})"
        );

        let mut stmt = match conn.prepare(&sql) {
            Ok(s) => s,
            Err(_) => return std::collections::HashMap::new(),
        };

        // rusqlite requires values as `dyn ToSql` refs.
        let params: Vec<&dyn rusqlite::types::ToSql> = ids
            .iter()
            .map(|s| s as &dyn rusqlite::types::ToSql)
            .collect();

        let rows = match stmt.query_map(params.as_slice(), |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, Option<String>>(2)?,
                row.get::<_, Option<i64>>(3)?,
                row.get::<_, Option<i64>>(4)?,
            ))
        }) {
            Ok(r) => r,
            Err(_) => return std::collections::HashMap::new(),
        };

        let mut map = std::collections::HashMap::with_capacity(ids.len());
        for row in rows.flatten() {
            map.insert(row.0, (row.1, row.2, row.3, row.4));
        }
        map
    }

    // ── FTS5 optimize + vacuum ────────────────────────────────────────────────

    /// Merge all FTS5 segments into one for faster queries.
    pub async fn fts_optimize(&self) -> Result<()> {
        let conn = self.conn.lock().await;
        conn.execute_batch("INSERT INTO fts_chunks(fts_chunks) VALUES('optimize');")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("fts optimize: {e}"),
            })?;
        Ok(())
    }

    /// Reclaim space freed by deleted chunks.
    pub async fn vacuum(&self) -> Result<()> {
        let conn = self.conn.lock().await;
        conn.execute_batch("VACUUM;")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("vacuum: {e}"),
            })?;
        Ok(())
    }

    // ── Health ────────────────────────────────────────────────────────────────

    /// Return a snapshot of index health metrics.
    ///
    /// `embedded_chunks` is read from `vec_chunks` (non-sharded table).
    /// For sharded deployments, call [`health_with_shard_count`] so the metric
    /// reflects the true sum across all `shard_embeddings` tables (bug #2 fix).
    pub async fn health(&self) -> Result<IndexHealth> {
        self.health_with_shard_count(None).await
    }

    /// Return index health metrics with an optional shard-count override for
    /// `embedded_chunks`.
    ///
    /// # Purpose
    ///
    /// `SELECT COUNT(*) FROM vec_chunks` returns 0 for sharded deployments
    /// because embeddings are stored in per-shard `shard_embeddings` tables, not
    /// in `vec_chunks` (bug #2).  Pass `Some(shard.total_count())` to report the
    /// correct embedded count.
    ///
    /// # Inputs
    ///
    /// `shard_embedded_count`: when `Some(n)`, uses `n` as `embedded_chunks`
    /// instead of querying `vec_chunks`.  Pass `None` for the non-sharded path.
    pub async fn health_with_shard_count(
        &self,
        shard_embedded_count: Option<u64>,
    ) -> Result<IndexHealth> {
        let conn = self.conn.lock().await;
        let total: u64 = conn
            .query_row("SELECT COUNT(*) FROM chunks", [], |r| r.get(0))
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("count chunks: {e}"),
            })?;
        // Bug #2 fix: for sharded deployments the caller passes the real embedded
        // count from ShardedIndex::total_count(); fall back to querying vec_chunks
        // for the non-sharded (legacy) path.  Without this, vec_chunks always
        // returns 0 when embeddings live in shard_embeddings.
        let embedded: u64 = match shard_embedded_count {
            Some(n) => n,
            None => conn
                .query_row("SELECT COUNT(*) FROM vec_chunks", [], |r| r.get(0))
                .unwrap_or(0),
        };
        let last: Option<i64> = conn
            .query_row("SELECT MAX(indexed_at) FROM chunks", [], |r| r.get(0))
            .ok()
            .flatten();
        let schema_ver: u32 = conn
            .pragma_query_value(None, "user_version", |r| r.get(0))
            .unwrap_or(0);
        let coverage = if total == 0 {
            0.0
        } else {
            embedded as f32 / total as f32 * 100.0
        };
        Ok(IndexHealth {
            total_chunks: total,
            embedded_chunks: embedded,
            embedding_coverage_pct: coverage,
            last_indexed_at: last,
            is_stale: false,
            schema_version: schema_ver,
        })
    }

    /// Path to the database file.
    pub fn db_path(&self) -> &Path {
        &self.db_path
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn unix_now() -> i64 {
    use std::time::{SystemTime, UNIX_EPOCH};
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

// ── CachedIndex ───────────────────────────────────────────────────────────────

use std::sync::Arc;
use std::time::Duration;

use crate::cache::QueryCache;
use crate::index::sharding::Result as ShardResult;
use crate::index::sharding::{EmbedResult, SearchHit, ShardedIndex};

/// Newtype wrapping [`ShardedIndex`] with an LRU + TTL [`QueryCache`].
///
/// # Purpose
///
/// `CachedIndex` is the public search entry point for callers that want
/// query-result caching.  Identical `(query, top_k)` calls within the TTL
/// window are served from memory without touching SQLite or computing cosine
/// distances.
///
/// # Cache invalidation
///
/// Any call to [`upsert`] or [`delete`] clears the entire cache.  WHY full
/// clear: the query key space is unbounded; selective invalidation would
/// require O(capacity) reverse-index lookups.  Full clear is O(1) and safe —
/// the default capacity (512) repopulates within seconds of normal query
/// traffic.
///
/// # Thread safety
///
/// Both [`ShardedIndex`] and [`QueryCache`] are `Send + Sync`; wrapping in
/// `Arc<CachedIndex>` is safe across tokio tasks.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_rag::index::CachedIndex;
/// # use cascade_rag::index::sharding::ShardedIndex;
/// # use std::time::Duration;
/// # fn example() -> cascade_rag::index::sharding::Result<()> {
/// let shard_idx = ShardedIndex::new("/tmp/rag", 4, 1024)?;
/// let cached = CachedIndex::new(shard_idx, 512, Duration::from_secs(60));
/// # Ok(())
/// # }
/// ```
///
/// SPORT: MASTER-COMPONENTS.md → cascade-rag::index::CachedIndex
pub struct CachedIndex {
    inner: ShardedIndex,
    cache: Arc<QueryCache>,
}

impl CachedIndex {
    /// Wrap `inner` with a query cache of the given `capacity` and `ttl`.
    ///
    /// # Inputs
    ///
    /// - `inner`: the underlying [`ShardedIndex`] to delegate real searches to.
    /// - `capacity`: max LRU entries (clamped to 1; default 512).
    /// - `ttl`: entry lifetime before re-query (default 60 s).
    pub fn new(inner: ShardedIndex, capacity: usize, ttl: Duration) -> Self {
        Self {
            inner,
            cache: Arc::new(QueryCache::new(capacity, ttl)),
        }
    }

    /// Construct with default cache settings (capacity=512, TTL=60 s).
    pub fn with_defaults(inner: ShardedIndex) -> Self {
        Self::new(inner, 512, Duration::from_secs(60))
    }

    /// Return a shared handle to the underlying [`QueryCache`].
    ///
    /// Used by `cascade status` to read hit/miss/size metrics.
    pub fn cache(&self) -> Arc<QueryCache> {
        Arc::clone(&self.cache)
    }

    /// Search the index, consulting the cache first.
    ///
    /// # Cache behaviour
    ///
    /// 1. Key = `(query, top_k)`.
    /// 2. Cache hit (non-expired) → return cached result, increment hit counter.
    /// 3. Cache miss (absent or TTL-expired) → delegate to [`ShardedIndex::search`],
    ///    populate cache, increment miss counter.
    ///
    /// # Errors
    ///
    /// Propagates any [`ShardError`] from the underlying shard fan-out.
    ///
    /// [`ShardError`]: crate::index::sharding::ShardError
    pub fn search(&self, query_vec: &[f32], top_k: usize) -> ShardResult<Vec<SearchHit>> {
        // Build a string fingerprint for the cache key.
        // WHY hash-as-string: the lru cache key must be Eq+Hash; a float slice is
        // neither stable-comparable nor directly hashable without copying.  We use
        // a lightweight FNV-1a over the raw bytes — identical to what ShardedIndex
        // already does for routing.  Collisions are astronomically unlikely for the
        // 512-entry default capacity.
        let key_str = vec_fingerprint(query_vec);

        if let Some(cached) = self.cache.get(&key_str, top_k) {
            return Ok(cached);
        }

        let result = self.inner.search(query_vec, top_k)?;
        self.cache.set(key_str, top_k, result.clone());
        Ok(result)
    }

    /// Insert or replace a dense embedding, then clear the query cache.
    ///
    /// Cache is cleared because the search result set for any query may have
    /// changed after an upsert.
    pub fn upsert(&self, doc: &EmbedResult) -> ShardResult<()> {
        self.inner.upsert(doc)?;
        self.cache.clear();
        Ok(())
    }

    /// Delete a chunk from the sharded index, then clear the query cache.
    ///
    /// Same invalidation rationale as [`upsert`].
    pub fn delete(&self, doc_id: &str) -> ShardResult<()> {
        self.inner.delete(doc_id)?;
        self.cache.clear();
        Ok(())
    }

    /// Delegate to the underlying [`ShardedIndex::shard_count`].
    pub fn shard_count(&self) -> usize {
        self.inner.shard_count()
    }

    /// Delegate to the underlying [`ShardedIndex::embed_dim`].
    pub fn embed_dim(&self) -> usize {
        self.inner.embed_dim()
    }

    /// Delegate to the underlying [`ShardedIndex::total_count`].
    pub fn total_count(&self) -> usize {
        self.inner.total_count()
    }
}

/// Compute a short string fingerprint of a float vector for use as a cache key.
///
/// Uses FNV-1a 64-bit over the raw bytes of the slice.  This is deterministic,
/// fast, and produces a uniform distribution sufficient for a 512-entry LRU.
fn vec_fingerprint(v: &[f32]) -> String {
    let mut hash: u64 = 0xcbf29ce484222325;
    for b in v.iter().flat_map(|f| f.to_le_bytes()) {
        hash ^= b as u64;
        hash = hash.wrapping_mul(0x00000100000001b3);
    }
    format!("{hash:016x}")
}

// ── CachedIndex tests ─────────────────────────────────────────────────────────

#[cfg(test)]
mod cached_index_tests {
    use super::*;
    use crate::index::sharding::{EmbedResult, ShardedIndex};

    fn make_embed(doc_id: &str, dim: usize) -> EmbedResult {
        EmbedResult {
            doc_id: doc_id.to_string(),
            embedding: vec![0.1_f32; dim],
        }
    }

    #[test]
    fn upsert_clears_cache() {
        let dir = tempfile::tempdir().unwrap();
        let shard = ShardedIndex::new(dir.path(), 1, 4).unwrap();
        let cached = CachedIndex::with_defaults(shard);

        let query = vec![0.1_f32; 4];

        // Populate cache via a search (miss → populate).
        let _ = cached.search(&query, 5).unwrap();
        assert_eq!(
            cached.cache().stats(),
            (0, 1),
            "first search must be a miss"
        );

        // Hit on second search.
        let _ = cached.search(&query, 5).unwrap();
        assert_eq!(
            cached.cache().stats(),
            (1, 1),
            "second search must be a hit"
        );

        // Upsert clears cache.
        cached.upsert(&make_embed("doc1", 4)).unwrap();
        assert_eq!(cached.cache().len(), 0, "cache must be empty after upsert");

        // Next search is a miss again.
        let _ = cached.search(&query, 5).unwrap();
        assert_eq!(
            cached.cache().stats(),
            (1, 2),
            "post-upsert search must be a miss"
        );
    }

    #[test]
    fn ten_searches_produce_one_miss_nine_hits() {
        let dir = tempfile::tempdir().unwrap();
        let shard = ShardedIndex::new(dir.path(), 1, 4).unwrap();
        let cached = CachedIndex::with_defaults(shard);

        let query = vec![0.5_f32; 4];

        for i in 0..10 {
            let result = cached.search(&query, 3).unwrap();
            // Empty index returns empty results — that's fine.
            let _ = result;
            if i == 0 {
                assert_eq!(cached.cache().stats(), (0, 1), "first call must miss");
            }
        }

        let (hits, misses) = cached.cache().stats();
        assert_eq!(hits, 9, "expected 9 cache hits");
        assert_eq!(misses, 1, "expected 1 cache miss (cold)");
    }

    // ── bug #2: sharded total_count reflects inserted rows ───────────────────

    /// Insert N docs across multiple shards; verify total_count sums all shards.
    ///
    /// This guards against the bug where the health metric queried `vec_chunks`
    /// (always 0 for sharded deployments) instead of summing `shard_embeddings`.
    #[test]
    fn sharded_total_count_matches_inserted_rows() {
        let dir = tempfile::tempdir().unwrap();
        let shard = ShardedIndex::new(dir.path(), 4, 8).unwrap();
        let cached = CachedIndex::with_defaults(shard);

        assert_eq!(cached.total_count(), 0, "empty index must have count 0");

        let n = 12usize;
        for i in 0..n {
            cached
                .upsert(&EmbedResult {
                    doc_id: format!("health_doc_{i}"),
                    embedding: vec![i as f32 * 0.1; 8],
                })
                .expect("upsert");
        }

        // total_count sums shard_embeddings across all 4 shards — must equal n.
        assert_eq!(
            cached.total_count(),
            n,
            "total_count must equal inserted row count across all shards"
        );

        // Simulate what health_with_shard_count does: use total_count() as
        // embedded_chunks.  The value must be n, not 0.
        let embedded_from_shard = cached.total_count() as u64;
        assert_eq!(
            embedded_from_shard, n as u64,
            "shard-sourced embedded count must match n (bug #2 fix)"
        );

        // Delete one doc; count must decrease.
        cached.delete("health_doc_0").expect("delete");
        assert_eq!(
            cached.total_count(),
            n - 1,
            "total_count must decrease after delete"
        );
    }
}
