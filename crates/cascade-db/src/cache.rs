//! Purpose: Embedded cache abstraction. SQLite-backed persistent cache + optional in-process
//!          bounded cache. Replaces the two competing query caches (QueryCache / LegacyQueryCache).
//! Inputs: string keys + byte values, optional TTL.
//! Outputs: a `CacheBackend` trait with a default SQLite implementation; Redis is opt-in only.
//! Constraints: Redis is NEVER required — default build uses SQLite + (optionally) moka.
//! SPORT: cascade-db / cache

use rusqlite::Connection;

use crate::conn::open_configured;
use crate::error::Result;

/// A simple string→bytes cache with optional time-to-live.
pub trait CacheBackend: Send + Sync {
    /// Fetch a value if present and not expired.
    fn get(&self, key: &str) -> Result<Option<Vec<u8>>>;
    /// Store a value with an optional TTL in seconds.
    fn put(&self, key: &str, value: &[u8], ttl_secs: Option<u64>) -> Result<()>;
    /// Remove a single key.
    fn invalidate(&self, key: &str) -> Result<()>;
    /// Remove every key whose name starts with `prefix` (namespace/project scoping).
    fn invalidate_prefix(&self, prefix: &str) -> Result<()>;
}

/// Persistent SQLite-backed cache. Suitable for the embed cache and query cache.
pub struct SqliteCache {
    conn: std::sync::Mutex<Connection>,
}

impl SqliteCache {
    /// Open (or create) a cache database at `path`.
    pub fn open(path: impl AsRef<std::path::Path>) -> Result<Self> {
        let conn = open_configured(path)?;
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS cache_entries(
                key        TEXT PRIMARY KEY,
                value      BLOB NOT NULL,
                expires_at INTEGER,           -- unix secs, NULL = never
                created_at INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_cache_expiry ON cache_entries(expires_at);",
        )?;
        Ok(Self { conn: std::sync::Mutex::new(conn) })
    }

    fn now() -> i64 {
        chrono::Utc::now().timestamp()
    }
}

impl CacheBackend for SqliteCache {
    fn get(&self, key: &str) -> Result<Option<Vec<u8>>> {
        let conn = self.conn.lock().expect("cache mutex poisoned");
        let now = Self::now();
        let row: Option<(Vec<u8>, Option<i64>)> = conn
            .query_row(
                "SELECT value, expires_at FROM cache_entries WHERE key = ?1",
                [key],
                |r| Ok((r.get(0)?, r.get(1)?)),
            )
            .ok();
        match row {
            Some((_v, Some(exp))) if exp <= now => {
                conn.execute("DELETE FROM cache_entries WHERE key = ?1", [key])?;
                Ok(None)
            }
            Some((v, _)) => Ok(Some(v)),
            None => Ok(None),
        }
    }

    fn put(&self, key: &str, value: &[u8], ttl_secs: Option<u64>) -> Result<()> {
        let conn = self.conn.lock().expect("cache mutex poisoned");
        let now = Self::now();
        let expires_at = ttl_secs.map(|t| now + t as i64);
        conn.execute(
            "INSERT INTO cache_entries(key, value, expires_at, created_at)
             VALUES(?1, ?2, ?3, ?4)
             ON CONFLICT(key) DO UPDATE SET value=?2, expires_at=?3, created_at=?4",
            rusqlite::params![key, value, expires_at, now],
        )?;
        Ok(())
    }

    fn invalidate(&self, key: &str) -> Result<()> {
        let conn = self.conn.lock().expect("cache mutex poisoned");
        conn.execute("DELETE FROM cache_entries WHERE key = ?1", [key])?;
        Ok(())
    }

    fn invalidate_prefix(&self, prefix: &str) -> Result<()> {
        let conn = self.conn.lock().expect("cache mutex poisoned");
        let pat = format!("{}%", prefix.replace('%', "\\%"));
        conn.execute(
            "DELETE FROM cache_entries WHERE key LIKE ?1 ESCAPE '\\'",
            [pat],
        )?;
        Ok(())
    }
}

/// In-process bounded cache (moka) for hot keys. Non-persistent; pairs with `SqliteCache`.
#[cfg(feature = "mem-cache")]
pub struct MemoryCache {
    inner: moka::sync::Cache<String, Vec<u8>>,
}

#[cfg(feature = "mem-cache")]
impl MemoryCache {
    /// Build a cache holding up to `max_entries`.
    pub fn new(max_entries: u64) -> Self {
        Self { inner: moka::sync::Cache::new(max_entries) }
    }
}

#[cfg(feature = "mem-cache")]
impl CacheBackend for MemoryCache {
    fn get(&self, key: &str) -> Result<Option<Vec<u8>>> {
        Ok(self.inner.get(key))
    }
    fn put(&self, key: &str, value: &[u8], _ttl_secs: Option<u64>) -> Result<()> {
        self.inner.insert(key.to_string(), value.to_vec());
        Ok(())
    }
    fn invalidate(&self, key: &str) -> Result<()> {
        self.inner.invalidate(key);
        Ok(())
    }
    fn invalidate_prefix(&self, prefix: &str) -> Result<()> {
        // moka has no prefix scan; iterate the (bounded) key set.
        let keys: Vec<String> = self
            .inner
            .iter()
            .map(|(k, _)| (*k).clone())
            .filter(|k| k.starts_with(prefix))
            .collect();
        for k in keys {
            self.inner.invalidate(&k);
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sqlite_cache_roundtrip_and_ttl() {
        let dir = tempfile::tempdir().unwrap();
        let c = SqliteCache::open(dir.path().join("c.db")).unwrap();
        c.put("a:1", b"hello", None).unwrap();
        assert_eq!(c.get("a:1").unwrap().as_deref(), Some(&b"hello"[..]));
        c.put("a:2", b"x", Some(0)).unwrap(); // already expired
        assert_eq!(c.get("a:2").unwrap(), None);
        c.put("b:1", b"y", None).unwrap();
        c.invalidate_prefix("a:").unwrap();
        assert_eq!(c.get("a:1").unwrap(), None);
        assert!(c.get("b:1").unwrap().is_some());
    }
}
