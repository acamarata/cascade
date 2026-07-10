//! LRU + TTL query cache for [`crate::index::CachedIndex`].
//!
//! # Purpose
//!
//! Wraps repeated calls to [`crate::index::sharding::ShardedIndex::search`] with
//! an in-memory LRU cache keyed by `(query_text, top_k, tenant_id, project_id)`.
//! Cache entries are invalidated after a configurable TTL.  The entire cache can
//! be cleared atomically on index mutation, or selectively invalidated by project
//! or tenant to support multi-tenant deployments.
//!
//! # Multi-tenant isolation
//!
//! `CacheKey` includes `tenant_id` and `project_id` (both `Option<String>`).
//! Single-tenant deployments pass `None` for both and behaviour is identical to
//! the previous single-tenant implementation.  Multi-tenant deployments should
//! always supply `tenant_id` to prevent cross-tenant cache contamination.
//!
//! # Inputs
//!
//! - `capacity`: maximum number of entries before LRU eviction (default 512).
//! - `ttl`: maximum age of a cached entry before it is treated as a miss (default 60 s).
//!
//! # Outputs
//!
//! - `get` returns `Some(Vec<SearchHit>)` on a valid (non-expired) cache hit.
//! - `stats` returns `(hits, misses)` cumulative counters.
//!
//! # Constraints
//!
//! - Thread-safe via `Mutex<LruCache<…>>` + `AtomicU64` counters.
//! - `lru` crate O(1) get/insert/evict.
//! - TTL expiry uses `std::time::Instant`; no system-clock syscall on every get.
//!
//! # Tickets
//!
//! T-P4-E04-07 (original), T-RAG-07 (multi-tenant + project scoping)
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-rag::cache::QueryCache

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;
use std::time::{Duration, Instant};

use lru::LruCache;

use crate::index::sharding::SearchHit;

// ── CacheEntry ────────────────────────────────────────────────────────────────

/// A single cached result with its insertion timestamp.
struct CacheEntry {
    hits: Vec<SearchHit>,
    inserted: Instant,
}

// ── CacheKey ──────────────────────────────────────────────────────────────────

/// Composite cache key: query text + top-k value + optional tenant/project scope.
///
/// `tenant_id` and `project_id` are `Option<String>` so that single-tenant
/// deployments (where both are `None`) continue to work without change.
/// Multi-tenant deployments must supply at least `tenant_id` to ensure that
/// tenant A cannot read tenant B's cached results.
///
/// # Example
///
/// ```rust
/// use cascade_rag::cache::query_cache::CacheKey;
///
/// // Single-tenant (default): no tenant/project scope.
/// let key = CacheKey { query: "hello".into(), top_k: 10, tenant_id: None, project_id: None };
///
/// // Multi-tenant: scoped to tenant "acme", project "alpha".
/// let scoped = CacheKey {
///     query: "hello".into(),
///     top_k: 10,
///     tenant_id: Some("acme".into()),
///     project_id: Some("alpha".into()),
/// };
/// ```
///
/// SPORT: MASTER-COMPONENTS.md → cascade-rag::cache::CacheKey (T-RAG-07)
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct CacheKey {
    pub query: String,
    pub top_k: usize,
    /// Optional tenant scope. `None` = default (single-tenant) tenant.
    pub tenant_id: Option<String>,
    /// Optional project scope within a tenant. `None` = cross-project.
    pub project_id: Option<String>,
}

// ── QueryCache ────────────────────────────────────────────────────────────────

/// LRU cache for ShardedIndex::search results with TTL expiry and multi-tenant isolation.
///
/// # Example
///
/// ```rust
/// use cascade_rag::cache::QueryCache;
/// use std::time::Duration;
///
/// let cache = QueryCache::new(512, Duration::from_secs(60));
/// cache.set("hello world".to_string(), 10, vec![]);
/// assert!(cache.get("hello world", 10).is_some());
/// let (hits, misses) = cache.stats();
/// assert_eq!(hits, 1);
/// assert_eq!(misses, 0);
/// ```
///
/// SPORT: MASTER-COMPONENTS.md → cascade-rag::cache::QueryCache
pub struct QueryCache {
    inner: Mutex<LruCache<CacheKey, CacheEntry>>,
    ttl: Duration,
    hits: AtomicU64,
    misses: AtomicU64,
    evictions: AtomicU64,
}

impl QueryCache {
    /// Construct a new query cache.
    ///
    /// # Inputs
    ///
    /// - `capacity`: maximum LRU entries (must be >= 1; clamped to 1 if zero).
    /// - `ttl`: TTL for cached entries; a zero duration disables caching effectively.
    pub fn new(capacity: usize, ttl: Duration) -> Self {
        let cap = std::num::NonZeroUsize::new(capacity.max(1)).expect("capacity clamped to >= 1");
        Self {
            inner: Mutex::new(LruCache::new(cap)),
            ttl,
            hits: AtomicU64::new(0),
            misses: AtomicU64::new(0),
            evictions: AtomicU64::new(0),
        }
    }

    // ── Single-tenant (backward-compat) API ──────────────────────────────────

    /// Look up a cached result for `(query, top_k)` with no tenant/project scope.
    ///
    /// Backward-compatible single-tenant variant. Equivalent to calling
    /// [`get_scoped`] with `tenant_id = None` and `project_id = None`.
    ///
    /// Returns `None` and increments the miss counter if:
    /// - the key is not present, or
    /// - the entry's age exceeds the configured TTL (entry is evicted on expiry).
    pub fn get(&self, query: &str, top_k: usize) -> Option<Vec<SearchHit>> {
        self.get_scoped(query, top_k, None, None)
    }

    /// Store a result under `(query, top_k)` with no tenant/project scope.
    ///
    /// Backward-compatible single-tenant variant.
    pub fn set(&self, query: String, top_k: usize, hits: Vec<SearchHit>) {
        self.set_scoped(query, top_k, None, None, hits);
    }

    // ── Multi-tenant (scoped) API ─────────────────────────────────────────────

    /// Look up a cached result for `(query, top_k, tenant_id, project_id)`.
    ///
    /// Returns `None` on cache miss or TTL expiry; increments counters accordingly.
    pub fn get_scoped(
        &self,
        query: &str,
        top_k: usize,
        tenant_id: Option<&str>,
        project_id: Option<&str>,
    ) -> Option<Vec<SearchHit>> {
        let key = CacheKey {
            query: query.to_owned(),
            top_k,
            tenant_id: tenant_id.map(str::to_owned),
            project_id: project_id.map(str::to_owned),
        };
        let mut guard = self.inner.lock().expect("QueryCache mutex poisoned");

        // `peek` avoids promoting an expired entry to LRU front.
        let expired = guard
            .peek(&key)
            .map(|e| e.inserted.elapsed() > self.ttl)
            .unwrap_or(false);

        if expired {
            guard.pop(&key);
            self.misses.fetch_add(1, Ordering::Relaxed);
            self.evictions.fetch_add(1, Ordering::Relaxed);
            return None;
        }

        match guard.get(&key) {
            Some(entry) => {
                self.hits.fetch_add(1, Ordering::Relaxed);
                Some(entry.hits.clone())
            }
            None => {
                self.misses.fetch_add(1, Ordering::Relaxed);
                None
            }
        }
    }

    /// Store a result under `(query, top_k, tenant_id, project_id)`.
    ///
    /// If the LRU is at capacity the least-recently-used entry is evicted by the
    /// `lru` crate; the eviction counter is incremented to track this.
    pub fn set_scoped(
        &self,
        query: String,
        top_k: usize,
        tenant_id: Option<&str>,
        project_id: Option<&str>,
        hits: Vec<SearchHit>,
    ) {
        let key = CacheKey {
            query,
            top_k,
            tenant_id: tenant_id.map(str::to_owned),
            project_id: project_id.map(str::to_owned),
        };
        let entry = CacheEntry {
            hits,
            inserted: Instant::now(),
        };
        let mut guard = self.inner.lock().expect("QueryCache mutex poisoned");
        let was_full = guard.len() >= guard.cap().into();
        guard.put(key, entry);
        if was_full {
            self.evictions.fetch_add(1, Ordering::Relaxed);
        }
    }

    // ── Invalidation ─────────────────────────────────────────────────────────

    /// Clear ALL entries across all tenants and projects.
    ///
    /// Called by [`crate::index::CachedIndex`] on any index mutation.
    ///
    /// WHY full clear (not selective invalidation) for the global case: the query
    /// key space is unbounded (arbitrary query strings × arbitrary top_k values).
    /// Computing which cache entries are affected by a upsert/delete would require
    /// scanning all keys and doing a reverse lookup into the index — O(n) and
    /// fragile. Full clear is O(1), safe, and the small default capacity
    /// repopulates within seconds of typical query traffic.
    pub fn clear(&self) {
        let mut guard = self.inner.lock().expect("QueryCache mutex poisoned");
        guard.clear();
    }

    /// Evict all entries belonging to a specific project (across all tenants).
    ///
    /// O(n) scan of the LRU. Use when a project's index is mutated in a
    /// multi-project deployment and you want to avoid a full global clear.
    ///
    /// Entries with `project_id = None` (cross-project) are NOT evicted — they
    /// are shared across projects and remain valid.
    pub fn invalidate_project(&self, project_id: &str) {
        let mut guard = self.inner.lock().expect("QueryCache mutex poisoned");
        // `lru::LruCache` does not expose `retain`; collect keys to evict then pop.
        let to_remove: Vec<CacheKey> = guard
            .iter()
            .filter(|(k, _)| k.project_id.as_deref() == Some(project_id))
            .map(|(k, _)| k.clone())
            .collect();
        for k in to_remove {
            guard.pop(&k);
            self.evictions.fetch_add(1, Ordering::Relaxed);
        }
    }

    /// Evict all entries belonging to a specific tenant (all projects under it).
    ///
    /// O(n) scan of the LRU. Use when a tenant's index is mutated and you want
    /// to avoid evicting the entire cross-tenant cache.
    ///
    /// Entries with `tenant_id = None` (default tenant) are NOT evicted.
    pub fn invalidate_tenant(&self, tenant_id: &str) {
        let mut guard = self.inner.lock().expect("QueryCache mutex poisoned");
        let to_remove: Vec<CacheKey> = guard
            .iter()
            .filter(|(k, _)| k.tenant_id.as_deref() == Some(tenant_id))
            .map(|(k, _)| k.clone())
            .collect();
        for k in to_remove {
            guard.pop(&k);
            self.evictions.fetch_add(1, Ordering::Relaxed);
        }
    }

    // ── Stats / size ─────────────────────────────────────────────────────────

    /// Return cumulative `(hits, misses)` counters.
    ///
    /// Counters are monotonically increasing; they do not reset on `clear()`.
    pub fn stats(&self) -> (u64, u64) {
        (
            self.hits.load(Ordering::Relaxed),
            self.misses.load(Ordering::Relaxed),
        )
    }

    /// Return `(hits, misses, evictions)` — extended stats.
    pub fn stats_extended(&self) -> (u64, u64, u64) {
        (
            self.hits.load(Ordering::Relaxed),
            self.misses.load(Ordering::Relaxed),
            self.evictions.load(Ordering::Relaxed),
        )
    }

    /// Current number of entries in the cache.
    pub fn len(&self) -> usize {
        self.inner.lock().map(|g| g.len()).unwrap_or(0)
    }

    /// Return `true` if the cache holds no entries.
    pub fn is_empty(&self) -> bool {
        self.len() == 0
    }
}

impl Default for QueryCache {
    fn default() -> Self {
        Self::new(512, Duration::from_secs(60))
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::index::sharding::SearchHit;

    fn make_hit(doc_id: &str) -> SearchHit {
        SearchHit {
            doc_id: doc_id.to_string(),
            distance: 0.1,
            shard_id: 0,
        }
    }

    // ── T-P4-E04-07 acceptance criterion 1: hit/miss counters ────────────────

    #[test]
    fn hit_returns_cached_value() {
        let cache = QueryCache::new(512, Duration::from_secs(60));
        cache.set("foo".into(), 5, vec![make_hit("a"), make_hit("b")]);

        let result = cache.get("foo", 5).expect("expected cache hit");
        assert_eq!(result.len(), 2);
        assert_eq!(result[0].doc_id, "a");

        let (hits, misses) = cache.stats();
        assert_eq!(hits, 1);
        assert_eq!(misses, 0);
    }

    #[test]
    fn miss_increments_miss_counter() {
        let cache = QueryCache::new(512, Duration::from_secs(60));
        assert!(cache.get("not-there", 10).is_none());

        let (hits, misses) = cache.stats();
        assert_eq!(hits, 0);
        assert_eq!(misses, 1);
    }

    #[test]
    fn ten_identical_calls_produce_one_miss_nine_hits() {
        let cache = QueryCache::new(512, Duration::from_secs(60));

        // First call: miss (cold cache), populate.
        assert!(cache.get("q", 3).is_none());
        cache.set("q".into(), 3, vec![make_hit("x")]);

        // Next 9 calls: hits.
        for _ in 0..9 {
            assert!(cache.get("q", 3).is_some());
        }

        let (hits, misses) = cache.stats();
        assert_eq!(hits, 9, "expected 9 hits");
        assert_eq!(misses, 1, "expected 1 miss (cold population)");
    }

    // ── T-P4-E04-07 acceptance criterion 2: TTL expiry ───────────────────────

    #[test]
    fn ttl_expiry_causes_miss() {
        // Use a very short TTL so the entry expires immediately.
        let cache = QueryCache::new(512, Duration::from_nanos(1));
        cache.set("q".into(), 5, vec![make_hit("z")]);

        // Spin until enough time has passed (should be nanoseconds).
        let start = Instant::now();
        while start.elapsed() < Duration::from_millis(10) {
            std::hint::spin_loop();
        }

        // Entry should now be expired → miss.
        assert!(cache.get("q", 5).is_none(), "expected TTL-expired miss");
        let (hits, misses) = cache.stats();
        assert_eq!(hits, 0);
        assert_eq!(misses, 1);
    }

    // ── T-P4-E04-07 acceptance criterion 3: LRU eviction ────────────────────

    #[test]
    fn lru_eviction_at_capacity() {
        let cache = QueryCache::new(2, Duration::from_secs(60));

        cache.set("a".into(), 1, vec![make_hit("a")]);
        cache.set("b".into(), 1, vec![make_hit("b")]);
        // Access "a" so it becomes MRU; "b" is now LRU.
        cache.get("a", 1);
        // Insert "c" — "b" should be evicted (LRU).
        cache.set("c".into(), 1, vec![make_hit("c")]);

        assert!(
            cache.get("a", 1).is_some(),
            "a should still be cached (MRU)"
        );
        assert!(
            cache.get("c", 1).is_some(),
            "c should be cached (just inserted)"
        );
        // "b" was evicted as LRU.
        assert!(
            cache.get("b", 1).is_none(),
            "b should have been LRU-evicted"
        );
    }

    // ── T-P4-E04-08 acceptance criterion: clear() invalidates cache ──────────

    #[test]
    fn clear_invalidates_all_entries() {
        let cache = QueryCache::new(512, Duration::from_secs(60));
        cache.set("q1".into(), 5, vec![make_hit("x")]);
        cache.set("q2".into(), 10, vec![make_hit("y")]);

        cache.clear();

        assert!(cache.get("q1", 5).is_none(), "after clear q1 must be miss");
        assert!(cache.get("q2", 10).is_none(), "after clear q2 must be miss");
        assert_eq!(cache.len(), 0);
    }

    // ── Concurrency smoke test ────────────────────────────────────────────────

    #[test]
    fn concurrent_get_set_does_not_panic() {
        use std::sync::Arc;
        use std::thread;

        let cache = Arc::new(QueryCache::new(64, Duration::from_secs(60)));
        let mut handles = vec![];

        for i in 0..8u64 {
            let c = Arc::clone(&cache);
            handles.push(thread::spawn(move || {
                let key = format!("key-{}", i % 4);
                c.set(key.clone(), 5, vec![]);
                let _ = c.get(&key, 5);
            }));
        }

        for h in handles {
            h.join().expect("thread panicked");
        }
    }

    // ── T-RAG-07: Multi-tenant isolation ─────────────────────────────────────

    #[test]
    fn scoped_keys_are_isolated_by_tenant() {
        let cache = QueryCache::new(512, Duration::from_secs(60));

        // Tenant A populates the cache.
        cache.set_scoped(
            "query".into(),
            5,
            Some("tenant-a"),
            None,
            vec![make_hit("doc-a")],
        );

        // Tenant B must NOT see tenant A's result.
        let b_result = cache.get_scoped("query", 5, Some("tenant-b"), None);
        assert!(
            b_result.is_none(),
            "tenant B must not see tenant A's cached result"
        );

        // Tenant A's own key is still a hit.
        let a_result = cache.get_scoped("query", 5, Some("tenant-a"), None);
        assert!(
            a_result.is_some(),
            "tenant A must hit its own cached result"
        );
    }

    // ── T-RAG-07: invalidate_project leaves other project intact ─────────────

    #[test]
    fn invalidate_project_leaves_other_project_intact() {
        let cache = QueryCache::new(512, Duration::from_secs(60));

        // Populate two projects under the same tenant.
        cache.set_scoped(
            "q".into(),
            10,
            Some("tenant-x"),
            Some("project-alpha"),
            vec![make_hit("alpha-doc")],
        );
        cache.set_scoped(
            "q".into(),
            10,
            Some("tenant-x"),
            Some("project-beta"),
            vec![make_hit("beta-doc")],
        );

        // Invalidate project-alpha only.
        cache.invalidate_project("project-alpha");

        // project-alpha must be gone.
        assert!(
            cache
                .get_scoped("q", 10, Some("tenant-x"), Some("project-alpha"))
                .is_none(),
            "project-alpha cache must be evicted"
        );

        // project-beta must remain.
        assert!(
            cache
                .get_scoped("q", 10, Some("tenant-x"), Some("project-beta"))
                .is_some(),
            "project-beta cache must remain intact after invalidating project-alpha"
        );
    }

    // ── T-RAG-07: invalidate_tenant clears all that tenant's projects ─────────

    #[test]
    fn invalidate_tenant_clears_all_tenant_projects() {
        let cache = QueryCache::new(512, Duration::from_secs(60));

        // Tenant A, two projects.
        cache.set_scoped(
            "q".into(),
            5,
            Some("tenant-a"),
            Some("proj-1"),
            vec![make_hit("a1")],
        );
        cache.set_scoped(
            "q".into(),
            5,
            Some("tenant-a"),
            Some("proj-2"),
            vec![make_hit("a2")],
        );
        // Tenant B.
        cache.set_scoped(
            "q".into(),
            5,
            Some("tenant-b"),
            Some("proj-1"),
            vec![make_hit("b1")],
        );

        // Invalidate all of tenant-a.
        cache.invalidate_tenant("tenant-a");

        // Both tenant-a projects must be gone.
        assert!(
            cache
                .get_scoped("q", 5, Some("tenant-a"), Some("proj-1"))
                .is_none(),
            "tenant-a proj-1 must be evicted"
        );
        assert!(
            cache
                .get_scoped("q", 5, Some("tenant-a"), Some("proj-2"))
                .is_none(),
            "tenant-a proj-2 must be evicted"
        );

        // Tenant B must be untouched.
        assert!(
            cache
                .get_scoped("q", 5, Some("tenant-b"), Some("proj-1"))
                .is_some(),
            "tenant-b cache must remain intact after invalidating tenant-a"
        );
    }
}
