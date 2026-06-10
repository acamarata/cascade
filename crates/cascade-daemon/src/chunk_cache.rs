//! In-memory mtime-validated chunk cache for cascade file reads.
//!
//! Purpose: avoid re-reading and re-chunking the same `.cascade/CASCADE.md`,
//!   `CLAUDE.md`, memory files, etc. on every RAG query when the files have
//!   not changed. The cache key is `(PathBuf, SystemTime)` — the mtime acts as
//!   a validity signal; a changed mtime produces a cache miss.
//!
//! Inputs:
//!   - `path: &Path` — absolute path to a cascade instruction file
//!   - `mtime: SystemTime` — last-modified timestamp from `fs::metadata`
//!   - `chunks: Vec<Chunk>` — parsed + chunked content to cache on miss
//!
//! Outputs:
//!   - `get(path, mtime)` → `Option<Vec<Chunk>>` — cache hit or miss
//!   - `insert(path, mtime, chunks)` — populate the cache
//!   - `counters()` → [`CacheCounters`] for the `cascade cache stats` CLI
//!   - `clear()` — evict all entries (used by `cascade cache clear --chunk`)
//!
//! Constraints:
//!   - Thread-safe via `Mutex` (not `RwLock`; writes are rare and short).
//!   - The Mutex is NOT held during file I/O — callers read the file outside
//!     this module and pass the resulting chunks in via `insert`. This keeps
//!     the critical section to a single HashMap lookup/insert, preventing
//!     lock contention from blocking other threads during slow disk reads.
//!   - Known limitation: a file modified to a new mtime and then restored to
//!     the original mtime would incorrectly return stale cache (mtime collision).
//!     This is acceptable because cascade instruction files are edited by
//!     humans; sub-second mtime collisions are vanishingly rare in practice,
//!     and the fix (content hash) would add I/O on every lookup.
//!   - Capacity is configured via `DaemonConfig.chunk_cache_capacity` (default 256).
//!
//! SPORT: MASTER-COMPONENTS.md → ChunkCache (cascade-daemon)

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::Mutex;
use std::time::SystemTime;

use cascade_types::chunker::Chunk;

// ── CacheCounters ─────────────────────────────────────────────────────────────

/// Snapshot of hit/miss/entry counts for this cache.
///
/// Consumed by the `cascade cache stats` IPC handler (T-P4-E04-11).
#[derive(Debug, Clone, Default)]
pub struct CacheCounters {
    /// Number of entries currently held in the cache.
    pub entries: usize,
    /// Total hits since the last `clear()` (or daemon start).
    pub hits: u64,
    /// Total misses since the last `clear()` (or daemon start).
    pub misses: u64,
}

// ── ChunkCache ────────────────────────────────────────────────────────────────

/// Key type: `(path, mtime)`.
///
/// WHY tuple: both components are required for correctness. Path alone is not
/// sufficient because the file may have been modified since last caching;
/// mtime alone is not sufficient because different files share the same mtime
/// under filesystem resolution (second granularity on some FSes).
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
struct CacheKey(PathBuf, SystemTime);

struct Inner {
    max: usize,
    /// `(lru_counter, chunks)` — the counter is bumped on every access so we
    /// can evict the least-recently-used entry when `max` is exceeded.
    entries: HashMap<CacheKey, (u64, Vec<Chunk>)>,
    counter: u64,
    hits: u64,
    misses: u64,
}

impl Inner {
    fn new(max: usize) -> Self {
        Self {
            max: max.max(1),
            entries: HashMap::new(),
            counter: 0,
            hits: 0,
            misses: 0,
        }
    }

    fn get(&mut self, key: &CacheKey) -> Option<Vec<Chunk>> {
        if let Some((ts, chunks)) = self.entries.get_mut(key) {
            self.counter += 1;
            *ts = self.counter;
            self.hits += 1;
            Some(chunks.clone())
        } else {
            self.misses += 1;
            None
        }
    }

    fn insert(&mut self, key: CacheKey, chunks: Vec<Chunk>) {
        self.counter += 1;
        self.entries.insert(key, (self.counter, chunks));
        if self.entries.len() > self.max {
            // O(n) LRU eviction — acceptable at the default capacity of 256.
            // WHY not `lru` crate: avoids adding a new dependency to this crate
            // for a cache accessed at most once per RAG query. When capacity
            // grows beyond ~1 000 entries, switch to the `lru` crate.
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

    fn clear(&mut self) {
        self.entries.clear();
        self.hits = 0;
        self.misses = 0;
        self.counter = 0;
    }
}

/// In-memory mtime-validated LRU cache for parsed cascade file content.
///
/// SPORT: MASTER-COMPONENTS.md → ChunkCache (cascade-daemon)
pub struct ChunkCache {
    inner: Mutex<Inner>,
}

impl ChunkCache {
    /// Create a new cache bounded to `capacity` entries.
    ///
    /// Use `DaemonConfig.chunk_cache_capacity` (default 256) as the argument.
    pub fn new(capacity: usize) -> Self {
        Self {
            inner: Mutex::new(Inner::new(capacity)),
        }
    }

    /// Look up cached chunks for `path` at `mtime`.
    ///
    /// Returns `None` on a miss (path not cached, or cached with a different
    /// mtime). The Mutex is released before returning.
    ///
    /// # Correctness
    ///
    /// The caller must not hold this result across an `insert` call from
    /// another thread — the returned `Vec<Chunk>` is a clone, so it is safe.
    pub fn get(&self, path: &Path, mtime: SystemTime) -> Option<Vec<Chunk>> {
        let key = CacheKey(path.to_path_buf(), mtime);
        self.inner.lock().ok()?.get(&key)
    }

    /// Cache the chunks produced by parsing `path` at the given `mtime`.
    ///
    /// If an entry for `(path, mtime)` already exists it is overwritten.
    /// The Mutex is NOT held during file I/O — the caller reads the file
    /// and chunks it before calling `insert`.
    pub fn insert(&self, path: &Path, mtime: SystemTime, chunks: Vec<Chunk>) {
        let key = CacheKey(path.to_path_buf(), mtime);
        if let Ok(mut g) = self.inner.lock() {
            g.insert(key, chunks);
        }
    }

    /// Return a snapshot of entry/hit/miss counters.
    ///
    /// Called by the daemon's `cache.stats` IPC handler (T-P4-E04-11).
    pub fn counters(&self) -> CacheCounters {
        self.inner
            .lock()
            .map(|g| CacheCounters {
                entries: g.entries.len(),
                hits: g.hits,
                misses: g.misses,
            })
            .unwrap_or_default()
    }

    /// Evict all entries and reset counters.
    ///
    /// Called when the daemon receives a `CacheClear { chunk: true }` IPC
    /// message from `cascade cache clear --chunk`.
    pub fn clear(&self) {
        if let Ok(mut g) = self.inner.lock() {
            g.clear();
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;
    use std::time::{Duration, UNIX_EPOCH};

    fn make_mtime(secs: u64) -> SystemTime {
        UNIX_EPOCH + Duration::from_secs(secs)
    }

    fn make_chunks(n: usize) -> Vec<Chunk> {
        use cascade_types::chunker::ChunkMetadata;
        use std::collections::HashMap;
        (0..n)
            .map(|i| Chunk {
                id: format!("chunk-{i}"),
                text: format!("text {i}"),
                metadata: ChunkMetadata {
                    start_byte: i * 10,
                    end_byte: i * 10 + 9,
                    source_path: None,
                    start_line: None,
                    end_line: None,
                    chunk_index: i,
                    total_chunks: n,
                    extra: HashMap::new(),
                },
            })
            .collect()
    }

    /// Same path + same mtime → cache hit; file is NOT re-read.
    #[test]
    fn chunk_cache_hit_same_mtime() {
        let cache = ChunkCache::new(10);
        let path = PathBuf::from("/fake/cascade.md");
        let mtime = make_mtime(1000);
        let chunks = make_chunks(3);

        cache.insert(&path, mtime, chunks.clone());

        let result = cache.get(&path, mtime).expect("expected cache hit");
        assert_eq!(result.len(), 3);

        let c = cache.counters();
        assert_eq!(c.entries, 1);
        assert_eq!(c.hits, 1);
        assert_eq!(c.misses, 0);
    }

    /// Same path but different mtime → cache miss (file was modified).
    #[test]
    fn chunk_cache_miss_on_mtime_change() {
        let cache = ChunkCache::new(10);
        let path = PathBuf::from("/fake/cascade.md");
        let mtime_old = make_mtime(1000);
        let mtime_new = make_mtime(2000);

        cache.insert(&path, mtime_old, make_chunks(2));

        // Lookup with the NEW mtime — must miss.
        let result = cache.get(&path, mtime_new);
        assert!(result.is_none(), "expected miss on stale mtime");

        let c = cache.counters();
        assert_eq!(c.misses, 1);
        assert_eq!(c.hits, 0);
    }

    /// Cache respects `chunk_cache_capacity` — oldest entry is evicted.
    #[test]
    fn chunk_cache_capacity_eviction() {
        let cap = 3usize;
        let cache = ChunkCache::new(cap);

        // Insert `cap + 1` distinct entries; the first should be evicted.
        for i in 0..=(cap as u64) {
            let path = PathBuf::from(format!("/fake/file-{i}.md"));
            cache.insert(&path, make_mtime(i + 1), make_chunks(1));
        }

        let c = cache.counters();
        // Exactly `cap` entries remain.
        assert_eq!(c.entries, cap, "expected {cap} entries after overflow; got {}", c.entries);
    }

    /// `clear()` evicts all entries and resets counters.
    #[test]
    fn chunk_cache_clear_resets_all() {
        let cache = ChunkCache::new(10);
        let path = PathBuf::from("/fake/file.md");
        let mtime = make_mtime(100);

        cache.insert(&path, mtime, make_chunks(2));
        let _ = cache.get(&path, mtime); // hit

        cache.clear();

        let c = cache.counters();
        assert_eq!(c.entries, 0);
        assert_eq!(c.hits, 0);
        assert_eq!(c.misses, 0);
        assert!(cache.get(&path, mtime).is_none(), "cache should be empty after clear");
    }
}
