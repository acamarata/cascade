//! Persistent SQLite embedding cache.
//!
//! Stores `(content_hash: blake3_hex) → Vec<f32>` in a dedicated
//! `cascade_embed_cache.db` SQLite database adjacent to the project's
//! RAG index directory.  Before calling BGE-M3 ONNX inference, callers
//! check this cache; a hit skips inference entirely (10-50 ms → ~0.1 ms).
//!
//! ## Schema
//!
//! ```sql
//! CREATE TABLE embed_cache_meta (
//!     key   TEXT PRIMARY KEY,
//!     value TEXT NOT NULL
//! );
//! -- Stores "model_id" and "model_version" keys.
//! -- On model mismatch the embeddings table is dropped and recreated.
//!
//! CREATE TABLE embeddings (
//!     content_hash TEXT PRIMARY KEY,
//!     vec          BLOB NOT NULL
//! );
//! -- vec: f32 array serialised as little-endian bytes (4 bytes per element).
//! ```
//!
//! ## Size characteristics
//!
//! Each BGE-M3 embedding is 1024 × f32 = 4 096 bytes.  10 000 chunks ≈ 40 MB.
//! The cache grows unboundedly (append-only, no eviction) because embeddings
//! are deterministic for a fixed model.  Use `cascade cache clear embed` to
//! reset (T-P4-E04-11 scope).
//!
//! ## Model invalidation
//!
//! The `embed_cache_meta` table stores `(model_id, model_version)`.  When
//! `EmbedCache::open` detects a mismatch it drops and recreates the
//! `embeddings` table, invalidating all stale entries atomically.
//!
//! ## Thread safety
//!
//! `EmbedCache` wraps the `rusqlite::Connection` in a `Mutex`; all public
//! methods are `&self` and safe to share via `Arc<EmbedCache>`.
//!
//! ## Corruption tolerance
//!
//! `get` returns `None` on any row-level deserialization error; the caller
//! falls through to inference and `set` overwrites the corrupt row.
//!
//! SPORT: MASTER-COMPONENTS.md → EmbedCache

use std::path::Path;
use std::sync::Mutex;

use rusqlite::{params, Connection, OptionalExtension};

// ── Error type ────────────────────────────────────────────────────────────────

/// Errors produced by [`EmbedCache`] operations.
///
/// Cache misses are not errors — they are represented by `None` return values.
#[derive(Debug, thiserror::Error)]
pub enum EmbedCacheError {
    /// SQLite operation failed.
    #[error("embed cache db error: {0}")]
    Db(#[from] rusqlite::Error),

    /// The database file exists but could not be opened (permissions, corruption).
    #[error("embed cache open failed at {path}: {source}")]
    Open {
        path: std::path::PathBuf,
        #[source]
        source: rusqlite::Error,
    },
}

// ── EmbedCache ────────────────────────────────────────────────────────────────

/// Persistent disk-backed embedding cache.
///
/// # Purpose
///
/// Short-circuit ONNX inference for previously-seen text by storing
/// `content_hash → Vec<f32>` in a local SQLite database.  The cache is keyed
/// on the **blake3 hex hash of the chunk text** — identical text always maps
/// to the same hash regardless of file origin.
///
/// # Inputs (constructor)
///
/// - `index_root` — directory that owns `cascade_embed_cache.db`.
/// - `model_id` — stable string identifier for the embedding model (e.g.
///   `"bge-m3"`, `"nomic-embed-text-v1"`, `"mock-embed-model"`).
/// - `model_version` — version string for the model (e.g. `"1"`, `"0.3.0"`).
///   Changing this value invalidates all cached entries atomically.
///
/// # Outputs
///
/// - `get(hash)` → `Option<Vec<f32>>` — `Some` on cache hit, `None` on miss
///   or corruption.
/// - `set(hash, vec)` — stores the embedding; silently no-ops on DB error to
///   avoid interrupting the ingest pipeline.
///
/// # Constraints
///
/// - Not `Clone` (wraps a `Mutex<Connection>`).
/// - `Send + Sync` — safe to share via `Arc<EmbedCache>`.
/// - The `embeddings` table uses `INSERT OR REPLACE` so concurrent writers
///   converge to the same value (embeddings are deterministic).
///
/// SPORT: MASTER-COMPONENTS.md → EmbedCache
pub struct EmbedCache {
    inner: Mutex<Connection>,
    /// `embed_cache_enabled` flag: if `false`, all operations are no-ops.
    enabled: bool,
}

impl std::fmt::Debug for EmbedCache {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("EmbedCache")
            .field("enabled", &self.enabled)
            .finish_non_exhaustive()
    }
}

impl EmbedCache {
    // ── Constructor ───────────────────────────────────────────────────────────

    /// Open or create the embedding cache database.
    ///
    /// # Algorithm
    ///
    /// 1. Open (or create) `{index_root}/cascade_embed_cache.db`.
    /// 2. Run schema migration (`embed_cache_meta` + `embeddings` tables).
    /// 3. Compare stored `(model_id, model_version)` against the provided values.
    /// 4. On mismatch: `DROP TABLE IF EXISTS embeddings` + recreate — atomically
    ///    invalidates all stale entries.
    /// 5. Update `embed_cache_meta` with the current model identifiers.
    ///
    /// # Errors
    ///
    /// Returns `EmbedCacheError::Open` if the database cannot be opened or the
    /// schema migration fails.
    pub fn open(
        index_root: &Path,
        model_id: &str,
        model_version: &str,
        enabled: bool,
    ) -> Result<Self, EmbedCacheError> {
        let db_path = index_root.join("cascade_embed_cache.db");

        let conn = Connection::open(&db_path).map_err(|e| EmbedCacheError::Open {
            path: db_path.clone(),
            source: e,
        })?;

        // WAL mode for concurrent readers.
        conn.execute_batch("PRAGMA journal_mode = WAL; PRAGMA synchronous = NORMAL;")?;

        // Create metadata table.
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS embed_cache_meta (
                key   TEXT PRIMARY KEY,
                value TEXT NOT NULL
            );",
        )?;

        // Read stored model identity.
        let stored_model_id: Option<String> = conn
            .query_row(
                "SELECT value FROM embed_cache_meta WHERE key = 'model_id'",
                [],
                |r| r.get(0),
            )
            .optional()?;

        let stored_model_version: Option<String> = conn
            .query_row(
                "SELECT value FROM embed_cache_meta WHERE key = 'model_version'",
                [],
                |r| r.get(0),
            )
            .optional()?;

        // Invalidate on model mismatch.
        let needs_invalidation = stored_model_id.as_deref() != Some(model_id)
            || stored_model_version.as_deref() != Some(model_version);

        if needs_invalidation {
            conn.execute_batch("DROP TABLE IF EXISTS embeddings;")?;
        }

        // Create (or recreate) embeddings table.
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS embeddings (
                content_hash TEXT PRIMARY KEY,
                vec          BLOB NOT NULL
            );",
        )?;

        // Persist current model identity.
        conn.execute(
            "INSERT OR REPLACE INTO embed_cache_meta (key, value) VALUES ('model_id', ?1)",
            params![model_id],
        )?;
        conn.execute(
            "INSERT OR REPLACE INTO embed_cache_meta (key, value) VALUES ('model_version', ?1)",
            params![model_version],
        )?;

        Ok(Self {
            inner: Mutex::new(conn),
            enabled,
        })
    }

    /// Create a disabled no-op cache (useful when `embed_cache_enabled = false`).
    ///
    /// All operations on this instance are no-ops; no SQLite file is opened.
    pub fn disabled() -> Self {
        // We need a Connection to satisfy the field type.  Use an in-memory DB
        // for the disabled sentinel so no disk I/O ever occurs.
        let conn = Connection::open_in_memory().expect("in-memory SQLite must always succeed");
        Self {
            inner: Mutex::new(conn),
            enabled: false,
        }
    }

    // ── Public API ─────────────────────────────────────────────────────────────

    /// Look up a cached embedding by its blake3 hex content hash.
    ///
    /// Returns `None` on cache miss, key absence, or row-level corruption
    /// (bad BLOB length that does not map to a whole number of `f32`s).
    /// Corruption is silent: callers should fall through to ONNX inference and
    /// call [`set`] to overwrite the corrupt row.
    pub fn get(&self, content_hash: &str) -> Option<Vec<f32>> {
        if !self.enabled {
            return None;
        }

        let conn = self.inner.lock().ok()?;
        let blob: Vec<u8> = conn
            .query_row(
                "SELECT vec FROM embeddings WHERE content_hash = ?1",
                params![content_hash],
                |r| r.get(0),
            )
            .optional()
            .ok()??;

        deserialize_f32_le(&blob)
    }

    /// Store an embedding under its blake3 hex content hash.
    ///
    /// Uses `INSERT OR REPLACE` so corrupt or duplicate rows are overwritten.
    /// Silently swallows DB errors to avoid aborting the ingest pipeline.
    ///
    /// # Errors
    ///
    /// Returns `Err(EmbedCacheError::Db)` only when the caller needs to
    /// know.  Most callers should use [`set_silent`] instead.
    pub fn set(&self, content_hash: &str, vec: &[f32]) -> Result<(), EmbedCacheError> {
        if !self.enabled {
            return Ok(());
        }

        let blob = serialize_f32_le(vec);
        let conn = self
            .inner
            .lock()
            .map_err(|_| EmbedCacheError::Db(rusqlite::Error::SqliteSingleThreadedMode))?;
        conn.execute(
            "INSERT OR REPLACE INTO embeddings (content_hash, vec) VALUES (?1, ?2)",
            params![content_hash, blob],
        )?;
        Ok(())
    }

    /// Like [`set`] but silently ignores errors.
    ///
    /// This is the intended call site inside the ingest pipeline where a cache
    /// write failure must not abort the embedding workflow.
    pub fn set_silent(&self, content_hash: &str, vec: &[f32]) {
        let _ = self.set(content_hash, vec);
    }

    /// Return the number of cached entries (for diagnostics / tests).
    ///
    /// Returns `0` on lock or DB error.
    pub fn count(&self) -> usize {
        if !self.enabled {
            return 0;
        }
        let Ok(conn) = self.inner.lock() else {
            return 0;
        };
        conn.query_row("SELECT COUNT(*) FROM embeddings", [], |r| {
            r.get::<_, i64>(0)
        })
        .unwrap_or(0) as usize
    }

    /// Batch lookup: returns a `Vec<Option<Vec<f32>>>` parallel to `hashes`.
    ///
    /// A `None` entry means cache miss or corruption for that hash; the caller
    /// should embed those indices and call `set_batch`.
    ///
    /// Uses a single DB connection lock for the whole batch.
    pub fn get_batch(&self, hashes: &[&str]) -> Vec<Option<Vec<f32>>> {
        if !self.enabled || hashes.is_empty() {
            return vec![None; hashes.len()];
        }

        let Ok(conn) = self.inner.lock() else {
            return vec![None; hashes.len()];
        };

        hashes
            .iter()
            .map(|hash| {
                let blob: Option<Vec<u8>> = conn
                    .query_row(
                        "SELECT vec FROM embeddings WHERE content_hash = ?1",
                        params![hash],
                        |r| r.get(0),
                    )
                    .optional()
                    .ok()
                    .flatten();
                blob.and_then(|b| deserialize_f32_le(&b))
            })
            .collect()
    }

    /// Whether the cache is active (not disabled).
    pub fn is_enabled(&self) -> bool {
        self.enabled
    }

    /// Delete all cached embeddings from the in-memory SQLite database.
    ///
    /// Purpose: `cascade cache clear --embed` calls this via the daemon IPC
    ///   (T-P4-E04-11) to evict in-memory embeddings. The CLI is responsible
    ///   for deleting the on-disk `cascade_embed_cache.db` file separately
    ///   before sending the IPC message, so this method targets the in-memory
    ///   state only.
    ///
    /// If the cache is disabled, this is a no-op.
    pub fn clear(&self) {
        if !self.enabled {
            return;
        }
        if let Ok(conn) = self.inner.lock() {
            let _ = conn.execute("DELETE FROM embeddings", []);
        }
    }
}

// ── Serialization helpers ─────────────────────────────────────────────────────

/// Serialize a `&[f32]` to a little-endian byte vector.
///
/// # Platform independence
///
/// Always writes in little-endian byte order regardless of the host CPU
/// endianness.  SQLite BLOBs must be portable across machines.
fn serialize_f32_le(vec: &[f32]) -> Vec<u8> {
    let mut out = Vec::with_capacity(vec.len() * 4);
    for &v in vec {
        out.extend_from_slice(&v.to_le_bytes());
    }
    out
}

/// Deserialize a little-endian byte slice to `Vec<f32>`.
///
/// Returns `None` if the byte length is not a multiple of 4 (corruption guard).
fn deserialize_f32_le(bytes: &[u8]) -> Option<Vec<f32>> {
    if bytes.len() % 4 != 0 {
        // Corrupt row — caller should recompute.
        return None;
    }
    Some(
        bytes
            .chunks_exact(4)
            .map(|chunk| {
                let arr: [u8; 4] = chunk.try_into().expect("chunks_exact guarantees 4 bytes");
                f32::from_le_bytes(arr)
            })
            .collect(),
    )
}

// ── Decorator: CachedEmbedModel ───────────────────────────────────────────────

use std::sync::Arc;

use crate::embed::{EmbedError, EmbedModel};

/// Decorator that wraps any [`EmbedModel`] with [`EmbedCache`] lookup.
///
/// # Purpose
///
/// Transparent cache layer over an inner [`EmbedModel`] implementation.
/// For each text in a `embed_dense` batch:
/// - Compute its blake3 content hash.
/// - If a hit exists in `cache`, return the cached vector (no ONNX call).
/// - On a miss, embed via the inner model and store the result in the cache.
///
/// Only `embed_dense` is cached (sparse vectors are cheap TF-IDF; caching
/// them would add complexity with minimal ROI).
///
/// # Inputs / Outputs
///
/// Same as the wrapped [`EmbedModel`] — `embed_dense` and `embed_sparse`
/// behave identically to the inner model, except that `embed_dense` may
/// short-circuit via the cache.
///
/// # Constraints
///
/// - `cache` must be `Arc<EmbedCache>` so the decorator can be cloned or
///   shared across threads.
/// - `inner` must be `Arc<dyn EmbedModel>` so it satisfies `Send + Sync`.
///
/// SPORT: MASTER-COMPONENTS.md → CachedEmbedModel
pub struct CachedEmbedModel {
    inner: Arc<dyn EmbedModel>,
    cache: Arc<EmbedCache>,
}

impl std::fmt::Debug for CachedEmbedModel {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("CachedEmbedModel")
            .field("model_id", &self.inner.model_id())
            .field("cache_enabled", &self.cache.is_enabled())
            .finish_non_exhaustive()
    }
}

impl CachedEmbedModel {
    /// Wrap `inner` with `cache`.
    pub fn new(inner: Arc<dyn EmbedModel>, cache: Arc<EmbedCache>) -> Self {
        Self { inner, cache }
    }

    /// Compute the blake3 hex content hash for a text slice.
    ///
    /// The hash is used as the cache key.  It is derived from the text only
    /// (not the model id) — the model-version invalidation in [`EmbedCache`]
    /// handles cross-model contamination at the DB level.
    fn content_hash(text: &str) -> String {
        let hash = blake3::hash(text.as_bytes());
        hash.to_hex().to_string()
    }
}

impl EmbedModel for CachedEmbedModel {
    /// Embed with cache: hit → return cached vec; miss → embed + cache.
    ///
    /// For batches with mixed hits/misses:
    /// 1. Compute hashes for all texts.
    /// 2. Batch-check the cache.
    /// 3. Collect miss indices; embed only those via the inner model.
    /// 4. Write misses back to cache.
    /// 5. Reassemble the full result vec in original order.
    fn embed_dense(&self, texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
        if texts.is_empty() {
            return Ok(vec![]);
        }

        // 1. Hashes for all texts.
        let hashes: Vec<String> = texts.iter().map(|t| Self::content_hash(t)).collect();
        let hash_refs: Vec<&str> = hashes.iter().map(String::as_str).collect();

        // 2. Batch cache lookup.
        let cache_results = self.cache.get_batch(&hash_refs);

        // 3. Identify misses.
        let miss_indices: Vec<usize> = cache_results
            .iter()
            .enumerate()
            .filter_map(|(i, r)| if r.is_none() { Some(i) } else { None })
            .collect();

        // 4. Embed misses via inner model.
        let miss_vecs: Vec<Vec<f32>> = if miss_indices.is_empty() {
            vec![]
        } else {
            let miss_texts: Vec<&str> = miss_indices.iter().map(|&i| texts[i]).collect();
            self.inner.embed_dense(&miss_texts)?
        };

        // 5. Write misses back to cache.
        for (miss_pos, &miss_idx) in miss_indices.iter().enumerate() {
            self.cache
                .set_silent(&hashes[miss_idx], &miss_vecs[miss_pos]);
        }

        // 6. Reassemble in original order.
        let mut miss_iter = miss_vecs.into_iter();
        let result = cache_results
            .into_iter()
            .map(|cached| {
                if let Some(v) = cached {
                    v
                } else {
                    miss_iter
                        .next()
                        .expect("miss_iter length matches miss count")
                }
            })
            .collect();

        Ok(result)
    }

    /// Sparse embedding is not cached — delegate directly to inner.
    fn embed_sparse(
        &self,
        texts: &[&str],
    ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
        self.inner.embed_sparse(texts)
    }

    fn dim(&self) -> usize {
        self.inner.dim()
    }

    fn model_id(&self) -> &str {
        self.inner.model_id()
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::embed::MockEmbedModel;
    use std::sync::{
        atomic::{AtomicUsize, Ordering},
        Arc,
    };
    use tempfile::TempDir;

    // ── Helper: open a fresh cache in a temp dir ──────────────────────────────

    fn temp_cache(model_id: &str, model_version: &str) -> (TempDir, EmbedCache) {
        let dir = TempDir::new().unwrap();
        let cache = EmbedCache::open(dir.path(), model_id, model_version, true).unwrap();
        (dir, cache)
    }

    // ── Serialization round-trip ──────────────────────────────────────────────

    #[test]
    fn embed_cache_f32_roundtrip() {
        let orig: Vec<f32> = vec![1.0, -0.5, 0.0, f32::MAX, f32::MIN_POSITIVE];
        let bytes = serialize_f32_le(&orig);
        let back = deserialize_f32_le(&bytes).unwrap();
        assert_eq!(orig, back, "f32 round-trip must be lossless");
    }

    #[test]
    fn embed_cache_f32_little_endian_explicit() {
        // 1.0_f32 in IEEE-754 little-endian = [0x00, 0x00, 0x80, 0x3f]
        let bytes = serialize_f32_le(&[1.0_f32]);
        assert_eq!(bytes, [0x00, 0x00, 0x80, 0x3f], "must be little-endian");
    }

    #[test]
    fn embed_cache_corrupt_blob_returns_none() {
        // 5 bytes is not a multiple of 4 — should return None, not panic.
        let corrupt: &[u8] = &[0x01, 0x02, 0x03, 0x04, 0x05];
        assert!(
            deserialize_f32_le(corrupt).is_none(),
            "corrupt blob must return None"
        );
    }

    // ── Basic get/set ─────────────────────────────────────────────────────────

    #[test]
    fn embed_cache_get_miss_returns_none() {
        let (_dir, cache) = temp_cache("test-model", "1");
        assert!(cache.get("nonexistent-hash").is_none());
    }

    #[test]
    fn embed_cache_set_then_get() {
        let (_dir, cache) = temp_cache("test-model", "1");
        let vec: Vec<f32> = (0..16).map(|i| i as f32 * 0.1).collect();
        cache.set("abc123", &vec).unwrap();
        let retrieved = cache.get("abc123").unwrap();
        assert_eq!(vec, retrieved, "stored vector must round-trip exactly");
    }

    #[test]
    fn embed_cache_count_matches_inserts() {
        let (_dir, cache) = temp_cache("test-model", "1");
        assert_eq!(cache.count(), 0);
        cache.set("h1", &[1.0, 2.0]).unwrap();
        cache.set("h2", &[3.0, 4.0]).unwrap();
        assert_eq!(cache.count(), 2);
    }

    // ── Model-version invalidation ────────────────────────────────────────────

    #[test]
    fn embed_cache_model_version_invalidation() {
        let dir = TempDir::new().unwrap();
        // Open with v1, populate.
        {
            let cache = EmbedCache::open(dir.path(), "bge-m3", "1", true).unwrap();
            cache.set("hash-a", &[1.0, 2.0, 3.0]).unwrap();
            assert_eq!(cache.count(), 1);
        }
        // Reopen with v2 — should invalidate all entries.
        {
            let cache = EmbedCache::open(dir.path(), "bge-m3", "2", true).unwrap();
            assert_eq!(
                cache.count(),
                0,
                "model version change must invalidate all cached entries"
            );
            assert!(
                cache.get("hash-a").is_none(),
                "stale entry must not be visible after invalidation"
            );
        }
    }

    #[test]
    fn embed_cache_model_id_change_invalidates() {
        let dir = TempDir::new().unwrap();
        {
            let cache = EmbedCache::open(dir.path(), "bge-m3", "1", true).unwrap();
            cache.set("h1", &[0.1, 0.2]).unwrap();
        }
        // Different model_id — must invalidate.
        {
            let cache = EmbedCache::open(dir.path(), "nomic-embed", "1", true).unwrap();
            assert_eq!(cache.count(), 0, "model_id change must invalidate cache");
        }
    }

    #[test]
    fn embed_cache_same_model_version_persists() {
        let dir = TempDir::new().unwrap();
        {
            let cache = EmbedCache::open(dir.path(), "bge-m3", "1", true).unwrap();
            cache.set("h1", &[0.5, 0.6]).unwrap();
        }
        // Same model_id + model_version — entries survive.
        {
            let cache = EmbedCache::open(dir.path(), "bge-m3", "1", true).unwrap();
            assert_eq!(cache.count(), 1, "same model version must keep entries");
            let v = cache.get("h1").unwrap();
            assert_eq!(v, vec![0.5, 0.6]);
        }
    }

    // ── Disabled cache ────────────────────────────────────────────────────────

    #[test]
    fn embed_cache_disabled_always_misses() {
        let cache = EmbedCache::disabled();
        assert!(!cache.is_enabled());
        assert!(cache.get("any-hash").is_none());
        assert_eq!(cache.count(), 0);
        // set_silent must not panic.
        cache.set_silent("any-hash", &[1.0, 2.0]);
    }

    // ── CachedEmbedModel: hit skips inner model calls ─────────────────────────

    /// Counting mock: counts calls to embed_dense.
    struct CountingMock {
        inner: MockEmbedModel,
        call_count: Arc<AtomicUsize>,
    }

    impl CountingMock {
        fn new(dim: usize, counter: Arc<AtomicUsize>) -> Self {
            Self {
                inner: MockEmbedModel::new(dim),
                call_count: counter,
            }
        }
    }

    impl EmbedModel for CountingMock {
        fn embed_dense(&self, texts: &[&str]) -> std::result::Result<Vec<Vec<f32>>, EmbedError> {
            self.call_count.fetch_add(texts.len(), Ordering::SeqCst);
            self.inner.embed_dense(texts)
        }

        fn embed_sparse(
            &self,
            texts: &[&str],
        ) -> std::result::Result<Vec<Vec<(u32, f32)>>, EmbedError> {
            self.inner.embed_sparse(texts)
        }

        fn dim(&self) -> usize {
            self.inner.dim()
        }

        fn model_id(&self) -> &str {
            "counting-mock"
        }
    }

    #[test]
    fn cached_embed_hit_skips_inference() {
        // Embed the same text twice; inner model must be called exactly once.
        let dir = TempDir::new().unwrap();
        let counter = Arc::new(AtomicUsize::new(0));
        let inner = Arc::new(CountingMock::new(16, Arc::clone(&counter)));
        let cache = Arc::new(EmbedCache::open(dir.path(), "counting-mock", "1", true).unwrap());
        let cached_model = CachedEmbedModel::new(inner, Arc::clone(&cache));

        let text = "the quick brown fox";
        let v1 = cached_model.embed_dense(&[text]).unwrap();
        let v2 = cached_model.embed_dense(&[text]).unwrap();

        assert_eq!(
            counter.load(Ordering::SeqCst),
            1,
            "inner embed_dense must be called exactly once for duplicate text"
        );
        assert_eq!(v1, v2, "cache hit must return identical vector");
    }

    #[test]
    fn cached_embed_partial_batch_hit() {
        // Batch of 3: first two already cached, third is a miss.
        let dir = TempDir::new().unwrap();
        let counter = Arc::new(AtomicUsize::new(0));
        let inner = Arc::new(CountingMock::new(8, Arc::clone(&counter)));
        let cache = Arc::new(EmbedCache::open(dir.path(), "counting-mock", "1", true).unwrap());
        let cached_model = CachedEmbedModel::new(
            Arc::clone(&inner) as Arc<dyn EmbedModel>,
            Arc::clone(&cache),
        );

        // Pre-populate two entries in the cache.
        let hit_texts = ["cached-text-one", "cached-text-two"];
        cached_model.embed_dense(&hit_texts).unwrap();
        let after_prepopulate = counter.load(Ordering::SeqCst);
        assert_eq!(after_prepopulate, 2, "prepopulate should call inner twice");

        // Now embed a batch with 2 hits + 1 miss.
        let batch = ["cached-text-one", "cached-text-two", "brand-new-text"];
        let results = cached_model.embed_dense(&batch).unwrap();

        // Inner should have been called for exactly 1 new text.
        assert_eq!(
            counter.load(Ordering::SeqCst),
            3,
            "partial-batch: only the miss should call inner"
        );
        // All 3 results must have the correct dimension.
        for v in &results {
            assert_eq!(v.len(), 8);
        }
    }

    #[test]
    fn cached_embed_model_version_invalidation_via_decorator() {
        let dir = TempDir::new().unwrap();
        let counter_v1 = Arc::new(AtomicUsize::new(0));
        let counter_v2 = Arc::new(AtomicUsize::new(0));

        // v1 session: embed one text.
        {
            let inner = Arc::new(CountingMock::new(8, Arc::clone(&counter_v1)));
            let cache = Arc::new(EmbedCache::open(dir.path(), "counting-mock", "1", true).unwrap());
            let m = CachedEmbedModel::new(inner, cache);
            m.embed_dense(&["same text"]).unwrap();
            assert_eq!(counter_v1.load(Ordering::SeqCst), 1);
        }

        // v2 session: same text must be re-embedded (cache was invalidated).
        {
            let inner = Arc::new(CountingMock::new(8, Arc::clone(&counter_v2)));
            let cache = Arc::new(EmbedCache::open(dir.path(), "counting-mock", "2", true).unwrap());
            let m = CachedEmbedModel::new(inner, cache);
            m.embed_dense(&["same text"]).unwrap();
            assert_eq!(
                counter_v2.load(Ordering::SeqCst),
                1,
                "model version change must invalidate cache: inner must be called again"
            );
        }
    }

    #[test]
    fn embed_cache_corrupt_row_triggers_recompute() {
        // Directly write a corrupt BLOB (5 bytes), then verify get() returns None
        // and set_silent overwrites it cleanly.
        let dir = TempDir::new().unwrap();
        let cache = EmbedCache::open(dir.path(), "test-model", "1", true).unwrap();

        // Manually insert corrupt blob via the internal connection.
        {
            let conn = cache.inner.lock().unwrap();
            conn.execute(
                "INSERT OR REPLACE INTO embeddings (content_hash, vec) VALUES (?1, ?2)",
                params!["corrupt-hash", vec![0xAA_u8, 0xBB, 0xCC, 0xDD, 0xEE]],
            )
            .unwrap();
        }

        // get() must return None (not panic) for the corrupt row.
        assert!(
            cache.get("corrupt-hash").is_none(),
            "corrupt row must return None, not panic"
        );

        // set_silent should overwrite the corrupt row with valid data.
        let valid = vec![1.0_f32, 2.0, 3.0, 4.0];
        cache.set_silent("corrupt-hash", &valid);
        assert_eq!(
            cache.get("corrupt-hash").unwrap(),
            valid,
            "overwrite must succeed"
        );
    }

    #[test]
    fn embed_cache_batch_lookup_all_miss() {
        let (_dir, cache) = temp_cache("test-model", "1");
        let hashes = ["h1", "h2", "h3"];
        let results = cache.get_batch(&hashes);
        assert!(
            results.iter().all(|r| r.is_none()),
            "all miss on empty cache"
        );
    }

    #[test]
    fn embed_cache_batch_lookup_mixed() {
        let (_dir, cache) = temp_cache("test-model", "1");
        cache.set("h1", &[1.0, 2.0]).unwrap();
        cache.set("h3", &[5.0, 6.0]).unwrap();

        let hashes = ["h1", "h2", "h3"];
        let results = cache.get_batch(&hashes);
        assert_eq!(results[0], Some(vec![1.0, 2.0]));
        assert_eq!(results[1], None);
        assert_eq!(results[2], Some(vec![5.0, 6.0]));
    }

    #[test]
    fn embed_cache_prune_by_reopening_with_new_version() {
        // Demonstrate the prune path: after version bump, old entries are gone.
        let dir = TempDir::new().unwrap();
        let cache_v1 = EmbedCache::open(dir.path(), "model", "v1", true).unwrap();
        for i in 0..10 {
            cache_v1.set(&format!("hash-{i}"), &[i as f32]).unwrap();
        }
        assert_eq!(cache_v1.count(), 10);
        drop(cache_v1);

        // Reopen with new version: prune fires.
        let cache_v2 = EmbedCache::open(dir.path(), "model", "v2", true).unwrap();
        assert_eq!(
            cache_v2.count(),
            0,
            "version bump must prune all old entries"
        );
    }
}
