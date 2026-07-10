//! # IndexManager — multi-project index routing with cascade-tier awareness.
//!
//! `IndexManager` is the per-project owner of a RAG SQLite database.  The daemon
//! instantiates one `IndexManager` per connected project and keeps it alive for
//! the lifetime of that project connection.
//!
//! ## DB path resolution (priority order)
//!
//! 1. `CASCADE_INDEX_ROOT` env var — `$CASCADE_INDEX_ROOT/<project-hash>/cascade.db`
//! 2. Default — `<project_root>/.cascade/index/cascade.db`
//!
//! Parent directories are created automatically before the DB is opened.
//!
//! ## Tier-aware source registration
//!
//! Every source (file) ingested via `register_source` carries a
//! [`CascadeTier`] tag so the RAG search layer can scope results to the
//! appropriate instruction tier (GCI → PAC).
//!
//! ## Pause / resume
//!
//! The volume-watcher integration calls `pause()` / `resume()` when the volume
//! hosting the index root is unmounted / remounted.  Paused state is tracked
//! atomically; callers check `is_paused()` before submitting ingest work.
//!
//! ## Connection cache
//!
//! One `rusqlite::Connection` per `IndexManager`, protected by a `tokio::sync::Mutex`
//! for safe concurrent async access.  WAL mode is enabled on open.
//!
//! SPORT: MASTER-CRATES.md → cascade-rag::index_manager::IndexManager

use crate::db::run_migrations;
use cascade_types::error::{CascadeError, Result};
use cascade_types::CascadeTier;
use rusqlite::{params, Connection};
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use tokio::sync::Mutex;
use tracing::{debug, info, instrument, warn};

// ── SourceInfo ────────────────────────────────────────────────────────────────

/// Metadata about a single indexed source file.
///
/// Returned by [`IndexManager::list_sources`].
///
/// SPORT: MASTER-CRATES.md → cascade-rag::index_manager::SourceInfo
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SourceInfo {
    /// Absolute or project-relative path to the source file.
    pub path: PathBuf,
    /// Unix timestamp of when this source was last indexed.
    pub indexed_at: Option<i64>,
    /// Number of chunks produced from this source.
    pub chunk_count: u64,
    /// Cascade instruction tier this source belongs to.
    pub tier: Option<String>,
}

// ── IndexManager ──────────────────────────────────────────────────────────────

/// Per-project RAG index manager.
///
/// Owns the SQLite connection for one project's index database.  Thread-safe
/// via internal `Mutex`; clone the `Arc<IndexManager>` to share across tasks.
///
/// # Example
///
/// ```rust,no_run
/// # use cascade_rag::index_manager::IndexManager;
/// # use cascade_types::CascadeTier;
/// # async fn example() -> cascade_types::error::Result<()> {
/// let mgr = IndexManager::open("/home/user/my-project").await?;
/// mgr.register_source(
///     std::path::Path::new("/home/user/my-project/.cascade/CLAUDE.md"),
///     CascadeTier::Prc,
/// ).await?;
/// let sources = mgr.list_sources().await?;
/// println!("{} sources indexed", sources.len());
/// # Ok(())
/// # }
/// ```
pub struct IndexManager {
    /// Resolved path to the SQLite database file.
    db_path: PathBuf,
    /// Project root this manager is scoped to.
    project_root: PathBuf,
    /// Read-write database connection.
    conn: Mutex<Connection>,
    /// Whether ingest is currently paused (e.g. volume unmounted).
    paused: Mutex<bool>,
}

impl IndexManager {
    // ── Construction ─────────────────────────────────────────────────────────

    /// Open (or create) the index database for `project_root`.
    ///
    /// Resolves the DB path, creates parent directories, opens the connection,
    /// enables WAL mode, and runs migrations.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError`] if the directory cannot be created or the DB
    /// cannot be opened / migrated.
    #[instrument(skip_all, fields(project = %project_root.as_ref().display()))]
    pub async fn open(project_root: impl AsRef<Path>) -> Result<Arc<Self>> {
        let project_root = project_root.as_ref().to_path_buf();
        let db_path = resolve_db_path(&project_root);

        info!(db = %db_path.display(), "IndexManager: opening project index");

        let conn =
            cascade_db::open_configured(&db_path).map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("open index db: {e}"),
            })?;

        // Run forward-only schema migrations.
        run_migrations(&conn).map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("migrations: {e}"),
        })?;

        // Ensure the tier column exists on rag_sources (additive migration).
        ensure_tier_column(&conn)?;

        Ok(Arc::new(Self {
            db_path,
            project_root,
            conn: Mutex::new(conn),
            paused: Mutex::new(false),
        }))
    }

    // ── Path resolution ───────────────────────────────────────────────────────

    /// Path to the SQLite database file used by this manager.
    pub fn db_path(&self) -> &Path {
        &self.db_path
    }

    /// Project root this manager is scoped to.
    pub fn project_root(&self) -> &Path {
        &self.project_root
    }

    /// Open a [`RagIndex`] backed by this manager's SQLite database.
    ///
    /// Returns a fresh `Arc<RagIndex>` each call, sharing the same on-disk WAL
    /// file.  SQLite WAL mode allows the `RagIndex` read path to coexist with
    /// the `IndexManager` write connection without blocking.
    ///
    /// # Use cases
    ///
    /// - Injecting an `RrfRetriever::fts_only` into the MCP `ToolRegistry` so
    ///   `cascade.search` returns live hits instead of "index not ready".
    /// - Integration tests that need a retriever wired to a real DB.
    ///
    /// # Errors
    ///
    /// Propagates any error from [`RagIndex::open`] (e.g. the DB file cannot be
    /// read, or migrations fail).
    pub async fn open_rag_index(&self) -> Result<Arc<crate::index::RagIndex>> {
        let idx = crate::index::RagIndex::open(&self.db_path).await?;
        Ok(Arc::new(idx))
    }

    // ── Tier-aware source registration ────────────────────────────────────────

    /// Register (upsert) a source path with its cascade tier tag.
    ///
    /// If the source already exists in `rag_sources` the tier is updated
    /// in-place without touching chunks or embeddings.
    ///
    /// Returns `false` without writing if the manager is currently paused.
    pub async fn register_source(&self, path: &Path, tier: CascadeTier) -> Result<bool> {
        if self.is_paused().await {
            warn!(
                path = %path.display(),
                "IndexManager: paused — skipping source registration"
            );
            return Ok(false);
        }
        let conn = self.conn.lock().await;
        let path_str = path.to_string_lossy();
        let tier_str = tier.acronym();
        conn.execute(
            "INSERT INTO rag_sources (file_path, indexed_at, tier)
             VALUES (?1, strftime('%s','now'), ?2)
             ON CONFLICT(file_path) DO UPDATE SET
               indexed_at = excluded.indexed_at,
               tier       = excluded.tier",
            params![path_str, tier_str],
        )
        .map_err(|e| CascadeError::RetrievalFailed {
            detail: format!("register source: {e}"),
        })?;
        debug!(path = %path.display(), tier = tier_str, "source registered");
        Ok(true)
    }

    // ── list_sources ──────────────────────────────────────────────────────────

    /// Return metadata for every indexed source in this project's database.
    ///
    /// Each row includes the path, last-indexed timestamp, chunk count, and
    /// optional tier tag.  An empty index returns an empty `Vec` (not an error).
    pub async fn list_sources(&self) -> Result<Vec<SourceInfo>> {
        let conn = self.conn.lock().await;
        let mut stmt = conn
            .prepare(
                "SELECT rs.file_path,
                        rs.indexed_at,
                        COUNT(rc.id)  AS chunk_count,
                        rs.tier
                 FROM   rag_sources  rs
                 LEFT JOIN rag_chunks rc ON rc.source_id = rs.id
                 GROUP BY rs.id
                 ORDER BY rs.file_path",
            )
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("list_sources prepare: {e}"),
            })?;

        let sources: Vec<SourceInfo> = stmt
            .query_map([], |row| {
                Ok(SourceInfo {
                    path: PathBuf::from(row.get::<_, String>(0)?),
                    indexed_at: row.get::<_, Option<i64>>(1)?,
                    chunk_count: row.get::<_, u64>(2)?,
                    tier: row.get::<_, Option<String>>(3)?,
                })
            })
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("list_sources query: {e}"),
            })?
            .filter_map(|r| r.ok())
            .collect();

        Ok(sources)
    }

    // ── Eviction ──────────────────────────────────────────────────────────────

    /// Remove all data for this project's index (sources + cascaded chunks).
    ///
    /// Does NOT delete the database file itself — use `evict_project` on the
    /// [`IndexRegistry`] to fully remove a project.
    pub async fn evict(&self) -> Result<()> {
        if self.is_paused().await {
            return Err(CascadeError::RetrievalFailed {
                detail: "evict called while index is paused".into(),
            });
        }
        let conn = self.conn.lock().await;
        // Cascaded deletes handle rag_chunks (ON DELETE CASCADE in migration).
        conn.execute_batch("DELETE FROM rag_sources;")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("evict: {e}"),
            })?;
        info!(project = %self.project_root.display(), "IndexManager: index evicted");
        Ok(())
    }

    // ── Pause / resume ────────────────────────────────────────────────────────

    /// Pause ingest operations.  Idempotent.
    pub async fn pause(&self) {
        *self.paused.lock().await = true;
        debug!(project = %self.project_root.display(), "IndexManager: paused");
    }

    /// Resume ingest operations.  Idempotent.
    pub async fn resume(&self) {
        *self.paused.lock().await = false;
        debug!(project = %self.project_root.display(), "IndexManager: resumed");
    }

    /// Returns `true` if the manager is currently paused.
    pub async fn is_paused(&self) -> bool {
        *self.paused.lock().await
    }
}

// ── IndexRegistry ─────────────────────────────────────────────────────────────

/// Connection cache: maps project root → open `IndexManager`.
///
/// The daemon holds one `IndexRegistry`; it lazily opens (and caches) an
/// `IndexManager` for each project root it tracks.  Eviction removes the
/// project from the cache and deletes the DB file.
///
/// SPORT: MASTER-CRATES.md → cascade-rag::index_manager::IndexRegistry
pub struct IndexRegistry {
    managers: Mutex<HashMap<PathBuf, Arc<IndexManager>>>,
}

impl IndexRegistry {
    /// Create an empty registry.
    pub fn new() -> Arc<Self> {
        Arc::new(Self {
            managers: Mutex::new(HashMap::new()),
        })
    }

    /// Get or open the `IndexManager` for `project_root`.
    ///
    /// Opens a new connection on first call for a given root; subsequent calls
    /// return the cached instance.
    pub async fn get_or_open(&self, project_root: &Path) -> Result<Arc<IndexManager>> {
        let mut map = self.managers.lock().await;
        if let Some(mgr) = map.get(project_root) {
            return Ok(mgr.clone());
        }
        let mgr = IndexManager::open(project_root).await?;
        map.insert(project_root.to_path_buf(), mgr.clone());
        Ok(mgr)
    }

    /// Remove and return the `IndexManager` for `project_root`, if cached.
    ///
    /// The caller is responsible for draining any in-flight work before
    /// dropping the returned `Arc`.
    pub async fn evict_project(&self, project_root: &Path) -> Option<Arc<IndexManager>> {
        let mut map = self.managers.lock().await;
        map.remove(project_root)
    }

    /// Number of projects currently tracked.
    pub async fn len(&self) -> usize {
        self.managers.lock().await.len()
    }

    /// `true` when no projects are tracked.
    pub async fn is_empty(&self) -> bool {
        self.managers.lock().await.is_empty()
    }
}

impl Default for IndexRegistry {
    fn default() -> Self {
        Self {
            managers: Mutex::new(HashMap::new()),
        }
    }
}

// ── Path resolution ───────────────────────────────────────────────────────────

/// Resolve the SQLite DB path for `project_root`.
///
/// Priority:
/// 1. `CASCADE_INDEX_ROOT` env var → `$CASCADE_INDEX_ROOT/<project-hash>/cascade.db`
/// 2. Default → `<project_root>/.cascade/index/cascade.db`
pub fn resolve_db_path(project_root: &Path) -> PathBuf {
    if let Ok(root) = std::env::var("CASCADE_INDEX_ROOT") {
        let hash = project_path_hash(project_root);
        PathBuf::from(root).join(hash).join("cascade.db")
    } else {
        project_root
            .join(".cascade")
            .join("index")
            .join("cascade.db")
    }
}

/// Stable short hash of the project root path for use as a directory name.
///
/// Uses a simple FNV-1a 64-bit hash encoded as 16 lowercase hex digits.
fn project_path_hash(project_root: &Path) -> String {
    let path_str = project_root.to_string_lossy();
    let mut hash: u64 = 0xcbf29ce484222325; // FNV offset basis
    for byte in path_str.as_bytes() {
        hash ^= *byte as u64;
        hash = hash.wrapping_mul(0x100000001b3); // FNV prime
    }
    format!("{hash:016x}")
}

// ── Additive migration: tier column ──────────────────────────────────────────

/// Add the `tier` column to `rag_sources` if it does not yet exist.
///
/// This is an additive migration applied in-process rather than via the
/// numbered SQL migration files, keeping compatibility with DBs created by
/// earlier migrations.
fn ensure_tier_column(conn: &Connection) -> Result<()> {
    // SQLite silently ignores `ADD COLUMN` if the column already exists only
    // when using `IF NOT EXISTS`; fall back to checking PRAGMA table_info.
    let has_tier: bool = conn
        .prepare("PRAGMA table_info(rag_sources)")
        .and_then(|mut stmt| {
            let cols: Vec<String> = stmt
                .query_map([], |r| r.get::<_, String>(1))?
                .filter_map(|r| r.ok())
                .collect();
            Ok(cols.iter().any(|c| c == "tier"))
        })
        .unwrap_or(false);

    if !has_tier {
        conn.execute_batch("ALTER TABLE rag_sources ADD COLUMN tier TEXT;")
            .map_err(|e| CascadeError::RetrievalFailed {
                detail: format!("add tier column: {e}"),
            })?;
        info!("IndexManager: added 'tier' column to rag_sources");
    }
    Ok(())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use tempfile::TempDir;

    // Helper: create a temp project dir and open an IndexManager.
    async fn open_tmp() -> (TempDir, Arc<IndexManager>) {
        let dir = TempDir::new().expect("tempdir");
        let mgr = IndexManager::open(dir.path()).await.expect("open");
        (dir, mgr)
    }

    // ── DB path resolution ────────────────────────────────────────────────────

    /// Default path: <project_root>/.cascade/index/cascade.db
    #[test]
    #[serial(global_env)]
    fn test_resolve_default_path() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("CASCADE_INDEX_ROOT");
        let root = Path::new("/tmp/my-project");
        let db = resolve_db_path(root);
        assert_eq!(db, root.join(".cascade/index/cascade.db"));
    }

    /// CASCADE_INDEX_ROOT override: uses hashed subdir.
    #[test]
    #[serial(global_env)]
    fn test_resolve_env_override() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::set_var("CASCADE_INDEX_ROOT", "/tmp/rag-root");
        let root = Path::new("/tmp/my-project");
        let db = resolve_db_path(root);
        // Prefix is correct; hash subdir is deterministic.
        assert!(
            db.starts_with("/tmp/rag-root"),
            "should use CASCADE_INDEX_ROOT prefix"
        );
        assert!(db.ends_with("cascade.db"), "should end with cascade.db");
        std::env::remove_var("CASCADE_INDEX_ROOT");
    }

    /// Same project root → same hash → same db path.
    #[test]
    #[serial(global_env)]
    fn test_path_hash_deterministic() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::set_var("CASCADE_INDEX_ROOT", "/tmp/x");
        let p1 = resolve_db_path(Path::new("/home/user/proj"));
        let p2 = resolve_db_path(Path::new("/home/user/proj"));
        assert_eq!(p1, p2, "same root must produce same path");
        std::env::remove_var("CASCADE_INDEX_ROOT");
    }

    /// Different project roots → different hash dirs.
    #[test]
    #[serial(global_env)]
    fn test_path_hash_collision_resistance() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::set_var("CASCADE_INDEX_ROOT", "/tmp/x");
        let pa = resolve_db_path(Path::new("/home/user/proj-a"));
        let pb = resolve_db_path(Path::new("/home/user/proj-b"));
        assert_ne!(pa, pb, "distinct roots must produce distinct paths");
        std::env::remove_var("CASCADE_INDEX_ROOT");
    }

    // ── IndexManager::open creates DB ────────────────────────────────────────

    /// DB file is created at the expected default path.
    #[tokio::test]
    #[serial(global_env)]
    // Test-only: std Mutex serializes global env-var mutation across
    // `#[serial(global_env)]` tests, which never run concurrently, so this
    // cannot deadlock despite being held across `.await`.
    #[allow(clippy::await_holding_lock)]
    async fn test_open_creates_db() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("CASCADE_INDEX_ROOT");
        let dir = TempDir::new().unwrap();
        let mgr = IndexManager::open(dir.path()).await.unwrap();
        assert!(
            mgr.db_path().exists(),
            "DB file must exist after open: {:?}",
            mgr.db_path()
        );
        assert_eq!(mgr.db_path(), dir.path().join(".cascade/index/cascade.db"));
    }

    // ── Multi-project isolation ────────────────────────────────────────────────

    /// Two IndexManagers on different roots use separate DB files.
    #[tokio::test]
    #[serial(global_env)]
    // Test-only: std Mutex serializes global env-var mutation across
    // `#[serial(global_env)]` tests, which never run concurrently, so this
    // cannot deadlock despite being held across `.await`.
    #[allow(clippy::await_holding_lock)]
    async fn test_multi_project_isolation() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("CASCADE_INDEX_ROOT");
        let dir_a = TempDir::new().unwrap();
        let dir_b = TempDir::new().unwrap();

        let mgr_a = IndexManager::open(dir_a.path()).await.unwrap();
        let mgr_b = IndexManager::open(dir_b.path()).await.unwrap();

        assert_ne!(
            mgr_a.db_path(),
            mgr_b.db_path(),
            "distinct projects must have distinct DB paths"
        );

        // Register a source in project A.
        let file_a = dir_a.path().join("README.md");
        std::fs::write(&file_a, "# A").unwrap();
        mgr_a
            .register_source(&file_a, CascadeTier::Prc)
            .await
            .unwrap();

        // Project B has no sources.
        let sources_a = mgr_a.list_sources().await.unwrap();
        let sources_b = mgr_b.list_sources().await.unwrap();
        assert_eq!(sources_a.len(), 1, "project A should have 1 source");
        assert_eq!(sources_b.len(), 0, "project B should have 0 sources");
    }

    // ── Pause / resume ────────────────────────────────────────────────────────

    /// IndexManager starts unpaused.
    #[tokio::test]
    async fn test_starts_unpaused() {
        let (_dir, mgr) = open_tmp().await;
        assert!(!mgr.is_paused().await, "should start active");
    }

    /// pause() / resume() round-trip.
    #[tokio::test]
    async fn test_pause_resume() {
        let (_dir, mgr) = open_tmp().await;

        mgr.pause().await;
        assert!(mgr.is_paused().await, "should be paused");

        mgr.resume().await;
        assert!(!mgr.is_paused().await, "should be resumed");
    }

    /// pause() is idempotent.
    #[tokio::test]
    async fn test_pause_idempotent() {
        let (_dir, mgr) = open_tmp().await;
        mgr.pause().await;
        mgr.pause().await;
        assert!(mgr.is_paused().await);
    }

    /// resume() is idempotent.
    #[tokio::test]
    async fn test_resume_idempotent() {
        let (_dir, mgr) = open_tmp().await;
        mgr.resume().await;
        mgr.resume().await;
        assert!(!mgr.is_paused().await);
    }

    /// register_source returns false when paused, true when active.
    #[tokio::test]
    #[serial(global_env)]
    // Test-only: std Mutex serializes global env-var mutation across
    // `#[serial(global_env)]` tests, which never run concurrently, so this
    // cannot deadlock despite being held across `.await`.
    #[allow(clippy::await_holding_lock)]
    async fn test_register_paused_skips() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("CASCADE_INDEX_ROOT");
        let dir = TempDir::new().unwrap();
        let mgr = IndexManager::open(dir.path()).await.unwrap();
        let path = dir.path().join("file.md");
        std::fs::write(&path, "hello").unwrap();

        mgr.pause().await;
        let ok = mgr.register_source(&path, CascadeTier::Gci).await.unwrap();
        assert!(!ok, "register should return false while paused");

        let sources = mgr.list_sources().await.unwrap();
        assert!(
            sources.is_empty(),
            "no source should be stored while paused"
        );

        mgr.resume().await;
        let ok2 = mgr.register_source(&path, CascadeTier::Gci).await.unwrap();
        assert!(ok2, "register should return true after resume");
    }

    // ── Tier tagging ──────────────────────────────────────────────────────────

    /// Registered sources carry the correct tier tag.
    #[tokio::test]
    #[serial(global_env)]
    // Test-only: std Mutex serializes global env-var mutation across
    // `#[serial(global_env)]` tests, which never run concurrently, so this
    // cannot deadlock despite being held across `.await`.
    #[allow(clippy::await_holding_lock)]
    async fn test_tier_tagging() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("CASCADE_INDEX_ROOT");
        let dir = TempDir::new().unwrap();
        let mgr = IndexManager::open(dir.path()).await.unwrap();

        let tiers = [
            (CascadeTier::Gci, "gci"),
            (CascadeTier::Pci, "pci"),
            (CascadeTier::Apc, "apc"),
            (CascadeTier::Ppc, "ppc"),
            (CascadeTier::Prc, "prc"),
            (CascadeTier::Pac, "pac"),
        ];

        for (i, (tier, acronym)) in tiers.iter().enumerate() {
            let p = dir.path().join(format!("file{i}.md"));
            std::fs::write(&p, format!("content {i}")).unwrap();
            mgr.register_source(&p, *tier).await.unwrap();
            let sources = mgr.list_sources().await.unwrap();
            let src = sources.iter().find(|s| s.path == p).unwrap();
            assert_eq!(
                src.tier.as_deref(),
                Some(*acronym),
                "tier tag mismatch for {acronym}"
            );
        }
    }

    // ── list_sources empty ────────────────────────────────────────────────────

    /// Empty index returns empty Vec, not an error.
    #[tokio::test]
    async fn test_list_sources_empty() {
        let (_dir, mgr) = open_tmp().await;
        let sources = mgr.list_sources().await.unwrap();
        assert!(sources.is_empty(), "fresh index should have no sources");
    }

    // ── IndexRegistry ─────────────────────────────────────────────────────────

    /// get_or_open returns the same Arc on repeated calls.
    #[tokio::test]
    #[serial(global_env)]
    // Test-only: std Mutex serializes global env-var mutation across
    // `#[serial(global_env)]` tests, which never run concurrently, so this
    // cannot deadlock despite being held across `.await`.
    #[allow(clippy::await_holding_lock)]
    async fn test_registry_caches_manager() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("CASCADE_INDEX_ROOT");
        let registry = IndexRegistry::new();
        let dir = TempDir::new().unwrap();

        let m1 = registry.get_or_open(dir.path()).await.unwrap();
        let m2 = registry.get_or_open(dir.path()).await.unwrap();
        assert!(
            Arc::ptr_eq(&m1, &m2),
            "registry must return the same Arc for the same root"
        );
        assert_eq!(registry.len().await, 1);
    }

    /// evict_project removes the entry from the cache.
    #[tokio::test]
    #[serial(global_env)]
    // Test-only: std Mutex serializes global env-var mutation across
    // `#[serial(global_env)]` tests, which never run concurrently, so this
    // cannot deadlock despite being held across `.await`.
    #[allow(clippy::await_holding_lock)]
    async fn test_registry_evict() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        std::env::remove_var("CASCADE_INDEX_ROOT");
        let registry = IndexRegistry::new();
        let dir = TempDir::new().unwrap();

        registry.get_or_open(dir.path()).await.unwrap();
        assert_eq!(registry.len().await, 1);

        let evicted = registry.evict_project(dir.path()).await;
        assert!(evicted.is_some(), "evicted entry should be Some");
        assert!(
            registry.is_empty().await,
            "registry should be empty after eviction"
        );
    }
}
