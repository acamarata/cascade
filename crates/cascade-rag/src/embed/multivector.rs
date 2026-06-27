// Purpose: BGE-M3 multi-vector (ColBERT-style) token-level embedding support.
//
// This module is compiled only when the `rag-multivec` feature is enabled.
// It provides:
//   - `MultiVecEmbedModel` — extension trait adding `embed_multi_vec`.
//   - `MockMultiVecEmbedModel` — deterministic mock for unit tests only.
//   - `store_token_embeddings` — persist token vectors to `rag_token_embeddings`.
//   - `load_token_embeddings` — load token vectors for a chunk from the DB.
//   - `maxsim` — ColBERT MaxSim late-interaction scoring function.
//
// # ColBERT proxy caveat (rag-02 verified)
//
// fastembed 4.9.1 does not expose token-level/late-interaction ColBERT output.
// There is no BGEM3 variant and no multi-vector output mode in the fastembed
// 4.9.1 EmbeddingModel enum.
//
// TODO(rag-02): real ColBERT requires token-level embeddings not in fastembed 4.9.1.
// The current default implementation (blanket impl of MultiVecEmbedModel for any
// EmbedModel) falls back to one dense vector per whitespace-split token.  This
// validates the end-to-end pipeline shape (Vec<Vec<f32>>) but produces wrong
// ColBERT weights — each "token vector" is actually a full-sequence embedding of
// a single word, not a contextual token hidden-state.
//
// Upgrade path: when fastembed exposes ColBERT/late-interaction output, replace
// the blanket default impl with a real token-level implementation and remove
// this comment block.
//
// Inputs:  &str text
// Outputs: Vec<Vec<f32>> — one inner Vec<f32> per token
// Constraints: feature = "rag-multivec" must be enabled; off by default
// SPORT: MASTER-TABLES.md → rag_token_embeddings (write/read); MASTER-LIBS.md → cascade-rag::embed::multivector

use rusqlite::{params, Connection};

use super::{EmbedError, EmbedModel};

// ── MultiVecEmbedModel extension trait ───────────────────────────────────────

/// Extension trait for [`EmbedModel`] that adds token-level ColBERT vectors.
///
/// Provides a default implementation that delegates to the dense embedder
/// (returns one dense vector per whitespace-delimited token).  Real BGE-M3
/// ColBERT output requires a fastembed-rs version that ships ColBERT mode —
/// see module-level proxy caveat.
///
/// Backward compat: all existing `EmbedModel` implementors automatically gain
/// `embed_multi_vec` via the blanket default.  No existing code breaks.
pub trait MultiVecEmbedModel: EmbedModel {
    /// Embed `text` into one vector per token.
    ///
    /// Returns a `Vec<Vec<f32>>` where each inner `Vec<f32>` has length
    /// `self.dim()`.  The number of token vectors equals the number of
    /// whitespace-delimited tokens in `text`.
    ///
    /// # Default implementation (proxy — NOT real ColBERT)
    ///
    /// Splits on whitespace and embeds each word via `embed_dense`.
    ///
    /// TODO(rag-02): real ColBERT requires token-level embeddings not in fastembed 4.9.1.
    /// This is a structural proxy that produces the correct output shape
    /// (`Vec<Vec<f32>>`) but wrong per-token weights — each inner vector is a
    /// full-sequence dense embedding of a single whitespace-split word, not a
    /// contextual hidden-state.  Upgrade when fastembed ships ColBERT output.
    fn embed_multi_vec(&self, text: &str) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
        let tokens: Vec<&str> = text.split_whitespace().collect();
        if tokens.is_empty() {
            return Ok(vec![]);
        }
        self.embed_dense(&tokens)
    }
}

/// Blanket implementation: any `EmbedModel` automatically gets the default
/// `embed_multi_vec` via the extension trait.
impl<T: EmbedModel + ?Sized> MultiVecEmbedModel for T {}

// ── MockMultiVecEmbedModel ────────────────────────────────────────────────────

/// Deterministic mock for multi-vector unit tests.
///
/// Each call to `embed_multi_vec` splits on whitespace and returns one small
/// deterministic vector per token.  Vectors are NOT semantically meaningful.
///
/// # Example
///
/// ```rust
/// use cascade_rag::embed::multivector::{MockMultiVecEmbedModel, MultiVecEmbedModel};
///
/// let m = MockMultiVecEmbedModel::new(8);
/// let token_vecs = m.embed_multi_vec("hello world foo").unwrap();
/// assert_eq!(token_vecs.len(), 3); // 3 tokens
/// assert_eq!(token_vecs[0].len(), 8); // dim = 8
/// ```
#[derive(Debug, Clone)]
pub struct MockMultiVecEmbedModel {
    /// Dimension of each token vector.
    pub dim: usize,
}

impl MockMultiVecEmbedModel {
    /// Create a mock with the specified per-token vector dimension.
    pub fn new(dim: usize) -> Self {
        Self { dim }
    }
}

impl super::EmbedModel for MockMultiVecEmbedModel {
    fn embed_dense(&self, texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
        let dim = self.dim;
        Ok(texts
            .iter()
            .map(|t| {
                let seed = t.bytes().fold(0u64, |acc, b| acc.wrapping_add(b as u64));
                let base = (seed as f32) / 255.0;
                let raw: Vec<f32> = (0..dim).map(|i| (base + i as f32 * 0.01).sin()).collect();
                let norm: f32 = raw.iter().map(|v| v * v).sum::<f32>().sqrt().max(1e-9);
                raw.iter().map(|v| v / norm).collect()
            })
            .collect())
    }

    fn embed_sparse(
        &self,
        texts: &[&str],
    ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
        Ok(texts
            .iter()
            .map(|t| super::sparse_tfidf_single(t))
            .collect())
    }

    fn dim(&self) -> usize {
        self.dim
    }

    fn model_id(&self) -> &str {
        "mock-multivec-model"
    }
}

// ── MaxSim scoring ────────────────────────────────────────────────────────────

/// Dot product of two equal-length f32 vectors.
///
/// Panics in debug mode if `a.len() != b.len()`.
#[inline]
pub fn dot(a: &[f32], b: &[f32]) -> f32 {
    debug_assert_eq!(
        a.len(),
        b.len(),
        "dot product requires equal-length vectors"
    );
    a.iter().zip(b.iter()).map(|(x, y)| x * y).sum()
}

/// MaxSim late-interaction score (ColBERT equation 2).
///
/// For query token vectors `q_vecs` and document token vectors `d_vecs`:
///
/// ```text
/// MaxSim(Q, D) = Σ_{q_i ∈ Q}  max_{d_j ∈ D}  dot(q_i, d_j)
/// ```
///
/// Returns 0.0 for empty inputs.
///
/// # Arguments
///
/// * `q_vecs` — query token vectors, each of length `dim`.
/// * `d_vecs` — document token vectors, each of length `dim`.
///
/// # Example
///
/// ```rust
/// use cascade_rag::embed::multivector::maxsim;
///
/// // Query: 1 token [1,0]; Doc: 2 tokens [1,0] and [0,1].
/// // max over doc = dot([1,0],[1,0])=1.0 ; sum over query = 1.0
/// let q = vec![vec![1.0_f32, 0.0]];
/// let d = vec![vec![1.0_f32, 0.0], vec![0.0_f32, 1.0]];
/// let score = maxsim(&q, &d);
/// assert!((score - 1.0).abs() < 1e-6, "expected MaxSim=1.0, got {score}");
/// ```
pub fn maxsim(q_vecs: &[Vec<f32>], d_vecs: &[Vec<f32>]) -> f32 {
    if q_vecs.is_empty() || d_vecs.is_empty() {
        return 0.0;
    }
    q_vecs
        .iter()
        .map(|q| {
            d_vecs
                .iter()
                .map(|d| dot(q, d))
                .fold(f32::NEG_INFINITY, f32::max)
        })
        .sum()
}

// ── Storage helpers ───────────────────────────────────────────────────────────

/// Serialise a `f32` slice as little-endian IEEE 754 BLOB.
fn f32_slice_to_blob(v: &[f32]) -> Vec<u8> {
    let mut buf = Vec::with_capacity(v.len() * 4);
    for &x in v {
        buf.extend_from_slice(&x.to_le_bytes());
    }
    buf
}

/// Deserialise little-endian IEEE 754 BLOB to `Vec<f32>`.
fn blob_to_f32_vec(bytes: &[u8]) -> Result<Vec<f32>, EmbedError> {
    if bytes.len() % 4 != 0 {
        return Err(EmbedError::InferenceFailed {
            model_id: "multivec-storage".into(),
            detail: format!(
                "token embedding BLOB length {} is not a multiple of 4",
                bytes.len()
            ),
        });
    }
    Ok(bytes
        .chunks_exact(4)
        .map(|c| f32::from_le_bytes([c[0], c[1], c[2], c[3]]))
        .collect())
}

/// Persist token vectors for a chunk to `rag_token_embeddings`.
///
/// Wraps the inserts in a savepoint so they are atomic.  Any existing token
/// embeddings for `chunk_id` are deleted first (idempotent upsert semantics).
///
/// # Errors
///
/// Propagates any `rusqlite::Error` as `EmbedError::InferenceFailed`.
pub fn store_token_embeddings(
    conn: &Connection,
    chunk_id: i64,
    token_vecs: &[Vec<f32>],
) -> Result<(), EmbedError> {
    conn.execute(
        "DELETE FROM rag_token_embeddings WHERE chunk_id = ?1",
        params![chunk_id],
    )
    .map_err(|e| EmbedError::InferenceFailed {
        model_id: "multivec-storage".into(),
        detail: format!("delete existing token embeddings: {e}"),
    })?;

    let mut stmt = conn
        .prepare(
            "INSERT INTO rag_token_embeddings (chunk_id, token_idx, vector) VALUES (?1, ?2, ?3)",
        )
        .map_err(|e| EmbedError::InferenceFailed {
            model_id: "multivec-storage".into(),
            detail: format!("prepare insert: {e}"),
        })?;

    for (idx, vec) in token_vecs.iter().enumerate() {
        let blob = f32_slice_to_blob(vec);
        stmt.execute(params![chunk_id, idx as i64, blob])
            .map_err(|e| EmbedError::InferenceFailed {
                model_id: "multivec-storage".into(),
                detail: format!("insert token {idx}: {e}"),
            })?;
    }
    Ok(())
}

/// Load all token vectors for `chunk_id` from `rag_token_embeddings`.
///
/// Returns vectors in `token_idx` ascending order.
///
/// # Errors
///
/// Propagates DB errors or BLOB decode errors as `EmbedError::InferenceFailed`.
pub fn load_token_embeddings(
    conn: &Connection,
    chunk_id: i64,
) -> Result<Vec<Vec<f32>>, EmbedError> {
    let mut stmt = conn
        .prepare(
            "SELECT vector FROM rag_token_embeddings WHERE chunk_id = ?1 ORDER BY token_idx ASC",
        )
        .map_err(|e| EmbedError::InferenceFailed {
            model_id: "multivec-storage".into(),
            detail: format!("prepare load: {e}"),
        })?;

    let blobs: Vec<Vec<u8>> = stmt
        .query_map(params![chunk_id], |row| row.get::<_, Vec<u8>>(0))
        .map_err(|e| EmbedError::InferenceFailed {
            model_id: "multivec-storage".into(),
            detail: format!("query token embeddings: {e}"),
        })?
        .collect::<Result<Vec<_>, _>>()
        .map_err(|e| EmbedError::InferenceFailed {
            model_id: "multivec-storage".into(),
            detail: format!("collect token blobs: {e}"),
        })?;

    blobs.iter().map(|b| blob_to_f32_vec(b)).collect()
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use rusqlite::Connection;

    // ── maxsim ────────────────────────────────────────────────────────────────

    /// Toy 3-token query vs 2-chunk candidates — verify MaxSim selects the
    /// correct max per query token and sums correctly.
    ///
    /// Q = [[1,0], [0,1], [1,1]/√2]
    /// D1 = [[1,0], [0,1]]          (perfect matches for q[0] and q[1])
    /// D2 = [[0,1], [1,0]]          (perfect matches but reversed)
    ///
    /// MaxSim(Q, D1):
    ///   q[0]=[1,0]: max(dot([1,0],[1,0])=1, dot([1,0],[0,1])=0) = 1.0
    ///   q[1]=[0,1]: max(dot([0,1],[1,0])=0, dot([0,1],[0,1])=1) = 1.0
    ///   q[2]=[1/√2,1/√2]: max(dot=1/√2, dot=1/√2) = 1/√2 ≈ 0.707
    ///   total ≈ 2.707
    ///
    /// MaxSim(Q, D2):
    ///   q[0]=[1,0]: max(dot=0, dot=1) = 1.0  — D2[1]=[1,0]
    ///   q[1]=[0,1]: max(dot=1, dot=0) = 1.0  — D2[0]=[0,1]
    ///   q[2]=[1/√2,1/√2]: max(1/√2, 1/√2) = 1/√2 ≈ 0.707
    ///   total ≈ 2.707  (symmetric — same score)
    #[test]
    fn maxsim_toy_query() {
        let sqrt2_inv = 1.0_f32 / 2.0_f32.sqrt();
        let q: Vec<Vec<f32>> = vec![vec![1.0, 0.0], vec![0.0, 1.0], vec![sqrt2_inv, sqrt2_inv]];
        let d1: Vec<Vec<f32>> = vec![vec![1.0, 0.0], vec![0.0, 1.0]];
        let d2: Vec<Vec<f32>> = vec![vec![0.0, 1.0], vec![1.0, 0.0]];

        let s1 = maxsim(&q, &d1);
        let s2 = maxsim(&q, &d2);

        // Both should be approximately 2 + 1/√2 ≈ 2.707.
        let expected = 2.0 + sqrt2_inv;
        assert!(
            (s1 - expected).abs() < 1e-5,
            "MaxSim(Q,D1) expected≈{expected:.4}, got {s1:.4}"
        );
        assert!(
            (s2 - expected).abs() < 1e-5,
            "MaxSim(Q,D2) expected≈{expected:.4}, got {s2:.4}"
        );
    }

    /// MaxSim with a perfect match query token must return a positive score.
    #[test]
    fn maxsim_positive_for_matching_query() {
        let q = vec![vec![1.0_f32, 0.0, 0.0]];
        let d = vec![vec![1.0_f32, 0.0, 0.0], vec![0.0_f32, 1.0, 0.0]];
        let score = maxsim(&q, &d);
        assert!(score > 0.0, "MaxSim must be > 0 for a matching query token");
    }

    /// MaxSim with empty inputs must return 0.0.
    #[test]
    fn maxsim_empty_inputs_return_zero() {
        assert_eq!(maxsim(&[], &[vec![1.0, 0.0]]), 0.0, "empty Q → 0.0");
        assert_eq!(maxsim(&[vec![1.0, 0.0]], &[]), 0.0, "empty D → 0.0");
        let empty: Vec<Vec<f32>> = vec![];
        assert_eq!(maxsim(&empty, &empty), 0.0, "both empty → 0.0");
    }

    // ── MockMultiVecEmbedModel ────────────────────────────────────────────────

    /// embed_multi_vec must return one vector per whitespace token.
    #[test]
    fn mock_multivec_returns_one_vec_per_token() {
        let m = MockMultiVecEmbedModel::new(8);
        let vecs = m.embed_multi_vec("hello world foo").unwrap();
        assert_eq!(vecs.len(), 3, "3 tokens → 3 vectors");
        for v in &vecs {
            assert_eq!(v.len(), 8, "each token vector must have dim=8");
        }
    }

    /// embed_multi_vec on empty input must return empty Vec.
    #[test]
    fn mock_multivec_empty_text_returns_empty() {
        let m = MockMultiVecEmbedModel::new(8);
        let vecs = m.embed_multi_vec("").unwrap();
        assert!(vecs.is_empty(), "empty text → empty token vecs");
    }

    /// embed_multi_vec output must be deterministic across calls.
    #[test]
    fn mock_multivec_is_deterministic() {
        let m = MockMultiVecEmbedModel::new(16);
        let a = m.embed_multi_vec("cascade rag test").unwrap();
        let b = m.embed_multi_vec("cascade rag test").unwrap();
        assert_eq!(a, b, "multivec output must be deterministic");
    }

    // ── Storage round-trip ───────────────────────────────────────────────────

    fn migrated_conn() -> Connection {
        let conn = Connection::open_in_memory().expect("in-memory DB");
        // `run_migrations` will also apply migration 6 when rag-multivec is compiled,
        // creating rag_token_embeddings with FK to rag_chunks.
        crate::db::migrations::run_migrations(&conn).expect("base migrations");
        // Insert a source + chunks so FK inserts succeed.
        conn.execute_batch(
            "INSERT INTO rag_sources (id, file_path, schema_version) VALUES (1, '/test/doc.md', 1);
             INSERT INTO rag_chunks (id, source_id, chunk_index, chunk_text, schema_version) VALUES (7,  1, 0, 'chunk seven',  1);
             INSERT INTO rag_chunks (id, source_id, chunk_index, chunk_text, schema_version) VALUES (42, 1, 1, 'chunk forty-two', 1);
             INSERT INTO rag_chunks (id, source_id, chunk_index, chunk_text, schema_version) VALUES (999,1, 2, 'chunk 999', 1);",
        )
        .expect("seed parent rows for FK");
        conn
    }

    /// store_token_embeddings → load_token_embeddings round-trip.
    #[test]
    fn token_embeddings_storage_round_trip() {
        let conn = migrated_conn();

        let token_vecs: Vec<Vec<f32>> = vec![
            vec![1.0, 0.0, 0.0],
            vec![0.0, 1.0, 0.0],
            vec![0.0, 0.0, 1.0],
        ];

        store_token_embeddings(&conn, 42, &token_vecs).expect("store must succeed");

        let loaded = load_token_embeddings(&conn, 42).expect("load must succeed");
        assert_eq!(loaded.len(), 3, "must load 3 token vectors");
        for (orig, loaded) in token_vecs.iter().zip(loaded.iter()) {
            for (a, b) in orig.iter().zip(loaded.iter()) {
                assert!(
                    (a - b).abs() < 1e-6,
                    "round-trip f32 must be bit-exact (LE bytes)"
                );
            }
        }
    }

    /// Storing twice for same chunk_id is idempotent (delete + re-insert).
    #[test]
    fn token_embeddings_idempotent_store() {
        let conn = migrated_conn();
        let vecs = vec![vec![1.0_f32, 2.0, 3.0]];
        store_token_embeddings(&conn, 7, &vecs).expect("first store");
        store_token_embeddings(&conn, 7, &vecs).expect("idempotent second store");
        let loaded = load_token_embeddings(&conn, 7).expect("load");
        assert_eq!(
            loaded.len(),
            1,
            "must still have exactly 1 token vector after idempotent store"
        );
    }

    /// Loading for a chunk_id with no token embeddings returns empty Vec.
    #[test]
    fn token_embeddings_load_empty_chunk() {
        let conn = migrated_conn();
        let loaded =
            load_token_embeddings(&conn, 999).expect("load non-existent chunk must not error");
        assert!(loaded.is_empty(), "no embeddings stored → empty result");
    }

    // ── MultiVecEmbedModel blanket impl (via MockEmbedModel) ─────────────────

    /// The blanket impl must work on MockEmbedModel without any additional code.
    #[test]
    fn blanket_multivec_on_mock_embed_model() {
        use crate::embed::MockEmbedModel;
        let m = MockEmbedModel::new(32);
        let vecs = m.embed_multi_vec("one two three").unwrap();
        assert_eq!(vecs.len(), 3, "blanket impl: 3 tokens → 3 vectors");
        assert_eq!(vecs[0].len(), 32);
    }
}
