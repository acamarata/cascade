//! # index::sharding
//!
//! Sharded sqlite-vec index layout for cascade-rag.
//!
//! ## Purpose
//!
//! Replaces the single `cascade_vec.db` with `N` database files named
//! `cascade_vec_shard_{i}.db` (where `N = RagConfig.shard_count`, default 4).
//! Each document is assigned to shard `shard_id = fnv1a(doc_id) % N`.
//!
//! ## Shard routing
//!
//! [`shard_for`] uses FNV-1a 64-bit hash — fast, non-cryptographic,
//! deterministic across restarts, and produces a uniform distribution for
//! typical chunk-id strings.
//!
//! ## KNN fan-out
//!
//! [`ShardedIndex::search`] spawns one thread per shard (via `std::thread`),
//! runs top-k KNN on each, collects all results, merges by ascending distance,
//! and truncates to the requested `top_k`.  WAL mode is set on every shard
//! connection to allow concurrent reads alongside the single writer.
//!
//! ## Migration path
//!
//! On [`ShardedIndex::new`], if `cascade_vec.db` is present in `index_root`,
//! it is copied to `cascade_vec_shard_0.db` and the original is left in place
//! (non-destructive per ADR-014).  A `WARN` is logged asking the operator to
//! re-index.
//!
//! ## Blob fallback parity
//!
//! Both `vec` and non-`vec` feature modes are supported.  Without the `vec`
//! feature, KNN is a full-table scan with squared-L2 in Rust (same as the
//! non-sharded path).  With `vec`, the vec0 MATCH syntax is used.
//!
//! ## Ticket
//!
//! T-P4-E04-04
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-rag ShardedIndex (replaces VecIndex)
//!        MASTER-TABLES.md     → cascade_vec_shard_{i} schema
//!        MASTER-ENV.md        → CASCADE_RAG_SHARD_COUNT default=4

use std::path::Path;
use std::sync::{Arc, Mutex};

use rusqlite::{params, Connection};
use tracing::{debug, instrument, warn};

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by [`ShardedIndex`].
#[derive(Debug, thiserror::Error)]
pub enum ShardError {
    #[error("SQLite error in shard {shard}: {source}")]
    Sqlite {
        shard: usize,
        #[source]
        source: rusqlite::Error,
    },
    #[error("I/O error: {0}")]
    Io(#[from] std::io::Error),
    #[error("Shard fan-out thread panicked")]
    ThreadPanic,
    #[error("Dimension mismatch: expected {expected}, got {actual}")]
    DimMismatch { expected: usize, actual: usize },
}

pub type Result<T> = std::result::Result<T, ShardError>;

// ── SearchHit ─────────────────────────────────────────────────────────────────

/// A single result from a KNN query.
///
/// `doc_id` is the chunk identifier passed to [`ShardedIndex::upsert`].
/// `distance` is squared-L2 (without `vec` feature) or L2 distance (with `vec`).
/// Lower is closer.
#[derive(Debug, Clone, PartialEq)]
pub struct SearchHit {
    /// Chunk/document identifier (same string passed to upsert).
    pub doc_id: String,
    /// Distance from the query vector. Ascending = closer.
    pub distance: f32,
    /// Which shard this hit came from.
    pub shard_id: usize,
}

// ── EmbedResult ──────────────────────────────────────────────────────────────

/// A dense embedding to be stored in the sharded index.
#[derive(Debug, Clone)]
pub struct EmbedResult {
    /// Chunk/document identifier — used as the routing key for shard assignment.
    pub doc_id: String,
    /// Dense float vector.  Length must equal `embed_dim` on the owning [`ShardedIndex`].
    pub embedding: Vec<f32>,
}

// ── FNV-1a hash ──────────────────────────────────────────────────────────────

/// FNV-1a 64-bit hash (public domain).
///
/// Deterministic, fast, uniform for typical string keys.  No dependencies.
///
/// # Inputs
///
/// `bytes`: the key to hash (typically `doc_id.as_bytes()`).
///
/// # Outputs
///
/// 64-bit FNV-1a hash value.
#[inline]
pub fn fnv1a(bytes: &[u8]) -> u64 {
    const OFFSET: u64 = 14695981039346656037;
    const PRIME: u64 = 1099511628211;
    let mut hash = OFFSET;
    for &b in bytes {
        hash ^= b as u64;
        hash = hash.wrapping_mul(PRIME);
    }
    hash
}

/// Assign `doc_id` to a shard index in `[0, n)`.
///
/// # Constraints
///
/// - `n` must be >= 1.
/// - Deterministic across restarts (FNV-1a is not randomised).
///
/// # Inputs
///
/// `doc_id`: chunk identifier string.
/// `n`: total shard count.
///
/// # Outputs
///
/// Shard index in `[0, n)`.
#[inline]
pub fn shard_for(doc_id: &str, n: usize) -> usize {
    debug_assert!(n >= 1, "shard_count must be >= 1");
    (fnv1a(doc_id.as_bytes()) as usize) % n
}

/// Maximum number of rows read from shard_0 per transaction during legacy
/// rebalance.  Bounds peak memory usage for very large legacy indexes.
const REBALANCE_BATCH_SIZE: usize = 1000;

// ── Schema helpers ────────────────────────────────────────────────────────────

/// Apply WAL mode, synchronous=NORMAL, and create the embedding table on a shard
/// connection.
///
/// The table schema matches the non-sharded `rag_embeddings` fallback:
/// - `rowid` is a synthetic integer PK (shard-local; not globally unique).
/// - `doc_id` is the stable cross-shard identifier.
/// - `embedding` is a little-endian f32 BLOB (or a vec0 virtual column with `vec`).
///
/// Both feature paths share an identical schema string so migration logic is
/// the same; only query execution differs.
fn apply_shard_schema(conn: &Connection, embed_dim: usize) -> rusqlite::Result<()> {
    conn.execute_batch("PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;")?;

    #[cfg(feature = "vec")]
    {
        // sqlite-vec vec0 virtual table for native KNN — dimension parameterised.
        let ddl = format!(
            "CREATE TABLE IF NOT EXISTS shard_meta (\
                shard_schema_version INTEGER NOT NULL DEFAULT 1\
            );\
            INSERT OR IGNORE INTO shard_meta(shard_schema_version) VALUES(1);\
            CREATE VIRTUAL TABLE IF NOT EXISTS shard_embeddings USING vec0(\
                doc_id TEXT PRIMARY KEY,\
                embedding FLOAT[{embed_dim}]\
            );"
        );
        conn.execute_batch(&ddl)?;
    }

    #[cfg(not(feature = "vec"))]
    {
        // Plain BLOB table — full-scan fallback KNN in Rust.
        // embed_dim not needed in DDL for BLOB path; suppress unused warning.
        let _ = embed_dim;
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS shard_meta (\
                shard_schema_version INTEGER NOT NULL DEFAULT 1\
            );\
            INSERT OR IGNORE INTO shard_meta(shard_schema_version) VALUES(1);\
            CREATE TABLE IF NOT EXISTS shard_embeddings (\
                doc_id TEXT PRIMARY KEY,\
                embedding BLOB NOT NULL\
            );",
        )?;
    }

    Ok(())
}

// ── ShardedIndex ──────────────────────────────────────────────────────────────

/// Sharded dense-vector index: `N` SQLite connections, one per shard file.
///
/// # Example
///
/// ```rust,no_run
/// # use std::path::Path;
/// # use cascade_rag::index::sharding::{ShardedIndex, EmbedResult};
/// # fn example() -> cascade_rag::index::sharding::Result<()> {
/// let idx = ShardedIndex::new(Path::new("/home/user/.cascade/index"), 4, 1024)?;
/// let hit = EmbedResult {
///     doc_id: "chunk_42".to_string(),
///     embedding: vec![0.0f32; 1024],
/// };
/// idx.upsert(&hit)?;
/// let results = idx.search(&vec![0.0f32; 1024], 5)?;
/// # Ok(())
/// # }
/// ```
///
/// SPORT: MASTER-COMPONENTS.md → cascade-rag ShardedIndex
pub struct ShardedIndex {
    /// One mutex-protected connection per shard.
    shards: Vec<Arc<Mutex<Connection>>>,
    /// Number of shards (= `shards.len()`).
    shard_count: usize,
    /// Dense vector dimension expected by this index.
    embed_dim: usize,
}

impl ShardedIndex {
    /// Open or create a sharded index in `index_root`.
    ///
    /// Creates `shard_count` database files named `cascade_vec_shard_{i}.db`.
    /// If the legacy single-file `cascade_vec.db` exists, copies it to shard 0
    /// and logs a warning.
    ///
    /// # Inputs
    ///
    /// `index_root`: directory where shard files are stored (created if absent).
    /// `shard_count`: number of shards; must be >= 1. Default from `RagConfig` is 4.
    /// `embed_dim`: vector dimension; must match the embedding model.
    ///
    /// # Errors
    ///
    /// Returns [`ShardError::Io`] if the directory cannot be created or the
    /// legacy file cannot be copied.  Returns [`ShardError::Sqlite`] if any
    /// shard connection fails to open or the schema cannot be applied.
    #[instrument(skip_all, fields(root = %index_root.as_ref().display(), shards = shard_count))]
    pub fn new(index_root: impl AsRef<Path>, shard_count: usize, embed_dim: usize) -> Result<Self> {
        let root = index_root.as_ref();
        std::fs::create_dir_all(root)?;

        // Migration path: copy legacy single-file index to shard 0.
        let legacy = root.join("cascade_vec.db");
        let shard_0_path = root.join("cascade_vec_shard_0.db");
        let did_legacy_copy = if legacy.exists() && !shard_0_path.exists() {
            std::fs::copy(&legacy, &shard_0_path)?;
            warn!(
                legacy = %legacy.display(),
                "Legacy single-shard index found; redistributing rows across {} shards",
                shard_count
            );
            true
        } else {
            false
        };

        let mut shards = Vec::with_capacity(shard_count);
        for i in 0..shard_count {
            let path = root.join(format!("cascade_vec_shard_{i}.db"));
            let conn = cascade_db::open_configured(&path).map_err(|e| {
                ShardError::Sqlite {
                    shard: i,
                    source: rusqlite::Error::SqliteFailure(
                        rusqlite::ffi::Error::new(rusqlite::ffi::SQLITE_CANTOPEN),
                        Some(e.to_string()),
                    ),
                }
            })?;
            apply_shard_schema(&conn, embed_dim).map_err(|e| ShardError::Sqlite {
                shard: i,
                source: e,
            })?;
            shards.push(Arc::new(Mutex::new(conn)));
        }

        let idx = Self {
            shards,
            shard_count,
            embed_dim,
        };

        // Bug #6 fix: after a legacy copy, shard_0 holds ALL rows regardless
        // of their correct shard assignment.  Redistribute rows that belong in
        // shards 1..N-1 so KNN fan-out returns balanced results.
        //
        // This only runs once (guarded by `did_legacy_copy`).  For each row in
        // shard_0 whose shard_for(doc_id, N) != 0, we INSERT it into the target
        // shard and DELETE it from shard_0.  Rows are processed in bounded
        // batches (REBALANCE_BATCH_SIZE rows per transaction) so a very large
        // legacy index does not OOM by loading all rows at once.
        if did_legacy_copy && shard_count > 1 {
            idx.rebalance_shard_0().map_err(|e| {
                warn!(error = %e, "Legacy shard rebalance failed; re-index recommended");
                e
            })?;
        }

        Ok(idx)
    }

    /// Redistribute rows from shard_0 that belong to shards 1..N-1.
    ///
    /// # Purpose
    ///
    /// After a legacy `cascade_vec.db` is copied to `shard_0`, all rows land in
    /// shard_0 regardless of the FNV-1a routing.  This method moves misrouted rows
    /// into their correct shards (bug #6 fix).
    ///
    /// # Algorithm
    ///
    /// Iterates shard_0 in batches of [`REBALANCE_BATCH_SIZE`] rows:
    /// 1. Read a page of (doc_id, embedding) rows from shard_0.
    /// 2. Group misrouted rows by their correct target shard.
    /// 3. For each target shard: INSERT OR REPLACE in a single transaction.
    /// 4. DELETE the moved doc_ids from shard_0 in a single transaction.
    /// 5. Repeat until the page is empty (no more misrouted rows remain).
    ///
    /// Only runs when the legacy copy actually happened (shard_0 had all the data).
    fn rebalance_shard_0(&self) -> Result<()> {
        if self.shard_count <= 1 {
            return Ok(());
        }

        let mut offset: i64 = 0;
        loop {
            // Read one batch of rows from shard_0.
            let batch: Vec<(String, Vec<u8>)> = {
                let conn_0 = self.shards[0].lock().expect("shard 0 mutex poisoned");
                let mut stmt = conn_0
                    .prepare(
                        "SELECT doc_id, embedding FROM shard_embeddings                          ORDER BY doc_id LIMIT ?1 OFFSET ?2",
                    )
                    .map_err(|e| ShardError::Sqlite { shard: 0, source: e })?;
                let result: std::result::Result<Vec<_>, _> = stmt
                    .query_map(
                        rusqlite::params![REBALANCE_BATCH_SIZE as i64, offset],
                        |row| Ok((row.get::<_, String>(0)?, row.get::<_, Vec<u8>>(1)?)),
                    )
                    .map_err(|e| ShardError::Sqlite { shard: 0, source: e })?
                    .collect();
                result.map_err(|e| ShardError::Sqlite { shard: 0, source: e })?
            };

            if batch.is_empty() {
                break;
            }

            // Group misrouted rows by their correct shard (only non-zero shards).
            let mut by_shard: Vec<Vec<(String, Vec<u8>)>> = vec![vec![]; self.shard_count];
            let mut stayed_in_0 = 0usize;
            for (doc_id, embedding) in batch {
                let target = shard_for(&doc_id, self.shard_count);
                if target != 0 {
                    by_shard[target].push((doc_id, embedding));
                } else {
                    stayed_in_0 += 1;
                }
            }

            // Move misrouted rows: INSERT into target, DELETE from shard_0.
            let mut moved_this_batch = 0usize;
            for (target_shard, rows_for_shard) in by_shard.iter().enumerate().skip(1) {
                if rows_for_shard.is_empty() {
                    continue;
                }

                // Insert into target shard.
                {
                    let conn_t =
                        self.shards[target_shard].lock().expect("target shard mutex poisoned");
                    conn_t
                        .execute_batch("BEGIN")
                        .map_err(|e| ShardError::Sqlite { shard: target_shard, source: e })?;
                    for (doc_id, embedding) in rows_for_shard {
                        conn_t
                            .execute(
                                "INSERT OR REPLACE INTO shard_embeddings(doc_id, embedding)                                  VALUES(?1, ?2)",
                                rusqlite::params![doc_id, embedding],
                            )
                            .map_err(|e| ShardError::Sqlite {
                                shard: target_shard,
                                source: e,
                            })?;
                    }
                    conn_t
                        .execute_batch("COMMIT")
                        .map_err(|e| ShardError::Sqlite { shard: target_shard, source: e })?;
                }

                // Delete moved rows from shard_0.
                {
                    let conn_0 = self.shards[0].lock().expect("shard 0 mutex poisoned");
                    conn_0
                        .execute_batch("BEGIN")
                        .map_err(|e| ShardError::Sqlite { shard: 0, source: e })?;
                    for (doc_id, _) in rows_for_shard {
                        conn_0
                            .execute(
                                "DELETE FROM shard_embeddings WHERE doc_id = ?1",
                                rusqlite::params![doc_id],
                            )
                            .map_err(|e| ShardError::Sqlite { shard: 0, source: e })?;
                    }
                    conn_0
                        .execute_batch("COMMIT")
                        .map_err(|e| ShardError::Sqlite { shard: 0, source: e })?;
                }

                moved_this_batch += rows_for_shard.len();
                debug!(
                    target_shard,
                    moved = rows_for_shard.len(),
                    "legacy rebalance: moved rows from shard_0"
                );
            }

            // Advance offset by rows that stayed in shard_0 (moved rows were
            // deleted, so they no longer shift subsequent pages).
            offset += stayed_in_0 as i64;

            // If nothing moved this batch, all remaining rows belong in shard_0.
            if moved_this_batch == 0 {
                break;
            }
        }

        Ok(())
    }

    /// Write (insert or replace) a dense embedding for `doc`.
    ///
    /// Routes to the shard determined by `shard_for(doc.doc_id, shard_count)`.
    ///
    /// # Errors
    ///
    /// [`ShardError::DimMismatch`] if `doc.embedding.len() != embed_dim`.
    /// [`ShardError::Sqlite`] on any database write failure.
    #[instrument(skip(self, doc), fields(doc_id = %doc.doc_id))]
    pub fn upsert(&self, doc: &EmbedResult) -> Result<()> {
        if doc.embedding.len() != self.embed_dim {
            return Err(ShardError::DimMismatch {
                expected: self.embed_dim,
                actual: doc.embedding.len(),
            });
        }
        let shard_id = shard_for(&doc.doc_id, self.shard_count);
        let blob: Vec<u8> = doc.embedding.iter().flat_map(|f| f.to_le_bytes()).collect();

        let conn = self.shards[shard_id].lock().expect("shard mutex poisoned");

        #[cfg(feature = "vec")]
        {
            conn.execute(
                "INSERT OR REPLACE INTO shard_embeddings(doc_id, embedding) VALUES(?1, ?2)",
                params![doc.doc_id, blob],
            )
            .map_err(|e| ShardError::Sqlite {
                shard: shard_id,
                source: e,
            })?;
        }

        #[cfg(not(feature = "vec"))]
        {
            conn.execute(
                "INSERT OR REPLACE INTO shard_embeddings(doc_id, embedding) VALUES(?1, ?2)",
                params![doc.doc_id, blob],
            )
            .map_err(|e| ShardError::Sqlite {
                shard: shard_id,
                source: e,
            })?;
        }

        debug!(doc_id = %doc.doc_id, shard = shard_id, "embedding stored");
        Ok(())
    }

    /// Remove a document from its shard.
    ///
    /// # Inputs
    ///
    /// `doc_id`: the chunk identifier to remove.
    ///
    /// # Outputs
    ///
    /// `Ok(())` even if the document did not exist (idempotent delete).
    #[instrument(skip(self), fields(doc_id))]
    pub fn delete(&self, doc_id: &str) -> Result<()> {
        let shard_id = shard_for(doc_id, self.shard_count);
        let conn = self.shards[shard_id].lock().expect("shard mutex poisoned");
        conn.execute(
            "DELETE FROM shard_embeddings WHERE doc_id = ?1",
            params![doc_id],
        )
        .map_err(|e| ShardError::Sqlite {
            shard: shard_id,
            source: e,
        })?;
        debug!(doc_id, shard = shard_id, "embedding deleted");
        Ok(())
    }

    /// Fan-out KNN across all shards, merge by distance, return top-k.
    ///
    /// Queries all `N` shards in parallel using `std::thread::scope`.  Each
    /// shard returns up to `top_k` candidates.  Results are merged into a
    /// single list sorted by ascending distance and truncated to `top_k`.
    ///
    /// # Inputs
    ///
    /// `query_vec`: dense query vector; length must equal `embed_dim`.
    /// `top_k`: number of results to return.
    ///
    /// # Outputs
    ///
    /// `Ok(Vec<SearchHit>)` sorted by ascending distance, length <= `top_k`.
    ///
    /// # Errors
    ///
    /// [`ShardError::DimMismatch`] if `query_vec.len() != embed_dim`.
    /// [`ShardError::ThreadPanic`] if a shard thread panics.
    /// [`ShardError::Sqlite`] on any database read failure.
    #[instrument(skip(self, query_vec), fields(top_k))]
    pub fn search(&self, query_vec: &[f32], top_k: usize) -> Result<Vec<SearchHit>> {
        if query_vec.len() != self.embed_dim {
            return Err(ShardError::DimMismatch {
                expected: self.embed_dim,
                actual: query_vec.len(),
            });
        }

        let dim = self.embed_dim;
        let blob: Vec<u8> = query_vec.iter().flat_map(|f| f.to_le_bytes()).collect();

        // Parallel fan-out: one thread per shard.
        let mut shard_results: Vec<Vec<SearchHit>> = vec![vec![]; self.shard_count];

        std::thread::scope(|s| -> Result<()> {
            let handles: Vec<_> = self
                .shards
                .iter()
                .enumerate()
                .map(|(shard_id, arc_conn)| {
                    let blob_ref = blob.clone();
                    let conn_ref = Arc::clone(arc_conn);
                    s.spawn(move || -> Result<Vec<SearchHit>> {
                        let conn = conn_ref.lock().expect("shard mutex poisoned");
                        shard_knn_query(&conn, shard_id, &blob_ref, dim, top_k)
                    })
                })
                .collect();

            for (i, handle) in handles.into_iter().enumerate() {
                let hits = handle.join().map_err(|_| ShardError::ThreadPanic)??;
                shard_results[i] = hits;
            }
            Ok(())
        })?;

        // Merge: collect all hits, sort ascending by distance, take top_k.
        let mut all: Vec<SearchHit> = shard_results.into_iter().flatten().collect();
        all.sort_by(|a, b| {
            a.distance
                .partial_cmp(&b.distance)
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        all.truncate(top_k);
        Ok(all)
    }

    /// Return the shard count this index was created with.
    pub fn shard_count(&self) -> usize {
        self.shard_count
    }

    /// Return the embedding dimension this index expects.
    pub fn embed_dim(&self) -> usize {
        self.embed_dim
    }

    /// Count total indexed items across all shards.
    ///
    /// Sums `COUNT(*)` from the `vec_items` table in each shard DB.
    /// Returns 0 if any shard cannot be queried (logged at warn level).
    ///
    /// # Purpose
    /// Used by `cascade status --json` to populate `rag.indexed_docs`.
    ///
    /// SPORT: MASTER-COMPONENTS.md → cascade-rag::ShardedIndex::total_count
    pub fn total_count(&self) -> usize {
        let mut total: usize = 0;
        for arc_conn in &self.shards {
            let conn = arc_conn.lock().expect("shard mutex poisoned");
            let n: i64 = conn
                .query_row("SELECT COUNT(*) FROM shard_embeddings", [], |r| r.get(0))
                .unwrap_or(0);
            total += n as usize;
        }
        total
    }
}

// ── Per-shard KNN query ───────────────────────────────────────────────────────

/// Run KNN on a single shard connection.
///
/// # Inputs
///
/// `conn`: open shard connection.
/// `shard_id`: index for tagging results.
/// `query_blob`: little-endian f32 BLOB of the query vector.
/// `dim`: expected vector dimension (elements, not bytes).
/// `top_k`: maximum results to return.
///
/// # Outputs
///
/// `Vec<SearchHit>` sorted ascending by distance, length <= `top_k`.
fn shard_knn_query(
    conn: &Connection,
    shard_id: usize,
    query_blob: &[u8],
    dim: usize,
    top_k: usize,
) -> Result<Vec<SearchHit>> {
    #[cfg(feature = "vec")]
    {
        // sqlite-vec MATCH syntax — native ANN.
        let mut stmt = conn
            .prepare_cached(
                "SELECT doc_id, distance \
                 FROM shard_embeddings \
                 WHERE embedding MATCH ? \
                 ORDER BY distance \
                 LIMIT ?",
            )
            .map_err(|e| ShardError::Sqlite {
                shard: shard_id,
                source: e,
            })?;

        let k_i64 = top_k as i64;
        let rows = stmt
            .query_map(params![query_blob, k_i64], |row| {
                Ok((row.get::<_, String>(0)?, row.get::<_, f64>(1)? as f32))
            })
            .map_err(|e| ShardError::Sqlite {
                shard: shard_id,
                source: e,
            })?;

        let mut hits = Vec::new();
        for r in rows {
            let (doc_id, distance) = r.map_err(|e| ShardError::Sqlite {
                shard: shard_id,
                source: e,
            })?;
            hits.push(SearchHit {
                doc_id,
                distance,
                shard_id,
            });
        }
        Ok(hits)
    }

    #[cfg(not(feature = "vec"))]
    {
        // Full-scan fallback — squared L2 in Rust.
        // Distance-metric note (task 4): the `vec` path above returns sqlite-vec
        // L2 distance; this path uses squared-L2.  Both are monotone for ranking
        // so ordering is identical, only the scale differs.  These paths are
        // never mixed in a single ranking list (shard fan-out uses one path or
        // the other uniformly at compile time), so there is no ranking skew.
        let query_vec: Vec<f32> = query_blob
            .chunks_exact(4)
            .map(|b| f32::from_le_bytes([b[0], b[1], b[2], b[3]]))
            .collect();

        let mut stmt = conn
            .prepare_cached("SELECT doc_id, embedding FROM shard_embeddings")
            .map_err(|e| ShardError::Sqlite {
                shard: shard_id,
                source: e,
            })?;

        let rows = stmt
            .query_map([], |row| {
                let doc_id: String = row.get(0)?;
                let raw: Vec<u8> = row.get(1)?;
                Ok((doc_id, raw))
            })
            .map_err(|e| ShardError::Sqlite {
                shard: shard_id,
                source: e,
            })?;

        let mut scored: Vec<SearchHit> = Vec::new();
        for r in rows {
            let (doc_id, raw) = r.map_err(|e| ShardError::Sqlite {
                shard: shard_id,
                source: e,
            })?;
            if raw.len() != dim * 4 {
                continue; // skip corrupt rows
            }
            let stored: Vec<f32> = raw
                .chunks_exact(4)
                .map(|b| f32::from_le_bytes([b[0], b[1], b[2], b[3]]))
                .collect();
            // Squared L2 — monotone for ranking, no sqrt required.
            let dist: f32 = query_vec
                .iter()
                .zip(stored.iter())
                .map(|(a, b)| (a - b) * (a - b))
                .sum();
            scored.push(SearchHit {
                doc_id,
                distance: dist,
                shard_id,
            });
        }

        // Sort ascending, take top_k.
        scored.sort_by(|a, b| {
            a.distance
                .partial_cmp(&b.distance)
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        scored.truncate(top_k);
        Ok(scored)
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::TempDir;

    // ── helpers ───────────────────────────────────────────────────────────────

    /// Register sqlite-vec as an auto-extension (no-op without the `vec` feature).
    #[cfg(feature = "vec")]
    fn load_vec_extension() {
        use rusqlite::ffi::sqlite3_auto_extension;
        unsafe {
            sqlite3_auto_extension(Some(std::mem::transmute(
                sqlite_vec::sqlite3_vec_init as *const (),
            )));
        }
    }

    /// Build a small deterministic unit vector seeded by `seed`.
    fn make_vec(seed: f32, dim: usize) -> Vec<f32> {
        let raw: Vec<f32> = (0..dim).map(|i| (seed + i as f32 * 0.01).sin()).collect();
        let norm = raw.iter().map(|v| v * v).sum::<f32>().sqrt().max(1e-9);
        raw.iter().map(|v| v / norm).collect()
    }

    /// Create a ShardedIndex in a temp directory.
    fn make_index(shard_count: usize, dim: usize) -> (TempDir, ShardedIndex) {
        #[cfg(feature = "vec")]
        load_vec_extension();

        let dir = TempDir::new().expect("tmpdir");
        let idx = ShardedIndex::new(dir.path(), shard_count, dim).expect("ShardedIndex::new");
        (dir, idx)
    }

    // ── shard routing determinism ─────────────────────────────────────────────

    /// shard_for must return the same shard for the same doc_id on every call.
    #[test]
    fn shard_routing_is_deterministic() {
        let doc_ids = ["chunk_0", "chunk_1", "hello_world", "abc", "x"];
        for &id in &doc_ids {
            let s1 = shard_for(id, 4);
            let s2 = shard_for(id, 4);
            assert_eq!(s1, s2, "shard_for({id}, 4) must be stable: {s1} != {s2}");
            assert!(s1 < 4, "shard_for must be in [0, 4)");
        }
    }

    /// shard_for with N=1 always returns 0.
    #[test]
    fn shard_routing_single_shard() {
        for id in &["a", "long_doc_id_12345", "cascade"] {
            assert_eq!(shard_for(id, 1), 0, "shard_for with n=1 must always be 0");
        }
    }

    /// shard_for distributes 1000 sequential IDs across 4 shards roughly evenly.
    #[test]
    fn shard_routing_roughly_uniform() {
        let n = 4;
        let mut counts = vec![0usize; n];
        for i in 0..1000 {
            let id = format!("chunk_{i}");
            counts[shard_for(&id, n)] += 1;
        }
        // Each shard should get 150–350 (very conservative range for uniformity).
        for (i, &c) in counts.iter().enumerate() {
            assert!(
                (150..=350).contains(&c),
                "shard {i} got {c}/1000; expected 150-350 for uniform distribution"
            );
        }
    }

    // ── upsert + delete ──────────────────────────────────────────────────────

    /// Upsert a doc, then delete it; search must return no hit.
    #[test]
    fn upsert_delete_removes_from_correct_shard() {
        let dim = 8;
        let (_dir, idx) = make_index(4, dim);

        let doc_id = "chunk_delete_test";
        let shard_id = shard_for(doc_id, 4);

        let doc = EmbedResult {
            doc_id: doc_id.to_string(),
            embedding: make_vec(1.0, dim),
        };
        idx.upsert(&doc).expect("upsert");

        // Verify it is in the correct shard.
        {
            let conn = idx.shards[shard_id].lock().unwrap();
            let count: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM shard_embeddings WHERE doc_id = ?1",
                    params![doc_id],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(count, 1, "doc must be in shard {shard_id} after upsert");
        }

        idx.delete(doc_id).expect("delete");

        {
            let conn = idx.shards[shard_id].lock().unwrap();
            let count: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM shard_embeddings WHERE doc_id = ?1",
                    params![doc_id],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(count, 0, "doc must be gone after delete");
        }
    }

    // ── upsert + search round-trip ────────────────────────────────────────────

    /// Store one doc; query with the same vector; must return that doc as top-1.
    #[test]
    fn upsert_search_round_trip_single() {
        let dim = 16;
        let (_dir, idx) = make_index(4, dim);

        let doc = EmbedResult {
            doc_id: "rt_single".to_string(),
            embedding: make_vec(1.0, dim),
        };
        idx.upsert(&doc).expect("upsert");

        let results = idx.search(&make_vec(1.0, dim), 1).expect("search");
        assert_eq!(results.len(), 1, "must return 1 hit");
        assert_eq!(results[0].doc_id, "rt_single");
        assert!(
            results[0].distance < 0.01,
            "self-distance must be < 0.01, got {}",
            results[0].distance
        );
    }

    // ── fan-out merge correctness vs single-shard baseline ───────────────────

    /// Store N docs on a 4-shard index; query with each doc's own vector; it must
    /// be top-1 in every query.  Verifies fan-out merge correctness.
    #[test]
    fn fanout_merge_correctness() {
        let dim = 16;
        let (_dir, idx) = make_index(4, dim);

        let n = 20usize;
        let mut docs: Vec<EmbedResult> = Vec::with_capacity(n);
        for i in 0..n {
            let emb = make_vec(i as f32 * 50.0 + 1.0, dim);
            docs.push(EmbedResult {
                doc_id: format!("doc_{i}"),
                embedding: emb,
            });
        }
        for d in &docs {
            idx.upsert(d).expect("upsert");
        }

        for (i, doc) in docs.iter().enumerate() {
            let results = idx.search(&doc.embedding, 3).expect("search");
            assert!(
                !results.is_empty(),
                "search must return results for doc_{i}"
            );
            assert_eq!(
                results[0].doc_id,
                format!("doc_{i}"),
                "doc_{i}: top-1 must be itself, got {}",
                results[0].doc_id
            );
            // Verify ascending order.
            for w in results.windows(2) {
                assert!(
                    w[0].distance <= w[1].distance,
                    "doc_{i}: results must be ascending by distance"
                );
            }
        }
    }

    /// Single-shard baseline matches 4-shard result for same data.
    ///
    /// Stores 10 docs on both a 1-shard and a 4-shard index; top-1 query must
    /// return the same doc_id from both.
    #[test]
    fn single_shard_baseline_matches_multishard() {
        let dim = 16;
        let (_dir1, idx1) = make_index(1, dim);
        let (_dir4, idx4) = make_index(4, dim);

        let n = 10usize;
        for i in 0..n {
            let emb = make_vec(i as f32 * 30.0 + 2.0, dim);
            let doc = EmbedResult {
                doc_id: format!("base_{i}"),
                embedding: emb.clone(),
            };
            idx1.upsert(&doc).expect("upsert 1");
            idx4.upsert(&doc).expect("upsert 4");
        }

        for i in 0..n {
            let query = make_vec(i as f32 * 30.0 + 2.0, dim);
            let r1 = idx1.search(&query, 1).expect("search 1");
            let r4 = idx4.search(&query, 1).expect("search 4");
            assert_eq!(
                r1[0].doc_id, r4[0].doc_id,
                "query {i}: 1-shard top-1 ({}) must match 4-shard top-1 ({})",
                r1[0].doc_id, r4[0].doc_id
            );
        }
    }

    // ── dimension mismatch ────────────────────────────────────────────────────

    /// Upsert with wrong dimension must return DimMismatch.
    #[test]
    fn upsert_dim_mismatch_error() {
        let dim = 16;
        let (_dir, idx) = make_index(4, dim);
        let bad = EmbedResult {
            doc_id: "bad_dim".to_string(),
            embedding: vec![0.0f32; 8], // wrong
        };
        let err = idx.upsert(&bad).unwrap_err();
        assert!(
            matches!(
                err,
                ShardError::DimMismatch {
                    expected: 16,
                    actual: 8
                }
            ),
            "unexpected: {err:?}"
        );
    }

    /// Search with wrong dimension must return DimMismatch.
    #[test]
    fn search_dim_mismatch_error() {
        let dim = 16;
        let (_dir, idx) = make_index(4, dim);
        let err = idx.search(&[0.0f32; 8], 3).unwrap_err();
        assert!(
            matches!(
                err,
                ShardError::DimMismatch {
                    expected: 16,
                    actual: 8
                }
            ),
            "unexpected: {err:?}"
        );
    }

    // ── migration path ────────────────────────────────────────────────────────

    /// If cascade_vec.db exists in the root, opening a ShardedIndex must copy
    /// it to cascade_vec_shard_0.db (and not error).
    #[test]
    fn legacy_migration_copies_to_shard_0() {
        #[cfg(feature = "vec")]
        load_vec_extension();

        let dir = TempDir::new().expect("tmpdir");
        let legacy = dir.path().join("cascade_vec.db");
        // Create a minimal SQLite file.
        let conn = Connection::open(&legacy).expect("open legacy db");
        conn.execute_batch("CREATE TABLE dummy (x INTEGER);")
            .unwrap();
        drop(conn);

        assert!(
            legacy.exists(),
            "legacy file must exist before ShardedIndex::new"
        );

        // Opening ShardedIndex must succeed and copy the legacy file.
        let idx = ShardedIndex::new(dir.path(), 4, 16).expect("ShardedIndex::new with legacy");
        assert_eq!(idx.shard_count(), 4);

        let shard_0 = dir.path().join("cascade_vec_shard_0.db");
        assert!(shard_0.exists(), "shard_0 must exist after migration");
        assert!(
            legacy.exists(),
            "legacy file must still exist (non-destructive)"
        );
    }

    // ── integration: 100 docs across 4 shards ────────────────────────────────

    /// Index 100 docs across 4 shards; top-5 search must include the best match.
    #[test]
    fn integration_100_docs_4_shards() {
        let dim = 16;
        let (_dir, idx) = make_index(4, dim);

        // Store 100 docs with widely-spaced seeds.
        let n = 100usize;
        for i in 0..n {
            let emb = make_vec(i as f32 * 10.0 + 1.0, dim);
            idx.upsert(&EmbedResult {
                doc_id: format!("integ_{i}"),
                embedding: emb,
            })
            .expect("upsert");
        }

        // Query for doc_42 — it must appear in top-5 results.
        let query = make_vec(42.0 * 10.0 + 1.0, dim);
        let results = idx.search(&query, 5).expect("search");
        assert!(!results.is_empty(), "must return results");
        assert_eq!(
            results[0].doc_id, "integ_42",
            "top-1 must be integ_42, got {}",
            results[0].doc_id
        );
        assert!(results.len() <= 5, "must return <= 5 hits");
    }

    // ── shard file isolation ─────────────────────────────────────────────────

    /// Doc stored in shard i must NOT appear when querying shard j directly.
    #[test]
    fn doc_not_in_wrong_shard() {
        let dim = 8;
        let (_dir, idx) = make_index(4, dim);

        let doc_id = "isolated_doc";
        let shard_id = shard_for(doc_id, 4);
        let other_shard = (shard_id + 1) % 4;

        idx.upsert(&EmbedResult {
            doc_id: doc_id.to_string(),
            embedding: make_vec(7.0, dim),
        })
        .expect("upsert");

        // The doc must be in its shard.
        {
            let conn = idx.shards[shard_id].lock().unwrap();
            let n: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM shard_embeddings WHERE doc_id=?1",
                    params![doc_id],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(n, 1, "doc must be in shard {shard_id}");
        }

        // It must NOT be in another shard.
        {
            let conn = idx.shards[other_shard].lock().unwrap();
            let n: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM shard_embeddings WHERE doc_id=?1",
                    params![doc_id],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(n, 0, "doc must NOT be in shard {other_shard}");
        }
    }

    // ── bug #6: legacy migration rebalance ────────────────────────────────────

    /// Seed shard_0 with N docs that all hash to different shards, then open a
    /// 4-shard ShardedIndex which triggers rebalance_shard_0.  After opening,
    /// rows must be distributed per shard_for — not all in shard_0.
    #[test]
    fn legacy_rebalance_distributes_rows_across_shards() {
        #[cfg(feature = "vec")]
        load_vec_extension();

        let dim = 8;
        let dir = TempDir::new().expect("tmpdir");
        let shard_count = 4usize;

        // Build doc_ids that cover multiple shards.
        // We use a large sample and verify no single shard holds all rows.
        let n = 40usize;
        let doc_ids: Vec<String> = (0..n).map(|i| format!("legacy_doc_{i}")).collect();

        // Pre-seed cascade_vec.db (the legacy single-file path) with all N docs.
        // Use the shard schema so ShardedIndex can read the rows on migration.
        {
            let legacy_path = dir.path().join("cascade_vec.db");
            let conn = Connection::open(&legacy_path).expect("open legacy");
            apply_shard_schema(&conn, dim).expect("apply schema");
            conn.execute_batch("BEGIN").unwrap();
            for id in &doc_ids {
                let blob: Vec<u8> = vec![0u8; dim * 4]; // zero vector
                conn.execute(
                    "INSERT OR REPLACE INTO shard_embeddings(doc_id, embedding) VALUES(?1, ?2)",
                    params![id, blob],
                )
                .expect("insert legacy row");
            }
            conn.execute_batch("COMMIT").unwrap();
        }

        // Opening ShardedIndex triggers the legacy copy + rebalance.
        let idx = ShardedIndex::new(dir.path(), shard_count, dim)
            .expect("ShardedIndex::new with legacy rebalance");

        // Count rows per shard after rebalance.
        let mut per_shard = vec![0usize; shard_count];
        for (i, arc_conn) in idx.shards.iter().enumerate() {
            let conn = arc_conn.lock().unwrap();
            let count: i64 = conn
                .query_row("SELECT COUNT(*) FROM shard_embeddings", [], |r| r.get(0))
                .unwrap();
            per_shard[i] = count as usize;
        }

        // Total must equal N (no rows lost, no duplicates).
        let total: usize = per_shard.iter().sum();
        assert_eq!(total, n, "total rows after rebalance must equal N");

        // Not all rows must be in shard_0 — rebalance must have moved some.
        assert!(
            per_shard[0] < n,
            "shard_0 must not hold all rows after rebalance (got {}/{})",
            per_shard[0],
            n
        );

        // Verify each doc_id is in its correct shard per shard_for.
        for id in &doc_ids {
            let expected_shard = shard_for(id, shard_count);
            let conn = idx.shards[expected_shard].lock().unwrap();
            let count: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM shard_embeddings WHERE doc_id = ?1",
                    params![id],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(
                count, 1,
                "doc {id} must be in shard {expected_shard} after rebalance"
            );
        }
    }

    // ── rag-04: chunked batching rebalance ───────────────────────────────────

    /// Seed shard_0 with N > REBALANCE_BATCH_SIZE rows, trigger legacy rebalance,
    /// and verify all rows end up in their correct shards with no losses.
    ///
    /// N = 2500, REBALANCE_BATCH_SIZE = 1000: requires at least 3 batch iterations
    /// to drain all misrouted rows, exercising the chunked loop.
    #[test]
    fn batched_rebalance_distributes_large_legacy_index() {
        #[cfg(feature = "vec")]
        load_vec_extension();

        let dim = 4;
        let shard_count = 4usize;
        let n = 2500usize; // intentionally > REBALANCE_BATCH_SIZE * 2
        let dir = TempDir::new().expect("tmpdir");

        // Pre-seed cascade_vec.db with 2500 rows.
        {
            let legacy_path = dir.path().join("cascade_vec.db");
            let conn = Connection::open(&legacy_path).expect("open legacy");
            apply_shard_schema(&conn, dim).expect("apply schema");
            conn.execute_batch("BEGIN").unwrap();
            for i in 0..n {
                let id = format!("batch_doc_{i}");
                let blob: Vec<u8> = vec![0u8; dim * 4];
                conn.execute(
                    "INSERT OR REPLACE INTO shard_embeddings(doc_id, embedding) VALUES(?1, ?2)",
                    params![id, blob],
                )
                .expect("insert row");
            }
            conn.execute_batch("COMMIT").unwrap();
        }

        // Opening ShardedIndex triggers copy + chunked rebalance.
        let idx = ShardedIndex::new(dir.path(), shard_count, dim)
            .expect("ShardedIndex::new with large legacy");

        // All N rows must survive (no losses, no duplicates).
        let total = idx.total_count();
        assert_eq!(total, n, "total rows after batched rebalance must equal N={n}");

        // Each doc must be in its correct shard per shard_for.
        for i in 0..n {
            let id = format!("batch_doc_{i}");
            let expected = shard_for(&id, shard_count);
            let conn = idx.shards[expected].lock().unwrap();
            let count: i64 = conn
                .query_row(
                    "SELECT COUNT(*) FROM shard_embeddings WHERE doc_id = ?1",
                    params![id],
                    |r| r.get(0),
                )
                .unwrap();
            assert_eq!(count, 1, "batch_doc_{i} must be in shard {expected}");
        }
    }
}
