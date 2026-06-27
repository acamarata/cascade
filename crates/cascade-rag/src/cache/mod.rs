//! Cache layer: query LRU+TTL, embedding persistent disk, chunk memory.
//!
//! Three independent cache types:
//!
//! | Cache | Key | Value | Backing | Eviction |
//! |-------|-----|-------|---------|---------|
//! | [`QueryCache`] | (query, top_k) | `Vec<SearchHit>` | RAM LRU (`lru` crate) | LRU + TTL |
//! | [`EmbedCache`] | sha256(text + model) | `Vec<f32>` | Disk (sled, T-P4-E04-09) | None (persistent) |
//! | [`ChunkCache`] | chunk_id | `Chunk` | RAM LRU | LRU (default 500 entries) |
//!
//! ## Invalidation
//!
//! - [`QueryCache::clear`] — full invalidation on any index mutation (upsert/delete).
//!   Called by [`crate::index::CachedIndex`]. WHY full clear: key space is unbounded;
//!   O(1) and safe (T-P4-E04-08).
//! - [`ChunkCache::invalidate_path`] — evict entries whose source path matches a changed file.
//!
//! ## Config (from [`crate::index::CachedIndex`])
//!
//! - `RagConfig.query_cache_capacity` — default 512 entries.
//! - `RagConfig.query_cache_ttl_secs` — default 60 s.
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::cache

pub mod embed_cache;
pub mod query_cache;

pub use embed_cache::{CachedEmbedModel, EmbedCache, EmbedCacheError, InMemoryEmbedCache};
pub use query_cache::QueryCache;

use std::collections::HashMap;
use std::path::Path;
use std::sync::Mutex;

use cascade_types::{chunker::Chunk, retriever::RetrievalHit};

// ── ChunkCache ────────────────────────────────────────────────────────────────

/// In-memory LRU cache for hot-path chunks.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::cache::ChunkCache
pub struct ChunkCache {
    inner: Mutex<LruMap<String, Chunk>>,
}

impl ChunkCache {
    /// Construct with a maximum entry count.
    pub fn new(max_entries: usize) -> Self {
        Self {
            inner: Mutex::new(LruMap::new(max_entries)),
        }
    }

    /// Retrieve a chunk by ID.
    pub fn get(&self, chunk_id: &str) -> Option<Chunk> {
        // WHY to_owned: LruMap<String,_>::get requires &String (the key type);
        // &str cannot be used directly without a Borrow impl on the handrolled LruMap.
        self.inner.lock().ok()?.get(&chunk_id.to_owned()).cloned()
    }

    /// Cache a chunk.
    pub fn insert(&self, chunk: Chunk) {
        let id = chunk.id.clone();
        if let Ok(mut inner) = self.inner.lock() {
            inner.insert(id, chunk);
        }
    }

    /// Evict all chunks whose source path matches `path`.
    pub fn invalidate_path(&self, path: &Path) {
        let path_str = path.to_string_lossy();
        if let Ok(mut inner) = self.inner.lock() {
            inner.retain(|_k, v| {
                v.metadata
                    .source_path
                    .as_ref()
                    .map(|p| p.to_string_lossy() != path_str)
                    .unwrap_or(true)
            });
        }
    }

    /// Return the number of cached entries.
    ///
    /// Used by `cache.stats` IPC handler (T-P4-E04-11).
    pub fn len(&self) -> usize {
        self.inner.lock().map(|g| g.entries.len()).unwrap_or(0)
    }

    /// Return `true` if no chunks are cached.
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }

    /// Evict all cached chunks.
    ///
    /// Used by `cascade cache clear --chunk` via the daemon IPC (T-P4-E04-11).
    pub fn clear(&self) {
        if let Ok(mut g) = self.inner.lock() {
            g.entries.clear();
        }
    }
}

impl Default for ChunkCache {
    fn default() -> Self {
        Self::new(500)
    }
}

// ── QueryCache (legacy re-export for old callers) ─────────────────────────────
//
// The old QueryCache (keyed by query+strategy+k+project → Vec<RetrievalHit>) is
// kept here for existing callers until migration is complete.
// The NEW QueryCache (keyed by query+top_k → Vec<SearchHit>) is in query_cache.rs
// and re-exported as `QueryCache` above.

/// Legacy query cache keyed by (query, strategy, k, project) → Vec<RetrievalHit>.
///
/// Used by callers that have not yet migrated to the new CachedIndex API.
/// New code should use [`QueryCache`] + [`crate::index::CachedIndex`] instead.
pub struct LegacyQueryCache {
    inner: Mutex<LruMap<LegacyQueryKey, Vec<RetrievalHit>>>,
}

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct LegacyQueryKey {
    query: String,
    strategy: String,
    k: usize,
    project: String,
}

impl LegacyQueryCache {
    /// Construct a new legacy query cache with a maximum entry count.
    pub fn new(max_entries: usize) -> Self {
        Self {
            inner: Mutex::new(LruMap::new(max_entries)),
        }
    }

    /// Look up a cached result.
    pub fn get(
        &self,
        query: &str,
        strategy: &str,
        k: usize,
        project: &str,
    ) -> Option<Vec<RetrievalHit>> {
        let key = LegacyQueryKey {
            query: query.to_string(),
            strategy: strategy.to_string(),
            k,
            project: project.to_string(),
        };
        self.inner.lock().ok()?.get(&key).cloned()
    }

    /// Insert a result set.
    pub fn insert(
        &self,
        query: &str,
        strategy: &str,
        k: usize,
        project: &str,
        hits: Vec<RetrievalHit>,
    ) {
        let key = LegacyQueryKey {
            query: query.to_string(),
            strategy: strategy.to_string(),
            k,
            project: project.to_string(),
        };
        if let Ok(mut inner) = self.inner.lock() {
            inner.insert(key, hits);
        }
    }

    /// Evict all entries whose results include chunks from `path`.
    pub fn invalidate_path(&self, path: &Path) {
        let path_str = path.to_string_lossy();
        if let Ok(mut inner) = self.inner.lock() {
            inner.retain(|_k, v| {
                !v.iter().any(|hit| {
                    hit.file_path
                        .as_ref()
                        .map(|p| p.to_string_lossy() == path_str)
                        .unwrap_or(false)
                })
            });
        }
    }
}

impl Default for LegacyQueryCache {
    fn default() -> Self {
        Self::new(1000)
    }
}

// ── Minimal LRU map ───────────────────────────────────────────────────────────
//
// A simple capacity-bounded HashMap that evicts the oldest entry on overflow.
// Used by ChunkCache and LegacyQueryCache (which don't need the `lru` crate).
// QueryCache (new) uses the `lru` crate directly for O(1) eviction.

struct LruMap<K, V> {
    max: usize,
    entries: HashMap<K, (u64, V)>,
    counter: u64,
}

impl<K: Eq + std::hash::Hash + Clone, V: Clone> LruMap<K, V> {
    fn new(max: usize) -> Self {
        Self {
            max: max.max(1),
            entries: HashMap::new(),
            counter: 0,
        }
    }

    fn get(&mut self, key: &K) -> Option<&V> {
        if let Some(entry) = self.entries.get_mut(key) {
            self.counter += 1;
            entry.0 = self.counter;
            Some(&entry.1)
        } else {
            None
        }
    }

    fn insert(&mut self, key: K, value: V) {
        self.counter += 1;
        self.entries.insert(key, (self.counter, value));
        if self.entries.len() > self.max {
            // Evict the entry with the smallest access counter (oldest).
            let oldest = self
                .entries
                .iter()
                .min_by_key(|(_, (ts, _))| *ts)
                .map(|(k, _)| k.clone());
            if let Some(k) = oldest {
                self.entries.remove(&k);
            }
        }
    }

    fn retain<F: Fn(&K, &mut V) -> bool>(&mut self, f: F) {
        self.entries.retain(|k, (_, v)| f(k, v));
    }
}
