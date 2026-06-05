//! WorktreeStore: central registry for git worktrees managed by Cascade.
//!
//! Models the Claude Squad and opencode-worktree pattern. Multiple harness sessions
//! (CC, OC, Codex, Cursor) create and destroy worktrees; Cascade maintains the
//! authoritative registry at `~/.cascade/worktrees.json` so all sessions can coordinate.
//!
//! Each [WorktreeEntry] records the repo root, branch, worktree path, owning harness,
//! and session ID. The `stale` flag marks entries whose paths no longer exist on disk.

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::PathBuf;

/// A single worktree entry in the store.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct WorktreeEntry {
    /// Unique UUID v4 identifier for this worktree entry.
    pub id: String,
    /// The root directory of the repository.
    pub repo_root: PathBuf,
    /// The branch name this worktree is tracking.
    pub branch: String,
    /// The path to the worktree directory.
    pub worktree_path: PathBuf,
    /// The harness that created this worktree ("cc" | "oc" | "codex" | "cursor").
    pub harness: String,
    /// Opaque session identifier from the harness.
    pub session_id: String,
    /// ISO-8601 timestamp of when this entry was created.
    pub created_at: String,
    /// If true, the worktree_path no longer exists on disk.
    pub stale: bool,
}

/// The complete WorktreeStore.
#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct WorktreeStore {
    /// Schema version for forward compatibility.
    pub schema_version: u32,
    /// ISO-8601 timestamp of the last update.
    pub updated_at: String,
    /// All registered worktrees.
    pub worktrees: Vec<WorktreeEntry>,
}

impl WorktreeStore {
    /// Create a new empty store.
    pub fn new() -> Self {
        Self {
            schema_version: 1,
            updated_at: chrono::Utc::now().to_rfc3339(),
            worktrees: Vec::new(),
        }
    }

    /// Add a new worktree entry to the store.
    pub fn add_worktree(&mut self, entry: WorktreeEntry) {
        self.updated_at = chrono::Utc::now().to_rfc3339();
        self.worktrees.push(entry);
    }

    /// Remove a worktree entry by ID.
    pub fn remove_worktree(&mut self, id: &str) {
        self.worktrees.retain(|e| e.id != id);
        self.updated_at = chrono::Utc::now().to_rfc3339();
    }

    /// Mark a worktree entry as stale by ID.
    pub fn mark_stale(&mut self, id: &str) {
        if let Some(entry) = self.worktrees.iter_mut().find(|e| e.id == id) {
            entry.stale = true;
        }
        self.updated_at = chrono::Utc::now().to_rfc3339();
    }

    /// Scan all entries and mark any with non-existent paths as stale.
    /// Returns a vector of IDs that were marked stale.
    pub fn scan_stale(&mut self) -> Vec<String> {
        let mut stale_ids = Vec::new();
        for entry in &mut self.worktrees {
            if !entry.stale && !entry.worktree_path.exists() {
                entry.stale = true;
                stale_ids.push(entry.id.clone());
            }
        }
        if !stale_ids.is_empty() {
            self.updated_at = chrono::Utc::now().to_rfc3339();
        }
        stale_ids
    }
}

/// Write the store to `~/.cascade/worktrees.json` atomically.
pub fn write_worktree_store(store: &WorktreeStore) -> std::io::Result<()> {
    let home = std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .map_err(|_| std::io::Error::new(std::io::ErrorKind::NotFound, "HOME not set"))?;

    let cascade_dir = PathBuf::from(home).join(".cascade");
    fs::create_dir_all(&cascade_dir)?;

    let store_path = cascade_dir.join("worktrees.json");
    let temp_path = cascade_dir.join("worktrees.json.tmp");

    // Write to temporary file first.
    let json = serde_json::to_string_pretty(store)
        .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;
    fs::write(&temp_path, json)?;

    // Atomically move into place.
    fs::rename(&temp_path, &store_path)?;

    Ok(())
}

/// Read the store from `~/.cascade/worktrees.json`.
pub fn read_worktree_store() -> std::io::Result<WorktreeStore> {
    let home = std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .map_err(|_| std::io::Error::new(std::io::ErrorKind::NotFound, "HOME not set"))?;

    let store_path = PathBuf::from(home).join(".cascade").join("worktrees.json");

    if !store_path.exists() {
        return Ok(WorktreeStore::new());
    }

    let json = fs::read_to_string(&store_path)?;
    let store = serde_json::from_str(&json)
        .map_err(|e| std::io::Error::new(std::io::ErrorKind::InvalidData, e))?;

    Ok(store)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Mutex, MutexGuard, OnceLock};
    use tempfile::TempDir;

    /// Serializes tests that mutate `HOME` via `std::env::set_var`.
    /// WHY: `set_var` is not thread-safe; concurrent tests racing on HOME cause
    /// spurious failures. All tests calling `set_var("HOME", …)` MUST hold this.
    fn home_lock() -> MutexGuard<'static, ()> {
        static LOCK: OnceLock<Mutex<()>> = OnceLock::new();
        LOCK.get_or_init(|| Mutex::new(()))
            .lock()
            .unwrap_or_else(|e| e.into_inner())
    }

    #[test]
    fn test_add_remove() {
        let mut store = WorktreeStore::new();

        // Create two entries
        let entry1 = WorktreeEntry {
            id: "id1".to_string(),
            repo_root: PathBuf::from("/repo1"),
            branch: "main".to_string(),
            worktree_path: PathBuf::from("/repo1/.git/worktrees/wt1"),
            harness: "cc".to_string(),
            session_id: "sess1".to_string(),
            created_at: "2026-01-01T00:00:00Z".to_string(),
            stale: false,
        };

        let entry2 = WorktreeEntry {
            id: "id2".to_string(),
            repo_root: PathBuf::from("/repo2"),
            branch: "dev".to_string(),
            worktree_path: PathBuf::from("/repo2/.git/worktrees/wt2"),
            harness: "oc".to_string(),
            session_id: "sess2".to_string(),
            created_at: "2026-01-01T00:00:01Z".to_string(),
            stale: false,
        };

        // Add both
        store.add_worktree(entry1);
        store.add_worktree(entry2);
        assert_eq!(store.worktrees.len(), 2);

        // Remove one
        store.remove_worktree("id1");
        assert_eq!(store.worktrees.len(), 1);
        assert_eq!(store.worktrees[0].id, "id2");
    }

    #[test]
    fn test_stale_scan() {
        let mut store = WorktreeStore::new();

        // Create a tempdir and an entry pointing to it
        let tmpdir = TempDir::new().expect("create tempdir");
        let tmppath = tmpdir.path().to_path_buf();

        let entry = WorktreeEntry {
            id: "stale-test".to_string(),
            repo_root: PathBuf::from("/repo"),
            branch: "main".to_string(),
            worktree_path: tmppath.clone(),
            harness: "cc".to_string(),
            session_id: "sess".to_string(),
            created_at: "2026-01-01T00:00:00Z".to_string(),
            stale: false,
        };

        store.add_worktree(entry);

        // Path exists, should not be marked stale
        let stale_ids = store.scan_stale();
        assert!(stale_ids.is_empty());
        assert!(!store.worktrees[0].stale);

        // Drop the tempdir (deletes the path)
        drop(tmpdir);

        // Now path does not exist, should be marked stale
        let stale_ids = store.scan_stale();
        assert_eq!(stale_ids.len(), 1);
        assert!(store.worktrees[0].stale);
    }

    #[test]
    fn test_roundtrip() {
        let tmpdir = TempDir::new().expect("create tempdir");
        let home = tmpdir.path().to_string_lossy().to_string();

        // Temporarily override HOME for this test
        let original_home = std::env::var("HOME").ok();
        let _guard = home_lock();
        std::env::set_var("HOME", &home);

        // Create and write a store
        let mut store = WorktreeStore::new();
        let entry = WorktreeEntry {
            id: "roundtrip-test".to_string(),
            repo_root: PathBuf::from("/repo1"),
            branch: "main".to_string(),
            worktree_path: PathBuf::from("/repo1/.git/worktrees/wt1"),
            harness: "cc".to_string(),
            session_id: "sess1".to_string(),
            created_at: "2026-01-01T00:00:00Z".to_string(),
            stale: false,
        };
        store.add_worktree(entry);

        write_worktree_store(&store).expect("write store");

        // Read it back
        let loaded = read_worktree_store().expect("read store");

        // Verify fields are preserved (note: updated_at will differ slightly)
        assert_eq!(loaded.schema_version, 1);
        assert_eq!(loaded.worktrees.len(), 1);

        let loaded_entry = &loaded.worktrees[0];
        assert_eq!(loaded_entry.id, "roundtrip-test");
        assert_eq!(loaded_entry.repo_root, PathBuf::from("/repo1"));
        assert_eq!(loaded_entry.branch, "main");
        assert_eq!(
            loaded_entry.worktree_path,
            PathBuf::from("/repo1/.git/worktrees/wt1")
        );
        assert_eq!(loaded_entry.harness, "cc");
        assert_eq!(loaded_entry.session_id, "sess1");
        assert_eq!(loaded_entry.created_at, "2026-01-01T00:00:00Z");
        assert!(!loaded_entry.stale);

        // Restore original HOME. We still hold `_guard` (home_lock) acquired at
        // the top of the test, so we MUST NOT call home_lock() again here:
        // std::sync::Mutex is not reentrant and re-locking on the same thread
        // self-deadlocks. Just restore the env var under the lock we already hold.
        match original_home {
            Some(h) => std::env::set_var("HOME", h),
            None => std::env::remove_var("HOME"),
        }
    }
}
