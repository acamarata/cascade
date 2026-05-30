//! Cache layer: query LRU, embedding persistent disk, chunk memory.
//!
//! Three independent cache types:
//!
//! | Cache | Key | Value | Backing | Eviction |
//! |-------|-----|-------|---------|---------|
//! | [`QueryCache`] | (query, strategy, K, project) | `Vec<RetrievalHit>` | RAM LRU | LRU (default 1000 entries) |
//! | [`EmbedCache`] | sha256(text + model) | `Vec<f32>` | Disk (sled) | None (persistent) |
//! | [`ChunkCache`] | chunk_id | `Chunk` | RAM LRU | LRU (default 500 entries) |
//!
//! ## Invalidation (S19-04)
//!
//! File-watcher events call [`QueryCache::invalidate_path`] and
//! [`ChunkCache::invalidate_path`] to evict entries whose source path matches
//! the changed file.
//!
//! ## Config (S19-05)
//!
//! ```toml
//! [rag.cache]
//! query_max_entries = 1000
//! chunk_max_entries = 500
//! embed_cache_dir = "~/.cascade/embed-cache"
//! ```
//!
//! SPORT: MASTER-LIBS.md → cascade-rag::cache

use std::collections::HashMap;
use std::path::Path;
use std::sync::Mutex;

use cascade_types::{chunker::Chunk, retriever::RetrievalHit};

// ── QueryCache ────────────────────────────────────────────────────────────────

/// LRU cache for search query results.
///
/// Key: `(query_text, strategy_name, k, project_id)`.
/// Thread-safe via internal `Mutex`.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::cache::QueryCache
pub struct QueryCache {
    inner: Mutex<LruMap<QueryKey, Vec<RetrievalHit>>>,
}

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct QueryKey {
    query: String,
    strategy: String,
    k: usize,
    project: String,
}

impl QueryCache {
    /// Construct a new query cache with a maximum entry count.
    pub fn new(max_entries: usize) -> Self {
        Self {
            inner: Mutex::new(LruMap::new(max_entries)),
        }
    }

    /// Look up a cached result.
    pub fn get(&self, query: &str, strategy: &str, k: usize, project: &str) -> Option<Vec<RetrievalHit>> {
        let key = QueryKey {
            query: query.to_string(),
            strategy: strategy.to_string(),
            k,
            project: project.to_string(),
        };
        self.inner.lock().ok()?.get(&key).cloned()
    }

    /// Insert a result set.
    pub fn insert(&self, query: &str, strategy: &str, k: usize, project: &str, hits: Vec<RetrievalHit>) {
        let key = QueryKey {
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

impl Default for QueryCache {
    fn default() -> Self {
        Self::new(1000)
    }
}

// ── EmbedCache ────────────────────────────────────────────────────────────────

/// Persistent disk cache for embedding vectors.
///
/// Keys are `sha256(text + model_name)` (hex-encoded).
/// Values are raw `f32` vectors serialised as little-endian byte arrays.
///
/// Backed by sled (embedded key-value store); the database is stored at
/// `~/.cascade/embed-cache/`.
///
/// SPORT: MASTER-LIBS.md → cascade-rag::cache::EmbedCache
pub struct EmbedCache {
    // sled::Db will be wired in T-P1-E7-S04-03.
    // For this scaffold we use a simple in-memory HashMap.
    inner: Mutex<HashMap<String, Vec<f32>>>,
}

impl EmbedCache {
    /// Open (or create) the embedding cache at `cache_dir`.
    pub fn open(_cache_dir: &Path) -> cascade_types::error::Result<Self> {
        // TODO(T-P1-E7-S04-03): open sled database at cache_dir.
        Ok(Self {
            inner: Mutex::new(HashMap::new()),
        })
    }

    /// Look up an embedding by its cache key.
    pub fn get(&self, key: &str) -> Option<Vec<f32>> {
        self.inner.lock().ok()?.get(key).cloned()
    }

    /// Store an embedding.
    pub fn insert(&self, key: impl Into<String>, embedding: Vec<f32>) {
        if let Ok(mut inner) = self.inner.lock() {
            inner.insert(key.into(), embedding);
        }
    }

    /// Compute the cache key for a text + model combination.
    pub fn cache_key(text: &str, model: &str) -> String {
        use std::collections::hash_map::DefaultHasher;
        use std::hash::{Hash, Hasher};
        let mut h = DefaultHasher::new();
        text.hash(&mut h);
        model.hash(&mut h);
        format!("{:016x}", h.finish())
    }
}

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
        self.inner.lock().ok()?.get(chunk_id).cloned()
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
}

impl Default for ChunkCache {
    fn default() -> Self {
        Self::new(500)
    }
}

// ── Minimal LRU map ───────────────────────────────────────────────────────────
//
// A simple capacity-bounded HashMap that evicts the oldest entry on overflow.
// A production implementation would use the `lru` crate for O(1) eviction;
// this scaffold uses O(n) linear scan to avoid adding a dependency.

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
