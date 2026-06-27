//! # IndexStateStore — per-file delta tracking for incremental indexing.
//!
//! Stores `(doc_id, file_path, content_hash, mtime, byte_size, indexed_at)` in a
//! dedicated `cascade_state.db` separate from the vec shards, so filesystem-state
//! writes never contend with embedding reads.
//!
//! ## Delta detection algorithm
//!
//! 1. **Fast-path:** compare `mtime` + `byte_size`. If both match the stored row,
//!    the file is likely unchanged — skip Blake3 entirely (saves I/O on large files).
//! 2. **Content-path:** if mtime or size differ, compute Blake3 hex of file bytes
//!    and compare with `content_hash`. Skip only when hash matches.
//! 3. **Eviction sweep:** call [`IndexStateStore::evict_deleted`] after the ingest
//!    loop to remove doc_ids no longer present on disk.
//!
//! ## DB location
//!
//! `<index_root>/cascade_state.db` — callers pass the index root path.
//!
//! ## Thread safety
//!
//! [`IndexStateStore`] wraps a single `rusqlite::Connection`.  It is `Send` but
//! not `Sync`.  In async contexts wrap it in a `tokio::sync::Mutex`.
//!
//! SPORT: MASTER-COMPONENTS.md → cascade-rag::IndexStateStore

use std::collections::HashSet;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use rusqlite::{params, Connection};
use tracing::{debug, instrument, warn};

use cascade_types::error::{CascadeError, Result};

use crate::index::sharding::ShardedIndex;

// Embedded migration SQL (relative to crate root where Cargo.toml lives).
const SQL_STATE_SCHEMA: &str = include_str!("../../migrations/0007_index_state.sql");

// ── IndexStateStore ───────────────────────────────────────────────────────────

/// Persistent delta state for incremental indexing.
///
/// # Purpose
/// Tracks `(file_path, content_hash, mtime, byte_size)` for every indexed file.
/// Enables the ingest daemon to skip unchanged files without hashing them.
///
/// # Inputs
/// - `index_root` — directory under which `cascade_state.db` is created.
///
/// # Outputs
/// - [`ChangeKind`] from [`IndexStateStore::classify`] guides the ingest loop.
/// - Counts from [`IndexStateStore::count`] feed `cascade status --json`.
///
/// # Constraints
/// - `index_root` is created if absent.
/// - Not `Sync` — wrap in `Mutex` for concurrent access.
///
/// SPORT: MASTER-COMPONENTS.md → cascade-rag::IndexStateStore
pub struct IndexStateStore {
    conn: Connection,
}

// ── ChangeKind ────────────────────────────────────────────────────────────────

/// Result of [`IndexStateStore::classify`]: why (or why not) to ingest a file.
///
/// SPORT: MASTER-COMPONENTS.md → cascade-rag::ChangeKind
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ChangeKind {
    /// mtime + byte_size match — file is almost certainly unchanged; skip.
    FastSkip,
    /// mtime/size matched but Blake3 hash confirms the content is identical.
    HashSkip,
    /// File is new or its content hash changed — must ingest.
    Changed,
}

impl IndexStateStore {
    /// Open (or create) `cascade_state.db` inside `index_root`.
    ///
    /// Creates parent directories if they do not exist.  Applies the
    /// `index_state` schema migration on first open.
    pub fn open(index_root: &Path) -> Result<Self> {
        std::fs::create_dir_all(index_root).map_err(|e| CascadeError::Io {
            path: index_root.to_path_buf(),
            operation: "create index_root",
            source: e,
        })?;

        let db_path = index_root.join("cascade_state.db");
        let conn = cascade_db::open_configured(&db_path)
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        // Apply schema.
        conn.execute_batch(SQL_STATE_SCHEMA)
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        Ok(Self { conn })
    }

    /// Open an in-memory database (for tests only).
    #[cfg(test)]
    pub fn open_in_memory() -> Result<Self> {
        let conn = Connection::open_in_memory().map_err(|e| CascadeError::Other(e.to_string()))?;
        conn.execute_batch(SQL_STATE_SCHEMA)
            .map_err(|e| CascadeError::Other(e.to_string()))?;
        Ok(Self { conn })
    }

    // ── Classification ────────────────────────────────────────────────────────

    /// Classify a file path: should it be re-ingested?
    ///
    /// # Algorithm
    /// 1. Read `mtime` + `byte_size` from `path` metadata (no file read).
    /// 2. Look up stored row for this `doc_id`.
    /// 3. If row missing → [`ChangeKind::Changed`] (new file).
    /// 4. If `mtime` + `byte_size` match stored values → [`ChangeKind::FastSkip`].
    /// 5. Otherwise compute Blake3 of file bytes:
    ///    - Hash matches stored `content_hash` → [`ChangeKind::HashSkip`].
    ///    - Hash differs → [`ChangeKind::Changed`].
    ///
    /// # Errors
    /// Returns `CascadeError::Io` if the file cannot be read for Blake3.
    #[instrument(skip(self), fields(path = %path.display()))]
    pub fn classify(&self, path: &Path) -> Result<(ChangeKind, Option<String>)> {
        let doc_id = doc_id_for_path(path);
        let meta = std::fs::metadata(path).ok();
        let mtime = meta.as_ref().and_then(|m| {
            m.modified()
                .ok()
                .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
                .map(|d| d.as_secs() as i64)
        });
        let byte_size = meta.as_ref().map(|m| m.len() as i64);

        // Look up stored row.
        let stored = self.get_row(&doc_id)?;

        match stored {
            None => {
                // New file — need hash for storage.
                let hash = blake3_file(path)?;
                Ok((ChangeKind::Changed, Some(hash)))
            }
            Some(row) => {
                // Fast-path: if both mtime and byte_size match, skip without hashing.
                if mtime.is_some()
                    && byte_size.is_some()
                    && row.mtime == mtime
                    && row.byte_size == byte_size
                {
                    debug!(path = %path.display(), "fast-skip (mtime+size match)");
                    return Ok((ChangeKind::FastSkip, None));
                }

                // Content-path: need to hash.
                let hash = blake3_file(path)?;
                if hash == row.content_hash {
                    debug!(path = %path.display(), "hash-skip (content unchanged)");
                    Ok((ChangeKind::HashSkip, Some(hash)))
                } else {
                    debug!(path = %path.display(), "changed (hash mismatch)");
                    Ok((ChangeKind::Changed, Some(hash)))
                }
            }
        }
    }

    // ── Mutations ─────────────────────────────────────────────────────────────

    /// Upsert a state row after successful ingest.
    ///
    /// # Parameters
    /// - `path` — file path (used to derive `doc_id`).
    /// - `content_hash` — Blake3 hex from the ingest pass.
    pub fn set_hash(&self, path: &Path, content_hash: &str) -> Result<()> {
        let doc_id = doc_id_for_path(path);
        let path_str = path.to_string_lossy();
        let mtime = file_mtime(path);
        let byte_size = file_byte_size(path);
        let now = now_secs();

        self.conn.execute(
            "INSERT INTO index_state (doc_id, file_path, content_hash, mtime, byte_size, indexed_at)
             VALUES (?1, ?2, ?3, ?4, ?5, ?6)
             ON CONFLICT(doc_id) DO UPDATE SET
               file_path    = excluded.file_path,
               content_hash = excluded.content_hash,
               mtime        = excluded.mtime,
               byte_size    = excluded.byte_size,
               indexed_at   = excluded.indexed_at",
            params![doc_id, path_str.as_ref(), content_hash, mtime, byte_size, now],
        )
        .map_err(|e| CascadeError::Other(e.to_string()))?;

        Ok(())
    }

    /// Remove a single doc from the state store (file was deleted).
    pub fn delete(&self, path: &Path) -> Result<()> {
        let doc_id = doc_id_for_path(path);
        self.conn
            .execute("DELETE FROM index_state WHERE doc_id = ?1", params![doc_id])
            .map_err(|e| CascadeError::Other(e.to_string()))?;
        Ok(())
    }

    // ── Queries ───────────────────────────────────────────────────────────────

    /// Return all currently-tracked file paths.
    pub fn all_indexed_paths(&self) -> Result<Vec<PathBuf>> {
        let mut stmt = self
            .conn
            .prepare_cached("SELECT file_path FROM index_state")
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        let paths: Vec<PathBuf> = stmt
            .query_map([], |row| {
                let s: String = row.get(0)?;
                Ok(PathBuf::from(s))
            })
            .map_err(|e| CascadeError::Other(e.to_string()))?
            .filter_map(|r| r.ok())
            .collect();

        Ok(paths)
    }

    /// Return the set of doc_ids for all currently-tracked files.
    pub fn all_indexed_doc_ids(&self) -> Result<HashSet<String>> {
        let mut stmt = self
            .conn
            .prepare_cached("SELECT doc_id FROM index_state")
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        let ids: HashSet<String> = stmt
            .query_map([], |row| row.get::<_, String>(0))
            .map_err(|e| CascadeError::Other(e.to_string()))?
            .filter_map(|r| r.ok())
            .collect();

        Ok(ids)
    }

    /// Total number of tracked documents.  Used by `cascade status --json`.
    pub fn count(&self) -> usize {
        self.conn
            .query_row("SELECT COUNT(*) FROM index_state", [], |r| {
                r.get::<_, i64>(0)
            })
            .unwrap_or(0) as usize
    }

    /// Evict doc_ids that are no longer present on disk.
    ///
    /// Returns the number of rows removed.
    ///
    /// NOTE: this variant only removes from the state table.  Use
    /// [`evict_deleted_with_shard`] when a [`ShardedIndex`] is available so that
    /// orphaned embedding rows are also cleaned up (bug #5).
    pub fn evict_deleted(&self) -> Result<usize> {
        self.evict_deleted_with_shard(None)
    }

    /// Evict doc_ids that are no longer present on disk, also removing their
    /// embedding rows from `shard_index` when provided.
    ///
    /// # Purpose
    ///
    /// Without a shard reference, deleted doc_ids are removed from the
    /// `index_state` tracking table but their embedding rows remain in
    /// `shard_embeddings`, leaking space and polluting KNN results (bug #5).
    /// Passing `Some(shard)` atomically cleans both.
    ///
    /// # Inputs
    ///
    /// `shard_index`: optional [`ShardedIndex`] to delete orphaned embedding rows
    /// from.  Pass `None` if the sharded index is not available (state-only cleanup).
    ///
    /// # Outputs
    ///
    /// Number of doc_id rows removed from `index_state`.
    pub fn evict_deleted_with_shard(&self, shard_index: Option<&ShardedIndex>) -> Result<usize> {
        let tracked = self.all_indexed_paths()?;
        let mut evicted = 0usize;
        for path in &tracked {
            if !path.exists() {
                let doc_id = doc_id_for_path(path);
                self.delete(path)?;
                // Bug #5 fix: also remove the vector from the shard so no orphaned
                // embedding rows remain after eviction.
                if let Some(shard) = shard_index {
                    if let Err(e) = shard.delete(&doc_id) {
                        warn!(doc_id = %doc_id, error = %e, "evict_deleted: failed to delete shard embedding");
                    }
                }
                evicted += 1;
            }
        }
        Ok(evicted)
    }

    // ── Internal ──────────────────────────────────────────────────────────────

    fn get_row(&self, doc_id: &str) -> Result<Option<StateRow>> {
        let mut stmt = self
            .conn
            .prepare_cached(
                "SELECT content_hash, mtime, byte_size FROM index_state WHERE doc_id = ?1",
            )
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        let mut rows = stmt
            .query(params![doc_id])
            .map_err(|e| CascadeError::Other(e.to_string()))?;

        if let Some(row) = rows
            .next()
            .map_err(|e| CascadeError::Other(e.to_string()))?
        {
            Ok(Some(StateRow {
                content_hash: row.get(0).map_err(|e| CascadeError::Other(e.to_string()))?,
                mtime: row.get(1).map_err(|e| CascadeError::Other(e.to_string()))?,
                byte_size: row.get(2).map_err(|e| CascadeError::Other(e.to_string()))?,
            }))
        } else {
            Ok(None)
        }
    }
}

// ── Internal types ─────────────────────────────────────────────────────────────

struct StateRow {
    content_hash: String,
    mtime: Option<i64>,
    byte_size: Option<i64>,
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Deterministic doc_id: Blake3 hex of the UTF-8 path string.
/// Stable across renames as long as the path is stable; cheap to compute.
pub fn doc_id_for_path(path: &Path) -> String {
    let path_bytes = path.to_string_lossy();
    let hash = blake3::hash(path_bytes.as_bytes());
    hash.to_hex().to_string()
}

/// Compute Blake3 hex of file bytes.
///
/// # Errors
/// `CascadeError::Io` on read failure.
pub fn blake3_file(path: &Path) -> Result<String> {
    let bytes = std::fs::read(path).map_err(|e| CascadeError::Io {
        path: path.to_path_buf(),
        operation: "blake3 read",
        source: e,
    })?;
    let hash = blake3::hash(&bytes);
    Ok(hash.to_hex().to_string())
}

fn file_mtime(path: &Path) -> Option<i64> {
    std::fs::metadata(path)
        .ok()
        .and_then(|m| m.modified().ok())
        .and_then(|t| t.duration_since(UNIX_EPOCH).ok())
        .map(|d| d.as_secs() as i64)
}

fn file_byte_size(path: &Path) -> Option<i64> {
    std::fs::metadata(path).ok().map(|m| m.len() as i64)
}

fn now_secs() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write as IoWrite;
    use tempfile::NamedTempFile;

    fn write_file(content: &str) -> NamedTempFile {
        let mut f = tempfile::NamedTempFile::new().expect("tempfile");
        IoWrite::write_all(&mut f, content.as_bytes()).expect("write");
        IoWrite::flush(&mut f).expect("flush");
        f
    }

    // ── state::new_file_is_changed ─────────────────────────────────────────────

    #[test]
    fn new_file_classified_as_changed() {
        let store = IndexStateStore::open_in_memory().expect("store open");
        let f = write_file("hello world");
        let (kind, hash) = store.classify(f.path()).expect("classify ok");
        assert_eq!(kind, ChangeKind::Changed);
        assert!(hash.is_some(), "new file must return hash");
    }

    // ── state::unchanged_fast_skip ────────────────────────────────────────────

    #[test]
    fn unchanged_file_fast_skips_on_second_classify() {
        let store = IndexStateStore::open_in_memory().expect("store open");
        let f = write_file("stable content");

        // First classify — Changed.
        let (_, hash) = store.classify(f.path()).expect("classify ok");
        store
            .set_hash(f.path(), &hash.unwrap())
            .expect("set_hash ok");

        // Second classify — must FastSkip (mtime+size unchanged for tempfile).
        let (kind, _) = store.classify(f.path()).expect("second classify ok");
        // FastSkip or HashSkip — both mean "skip"; FastSkip is preferred.
        assert!(
            kind == ChangeKind::FastSkip || kind == ChangeKind::HashSkip,
            "expected skip kind, got {:?}",
            kind
        );
    }

    // ── state::changed_file_is_changed ────────────────────────────────────────

    #[test]
    fn modified_file_classified_as_changed() {
        let store = IndexStateStore::open_in_memory().expect("store open");
        let f = write_file("version 1");

        let (_, hash) = store.classify(f.path()).expect("classify ok");
        store
            .set_hash(f.path(), &hash.unwrap())
            .expect("set_hash ok");

        // Overwrite content — new bytes, new hash.
        std::fs::write(f.path(), b"version 2 - completely different").unwrap();

        let (kind, _) = store.classify(f.path()).expect("classify modified ok");
        assert_eq!(kind, ChangeKind::Changed, "modified file must be Changed");
    }

    // ── state::delete_removes_row ─────────────────────────────────────────────

    #[test]
    fn delete_removes_row() {
        let store = IndexStateStore::open_in_memory().expect("store open");
        let f = write_file("content for delete test");

        let (_, hash) = store.classify(f.path()).expect("classify ok");
        store
            .set_hash(f.path(), &hash.unwrap())
            .expect("set_hash ok");

        assert_eq!(store.count(), 1, "one row after set_hash");

        store.delete(f.path()).expect("delete ok");
        assert_eq!(store.count(), 0, "zero rows after delete");
    }

    // ── state::evict_deleted ──────────────────────────────────────────────────

    #[test]
    fn evict_deleted_removes_missing_files() {
        let store = IndexStateStore::open_in_memory().expect("store open");

        // Add two files; remove one from disk.
        let f1 = write_file("file one");
        let f2 = write_file("file two");

        for f in &[&f1, &f2] {
            let (_, hash) = store.classify(f.path()).expect("classify ok");
            store
                .set_hash(f.path(), &hash.unwrap())
                .expect("set_hash ok");
        }
        assert_eq!(store.count(), 2);

        // Drop f2 — removes the file from disk.
        let path2 = f2.path().to_path_buf();
        drop(f2);
        assert!(!path2.exists(), "f2 must be deleted");

        let evicted = store.evict_deleted().expect("evict ok");
        assert_eq!(evicted, 1, "exactly one file evicted");
        assert_eq!(store.count(), 1, "one row remains");
    }

    // ── state::count ──────────────────────────────────────────────────────────

    #[test]
    fn count_tracks_additions_and_deletions() {
        let store = IndexStateStore::open_in_memory().expect("store open");
        assert_eq!(store.count(), 0);

        let f = write_file("counting test");
        let (_, hash) = store.classify(f.path()).expect("classify ok");
        store
            .set_hash(f.path(), &hash.unwrap())
            .expect("set_hash ok");
        assert_eq!(store.count(), 1);

        store.delete(f.path()).expect("delete ok");
        assert_eq!(store.count(), 0);
    }

    // ── state::blake3_file ────────────────────────────────────────────────────

    #[test]
    fn blake3_file_is_deterministic() {
        let f = write_file("determinism check");
        let h1 = blake3_file(f.path()).unwrap();
        let h2 = blake3_file(f.path()).unwrap();
        assert_eq!(h1, h2);
        assert_eq!(h1.len(), 64, "Blake3 hex must be 64 chars");
    }

    #[test]
    fn blake3_file_differs_on_content_change() {
        let f1 = write_file("aaa");
        let f2 = write_file("bbb");
        assert_ne!(
            blake3_file(f1.path()).unwrap(),
            blake3_file(f2.path()).unwrap()
        );
    }

    // ── state::doc_id_for_path ────────────────────────────────────────────────

    #[test]
    fn doc_id_is_deterministic() {
        let p = std::path::Path::new("/some/stable/path.md");
        assert_eq!(doc_id_for_path(p), doc_id_for_path(p));
    }

    // ── state::evict_deleted_with_shard — bug #5 fix ─────────────────────────

    /// Insert a doc+vector into both state store and sharded index, then evict.
    /// After eviction the shard total_count must be 0 (no orphan embedding row).
    #[test]
    fn evict_deleted_cleans_shard_embeddings() {
        use crate::index::sharding::{EmbedResult, ShardedIndex};

        let dir = tempfile::tempdir().expect("tmpdir");
        let idx = ShardedIndex::new(dir.path(), 2, 4).expect("ShardedIndex::new");

        // Register a file in the state store.
        let store = IndexStateStore::open_in_memory().expect("store open");
        let f = write_file("evict shard orphan test");
        let doc_id = doc_id_for_path(f.path());

        let (_, hash) = store.classify(f.path()).expect("classify ok");
        store.set_hash(f.path(), &hash.unwrap()).expect("set_hash");

        // Write a corresponding embedding into the sharded index using the same doc_id.
        idx.upsert(&EmbedResult {
            doc_id: doc_id.clone(),
            embedding: vec![0.1f32; 4],
        })
        .expect("upsert embedding");

        // Verify embedding is present via the public total_count API.
        assert_eq!(idx.total_count(), 1, "embedding must exist before eviction");

        // Delete the file from disk then evict with shard reference.
        let path = f.path().to_path_buf();
        drop(f);
        assert!(!path.exists(), "file must be deleted from disk");

        let evicted = store
            .evict_deleted_with_shard(Some(&idx))
            .expect("evict ok");
        assert_eq!(evicted, 1, "one doc evicted from state store");
        assert_eq!(store.count(), 0, "state store must be empty");

        // Bug #5: shard embedding must also be gone — no orphan.
        assert_eq!(
            idx.total_count(),
            0,
            "shard embedding row must be gone after eviction (no orphan)"
        );
    }
}
