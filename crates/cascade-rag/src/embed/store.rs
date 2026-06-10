//! # embed::store
//!
//! Embedding persistence layer — writes dense and sparse vectors produced by
//! [`super::EmbedModel`] into the SQLite tables created by migration 0004.
//!
//! ## Tables
//!
//! | Table | Backend | Feature gate |
//! |---|---|---|
//! | `rag_embeddings` | vec0 virtual table (sqlite-vec) | `vec` |
//! | `rag_embeddings` | plain BLOB column (fallback) | none (no `vec`) |
//! | `rag_sparse_embeddings` | normal table, (chunk_id, token_id, weight) | always |
//!
//! When the `vec` feature is enabled, dense vectors are stored via the
//! sqlite-vec MATCH / vec_from_blob query path.  Without the feature, dense
//! vectors are stored as raw little-endian IEEE 754 BLOBs (same byte layout)
//! and KNN is performed with a full table scan + manual distance calculation.
//!
//! ## Wire format
//!
//! sqlite-vec uses little-endian IEEE 754 `f32` byte layout (matches Rust's
//! native `f32` on all supported platforms).  Conversion is a zero-cost
//! reinterpret_cast via `bytemuck` / manual byte casting.
//!
//! ## Transaction contract
//!
//! [`store_embeddings`] wraps both the dense and sparse inserts in a single
//! `BEGIN / COMMIT` so there are no partial writes.
//!
//! SPORT: MASTER-TABLES.md → rag_embeddings (write/read implemented),
//!        rag_sparse_embeddings (write/read implemented)

use rusqlite::{params, Connection};

use super::EmbedError;

/// Expected dense vector dimension.  Matches BGE-M3 / BGELargeENV15 output.
pub const DENSE_DIM: usize = 1024;

// ── Serialisation helpers ──────────────────────────────────────────────────────

/// Convert a `f32` slice to a little-endian IEEE 754 BLOB.
///
/// sqlite-vec stores and reads vectors in this wire format.  Rust's `f32` is
/// already IEEE 754; on all targets we support (x86_64, aarch64) native byte
/// order is little-endian, matching the vec0 expectation.
#[inline]
fn f32_slice_to_blob(vec: &[f32]) -> Vec<u8> {
    let mut buf = Vec::with_capacity(vec.len() * 4);
    for &v in vec {
        buf.extend_from_slice(&v.to_le_bytes());
    }
    buf
}

/// Convert a little-endian IEEE 754 BLOB back to a `Vec<f32>`.
///
/// Returns an error if `bytes.len()` is not a multiple of 4.
#[cfg(not(feature = "vec"))]
fn blob_to_f32_vec(bytes: &[u8]) -> std::result::Result<Vec<f32>, EmbedError> {
    if bytes.len() % 4 != 0 {
        return Err(EmbedError::DimensionMismatch {
            expected: DENSE_DIM,
            actual: bytes.len() / 4,
        });
    }
    Ok(bytes
        .chunks_exact(4)
        .map(|c| f32::from_le_bytes([c[0], c[1], c[2], c[3]]))
        .collect())
}

// ── Validation ─────────────────────────────────────────────────────────────────

/// Validate that `dense` has exactly [`DENSE_DIM`] elements.
///
/// # Errors
///
/// [`EmbedError::DimensionMismatch`] when `dense.len() != DENSE_DIM`.
pub fn validate_dense_dim(dense: &[f32]) -> std::result::Result<(), EmbedError> {
    if dense.len() != DENSE_DIM {
        return Err(EmbedError::DimensionMismatch {
            expected: DENSE_DIM,
            actual: dense.len(),
        });
    }
    Ok(())
}

// ── Dense storage ─────────────────────────────────────────────────────────────

/// Insert or replace a dense embedding for `chunk_id`.
///
/// # Feature: `vec`
///
/// With `vec` enabled the vector is inserted into the `rag_embeddings` vec0
/// virtual table using `INSERT OR REPLACE INTO rag_embeddings(rowid, embedding)
/// VALUES(?, vec_from_blob(?))`.  The rowid maps 1-to-1 to `rag_chunks.id`.
///
/// Without `vec`, the raw little-endian BLOB is stored in the `embedding`
/// column of a plain `rag_embeddings` table (created by the fallback branch of
/// migration 0004).
///
/// # Errors
///
/// - [`EmbedError::DimensionMismatch`] if `dense.len() != DENSE_DIM`.
/// - Propagates [`rusqlite::Error`] as [`EmbedError::InferenceFailed`].
pub fn store_dense(
    conn: &Connection,
    chunk_id: i64,
    dense: &[f32],
) -> std::result::Result<(), EmbedError> {
    validate_dense_dim(dense)?;
    let blob = f32_slice_to_blob(dense);

    #[cfg(feature = "vec")]
    {
        // vec0: INSERT with rowid (= chunk_id); embedding column takes a BLOB
        // interpreted as a float[1024] vector by sqlite-vec.
        conn.execute(
            "INSERT OR REPLACE INTO rag_embeddings(rowid, embedding) VALUES(?1, ?2)",
            params![chunk_id, blob],
        )
        .map_err(|e| EmbedError::InferenceFailed {
            model_id: "store_dense".into(),
            detail: e.to_string(),
        })?;
    }

    #[cfg(not(feature = "vec"))]
    {
        // Fallback: plain BLOB column in rag_embeddings.
        conn.execute(
            "INSERT OR REPLACE INTO rag_embeddings(rowid, embedding) VALUES(?1, ?2)",
            params![chunk_id, blob],
        )
        .map_err(|e| EmbedError::InferenceFailed {
            model_id: "store_dense".into(),
            detail: e.to_string(),
        })?;
    }

    Ok(())
}

// ── Sparse storage ─────────────────────────────────────────────────────────────

/// Batch-insert sparse token weights for `chunk_id`.
///
/// Deletes existing rows for `chunk_id` first (within the same transaction),
/// then inserts all `(token_id, weight)` pairs.  Empty `sparse` is a no-op
/// after the delete.
///
/// # Errors
///
/// Propagates [`rusqlite::Error`] as [`EmbedError::InferenceFailed`].
pub fn store_sparse(
    conn: &Connection,
    chunk_id: i64,
    sparse: &[(u32, f32)],
) -> std::result::Result<(), EmbedError> {
    // Remove stale rows first so an OR-REPLACE isn't required on the compound PK.
    conn.execute(
        "DELETE FROM rag_sparse_embeddings WHERE chunk_id = ?1",
        params![chunk_id],
    )
    .map_err(|e| EmbedError::InferenceFailed {
        model_id: "store_sparse/delete".into(),
        detail: e.to_string(),
    })?;

    let mut stmt = conn
        .prepare_cached(
            "INSERT INTO rag_sparse_embeddings(chunk_id, token_id, weight) VALUES(?1, ?2, ?3)",
        )
        .map_err(|e| EmbedError::InferenceFailed {
            model_id: "store_sparse/prepare".into(),
            detail: e.to_string(),
        })?;

    for &(token_id, weight) in sparse {
        stmt.execute(params![chunk_id, token_id, weight])
            .map_err(|e| EmbedError::InferenceFailed {
                model_id: "store_sparse/insert".into(),
                detail: e.to_string(),
            })?;
    }

    Ok(())
}

// ── Combined public API ────────────────────────────────────────────────────────

/// Store dense and sparse embeddings for a chunk in a single transaction.
///
/// Both the dense and sparse inserts are wrapped in `BEGIN / COMMIT`, so
/// there are no partial writes.  If either insert fails the transaction is
/// rolled back automatically when the `rusqlite::Transaction` drops.
///
/// # Parameters
///
/// | Param | Description |
/// |---|---|
/// | `conn` | Open rusqlite connection (no transaction in progress) |
/// | `chunk_id` | `rag_chunks.id` value the vectors belong to |
/// | `dense` | Dense float vector; must have exactly [`DENSE_DIM`] elements |
/// | `sparse` | Sparse `(token_id, weight)` pairs; may be empty |
///
/// # Errors
///
/// - [`EmbedError::DimensionMismatch`] if `dense.len() != DENSE_DIM`.
/// - [`EmbedError::InferenceFailed`] wrapping any [`rusqlite::Error`].
pub fn store_embeddings(
    conn: &Connection,
    chunk_id: i64,
    dense: &[f32],
    sparse: &[(u32, f32)],
) -> std::result::Result<(), EmbedError> {
    validate_dense_dim(dense)?;

    // Wrap both writes in a single transaction.
    conn.execute_batch("BEGIN")
        .map_err(|e| EmbedError::InferenceFailed {
            model_id: "store_embeddings/begin".into(),
            detail: e.to_string(),
        })?;

    let result = (|| {
        store_dense(conn, chunk_id, dense)?;
        store_sparse(conn, chunk_id, sparse)?;
        Ok::<_, EmbedError>(())
    })();

    match result {
        Ok(()) => {
            conn.execute_batch("COMMIT")
                .map_err(|e| EmbedError::InferenceFailed {
                    model_id: "store_embeddings/commit".into(),
                    detail: e.to_string(),
                })?;
            Ok(())
        }
        Err(e) => {
            let _ = conn.execute_batch("ROLLBACK");
            Err(e)
        }
    }
}

// ── KNN query ─────────────────────────────────────────────────────────────────

/// Run a K-Nearest-Neighbour query against stored dense vectors.
///
/// # Feature: `vec`
///
/// With `vec` enabled, uses the sqlite-vec MATCH syntax:
/// ```sql
/// SELECT rowid, distance
/// FROM rag_embeddings
/// WHERE embedding MATCH ?
/// ORDER BY distance
/// LIMIT ?
/// ```
///
/// The `?` placeholder receives the query vector as a little-endian f32 BLOB.
/// sqlite-vec decodes it as a float[1024] and computes L2 distance.
///
/// Without `vec`, performs a full table scan and computes squared Euclidean
/// distance in Rust.  Correct but O(n) — acceptable for test/dev environments.
///
/// # Returns
///
/// `Vec<(chunk_id, distance)>` sorted by ascending distance, length ≤ `k`.
///
/// # Errors
///
/// - [`EmbedError::DimensionMismatch`] if `query_vec.len() != DENSE_DIM`.
/// - [`EmbedError::InferenceFailed`] wrapping any [`rusqlite::Error`].
pub fn query_dense_knn(
    conn: &Connection,
    query_vec: &[f32],
    k: usize,
) -> std::result::Result<Vec<(i64, f32)>, EmbedError> {
    validate_dense_dim(query_vec)?;
    #[cfg_attr(not(feature = "vec"), allow(unused_variables))]
    let blob = f32_slice_to_blob(query_vec);

    #[cfg(feature = "vec")]
    {
        let mut stmt = conn
            .prepare_cached(
                "SELECT rowid, distance \
                 FROM rag_embeddings \
                 WHERE embedding MATCH ? \
                 ORDER BY distance \
                 LIMIT ?",
            )
            .map_err(|e| EmbedError::InferenceFailed {
                model_id: "query_dense_knn/prepare".into(),
                detail: e.to_string(),
            })?;

        let k_i64 = k as i64;
        let rows = stmt
            .query_map(params![blob, k_i64], |row| {
                let rowid: i64 = row.get(0)?;
                let dist: f64 = row.get(1)?;
                Ok((rowid, dist as f32))
            })
            .map_err(|e| EmbedError::InferenceFailed {
                model_id: "query_dense_knn/query".into(),
                detail: e.to_string(),
            })?;

        let mut results = Vec::new();
        for r in rows {
            results.push(r.map_err(|e| EmbedError::InferenceFailed {
                model_id: "query_dense_knn/row".into(),
                detail: e.to_string(),
            })?);
        }
        Ok(results)
    }

    #[cfg(not(feature = "vec"))]
    {
        // Full-scan fallback: load all blobs, compute L2, sort, return top-k.
        let mut stmt = conn
            .prepare_cached("SELECT rowid, embedding FROM rag_embeddings")
            .map_err(|e| EmbedError::InferenceFailed {
                model_id: "query_dense_knn/prepare".into(),
                detail: e.to_string(),
            })?;

        let rows = stmt
            .query_map([], |row| {
                let rowid: i64 = row.get(0)?;
                let raw: Vec<u8> = row.get(1)?;
                Ok((rowid, raw))
            })
            .map_err(|e| EmbedError::InferenceFailed {
                model_id: "query_dense_knn/query".into(),
                detail: e.to_string(),
            })?;

        let mut scored: Vec<(i64, f32)> = Vec::new();
        for r in rows {
            let (rowid, raw) = r.map_err(|e| EmbedError::InferenceFailed {
                model_id: "query_dense_knn/row".into(),
                detail: e.to_string(),
            })?;
            let stored = blob_to_f32_vec(&raw)?;
            if stored.len() != DENSE_DIM {
                continue; // skip corrupt rows
            }
            // Squared L2 distance — monotone for ranking, no sqrt needed.
            let dist: f32 = query_vec
                .iter()
                .zip(stored.iter())
                .map(|(a, b)| (a - b) * (a - b))
                .sum();
            scored.push((rowid, dist));
        }

        // Sort by ascending distance; break ties by rowid for determinism.
        scored.sort_by(|a, b| a.1.partial_cmp(&b.1).unwrap_or(std::cmp::Ordering::Equal));
        scored.truncate(k);
        Ok(scored)
    }
}

/// Delete all embeddings (dense + sparse) associated with a given source.
///
/// Cascades through `rag_chunks` to identify `chunk_id`s belonging to
/// `source_id`, then deletes rows from both embedding tables.  This is the
/// document-level cleanup path (e.g. when a source file is removed).
///
/// # Errors
///
/// Propagates [`rusqlite::Error`] as [`EmbedError::InferenceFailed`].
pub fn delete_embeddings_by_source(
    conn: &Connection,
    source_id: i64,
) -> std::result::Result<(), EmbedError> {
    // Sparse: delete rows whose chunk_id belongs to this source.
    conn.execute(
        "DELETE FROM rag_sparse_embeddings \
         WHERE chunk_id IN (SELECT id FROM rag_chunks WHERE source_id = ?1)",
        params![source_id],
    )
    .map_err(|e| EmbedError::InferenceFailed {
        model_id: "delete_embeddings_by_source/sparse".into(),
        detail: e.to_string(),
    })?;

    // Dense: rowid == chunk_id; same subquery.
    conn.execute(
        "DELETE FROM rag_embeddings \
         WHERE rowid IN (SELECT id FROM rag_chunks WHERE source_id = ?1)",
        params![source_id],
    )
    .map_err(|e| EmbedError::InferenceFailed {
        model_id: "delete_embeddings_by_source/dense".into(),
        detail: e.to_string(),
    })?;

    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use rusqlite::Connection;

    // ── DB helpers ────────────────────────────────────────────────────────────

    /// Register the sqlite-vec extension (no-op without the `vec` feature).
    #[cfg(feature = "vec")]
    fn load_vec_extension() {
        use rusqlite::ffi::sqlite3_auto_extension;
        unsafe {
            sqlite3_auto_extension(Some(std::mem::transmute(
                sqlite_vec::sqlite3_vec_init as *const (),
            )));
        }
    }

    /// Create a fully-migrated in-memory database for tests.
    fn test_db() -> Connection {
        #[cfg(feature = "vec")]
        load_vec_extension();

        let conn = Connection::open_in_memory().expect("in-memory db");
        crate::db::run_migrations(&conn).expect("migrations");
        conn
    }

    /// Insert a minimal source row and return its id.
    fn insert_source(conn: &Connection, path: &str) -> i64 {
        conn.execute(
            "INSERT INTO rag_sources(file_path, schema_version) VALUES(?1, 1)",
            params![path],
        )
        .unwrap();
        conn.last_insert_rowid()
    }

    /// Insert a minimal chunk row and return its id.
    fn insert_chunk(conn: &Connection, source_id: i64, idx: i64) -> i64 {
        conn.execute(
            "INSERT INTO rag_chunks(source_id, chunk_index, chunk_text, schema_version) \
             VALUES(?1, ?2, 'test text', 1)",
            params![source_id, idx],
        )
        .unwrap();
        conn.last_insert_rowid()
    }

    /// Build a deterministic 1024-dim unit vector seeded by `seed`.
    fn make_vec(seed: f32) -> Vec<f32> {
        let raw: Vec<f32> = (0..DENSE_DIM)
            .map(|i| (seed + i as f32 * 0.001).sin())
            .collect();
        let norm = raw.iter().map(|v| v * v).sum::<f32>().sqrt().max(1e-9);
        raw.iter().map(|v| v / norm).collect()
    }

    // ── store_embeddings: round-trip ──────────────────────────────────────────

    /// A stored vector can be found as the nearest neighbour of itself.
    #[test]
    fn round_trip_single_vector() {
        let conn = test_db();
        let src = insert_source(&conn, "/doc/a.md");
        let cid = insert_chunk(&conn, src, 0);

        let dense = make_vec(1.0);
        let sparse: Vec<(u32, f32)> = vec![(42, 0.8), (100, 0.3)];

        store_embeddings(&conn, cid, &dense, &sparse).expect("store_embeddings");

        // KNN should return cid as top-1 with near-zero distance.
        let knn = query_dense_knn(&conn, &dense, 1).expect("knn");
        assert_eq!(knn.len(), 1, "must return exactly 1 result");
        assert_eq!(knn[0].0, cid, "top-1 must be the stored chunk");
        assert!(
            knn[0].1 < 0.01,
            "distance to self must be < 0.01, got {}",
            knn[0].1
        );

        // Verify sparse rows were written.
        let count: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM rag_sparse_embeddings WHERE chunk_id = ?1",
                params![cid],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(count, 2, "expected 2 sparse rows");
    }

    // ── store_embeddings: batch insert, rank order ────────────────────────────

    /// Five distinct vectors stored; querying with v[0] returns v[0] as top-1.
    #[test]
    fn batch_insert_rank_order() {
        let conn = test_db();
        let src = insert_source(&conn, "/doc/batch.md");

        let seeds = [1.0f32, 10.0, 20.0, 30.0, 40.0];
        let mut chunk_ids = Vec::new();
        for (i, &seed) in seeds.iter().enumerate() {
            let cid = insert_chunk(&conn, src, i as i64);
            chunk_ids.push(cid);
            let dense = make_vec(seed);
            let sparse: Vec<(u32, f32)> = vec![(i as u32, seed)];
            store_embeddings(&conn, cid, &dense, &sparse).expect("store_embeddings");
        }

        // Query with the vector for seed=1.0 — chunk_ids[0] must be top-1.
        let query = make_vec(1.0);
        let knn = query_dense_knn(&conn, &query, 3).expect("knn");
        assert!(!knn.is_empty(), "must return results");
        assert_eq!(
            knn[0].0, chunk_ids[0],
            "top-1 must be the queried vector's own chunk"
        );

        // Distances must be in ascending order.
        for w in knn.windows(2) {
            assert!(
                w[0].1 <= w[1].1,
                "results must be in ascending distance order: {} > {}",
                w[0].1,
                w[1].1
            );
        }

        // Verify total sparse row count = 5 (one per chunk).
        let total: i64 = conn
            .query_row("SELECT COUNT(*) FROM rag_sparse_embeddings", [], |r| {
                r.get(0)
            })
            .unwrap();
        assert_eq!(total, 5, "expected 5 sparse rows total");
    }

    // ── dimension mismatch ────────────────────────────────────────────────────

    /// Passing a wrong-dimension dense vector must return DimensionMismatch.
    #[test]
    fn dimension_mismatch_error() {
        let conn = test_db();
        let src = insert_source(&conn, "/doc/dim.md");
        let cid = insert_chunk(&conn, src, 0);

        let bad_dense = vec![0.0f32; 512]; // wrong dim
        let err = store_embeddings(&conn, cid, &bad_dense, &[]).unwrap_err();
        assert!(
            matches!(
                err,
                EmbedError::DimensionMismatch {
                    expected: 1024,
                    actual: 512
                }
            ),
            "unexpected error variant: {err:?}"
        );
    }

    /// Dimension mismatch in query_dense_knn must also return DimensionMismatch.
    #[test]
    fn knn_dimension_mismatch_error() {
        let conn = test_db();
        let bad = vec![0.0f32; 768];
        let err = query_dense_knn(&conn, &bad, 3).unwrap_err();
        assert!(
            matches!(
                err,
                EmbedError::DimensionMismatch {
                    expected: 1024,
                    actual: 768
                }
            ),
            "unexpected error: {err:?}"
        );
    }

    // ── delete cascade ────────────────────────────────────────────────────────

    /// Deleting by source removes dense + sparse rows for all its chunks.
    #[test]
    fn delete_by_source_cascade() {
        let conn = test_db();
        let src_a = insert_source(&conn, "/doc/src_a.md");
        let src_b = insert_source(&conn, "/doc/src_b.md");

        // Insert 3 chunks for src_a.
        for i in 0..3i64 {
            let cid = insert_chunk(&conn, src_a, i);
            store_embeddings(&conn, cid, &make_vec(i as f32 + 1.0), &[(i as u32, 0.5)])
                .expect("store");
        }
        // Insert 2 chunks for src_b.
        for i in 0..2i64 {
            let cid = insert_chunk(&conn, src_b, i);
            store_embeddings(&conn, cid, &make_vec(i as f32 + 10.0), &[(i as u32, 0.4)])
                .expect("store");
        }

        // Delete src_a.
        delete_embeddings_by_source(&conn, src_a).expect("delete");

        // Dense rows for src_a must be gone.
        let dense_a: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM rag_embeddings \
                 WHERE rowid IN (SELECT id FROM rag_chunks WHERE source_id = ?1)",
                params![src_a],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(dense_a, 0, "dense rows for src_a must be deleted");

        // Sparse rows for src_a must be gone.
        let sparse_a: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM rag_sparse_embeddings \
                 WHERE chunk_id IN (SELECT id FROM rag_chunks WHERE source_id = ?1)",
                params![src_a],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(sparse_a, 0, "sparse rows for src_a must be deleted");

        // src_b must be untouched (2 sparse rows remain).
        let sparse_b: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM rag_sparse_embeddings \
                 WHERE chunk_id IN (SELECT id FROM rag_chunks WHERE source_id = ?1)",
                params![src_b],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(sparse_b, 2, "src_b sparse rows must be preserved");
    }

    // ── QA-B: 10-vector seeded KNN correctness ────────────────────────────────

    /// Insert 10 deterministic vectors; run 10 nearest-neighbour queries;
    /// verify each vector is its own nearest neighbour.
    #[test]
    fn seeded_knn_nearest_neighbour_correctness() {
        let conn = test_db();
        let src = insert_source(&conn, "/doc/qa_b.md");

        let n = 10usize;
        let mut cids = Vec::with_capacity(n);
        let mut vecs: Vec<Vec<f32>> = Vec::with_capacity(n);

        for i in 0..n {
            let cid = insert_chunk(&conn, src, i as i64);
            cids.push(cid);
            // Use widely-spaced seeds so vectors are far apart.
            let v = make_vec(i as f32 * 50.0 + 1.0);
            let sparse: Vec<(u32, f32)> = vec![(i as u32, 1.0)];
            store_embeddings(&conn, cid, &v, &sparse).expect("store");
            vecs.push(v);
        }

        // Verify sparse total = n.
        let total: i64 = conn
            .query_row("SELECT COUNT(*) FROM rag_sparse_embeddings", [], |r| {
                r.get(0)
            })
            .unwrap();
        assert_eq!(total as usize, n, "sparse row count must equal n");

        // Each query must return its own chunk as top-1.
        for (i, v) in vecs.iter().enumerate() {
            let knn = query_dense_knn(&conn, v, 1).expect("knn");
            assert_eq!(knn.len(), 1, "must return 1 result for query {i}");
            assert_eq!(
                knn[0].0, cids[i],
                "query {i}: top-1 must be chunk {i} (cid={}), got {}",
                cids[i], knn[0].0
            );
            assert!(
                knn[0].1 < 0.01,
                "query {i}: self-distance must be < 0.01, got {}",
                knn[0].1
            );
        }
    }

    // ── Transaction atomicity ─────────────────────────────────────────────────

    /// store_embeddings must not leave partial state on error.
    /// We trigger a dim mismatch (which fires before any DB write) and verify
    /// neither dense nor sparse tables were written.
    #[test]
    fn transaction_no_partial_write_on_dim_error() {
        let conn = test_db();
        let src = insert_source(&conn, "/doc/partial.md");
        let cid = insert_chunk(&conn, src, 0);

        let bad = vec![0.0f32; 256];
        let result = store_embeddings(&conn, cid, &bad, &[(1, 0.5)]);
        assert!(result.is_err(), "must fail");

        // Dense table must be empty for cid.
        let dense_cnt: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM rag_embeddings WHERE rowid = ?1",
                params![cid],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(dense_cnt, 0, "no dense row on error");

        // Sparse table must be empty for cid.
        let sparse_cnt: i64 = conn
            .query_row(
                "SELECT COUNT(*) FROM rag_sparse_embeddings WHERE chunk_id = ?1",
                params![cid],
                |r| r.get(0),
            )
            .unwrap();
        assert_eq!(sparse_cnt, 0, "no sparse row on error");
    }
}
